package wtsync

import (
	"fmt"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// A fold with --onto replays the line onto another commit, so the tip cannot
// stay the line's. A move only shares changes out between commits: the tip
// must be the tree the same rebase gives without the move! commits.

// PlanMovesOnto is PlanMoves for a fold that replays commits, the line oldest
// first, onto onto; upstream is where --upstream says the replayed commits
// start, "" without it. Onto the line's own base, or with no onto, it is a
// fold in place. Onto another commit why is also set when that commit already
// carries a move's target or source, when a lifting rule of cfg claims a file
// that both it and the line changed, when the line does not replay onto it
// clean without the moves, and when the fold with them would not end in the
// tree of that replay.
func PlanMovesOnto(dir string, cfg *Config, commits []string, onto, upstream string) ([]PartMove, string, error) {
	if ok, err := HasMoves(dir, commits); err != nil || !ok {
		return nil, "", err
	}
	base, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", commits[0]+"^")
	if err != nil {
		return nil, "", err
	}
	if onto != "" {
		if onto, err = gitEnv(dir, nil, nil, "rev-parse", "--verify", onto+"^{commit}"); err != nil {
			return nil, "", err
		}
	}
	if onto == "" || onto == base {
		return PlanMoves(dir, commits)
	}
	short := git.ShortID(onto, 7)
	first := fmt.Sprintf("rebase the line onto %s without the move! commit first, then write the move again", short)
	all, err := readMoveCommits(dir, commits)
	if err != nil {
		return nil, "", err
	}
	moves, why, err := readMoves(dir, all, func(name string) (string, error) {
		carried, err := namedOn(dir, onto, name)
		if err != nil || !carried {
			return "", err
		}
		return fmt.Sprintf("%q is a commit the new base %s already carries: %s", name, short, first), nil
	})
	if err != nil || why != "" {
		return nil, why, err
	}
	if why, err := liftedByBase(dir, cfg, base, onto, commits[len(commits)-1]); err != nil {
		return nil, "", err
	} else if why != "" {
		return nil, fmt.Sprintf("the new base %s changes %s, which the line changes too and a lifting rule renumbers as the line is replayed, so a move cannot be checked against it: %s", short, why, first), nil
	}
	skip, err := carriedPatches(dir, onto, upstream, commits[len(commits)-1])
	if err != nil {
		return nil, "", err
	}
	carries := func(m PartMove, what, sha, subject string) string {
		return fmt.Sprintf("%s: the new base %s already carries the %s %s %q: %s", m.Line(), short, what, git.ShortID(sha, 7), oneLinePlan(subject), first)
	}
	for _, m := range moves {
		if skip[m.Target] {
			return nil, carries(m, "target", m.Target, m.TargetSubject), nil
		}
		if skip[m.Source] {
			return nil, carries(m, "source", m.Source, m.SourceSubject), nil
		}
	}
	plain, why, err := replayWithout(dir, all, onto, skip)
	if err != nil || why != "" {
		if why != "" {
			why += ": " + first
		}
		return nil, why, err
	}
	// git drops a pick that comes out empty, and a fixup after it would go
	// into the commit below.
	for _, m := range moves {
		if plain.emptied[m.Target] {
			return nil, carries(m, "target", m.Target, m.TargetSubject), nil
		}
		if plain.emptied[m.Source] {
			return nil, carries(m, "source", m.Source, m.SourceSubject), nil
		}
	}
	from, err := ontoReplay(dir, all, onto, skip)
	if err != nil {
		return nil, "", err
	}
	from.conflict = func(i int, _ []Conflict) string { return moveConflict(all, moves, i) }
	from.between = func(sha string) string { return plain.after[sha] }
	got, why, err := replayLine(dir, all, moves, from)
	if err != nil {
		return nil, "", err
	}
	if why == "" && got.tree != plain.tree {
		why = "the fold with the move would end in another tree than the line replayed without it: nothing is moved"
	}
	if why == "" {
		return moves, "", nil
	}
	why = "replayed onto " + short + ": " + why
	// Moves that fold in place are refused for what the new base brought, and
	// can be written again once the line is on it.
	if _, inPlace, err := PlanMoves(dir, commits); err != nil {
		return nil, "", err
	} else if inPlace == "" {
		why += ": " + first
	}
	return nil, why, nil
}

// ontoReplay is a replay of all onto the commit onto, leaving out skip.
func ontoReplay(dir string, all []moveCommit, onto string, skip map[string]bool) (replayFrom, error) {
	tree, err := gitEnv(dir, nil, nil, "rev-parse", onto+"^{tree}")
	return replayFrom{tree: tree, skip: skip,
		conflict: func(i int, conflicts []Conflict) string {
			var paths []string
			for _, c := range conflicts {
				paths = append(paths, c.Path)
			}
			return fmt.Sprintf("the line does not replay onto %s: %s %q conflicts with it in %s", git.ShortID(onto, 7),
				git.ShortID(all[i].sha, 7), oneLinePlan(all[i].subject), strings.Join(paths, ", "))
		},
		between: func(string) string { return "" },
	}, err
}

// replayWithout replays all onto onto with no move made.
func replayWithout(dir string, all []moveCommit, onto string, skip map[string]bool) (replayedLine, string, error) {
	from, err := ontoReplay(dir, all, onto, skip)
	if err != nil {
		return replayedLine{}, "", err
	}
	return replayLine(dir, all, nil, from)
}

// carriedPatches is the commits of head a rebase onto onto leaves out of its
// list because onto has their patch. With upstream, git looks for those on
// upstream's side, which has none of the line's.
func carriedPatches(dir, onto, upstream, head string) (map[string]bool, error) {
	carried := map[string]bool{}
	if upstream != "" {
		return carried, nil
	}
	out, err := gitEnv(dir, nil, nil, "cherry", onto, head)
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(out, "\n") {
		if sha, ok := strings.CutPrefix(l, "- "); ok {
			carried[strings.TrimSpace(sha)] = true
		}
	}
	return carried, nil
}

// namedOn is whether name, which finds no commit of the line, names a commit
// onto has: by id, or by whole subject.
func namedOn(dir, onto, name string) (bool, error) {
	if sha, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}"); err == nil {
		_, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", sha, onto)
		return err == nil && code == 0, err
	}
	out, err := gitEnv(dir, nil, nil, "log", "--format=%s", onto, "--")
	if err != nil {
		return false, err
	}
	return slices.Contains(strings.Split(out, "\n"), name), nil
}

// liftedByBase is the first file a lifting rule of cfg claims that onto and
// the line from base to head both changed, "" for none.
func liftedByBase(dir string, cfg *Config, base, onto, head string) (string, error) {
	patterns := cfg.liftPatterns()
	if len(patterns) == 0 {
		return "", nil
	}
	theirs, err := ChangedPaths(dir, base, onto)
	if err != nil {
		return "", err
	}
	ours, err := logWithPaths(dir, base+".."+head)
	if err != nil {
		return "", err
	}
	for _, p := range theirs {
		if GlobMatching(patterns, p) != "" && slices.ContainsFunc(ours, func(c commitPaths) bool { return slices.Contains(c.Paths, p) }) {
			return p, nil
		}
	}
	return "", nil
}

// movedLineTree is the tree a fold with move! commits, which replayed the line
// from base to oldTip onto onto, must end in: the same rebase without them.
// moved is whether the line has any; why is set when it cannot be replayed.
func movedLineTree(dir, base, onto, upstream, oldTip string) (tree string, moved bool, why string, err error) {
	out, err := gitEnv(dir, nil, nil, "rev-list", "--reverse", base+".."+oldTip)
	if err != nil {
		return "", false, "", err
	}
	all, err := readMoveCommits(dir, strings.Fields(out))
	if err != nil || !slices.ContainsFunc(all, func(c moveCommit) bool { return isMove(c.subject) }) {
		return "", false, "", err
	}
	skip, err := carriedPatches(dir, onto, upstream, oldTip)
	if err != nil {
		return "", true, "", err
	}
	got, why, err := replayWithout(dir, all, onto, skip)
	return got.tree, true, why, err
}
