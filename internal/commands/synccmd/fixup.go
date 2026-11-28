package synccmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
	"github.com/anlindstrom/wt/schema"
)

// FixupOptions is wt sync fixup <rev>.
type FixupOptions struct {
	// Rev names the commit of the line the fixup is for.
	Rev string
	// DryRun says what would be committed and writes nothing.
	DryRun bool
	// JSON prints one object on stdout, for a commit or a refusal, in place
	// of the lines for a person.
	JSON bool
}

// fixupResult is wt sync fixup --json.
type fixupResult struct {
	Schema        int                 `json:"schema"`
	SchemaVersion string              `json:"schemaVersion"`
	Command       string              `json:"command"`
	DryRun        bool                `json:"dryRun"`
	Branch        *string             `json:"branch"`
	Target        *lineCommitRef      `json:"target"`
	Commit        *fixupCommit        `json:"commit"`
	Files         []wtsync.StagedFile `json:"files"`
	Refusal       *string             `json:"refusal"`
}

type fixupCommit struct {
	// SHA is null for a dry run.
	SHA     *string `json:"sha"`
	Subject string  `json:"subject"`
}

// SyncFixup is wt sync fixup <rev>: it commits what is staged in the worktree
// the caller stands in as a fixup whose subject names rev, a commit of its
// line, and no other.
func SyncFixup(ctx *kit.Context, opts FixupOptions, w io.Writer) error {
	res := newFixupResult(opts)
	work, err := writeFixup(ctx, opts, &res)
	if err != nil {
		return refuseFixup(w, opts, res, err)
	}
	if opts.JSON {
		return jsonout.WriteJSON(w, res)
	}
	if sha := res.Commit.SHA; sha != nil {
		fmt.Fprintf(w, "committed %s %s\n", git.ShortID(*sha, 7), kit.OneLine(res.Commit.Subject))
	} else {
		fmt.Fprintf(w, "would commit %s\n", kit.OneLine(res.Commit.Subject))
	}
	fmt.Fprintf(w, "  a fixup of %s\n", refLine(*res.Target))
	for _, f := range res.Files {
		fmt.Fprintf(w, "  %s %s\n", f.Status, f.Path)
	}
	if work != "" && res.Commit.SHA != nil {
		fmt.Fprintf(w, "  nothing is folded yet: wt sync rebase %s --stack --autosquash\n", work)
	}
	return nil
}

// RefuseFixup reports err, which kept wt sync fixup from running at all, as
// SyncFixup reports a refusal.
func RefuseFixup(w io.Writer, opts FixupOptions, err error) error {
	return refuseFixup(w, opts, newFixupResult(opts), err)
}

func newFixupResult(opts FixupOptions) fixupResult {
	return fixupResult{Schema: 1, SchemaVersion: schema.VersionOf("sync-fixup"), Command: "sync fixup",
		DryRun: opts.DryRun, Files: []wtsync.StagedFile{}}
}

func refuseFixup(w io.Writer, opts FixupOptions, res fixupResult, refusal error) error {
	if opts.JSON {
		why := refusal.Error()
		res.Refusal = &why
		if err := jsonout.WriteJSON(w, res); err != nil {
			return errors.Join(refusal, err)
		}
	}
	return refusal
}

// writeFixup commits, unless opts.DryRun, and records in res what it learns on
// the way; a commit in res with an error is on the branch although git
// reported a failure. work is the name a fold of the line takes, "" when the
// line has no layer for --stack to run on.
func writeFixup(ctx *kit.Context, opts FixupOptions, res *fixupResult) (work string, err error) {
	nothing := func(why string) error { return fmt.Errorf("%s; nothing is committed", why) }
	if name := wtsync.IndexOverride(); name != "" {
		return "", nothing(name + " is set: wt sync fixup commits the index of the worktree you stand in; unset it")
	}
	wt, err := standingWorktree(ctx)
	if err != nil {
		return "", err
	}
	gitDir, err := wtsync.GitDir(wt.Path)
	if err != nil {
		return "", err
	}
	// A signal ends the command from here on, with the locks let go; deferred
	// first, so it is the last thing to end.
	guard := &fixupGuard{}
	if !opts.DryRun {
		defer guard.watch(os.Stdout, os.Stderr, opts)()
	}
	if opts.DryRun {
		if held, err := wtsync.Locked(gitDir, time.Now()); err != nil {
			return "", err
		} else if held != "" {
			return "", nothing(held)
		}
	} else {
		lock, err := wtsync.Acquire(gitDir, time.Now())
		if err != nil {
			return "", nothing("locked: " + err.Error())
		}
		defer func() { _ = lock.Release() }()
	}
	// Held from here to the commit, so nothing is staged, merged or switched
	// between the look at the worktree and the branch's move.
	var held *wtsync.HeldIndex
	index, locked := "", ""
	if opts.DryRun {
		locked, err = wtsync.IndexBlocked(wt.Path)
	} else if held, locked, err = wtsync.HoldIndex(wt.Path); held != nil {
		guard.index.Store(held)
		defer held.Release()
		index = held.Copy
	}
	if err != nil {
		return "", err
	}
	if locked != "" {
		return "", nothing(locked)
	}
	// Ahead of the branch: a rebase leaves none checked out.
	if what, err := repo.OperationInProgress(wt.Path); err != nil {
		return "", err
	} else if what != "" {
		return "", nothing(fmt.Sprintf("%s is in the middle of a %s: finish or abort it first", wt.Path, what))
	}
	line, err := heldLine(ctx, wt)
	if err != nil {
		return "", nothing(err.Error())
	}
	res.Branch = &line.Branch
	files, why, err := wtsync.Staged(wt.Path, index)
	if err != nil {
		return "", err
	}
	if why != "" {
		return "", nothing(why)
	}
	res.Files = files
	i, err := onLine(wt.Path, line, opts.Rev)
	if err != nil {
		return "", nothing(err.Error())
	}
	subject, err := git.Run(ctx.Repo.MainRoot, "log", "-1", "--format=%s", line.Commits[i])
	if err != nil {
		return "", err
	}
	res.Target = &lineCommitRef{SHA: line.Commits[i], Subject: subject, Layer: line.LayerAt(i)}
	fixup, why, err := wtsync.FixupSubject(wt.Path, line.Commits, i)
	if err != nil {
		return "", err
	}
	if why != "" {
		return "", nothing(why)
	}
	if len(line.Layers) > 0 && !wt.IsMain {
		work = kit.WorktreeName(ctx, line.Branch, wt.Path)
	}
	if opts.DryRun {
		res.Commit = &fixupCommit{Subject: fixup}
		return work, nil
	}
	sha, err := wtsync.CommitStaged(wt.Path, held, line.Branch, line.Head, fixup, guard.proceed)
	if sha != "" {
		res.Commit = &fixupCommit{SHA: &sha, Subject: fixup}
	}
	return work, err
}

// fixupGuard is what a signal during a fixup needs: the index to let go of,
// and whether the branch was already being moved.
type fixupGuard struct {
	index      atomic.Pointer[wtsync.HeldIndex]
	publishing atomic.Bool
	caught     chan os.Signal
}

// proceed is whether the branch may move: not after a signal.
func (g *fixupGuard) proceed() bool {
	if len(g.caught) > 0 {
		return false
	}
	g.publishing.Store(true)
	return true
}

// watch ends the command on Ctrl-C, SIGTERM or a hangup, as every sync verb
// ends: the git under way is killed, the index and the run's lock are let go,
// and --json still prints its object on out. The returned func ends the watch.
func (g *fixupGuard) watch(out, said io.Writer, opts FixupOptions) func() {
	g.caught = make(chan os.Signal, 1)
	signal.Notify(g.caught, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	stop := kit.WatchSignals(said, func(w io.Writer) string {
		why := "interrupted; nothing is committed"
		if g.publishing.Load() {
			why = "interrupted while the branch was moving: look at it before trying again"
		}
		if index := g.index.Load(); index != nil {
			index.Release()
		}
		syncrun.OnInterrupt(w, wtsync.HeldLocks(), nil)
		// The error is the command's exit status, which the signal decides.
		_ = refuseFixup(out, opts, newFixupResult(opts), errors.New(why))
		return ""
	})
	// kit watches no hangup, which would end wt with the locks left behind.
	hangup, done := make(chan os.Signal, 1), make(chan struct{})
	if !signal.Ignored(syscall.SIGHUP) {
		signal.Notify(hangup, syscall.SIGHUP)
	}
	go func() {
		select {
		case <-hangup:
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(hangup)
		close(done)
		stop()
		signal.Stop(g.caught)
	}
}
