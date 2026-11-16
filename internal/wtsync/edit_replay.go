package wtsync

import (
	"slices"
	"strings"
)

// ReplayOnto simulates, with plumbing alone, replaying the old line's
// commits above edited, up to oldTip, onto amended, commit by commit as the
// rebase does: each a three-way merge from its own parent. It returns the
// tree the replay ends at and every path any of those merges conflicted on,
// where that tree's content is not a merge and holds nothing. No ref, index
// or worktree is touched; the commits it writes are unreferenced objects.
func ReplayOnto(mainRoot, edited, amended, oldTip string) (tree string, conflicted []string, err error) {
	out, err := gitEnv(mainRoot, nil, nil, "rev-list", "--reverse", "--first-parent", edited+".."+oldTip)
	if err != nil {
		return "", nil, err
	}
	env := []string{"GIT_AUTHOR_NAME=wt sync", "GIT_AUTHOR_EMAIL=wt-sync@localhost",
		"GIT_COMMITTER_NAME=wt sync", "GIT_COMMITTER_EMAIL=wt-sync@localhost"}
	cur := amended
	for _, c := range strings.Fields(out) {
		merged, clean, conflicts, _, err := mergeTree(mainRoot, c+"^", cur, c)
		if err != nil {
			return "", nil, err
		}
		if !clean {
			for _, cf := range conflicts {
				if !slices.Contains(conflicted, cf.Path) {
					conflicted = append(conflicted, cf.Path)
				}
			}
		}
		if cur, err = gitEnv(mainRoot, env, nil, "commit-tree", merged, "-p", cur, "-m", "wt sync: simulated replay"); err != nil {
			return "", nil, err
		}
	}
	tree, err = gitEnv(mainRoot, nil, nil, "rev-parse", cur+"^{tree}")
	return tree, conflicted, err
}
