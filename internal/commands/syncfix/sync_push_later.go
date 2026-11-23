package syncfix

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// PushLaterOptions tunes SyncPushLater.
type PushLaterOptions struct {
	// PushOptions: Push is PushAlways for --yes, else ConfirmPush asks.
	syncrun.PushOptions
	// DryRun says what would be pushed and moves nothing.
	DryRun bool
	// Forget drops the line's record and does nothing else (--forget).
	Forget bool
	// Journal records the push, for --json; nil records nothing.
	Journal *syncrun.RunJournal
}

// SyncPushLater is wt sync push <work> --stack: the push a --stack run left
// for later (--no-push), over the leases the run recorded.
func SyncPushLater(ctx *kit.Context, work string, opts PushLaterOptions, w io.Writer) error {
	return syncrun.Journaled(opts.Journal, func() error { return syncPushLater(ctx, work, opts, w) })
}

func syncPushLater(ctx *kit.Context, work string, opts PushLaterOptions, w io.Writer) error {
	defer syncrun.WatchSignals(w, nil)()
	target, err := kit.LocateBranch(ctx, work)
	if err != nil {
		return err
	}
	name := kit.WorktreeName(ctx, target.Branch, target.Path)
	j := opts.Journal
	if j != nil {
		j.Repo(ctx.Repo.MainRoot)
		tip, _, _ := ctx.Repo.ResolveRef("refs/heads/" + target.Branch)
		j.Join(name, target.Branch, target.Path, tip, nil)
	}
	if opts.Forget {
		return forgetPushRecord(ctx, target, name, opts, w)
	}
	// The line's worktree lock, which a run and wt sync undo take for it,
	// from reading the record to deleting or rewriting it: no run or undo
	// changes the line or its record under a push.
	var s *syncrun.Stack
	later := syncrun.LaterOptions{PushOptions: opts.PushOptions, DryRun: opts.DryRun}
	if opts.DryRun {
		// It changes nothing, a lock included: Acquire breaks an expired
		// lock a dead process left. It only reads.
		s, err = syncrun.PushRecorded(ctx, w, name, target.Branch, later)
	} else if gitDir, gerr := wtsync.GitDir(target.Path); gerr != nil {
		err = gerr
	} else if lock, lerr := wtsync.Acquire(gitDir, time.Now()); lerr != nil {
		err = lerr
	} else {
		defer func() { _ = lock.Release() }()
		s, err = syncrun.PushRecorded(ctx, w, name, target.Branch, later)
	}
	pushedAny := s != nil && s.Push != nil && slices.ContainsFunc(s.Push.Branches, func(b syncrun.StackPushed) bool { return b.Outcome == syncrun.PushedOK })
	j.SetSync(target.Branch, func(p *syncrun.SyncParticipant) {
		switch {
		case pushedAny:
			p.Result = syncrun.ResultPushed
		case err != nil:
			// A refusal, with no stack loaded too: no record, or a lock held.
			p.Result, p.Reason = syncrun.ResultRefused, jsonout.Strp(err.Error())
		default:
			// --dry-run: nothing changed.
			p.Result = syncrun.ResultSkipped
		}
		if s == nil {
			return
		}
		p.Stack = s
		if s.Push != nil {
			for _, b := range s.Push.Branches {
				if b.Branch == target.Branch {
					p.Pushed = b.Outcome == syncrun.PushedOK
				}
			}
		}
	})
	if pushedAny && err != nil {
		// Some remote went and another did not: the outcome is partial, with
		// the error, as for an undo that stopped part way.
		j.FailWith(err)
	}
	return err
}

// forgetPushRecord is wt sync push <work> --stack --forget: the line's record
// dropped, and nothing else. No ref moves and nothing is pushed. It holds the
// record's flock while it reads and deletes, so a run saving its record is
// not dropped unseen; a --dry-run only reads.
func forgetPushRecord(ctx *kit.Context, target repo.Worktree, name string, opts PushLaterOptions, w io.Writer) error {
	main := ctx.Repo.MainRoot
	var rec *wtsync.PushRecord
	var unreadable, err error
	if opts.DryRun {
		rec, unreadable, err = wtsync.InspectPushRecord(main, target.Branch)
	} else {
		rec, unreadable, err = wtsync.ForgetPushRecordLocked(main, target.Branch)
	}
	if err == nil {
		verb, done := "dropped", "no ref moved and nothing was pushed"
		if opts.DryRun {
			verb, done = "would drop", "--dry-run: nothing dropped"
		}
		switch {
		case unreadable != nil:
			rm := "removed"
			if opts.DryRun {
				rm = "would remove"
			}
			fmt.Fprintf(w, "  %s: the push record could not be read (%v); %s it\n  %s\n", name, unreadable, rm, done)
		case rec == nil:
			fmt.Fprintf(w, "  %s: nothing recorded, nothing to drop\n", name)
		default:
			runs := rec.Runs()
			fmt.Fprintf(w, "  %s: %s the push record of %s: %s\n", name, verb, kit.Count(len(runs), "run"), strings.Join(rec.Branches(), ", "))
			for _, r := range runs {
				fmt.Fprintf(w, "    run %d, %s\n", r.Epoch, r.Written.Local().Format("2006-01-02 15:04"))
			}
			fmt.Fprintf(w, "  %s\n", done)
		}
	}
	opts.Journal.SetSync(target.Branch, func(p *syncrun.SyncParticipant) {
		if err != nil {
			p.Result, p.Reason = syncrun.ResultRefused, jsonout.Strp(err.Error())
			return
		}
		p.Result = syncrun.ResultSkipped
		if rec == nil && unreadable == nil {
			return
		}
		d := &syncrun.DroppedRecordJSON{Runs: []syncrun.StackRunRef{}, Branches: []string{}, Dropped: !opts.DryRun}
		if unreadable != nil {
			d.Unreadable = jsonout.Strp(unreadable.Error())
		} else {
			d.Branches = rec.Branches()
			for _, r := range rec.Runs() {
				d.Runs = append(d.Runs, syncrun.StackRunRef{Epoch: r.Epoch, Time: r.Written})
			}
		}
		p.DroppedRecord = d
	})
	return err
}
