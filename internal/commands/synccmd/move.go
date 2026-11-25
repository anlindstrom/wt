package synccmd

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// MoveOptions is wt sync move: the commit the part comes from, the commit
// it goes into, each by id or whole subject, and the part, whole paths.
type MoveOptions struct {
	From, Into string
	Paths      []string
}

// SyncMove is wt sync move: it writes the move! commit that moves opts.Paths
// of opts.From into opts.Into on work's line, at its top, and nothing else:
// no file, no index and no other branch changes, and nothing is rebased. The
// commit is checked as a fold in place would take it before the branch
// moves; one the fold would refuse is not written.
func SyncMove(ctx *kit.Context, work string, opts MoveOptions, w io.Writer) error {
	if opts.From == "" || opts.Into == "" || len(opts.Paths) == 0 {
		return errors.New("wt sync move needs --from, --into and at least one path")
	}
	wt, err := kit.LocateBranch(ctx, work)
	if err != nil {
		return err
	}
	main := ctx.Repo.MainRoot
	if _, on := repo.OnBranch(wt.Path, wt.Branch); !on {
		return fmt.Errorf("%s is not on %s: nothing is written", work, wt.Branch)
	}
	if busy, err := wtsync.RebaseInProgress(wt.Path); err != nil {
		return err
	} else if busy {
		return fmt.Errorf("%s is in the middle of a rebase: finish it (wt sync resume %s) or put it back (wt sync undo %s) first; nothing is written", work, work, work)
	}
	gitDir, err := wtsync.GitDir(wt.Path)
	if err != nil {
		return err
	}
	lock, err := wtsync.Acquire(gitDir, time.Now())
	if err != nil {
		return fmt.Errorf("%s: locked: %w; nothing is written", work, err)
	}
	defer func() { _ = lock.Release() }()
	trunk := "origin/" + ctx.Config.MainBranch
	if _, err := git.Run(main, "rev-parse", "--verify", "--quiet", trunk+"^{commit}"); err != nil {
		trunk = ctx.Config.MainBranch
	}
	line, err := wtsync.ReadLine(main, wt.Branch, trunk, nil, nil)
	if err != nil {
		return err
	}
	_, fromName, why, err := wtsync.MoveName(main, line.Commits, opts.From)
	if err == nil && why == "" {
		var into string
		if _, into, why, err = wtsync.MoveName(main, line.Commits, opts.Into); err == nil && why == "" {
			msg := wtsync.MoveMessage(into, fromName, opts.Paths)
			return writeMove(ctx, w, work, wt, line, msg)
		}
		why = "--into " + why
	} else if why != "" {
		why = "--from " + why
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%s; nothing is written", why)
}

// writeMove writes the move! commit through wtsync and says what it moves
// and how the fold makes it.
func writeMove(ctx *kit.Context, w io.Writer, work string, wt repo.Worktree, line wtsync.Line, msg string) error {
	m, why, err := wtsync.WriteMove(ctx.Repo.MainRoot, line, msg)
	if err != nil {
		return fmt.Errorf("%w; nothing is written", err)
	}
	if why != "" {
		return fmt.Errorf("%s; nothing is written", why)
	}
	fmt.Fprintf(w, "%s\n", m.Line())
	fmt.Fprintf(w, "  written at the top of %s; nothing moves until the fold:\n  wt sync rebase %s --stack --autosquash --each-commit=targets\n",
		wt.Branch, work)
	return nil
}
