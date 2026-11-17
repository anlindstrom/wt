package synccmd

import (
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// looseOf reads the branches in no worktree on the line of branch that a run
// moves with it: above its stack parent's tip, or above base when it has
// none. What it cannot read it does not list.
func looseOf(ctx *kit.Context, worktrees repo.Worktrees, base string, parents map[string]string, branch string) wtsync.LooseLine {
	if parent, ok := parents[branch]; ok {
		if tip, err := git.Run(ctx.Repo.MainRoot, "rev-parse", "--verify", "refs/heads/"+parent); err == nil {
			base = tip
		}
	}
	if base == "" {
		return wtsync.LooseLine{}
	}
	users := worktrees.Users()
	line, err := wtsync.ReadLoose(ctx.Repo.MainRoot, branch, base, ctx.Config.MainBranch, ctx.UserConfig().RefSweepPatterns, func(name string) bool { _, ok := users[name]; return ok })
	if err != nil {
		return wtsync.LooseLine{}
	}
	return line
}
