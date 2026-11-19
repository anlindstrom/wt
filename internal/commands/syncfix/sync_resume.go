// Package syncfix implements wt sync resume and wt sync undo: finishing a run
// a person took over, and putting back what a run did.
package syncfix

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// ResumeOptions tunes SyncResume for callers and tests. Resume asks only when
// idle sessions are in the worktree.
type ResumeOptions struct {
	// Journal records what the resume does, for --json; nil records
	// nothing.
	Journal *syncrun.RunJournal
	// Adopt takes over a rebase finished by hand with commits added on top,
	// when the branch's reflog proves that shape: --adopt.
	Adopt bool
	// AllowDropped goes on past a resolution that drops lines of a fixup
	// moved ahead of its context: --allow-dropped.
	AllowDropped bool
	syncrun.VerbOptions
	syncrun.PushOptions
	syncrun.StepOptions
}

// SyncResume continues the rebase a run left at a stop a person owned. It
// verifies the worktree is still what the run left, then drives the same
// loop to the end: the strategies at any later stop, the deferred steps, the
// result ref, the push. A later stop a person owns is handed over again,
// with a fresh plan file.
//
// A rebase somebody finished themselves with `git rebase --continue` is
// tolerated: there is nothing to continue, so only what follows the rebase
// runs — after checking that it really finished rather than being aborted.
// Nothing here ever resets the worktree: a person's own resolution is in it.
//
// Every refusal comes before the lock is taken over. Taking it and then
// refusing would release the lock the handover kept, so a refusal would not
// change nothing after all.
func SyncResume(ctx *kit.Context, work string, opts ResumeOptions, w io.Writer) error {
	return syncrun.Journaled(opts.Journal, func() error { return syncResume(ctx, work, opts, w) })
}

func syncResume(ctx *kit.Context, work string, opts ResumeOptions, w io.Writer) (err error) {
	r := &resume{ctx: ctx, opts: opts, w: w, j: opts.Journal, tracker: &syncrun.RebaseTracker{}}
	defer syncrun.WatchSignals(w, r.tracker)()

	if err := r.readHandover(work); err != nil {
		return err
	}
	r.joinJournal()
	// Whatever returns an error before the rebase is touched refused it;
	// every other end says what it came to on the way out.
	defer func() { r.recordEnd(err) }()
	if err := r.checkSafety(); err != nil {
		return err
	}
	if ok, err := r.askSessions(); err != nil || !ok {
		return err
	}
	if err := r.verify(); err != nil {
		return err
	}
	if err := r.checkStack(); err != nil {
		return err
	}
	if err := r.checkDropped(); err != nil {
		return err
	}
	// Released however the resume ends, the refusal after the take-over
	// included, unless a fresh handover keeps it.
	defer func() {
		if r.lock != nil {
			_ = r.lock.Release()
		}
	}()
	if err := r.takeLock(); err != nil {
		return err
	}
	res, err := r.rebase()
	if err != nil {
		return err
	}
	if res.Left != nil {
		return r.handOverAgain(res)
	}
	if err := r.complete(res); err != nil {
		return err
	}
	return r.push()
}

// resume is one wt sync resume under way: the handover it continues, and
// what each step learns for the steps after it.
type resume struct {
	ctx     *kit.Context
	opts    ResumeOptions
	w       io.Writer
	j       *syncrun.RunJournal
	tracker *syncrun.RebaseTracker

	target repo.Worktree
	gitDir string
	st     wtsync.State
	// name is the work as the run recorded it.
	name string
	// confirmed is whether this resume began with the rebase still at the
	// handover's stop, on a run no earlier leg of which was advanced by hand.
	confirmed bool
	// restored is set once the layers are settled when an edit run's line
	// was put back at the tip the run found.
	restored bool
	// pinned is the tip the safety ref pins.
	pinned   string
	sessions agents.Sessions
	// landed is how many commits trunk gained since the run, counted for
	// an idle session's notice.
	landed int
	cfg    *wtsync.Config
	// busy is the rebase still in progress, rather than finished by hand.
	busy bool
	// lock is the handover's lock, taken over; nil once a fresh handover
	// keeps it.
	lock *wtsync.Lock
	// unavailable is the needs that did not answer, in a resume told to go
	// on with --allow-owed: the steps that need one are owed.
	unavailable map[string]string
	// adopt is the rebase finished by hand that --adopt takes over, and
	// adoptTips the layers' new tips, read from the run's markers and
	// checked before anything moves; nil without --adopt.
	adopt     *wtsync.Adoption
	adoptTips []wtsync.LayerTip
	// probed is every need probed before anything moved.
	probed map[string]bool
	// gaps is, for a fold, the hunks of its fixups that lack their context
	// behind their targets.
	gaps []wtsync.ContextGap
	// unchecked is what of the fold's fixups could not be analysed.
	unchecked []string
}

// joinJournal records the run's repository and trunk, and the branch as
// resume finds it: the run's old tip while the rebase waits, the rebased
// tip when a person finished it by hand.
func (r *resume) joinJournal() {
	st := r.st
	r.j.Repo(r.ctx.Repo.MainRoot)
	r.j.Trunk(strings.TrimPrefix(st.TrunkRef, "origin/"), nil, st.TrunkRef, st.Trunk, false)
	if st.OntoRef != "" {
		t := &syncrun.RunTarget{Ref: st.OntoRef, Upstream: jsonout.Strp(st.Upstream)}
		r.j.Target(t, st.Onto)
	} else {
		r.j.Target(&syncrun.RunTarget{Ref: st.TrunkRef, Trunk: true}, st.Trunk)
	}
	// The journal's record: a tip git cannot read is recorded as none.
	found, _, _ := r.ctx.Repo.ResolveRef("refs/heads/" + st.Branch)
	r.j.Join(r.name, st.Branch, r.target.Path, found, nil)
	// The fold as the stack's record has it, so a resume that stops again,
	// is refused or is interrupted still says what the line is folded on.
	var fold *syncrun.StackFold
	if rec, ok, err := wtsync.ReadStackRecord(r.ctx.Repo.MainRoot, st.Epoch); err == nil && ok {
		fold = syncrun.FoldOf(rec)
	}
	r.j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) {
		p.Fold = fold
		p.SafetyRef, p.PlanFile = jsonout.Strp(st.Safety), syncrun.PlanFileOf(r.target.Path)
		// The run's own mode, also for a resume refused or interrupted
		// before its rebase says it.
		p.EachCommit, _ = wtsync.EachCommitMode(st.EachCommit)
	})
}

// recordEnd records where the branch is as the resume ends, and a resume
// that ended in err before anything else was recorded as refused.
func (r *resume) recordEnd(err error) {
	after, _, _ := r.ctx.Repo.ResolveRef("refs/heads/" + r.st.Branch) // recorded as none when unreadable
	r.j.SetSync(r.st.Branch, func(p *syncrun.SyncParticipant) {
		p.After = jsonout.Strp(after)
		if p.Result == syncrun.ResultNotRun && err != nil {
			p.Result, p.Reason = syncrun.ResultRefused, jsonout.Strp(err.Error())
		}
	})
}

// readHandover finds work's worktree and the handover the run left in it.
func (r *resume) readHandover(work string) error {
	target, err := kit.LocateBranch(r.ctx, work)
	if err != nil {
		return err
	}
	r.target = target
	if r.gitDir, err = wtsync.GitDir(target.Path); err != nil {
		return err
	}
	st, ok, err := wtsync.ReadState(r.gitDir)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s was not left mid-rebase by wt sync rebase: nothing to resume", work)
	}
	if st.Branch != target.Branch {
		return fmt.Errorf("the handover in %s is for %s, not %s; nothing is resumed", r.gitDir, st.Branch, target.Branch)
	}
	// One name from here on: the one the run recorded, so every message and
	// a fresh handover say what the run said, whichever form was typed. An
	// old sidecar without one falls back to the argument, never the branch.
	st.Work = cmp.Or(st.Work, work)
	r.st, r.name = st, st.Work
	return nil
}

// checkSafety checks the handover's safety ref is the run's and pins the
// tip the handover says.
func (r *resume) checkSafety() error {
	st := r.st
	// The sidecar must describe the run it claims to: a safety ref built
	// from a different branch or epoch is somebody else's, and pinning the
	// wrong old tip would make undo restore to the wrong commit.
	if want := wtsync.SafetyRef(st.Branch, st.Epoch); st.Safety != want {
		return fmt.Errorf("the handover names %s, not %s; nothing is resumed", st.Safety, want)
	}
	tip, err := git.Run(r.ctx.Repo.MainRoot, "rev-parse", "--verify", st.Safety)
	if err != nil {
		return fmt.Errorf("the safety ref %s is gone; nothing is resumed", st.Safety)
	}
	// What the safety ref pins: the tip the rebase started from, or the one
	// the run found before it fast-forwarded the branch to its own remote.
	r.pinned = cmp.Or(st.Found, st.OldTip)
	if tip != r.pinned {
		return fmt.Errorf("%s pins %s but the handover says %s; nothing is resumed", st.Safety, git.ShortID(tip, 7), git.ShortID(r.pinned, 7))
	}
	return nil
}

// askSessions refuses a busy session in the worktree and asks about an idle
// one. ok is false when the answer was no, which the journal records.
func (r *resume) askSessions() (ok bool, err error) {
	ctx, st, name := r.ctx, r.st, r.name
	listed, err := r.opts.SessionsNow(ctx, syncrun.ResumedNothing)
	if err != nil {
		return false, err
	}
	r.sessions = agents.SessionsAt(listed, r.target.Path)
	if len(r.sessions.Busy()) > 0 {
		return false, fmt.Errorf("an agent session is busy in %s: %s; nothing is resumed", name, r.sessions.Label(kit.SessionLabel))
	}
	// An idle session is named and asked about before the handover is
	// verified: a person can go on editing while the question waits, so the
	// verification has to see what they left after answering.
	if len(r.sessions) == 0 {
		return true, nil
	}
	landedOn := st.Trunk
	if st.OntoRef != "" {
		landedOn = st.Onto
	}
	behind, _, err := wtsync.BehindAhead(ctx.Repo.MainRoot, landedOn, st.OldTip)
	if err != nil {
		return false, fmt.Errorf("%s: counting what landed: %w; nothing is resumed", name, err)
	}
	r.landed = behind
	fmt.Fprintln(r.w, syncrun.IdleNotice(name, r.sessions))
	told := []syncrun.Idle{{Label: name, Path: r.target.Path, Sessions: r.sessions}}
	ok, _, err = syncrun.AskIdle(ctx, r.w, r.opts.VerbOptions, []string{name}, told, listed, syncrun.ResumedNothing)
	if err != nil {
		return false, err
	}
	if !ok {
		r.j.Set(st.Branch, func(p *syncrun.UpParticipant) {
			p.Result, p.Reason = syncrun.ResultRefused, jsonout.Strp("not confirmed: "+syncrun.ResumedNothing.Line)
		})
		r.j.FailWith(errors.New("not confirmed: " + syncrun.ResumedNothing.Line))
	}
	return ok, nil
}

// verify loads the run's declaration and checks the worktree is what the
// run left: the rebase it handed over, or that rebase finished by hand.
func (r *resume) verify() error {
	st, name := r.st, r.name
	// The run's trunk, not today's: a resume that read a newer declaration
	// would apply strategies the stopped rebase was never planned with.
	cfg, err := wtsync.LoadFromRef(r.ctx.Repo.MainRoot, st.Trunk)
	if err != nil {
		return err
	}
	r.cfg = cfg
	if r.busy, err = wtsync.RebaseInProgress(r.target.Path); err != nil {
		return err
	}
	if r.busy && r.opts.Adopt {
		return fmt.Errorf("%s: --adopt takes over a rebase finished by hand, and this one is still in progress; nothing is resumed: wt sync resume %s goes on with it", name, name)
	}
	if r.opts.Adopt {
		// Proved first: which checks the adopt runs, and so which needs are
		// probed, follows from the layers' tips and the adopted commits.
		a, tips, err := r.proveAdopt()
		if err != nil {
			return fmt.Errorf("%s: %w; nothing is resumed", name, err)
		}
		r.adopt, r.adoptTips = &a, tips
	}
	if err := r.probeNeeds(); err != nil {
		return err
	}
	if r.busy {
		if err := wtsync.VerifyLeft(r.target.Path, st); err != nil {
			return fmt.Errorf("%s: %w; nothing is resumed", name, err)
		}
		return verifyHandover(r.target.Path, st, r.w)
	}
	if a := r.adopt; a != nil {
		fmt.Fprintf(r.w, "%s  the rebase was finished by hand at %s, with %s made after it; adopting it\n", name, git.ShortID(a.Finish, 7), kit.Count(len(a.Commits), "commit"))
		if len(st.Resolved)+len(st.Deleted) > 0 {
			noteNotRechecked(r.w, fmt.Sprintf("it was finished by hand from the %d/%d the plan describes", st.Stop, st.Total), st)
		}
		return nil
	}
	if err := wtsync.VerifyFinished(r.target.Path, st.Branch, name, st.Onto, st.OldTip, st.Total); err != nil {
		if _, _, perr := r.proveAdopt(); perr == nil {
			// The one shape --adopt can prove: said, and still refused
			// here, since a plain resume cannot tell whose the commits are.
			return fmt.Errorf("%s: %w; nothing is resumed. It was finished by hand with commits made on top, which wt sync resume --adopt %s takes over", name, err, name)
		}
		return fmt.Errorf("%s: %w; nothing is resumed", name, err)
	}
	fmt.Fprintf(r.w, "%s  the rebase is already finished; running what is left\n", name)
	if len(st.Resolved)+len(st.Deleted) > 0 {
		noteNotRechecked(r.w, fmt.Sprintf("it was finished by hand from the %d/%d the plan describes", st.Stop, st.Total), st)
	}
	return nil
}

// probeNeeds probes the needs of the steps the rest of the rebase may run,
// before anything moves, as a run does: a resume whose step needs one that
// does not answer is refused, unless told to go on, when the step is owed.
func (r *resume) probeNeeds() error {
	st := r.st
	base := cmp.Or(st.Upstream, st.Onto)
	due, err := wtsync.StepsDue(r.target.Path, r.cfg, base, st.OldTip, st.Onto, st.Upstream)
	if a := r.adopt; a != nil && err == nil {
		// What the adopt runs: the checks at the tips, which the adopted
		// commits may call for, and the deferred steps over the line as it
		// is now.
		var tips []wtsync.Tip
		if st.EachCommit != wtsync.EachOff {
			tips = r.tipsOf(a.Head)
		}
		due, err = wtsync.AdoptDue(r.target.Path, r.cfg, tips, r.pinned, a.Head)
	}
	if err != nil {
		return fmt.Errorf("%s: could not tell which steps the rest of the rebase runs: %w; nothing is resumed", r.name, err)
	}
	if (!r.busy && r.adopt == nil) || st.EachCommit == wtsync.EachOff {
		// Finished by hand, or a run whose checks are off: only the deferred
		// steps are left. An adopt checks the tips.
		due.Checks, due.CheckCommits = nil, nil
	}
	r.probed = map[string]bool{}
	for _, n := range due.Needs() {
		r.probed[n] = true
	}
	unavailable, refusal := syncrun.NeedsFor(r.opts.Prober(), due, r.opts.AllowOwed)
	if refusal != "" {
		return fmt.Errorf("%s: %s; nothing is resumed", r.name, refusal)
	}
	r.unavailable = unavailable
	if line := syncrun.AllowedOwedLine(unavailable); line != "" {
		fmt.Fprintf(r.w, "  %s\n", line)
	}
	return nil
}

// takeLock takes over the lock the handover kept, and checks the handover
// did not change while the checks ran without it.
func (r *resume) takeLock() error {
	lock, err := wtsync.TakeOver(r.gitDir, r.opts.Clock(), r.st.Lock)
	if err != nil {
		return fmt.Errorf("%s: %w; nothing is resumed", r.name, err)
	}
	r.lock = lock
	// The checks ran without the lock. Another wt that ended this run in
	// between had to hold it to do so, so with the lock in hand the handover
	// is either still the one they read or gone.
	if cur, ok, err := wtsync.ReadState(r.gitDir); err != nil {
		return err
	} else if !ok || cur.Epoch != r.st.Epoch {
		return fmt.Errorf("the handover in %s changed while resume was checking it; nothing is resumed", r.name)
	}
	return nil
}

// rebase drives the rebase on from where the handover left it.
func (r *resume) rebase() (wtsync.Result, error) {
	st, name := r.st, r.name
	// Stacked is deliberately not set. It exists so a run does not leave a
	// branch its children are about to be rebased onto waiting on a person;
	// a resume rebases exactly one worktree, so there is no child in flight
	// to strand, and a later unclaimed stop is better answered with a fresh
	// plan file than with a refusal whose only remedy is undo.
	req := wtsync.Request{
		Path: r.target.Path, Branch: st.Branch, Trunk: st.Trunk,
		Onto: st.Onto, Upstream: st.Upstream, Epoch: st.Epoch, Work: name, Total: st.Total,
		Unavailable: r.unavailable, HandedAt: st.Stop, HandedHead: st.Head,
	}
	req.UncheckedBefore = st.Unchecked
	// The run's own mode, whatever the flags of this resume (it has none).
	req.EachCommit = st.EachCommit
	if st.Edit != nil {
		// A --stack --edit run goes on in place, its content held at the
		// end of the list to the old line with the edit merged in.
		req.Edit = st.Edit
		req.Content = syncrun.EditContent(r.ctx.Repo.MainRoot, st.OldTip, syncrun.FoldPaths(r.cfg), st.Stopped)
	}
	// What the report of the commits the rebase emptied needs of the run:
	// its stack and its plan, which every stack run records, per-layer steps
	// or not. No record is no stack, so no fold; a record without a plan
	// (an older one) is a plan nobody knows when it folded.
	rec, hasRec, err := wtsync.ReadStackRecord(r.ctx.Repo.MainRoot, st.Epoch)
	if err != nil {
		return wtsync.Result{}, fmt.Errorf("%s: the stack's record: %w", name, err)
	}
	req.ReportPlan = &wtsync.ReplayPlan{Folds: []string{}}
	if hasRec {
		req.ReportLine, req.ReportPlan = &rec.Line, rec.Replay
		if rec.Replay == nil && !rec.Autosquash {
			req.ReportPlan = &wtsync.ReplayPlan{Folds: []string{}}
		}
	}
	if hasRec && rec.Autosquash {
		// A later stop at a fixup ahead of its context says so too, and
		// the report keeps them.
		if r.gaps, r.unchecked, err = syncrun.ContextGapsOf(r.ctx.Repo.MainRoot, rec.Line.Commits); err != nil {
			r.unchecked = []string{"which fixups lack their context could not be read: " + err.Error()}
			fmt.Fprintf(r.w, "  note: %s\n", r.unchecked[0])
		}
		req.Gaps = r.gaps
	}
	// A run with per-layer steps goes on as it was: the same layers, the
	// same steps, and what its stops did before the handover.
	if l := st.Layers; l != nil {
		req.Resumed, req.Steps, req.ForceSteps = l, l.StepSelection(), l.ForceSteps
		if hasRec {
			req.Stack = &wtsync.StackRequest{Line: rec.Line, Autosquash: rec.Autosquash}
		}
	}
	r.confirmed = r.adopt == nil && !st.ByHand && wtsync.AtHandoverStop(r.target.Path, st)
	safety := wtsync.Safety{Branch: st.Branch, Epoch: st.Epoch, Ref: st.Safety, Tip: r.pinned}
	if r.busy {
		fmt.Fprintf(r.w, "%s  %s  resuming at %d/%d\n", name, st.Branch, st.Stop, st.Total)
	}
	// Resuming, not rebasing: an interrupt must point back at resume, since
	// the abort that is right for a run would discard a person's resolution.
	res, rerr := syncrun.TrackedRebase(r.tracker, &syncrun.RebaseInFlight{Work: name, Path: r.target.Path, Safety: st.Safety, Resuming: true}, func() (wtsync.Result, error) {
		if r.adopt != nil {
			return wtsync.Adopt(r.ctx.Repo.MainRoot, r.cfg, req, st.OldTip, safety, *r.adopt, r.w)
		}
		return wtsync.Resume(r.ctx.Repo.MainRoot, r.cfg, req, st.OldTip, safety, r.w)
	})
	r.j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) {
		p.Checked, p.CheckFailed, p.Rechecked = res.Checked, syncrun.CheckFailedOf(res.CheckFailed), res.Rechecked
		p.EachCommit = cmp.Or(res.EachCommit, p.EachCommit)
	})
	if rerr != nil && res.Restored {
		// An edit run whose content was refused: the rebase is put back,
		// with what it came to kept under a ref the error names.
		fmt.Fprintf(r.w, "  ✗ refused: %v\n", rerr)
		r.j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) {
			p.Result, p.Reason, p.SafetyRef = syncrun.ResultRestored, jsonout.Strp(rerr.Error()), jsonout.Strp(st.Safety)
			p.After, p.PlanFile = jsonout.Strp(st.OldTip), nil
		})
		return res, fmt.Errorf("not completed: %s (refused, nothing moved)", name)
	}
	if rerr != nil {
		recordResumeFailed(r.j, st, r.target.Path, rerr)
		return res, fmt.Errorf("%s: %w", name, rerr)
	}
	if res.Adopted != nil {
		// On record from the moment the tip is kept, whatever stops the
		// resume after.
		r.j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) { p.Adopted = syncrun.AdoptedOf(res.Adopted) })
	}
	return res, nil
}

// handOverAgain hands a rebase that stopped again at a stop a person owns
// back to them, with a fresh plan file, keeping the lock.
func (r *resume) handOverAgain(res wtsync.Result) error {
	st, name, w := r.st, r.name, r.w
	fmt.Fprintf(w, "  ⚠ stopped again at %d/%d\n", res.Left.Index, res.Left.Total)
	herr := syncrun.HandOver(r.ctx, w, syncrun.HandoverInput{
		Work: name, Branch: st.Branch, Path: r.target.Path, TrunkRef: st.TrunkRef, TrunkSHA: st.Trunk, OntoRef: st.OntoRef,
		Onto: st.Onto, Upstream: st.Upstream, Epoch: st.Epoch, Cfg: r.cfg, Res: res, Lock: r.lock,
		Found: st.Found, Earlier: st.Stopped, Tracker: r.tracker, FollowUps: st.FollowUps, ByHand: st.ByHand || !r.confirmed,
	})
	if herr != nil {
		fmt.Fprintf(w, "  ✗ failed: %v\n", herr)
		// As in run: a brief and a sidecar that may now describe different
		// stops both go, and the person is told where the worktree is. The
		// branch ref has not moved since the run, so the abort is the whole
		// of putting it back; undo refuses a mid-rebase worktree.
		syncrun.ClearHandover(w, r.target.Path)
		way := wtsync.WayOut(wtsync.Way{Work: name, Path: r.target.Path, Rebasing: true})
		fmt.Fprintf(w, "  ⚠ %s is left mid-rebase with no plan: %s\n", name, way)
		r.j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) {
			p.Result, p.Reason, p.Recovery, p.PlanFile = syncrun.ResultNeedsRecovery, jsonout.Strp(herr.Error()), jsonout.Strp(way), nil
		})
		return fmt.Errorf("not completed: %s (failed)", name)
	}
	r.lock = nil // kept on purpose
	r.j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) {
		p.Result, p.Reason = syncrun.ResultHandedOver, jsonout.Strp("stopped again at a conflict that is yours")
		if c := res.Left.Check; c != nil {
			p.Reason = jsonout.Strp("stopped again at a commit whose check failed: " + c.Line())
		}
		if res.Left.Edit {
			p.Reason = jsonout.Strp(syncrun.EditReason(res.Left))
		}
		p.Recovery = jsonout.Strp(wtsync.WayOut(wtsync.Way{Work: name, Plan: true, Rebasing: true, OwesAdd: true}))
		p.UndoCommand = syncrun.UndoCommand(name)
	})
	return fmt.Errorf("not completed: %s (needs you)", name)
}

// complete runs what follows a finished rebase, as a run does: the deferred
// steps, the result ref, the handover's removal.
func (r *resume) complete(res wtsync.Result) error {
	st, name := r.st, r.name
	line := "  ✓ rebased " + kit.Count(res.Replayed, "commit")
	if st.OntoRef != "" {
		line += " onto " + st.OntoRef
	}
	fmt.Fprintln(r.w, line)
	if note := wtsync.EachCommitNote(res.EachCommit, res.Layered); note != "" {
		fmt.Fprintf(r.w, "    %s\n", note)
	}
	if a := res.Adopted; a != nil {
		for _, c := range a.Commits {
			fmt.Fprintf(r.w, "  + %s %s  adopted: made after the rebase\n", git.ShortID(c.SHA, 7), c.Subject)
		}
		if owed, err := wtsync.AdoptedLayerSteps(r.target.Path, r.cfg, res.LayerSteps, r.tipsOf(a.Head)); err != nil {
			fmt.Fprintf(r.w, "  note: which per-layer steps the hand finish skipped could not be read: %v\n", err)
		} else {
			res.LayerSteps = append(res.LayerSteps, owed...)
		}
	}
	head, ran, cerr := syncrun.CompleteRun(r.ctx, r.w, r.cfg, r.tracker, name, r.target.Path, func() syncrun.CompleteInput {
		// As a run does: the tip the run found is what the deferred steps
		// compare with and what the undo line names.
		done := res
		done.OldTip = r.pinned
		return syncrun.CompleteInput{
			Branch: st.Branch, Epoch: st.Epoch, Res: done,
			Tell: r.sessions, TrunkName: cmp.Or(st.OntoRef, strings.TrimPrefix(st.TrunkRef, "origin/")), Landed: r.landed,
			Check:       syncrun.PathsOnce(st.Stopped, st.Left, st.ResolvedPaths(), st.Deleted, wtsync.StopPaths(res.Stops)),
			Unavailable: r.unavailable,
			// A wt sync steps run handed over goes on as it was: its
			// selection, run whatever the rebase changed.
			Steps: st.Layers.StepSelection(), ForceSteps: st.Layers != nil && st.Layers.ForceSteps,
		}
	})
	if err := syncrun.RecordOwed(r.ctx.Repo.MainRoot, st.Branch, st.Epoch, res, ran); err != nil {
		fmt.Fprintf(r.w, "  note: what is owed could not be recorded for wt sync steps: %v\n", err)
	}
	settled := res
	if a := res.Adopted; a != nil {
		// The layers are where the rebase itself finished; the commits made
		// after it stay on the line.
		settled.NewTip = a.Finish
	}
	stackFailure := r.finishStack(settled)
	tipOwed := r.checkAdoptedTips(res, head, cerr)
	// What the rebase did to its commits is told once the layers are
	// settled, and not at all when the line was put back: the result it
	// describes is gone.
	if !r.restored {
		syncrun.PrintReplayReport(r.w, res)
	}
	syncrun.PrintUnchecked(r.w, res.Unchecked)
	owed := append(syncrun.OwedChecks(res.Unchecked), syncrun.OwedLayerSteps(res.LayerSteps)...)
	owed = append(owed, syncrun.OwedSteps(ran)...)
	owed = append(owed, tipOwed...)
	r.j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) {
		p.Result, p.PlanFile, p.UndoCommand, p.Deferred = syncrun.ResultRebased, nil, syncrun.UndoCommand(name), syncrun.DeferredSteps(ran)
		p.LayerSteps = syncrun.LayerStepsOf(res.LayerSteps)
		p.Adopted = syncrun.AdoptedOf(res.Adopted)
		if !r.restored {
			syncrun.SetReplayReport(p, res)
		}
		switch {
		case cerr != nil:
			p.Result, p.FailedSteps = syncrun.ResultRebasedStepFailed, append(p.FailedSteps, "finish: "+cerr.Error())
		case len(owed) > 0:
			p.Result, p.FailedSteps = syncrun.ResultRebasedStepFailed, append(p.FailedSteps, owed...)
		}
		if p.Stack != nil && p.Stack.Restored {
			// An edit run whose layers were refused: the line is back at
			// the tip the run found.
			p.Result, p.After, p.Reason = syncrun.ResultRestored, jsonout.Strp(p.Stack.Head), p.Stack.Refusal
		}
	})
	if cerr != nil {
		return cerr
	}
	syncrun.PrintFollowUps(r.w, name, st.FollowUps)
	failures := syncrun.OwedBy(name, owed)
	if stackFailure != "" {
		failures = append(failures, name+" ("+stackFailure+")")
	}
	if len(failures) > 0 {
		return fmt.Errorf("not completed: %s", strings.Join(failures, ", "))
	}
	return nil
}

// proveAdopt proves the one shape --adopt takes over, before anything
// moves: a worktree with nothing uncommitted, the branch's reflog showing
// the rebase's own finish with only commits added on top after it, and,
// for a stack, every layer's marker where the run's plan says it must be on
// the rebase's result. tips is the layers' new tips, bottom first.
func (r *resume) proveAdopt() (wtsync.Adoption, []wtsync.LayerTip, error) {
	st, main := r.st, r.ctx.Repo.MainRoot
	dirty, err := git.Run(r.target.Path, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return wtsync.Adoption{}, nil, err
	}
	if strings.TrimSpace(dirty) != "" {
		return wtsync.Adoption{}, nil, fmt.Errorf("the worktree has changes that are not committed, and the adopted tip is what is committed: commit them or put them aside, then wt sync resume --adopt %s", r.name)
	}
	a, err := wtsync.ProveAdoption(r.target.Path, st.Branch, st.Onto, st.OldTip, st.Total)
	if err != nil {
		return wtsync.Adoption{}, nil, err
	}
	rec, ok, err := wtsync.ReadStackRecord(main, st.Epoch)
	if err != nil {
		return wtsync.Adoption{}, nil, fmt.Errorf("the stack's record cannot be read (%w)", err)
	}
	if !ok {
		return a, nil, nil
	}
	switch {
	case rec.Edit != nil:
		return wtsync.Adoption{}, nil, errors.New("--adopt does not take over a --stack --edit run: its content is held to the edit, which a hand finish never reached")
	case rec.Autosquash || rec.InPlace:
		return wtsync.Adoption{}, nil, errors.New("--adopt does not take over a fold: its content is held to the line it folded, and commits made after it are not part of that")
	}
	plans := rec.Plans
	if st.Layers != nil && st.Layers.Plans != nil {
		plans = st.Layers.Plans
	}
	// No reconcile: wt did not drive this rebase from start to end, so no
	// commit can be proved dropped; a layer that counts differently is refused.
	tips, err := wtsync.CheckLayers(main, a.Finish, rec.Onto, plans)
	if err != nil {
		return wtsync.Adoption{}, nil, err
	}
	return a, tips, nil
}

// tipsOf is the tips an adopt checks, bottom first: every layer's new tip,
// then the line's own at head.
func (r *resume) tipsOf(head string) []wtsync.Tip {
	var tips []wtsync.Tip
	base := r.st.Onto
	for _, t := range r.adoptTips {
		tips = append(tips, wtsync.Tip{Branch: t.Branch, Commit: t.New, Base: base})
		base = t.New
	}
	return append(tips, wtsync.Tip{Branch: r.st.Branch, Commit: head, Base: base})
}

// checkAdoptedTips runs the each_commit checks at every tip of an adopted
// line, the way --each-commit=tips does, under the run's own mode: under all
// the commits between were never checked, which is said; under off nothing
// runs. What fails or cannot run is owed. head is the line's tip after the
// deferred steps; a finish that failed checks nothing.
func (r *resume) checkAdoptedTips(res wtsync.Result, head string, cerr error) []string {
	if res.Adopted == nil || cerr != nil || r.restored {
		return nil
	}
	switch res.EachCommit {
	case wtsync.EachOff:
		return nil
	case wtsync.EachAll:
		fmt.Fprintf(r.w, "    each_commit checks run at each layer's tip only: the rebase was finished by hand, so the commits between are not checked\n")
	case wtsync.EachTargets:
		fmt.Fprintf(r.w, "    each_commit checks run at each layer's tip only: the rebase was finished by hand, so the commits a fixup folded into are not known, and are not checked\n")
	}
	// The line is finished, its result pinned and its layers moved: what an
	// interrupt now leaves is checks owed, which it says, and undo's way
	// back.
	r.j.SetSync(r.st.Branch, func(p *syncrun.SyncParticipant) {
		p.Adopted, p.UndoCommand, p.PlanFile = syncrun.AdoptedOf(res.Adopted), syncrun.UndoCommand(r.name), nil
	})
	r.tracker.Set(&syncrun.RebaseInFlight{Work: r.name, Path: r.target.Path, Rebased: true, Adopted: res.Adopted.Kept})
	r.probeLate(head)
	checks := wtsync.CheckTips(r.target.Path, r.cfg, r.tipsOf(head), r.unavailable, r.w)
	r.tracker.Set(nil)
	syncrun.PrintUnchecked(r.w, checks.Unchecked)
	owed := syncrun.OwedChecks(checks.Unchecked)
	for _, f := range checks.Failed {
		owed = append(owed, f.Owed())
	}
	r.j.SetSync(r.st.Branch, func(p *syncrun.SyncParticipant) { p.Checked += checks.Checked })
	return owed
}

// probeLate probes the needs of the checks that came due only once the
// deferred steps committed their output on top of the line, which nothing
// probed before anything moved. The layers have moved by now, so a need that
// does not answer owes the checks that need it, with --allow-owed or not.
func (r *resume) probeLate(head string) {
	steps, err := wtsync.TipsDue(r.target.Path, r.cfg, r.tipsOf(head))
	if err != nil {
		// Which checks are due cannot be told: every need not yet probed
		// owes the checks that need it.
		steps = r.cfg.EachCommit
	}
	var prober *syncrun.Prober
	for _, n := range (wtsync.Due{Checks: steps}).Needs() {
		if r.probed[n] {
			continue
		}
		r.probed[n] = true
		why := ""
		if err != nil {
			why = "which checks the deferred steps' output calls for could not be read: " + err.Error()
		} else {
			if prober == nil {
				prober = r.opts.Prober()
			}
			if perr := prober.Probe(n); perr != nil {
				why = kit.OneLine(perr.Error())
			}
		}
		if why == "" {
			continue
		}
		if r.unavailable == nil {
			r.unavailable = map[string]string{}
		}
		r.unavailable[n] = why
		fmt.Fprintf(r.w, "  ⚠ %s did not answer: the checks that need it are owed (%s)\n", n, why)
	}
}

// checkStack refuses, before anything moves, a handover of a --stack run
// whose record has no plan of its layers: the layers could be neither
// checked nor moved once the rebase finished.
func (r *resume) checkStack() error {
	rec, ok, err := wtsync.ReadStackRecord(r.ctx.Repo.MainRoot, r.st.Epoch)
	undo := "wt sync undo " + r.name + " puts back the line"
	switch {
	case err != nil:
		return fmt.Errorf("the stack's record cannot be read (%w); nothing is resumed: %s", err, undo)
	case ok && len(rec.Plans) == 0 && len(rec.Line.Layers) > 0:
		// Either way undo puts it back: it aborts a rebase still in
		// progress, and accepts one finished by hand as the run's own.
		undo = "wt sync undo " + r.name + " aborts the rebase and puts back the line"
		if !r.busy {
			undo = "the rebase was finished by hand, and wt sync undo " + r.name + " puts back the line"
		}
		return fmt.Errorf("the stack's record has no plan of its layers, so they could not be checked or moved; nothing is resumed: %s", undo)
	}
	return nil
}

// checkDropped refuses, at a stop at a fixup a fold moved ahead of its
// context, a resolution that drops lines of the fixup the old tip has and no
// commit still to be replayed brings: today such a loss shows only at the
// content check after the whole run. It reads and moves nothing but what it
// prints; --allow-dropped goes on, saying what it lets through.
func (r *resume) checkDropped() error {
	st, name := r.st, r.name
	if !r.busy || r.adopt != nil || len(st.Context) == 0 {
		return nil
	}
	fixup := st.Context[0]
	// A check that cannot run is never a block --allow-dropped cannot pass.
	cannot := func(err error) error {
		why := fmt.Sprintf("what the resolution keeps of %s could not be checked: %v", git.ShortID(fixup.Fixup, 7), err)
		if r.opts.AllowDropped {
			fmt.Fprintf(r.w, "  ⚠ --allow-dropped: %s; going on\n", why)
			return nil
		}
		return fmt.Errorf("%s: %s; nothing is resumed: --allow-dropped goes on without the check", name, why)
	}
	p, err := wtsync.RebaseProgress(r.target.Path)
	if err != nil {
		return cannot(err)
	}
	if p.Index != st.Stop || len(wtsync.GapsAt(st.Context, p.Commit)) == 0 {
		// The rebase went on by hand past the fixup's stop: what it made of
		// the fixup is committed, and the content check at the end holds
		// the line to the old tip.
		return nil
	}
	drops, unchecked, err := wtsync.ResolutionDrops(r.target.Path, fixup.Fixup, st.Safety)
	if err != nil {
		return cannot(err)
	}
	// What the check cannot see is said, never passed in silence.
	for _, u := range unchecked {
		fmt.Fprintf(r.w, "  ⚠ not checked, of %s: %s\n", git.ShortID(fixup.Fixup, 7), u)
	}
	if len(drops) == 0 {
		return nil
	}
	line := wtsync.DropsLine(drops, fixup.Fixup, fixup.FixupSubject)
	if r.opts.AllowDropped {
		fmt.Fprintf(r.w, "  ⚠ --allow-dropped: %s; going on as resolved\n", line)
		return nil
	}
	return fmt.Errorf("%s: %s, and no later commit brings them; nothing is resumed: put them in the resolution, git add it and wt sync resume %s, "+
		"or wt sync undo %s and move that hunk into a fixup of %s; --allow-dropped goes on when the drop is meant",
		name, line, name, name, git.ShortID(fixup.Needs, 7))
}

// finishStack checks and moves the layers of a stack held on the line, when
// the run that handed this rebase over was one, exactly as the run would
// have once its rebase finished. It returns the failure the resume ends with.
func (r *resume) finishStack(res wtsync.Result) string {
	main := r.ctx.Repo.MainRoot
	rec, ok, err := wtsync.ReadStackRecord(main, r.st.Epoch)
	switch {
	case err != nil:
		fmt.Fprintf(r.w, "  ✗ no layer moved: the stack's record: %v\n", err)
		return "no layer moved"
	case !ok:
		return ""
	}
	if res.Layers != nil {
		// The plan as the layer stops changed it, before and after the
		// handover.
		rec.Plans = res.Layers
	}
	if res.Edit != nil {
		// The edit as the resume went on from it: what it was amended to.
		rec.Edit = res.Edit
	}
	listed, lerr := r.opts.SessionsAgain(r.ctx, syncrun.RebasedNothing)
	// Confirmed only for a replay wt drove from start to end: the sequencer
	// was still at the handover's stop when this resume began (what the
	// person did there was to resolve it), and no earlier leg was advanced by
	// hand. Anything else leaves the plan strict.
	s, failure, ferr := syncrun.FinishStack(r.ctx, r.w, syncrun.StackFinish{Record: rec, OldTip: r.st.OldTip, NewTip: res.NewTip,
		Onto: rec.Onto, Own: wtsync.ReadOwnOrUnknown(main, r.ctx.Config.MainBranch), Listed: listed, ListErr: lerr,
		Force: rec.Force, Now: r.opts.Clock(), Folds: syncrun.FoldPaths(r.cfg), Stopped: append(append(slices.Clone(r.st.Stopped), wtsync.StopPaths(res.Stops)...), res.ByHand...),
		Dropped: res.Emptied, Confirmed: r.confirmed, Gaps: r.gaps, Unchecked: r.unchecked})
	r.j.SetSync(r.st.Branch, func(p *syncrun.SyncParticipant) { p.Stack = s; syncrun.SetFold(p, s) })
	r.restored = s != nil && s.Restored
	if ferr != nil {
		fmt.Fprintf(r.w, "  ✗ the layer moves stopped: %v; wt sync undo %s puts back the line and every layer moved\n", ferr, r.name)
		return "layer moves stopped"
	}
	return failure
}

// push offers the rebased branch's push, as a run does.
func (r *resume) push() error {
	ctx, st := r.ctx, r.st
	t := syncrun.PushTarget{Work: r.name, Branch: st.Branch, Path: r.target.Path}
	r.j.Set(st.Branch, func(p *syncrun.UpParticipant) {
		p.PushCommand, p.NoPushReason, p.FixCommand = syncrun.PushJSON(t, syncrun.PushOf(ctx, t))
	})
	pushed, failed, err := syncrun.OfferPush(ctx, r.w, r.opts.Push, r.opts.PushOptions, []syncrun.PushTarget{t})
	if err != nil {
		return err
	}
	r.j.Set(st.Branch, func(p *syncrun.UpParticipant) {
		// An answer to the push question may have given it a destination.
		if p.NoPushReason != nil {
			p.PushCommand, p.NoPushReason, p.FixCommand = syncrun.PushJSON(t, syncrun.PushOf(ctx, t))
		}
		p.Pushed = len(pushed) > 0
	})
	if len(failed) > 0 {
		return fmt.Errorf("not completed: %s", strings.Join(failed, ", "))
	}
	return nil
}

// recordResumeFailed records a resume whose rebase failed, by what it left:
// the handover still standing, which resume or undo takes on from, or a
// worktree that needs putting back by hand.
func recordResumeFailed(j *syncrun.RunJournal, st wtsync.State, path string, rerr error) {
	busy, _ := wtsync.RebaseInProgress(path)
	gitDir, _ := wtsync.GitDir(path)
	_, planned, _ := wtsync.ReadState(gitDir)
	j.SetSync(st.Branch, func(p *syncrun.SyncParticipant) {
		p.Reason = jsonout.Strp(rerr.Error())
		if busy && planned {
			p.Result, p.UndoCommand = syncrun.ResultHandedOver, syncrun.UndoCommand(st.Work)
			p.Recovery = jsonout.Strp(wtsync.WayOut(wtsync.Way{Work: st.Work, Plan: true, Rebasing: true}))
			return
		}
		p.Result, p.PlanFile = syncrun.ResultNeedsRecovery, nil
		p.Recovery = jsonout.Strp(wtsync.WayOut(wtsync.Way{Work: st.Work, Path: path, Rebasing: busy, Safety: st.Safety}))
	})
}

// verifyHandover refuses to continue a rebase that is not what the run left.
// None of what it finds is something the tool may repair by re-running a
// strategy over the recorded stop: that would overwrite a person's work.
//
//   - A tracked file changed but not staged makes git refuse to continue,
//     and the loop's did-not-advance guard would read that refusal as a
//     stuck rebase. In a run that means a restore; here it would mean
//     throwing away the resolution. This is refused wherever the rebase is.
//   - At the recorded stop, something still unmerged means the person is not
//     done — unless a strategy resolved it, in which case merging it is not
//     the person's to do and they are pointed at undo instead.
//   - At the recorded stop, a different blob under a strategy's name means a
//     file the brief said never to hand-merge was hand-merged.
//
// Past the recorded stop, reached by a person's own git rebase --continue,
// whatever is unmerged belongs to a stop the strategies have never seen. The
// loop gives it to them exactly as the run would have and hands over afresh
// what they do not claim; telling the person to resolve it would invite a
// hand-merge of a file a strategy owns. The recorded stop's blobs are
// committed by then, so they are not compared, and that is said out loud.
func verifyHandover(wtPath string, st wtsync.State, w io.Writer) error {
	// --untracked-files=no: an untracked file never blocks a rebase, and a
	// script may have left one.
	dirty, err := git.Run(wtPath, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	atEdit, atCheck := st.AtEdit, st.Check != nil
	if atEdit || (atCheck && st.Edit != nil) {
		// Past the edit stop, or in an edit run past a check stop, by a
		// person's own continue or a resume cut short, what is left is an
		// ordinary stop.
		if p, err := wtsync.RebaseProgress(wtPath); err == nil && p.Index != st.Stop {
			atEdit, atCheck = false, false
		}
	}
	if atEdit {
		// The edit stop: what HEAD is when the resume goes on is what the
		// rest of the line is replayed onto, so the change must be
		// committed; git would amend staged changes into the commit on its
		// own and leave unstaged ones in the way.
		if strings.TrimSpace(dirty) != "" {
			return fmt.Errorf("the edit of %s goes on from what is committed, and the worktree has changes that are not: amend them into the commit (git commit --amend) or commit them, then wt sync resume %s",
				git.ShortID(st.Edit.Commit, 7), st.Work)
		}
		return nil
	}
	if atCheck {
		// A failed check: HEAD is the commit, and resume runs the check
		// there again. Nothing a strategy staged is waiting, so there is
		// nothing to compare; what must hold is that the fix is committed,
		// since the check runs on the worktree and judges the commit.
		if strings.TrimSpace(dirty) != "" {
			return fmt.Errorf("the check that failed at %s runs on what is committed, and the worktree has changes that are not: amend them into the commit (git commit --amend) or commit them, then wt sync resume %s",
				git.ShortID(st.Check.Commit, 7), st.Work)
		}
		return nil
	}
	var unstaged []string
	for _, line := range strings.Split(dirty, "\n") {
		// The second status column is the worktree against the index: any
		// mark there is a change git will refuse to continue over. An
		// unmerged path marks both columns and is not a change of that kind.
		if len(line) > 3 && line[1] != ' ' && !unmergedStatus(line[:2]) {
			unstaged = append(unstaged, strings.TrimSpace(line[3:]))
		}
	}
	add := wtsync.WayOut(wtsync.Way{Work: st.Work, Plan: true, Rebasing: true, OwesAdd: true})
	undo := wtsync.WayOut(wtsync.Way{Work: st.Work, Plan: true, Rebasing: true, Restart: true})
	if len(unstaged) > 0 {
		return fmt.Errorf("changed but not staged: %s; git refuses to continue over that, so %s", strings.Join(unstaged, ", "), add)
	}
	p, err := wtsync.RebaseProgress(wtPath)
	if err != nil {
		return err
	}
	if p.Index != st.Stop {
		noteNotRechecked(w, fmt.Sprintf("the rebase is at %d/%d, not the %d/%d the plan describes", p.Index, p.Total, st.Stop, st.Total), st)
		return nil
	}
	unmerged, err := wtsync.StagedConflicts(wtPath)
	if err != nil {
		return err
	}
	if len(unmerged) > 0 {
		var owned, yours []string
		for _, c := range unmerged {
			if s := st.Strategy[c.Path]; s != "" {
				owned = append(owned, c.Path+" ("+s+")")
			} else {
				yours = append(yours, c.Path)
			}
		}
		if len(owned) > 0 {
			return fmt.Errorf("unmerged again after a strategy resolved it: %s; that is not yours to merge: %s", strings.Join(owned, ", "), undo)
		}
		return fmt.Errorf("still unmerged: %s; %s", strings.Join(yours, ", "), add)
	}
	var changed []string
	for path := range st.Resolved {
		cur, err := git.Run(wtPath, "rev-parse", "--verify", ":0:"+path)
		if err != nil {
			return fmt.Errorf("%s is no longer staged and %s owns it: %s", path, st.Strategy[path], undo)
		}
		if cur != st.Resolved[path] {
			changed = append(changed, path)
		}
	}
	for _, path := range st.Deleted {
		if _, err := git.Run(wtPath, "rev-parse", "--verify", ":0:"+path); err == nil {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	var named []string
	for _, p := range changed {
		fmt.Fprintf(w, "  %s was hand-merged; %s owns it\n", p, st.Strategy[p])
		if oid := st.Resolved[p]; oid != "" {
			fmt.Fprintf(w, "    put it back: git -C %s show %s > %s && git -C %s add -- %s\n",
				wtPath, oid, filepath.Join(wtPath, p), wtPath, p)
		} else {
			fmt.Fprintf(w, "    put it back: git -C %s rm --cached -- %s && rm %s\n", wtPath, p, filepath.Join(wtPath, p))
		}
		named = append(named, p+" ("+st.Strategy[p]+")")
	}
	if len(changed) > 0 {
		return fmt.Errorf("%s under \"never hand-merge here\" changed: %s; nothing is resumed",
			kit.Count(len(changed), "file"), strings.Join(named, ", "))
	}
	return nil
}

// unmergedStatus reports whether a porcelain v1 XY pair marks an unmerged
// path: DD, AU, UD, UA, DU, AA or UU.
func unmergedStatus(xy string) bool {
	return xy[0] == 'U' || xy[1] == 'U' || xy == "AA" || xy == "DD"
}

// noteNotRechecked says, where the recorded stop is already committed, which
// of the strategies' answers there resume can no longer compare.
func noteNotRechecked(w io.Writer, where string, st wtsync.State) {
	ps := append(st.ResolvedPaths(), st.Deleted...)
	sort.Strings(ps)
	line := "  note: " + where + "; what the strategies staged there is already committed and is not re-checked"
	for i, p := range ps {
		ps[i] = p + " (" + st.Strategy[p] + ")"
	}
	if len(ps) > 0 {
		line += ": " + strings.Join(ps, ", ")
	}
	fmt.Fprintln(w, line)
}
