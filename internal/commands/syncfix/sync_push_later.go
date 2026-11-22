package syncfix

import (
	"io"
	"slices"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// PushLaterOptions tunes SyncPushLater.
type PushLaterOptions struct {
	// PushOptions: Push is PushAlways for --yes, else ConfirmPush asks.
	syncrun.PushOptions
	// DryRun says what would be pushed and moves nothing.
	DryRun bool
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
