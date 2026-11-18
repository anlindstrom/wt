package wtsync

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// ContextGap is one hunk of a fixup!, squash! or amend! commit that does not
// apply where autosquash puts it, right behind its target: the hunk's
// context only a later commit of the line adds. A fixup written at the top
// of a line and folded into a commit low on it carries such hunks.
type ContextGap struct {
	Fixup         string `json:"fixup"`
	FixupSubject  string `json:"fixupSubject"`
	Target        string `json:"target"`
	TargetSubject string `json:"targetSubject"`
	// Path and Lines are the hunk: its file, and the lines it changes in
	// the fixup, "13" or "13-15".
	Path  string `json:"path"`
	Lines string `json:"lines"`
	// Needs is the first commit after the target, in line order, whose tree
	// has the hunk's context.
	Needs        string `json:"needs"`
	NeedsSubject string `json:"needsSubject"`
}

// Line is what a person is told about the gap.
func (g ContextGap) Line() string {
	return fmt.Sprintf("fixup moved ahead of its context: %s:%s needs %s %q, which comes later on the line",
		g.Path, g.Lines, git.ShortID(g.Needs, 7), oneLinePlan(g.NeedsSubject))
}

// Retarget is the way out of a stop at the gap: not a resolution.
func (g ContextGap) Retarget(work string) string {
	return fmt.Sprintf("not a resolution: wt sync undo %s, then move that hunk of %s into a fixup of %s (git commit --fixup=%s) and run again",
		work, git.ShortID(g.Fixup, 7), git.ShortID(g.Needs, 7), git.ShortID(g.Needs, 7))
}

// GapsAt is the gaps of the fold commit c, matched by id or by a prefix of
// one as the sequencer may abbreviate it.
func GapsAt(gaps []ContextGap, c string) []ContextGap {
	var out []ContextGap
	for _, g := range gaps {
		if c != "" && (g.Fixup == c || strings.HasPrefix(g.Fixup, c)) {
			out = append(out, g)
		}
	}
	return out
}

// ContextGaps is every hunk of the line's fixups that lacks its context at
// its target. commits is the line, oldest first. Each fixup is merged, in
// the object store, onto its target's tree with the fixups of the same
// target before it already folded, from its own parent, as the rebase will
// merge it; a file that does not merge clean is looked at hunk by hunk, and
// a hunk whose lines before the change are not in the target's file but are
// in a later commit's names the first such commit. A conflict no later
// commit explains is not a gap. The trees are the line's own, before any
// move onto another base. Nothing but objects in the store is written: no
// ref, no index and no worktree.
func ContextGaps(dir string, commits []string) ([]ContextGap, []string, error) {
	if len(commits) == 0 {
		return nil, nil, nil
	}
	out, err := gitEnv(dir, nil, nil, append([]string{"log", "--no-walk=unsorted", "--format=%H%x00%s"}, commits...)...)
	if err != nil {
		return nil, nil, err
	}
	var all []lineCommit
	for _, l := range strings.Split(out, "\n") {
		if sha, subject, ok := strings.Cut(l, "\x00"); ok {
			all = append(all, lineCommit{sha, subject})
		}
	}
	index := map[string]int{}
	for i, e := range all {
		index[e.sha] = i
	}
	blobs := blobCache{dir: dir, got: map[string]*string{}}
	folded := map[int]string{} // a target's tree with its fixups so far
	// conflicted is, per target, the first fixup of it that conflicted
	// there: what a fixup after it lacks may be that one's conflicting part,
	// which the analysis cannot fold in.
	conflicted := map[int]int{}
	isFoldAt := map[int]bool{}
	for i, e := range all {
		isFoldAt[i] = isFold(e.subject)
	}
	var gaps []ContextGap
	var unchecked []string
	for i, e := range all {
		j, ok := foldTarget(dir, all[:i], e.subject, index)
		if !ok {
			continue
		}
		cur, ok := folded[j]
		if !ok {
			if cur, err = gitEnv(dir, nil, nil, "rev-parse", all[j].sha+"^{tree}"); err != nil {
				return nil, nil, err
			}
		}
		parent, err := gitEnv(dir, nil, nil, "rev-parse", e.sha+"^^{tree}")
		if err != nil {
			return nil, nil, err
		}
		tree, clean, conflicts, _, err := mergeTree(dir, parent, cur, e.sha)
		if err != nil {
			return nil, nil, err
		}
		if clean {
			folded[j] = tree
			continue
		}
		// The fixups after this one are checked against its clean part:
		// the merge with each conflicted file as the target had it.
		earlier, tainted := conflicted[j]
		if !tainted {
			conflicted[j] = i
		}
		if kept, err := cleanPart(dir, tree, cur, conflicts); err == nil {
			folded[j] = kept
		} else {
			folded[j] = cur
		}
		uncertain := false
		var mine []ContextGap
		for _, c := range conflicts {
			hunks, err := fixupHunks(dir, e.sha, c.Path)
			if err != nil {
				return nil, nil, err
			}
			for _, h := range hunks {
				if len(h.before) == 0 {
					continue
				}
				if has, err := blobs.has(cur, c.Path, h.before); err != nil {
					return nil, nil, err
				} else if has {
					continue
				}
				for k := j + 1; k < i; k++ {
					later := all[k]
					has, err := blobs.has(later.sha+"^{tree}", c.Path, h.before)
					if err != nil {
						return nil, nil, err
					}
					if !has {
						continue
					}
					// Found at or after an earlier fixup of the same target
					// that conflicted, or in a fixup: the context may be a
					// fixup's own, which comes before, not later.
					if (tainted && k >= earlier) || isFoldAt[k] {
						uncertain = true
						break
					}
					mine = append(mine, ContextGap{Fixup: e.sha, FixupSubject: e.subject, Target: all[j].sha, TargetSubject: all[j].subject,
						Path: c.Path, Lines: h.lines(), Needs: later.sha, NeedsSubject: later.subject})
					break
				}
			}
		}
		if uncertain {
			why := "another fixup's"
			if tainted {
				why = fmt.Sprintf("the earlier fixup %s's, which conflicts there too", git.ShortID(all[earlier].sha, 7))
			}
			unchecked = append(unchecked, fmt.Sprintf("%s %q: not analysed: what it lacks behind %s may be %s",
				git.ShortID(e.sha, 7), oneLinePlan(e.subject), git.ShortID(all[j].sha, 7), why))
			continue
		}
		gaps = append(gaps, mine...)
	}
	return gaps, unchecked, nil
}

// cleanPart is merged, a merge-tree result, with each conflicted file as
// cur has it: what of a fixup that conflicts folds in clean. It writes only
// objects, through a private index.
func cleanPart(dir, merged, cur string, conflicts []Conflict) (string, error) {
	idx, cleanup, err := scratchIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitEnv(dir, env, nil, "read-tree", merged); err != nil {
		return "", err
	}
	var info strings.Builder
	zero := strings.Repeat("0", len(cur))
	for _, c := range conflicts {
		entry, err := gitEnv(dir, nil, nil, "--literal-pathspecs", "ls-tree", "-z", "--full-tree", cur, "--", c.Path)
		if err != nil {
			return "", err
		}
		// Every stage the merged index holds for the path goes, then the
		// target's own entry, if it has one, comes back.
		fmt.Fprintf(&info, "0 %s\t%s\x00", zero, c.Path)
		if meta, path, ok := strings.Cut(strings.TrimSuffix(entry, "\x00"), "\t"); ok && path == c.Path {
			f := strings.Fields(meta) // mode type sha
			fmt.Fprintf(&info, "%s %s\t%s\x00", f[0], f[2], c.Path)
		}
	}
	if _, err := gitEnv(dir, env, strings.NewReader(info.String()), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	return gitEnv(dir, env, nil, "write-tree")
}

// lineCommit is a commit of the line and its subject.
type lineCommit struct{ sha, subject string }

// foldTarget is the commit of earlier a fold subject names, found as git's
// autosquash finds it: after the prefixes and their whitespace, a commit
// named by revision, else one with that subject, else one whose subject
// begins with it. ok is false for a commit that is no fold, and for a name
// that finds no commit or more than one: the run refuses those itself.
func foldTarget(dir string, earlier []lineCommit, subject string, index map[string]int) (int, bool) {
	target, folding := subject, false
	for {
		rest, ok := cutFoldPrefix(target)
		if !ok {
			break
		}
		target, folding = strings.TrimSpace(rest), true
	}
	if !folding || target == "" {
		return 0, false
	}
	if sha, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", target+"^{commit}"); err == nil {
		if j, ok := index[strings.TrimSpace(sha)]; ok && j < len(earlier) {
			return j, true
		}
	}
	var exact, prefix []int
	for j, c := range earlier {
		switch {
		case c.subject == target:
			exact = append(exact, j)
		case strings.HasPrefix(c.subject, target):
			prefix = append(prefix, j)
		}
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = prefix
	}
	if len(candidates) != 1 {
		return 0, false
	}
	return candidates[0], true
}

// hunk is one hunk of a fixup's diff: the lines its change needs before it
// (context and removed lines, in order), and the lines of the fixup's own
// file it adds, or where it removes, for a person to find it.
type hunk struct {
	before     []string
	first, end int
}

func (h hunk) lines() string {
	if h.end <= h.first {
		return strconv.Itoa(h.first)
	}
	return strconv.Itoa(h.first) + "-" + strconv.Itoa(h.end)
}

// fixupHunks is the hunks of commit c's change to path against its parent.
func fixupHunks(dir, c, path string) ([]hunk, error) {
	out, err := gitEnv(dir, nil, nil, "--literal-pathspecs", "diff-tree", "-p", "--no-color", "--no-ext-diff", "--no-renames", "-U3",
		"--src-prefix=a/", "--dst-prefix=b/", c+"^", c, "--", path)
	if err != nil {
		return nil, err
	}
	return parseHunks(out), nil
}

// parseHunks reads the hunks of a one-file unified diff.
func parseHunks(diff string) []hunk {
	var hunks []hunk
	var h *hunk
	newLine := 0
	flush := func() {
		if h != nil {
			hunks = append(hunks, *h)
		}
		h = nil
	}
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "@@ ") {
			flush()
			newLine = hunkNewStart(l)
			h = &hunk{}
			continue
		}
		if h == nil || l == "" {
			continue
		}
		switch l[0] {
		case ' ':
			h.before = append(h.before, l[1:])
			newLine++
		case '-':
			h.before = append(h.before, l[1:])
			if h.first == 0 {
				h.first, h.end = newLine, newLine
			}
		case '+':
			if h.first == 0 {
				h.first = newLine
			}
			h.end = newLine
			newLine++
		}
	}
	flush()
	for i := range hunks {
		if hunks[i].first == 0 {
			hunks[i].first = 1
		}
	}
	return hunks
}

// hunkNewStart is the first line of the new side a hunk header names.
func hunkNewStart(header string) int {
	_, rest, _ := strings.Cut(header, "+")
	n, _, _ := strings.Cut(rest, ",")
	n, _, _ = strings.Cut(n, " ")
	v, err := strconv.Atoi(n)
	if err != nil {
		return 0
	}
	return v
}

// blobCache reads files of trees once each.
type blobCache struct {
	dir string
	got map[string]*string
}

// file is path in tree, nil when the tree has no such file.
func (b blobCache) file(tree, path string) (*string, error) {
	key := tree + ":" + path
	if s, ok := b.got[key]; ok {
		return s, nil
	}
	out, code, err := gitEnvAllow(b.dir, nil, nil, 128, "cat-file", "-p", key)
	if err != nil {
		return nil, err
	}
	var s *string
	if code == 0 {
		s = &out
	}
	b.got[key] = s
	return s, nil
}

// has is whether path in tree holds lines, one after the other.
func (b blobCache) has(tree, path string, lines []string) (bool, error) {
	f, err := b.file(tree, path)
	if err != nil || f == nil {
		return false, err
	}
	return containsRun(strings.Split(*f, "\n"), lines), nil
}

// containsRun is whether lines appear in file, in order and next to each
// other.
func containsRun(file, lines []string) bool { return indexRun(file, lines) >= 0 }

// HunkDrop is lines of a fixup's hunk that a resolution left out.
type HunkDrop struct {
	Path   string
	Lines  string
	N      int
	Binary bool
}

// ResolutionDrops is what the resolution of a stop at fixup leaves out.
func ResolutionDrops(wtPath, fixup, oldTip string) ([]HunkDrop, []string, error) {
	return nil, nil, nil
}

// DropsLine is what a person is told about drops of fixup.
func DropsLine(drops []HunkDrop, fixup, subject string) string { return "" }

// indexRun is where lines start in file, -1 when they are not in it.
func indexRun(file, lines []string) int {
	for i := 0; i+len(lines) <= len(file); i++ {
		match := true
		for k, l := range lines {
			if file[i+k] != l {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
