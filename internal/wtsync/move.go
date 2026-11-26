package wtsync

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// movePrefix starts the subject of a move! commit: an empty commit naming,
// after the prefix, the commit the part goes into, and in its body the
// commit the part comes from (Move-From) and the part: whole paths
// (Move-Path) and line ranges taking whole hunks (Move-Lines), one or more
// of either. Git's autosquash does not know it; a fold of the line does.
const movePrefix = "move! "

// The body lines of a move! commit, and of the commit the part is kept as.
const (
	moveFromKey   = "Move-From:"
	movePathKey   = "Move-Path:"
	moveLinesKey  = "Move-Lines:"
	moveCommitKey = "Move-Commit:"
	moveIntoKey   = "Move-Into:"
)

func isMove(subject string) bool { return strings.HasPrefix(subject, movePrefix) }

// PartMove is a move! commit of a line: the part of Source it names, folded
// into Target, which lies below.
type PartMove struct {
	Commit        string `json:"commit"`
	Subject       string `json:"subject"`
	Target        string `json:"target"`
	TargetSubject string `json:"targetSubject"`
	Source        string `json:"source"`
	SourceSubject string `json:"sourceSubject"`
	// Paths is the paths moved whole; Lines the ranges moved of others.
	Paths []string    `json:"paths"`
	Lines []LineRange `json:"lines,omitempty"`
	// Part is the commit the run folds into Target: Target's tree with the
	// part applied, Target its parent. Kept is the ref it is kept under
	// once a run keeps it (KeepMoves); empty before.
	Part string `json:"part"`
	Kept string `json:"kept"`
	// whole is the source's change to Paths, and picks the hunks taken of
	// each path in Lines: what the replay builds Part from.
	whole string
	picks map[string]linesPick
}

// patchOnto is the source's change the move takes, as a patch of tree.
func (m PartMove) patchOnto(dir, tree string) (string, error) {
	var patch strings.Builder
	for _, p := range slices.Sorted(maps.Keys(m.picks)) {
		back, err := diffOfPath(dir, tree, m.Source+"^", p)
		if err != nil {
			return "", err
		}
		patch.WriteString(m.picks[p].patch(back.linesAhead))
	}
	return patch.String() + m.whole, nil
}

// touched is every path the move changes, each once.
func (m PartMove) touched() []string {
	paths := slices.Clone(m.Paths)
	for _, r := range m.Lines {
		if !slices.Contains(paths, r.Path) {
			paths = append(paths, r.Path)
		}
	}
	return paths
}

// What is the part as a person reads it: the whole paths, then the ranges.
func (m PartMove) What() string { return MoveWhat(m.Paths, m.Lines) }

// MoveWhat names whole paths and line ranges as What does.
func MoveWhat(paths []string, lines []LineRange) string {
	what := slices.Clone(paths)
	for _, r := range lines {
		what = append(what, r.String())
	}
	return strings.Join(what, ", ")
}

// Line is what a person is told about the move.
func (m PartMove) Line() string {
	return fmt.Sprintf("move! %s moves %s of %s %q into %s %q", git.ShortID(m.Commit, 7), m.What(),
		git.ShortID(m.Source, 7), oneLinePlan(m.SourceSubject), git.ShortID(m.Target, 7), oneLinePlan(m.TargetSubject))
}

// MoveRunRefusal is why a run with move! commits cannot take them, "" when
// it can: they fold in place only, with the checks at their targets and
// sources (--each-commit=targets).
func MoveRunRefusal(inPlace, edit bool, eachCommit string) string {
	switch {
	case !inPlace:
		return "a move! commit folds in place only: run it without --onto, then move the line onto the target"
	case edit:
		return "a move! commit cannot be folded by an --edit run: run --autosquash without --edit"
	case eachCommit != EachTargets:
		return "a run with a move! commit checks at each target and source: add --each-commit=targets"
	}
	return ""
}

// moveCommit is a commit of the line read for PlanMoves.
type moveCommit struct {
	sha, subject, body string
}

// PlanMoves is every move! commit among commits, the line oldest first, with
// the part each moves built as a commit in the object store (PartMove.Part).
// why is set, and no move returned, when the fold cannot take one: the
// target or source does not name exactly one commit below it, the target is
// not below the source, the move! commit changes anything, a path is one the
// source does not change or is named both whole and by lines, a range splits
// a hunk or selects none, the part is the whole source, two moves take the
// same hunk, the part does not apply at the target, or the fold, replayed in
// the object store with the part behind its target's fixups, conflicts
// anywhere, puts a hunk elsewhere than the source has it, leaves the source
// other than what no move takes or ends in another tree than the line's tip.
// Nothing but objects in the store is written.
func PlanMoves(dir string, commits []string) ([]PartMove, string, error) {
	if len(commits) == 0 {
		return nil, "", nil
	}
	all, err := readMoveCommits(dir, commits)
	if err != nil {
		return nil, "", err
	}
	var moves []PartMove
	for i, c := range all {
		if !isMove(c.subject) {
			continue
		}
		m, why, err := planMove(dir, all, i)
		if err != nil || why != "" {
			return nil, why, err
		}
		moves = append(moves, m)
	}
	if len(moves) == 0 {
		return nil, "", nil
	}
	if why := sharedParts(moves); why != "" {
		return nil, why, nil
	}
	if why, err := replayMoves(dir, all, moves); err != nil || why != "" {
		return nil, why, err
	}
	return moves, "", nil
}

// readMoveCommits is commits with their subjects and bodies, in order.
func readMoveCommits(dir string, commits []string) ([]moveCommit, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	out, err := gitEnv(dir, nil, nil, append([]string{"log", "--no-walk=unsorted", "--format=%H%x00%s%x00%b%x1e"}, commits...)...)
	if err != nil {
		return nil, err
	}
	var all []moveCommit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x00", 3)
		if len(f) == 3 {
			all = append(all, moveCommit{f[0], f[1], f[2]})
		}
	}
	return all, nil
}

// planMove reads, resolves and checks the move! commit all[i], and builds
// its part.
func planMove(dir string, all []moveCommit, i int) (PartMove, string, error) {
	c := all[i]
	m := PartMove{Commit: c.sha, Subject: c.subject}
	refuse := func(format string, a ...any) (PartMove, string, error) {
		return PartMove{}, fmt.Sprintf("%s %q: ", git.ShortID(c.sha, 7), oneLinePlan(c.subject)) + fmt.Sprintf(format, a...), nil
	}
	var from []string
	var badRange error
	for _, l := range strings.Split(c.body, "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, moveFromKey); ok {
			from = append(from, strings.TrimSpace(v))
		} else if v, ok := strings.CutPrefix(l, movePathKey); ok {
			if p := strings.TrimSpace(v); p != "" && !slices.Contains(m.Paths, p) {
				m.Paths = append(m.Paths, p)
			}
		} else if v, ok := strings.CutPrefix(l, moveLinesKey); ok {
			r, err := ParseLineRange(strings.TrimSpace(v))
			if err != nil {
				badRange = err
			} else if !slices.Contains(m.Lines, r) {
				m.Lines = append(m.Lines, r)
			}
		}
	}
	into := strings.TrimSpace(strings.TrimPrefix(c.subject, movePrefix))
	switch {
	case into == "":
		return refuse("names no target after move!")
	case len(from) != 1 || from[0] == "":
		return refuse("needs one %s line naming the commit the part comes from", moveFromKey)
	case badRange != nil:
		return refuse("its %s %s", moveLinesKey, badRange)
	case len(m.Paths) == 0 && len(m.Lines) == 0:
		return refuse("needs a %s line for each path it moves, or a %s line for each range", movePathKey, moveLinesKey)
	}
	for _, r := range m.Lines {
		if slices.Contains(m.Paths, r.Path) {
			return refuse("%s is named by %s and by %s: a path is moved whole or by lines, not both", r.Path, movePathKey, moveLinesKey)
		}
	}
	t, why, err := resolveOnLine(dir, all[:i], into)
	if err != nil {
		return PartMove{}, "", err
	}
	if why != "" {
		return refuse("its target %s", why)
	}
	s, why, err := resolveOnLine(dir, all[:i], from[0])
	if err != nil {
		return PartMove{}, "", err
	}
	if why != "" {
		return refuse("its %s %s", moveFromKey, why)
	}
	target, source := all[t], all[s]
	m.Target, m.TargetSubject, m.Source, m.SourceSubject = target.sha, target.subject, source.sha, source.subject
	switch {
	case t >= s:
		return refuse("the target %s is not below the source %s on the line", git.ShortID(target.sha, 7), git.ShortID(source.sha, 7))
	case isFold(target.subject) || isMove(target.subject):
		return refuse("the target %s is itself folded into another commit: name that one", git.ShortID(target.sha, 7))
	case isFold(source.subject) || isMove(source.subject):
		return refuse("the source %s is itself folded into another commit: move the part from that one", git.ShortID(source.sha, 7))
	}
	tree, err := gitEnv(dir, nil, nil, "rev-parse", c.sha+"^{tree}")
	if err != nil {
		return PartMove{}, "", err
	}
	if parent, err := gitEnv(dir, nil, nil, "rev-parse", c.sha+"^^{tree}"); err != nil {
		return PartMove{}, "", err
	} else if parent != tree {
		return refuse("a move! commit changes nothing itself, and this one does: commit those changes on their own")
	}
	changed, err := gitEnv(dir, nil, nil, "diff-tree", "--no-commit-id", "--name-only", "-r", "--no-renames", "-z", source.sha)
	if err != nil {
		return PartMove{}, "", err
	}
	paths := strings.Split(strings.TrimRight(changed, "\x00"), "\x00")
	for _, p := range m.touched() {
		if !slices.Contains(paths, p) {
			return refuse("the source %s does not change %s: a move takes what the source changes", git.ShortID(source.sha, 7), p)
		}
	}
	m.picks = map[string]linesPick{}
	for _, p := range m.touched() {
		if slices.Contains(m.Paths, p) {
			continue
		}
		d, err := diffOfPath(dir, source.sha+"^", source.sha, p)
		if err != nil {
			return PartMove{}, "", err
		}
		var ranges []LineRange
		for _, r := range m.Lines {
			if r.Path == p {
				ranges = append(ranges, r)
			}
		}
		pick, why := pickHunks(d, ranges)
		if why != "" {
			return refuse("%s", why)
		}
		m.picks[p] = pick
	}
	if takesAll(m, paths) {
		what := "every path"
		if len(m.Lines) > 0 {
			what = "every hunk of every path"
		}
		return refuse("it moves %s the source %s changes: make the source a fixup! of %s instead", what, git.ShortID(source.sha, 7), git.ShortID(target.sha, 7))
	}
	if len(m.Paths) > 0 {
		out, err := runGit(dir, nil, nil, append([]string{"--literal-pathspecs", "diff-tree", "-p", "--binary", "--full-index", "--no-renames", "--no-color", "--no-ext-diff",
			source.sha + "^", source.sha, "--"}, m.Paths...)...)
		if err != nil {
			return PartMove{}, "", err
		}
		m.whole = string(out)
	}
	return m, "", nil
}

// takesAll is whether m leaves its source nothing: each of paths, what the
// source changes, is taken whole, or every hunk of it by lines with no change
// of mode left over.
func takesAll(m PartMove, paths []string) bool {
	for _, p := range paths {
		if slices.Contains(m.Paths, p) {
			continue
		}
		pick, ok := m.picks[p]
		if !ok || pick.diff.newMode != "" || slices.Contains(pick.picked, false) {
			return false
		}
	}
	return true
}

// sharedParts is why the moves cannot be made together, "" when they can: two
// of one source take the same hunk.
func sharedParts(moves []PartMove) string {
	bySource := map[string][]PartMove{}
	for _, m := range moves {
		for _, o := range bySource[m.Source] {
			for _, p := range slices.Sorted(maps.Keys(m.picks)) {
				pick := m.picks[p]
				other, byLines := o.picks[p]
				for i, h := range pick.diff.hunks {
					if pick.picked[i] && (slices.Contains(o.Paths, p) || byLines && other.picked[i]) {
						return fmt.Sprintf("%s: it takes the hunk %s:%s, which %s also takes", m.Line(), p, h.header, git.ShortID(o.Commit, 7))
					}
				}
			}
			for _, p := range slices.Sorted(maps.Keys(o.picks)) {
				if pick := o.picks[p]; slices.Contains(m.Paths, p) {
					i := slices.Index(pick.picked, true)
					return fmt.Sprintf("%s: it takes the hunk %s:%s, which %s also takes", m.Line(), p, pick.diff.hunks[i].header, git.ShortID(o.Commit, 7))
				}
			}
		}
		bySource[m.Source] = append(bySource[m.Source], m)
	}
	return ""
}

// resolveOnLine is the commit of earlier name names: the one commit its id
// names, else the one commit with that subject. why says what it names
// otherwise, the candidates when there are several.
func resolveOnLine(dir string, earlier []moveCommit, name string) (int, string, error) {
	if sha, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}"); err == nil {
		sha = strings.TrimSpace(sha)
		for j, c := range earlier {
			if c.sha == sha {
				return j, "", nil
			}
		}
	}
	var found []int
	for j, c := range earlier {
		if c.subject == name {
			found = append(found, j)
		}
	}
	switch len(found) {
	case 1:
		return found[0], "", nil
	case 0:
		return 0, fmt.Sprintf("%q names no commit below it on the line: name one by its id or its whole subject", name), nil
	}
	ids := make([]string, len(found))
	for k, j := range found {
		ids[k] = git.ShortID(earlier[j].sha, 7)
	}
	return 0, fmt.Sprintf("%q names %d commits of the line (%s): name one by its id", name, len(found), strings.Join(ids, ", ")), nil
}

// applyPart is tree with the patch applied, exactly, with no three-way
// fallback; ok is false when it does not apply. It writes only objects,
// through a private index.
func applyPart(dir, tree, patch string) (string, bool, error) {
	idx, cleanup, err := scratchIndex()
	if err != nil {
		return "", false, err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitEnv(dir, env, nil, "read-tree", tree); err != nil {
		return "", false, err
	}
	if _, code, err := gitEnvAllow(dir, env, strings.NewReader(patch), 1, "apply", "--cached", "--binary", "-"); err != nil {
		return "", false, err
	} else if code != 0 {
		return "", false, nil
	}
	out, err := gitEnv(dir, env, nil, "write-tree")
	return out, err == nil, err
}

// replayMoves replays the fold in the object store as the run will make it:
// the line's commits in order, each fixup!, squash! and amend! right behind
// the commit it folds into (and a fold of a fold behind that one), each
// move's part last in its target's group, and the move! commits left out.
// why is set when any of them does not merge clean, when a part does not
// apply at its target as the target's fixups leave it, when a source's
// replay changes a path moved whole, when a path moved by lines does not have
// its hunks where the source has them or the replay adds other than the rest,
// or when the tip's tree is not the line's. Each move's Part is built there (buildPart).
func replayMoves(dir string, all []moveCommit, moves []PartMove) (string, error) {
	lc := make([]lineCommit, len(all))
	index := map[string]int{}
	for i, c := range all {
		lc[i] = lineCommit{c.sha, c.subject}
		index[c.sha] = i
	}
	folds := map[int][]int{} // a commit's folds, line order
	isFoldOf := map[int]bool{}
	for i, c := range all {
		if !isFold(c.subject) {
			continue
		}
		if j, ok := foldTarget(dir, lc[:i], c.subject, index); ok {
			folds[j] = append(folds[j], i)
			isFoldOf[i] = true
		}
	}
	parts := map[string][]int{}
	sources := map[string][]PartMove{}
	for n, m := range moves {
		parts[m.Target] = append(parts[m.Target], n)
		sources[m.Source] = append(sources[m.Source], m)
	}
	cur, err := gitEnv(dir, nil, nil, "rev-parse", all[0].sha+"^^{tree}")
	if err != nil {
		return "", err
	}
	step := func(rev string) (bool, error) {
		parent, err := gitEnv(dir, nil, nil, "rev-parse", rev+"^^{tree}")
		if err != nil {
			return false, err
		}
		tree, clean, _, _, err := mergeTree(dir, parent, cur, rev)
		if err != nil || !clean {
			return false, err
		}
		cur = tree
		return true, nil
	}
	var group func(i int) (string, error)
	group = func(i int) (string, error) {
		c := all[i]
		// A path moved whole is there already, so the replay must leave it as
		// it is; one moved by lines keeps the hunks no move takes.
		var whole []string
		byLines := map[string]linesPick{}
		for _, m := range sources[c.sha] {
			whole = append(whole, m.Paths...)
			for p, pick := range m.picks {
				byLines[p] = pick.and(byLines[p])
			}
		}
		var before []treeEntry
		for _, p := range whole {
			e, err := entryAt(dir, cur, p)
			if err != nil {
				return "", err
			}
			before = append(before, e)
		}
		below := cur
		if ok, err := step(c.sha); err != nil {
			return "", err
		} else if !ok {
			return moveConflict(all, moves, i), nil
		}
		for k, p := range whole {
			got, err := entryAt(dir, cur, p)
			if err != nil {
				return "", err
			}
			if got != before[k] {
				return fmt.Sprintf("%s: the source's replay still changes %s once the part is below it", sources[c.sha][0].Line(), p), nil
			}
		}
		if len(byLines) > 0 {
			if p, err := keepsTheRest(dir, c.sha, below, cur, byLines); err != nil {
				return "", err
			} else if p != "" {
				return fmt.Sprintf("%s: the source's replay would not leave it exactly the rest of %s once the part is below it", sources[c.sha][0].Line(), p), nil
			}
		}
		for _, f := range folds[i] {
			if why, err := group(f); err != nil || why != "" {
				return why, err
			}
		}
		for _, n := range parts[c.sha] {
			why, err := buildPart(dir, all, index, &moves[n], cur)
			if err != nil || why != "" {
				return why, err
			}
			if cur, err = gitEnv(dir, nil, nil, "rev-parse", moves[n].Part+"^{tree}"); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	for i, c := range all {
		if isFoldOf[i] || isMove(c.subject) {
			continue
		}
		if why, err := group(i); err != nil || why != "" {
			return why, err
		}
	}
	tip, err := gitEnv(dir, nil, nil, "rev-parse", all[len(all)-1].sha+"^{tree}")
	if err != nil {
		return "", err
	}
	if cur != tip {
		return fmt.Sprintf("the fold with the move would end in another tree than the line's tip %s: nothing is moved", git.ShortID(all[len(all)-1].sha, 7)), nil
	}
	return "", nil
}

// buildPart builds m.Part where the fold applies it: on folded, the target's
// tree with every fixup of it folded in, the part applied with no three-way
// fallback. The part's parent is a commit on the target with folded as its
// tree, so the fold's fixup of it brings exactly the part. why is set when
// it does not apply, naming the first commit between target and source
// whose tree carries its context.
func buildPart(dir string, all []moveCommit, index map[string]int, m *PartMove, folded string) (string, error) {
	patch, err := m.patchOnto(dir, folded)
	if err != nil {
		return "", err
	}
	tree, ok, err := applyPart(dir, folded, patch)
	if err != nil {
		return "", err
	}
	if !ok {
		why := fmt.Sprintf("%s %q: the part does not apply at the target %s as its fixups leave it", git.ShortID(m.Commit, 7), oneLinePlan(m.Subject), git.ShortID(m.Target, 7))
		for k := index[m.Target] + 1; k < index[m.Source]; k++ {
			patch, err := m.patchOnto(dir, all[k].sha+"^{tree}")
			if err != nil {
				return "", err
			}
			if _, ok, err := applyPart(dir, all[k].sha+"^{tree}", patch); err != nil {
				return "", err
			} else if ok {
				why += fmt.Sprintf(": its context comes from %s %q, which lies between; name that commit, or one above it, as the target", git.ShortID(all[k].sha, 7), oneLinePlan(all[k].subject))
				break
			}
		}
		return why, nil
	}
	if ok, err := landsAsSourced(dir, m.Source, m.picks, folded, tree); err != nil {
		return "", err
	} else if !ok {
		return fmt.Sprintf("%s: the part would not land at the target %s where the source has it", m.Line(), git.ShortID(m.Target, 7)), nil
	}
	base, err := gitEnv(dir, nil, strings.NewReader("wt sync: "+git.ShortID(m.Target, 7)+" with its fixups, before the part\n"), "commit-tree", folded, "-p", m.Target)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("wt sync: the part %s moves into %s\n\n%s %s\n%s %s\n%s %s\n", git.ShortID(m.Commit, 7), git.ShortID(m.Target, 7),
		moveCommitKey, m.Commit, moveFromKey, m.Source, moveIntoKey, m.Target)
	msg += partLines(m.Paths, m.Lines)
	m.Part, err = gitEnv(dir, nil, strings.NewReader(msg), "commit-tree", tree, "-p", base)
	return "", err
}

// moveConflict is why the fold refuses a conflict at all[i]: a commit
// between a move's target and its source conflicts with the part; any other
// commit is a fold that does not merge clean, which a move cannot prove.
func moveConflict(all []moveCommit, moves []PartMove, i int) string {
	pos := map[string]int{}
	for k, c := range all {
		pos[c.sha] = k
	}
	for _, m := range moves {
		if pos[m.Target] < i && i <= pos[m.Source] {
			return fmt.Sprintf("%s: %s %q conflicts with the part moved below it", m.Line(), git.ShortID(all[i].sha, 7), oneLinePlan(all[i].subject))
		}
	}
	return fmt.Sprintf("the fold with the move does not merge clean at %s %q: a move needs a fold that does; fold the line without it first, then move", git.ShortID(all[i].sha, 7), oneLinePlan(all[i].subject))
}

// KeepMoves keeps each move's part under a ref of its own, created and never
// moved: refs/wt-sync-kept/<branch>/<epoch>-move-1, -move-2, and so on, and
// sets each move's Kept. A ref that exists already is refused.
func KeepMoves(mainRoot, branch string, epoch int64, moves []PartMove) error {
	for n := range moves {
		ref := fmt.Sprintf("%s%s/%d-move-%d", EditKeptPrefix, branch, epoch, n+1)
		part := moves[n].Part
		if _, err := gitEnv(mainRoot, nil, nil, "update-ref", "-m", "wt sync: a move's part", ref, part, strings.Repeat("0", len(part))); err != nil {
			return fmt.Errorf("the part of %s could not be kept under %s: %w", git.ShortID(moves[n].Commit, 7), ref, err)
		}
		moves[n].Kept = ref
	}
	return nil
}

// KeptMoves is the parts a run of branch at epoch kept (KeepMoves), read
// back from their refs: what a resume of the run checks at.
func KeptMoves(mainRoot, branch string, epoch int64) ([]PartMove, error) {
	prefix := fmt.Sprintf("%s%s/%d-move-", EditKeptPrefix, branch, epoch)
	// for-each-ref matches a pattern by whole path segments, or by glob:
	// the refs are listed under the branch and taken by their prefix here.
	out, err := gitEnv(mainRoot, nil, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(contents:body)%1e", EditKeptPrefix+branch+"/")
	if err != nil {
		return nil, err
	}
	type kept struct {
		n int
		m PartMove
	}
	var found []kept
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x00", 3)
		if len(f) != 3 {
			continue
		}
		rest, ok := strings.CutPrefix(f[0], prefix)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		m := PartMove{Kept: f[0], Part: f[1]}
		for _, l := range strings.Split(f[2], "\n") {
			key, v, _ := strings.Cut(strings.TrimSpace(l), " ")
			switch key {
			case moveCommitKey:
				m.Commit = v
			case moveFromKey:
				m.Source = v
			case moveIntoKey:
				m.Target = v
			case movePathKey:
				m.Paths = append(m.Paths, v)
			case moveLinesKey:
				r, err := ParseLineRange(v)
				if err != nil {
					return nil, fmt.Errorf("the kept part %s does not say what it moves: %w", f[0], err)
				}
				m.Lines = append(m.Lines, r)
			}
		}
		if m.Commit == "" || m.Target == "" || len(m.touched()) == 0 {
			return nil, errors.New("the kept part " + f[0] + " does not say what it moves")
		}
		found = append(found, kept{n, m})
	}
	slices.SortFunc(found, func(a, b kept) int { return a.n - b.n })
	moves := make([]PartMove, len(found))
	for i, k := range found {
		moves[i] = k.m
	}
	return moves, nil
}

// MoveName is what a move! commit written on the line commits should call
// name: the one commit of the line it names, by id or by whole subject, and
// the name to write for it, its subject when no other commit of the line
// has that subject, which outlives a fold below rewriting its id, else its
// full id. why says what it names otherwise.
func MoveName(dir string, commits []string, name string) (sha, write, why string, err error) {
	all, err := readMoveCommits(dir, commits)
	if err != nil {
		return "", "", "", err
	}
	j, why, err := resolveOnLine(dir, all, name)
	if err != nil || why != "" {
		return "", "", why, err
	}
	c := all[j]
	same := 0
	for _, o := range all {
		if o.subject == c.subject {
			same++
		}
	}
	if same == 1 {
		return c.sha, c.subject, "", nil
	}
	return c.sha, c.sha, "", nil
}

// MoveMessage is the message of a move! commit that moves paths, and the
// ranges lines, of the commit from names into the commit into names.
func MoveMessage(into, from string, paths []string, lines []LineRange) string {
	return movePrefix + into + "\n\n" + moveFromKey + " " + from + "\n" + partLines(paths, lines)
}

// partLines is the body lines naming a part.
func partLines(paths []string, lines []LineRange) string {
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "%s %s\n", movePathKey, p)
	}
	for _, r := range lines {
		fmt.Fprintf(&b, "%s %s\n", moveLinesKey, r)
	}
	return b.String()
}

// WriteMove commits msg as an empty move! commit on line's tip, checks the
// line with it as a fold would take it (PlanMoves), and only then moves
// line's branch to it, over the tip it read. why is set, and the branch left
// where it was, when the fold would refuse the commit; the returned move is
// the one the commit makes.
func WriteMove(mainRoot string, line Line, msg string) (PartMove, string, error) {
	commit, err := gitEnv(mainRoot, nil, strings.NewReader(msg), "commit-tree", line.Head+"^{tree}", "-p", line.Head, "-F", "-")
	if err != nil {
		return PartMove{}, "", err
	}
	moves, why, err := PlanMoves(mainRoot, append(append([]string{}, line.Commits...), commit))
	if err != nil || why != "" {
		return PartMove{}, why, err
	}
	if _, err := gitEnv(mainRoot, nil, nil, "update-ref", "-m", "wt sync move", "refs/heads/"+line.Branch, commit, line.Head); err != nil {
		return PartMove{}, "", fmt.Errorf("%s moved while the move! commit was written: %w", line.Branch, err)
	}
	for _, m := range moves {
		if m.Commit == commit {
			return m, "", nil
		}
	}
	return PartMove{}, "", errors.New("the move! commit written is not among the line's moves")
}
