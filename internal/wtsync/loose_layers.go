package wtsync

import (
	"fmt"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/git"
)

// LooseLayer is a branch in no worktree whose tip is a commit of a line a
// run rebases: wt up and wt sync rebase move the worktrees of a stack, and
// this branch, sitting between two of them, would stay on the old commits.
type LooseLayer struct {
	Branch string
	Old    string
	// Count is the position of its tip among the line's commits, from 1.
	Count int
	// New is where the run moved it; empty while it is not moved.
	New string
	// Left is why it was not moved; empty for a branch that was, or is still
	// to be.
	Left string
}

// LooseLine is the commits a run replays for one worktree, oldest first, and
// the layer branches among them that have no worktree.
type LooseLine struct {
	Commits []string
	Layers  []LooseLayer
}

// ReadLoose reads the layer branches in no worktree on the line of branch
// above base: the commits base..branch. A branch listed in held (the
// branches a worktree checks out or holds) is no layer, and neither is one at
// base itself, nor branch. One that cannot be moved is listed with Left
// set: the trunk branch itself, which is never a layer, a backup name, a tip that is a fixup!, squash! or amend! commit, or
// any branch of a line with a merge commit, which a rebase replays without
// its merge.
func ReadLoose(dir, branch, base, trunk string, backups []string, held func(string) bool) (LooseLine, error) {
	var line LooseLine
	sweep := config.CompileRefPatterns(backups)
	head, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return line, err
	}
	rng := base + ".." + head
	out, err := gitEnv(dir, nil, nil, "rev-list", "--reverse", rng)
	if err != nil {
		return line, err
	}
	pos := map[string]int{}
	for _, sha := range strings.Fields(out) {
		line.Commits = append(line.Commits, sha)
		pos[sha] = len(line.Commits)
	}
	merge, err := gitEnv(dir, nil, nil, "log", "--merges", "--format=%h %s", "-1", rng)
	if err != nil {
		return line, err
	}
	refs, err := gitEnv(dir, nil, nil, "for-each-ref", "--format=%(refname:short) %(objectname) %(contents:subject)", "refs/heads/")
	if err != nil {
		return line, err
	}
	for _, r := range strings.Split(refs, "\n") {
		name, rest, _ := strings.Cut(r, " ")
		tip, subject, _ := strings.Cut(rest, " ")
		if name == "" || name == branch || held(name) || pos[tip] == 0 {
			continue
		}
		l := LooseLayer{Branch: name, Old: tip, Count: pos[tip]}
		switch {
		case merge != "":
			first, _, _ := strings.Cut(merge, "\n")
			l.Left = fmt.Sprintf("the line has a merge commit (%s), which a rebase replays without its merge", first)
		case trunk != "" && name == trunk:
			l.Left = "it is trunk, which is never moved as a layer"
		case backup(name, sweep):
			l.Left = "a backup name; name it with wt sync rebase --stack --layer to move it"
		case isFold(subject):
			l.Left = fmt.Sprintf("it ends at a commit autosquash folds (%s %q): where the layer ends is not defined", git.ShortID(tip, 7), subject)
		}
		line.Layers = append(line.Layers, l)
	}
	slices.SortStableFunc(line.Layers, func(a, b LooseLayer) int {
		if a.Count != b.Count {
			return a.Count - b.Count
		}
		return strings.Compare(a.Branch, b.Branch)
	})
	return line, nil
}

// LeaveAll marks every layer not yet moved or left as left, for why.
func (l *LooseLine) LeaveAll(why string) {
	for i := range l.Layers {
		if l.Layers[i].New == "" && l.Layers[i].Left == "" {
			l.Layers[i].Left = why
		}
	}
}

// Move moves each layer to the replay of its commit, once the rebase of the
// line has finished: the run's new commits above onto, up to newTip, must be
// the old ones one for one, in order, with the same subjects. A rebase that
// dropped, folded or added a commit leaves them where they are, for wt sync
// rebase --stack to place. Each moved layer is pinned under the run's epoch
// with a safety and a result ref, so wt sync undo puts it back with the
// worktrees; the branch moves only from the tip it was read at. A layer
// checked out in the meantime is left.
func (l *LooseLine) Move(mainRoot string, epoch int64, onto, newTip string) error {
	pending := slices.ContainsFunc(l.Layers, func(x LooseLayer) bool { return x.Left == "" && x.New == "" })
	if !pending {
		return nil
	}
	fresh, err := gitEnv(mainRoot, nil, nil, "rev-list", "--reverse", onto+".."+newTip)
	if err != nil {
		return err
	}
	moved := strings.Fields(fresh)
	if len(moved) != len(l.Commits) {
		l.LeaveAll(fmt.Sprintf("the rebase replayed %d of the line's %d commits: wt sync rebase --stack moves layers through a rebase that drops or folds commits", len(moved), len(l.Commits)))
		return nil
	}
	for i, c := range l.Commits {
		was, err := gitEnv(mainRoot, nil, nil, "log", "-1", "--format=%s", c)
		if err != nil {
			return err
		}
		now, err := gitEnv(mainRoot, nil, nil, "log", "-1", "--format=%s", moved[i])
		if err != nil {
			return err
		}
		if was != now {
			l.LeaveAll(fmt.Sprintf("commit %d of the line is %q after the rebase and was %q before: wt sync rebase --stack places layers through a rebase that changed commits", i+1, now, was))
			return nil
		}
	}
	for i := range l.Layers {
		x := &l.Layers[i]
		if x.Left != "" || x.New != "" {
			continue
		}
		m := LayerMove{Branch: x.Branch, Old: x.Old, New: moved[x.Count-1], Result: MovePlanned, How: MoveByRef}
		if m.New == m.Old {
			continue
		}
		// Loose layers keep no stack record; their pins are what undo reads.
		if err := moveRef(mainRoot, epoch, &m, MoveOptions{}, func() error { return nil }); err != nil {
			return err
		}
		if m.Result == MoveMoved {
			x.New = m.New
		} else {
			x.Left = m.Held
		}
	}
	return nil
}
