package synccmd

import (
	"errors"
	"fmt"

	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// readStack reads the stack held on the named worktree's line, for --stack,
// before anything is fetched further or moved: a line that cannot be one is
// refused here. It lists the layers and what holds each.
func (r *RunPlan) readStack() error {
	if r.opts.Stack == nil {
		return nil
	}
	if why := r.cfg.StackFoldRefusal(); why != "" {
		return errors.New(why)
	}
	b := r.branches[0]
	base := r.trunkSHA
	if r.target != nil {
		base = r.target.cutoff()
	}
	line, err := readLine(r.ctx, b, base, r.opts.Stack.Layers)
	if err != nil {
		return err
	}
	if r.target != nil {
		if why := r.target.onLine(r.ctx, line, r.trunkSHA, r.parts[b].work); why != "" {
			return errors.New(why + "; nothing is rebased")
		}
	}
	if len(line.Layers) == 0 {
		return fmt.Errorf("no layer branch is on the line of %s: nothing for --stack to move; wt sync rebase %s rebases it alone", r.parts[b].work, r.parts[b].work)
	}
	if rev := r.opts.Stack.Edit; rev != "" {
		e, err := editOnLine(r.ctx.Repo.MainRoot, rev, line, r.parts[b].work)
		if err != nil {
			return err
		}
		r.edit = e
	}
	r.line = line
	r.printFoldHeader(r.parts[b].work, line.Base)
	listed, err := r.listAgents()
	if err != nil {
		return err
	}
	holders, err := syncrun.ReadHolders(r.ctx, line, listed)
	if err != nil {
		return err
	}
	s := syncrun.StackOf(line, r.opts.Stack.Autosquash, holders, r.parts[b].wt.Path)
	if !r.opts.Stack.NoSideBranches {
		if r.sides, err = readSides(r.ctx, line, r.trunkSHA, r.target); err != nil {
			return fmt.Errorf("the branches off the line of %s could not be read: %w; --no-side-branches leaves them out", r.parts[b].work, err)
		}
		sideHolders, err := syncrun.ReadSideHolders(r.ctx, r.sides, listed)
		if err != nil {
			return err
		}
		syncrun.SetSides(s, r.sides, sideHolders, aloneSides(r.opts.Alone))
	}
	if r.opts.Stack.Autosquash {
		// Named before anything replays; a warning, so the run goes ahead.
		r.gaps = syncrun.ContextGaps(r.ctx.Repo.MainRoot, line, s)
		r.gapsUnchecked = s.ContextUnchecked
	}
	syncrun.PrintLayers(r.w, s)
	if r.opts.Alone {
		if err := r.layersLeftAlone(b, line); err != nil {
			return err
		}
	}
	r.opts.Journal.SetSync(b, func(jp *syncrun.SyncParticipant) {
		jp.Stack = s
		if r.opts.Stack.Autosquash || r.opts.Steps != nil || r.edit != nil {
			jp.Fold = &syncrun.StackFold{InPlace: r.foldsInPlace(), Base: line.Base}
		}
	})
	return nil
}

// foldHeader is what the header of a fold in place says, apart from the
// line and its base.
type foldHeader struct {
	label, onto, trunkSHA, fetched string
}

// printFoldHeader prints the header of a run that folds work in place on
// its own base, once.
func (r *RunPlan) printFoldHeader(work, base string) {
	h := r.foldHeader
	if h == nil {
		return
	}
	r.foldHeader = nil
	verb := "folds " + work
	switch {
	case r.opts.Stack.Autosquash:
	case r.edit != nil:
		verb = "edits " + work
	default:
		verb = "runs its steps over " + work
	}
	fmt.Fprintf(r.w, "%s  %s in place on its own base %s\n", h.label, verb, git.ShortID(base, 7))
	fmt.Fprintf(r.w, "the declaration and its scripts are read from trunk, %s %s %s\n", h.onto, h.trunkSHA, h.fetched)
	if r.opts.Stack.Autosquash {
		fmt.Fprintln(r.w, "the fold's own stops are not simulated before it runs")
	}
}

// flushFoldHeader prints the plain header in place of a fold's own when the
// run stops before the line is read, so a failure still says what was run.
func (r *RunPlan) flushFoldHeader() {
	if h := r.foldHeader; h != nil {
		r.foldHeader = nil
		fmt.Fprintf(r.w, "%s  onto %s %s %s\n", h.label, h.onto, h.trunkSHA, h.fetched)
	}
}

// foldsInPlace is whether the run folds the line on its own base: with
// --autosquash and no --onto, with wt sync steps --stack, and with --edit.
// A run that folds the line does not rebase it onto trunk, which is only
// what it is assessed against.
func (r *RunPlan) foldsInPlace() bool {
	st := r.opts.Stack
	return st != nil && ((st.Autosquash && r.opts.Onto == "") || r.opts.Steps != nil || st.Edit != "")
}

// lineMoved is why the line or a layer is no longer what readStack read,
// or the worktree at path is no longer on the line's branch at its tip; ""
// when everything is as read.
func (r *RunPlan) lineMoved(b, path string) string {
	main := r.ctx.Repo.MainRoot
	// The rebase moves whatever the worktree has checked out: it must still
	// be the line's branch, at the line's tip.
	if ref, err := git.Run(path, "symbolic-ref", "--quiet", "HEAD"); err != nil || ref != "refs/heads/"+b {
		return fmt.Sprintf("the worktree is no longer on %s (HEAD is %q); nothing is rebased: run it again", b, ref)
	}
	if head, err := git.Run(path, "rev-parse", "--verify", "HEAD"); err != nil || head != r.line.Head {
		return fmt.Sprintf("the line moved since it was read (%s then, %s now); nothing is rebased: run it again", git.ShortID(r.line.Head, 7), git.ShortID(head, 7))
	}
	if tip, err := git.Run(main, "rev-parse", "--verify", "refs/heads/"+b); err != nil || tip != r.line.Head {
		return fmt.Sprintf("the line moved since it was read (%s then, %s now); nothing is rebased: run it again", git.ShortID(r.line.Head, 7), git.ShortID(tip, 7))
	}
	for _, l := range r.line.Layers {
		if tip, err := git.Run(main, "rev-parse", "--verify", "refs/heads/"+l.Branch); err != nil || tip != l.Tip {
			return fmt.Sprintf("layer %s moved since it was read (%s then, %s now); nothing is rebased: run it again", l.Branch, git.ShortID(l.Tip, 7), git.ShortID(tip, 7))
		}
	}
	return ""
}

// putBackStack puts back a --stack rebase that stopped at a stop a person
// owns when its plan could not be recorded: a handover with no plan would
// leave a resume nothing to check the layers against.
func (r *RunPlan) putBackStack(b string, p *participant, res wtsync.Result, why string) {
	main := r.ctx.Repo.MainRoot
	_, aerr := git.Run(p.wt.Path, "rebase", "--abort")
	busy, _ := wtsync.RebaseInProgress(p.wt.Path)
	head, _ := git.Run(p.wt.Path, "rev-parse", "HEAD")
	_ = wtsync.DropStackMap(main, res.Layers)
	ferr := wtsync.ForgetStack(main, r.epoch)
	if aerr != nil || busy || head != res.OldTip {
		r.rebaseFailed(b, p, res, fmt.Errorf("%s, and the rebase could not be put back", why))
		return
	}
	restored := why + "; the rebase is put back"
	if ferr != nil {
		// The safety ref stays: it is how undo finds this run, and with it
		// the record to remove.
		restored += fmt.Sprintf("; the stack's record could not be removed (%v): wt sync undo %s removes it", ferr, p.work)
	} else if err := wtsync.DeleteSafety(main, res.Safety); err != nil {
		fmt.Fprintf(r.w, "  note: %v\n", err)
	}
	fmt.Fprintf(r.w, "  ✗ restored: %s\n", restored)
	r.opts.Journal.SetSync(b, func(jp *syncrun.SyncParticipant) {
		jp.Result, jp.Reason, jp.After = syncrun.ResultRestored, jsonout.Strp(restored), jsonout.Strp(res.OldTip)
	})
	r.settle("restored", "✗ "+p.work+"  "+restored, p.work+" (restored)")
}

// stackRecord writes the record of a --stack run before its rebase starts,
// so a resume or an undo knows the line and its layers however the run
// ends; nil for any other run. A record that cannot be written refuses the
// run.
func (r *RunPlan) stackRecord(b string, p *participant, req wtsync.Request) (*wtsync.StackRecord, error) {
	st := r.opts.Stack
	if st == nil {
		return nil, nil
	}
	rec := &wtsync.StackRecord{Epoch: r.epoch, Work: p.work, Branch: b, Path: p.wt.Path, Onto: req.Onto, OntoRef: r.ontoRef(), Upstream: req.Upstream,
		Autosquash: st.Autosquash, InPlace: r.opts.Steps != nil, Force: r.opts.Force, Line: r.line, Edit: r.edit, Sides: r.sides}
	if err := wtsync.WriteStackRecord(r.ctx.Repo.MainRoot, rec); err != nil {
		return rec, err
	}
	if r.opts.Alone {
		// A resume of this run's handover must move no layer either.
		if err := syncrun.MarkAlone(r.ctx.Repo.MainRoot, r.epoch); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

// finishStack checks and moves the layers once the line's rebase has
// finished, and records what came of them. restored says an edit run's line
// was put back at the tip the run found.
func (r *RunPlan) finishStack(b string, p *participant, res wtsync.Result) (restored bool) {
	st := r.opts.Stack
	if st == nil {
		return
	}
	if r.stackBroken != "" {
		fmt.Fprintf(r.w, "  ✗ no layer moved: %s; the line is rebased, wt sync undo %s puts it back\n", r.stackBroken, p.work)
		if err := wtsync.DropStackMap(r.ctx.Repo.MainRoot, res.Layers); err != nil {
			fmt.Fprintf(r.w, "  note: %v\n", err)
		}
		if err := wtsync.ForgetStack(r.ctx.Repo.MainRoot, r.epoch); err != nil {
			fmt.Fprintf(r.w, "  ✗ the stack's record could not be removed (%v): wt sync undo %s removes it\n", err, p.work)
		}
		r.failures = append(r.failures, p.work+" (no layer moved)")
		return
	}
	rec, ok, err := wtsync.ReadStackRecord(r.ctx.Repo.MainRoot, r.epoch)
	if err != nil || !ok {
		why := "the stack's record is gone"
		if err != nil {
			why = err.Error()
		}
		fmt.Fprintf(r.w, "  ✗ no layer moved: %s\n", why)
		r.failures = append(r.failures, p.work+" (no layer moved)")
		return
	}
	if res.Edit != nil {
		rec.Edit = res.Edit
	}
	if st.BeforeCheck != nil {
		st.BeforeCheck(rec)
	}
	// Listed again: a session may have started, or turned busy, in a
	// layer's worktree while the line was rebased. One that cannot be
	// listed holds every layer in a worktree.
	listed, lerr := r.opts.SessionsAgain(r.ctx, syncrun.RebasedNothing)
	s, failure, ferr := syncrun.FinishStack(r.ctx, r.w, syncrun.StackFinishFor(rec, res, r.cfg, r.opts.Clock(), listed, lerr, syncrun.StackFinishCaller{
		OldTip: res.OldTip, Own: r.own, Stopped: wtsync.StopPaths(res.Stops), Confirmed: res.Replay != nil && res.Replay.Confirmed,
		Gaps: r.gaps, Unchecked: r.gapsUnchecked, AfterMove: st.AfterMove, Between: st.Between}))
	r.opts.Journal.SetSync(b, func(jp *syncrun.SyncParticipant) { jp.Stack = s; syncrun.SetFold(jp, s) })
	restored = s != nil && s.Restored
	if ferr != nil {
		fmt.Fprintf(r.w, "  ✗ the layer moves stopped: %v; wt sync undo %s puts back the line and every layer moved\n", ferr, p.work)
		r.failures = append(r.failures, p.work+" (layer moves stopped)")
		return
	}
	if failure != "" {
		r.failures = append(r.failures, p.work+" ("+failure+")")
	}
	return restored
}
