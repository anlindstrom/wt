package synccmd

import (
	"fmt"
	"slices"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// fastForward moves b, which is behind its own remote, up to the remote's
// commit before its rebase. Two refs are pinned first, in one transaction:
// the safety ref at the tip the run found, so undo puts the branch back
// where it stood before both, and the fast-forward ref at the remote's
// commit, where the fast-forward leaves it, which is what lets undo take a
// fast-forwarded branch back from a rebase that was handed over, or from an
// interrupt between the two. It is not the result ref: that one says a run
// finished, and a commit the branch holds only because the run is moving it
// there is not yet anything the branch had. A fast-forward that does not go touches
// nothing: the branch is refused, and what sits on it with it. ok is false
// then. done is what the line adds when the fast-forward is all the run does.
func (r *RunPlan) fastForward(b string, p *participant, done string) (safety wtsync.Safety, ok bool) {
	ctx, w, own := r.ctx, r.w, p.a.Own
	refuse := func(why string) (wtsync.Safety, bool) {
		why = "cannot fast-forward to " + own.Ref + ": " + why
		fmt.Fprintf(w, "  ✗ refused: %s\n", why)
		r.settle("refused", "✗ "+p.work+"  refused: "+why, p.work)
		r.opts.Journal.Set(b, func(jp *syncrun.UpParticipant) {
			jp.Result, jp.Reason = syncrun.ResultRefused, jsonout.Strp(why)
			jp.OwnRemoteSync.SkippedReason = jsonout.Strp(syncrun.OwnSkipFailed)
		})
		r.refuseAbove(b, p.work+" could not be fast-forwarded")
		return wtsync.Safety{}, false
	}
	tip, there, err := ctx.Repo.ResolveRef("refs/heads/" + b)
	if err != nil {
		return refuse("git could not read where the branch is: " + git.Reason(err))
	}
	if !there || tip != own.Local {
		return refuse("the branch moved since it was checked")
	}
	safety = wtsync.Safety{Branch: b, Epoch: r.epoch, Ref: wtsync.SafetyRef(b, r.epoch), Tip: tip}
	if err := wtsync.WriteRun(ctx.Repo.MainRoot, r.epoch, []wtsync.Pin{{Branch: b, Safety: tip, Forward: own.Commit}}); err != nil {
		return refuse(err.Error())
	}
	// An interrupt from here names the worktree and the ref that puts it
	// back, as one during the rebase does.
	r.tracker.Set(&syncrun.RebaseInFlight{Work: p.work, Path: p.wt.Path, Safety: safety.Ref, Forwarding: true})
	if err := syncrun.FastForwardOwn(p.wt.Path, b, own.Commit); err != nil {
		// git moves the branch before it runs a hook, so a merge that
		// reports a failure may still have gone through: where the branch
		// is now decides, not the exit status.
		now, _, nerr := ctx.Repo.ResolveRef("refs/heads/" + b)
		switch {
		case nerr != nil:
			// Where it is cannot be told: the refs stay, since the old tip
			// is under one of them.
			r.tracker.Set(nil)
			return refuse(fmt.Sprintf("%v; git could not read where the branch is now (%s), and its old tip is under %s",
				err, git.Reason(nerr), safety.Ref))
		case now == own.Commit:
			fmt.Fprintf(w, "  note: the fast-forward went through, and git then reported: %v\n", err)
		case now == tip:
			r.tracker.Set(nil)
			if derr := wtsync.DeleteSafety(ctx.Repo.MainRoot, safety); derr != nil {
				fmt.Fprintf(w, "  note: could not remove %s: %v\n", safety.Ref, derr)
			}
			return refuse(err.Error())
		default:
			// Neither where it was nor where it was going: the refs stay,
			// since the old tip is under one of them.
			r.tracker.Set(nil)
			return refuse(fmt.Sprintf("%v; the branch is at %s, and its old tip is under %s", err, git.ShortID(now, 7), safety.Ref))
		}
	}
	p.found = tip
	fmt.Fprintf(w, "  ✓ fast-forwarded to %s  %s → %s (%s)%s%s\n", own.Ref,
		git.ShortID(tip, 7), git.ShortID(own.Commit, 7), kit.Count(own.Behind, "commit"), syncrun.AsLastFetched(own), done)
	// Where the branch stands from here, whatever the rebase comes to: a
	// rebase that is put back says so itself.
	r.opts.Journal.Set(b, func(jp *syncrun.UpParticipant) {
		jp.OwnRemoteSync.FastForwarded, jp.After = true, jsonout.Strp(own.Commit)
	})
	return safety, true
}

// fastForwardOnly is the whole of a run for a branch that is behind its own
// remote and has nothing to rebase once it is at the remote's commit: it is
// moved there, as before a rebase and with the same refs pinned, so undo
// puts it back, and that is all. No deferred step runs and nothing is
// offered to push: the branch is what its remote has. What sits on it in the
// run is rebased onto where it now is.
func (r *RunPlan) fastForwardOnly(b string, p *participant) {
	w, own := r.w, p.a.Own
	why := "; already on " + r.targetName() + ", nothing to rebase"
	if p.a.Class == wtsync.Stale {
		why = "; nothing ahead of " + r.targetName() + ", nothing to rebase"
	}
	safety, ok := r.fastForward(b, p, why)
	if !ok {
		return
	}
	// Nothing is in flight any more: the branch is where the run leaves it.
	r.tracker.Set(nil)
	p.result = &wtsync.Result{Branch: b, OldTip: p.found, NewTip: own.Commit, Safety: safety}
	p.head = own.Commit
	// The run is finished for this branch, here.
	if err := wtsync.WriteResult(r.ctx.Repo.MainRoot, b, own.Commit, r.epoch); err != nil {
		fmt.Fprintf(w, "  note: %v\n", err)
	}
	fmt.Fprintf(w, "  ↩ %s\n", wtsync.WayOut(wtsync.Way{Work: p.work, Result: true, Safety: git.ShortID(p.found, 7)}))
	syncrun.TellIdle(w, append(slices.Clone(p.a.Sessions), p.forced...), wtsync.FastForwardedLine(p.work, own.Ref, own.Behind))
	r.settle("fast-forwarded", "", "")
	r.Forwarded = append(r.Forwarded, p.work)
	r.opts.Journal.SetSync(b, func(jp *syncrun.SyncParticipant) {
		jp.Result = syncrun.ResultFastForwarded
		jp.SafetyRef, jp.UndoCommand = jsonout.Strp(safety.Ref), syncrun.UndoCommand(p.work)
	})
}

// dropFastForward takes back what a fast-forward left behind, once a rebase
// that was put back has taken the branch back to the tip the run found: the
// remote's commit is no longer anything the branch had, in wt's refs or in
// its reflog.
func (r *RunPlan) dropFastForward(b string, p *participant) {
	if p.found == "" {
		return
	}
	if err := wtsync.Disown(r.ctx.Repo.MainRoot, b, r.epoch); err != nil {
		fmt.Fprintf(r.w, "  note: %v\n", err)
	}
}
