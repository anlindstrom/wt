package synccmd

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
	"github.com/anlindstrom/wt/schema"
)

// OwnerOptions is wt sync owner: the lines of a file, and the commit a change
// to them would fold into.
type OwnerOptions struct {
	// Work names the worktree whose line is read; "" is the one the caller
	// stands in.
	Work string
	// Lines is the file, relative to the caller's directory (to the worktree's
	// root with Work), and the lines of it; From 0 is the whole file.
	Lines wtsync.LineRange
	// Into is a commit of the line to ask about, "" for none.
	Into string
	// Probes replaces the probes of the needs a fold's steps declare, in a
	// test.
	Probes map[string]func() error
	// JSON prints one object on stdout, for an answer or a refusal, in place
	// of the lines for a person.
	JSON bool
}

// lineCommitRef is a commit of the line and the layer it is in.
type lineCommitRef struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Layer   string `json:"layer"`
}

// ownerResult is wt sync owner --json.
type ownerResult struct {
	Schema        int              `json:"schema"`
	SchemaVersion string           `json:"schemaVersion"`
	Command       string           `json:"command"`
	Work          *string          `json:"work"`
	Branch        *string          `json:"branch"`
	Tip           *string          `json:"tip"`
	Path          *string          `json:"path"`
	Lines         *wtsync.LineSpan `json:"lines"`
	Edited        bool             `json:"edited"`
	// Owners is newest first.
	Owners  []ownedLines      `json:"owners"`
	Unowned []wtsync.LineSpan `json:"unowned"`
	Into    *ownerInto        `json:"into"`
	Refusal *string           `json:"refusal"`
}

type ownedLines struct {
	lineCommitRef
	// FoldsInto is, for an owner that is itself a fixup, the commit the fold
	// makes it part of.
	FoldsInto *lineCommitRef    `json:"foldsInto"`
	Lines     []wtsync.LineSpan `json:"lines"`
}

type ownerInto struct {
	lineCommitRef
	Verdict wtsync.ProbeVerdict `json:"verdict"`
	Needs   *lineCommitRef      `json:"needs"`
	Why     *string             `json:"why"`
}

// SyncOwner is wt sync owner: it says which commits of a worktree's line own
// some lines of a file as the line's tip has it, and with opts.Into whether a
// change to those lines folds into that commit. Nothing but objects in the
// store is written.
func SyncOwner(ctx *kit.Context, opts OwnerOptions, w io.Writer) error {
	res := newOwnerResult(opts)
	if err := readOwner(ctx, opts, &res); err != nil {
		return refuseOwner(w, opts, res, err)
	}
	if opts.JSON {
		return jsonout.WriteJSON(w, res)
	}
	printOwner(w, res)
	return nil
}

// RefuseOwner reports err, which kept wt sync owner from running at all, as
// SyncOwner reports a refusal.
func RefuseOwner(w io.Writer, opts OwnerOptions, err error) error {
	return refuseOwner(w, opts, newOwnerResult(opts), err)
}

func newOwnerResult(opts OwnerOptions) ownerResult {
	res := ownerResult{Schema: 1, SchemaVersion: schema.VersionOf("sync-owner"), Command: "sync owner",
		Owners: []ownedLines{}, Unowned: []wtsync.LineSpan{}}
	if opts.Work != "" {
		res.Work = &opts.Work
	}
	return res
}

func refuseOwner(w io.Writer, opts OwnerOptions, res ownerResult, refusal error) error {
	if opts.JSON {
		why := refusal.Error()
		res.Refusal = &why
		if err := jsonout.WriteJSON(w, res); err != nil {
			return errors.Join(refusal, err)
		}
	}
	return refusal
}

// readOwner records in res what it learns on the way; an error is why there
// is no answer.
func readOwner(ctx *kit.Context, opts OwnerOptions, res *ownerResult) error {
	var wt repo.Worktree
	var err error
	if opts.Work != "" {
		wt, err = kit.LocateBranch(ctx, opts.Work)
	} else {
		wt, err = standingWorktree(ctx)
	}
	if err != nil {
		return err
	}
	line, err := heldLine(ctx, wt)
	if err != nil {
		return err
	}
	res.Branch, res.Tip = &line.Branch, &line.Head
	from := ctx.Cwd
	if opts.Work != "" {
		from = wt.Path
	}
	path, err := pathInWorktree(wt.Path, from, opts.Lines.Path)
	if err != nil {
		return err
	}
	res.Path = &path
	content, _, why, err := wtsync.LinesOf(wt.Path, line.Head, path)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	span := wtsync.LineSpan{From: opts.Lines.From, To: opts.Lines.To}
	if span.From == 0 {
		span = wtsync.LineSpan{From: 1, To: len(content)}
	}
	if span.To > len(content) {
		return fmt.Errorf("%s has %d lines at the line's tip: %s is not within them", path, len(content), span)
	}
	res.Lines = &span
	var into int
	if opts.Into != "" {
		if into, err = onLine(wt.Path, line, opts.Into); err != nil {
			return fmt.Errorf("--into %w", err)
		}
	}
	main := ctx.Repo.MainRoot
	ref := func(i int) (lineCommitRef, error) {
		subject, err := git.Run(main, "log", "-1", "--format=%s", line.Commits[i])
		return lineCommitRef{SHA: line.Commits[i], Subject: subject, Layer: line.LayerAt(i)}, err
	}
	if res.Edited, err = wtsync.Edited(wt.Path, path, span); err != nil {
		return err
	}
	owners, unowned, err := wtsync.Owners(wt.Path, line, path, span)
	if err != nil {
		return err
	}
	pairs, err := wtsync.FoldPairs(wt.Path, line.Commits)
	if err != nil {
		return err
	}
	targetOf := map[int]int{}
	for _, p := range pairs {
		targetOf[p.FixupAt] = p.TargetAt
	}
	for _, o := range owners {
		c, err := ref(o.At)
		if err != nil {
			return err
		}
		owned := ownedLines{lineCommitRef: c, Lines: o.Lines}
		if t, fold := targetOf[o.At]; fold {
			for next, more := targetOf[t]; more; next, more = targetOf[t] {
				t = next
			}
			target, err := ref(t)
			if err != nil {
				return err
			}
			owned.FoldsInto = &target
		}
		res.Owners = append(res.Owners, owned)
	}
	res.Unowned = append(res.Unowned, unowned...)
	if opts.Into == "" {
		return nil
	}
	c, err := ref(into)
	if err != nil {
		return err
	}
	probe, err := probeInto(ctx, opts, wt, line, into, path, span)
	if err != nil {
		return err
	}
	res.Into = &ownerInto{lineCommitRef: c, Verdict: probe.Verdict}
	if probe.Why != "" {
		res.Into.Why = &probe.Why
	}
	if probe.Needs >= 0 {
		needs, err := ref(probe.Needs)
		if err != nil {
			return err
		}
		res.Into.Needs = &needs
	}
	return nil
}

// probeInto is whether a change to span of path folds into line.Commits[into]:
// refused first for what the look and the run refuse of the fold, on the line
// as it is and then with the change on top.
func probeInto(ctx *kit.Context, opts OwnerOptions, wt repo.Worktree, line wtsync.Line, into int, path string, span wtsync.LineSpan) (wtsync.Probe, error) {
	none := wtsync.Probe{Needs: -1}
	refused := func(why string) (wtsync.Probe, error) {
		return wtsync.Probe{Verdict: wtsync.ProbeRefused, Needs: -1, Why: why}, nil
	}
	if wt.IsMain {
		return refused(kit.ErrMainCheckout.Error())
	}
	onto, _, cfg, err := SyncDeclaration(ctx)
	if err != nil {
		return refused(err.Error())
	}
	if cfg == nil {
		return refused(fmt.Sprintf("%s declares no %s: wt sync rebase rebases nothing here", onto, wtsync.ConfigFile))
	}
	listed, err := syncrun.VerbOptions{}.SessionsNow(ctx, syncrun.RebasedNothing)
	if err != nil {
		return refused(err.Error())
	}
	main, work := ctx.Repo.MainRoot, kit.WorktreeName(ctx, wt.Branch, wt.Path)
	gitDir, err := wtsync.GitDir(wt.Path)
	if err != nil {
		return none, err
	}
	if held, err := wtsync.Locked(gitDir, time.Now()); err != nil {
		return none, err
	} else if held != "" {
		return refused(work + ": locked: " + held)
	}
	if moves, err := wtsync.HasMoves(main, line.Commits); err != nil {
		return none, err
	} else if moves {
		return refused(wtsync.MoveRunRefusal(false, wtsync.EachAll))
	}
	own := wtsync.ReadOwnOrUnknown(main, ctx.Config.MainBranch)
	a := wtsync.AssessTo(main, wtsync.Target{Onto: onto, InPlace: true}, cfg, wt, listed, syncrun.OwnStateOf(own, wt.Branch))
	// The change asked about is yet to be committed: that the worktree holds
	// uncommitted changes now is no refusal of it.
	a.Dirty = false
	if why := stackRefusal(cfg, syncrun.StackOf(line, true, nil, wt.Path), work, a); why != "" {
		return refused(why)
	}
	prospect, answer, err := wtsync.Propose(wt.Path, line, into, path, span)
	if err != nil || answer != nil {
		if answer != nil {
			return *answer, nil
		}
		return none, err
	}
	stack := syncrun.StackOf(prospect.Line, true, nil, wt.Path)
	fl, err := foldLookOf(main, cfg, stack, nil, false)
	if err != nil {
		return none, err
	}
	if fl.Refusal != nil {
		return refused(*fl.Refusal)
	}
	due, _, _, err := foldDue(main, wt.Path, cfg, stack, nil, fl)
	if err != nil {
		return none, err
	}
	if why := needRefusal(syncrun.StepOptions{Probes: opts.Probes}.Prober(), due); why != "" {
		return refused(why)
	}
	return wtsync.ProbeFold(wt.Path, cfg, prospect)
}

// standingWorktree is the worktree the caller stands in, the main checkout
// included.
func standingWorktree(ctx *kit.Context) (repo.Worktree, error) {
	names, err := kit.WorkNames(ctx)
	if err != nil {
		return repo.Worktree{}, err
	}
	wt, _, err := kit.StandingWorktree(ctx, names)
	return wt, err
}

// heldLine reads the line of the branch wt has checked out, as a --stack run
// reads it, against trunk as last fetched, or local trunk without an origin.
func heldLine(ctx *kit.Context, wt repo.Worktree) (wtsync.Line, error) {
	if _, on := repo.OnBranch(wt.Path, wt.Branch); wt.Branch == "" || !on {
		return wtsync.Line{}, fmt.Errorf("%s has no branch checked out, so there is no line to read", wt.Path)
	}
	trunk := "origin/" + ctx.Config.MainBranch
	if _, err := git.Run(ctx.Repo.MainRoot, "rev-parse", "--verify", "--quiet", trunk+"^{commit}"); err != nil {
		trunk = ctx.Config.MainBranch
	}
	return readLine(ctx, wt.Branch, trunk, nil)
}

// onLine is the place in line of the commit rev names, as git reads it in the
// line's worktree.
func onLine(wtPath string, line wtsync.Line, rev string) (int, error) {
	// Resolved, then peeled: a suffix on rev itself would become part of a
	// :/<text> search.
	sha, err := git.Run(wtPath, "rev-parse", "--verify", "--quiet", "--end-of-options", rev)
	if err == nil && sha != "" {
		sha, err = git.Run(wtPath, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	}
	if err != nil || sha == "" {
		return 0, fmt.Errorf("%s is not a commit here, or names more than one", rev)
	}
	for i, c := range line.Commits {
		if c == sha {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s (%s) is not a commit of the line of %s, from its base %s to its tip",
		rev, git.ShortID(sha, 7), line.Branch, git.ShortID(line.Base, 7))
}

// pathInWorktree is p, relative to the directory from unless absolute, as a
// path of the worktree at root. Only the way to the worktree is resolved: what
// p says inside it is taken as written, the name a commit has it under, not
// where a symlink in the checkout leads.
func pathInWorktree(root, from, p string) (string, error) {
	resolved := func(path string) string {
		if r, err := filepath.EvalSymlinks(path); err == nil {
			return r
		}
		return path
	}
	top := resolved(root)
	rel := ""
	if filepath.IsAbs(p) {
		clean := filepath.Clean(p)
		// The highest directory of p that is the worktree, however it is
		// spelled.
		for dir := filepath.Dir(clean); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if resolved(dir) == top {
				rel, _ = filepath.Rel(dir, clean)
			}
		}
	} else if base, err := filepath.Rel(top, resolved(from)); err == nil {
		rel = filepath.Join(base, p)
	}
	if rel == "" || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not a file inside the worktree %s", p, root)
	}
	return filepath.ToSlash(rel), nil
}

func refLine(c lineCommitRef) string {
	return fmt.Sprintf("%s %s  (layer %s)", git.ShortID(c.SHA, 7), kit.OneLine(c.Subject), c.Layer)
}

func spanList(spans []wtsync.LineSpan) string {
	s := make([]string, len(spans))
	for i, span := range spans {
		s[i] = span.String()
	}
	return strings.Join(s, ", ")
}

func printOwner(w io.Writer, res ownerResult) {
	fmt.Fprintf(w, "%s:%s on %s at %s\n", *res.Path, res.Lines, *res.Branch, git.ShortID(*res.Tip, 7))
	for _, o := range res.Owners {
		fmt.Fprintf(w, "  %s  owns lines %s\n", refLine(o.lineCommitRef), spanList(o.Lines))
		if o.FoldsInto != nil {
			fmt.Fprintf(w, "    a fold makes it part of %s\n", refLine(*o.FoldsInto))
		}
	}
	if len(res.Unowned) > 0 {
		fmt.Fprintf(w, "  no commit on the line owns lines %s: its base already has them\n", spanList(res.Unowned))
	}
	if res.Edited {
		fmt.Fprintf(w, "  uncommitted changes are in or beside these lines: this answers for the committed ones\n")
	}
	if in := res.Into; in != nil {
		fmt.Fprintf(w, "  into %s: ", refLine(in.lineCommitRef))
		switch in.Verdict {
		case wtsync.ProbeFolds:
			fmt.Fprintf(w, "folds\n")
		case wtsync.ProbeDoesNotFold:
			fmt.Fprintf(w, "does not fold: %s carries its context\n", refLine(*in.Needs))
		case wtsync.ProbeRefused:
			fmt.Fprintf(w, "refused: %s\n", kit.OneLine(*in.Why))
		default:
			fmt.Fprintf(w, "cannot tell: %s\n", kit.OneLine(*in.Why))
		}
	}
}
