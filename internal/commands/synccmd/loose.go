package synccmd

import (
	"cmp"
	"fmt"

	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// readLoose reads, before the rebase, the branches in no worktree on the
// line it replays: what wt up moves with it. A run of a stack held on one
// line, in place or onto a target already reads its own layers. With --alone
// it moves none of them and names them as left alone.
func (r *RunPlan) readLoose(b string, p *participant, req wtsync.Request) error {
	if r.opts.Stack != nil || r.opts.Steps != nil {
		return nil
	}
	base := cmp.Or(req.Upstream, req.Onto)
	worktrees, err := r.ctx.Repo.Worktrees()
	if err != nil {
		return err
	}
	users := worktrees.Users()
	line, err := wtsync.ReadLoose(r.ctx.Repo.MainRoot, b, base, r.ctx.Config.MainBranch, r.ctx.UserConfig().BackupPatterns(), func(name string) bool { _, ok := users[name]; return ok })
	if err != nil {
		return err
	}
	if r.opts.Alone {
		// Read, never planned: nothing but the named branch moves.
		r.looseLeftAlone(worktrees, line)
		return nil
	}
	if len(line.Layers) > 0 {
		p.loose, p.looseOnto = &line, req.Onto
	}
	return nil
}

// moveLoose moves the branches readLoose found to the replay of their
// commits, tells what moved and what stayed, and records both.
func (r *RunPlan) moveLoose(b string, p *participant, res wtsync.Result) {
	if p.loose == nil {
		return
	}
	if err := p.loose.Move(r.ctx.Repo.MainRoot, r.epoch, p.looseOnto, res.NewTip); err != nil {
		p.loose.LeaveAll("it could not be moved: " + err.Error())
	}
	r.reportLoose(b, p)
}

// leaveLoose names the branches readLoose found as left, for why.
func (r *RunPlan) leaveLoose(b string, p *participant, why string) {
	if p.loose == nil {
		return
	}
	p.loose.LeaveAll(why)
	r.reportLoose(b, p)
}

func (r *RunPlan) reportLoose(b string, p *participant) {
	for _, x := range p.loose.Layers {
		switch {
		case x.New != "":
			fmt.Fprintf(r.w, "  ↳ moved %s  %s → %s\n", x.Branch, git.ShortID(x.Old, 7), git.ShortID(x.New, 7))
		case x.Left != "":
			fmt.Fprintf(r.w, "  ⚠ left %s at %s: %s\n", x.Branch, git.ShortID(x.Old, 7), x.Left)
		}
	}
	layers := syncrun.LayerBranchesOf(p.loose)
	r.opts.Journal.Set(b, func(jp *syncrun.UpParticipant) { jp.Layers = layers })
}

// layerNames is, for each branch of the run, the names of the branches in no
// worktree on its line: what wt status lists for it, which its token covers.
func (r *RunPlan) layerNames() map[string][]string {
	out := map[string][]string{}
	all, err := r.ctx.Repo.Worktrees()
	if err != nil {
		return out
	}
	for _, b := range r.branches {
		for _, l := range looseOf(r.ctx, all, r.trunkSHA, r.parents, b).Layers {
			out[b] = append(out[b], l.Branch)
		}
	}
	return out
}
