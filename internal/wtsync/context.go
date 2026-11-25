package wtsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

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
	// cleanFold is the fixups that merge clean onto their target.
	cleanFold := map[int]bool{}
	// targetOf is the commit each fixup seen so far folds into.
	targetOf := map[int]int{}
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
		targetOf[i] = j
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
			cleanFold[i] = true
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
		// Everything that lacks its context behind the target and is found
		// later is at risk: the files, the hunks and the commits that add
		// it. why is set when the analysis cannot place a hunk.
		why := ""
		atRiskFiles, atRiskLater := map[string]bool{}, map[int]bool{}
		atRiskHunks := 0
		var mine []ContextGap
		// replayed is the trees the commits after the target come to when
		// the fixups before this one are folded: built once, on first use.
		var replayed map[int]string
		replayDone := false
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
				// foldedHas is whether this hunk's context is in commit t's
				// tree with its fixups folded in so far: what that commit
				// comes to in the rebase, not the tree of the fixup that
				// holds the context in the line. A tree that cannot be read
				// has nothing.
				foldedHas := func(t int) bool {
					tree, ok := folded[t]
					if !ok {
						tree = all[t].sha + "^{tree}"
					}
					has, err := blobs.has(tree, c.Path, h.before)
					return err == nil && has
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
					atRiskFiles[c.Path] = true
					atRiskHunks++
					needs := k
					switch {
					case tainted && k >= earlier:
						// At or after an earlier fixup of the same target
						// that conflicted: the context may be its own,
						// which comes before, not later.
						if why == "" {
							why = fmt.Sprintf("the earlier fixup %s conflicts there too", git.ShortID(all[earlier].sha, 7))
						}
					case isFoldAt[k]:
						// A fixup of a later commit folds into that commit,
						// so what it adds is that commit's. Any other
						// fixup's share of it cannot be told.
						t, ok := foldTarget(dir, all[:k], later.subject, index)
						laterFold := ok && t > j && cleanFold[k]
						if laterFold && foldedHas(t) {
							needs = t
							break
						}
						if laterFold {
							// The context may be what that fold and the
							// commits up to a later one come to together.
							if !replayDone {
								replayDone = true
								if replayed, err = replayAfter(dir, all, isFoldAt, targetOf, cleanFold, cur, j, i); err != nil {
									return nil, nil, err
								}
							}
							if m, found, err := firstReplayedWith(blobs, replayed, j, i, c.Path, h.before); err != nil {
								return nil, nil, err
							} else if found {
								needs = m
								break
							}
						}
						if why == "" {
							why = fmt.Sprintf("the context is in the fixup %s, whose own fold wt cannot predict", git.ShortID(later.sha, 7))
						}
					}
					atRiskLater[needs] = true
					mine = append(mine, ContextGap{Fixup: e.sha, FixupSubject: e.subject, Target: all[j].sha, TargetSubject: all[j].subject,
						Path: c.Path, Lines: h.lines(), Needs: all[needs].sha, NeedsSubject: all[needs].subject})
					break
				}
			}
		}
		if why != "" {
			unchecked = append(unchecked, fmt.Sprintf("%s %q: not analysed: %s (%s) lack context behind %s that %s %s; %s",
				git.ShortID(e.sha, 7), oneLinePlan(e.subject), plural(len(atRiskFiles), "file"), plural(atRiskHunks, "hunk"),
				git.ShortID(all[j].sha, 7), plural(len(atRiskLater), "later commit"), addVerb(len(atRiskLater)), why))
			continue
		}
		gaps = append(gaps, mine...)
	}
	return gaps, unchecked, nil
}

// replayAfter is the tree each ordinary commit between target j and fixup i
// comes to in the fold, from start, the target's tree: each commit applied
// in line order, and the clean fixups of it seen before i folded in right
// behind it, as autosquash orders them. nil when the replay cannot be
// established: a commit or a fixup of one that does not merge clean, a
// fixup of one that conflicted, or a fixup of a commit below j seen before i,
// whose fold changes the trees from j on in a way the replay does not apply.
func replayAfter(dir string, all []lineCommit, isFoldAt map[int]bool, targetOf map[int]int, cleanFold map[int]bool, start string, j, i int) (map[int]string, error) {
	for f := 0; f < i; f++ {
		if t, ok := targetOf[f]; ok && t < j {
			return nil, nil
		}
	}
	trees := map[int]string{}
	cur := start
	apply := func(c int) (bool, error) {
		parent, err := gitEnv(dir, nil, nil, "rev-parse", all[c].sha+"^^{tree}")
		if err != nil {
			return false, err
		}
		tree, clean, _, _, err := mergeTree(dir, parent, cur, all[c].sha)
		if err != nil || !clean {
			return false, err
		}
		cur = tree
		return true, nil
	}
	for m := j + 1; m < i; m++ {
		if isFoldAt[m] {
			continue
		}
		if ok, err := apply(m); err != nil || !ok {
			return nil, err
		}
		for f := m + 1; f < i; f++ {
			if t, ok := targetOf[f]; !ok || t != m {
				continue
			}
			if !cleanFold[f] {
				return nil, nil
			}
			if ok, err := apply(f); err != nil || !ok {
				return nil, err
			}
		}
		trees[m] = cur
	}
	return trees, nil
}

// firstReplayedWith is the first commit after j, before i, whose replayed
// tree has the hunk's context in path.
func firstReplayedWith(blobs blobCache, trees map[int]string, j, i int, path string, before []string) (int, bool, error) {
	for m := j + 1; m < i; m++ {
		tree, ok := trees[m]
		if !ok {
			continue
		}
		has, err := blobs.has(tree, path, before)
		if err != nil {
			return 0, false, err
		}
		if has {
			return m, true, nil
		}
	}
	return 0, false, nil
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

// HunkDrop is lines of a fixup's hunk that a resolution left out: the old
// tip has them, and no commit still to be replayed brings them.
type HunkDrop struct {
	Path  string
	Lines string // where the old tip has them, "13" or "13-15"
	N     int
	// Binary is a binary file whose change the resolution left out.
	Binary bool
}

// ResolutionDrops is what the resolution of a stop at fixup leaves out of
// it, read-only, and what of it could not be checked. Line by line, with
// their counts: each line the fixup adds to a file that the old tip still
// has there (following renames after the fixup) must be in the resolution,
// or added by a commit the sequencer still has to replay; a line short is
// dropped. A line with no letter or digit in it (a blank, a lone brace) is
// too common to prove anything and is not checked; when such lines fall
// short they are named as not checked. A binary file is compared by blob:
// the old tip has the fixup's blob, the index does not, and nothing to come
// writes it, is a drop. A file the old tip no longer has under a name that
// can be followed, a binary file changed again after the fixup, and a file
// still unmerged, are named as not checked, never passed. Only objects and
// the index are read; no ref, index entry or file moves.
func ResolutionDrops(wtPath, fixup, oldTip string) (drops []HunkDrop, unchecked []string, err error) {
	later, err := todoCommits(wtPath)
	if err != nil {
		return nil, nil, err
	}
	binary, err := binaryPaths(wtPath, fixup)
	if err != nil {
		return nil, nil, err
	}
	paths, err := pathsOfCommit(wtPath, fixup)
	if err != nil {
		return nil, nil, err
	}
	renamed, gone, err := followTo(wtPath, fixup, oldTip)
	if err != nil {
		return nil, nil, err
	}
	staged, err := stagedNames(wtPath)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range paths {
		d, why, err := pathDrop(wtPath, fixup, oldTip, p, later, binary[p], renamed, gone, staged)
		if err != nil {
			// A file that cannot be read is said, never a block.
			why = p + ": could not be checked: " + kitOneLine(err.Error())
		}
		if why != "" {
			unchecked = append(unchecked, why)
		}
		if d != nil {
			drops = append(drops, *d)
		}
	}
	return drops, unchecked, nil
}

// pathDrop is what the resolution drops of fixup's change to p, or why it
// cannot tell.
func pathDrop(wtPath, fixup, oldTip, p string, later []string, binary bool, renamed map[string]string, gone map[string]bool, staged func(string) string) (*HunkDrop, string, error) {
	if unmerged, err := gitEnv(wtPath, nil, nil, "--literal-pathspecs", "ls-files", "--unmerged", "--", p); err != nil {
		return nil, "", err
	} else if unmerged != "" {
		return nil, p + ": still unmerged, so what the resolution keeps of it is not checked", nil
	}
	at := p
	if q, ok := renamed[p]; ok {
		at = q
	}
	if gone[p] {
		return nil, p + ": the old tip no longer has it under a name that can be followed, so what the resolution keeps of it is not checked", nil
	}
	names, err := namesAhead(wtPath, later, p)
	if err != nil {
		return nil, "", err
	}
	if binary {
		return binaryDrop(wtPath, fixup, oldTip, p, at, later, names, staged(p))
	}
	return textDrop(wtPath, fixup, oldTip, p, at, later, names, staged(p))
}

// stagedNames is where the index holds a path of the line: the path itself,
// or the name git carried it to when the rebase is onto a branch that
// renamed it, read by comparing the stop's target with HEAD, its rebased
// self; "" when it is in the index under neither.
func stagedNames(wtPath string) (func(string) string, error) {
	listed, err := gitEnv(wtPath, nil, nil, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, rec := range strings.Split(listed, "\x00") {
		if _, path, ok := strings.Cut(rec, "\t"); ok {
			in[path] = true
		}
	}
	moved := map[string]string{}
	if target, err := stopTarget(wtPath); err == nil && target != "" {
		if out, err := gitEnv(wtPath, nil, nil, "diff-tree", "-r", "-z", "-M", "--name-status", target, "HEAD"); err == nil {
			f := strings.Split(out, "\x00")
			for i := 0; i+2 < len(f); i++ {
				if strings.HasPrefix(f[i], "R") {
					moved[f[i+1]] = f[i+2]
					i += 2
				}
			}
		}
	}
	return func(p string) string {
		switch {
		case in[p]:
			return p
		case in[moved[p]]:
			return moved[p]
		}
		return ""
	}, nil
}

// stopTarget is the commit the stopped fixup folds into, as the
// sequencer's done list names it: the last pick before the fixups.
func stopTarget(wtPath string) (string, error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return "", err
	}
	done, err := os.ReadFile(filepath.Join(dir, "done"))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(done)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		f := strings.Fields(lines[i])
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "pick", "p", "edit", "e", "reword", "r":
			return gitEnv(wtPath, nil, nil, "rev-parse", "--verify", "--quiet", f[1]+"^{commit}")
		}
	}
	return "", nil
}

// textDrop is what the resolution drops of the lines fixup adds to p, at
// in the old tip, rp in the index; why names what it cannot tell. Each
// line counts with what the commits to come do to it: what the
// resolution has of it, plus what they add, less what they remove, must
// come to what the old tip has.
func textDrop(wtPath, fixup, oldTip, p, at string, later []string, names map[string][]string, rp string) (*HunkDrop, string, error) {
	runs, err := fixupHunkAdds(wtPath, fixup, p)
	if err != nil {
		return nil, "", err
	}
	added := map[string]bool{}
	for _, r := range runs {
		for _, l := range r {
			added[l] = true
		}
	}
	if len(added) == 0 {
		return nil, "", nil
	}
	if rp == "" {
		return nil, p + ": not in the resolution under a name that can be followed, so what it keeps of it is not checked", nil
	}
	blobs := blobCache{dir: wtPath, got: map[string]*string{}}
	old, err := blobs.file(oldTip+"^{tree}", at)
	if err != nil || old == nil {
		return nil, "", err
	}
	oldLines := strings.Split(*old, "\n")
	oldCount := countLines(oldLines)
	resolved, err := gitEnv(wtPath, nil, nil, "cat-file", "-p", ":0:"+rp)
	if err != nil {
		return nil, "", err
	}
	expected := countLines(strings.Split(resolved, "\n"))
	for _, c := range later {
		ns := names[c]
		if len(ns) == 0 {
			continue
		}
		adds, removes, err := commitChanges(wtPath, c, ns...)
		if err != nil {
			return nil, "", err
		}
		for _, l := range adds {
			expected[l]++
		}
		for _, l := range removes {
			expected[l]--
		}
	}
	short, common := map[string]int{}, 0
	for l := range added {
		missing := oldCount[l] - expected[l]
		if missing <= 0 {
			continue
		}
		if !strings.ContainsFunc(l, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			common += missing
			continue
		}
		short[l] = missing
	}
	why := ""
	if common > 0 {
		why = fmt.Sprintf("%s: %s with no letter or digit (blank lines, braces) may be dropped; too common to tell, not checked", at, plural(common, "line"))
	}
	if len(short) == 0 {
		return nil, why, nil
	}
	var at1 []int
	n := 0
	for i := len(oldLines) - 1; i >= 0; i-- {
		// The last ones: a copy the target had already comes first.
		if l := oldLines[i]; short[l] > 0 {
			at1 = append([]int{i + 1}, at1...)
			short[l]--
			n++
		}
	}
	return &HunkDrop{Path: at, Lines: lineRanges(at1), N: n}, why, nil
}

// binaryDrop is whether the resolution drops fixup's change to the binary
// file p, at in the old tip, rp in the index. A commit to come brings it
// back only when its own change writes that blob.
func binaryDrop(wtPath, fixup, oldTip, p, at string, later []string, names map[string][]string, rp string) (*HunkDrop, string, error) {
	blob := func(rev string) (string, error) {
		// An absent path is exit 1 under --quiet: absence, not an error.
		out, code, err := gitEnvAllow(wtPath, nil, nil, 1, "rev-parse", "--verify", "--quiet", rev)
		if err != nil || code != 0 {
			return "", err
		}
		return out, nil
	}
	mine, err := blob(fixup + ":" + p)
	if err != nil {
		return nil, "", err
	}
	idx := ""
	if rp != "" {
		if idx, err = blob(":0:" + rp); err != nil {
			return nil, "", err
		}
	}
	if mine == "" {
		// The fixup deletes it: kept when the resolution has it no more.
		if idx == "" {
			return nil, "", nil
		}
		return nil, p + ": a binary file the fixup deletes and the resolution keeps: not checked", nil
	}
	old, err := blob(oldTip + ":" + at)
	if err != nil {
		return nil, "", err
	}
	if old != mine {
		return nil, at + ": a binary file the line changes again after the fixup, or removes: not checked", nil
	}
	if idx == mine {
		return nil, "", nil
	}
	for _, c := range later {
		if len(names[c]) == 0 {
			continue
		}
		out, err := gitEnv(wtPath, nil, nil, append([]string{"--literal-pathspecs", "diff-tree", "-r", "-z", "--no-renames", c + "^", c, "--"}, names[c]...)...)
		if err != nil {
			return nil, "", err
		}
		for _, rec := range strings.Split(out, "\x00") {
			// ":<mode> <mode> <old> <new> <status>"
			if f := strings.Fields(strings.TrimPrefix(rec, ":")); len(f) >= 4 && f[3] == mine {
				return nil, "", nil
			}
		}
	}
	return &HunkDrop{Path: at, Binary: true}, "", nil
}

// binaryPaths is the files commit c changes that git diffs as binary.
func binaryPaths(dir, c string) (map[string]bool, error) {
	out, err := gitEnv(dir, nil, nil, "diff-tree", "--numstat", "-z", "--no-renames", "-r", c+"^", c)
	if err != nil {
		return nil, err
	}
	bin := map[string]bool{}
	for _, rec := range strings.Split(out, "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) == 3 && f[0] == "-" && f[1] == "-" {
			bin[f[2]] = true
		}
	}
	return bin, nil
}

// followTo is where each path fixup has is in the old tip: renamed is the
// files git sees renamed or copied on the way, gone those the old tip has
// no more under any name git can follow.
func followTo(dir, fixup, oldTip string) (renamed map[string]string, gone map[string]bool, err error) {
	renamed, gone = map[string]string{}, map[string]bool{}
	out, err := gitEnv(dir, nil, nil, "diff-tree", "-r", "-z", "-M", "--name-status", fixup, oldTip)
	if err != nil {
		return nil, nil, err
	}
	f := strings.Split(out, "\x00")
	for i := 0; i < len(f); i++ {
		status := f[i]
		switch {
		case status == "":
		case (status[0] == 'R' || status[0] == 'C') && i+2 < len(f):
			if status[0] == 'R' {
				renamed[f[i+1]] = f[i+2]
			}
			i += 2
		case status[0] == 'D' && i+1 < len(f):
			gone[f[i+1]] = true
			i++
		default:
			i++
		}
	}
	return renamed, gone, nil
}

// namesAhead is, for each commit still to be replayed, the names p goes by
// in that commit's change, following the renames git sees along the way:
// the name before it and, when it renames p, the name after.
func namesAhead(dir string, later []string, p string) (map[string][]string, error) {
	names := map[string][]string{}
	name := p
	for _, c := range later {
		out, err := gitEnv(dir, nil, nil, "diff-tree", "-r", "-z", "-M", "--name-status", c+"^", c)
		if err != nil {
			return nil, err
		}
		names[c] = []string{name}
		f := strings.Split(out, "\x00")
		for i := 0; i+2 < len(f); i++ {
			if strings.HasPrefix(f[i], "R") && f[i+1] == name {
				name = f[i+2]
				names[c] = append(names[c], name)
				break
			}
		}
	}
	return names, nil
}

// commitChanges is every line commit c's change to paths adds and every
// line it removes, renames followed, so a moved file's lines count as
// neither.
func commitChanges(dir, c string, paths ...string) (adds, removes []string, err error) {
	out, err := gitEnv(dir, nil, nil, append([]string{"--literal-pathspecs", "diff-tree", "-p", "--no-color", "--no-ext-diff", "-M", "-U0",
		"--src-prefix=a/", "--dst-prefix=b/", c + "^", c, "--"}, paths...)...)
	if err != nil {
		return nil, nil, err
	}
	body := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			body = false
		case strings.HasPrefix(l, "@@ "):
			body = true
		case body && strings.HasPrefix(l, "+"):
			adds = append(adds, l[1:])
		case body && strings.HasPrefix(l, "-"):
			removes = append(removes, l[1:])
		}
	}
	return adds, removes, nil
}

func countLines(lines []string) map[string]int {
	n := map[string]int{}
	for _, l := range lines {
		n[l]++
	}
	return n
}

// lineRanges is sorted line numbers as "3", "3-5" or "3-5,9".
func lineRanges(at []int) string {
	var parts []string
	for i := 0; i < len(at); {
		j := i
		for j+1 < len(at) && at[j+1] == at[j]+1 {
			j++
		}
		parts = append(parts, hunk{first: at[i], end: at[j]}.lines())
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// DropsLine is what a person is told about drops of fixup.
func DropsLine(drops []HunkDrop, fixup, subject string) string {
	n, bins := 0, 0
	var where []string
	for _, d := range drops {
		if d.Binary {
			bins++
			where = append(where, d.Path+" (binary)")
			continue
		}
		n += d.N
		where = append(where, d.Path+":"+d.Lines)
	}
	var what []string
	if n > 0 {
		what = append(what, plural(n, "line"))
	}
	if bins > 0 {
		what = append(what, "the change to "+plural(bins, "binary file"))
	}
	return fmt.Sprintf("the resolution drops %s of %s %q that the old tip has: %s", strings.Join(what, " and "), git.ShortID(fixup, 7), oneLinePlan(subject), strings.Join(where, ", "))
}

// fixupHunkAdds is each run of lines commit c's change to path adds,
// in order.
func fixupHunkAdds(dir, c, path string) ([][]string, error) {
	out, err := gitEnv(dir, nil, nil, "--literal-pathspecs", "diff-tree", "-p", "--no-color", "--no-ext-diff", "--no-renames", "-U0",
		"--src-prefix=a/", "--dst-prefix=b/", "--root", c, "--", path)
	if err != nil {
		return nil, err
	}
	var runs [][]string
	var run []string
	end := func() {
		if len(run) > 0 {
			runs = append(runs, run)
		}
		run = nil
	}
	body := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "@@ "):
			end()
			body = true
		case !body:
		case strings.HasPrefix(l, "+"):
			run = append(run, l[1:])
		default:
			end()
		}
	}
	end()
	return runs, nil
}

// todoCommits is the commits the sequencer in wtPath still has to replay.
func todoCommits(wtPath string) ([]string, error) {
	dir, err := rebaseDir(wtPath)
	if err != nil {
		return nil, err
	}
	todo, err := os.ReadFile(filepath.Join(dir, "git-rebase-todo"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(todo), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch f[0] {
		case "pick", "p", "edit", "e", "reword", "r", "squash", "s", "fixup", "f":
			id := f[1]
			if strings.HasPrefix(id, "-") && len(f) > 2 { // fixup -C <id>
				id = f[2]
			}
			sha, err := gitEnv(wtPath, nil, nil, "rev-parse", "--verify", "--quiet", id+"^{commit}")
			if err == nil && sha != "" {
				out = append(out, sha)
			}
		}
	}
	return out, nil
}

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

// addVerb is "adds" for one commit, "add" for more.
func addVerb(n int) string {
	if n == 1 {
		return "adds"
	}
	return "add"
}
