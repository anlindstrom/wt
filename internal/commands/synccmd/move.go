package synccmd

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
	"github.com/anlindstrom/wt/schema"
)

// MoveOptions is wt sync move: the commit the part comes from, the commit
// it goes into, each by id or whole subject, and the part, whole paths and
// line ranges.
type MoveOptions struct {
	From, Into string
	Paths      []string
	Lines      []wtsync.LineRange
	// JSON prints one object on stdout, for a move written or refused, in
	// place of the lines for a person.
	JSON bool
}

// moveResult is wt sync move --json.
type moveResult struct {
	Schema        int    `json:"schema"`
	SchemaVersion string `json:"schemaVersion"`
	Command       string `json:"command"`
	// Work is the worktree as it was named; Branch its branch, null when it
	// was not found.
	Work   string  `json:"work"`
	Branch *string `json:"branch"`
	// Commit is the move! commit written; null when none was.
	Commit *moveWritten `json:"commit"`
	// From and Into are the commits --from and --into name on the line; null
	// for one that was not found.
	From    *moveEnd           `json:"from"`
	Into    *moveEnd           `json:"into"`
	Paths   []string           `json:"paths"`
	Lines   []wtsync.LineRange `json:"lines"`
	Refusal *string            `json:"refusal"`
}

type moveWritten struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

type moveEnd struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// SyncMove is wt sync move: it writes the move! commit that moves opts.Paths
// and opts.Lines of opts.From into opts.Into on work's line, at its top, and
// nothing else: no file, no index and no other branch changes, and nothing is
// rebased. The commit is checked as a fold in place would take it before the
// branch moves; one the fold would refuse is not written.
func SyncMove(ctx *kit.Context, work string, opts MoveOptions, w io.Writer) error {
	res := newMoveResult(work, opts)
	m, err := writeMove(ctx, work, opts, &res)
	if err != nil {
		return refuseMove(w, opts, res, err)
	}
	if opts.JSON {
		return jsonout.WriteJSON(w, res)
	}
	fmt.Fprintf(w, "%s\n", m.Line())
	fmt.Fprintf(w, "  written at the top of %s; nothing moves until the fold:\n  wt sync rebase %s --stack --autosquash --each-commit=targets\n",
		*res.Branch, work)
	return nil
}

// RefuseMove reports err, which kept wt sync move from running at all, as
// SyncMove reports a refusal.
func RefuseMove(w io.Writer, work string, opts MoveOptions, err error) error {
	return refuseMove(w, opts, newMoveResult(work, opts), err)
}

func newMoveResult(work string, opts MoveOptions) moveResult {
	return moveResult{Schema: 1, SchemaVersion: schema.VersionOf("sync-move"), Command: "sync move", Work: work,
		Paths: append([]string{}, opts.Paths...), Lines: append([]wtsync.LineRange{}, opts.Lines...)}
}

func refuseMove(w io.Writer, opts MoveOptions, res moveResult, refusal error) error {
	if opts.JSON {
		why := refusal.Error()
		res.Refusal = &why
		if err := jsonout.WriteJSON(w, res); err != nil {
			return errors.Join(refusal, err)
		}
	}
	return refusal
}

// writeMove writes the commit and records in res what it learns on the way.
// An error means no commit was written.
func writeMove(ctx *kit.Context, work string, opts MoveOptions, res *moveResult) (wtsync.PartMove, error) {
	var none wtsync.PartMove
	if opts.From == "" || opts.Into == "" || len(opts.Paths)+len(opts.Lines) == 0 {
		return none, errors.New("wt sync move needs --from, --into and a path or --lines")
	}
	wt, err := kit.LocateBranch(ctx, work)
	if err != nil {
		return none, err
	}
	res.Branch = &wt.Branch
	main := ctx.Repo.MainRoot
	if _, on := repo.OnBranch(wt.Path, wt.Branch); !on {
		return none, fmt.Errorf("%s is not on %s: nothing is written", work, wt.Branch)
	}
	if busy, err := wtsync.RebaseInProgress(wt.Path); err != nil {
		return none, err
	} else if busy {
		return none, fmt.Errorf("%s is in the middle of a rebase: finish it (wt sync resume %s) or put it back (wt sync undo %s) first; nothing is written", work, work, work)
	}
	gitDir, err := wtsync.GitDir(wt.Path)
	if err != nil {
		return none, err
	}
	lock, err := wtsync.Acquire(gitDir, time.Now())
	if err != nil {
		return none, fmt.Errorf("%s: locked: %w; nothing is written", work, err)
	}
	defer func() { _ = lock.Release() }()
	trunk := "origin/" + ctx.Config.MainBranch
	if _, err := git.Run(main, "rev-parse", "--verify", "--quiet", trunk+"^{commit}"); err != nil {
		trunk = ctx.Config.MainBranch
	}
	line, err := wtsync.ReadLine(main, wt.Branch, trunk, nil, nil)
	if err != nil {
		return none, err
	}
	var names [2]string
	var unnamed string
	for i, end := range []struct {
		flag, name string
		into       **moveEnd
	}{{"--from", opts.From, &res.From}, {"--into", opts.Into, &res.Into}} {
		sha, write, why, err := wtsync.MoveName(main, line.Commits, end.name)
		if err != nil {
			return none, err
		}
		if why != "" {
			if unnamed == "" {
				unnamed = end.flag + " " + why
			}
			continue
		}
		subject, err := git.Run(main, "log", "-1", "--format=%s", sha)
		if err != nil {
			return none, err
		}
		names[i], *end.into = write, &moveEnd{SHA: sha, Subject: subject}
	}
	if unnamed != "" {
		return none, fmt.Errorf("%s; nothing is written", unnamed)
	}
	msg := wtsync.MoveMessage(names[1], names[0], opts.Paths, opts.Lines)
	m, why, err := wtsync.WriteMove(main, line, msg)
	if err != nil {
		return none, fmt.Errorf("%w; nothing is written", err)
	}
	if why != "" {
		return none, fmt.Errorf("%s; nothing is written", why)
	}
	res.Commit = &moveWritten{SHA: m.Commit, Message: msg}
	res.Paths, res.Lines = append([]string{}, m.Paths...), append([]wtsync.LineRange{}, m.Lines...)
	return m, nil
}
