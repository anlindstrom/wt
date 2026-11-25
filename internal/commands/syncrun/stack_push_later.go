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
	if s.kept == nil {
		// Not pushable: it adds nothing, and what earlier runs kept stays.
		// Their tips no longer match the line, so a push refuses until the
		// line is put back (wt sync undo) or the record is dropped (--forget).
		return
	}
	var err error
	if s.kept, err = wtsync.AddPushRun(main, s.kept); err != nil {
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
	all := s.kept.Branches()
	next := s.kept.Without(sent)
	var left []string
	if next != nil {
		left = next.Branches()
	}
	if len(left) == len(all) && len(left) > 0 {
		fmt.Fprintf(w, "  ↪ %s\n", s.laterLine(next, left))
		return
	}
	err := wtsync.ReplacePushRecord(ctx.Repo.MainRoot, s.kept, next)
	switch {
	case errors.Is(err, wtsync.ErrPushRecordReplaced):
	case err != nil:
		fmt.Fprintf(w, "  note: the record of what is left for a later push could not be updated (%v)\n", err)
	case next != nil:
		fmt.Fprintf(w, "  ↪ %s\n", s.laterLine(next, left))
	}
}

// laterLine is what is left for wt sync push --stack, counting the branches
// that push sends: the line's own branch, left out when it would create its
// remote branch, is named with the flag that includes it.
func (s *Stack) laterLine(next *wtsync.PushRecord, left []string) string {
	sends := left
	if lineCreates(next) {
		sends = slices.DeleteFunc(slices.Clone(left), func(b string) bool { return b == next.Branch })
	}
	if len(sends) == 0 {
		return fmt.Sprintf("%s kept for wt sync push %s --stack --push-line", next.Branch, s.work)
	}
	pronoun := "it"
	if len(sends) > 1 {
		pronoun = "them"
	}
	line := fmt.Sprintf("%s: wt sync push %s --stack pushes %s later", s.unpushedLine(sends), s.work, pronoun)
	if len(sends) < len(left) {
		line += fmt.Sprintf("; the line's own branch, %s, goes with --push-line", next.Branch)
	}
	return line
}

// lineCreates is whether the record holds the line's own branch and a push
// would create it on its remote.
func lineCreates(rec *wtsync.PushRecord) bool {
	items, _ := rec.Merged()
	return slices.ContainsFunc(items, func(it wtsync.MergedItem) bool { return it.Top && it.Creates() })
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
// recorded. A record several unpushed runs kept is pushed as one: each branch
// over the oldest run's lease that moved it, to the newest run's tip, and a
// branch whose runs disagree is refused with the whole push. The returned
// Stack carries the push, for --json.
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
	conflicted := []StackConflicted{}
	refuse := func(why string) (*Stack, error) {
		s.Refusal = jsonout.Strp(why)
		if len(conflicted) > 0 {
			// A refusal for conflicted branches says which, for --json.
			s.Push = &StackPush{Branches: []StackPushed{}, Conflicted: conflicted}
		}
		fmt.Fprintf(w, "  ✗ not pushed: %s; nothing was pushed, and the record is kept\n", why)
		return s, errors.New("not pushed: " + why)
	}
	items, merged, conflicts, why := recordedItems(main, rec)
	for _, c := range conflicts {
		conflicted = append(conflicted, StackConflicted{Branch: c.Branch, Why: c.Why})
	}
	// The line's own branch is left out when pushing it would create its
	// remote branch, unless asked for.
	isLeftOut := func(it stackItem) bool { return it.Top && it.Creates() && !opts.PushLine }
	pushing := slices.DeleteFunc(slices.Clone(items), isLeftOut)
	runs := rec.Runs()
	if len(runs) > 1 {
		fmt.Fprintf(w, "  %s: the push %d unpushed runs left for later (%s to %s), %s\n", work, len(runs), runLabel(runs[0].Written), runLabel(rec.Written), kit.Count(len(pushing), "branch"))
	} else {
		fmt.Fprintf(w, "  %s: the push the run of %s left for later, %s\n", work, rec.Written.Local().Format("2006-01-02 15:04"), kit.Count(len(pushing), "branch"))
	}
	for _, l := range rec.LeftOut() {
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
	if opts.DryRun && len(conflicts) > 0 {
		// The refusal says why; what else would go is shown first.
		printWouldPush(w, pushing, merged, len(runs) > 1)
	}
	if why != "" {
		return refuse(why)
	}
	if opts.DryRun {
		printWouldPush(w, pushing, merged, len(runs) > 1)
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
		if !ok || now.Rev != rec.Rev || now.Epoch != rec.Epoch || !now.Written.Equal(rec.Written) {
			return refuse("the record changed while the question waited")
		}
		if items, merged, _, why = recordedItems(main, rec); why != "" {
			return refuse(why)
		}
		pushing = slices.DeleteFunc(slices.Clone(items), isLeftOut)
		remotes = byRemote(pushing)
	}
	fmt.Fprintln(w)
	results, allIn := pushRemotes(ctx, w, remotes, work)
	s.Push = &StackPush{Branches: []StackPushed{}, LineLeftOut: lineWhy, Conflicted: []StackConflicted{}}
	pushedOK := map[string]bool{}
	for _, it := range pushing {
		r := results[it.Branch]
		if m, ok := mergedBy(merged, it.Branch); ok {
			r.From = &StackRunRef{Epoch: m.TipFrom.Epoch, Time: m.TipFrom.Time}
		}
		s.Push.Branches = append(s.Push.Branches, r)
		pushedOK[it.Branch] = r.Outcome == PushedOK
	}
	// Only the record that was pushed from is deleted or rewritten, and what
	// was sent is taken out of every run: a push supersedes what any run
	// recorded for the branch.
	left := rec.Without(pushedOK)
	switch err := wtsync.ReplacePushRecord(main, rec, left); {
	case err != nil && left == nil:
		fmt.Fprintf(w, "  note: everything was pushed, but the record could not be removed (%v)\n", err)
	case err != nil:
		fmt.Fprintf(w, "  note: the record could not be rewritten to what is left (%v)\n", err)
	}
	if !allIn {
		return s, errors.New(work + " (stack push failed)")
	}
	return s, nil
}

// recordedItems is the record's branches as a push takes them (see
// wtsync.PushRecord.Merged), each with the runs it came from, the branches
// the runs disagree on, and why not when the push is refused: a branch that is
// no longer at the tip the newest run left it at, a conflicted branch, or a
// remote that no longer pushes where the leases were taken.
func recordedItems(main string, rec *wtsync.PushRecord) ([]stackItem, []wtsync.MergedItem, []wtsync.PushConflict, string) {
	merged, conflicts := rec.Merged()
	var moved, names, reasons []string
	for _, it := range merged {
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
		reasons = append(reasons, strings.Join(names, ", ")+" moved since the run: "+strings.Join(moved, "; "))
	}
	if len(conflicts) > 0 {
		var parts, cn []string
		for _, c := range conflicts {
			cn = append(cn, c.Branch)
			parts = append(parts, c.Why)
		}
		reasons = append(reasons, strings.Join(cn, ", ")+" conflicted between the unpushed runs: "+strings.Join(parts, "; ")+
			"; wt sync push --stack --forget drops the record, and a fresh --no-push run records the line again")
	}
	items := make([]stackItem, len(merged))
	for i, it := range merged {
		items[i] = stackItem{BranchPush: it.BranchPush, New: it.New, Top: it.Top}
	}
	if len(reasons) > 0 {
		return items, merged, conflicts, strings.Join(reasons, "; ")
	}
	if err := checkEndpoints(main, byRemote(items)); err != nil {
		return nil, merged, conflicts, err.Error()
	}
	return items, merged, conflicts, ""
}

func mergedBy(merged []wtsync.MergedItem, branch string) (wtsync.MergedItem, bool) {
	i := slices.IndexFunc(merged, func(m wtsync.MergedItem) bool { return m.Branch == branch })
	if i < 0 {
		return wtsync.MergedItem{}, false
	}
	return merged[i], true
}

func runLabel(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

// printWouldPush is --dry-run's line for each branch: where it pushes and by
// which rule, lease → new, and, for a record of several runs, the runs the
// lease and the tip are from.
func printWouldPush(w io.Writer, pushing []stackItem, merged []wtsync.MergedItem, several bool) {
	for _, it := range pushing {
		from := "new on " + it.Remote
		if it.Lease != "" {
			from = git.ShortID(it.Lease, 7)
		}
		fmt.Fprintf(w, "  would push %s  %s/%s (%s)  %s → %s", it.Branch, it.Remote, it.RemoteBranch, it.Rule, from, git.ShortID(it.New, 7))
		if m, ok := mergedBy(merged, it.Branch); ok && several {
			if m.LeaseFrom == m.TipFrom {
				fmt.Fprintf(w, "  [run %d, %s]", m.TipFrom.Epoch, runLabel(m.TipFrom.Time))
			} else {
				fmt.Fprintf(w, "  [lease from run %d, %s; tip from run %d, %s]", m.LeaseFrom.Epoch, runLabel(m.LeaseFrom.Time), m.TipFrom.Epoch, runLabel(m.TipFrom.Time))
			}
		}
		fmt.Fprintln(w)
	}
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
	for _, l := range rec.LeftOut() {
		s.Skipped = append(s.Skipped, StackSkipped{Branch: l.Branch, Tip: l.Tip, Reason: l.Why})
	}
	return s
}

// unpushedLine is how many branches the record leaves for a later push, and
// how many of them an earlier run left there and this run did not move.
func (s *Stack) unpushedLine(left []string) string {
	own := map[string]bool{}
	for _, it := range s.kept.Run().Items {
		own[it.Branch] = true
	}
	earlier := 0
	for _, b := range left {
		if !own[b] {
			earlier++
		}
	}
	line := kit.Count(len(left), "branch", "branches") + " left unpushed"
	switch {
	case earlier == 1 && len(left) == 1:
		line += " (from an earlier run)"
	case earlier == len(left) && earlier > 0:
		line += " (all from earlier runs)"
	case earlier > 0:
		line += fmt.Sprintf(" (%d of them from earlier runs)", earlier)
	}
	if len(left) == 1 {
		return line + ", with its lease"
	}
	return line + ", with their leases"
}
