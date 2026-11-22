package syncrun

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// KeepStackPush keeps, for wt sync push <work> --stack, what a --stack run or
// resume may leave unpushed: the moved layers and the line's own branch, with
// the leases the run took and the tips it left. It runs while the run still
// holds the line's lock, so a later run, which has to take it, cannot be
// replaced by this one's save; head is the line's tip as the run left it
// (empty: read now, for a caller that still holds the lock). The record is
// written before anything is pushed; SettleStackPush takes out what the push
// then sent. Nothing is kept for a line not left pushable (a step or check
// failed or is owed, its own branch has no safe push, the record has no
// leases), and a run that moved nothing (refused, restored, nothing to
// rebase) leaves what an earlier one kept: its tips are checked when it is
// pushed, so keeping it longer is safe and deleting it early is what costs.
// A record that cannot be written is said; it never fails the run.
func KeepStackPush(ctx *kit.Context, w io.Writer, s *Stack, targets []PushTarget, head string) {
	if s == nil || s.Restored || s.Refusal != nil {
		return
	}
	main := ctx.Repo.MainRoot
	if head == "" {
		var err error
		if head, err = git.Run(main, "rev-parse", "--verify", "refs/heads/"+s.Branch); err != nil {
			return
		}
	}
	if !movedSomething(s, head) {
		return
	}
	s.kept = keptPush(s, head, targets)
	var err error
	if s.kept == nil {
		err = wtsync.ForgetPushRecord(main, s.Branch)
	} else {
		err = wtsync.WritePushRecord(main, s.kept)
	}
	if err != nil {
		s.kept = nil
		fmt.Fprintf(w, "  note: what is left unpushed could not be kept for a later push (%v)\n", err)
	}
}

// SettleStackPush is the end of the push of a run that KeepStackPush kept a
// record for: what the push sent is taken out of it, and the record goes when
// that was everything. Only the record this run wrote is touched: a later run
// of the line has its own. Nothing is left to say when the push sent it all.
func SettleStackPush(ctx *kit.Context, w io.Writer, s *Stack) {
	if s == nil || s.kept == nil {
		return
	}
	sent := map[string]bool{}
	if s.Push != nil {
		for _, b := range s.Push.Branches {
			sent[b.Branch] = b.Outcome == PushedOK
		}
	}
	var left []wtsync.PushRecordItem
	for _, it := range s.kept.Items {
		if !sent[it.Branch] {
			left = append(left, it)
		}
	}
	if len(left) == len(s.kept.Items) && len(left) > 0 {
		fmt.Fprintf(w, "  ↪ %s left unpushed, with its leases: wt sync push %s --stack pushes it later\n", kit.Count(len(left), "branch"), s.work)
		return
	}
	var next *wtsync.PushRecord
	if len(left) > 0 {
		next = &wtsync.PushRecord{}
		*next = *s.kept
		next.Items = left
	}
	err := wtsync.ReplacePushRecord(ctx.Repo.MainRoot, s.kept, next)
	switch {
	case errors.Is(err, wtsync.ErrPushRecordReplaced):
	case err != nil:
		fmt.Fprintf(w, "  note: the record of what is left for a later push could not be updated (%v)\n", err)
	case next != nil:
		fmt.Fprintf(w, "  ↪ %s left unpushed, with its leases: wt sync push %s --stack pushes it later\n", kit.Count(len(left), "branch"), s.work)
	}
}

// movedSomething is whether the run moved a layer or the line.
func movedSomething(s *Stack, head string) bool {
	return head != s.Head || slices.ContainsFunc(s.Layers, func(l StackLayer) bool { return l.Result != nil && *l.Result == "moved" })
}

// keptPush is what KeepStackPush keeps, nil for nothing.
func keptPush(s *Stack, head string, targets []PushTarget) *wtsync.PushRecord {
	if !slices.ContainsFunc(targets, func(t PushTarget) bool { return t.Branch == s.Branch }) ||
		s.declined != "" || s.top == nil || !s.top.Pushes() || s.Top == nil {
		return nil
	}
	items := append(slices.Clone(s.items), stackItem{BranchPush: *s.top, New: head, Top: true})
	if len(items) == 0 || len(s.items) == 0 && head == s.Head {
		return nil
	}
	rec := &wtsync.PushRecord{Branch: s.Branch, Work: s.work, Epoch: s.epoch, Written: time.Now(),
		Head: s.Head, Base: s.Base, Commits: s.Top.Commits, Subject: s.Top.Subject}
	if s.Top.Worktree != nil {
		rec.Path = *s.Top.Worktree
	}
	for _, it := range items {
		rec.Items = append(rec.Items, wtsync.PushRecordItem{BranchPush: it.BranchPush, New: it.New, Top: it.Top})
	}
	for _, l := range s.Layers {
		switch {
		case l.Held != nil:
			rec.Left = append(rec.Left, wtsync.PushLeft{Branch: l.Branch, Tip: l.OldTip, Why: "held: " + *l.Held})
		case l.NoPushReason != nil && l.Result != nil && *l.Result == "moved":
			rec.Left = append(rec.Left, wtsync.PushLeft{Branch: l.Branch, Tip: str(l.NewTip), Why: "no safe push: " + *l.NoPushReason})
		}
	}
	return rec
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// LaterOptions tunes PushRecorded.
type LaterOptions struct {
	PushOptions
	// DryRun says what would be pushed and moves nothing.
	DryRun bool
}

// PushRecorded is wt sync push <work> --stack: the push a --stack run left
// for later, from the record it kept for the line whose own branch is branch.
// Nothing is read from a remote and nothing fetched: each branch goes over the
// lease the run took. It refuses, before anything is pushed, a line with no
// record, a branch that is no longer at the tip the run left it (somebody
// worked on it since), and a remote that no longer pushes where the run
// recorded. The returned Stack carries the push, for --json.
func PushRecorded(ctx *kit.Context, w io.Writer, work, branch string, opts LaterOptions) (*Stack, error) {
	main := ctx.Repo.MainRoot
	rec, ok, err := wtsync.ReadPushRecord(main, branch)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("nothing recorded to push for %s; rebase with wt sync rebase --stack first", work)
	}
	s := recordedStack(rec)
	refuse := func(why string) (*Stack, error) {
		s.Refusal = jsonout.Strp(why)
		fmt.Fprintf(w, "  ✗ not pushed: %s; nothing was pushed, and the record is kept\n", why)
		return s, errors.New("not pushed: " + why)
	}
	items, why := recordedItems(main, rec)
	if why != "" {
		return refuse(why)
	}
	// The line's own branch is left out when pushing it would create its
	// remote branch, unless asked for.
	isLeftOut := func(it stackItem) bool { return it.Top && it.Creates() && !opts.PushLine }
	pushing := slices.DeleteFunc(slices.Clone(items), isLeftOut)
	fmt.Fprintf(w, "  %s: the push the run of %s left for later, %s\n", work, rec.Written.Local().Format("2006-01-02 15:04"), kit.Count(len(pushing), "branch"))
	for _, l := range rec.Left {
		fmt.Fprintf(w, "  = %s left out by the run: %s\n", l.Branch, l.Why)
	}
	var lineWhy *string
	if len(pushing) < len(items) {
		for _, it := range items {
			if isLeftOut(it) {
				why := LineLeftOutWhy(it.BranchPush, work)
				fmt.Fprintf(w, "  = %s left out: %s\n", it.Branch, why)
				lineWhy = jsonout.Strp(why)
			}
		}
	}
	if opts.DryRun {
		for _, it := range pushing {
			from := "new on " + it.Remote
			if it.Lease != "" {
				from = git.ShortID(it.Lease, 7)
			}
			fmt.Fprintf(w, "  would push %s  %s/%s (%s)  %s → %s\n", it.Branch, it.Remote, it.RemoteBranch, it.Rule, from, git.ShortID(it.New, 7))
		}
		fmt.Fprintf(w, "  --dry-run: nothing pushed\n")
		return s, nil
	}
	if len(pushing) == 0 {
		fmt.Fprintf(w, "  nothing to push, and the record is kept\n")
		return s, nil
	}
	remotes := byRemote(pushing)
	branches := make([]string, len(pushing))
	for i, it := range pushing {
		branches[i] = it.Branch
	}
	if opts.Push != PushAlways {
		if opts.ConfirmPush == nil {
			return refuse("nobody to ask: pass --yes to push")
		}
		if !opts.ConfirmPush(branches) {
			return refuse("not confirmed")
		}
		// The answer took time: the record is read and every tip checked
		// again, and a record that is not the one asked about is not pushed.
		now, ok, err := wtsync.ReadPushRecord(main, branch)
		if err != nil {
			return nil, err
		}
		if !ok || now.Epoch != rec.Epoch || !now.Written.Equal(rec.Written) {
			return refuse("the record changed while the question waited")
		}
		if items, why = recordedItems(main, rec); why != "" {
			return refuse(why)
		}
		pushing = slices.DeleteFunc(slices.Clone(items), isLeftOut)
		remotes = byRemote(pushing)
	}
	fmt.Fprintln(w)
	results, allIn := pushRemotes(ctx, w, remotes, work)
	s.Push = &StackPush{Branches: []StackPushed{}, LineLeftOut: lineWhy}
	pushedOK := map[string]bool{}
	for _, it := range pushing {
		r := results[it.Branch]
		s.Push.Branches = append(s.Push.Branches, r)
		pushedOK[it.Branch] = r.Outcome == PushedOK
	}
	var left []wtsync.PushRecordItem
	for _, it := range rec.Items {
		if !pushedOK[it.Branch] {
			left = append(left, it)
		}
	}
	// Only the record that was pushed from is deleted or rewritten.
	var next *wtsync.PushRecord
	if len(left) > 0 {
		next = &wtsync.PushRecord{}
		*next = *rec
		next.Items = left
	}
	switch err := wtsync.ReplacePushRecord(main, rec, next); {
	case err != nil && len(left) == 0:
		fmt.Fprintf(w, "  note: everything was pushed, but the record could not be removed (%v)\n", err)
	case err != nil:
		fmt.Fprintf(w, "  note: the record could not be rewritten to what is left (%v)\n", err)
	}
	if !allIn {
		return s, errors.New(work + " (stack push failed)")
	}
	return s, nil
}

// recordedItems is the record's branches as a push takes them, and why not
// when it is refused: a branch that is no longer at the tip the run left it
// at, or a remote that no longer pushes where the leases were taken.
func recordedItems(main string, rec *wtsync.PushRecord) ([]stackItem, string) {
	var moved, names []string
	for _, it := range rec.Items {
		tip, err := git.Run(main, "rev-parse", "--verify", "refs/heads/"+it.Branch)
		switch {
		case err != nil:
			moved = append(moved, it.Branch+" is gone")
		case tip != it.New:
			moved = append(moved, fmt.Sprintf("%s is at %s, the run left it at %s", it.Branch, git.ShortID(tip, 7), git.ShortID(it.New, 7)))
		default:
			continue
		}
		names = append(names, it.Branch)
	}
	if len(moved) > 0 {
		return nil, strings.Join(names, ", ") + " moved since the run: " + strings.Join(moved, "; ")
	}
	items := make([]stackItem, len(rec.Items))
	for i, it := range rec.Items {
		items[i] = stackItem{BranchPush: it.BranchPush, New: it.New, Top: it.Top}
	}
	if err := checkEndpoints(main, byRemote(items)); err != nil {
		return nil, err.Error()
	}
	return items, ""
}

// recordedStack is the stack a later push reports: the line as the run read
// it, no layers moved by this push, the ones left out named as skipped.
func recordedStack(rec *wtsync.PushRecord) *Stack {
	s := &Stack{Branch: rec.Branch, Base: rec.Base, Head: rec.Head,
		Layers: []StackLayer{}, Skipped: []StackSkipped{}, SideBranches: []StackSide{}, ContextGaps: []StackContextGap{}, ContextUnchecked: []string{},
		Top: &StackTop{Branch: rec.Branch, OldTip: rec.Head, Commits: rec.Commits, Subject: rec.Subject}, work: rec.Work, epoch: rec.Epoch}
	if rec.Path != "" {
		s.Top.Worktree = jsonout.Strp(rec.Path)
	}
	for _, l := range rec.Left {
		s.Skipped = append(s.Skipped, StackSkipped{Branch: l.Branch, Tip: l.Tip, Reason: l.Why})
	}
	return s
}
