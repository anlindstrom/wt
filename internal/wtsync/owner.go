package wtsync

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// LineSpan is lines From to To, inclusive, of a file.
type LineSpan struct {
	From int `json:"from"`
	To   int `json:"to"`
}

func (s LineSpan) String() string {
	if s.From == s.To {
		return strconv.Itoa(s.From)
	}
	return fmt.Sprintf("%d-%d", s.From, s.To)
}

// addLine is spans, in order, with line n above them all.
func addLine(spans []LineSpan, n int) []LineSpan {
	if last := len(spans) - 1; last >= 0 && spans[last].To == n-1 {
		spans[last].To = n
		return spans
	}
	return append(spans, LineSpan{n, n})
}

// LinesOf is path as tree has it, each line with its newline. why is set when
// it has no lines to name: no such file there, not a regular file, binary or
// empty.
func LinesOf(dir, tree, path string) (lines []string, mode, why string, err error) {
	out, err := gitEnv(dir, nil, nil, "--literal-pathspecs", "ls-tree", "-z", "--full-tree", tree, "--", path)
	if err != nil {
		return nil, "", "", err
	}
	meta, found, ok := strings.Cut(strings.TrimSuffix(out, "\x00"), "\t")
	if !ok {
		return nil, "", path + " is not committed at the line's tip", nil
	}
	f := strings.Fields(meta) // mode type sha
	if found != path || f[1] != "blob" || !strings.HasPrefix(f[0], "100") {
		return nil, "", path + " is not a file at the line's tip", nil
	}
	blob, err := runGit(dir, nil, nil, "cat-file", "blob", f[2])
	if err != nil {
		return nil, "", "", err
	}
	switch {
	case len(blob) == 0:
		return nil, "", path + " is empty at the line's tip: it has no lines", nil
	case strings.IndexByte(string(blob), 0) >= 0:
		return nil, "", path + " is a binary file: it has no lines", nil
	}
	lines = strings.SplitAfter(string(blob), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, f[0], "", nil
}

// LineOwner is a commit of a line and the lines of a file it is the last on
// the line to have changed. At is its place in the line's commits.
type LineOwner struct {
	SHA   string
	At    int
	Lines []LineSpan
}

var blameHeader = regexp.MustCompile(`^([0-9a-f]{40,64}) \d+ (\d+)`)

// Owners is, for the lines span of path as line's tip has it, each commit of
// the line that last changed some of them, newest first, and the lines no
// commit of the line changed, which the line's base already had.
func Owners(dir string, line Line, path string, span LineSpan) (owners []LineOwner, unowned []LineSpan, err error) {
	out, err := runGit(dir, nil, nil, "-c", "blame.ignoreRevsFile=", "blame", "--porcelain", "--no-textconv", "--ignore-revs-file=",
		fmt.Sprintf("-L%d,%d", span.From, span.To), line.Base+".."+line.Head, "--", path)
	if err != nil {
		return nil, nil, err
	}
	at := map[string]int{}
	for i, c := range line.Commits {
		at[c] = i
	}
	byAt := map[int]*LineOwner{}
	for _, l := range strings.Split(string(out), "\n") {
		m := blameHeader.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, nil, fmt.Errorf("git blame: %q does not read", l)
		}
		i, onLine := at[m[1]]
		if !onLine {
			unowned = addLine(unowned, n)
			continue
		}
		if byAt[i] == nil {
			byAt[i] = &LineOwner{SHA: m[1], At: i}
		}
		byAt[i].Lines = addLine(byAt[i].Lines, n)
	}
	for _, o := range byAt {
		owners = append(owners, *o)
	}
	slices.SortFunc(owners, func(a, b LineOwner) int { return b.At - a.At })
	return owners, unowned, nil
}

// Edited is whether the worktree at wtPath has uncommitted changes, staged or
// not, in the lines span of path as HEAD has it, or right beside them.
func Edited(wtPath, path string, span LineSpan) (bool, error) {
	for _, staged := range [][]string{{"--cached"}, nil} {
		args := append([]string{"--literal-pathspecs", "diff-index", "-p", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv",
			"-U0", "--inter-hunk-context=0", "--src-prefix=a/", "--dst-prefix=b/"}, staged...)
		out, err := runGit(wtPath, nil, nil, append(args, "HEAD", "--", path)...)
		if err != nil {
			return false, err
		}
		d, err := parseFileDiff(string(out))
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(d.head, func(l string) bool { return strings.HasPrefix(l, "Binary files ") }) {
			return true, nil
		}
		for _, h := range d.hunks {
			// An insertion has no old lines: it sits after line oldStart.
			if h.oldLen == 0 && h.oldStart >= span.From-1 && h.oldStart <= span.To {
				return true, nil
			}
			if h.oldLen > 0 && h.oldStart <= span.To && h.oldStart+h.oldLen-1 >= span.From {
				return true, nil
			}
		}
	}
	return false, nil
}

// ProbeVerdict is what a fold of the line makes of a change.
type ProbeVerdict string

const (
	// ProbeFolds means the fold in place applies the change at its target and
	// replays the rest of the line clean, to the line's tree with the change.
	ProbeFolds ProbeVerdict = "folds"
	// ProbeDoesNotFold means a later commit of the line has the change's
	// context, which its target lacks.
	ProbeDoesNotFold ProbeVerdict = "does-not-fold"
	// ProbeCannotTell means wt cannot say, for the reason in Why.
	ProbeCannotTell ProbeVerdict = "cannot-tell"
	// ProbeRefused means no run would take the change: Why says why.
	ProbeRefused ProbeVerdict = "refused"
)

// Probe is the answer of ProbeFold. Needs is the place in the line of the
// commit that carries the context, -1 for none.
type Probe struct {
	Verdict ProbeVerdict
	Needs   int
	Why     string
}

// probeMark stands in for each line a probe changes, followed by the id of the
// tip, which no file of the line can hold.
const probeMark = "wt sync owner: a changed line "

// Prospect is a line with a change on top of it that is not there: a commit in
// the object store only, as a fixup of one of the line's commits.
type Prospect struct {
	// Line is the line with the change as its tip.
	Line         Line
	target       int
	commit, tree string
}

// Propose commits, on line's tip and in the object store only, a change to
// exactly the lines span of path, as a fixup of line.Commits[target]. No ref,
// index or worktree is written. A non-nil answer says why there is none to
// fold. dir is the line's worktree, where a run resolves revisions.
func Propose(dir string, line Line, target int, path string, span LineSpan) (Prospect, *Probe, error) {
	var none Prospect
	answer := func(v ProbeVerdict, why string) (Prospect, *Probe, error) {
		return none, &Probe{Verdict: v, Needs: -1, Why: why}, nil
	}
	// The target's own eligibility first: it stands whatever a revision says
	// after the commit.
	subject, why, err := FixupSubject(dir, line.Commits, target)
	if err != nil {
		return none, nil, err
	}
	if why != "" {
		return answer(ProbeRefused, why)
	}
	if why, err := revisionFold(dir, line.Commits); err != nil || why != "" {
		if err != nil {
			return none, nil, err
		}
		return answer(ProbeCannotTell, why)
	}
	commit, tree, err := probeCommit(dir, line.Head, path, span, subject)
	if err != nil {
		return none, nil, err
	}
	if was, err := gitEnv(dir, nil, nil, "rev-parse", line.Head+"^{tree}"); err != nil {
		return none, nil, err
	} else if was == tree {
		return answer(ProbeCannotTell, "the change tried on those lines changes nothing")
	}
	line.Head, line.Commits = commit, append(slices.Clone(line.Commits), commit)
	return Prospect{Line: line, target: target, commit: commit, tree: tree}, nil, nil
}

// revisionFold is why commits cannot be read with a commit on top of them: a
// fold among them names its target by a revision, such as HEAD~1, which that
// commit may move. An id and a tag name the same commit either way.
func revisionFold(dir string, commits []string) (string, error) {
	all, err := readMoveCommits(dir, commits)
	if err != nil {
		return "", err
	}
	subjects := map[string]bool{}
	for _, c := range all {
		name, ok := foldName(c.subject)
		if ok && !subjects[name] && !strings.Contains(name, " ") {
			sha, code, err := gitEnvAllow(dir, nil, nil, 1, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}")
			if err != nil {
				return "", err
			}
			_, tag, err := gitEnvAllow(dir, nil, nil, 1, "show-ref", "--verify", "--quiet", "refs/tags/"+name)
			if err != nil {
				return "", err
			}
			if code == 0 && tag != 0 && !strings.HasPrefix(sha, strings.ToLower(name)) {
				return fmt.Sprintf("%s %q names its target by a revision, which a commit on top of the line may change",
					git.ShortID(c.sha, 7), oneLinePlan(c.subject)), nil
			}
		}
		subjects[c.subject] = true
	}
	return "", nil
}

// ProbeFold is whether p's change folds into its target: the line is analysed
// and replayed with it to its tip, as a fold in place takes it, by the
// declaration cfg a run reads.
func ProbeFold(dir string, cfg *Config, p Prospect) (Probe, error) {
	commits := p.Line.Commits
	a, run, err := analyseFixups(dir, commits, cfg, true)
	if err != nil {
		return Probe{Needs: -1}, err
	}
	cannot := func(format string, args ...any) (Probe, error) {
		return Probe{Verdict: ProbeCannotTell, Needs: -1, Why: fmt.Sprintf(format, args...)}, nil
	}
	i := slices.IndexFunc(a.Fixups, func(f FixupOutcome) bool { return f.Fixup == p.commit })
	if i < 0 || a.Fixups[i].Target != commits[p.target] {
		return cannot("the analysis of the line does not place the change at its target")
	}
	switch a.Fixups[i].State {
	case FixupApplies:
	case FixupAheadOfContext:
		needs := -1
		for _, g := range a.Gaps {
			if g.Fixup != p.commit {
				continue
			}
			if g.Doubt != "" {
				return cannot("%s:%s lacks its context at the target: %s", g.Path, g.Lines, g.Doubt)
			}
			needs = max(needs, slices.Index(commits, g.Needs))
		}
		if needs < 0 {
			return cannot("the change lacks its context at the target, and no commit is found to have it")
		}
		return Probe{Verdict: ProbeDoesNotFold, Needs: needs}, nil
	case FixupConflicts:
		return cannot("the fold in place conflicts at this change, and no later commit alone has its context (.wt-sync.yaml may resolve the file)")
	default:
		return cannot("%s", a.Fixups[i].Why)
	}
	// The change applies; the commits above it must too.
	if run.unreached != "" {
		return cannot("with the change folded in, %s", run.unreached)
	}
	last := run.order[len(run.order)-1]
	paths := pathCache{dir: dir, got: map[string][]string{}}
	if step, err := layerStepOver(cfg, paths, run.upTo(last)); err != nil {
		return Probe{Needs: -1}, err
	} else if step != "" {
		return cannot("the layer step %q may change what a commit above is merged onto", step)
	}
	if run.after[commits[last]] != p.tree {
		return cannot("with the change folded in, the line ends in another tree than with it at the top")
	}
	return Probe{Verdict: ProbeFolds, Needs: -1}, nil
}

// probeCommit commits, on head and in the object store only, a change to the
// lines span of path, as a commit named subject, and returns it with its tree.
func probeCommit(dir, head, path string, span LineSpan, subject string) (commit, tree string, err error) {
	lines, mode, why, err := LinesOf(dir, head, path)
	if err == nil && why != "" {
		err = errors.New(why)
	}
	if err != nil {
		return "", "", err
	}
	for n := span.From; n <= span.To; n++ {
		end := ""
		if strings.HasSuffix(lines[n-1], "\n") {
			end = "\n"
		}
		lines[n-1] = probeMark + head + " " + strconv.Itoa(n) + end
	}
	blob, err := gitEnv(dir, nil, strings.NewReader(strings.Join(lines, "")), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", "", err
	}
	idx, cleanup, err := scratchIndex()
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitEnv(dir, env, nil, "read-tree", head); err != nil {
		return "", "", err
	}
	if _, err := gitEnv(dir, env, nil, "update-index", "--cacheinfo", mode+","+blob+","+path); err != nil {
		return "", "", err
	}
	if tree, err = gitEnv(dir, env, nil, "write-tree"); err != nil {
		return "", "", err
	}
	// Nobody's commit: a look needs no identity of the user's.
	who := []string{"GIT_AUTHOR_NAME=wt", "GIT_AUTHOR_EMAIL=wt@localhost", "GIT_COMMITTER_NAME=wt", "GIT_COMMITTER_EMAIL=wt@localhost"}
	commit, err = gitEnv(dir, who, strings.NewReader(subject+"\n"), "commit-tree", tree, "-p", head, "-F", "-")
	return commit, tree, err
}
