package syncrun

import (
	"errors"
	"fmt"
	"io"
	"maps"
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
		s.kept, s.unsaved = nil, true
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
	// Another push or a --forget may have taken the record since the run let
	// the line's lock go: the result names it only once it is checked, or
	// rewritten, under the records' lock.
	unchanged := len(left) == len(all) && len(left) > 0
	var err error
	if unchanged {
		err = wtsync.PushRecordStands(ctx.Repo.MainRoot, s.kept)
	} else {
		err = wtsync.ReplacePushRecord(ctx.Repo.MainRoot, s.kept, next)
	}
	// Whether the record still owes a branch of this run's.
	mine := next != nil && slices.ContainsFunc(next.Runs(), func(r wtsync.PushRun) bool { return r.Epoch == s.kept.Epoch })
	s.settled = true
	if err != nil {
		if s.Push != nil {
			s.Push.LineLeftOut = nil
		}
		why := "changed after this run saved it"
		if !errors.Is(err, wtsync.ErrPushRecordReplaced) {
			why = "could not be checked"
			doing := "updated"
			if unchanged {
				doing = "read"
			}
			fmt.Fprintf(w, "  note: the record of what is left for a later push could not be %s (%v)\n", doing, err)
		}
		if mine {
			s.recordNote = fmt.Sprintf("the line's push record %s; wt sync push %s --stack --dry-run shows what is left to push now", why, s.work)
		}
		return
	}
	// A run whose own branches all went has nothing left to name, whatever
	// earlier runs still hold.
	if mine {
		s.nameLater(next, left)
	}
	switch {
	case next != nil:
		fmt.Fprintf(w, "  ↪ %s\n", s.laterLine(next, left))
	case len(s.nowhere) > 0:
		fmt.Fprintf(w, "  ↪ %s with nowhere to push: %s\n", kit.Count(len(s.nowhere), "moved layer"), strings.Join(s.nowhere, ", "))
	}
}

// recoveryNote is what a reader of the result has to be told about a record
// the result cannot name, "" when there is nothing.
func (s *Stack) recoveryNote() string {
	if s.kept != nil && !s.settled {
		return fmt.Sprintf("the run ended before it settled its push; wt sync push %s --stack --dry-run shows what is left to push", s.work)
	}
	return s.recordNote
}

// laterLine is what is left for wt sync push --stack, counting the branches
// that push sends: the line's own branch, left out when it would create its
// remote branch, is named with the flag that includes it.
func (s *Stack) laterLine(next *wtsync.PushRecord, left []string) string {
	sends := laterSends(next, left)
	if len(sends) == 0 {
		return fmt.Sprintf("%s kept for wt sync push %s --stack --push-line", next.Branch, s.work) + s.nowhereClause()
	}
	line := fmt.Sprintf("%s: wt sync push %s --stack pushes %s later", s.unpushedLine(sends), s.work, itOrThem(len(sends)))
	if len(sends) < len(left) {
		line += fmt.Sprintf("; the line's own branch, %s, goes with --push-line", next.Branch)
	}
	return line + s.nowhereClause()
}

// nowhereClause ends the line that counts what is left with the moved layers
// that count leaves out, as nothing says where they push; "" with none.
func (s *Stack) nowhereClause() string {
	if len(s.nowhere) == 0 {
		return ""
	}
	return fmt.Sprintf("; %d more with nowhere to push: %s", len(s.nowhere), strings.Join(s.nowhere, ", "))
}

// laterSends is what of left a plain wt sync push --stack sends: all of it
// but the line's own branch when that would create its remote branch.
func laterSends(next *wtsync.PushRecord, left []string) []string {
	if !lineCreates(next) {
		return left
	}
	return slices.DeleteFunc(slices.Clone(left), func(b string) bool { return b == next.Branch })
}

// nameLater is what the run's result says of rec's branches left: the
// command that pushes them, never with --push-line, which publishes the
// line's own branch and is for a person to add. With that branch alone left
// there is no command, and the push says it is left out, unless it tried that
// branch.
func (s *Stack) nameLater(rec *wtsync.PushRecord, left []string) {
	if len(laterSends(rec, left)) > 0 {
		s.pushLater = []string{"wt", "sync", "push", s.work, "--stack"}
		return
	}
	items, _ := rec.Merged()
	for _, it := range items {
		tried := s.Push != nil && slices.ContainsFunc(s.Push.Branches, func(b StackPushed) bool { return b.Branch == it.Branch })
		if !it.Top || tried {
			continue
		}
		if s.Push == nil {
			s.Push = &StackPush{Branches: []StackPushed{}, Conflicted: []StackConflicted{}}
		}
		s.Push.LineLeftOut = jsonout.Strp(LineLeftOutWhy(it.BranchPush, s.work))
	}
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
	rec.LayersRead = true
	for _, l := range s.Layers {
		pl := wtsync.PushLayer{Branch: l.Branch, Result: str(l.Result), Tip: l.OldTip}
		if pl.Result == wtsync.MoveMoved {
			pl.Tip = str(l.NewTip)
		}
		rec.Layers = append(rec.Layers, pl)
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
// Nothing is fetched: each branch goes over a lease a run took. A remote is
// read for a branch whose runs took different ones, and by a dry run, which
// holds every branch to what git will. It refuses, before anything is pushed, a line with no record, a branch that is no longer
// at the tip the run left it (somebody worked on it since), and a remote that
// no longer pushes where the run recorded. A record several unpushed runs kept
// is pushed as one: each branch to the newest run's tip over the oldest run's
// lease that moved it, or over the newest's where the leases differ and the
// remote is at it; any other branch whose runs disagree is refused with the
// whole push. The returned Stack carries the push, for --json.
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
		if len(conflicted) > 0 {
			return s, saidRefusal{why}
		}
		return s, errors.New("not pushed: " + why)
	}
	read := recordedItems(ctx, rec)
	for _, c := range read.conflicts {
		conflicted = append(conflicted, StackConflicted{Branch: c.Branch, Why: c.Why})
	}
	items, merged, why := read.items, read.merged, read.why
	// The line's own branch is left out when pushing it would create its
	// remote branch, unless asked for.
	isLeftOut := func(it stackItem) bool { return it.Top && it.Creates() && !opts.PushLine }
	pushing := slices.DeleteFunc(slices.Clone(items), isLeftOut)
	runs := rec.Runs()
	if len(runs) > 1 {
		fmt.Fprintf(w, "  %s: the push %d unpushed runs left for later (%s to %s), %s\n", work, len(runs), runLabel(runs[0].Written), runLabel(rec.Written), kit.Count(len(pushing), "branch", "branches"))
	} else {
		fmt.Fprintf(w, "  %s: the push the run of %s left for later, %s\n", work, rec.Written.Local().Format("2006-01-02 15:04"), kit.Count(len(pushing), "branch", "branches"))
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
	if opts.DryRun {
		for _, c := range read.conflicts {
			fmt.Fprintf(w, "  ✗ %s: %s\n", c.Branch, c.Why)
		}
	}
	if why != "" {
		return refuse(why)
	}
	if opts.DryRun {
		going, err := predictPushes(w, main, byRemote(pushing))
		if notice := read.staleNotice(true, going); notice != "" {
			fmt.Fprintf(w, "  %s\n", notice)
		}
		printWouldPush(w, slices.DeleteFunc(slices.Clone(pushing), func(it stackItem) bool { return !going[it.Remote] }), merged, len(runs) > 1)
		fmt.Fprintf(w, "  --dry-run: nothing pushed\n")
		return s, err
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
		again := recordedItems(ctx, rec)
		if again.why != "" {
			conflicted = conflicted[:0]
			for _, c := range again.conflicts {
				conflicted = append(conflicted, StackConflicted{Branch: c.Branch, Why: c.Why})
			}
			return refuse(again.why)
		}
		read, items, merged = again, again.items, again.merged
		pushing = slices.DeleteFunc(slices.Clone(items), isLeftOut)
		remotes = byRemote(pushing)
	}
	fmt.Fprintln(w)
	if notice := read.staleNotice(false, nil); notice != "" {
		fmt.Fprintf(w, "  %s\n", notice)
	}
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

// predictPushes asks git for a dry run of each remote's atomic push, over the
// leases and refspecs the push would carry. It says what stops each branch of
// one that would fail, and returns the remotes whose push would go, with an
// error for the others.
func predictPushes(w io.Writer, main string, remotes map[string][]stackItem) (map[string]bool, error) {
	going := map[string]bool{}
	var failing []string
	for _, remote := range slices.Sorted(maps.Keys(remotes)) {
		group := remotes[remote]
		err := wtsync.PushAtomicDryRun(main, group[0].Endpoint, atomicRefs(group), kit.NetworkTimeout)
		if err == nil {
			going[remote] = true
			continue
		}
		reasons := refusals(err)
		why := pushReason(err)
		if strings.Contains(err.Error(), noAtomic) {
			why = "it " + noAtomic + " push"
		}
		for _, it := range group {
			switch r := reasons[it.RemoteBranch]; {
			case len(reasons) == 0:
				fmt.Fprintf(w, "  ✗ %s: the push to %s would fail: %s\n", it.Branch, remote, why)
			case r == "stale info":
				fmt.Fprintf(w, "  ✗ %s: %s/%s is not at %s, the lease the push goes over (git: stale info)\n", it.Branch, remote, it.RemoteBranch, leaseWord(it.Lease))
			case r == "" || r == "atomic push failed":
				fmt.Fprintf(w, "  ✗ %s: not pushed, as %s takes its branches in one atomic push\n", it.Branch, remote)
			default:
				fmt.Fprintf(w, "  ✗ %s: %s would refuse it: %s\n", it.Branch, remote, r)
			}
		}
		if len(reasons) > 0 {
			why = "git rejects it"
		}
		failing = append(failing, "to "+remote+", "+why)
	}
	if len(failing) > 0 {
		return going, errors.New("the push would fail: " + strings.Join(failing, "; "))
	}
	return going, nil
}

// saidRefusal is a refusal PushRecorded has printed: it ends the command
// with a failure and is not printed a second time.
type saidRefusal struct{ why string }

func (e saidRefusal) Error() string { return "not pushed: " + e.why }
func (e saidRefusal) Unwrap() error { return kit.ExitError{Code: 1} }

// recordedPush is what a record comes to as a push, read now.
type recordedPush struct {
	items  []stackItem
	merged []wtsync.MergedItem
	// conflicts are the branches the push is refused for because the runs
	// disagree about them.
	conflicts []wtsync.PushConflict
	// stale are the branches whose push goes over a newer run's lease in place
	// of older ones, each remote read to be at it.
	stale []wtsync.MergedItem
	// why is why the push is refused, "" when it goes.
	why string
}

// staleNotice says whose leases replace the stale ones, "" when none do. A
// remote goes marks out, when it is not nil, is one whose push does not go.
func (p recordedPush) staleNotice(dryRun bool, goes map[string]bool) string {
	var runs []int64
	for _, it := range p.stale {
		if (goes == nil || goes[it.Remote]) && !slices.Contains(runs, it.LeaseFrom.Epoch) {
			runs = append(runs, it.LeaseFrom.Epoch)
		}
	}
	slices.Sort(runs)
	if len(runs) == 0 {
		return ""
	}
	verb := "pushing over"
	if dryRun {
		verb = "would push over"
	}
	matched, whose := fmt.Sprintf("run %d", runs[0]), fmt.Sprintf("run %d's", runs[0])
	if n := len(runs); n > 1 {
		names := make([]string, n)
		for i, e := range runs {
			names[i] = fmt.Sprint(e)
		}
		matched, whose = "runs "+strings.Join(names[:n-1], ", ")+" and "+names[n-1], "their"
	}
	return fmt.Sprintf("older runs' leases are stale: the remote matches %s; %s %s leases", matched, verb, whose)
}

// recordedItems is the record's branches as a push takes them (see
// wtsync.PushRecord.Merged), each with the runs it came from, and why not when
// the push is refused: the runs push a branch to different places, a branch is
// no longer at the tip the newest run left it at, a remote no longer pushes
// where the leases were taken, or, for a branch whose runs took different
// leases, the remote cannot be read or is not at the newest run's.
func recordedItems(ctx *kit.Context, rec *wtsync.PushRecord) recordedPush {
	main := ctx.Repo.MainRoot
	merged, conflicts := rec.Merged()
	var moved, names, reasons []string
	if n := len(conflicts); n > 0 {
		reasons = append(reasons, fmt.Sprintf("%s: the recorded runs push %s to different remote branches or endpoints; %s",
			kit.Count(n, "branch", "branches"), itOrThem(n), forgetAndRunAgain(rec.Work)))
	}
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
	reasons = append(reasons, unsentLayers(ctx, rec, merged, conflicts)...)
	if unread := runsWithoutLayers(rec); len(unread) > 0 {
		reasons = append(reasons, "the record of "+strings.Join(unread, ", ")+" was written by a wt that did not keep the line's layers, so it cannot show that every moved layer is in it; push the layers it names by hand over the leases it recorded, or wt sync push "+rec.Work+" --stack --forget drops the record, and a fresh --no-push run records the line again")
	}
	out := recordedPush{items: make([]stackItem, len(merged)), merged: merged, conflicts: conflicts}
	for i, it := range merged {
		out.items[i] = stackItem{BranchPush: it.BranchPush, New: it.New, Top: it.Top}
	}
	if len(conflicts) > 0 {
		reasons = append(reasons, "wt sync push "+rec.Work+" --stack --dry-run lists them")
	}
	if len(reasons) > 0 {
		out.why = strings.Join(reasons, "; ")
		return out
	}
	if err := checkEndpoints(main, byRemote(out.items)); err != nil {
		out.items, out.why = nil, err.Error()
		return out
	}
	out.settleLeases(main, rec.Work)
	return out
}

// settleLeases reads from their remotes the branches whose runs took
// different leases, all of them before anything is pushed: each must be at
// the newest run's, which a push made between the runs explains. The push is
// still leased on that commit, so git refuses a remote that moves after this.
func (p *recordedPush) settleLeases(main, work string) {
	disputed := map[string][]wtsync.MergedItem{}
	for _, it := range p.merged {
		if it.LeasesDiffer != "" {
			disputed[it.Remote] = append(disputed[it.Remote], it)
		}
	}
	var off, unread int
	var unreadWhy []string
	for _, remote := range slices.Sorted(maps.Keys(disputed)) {
		group := disputed[remote]
		names := make([]string, len(group))
		for i, it := range group {
			names[i] = it.RemoteBranch
		}
		at, err := wtsync.RemoteRefs(main, group[0].Endpoint, names, kit.NetworkTimeout)
		if err != nil {
			unreadWhy = append(unreadWhy, fmt.Sprintf("%s could not be read (%s)", remote, pushReason(err)))
		}
		for _, it := range group {
			switch {
			case err != nil:
				unread++
				p.conflicts = append(p.conflicts, wtsync.PushConflict{Branch: it.Branch,
					Why: fmt.Sprintf("the leases differ (%s), and %s could not be read", it.LeasesDiffer, remote)})
			case at[it.RemoteBranch] != it.Lease:
				off++
				p.conflicts = append(p.conflicts, wtsync.PushConflict{Branch: it.Branch,
					Why: fmt.Sprintf("the leases differ (%s), and %s/%s is %s", it.LeasesDiffer, remote, it.RemoteBranch, leaseWhere(at[it.RemoteBranch]))})
			default:
				p.stale = append(p.stale, it)
			}
		}
	}
	const differ = "the recorded runs' leases differ and"
	list := "wt sync push " + work + " --stack --dry-run lists them"
	switch {
	case off > 0:
		p.why = fmt.Sprintf("%s: %s the remote is not at the newest run's", kit.Count(off, "branch", "branches"), differ)
		if unread > 0 {
			p.why += fmt.Sprintf("; %d more could not be checked, as %s", unread, strings.Join(unreadWhy, " and "))
		}
		p.why += "; " + forgetAndRunAgain(work) + "; " + list
	case unread > 0:
		p.why = fmt.Sprintf("%s: %s %s; push again once it can be read; %s", kit.Count(unread, "branch", "branches"), differ, strings.Join(unreadWhy, " and "), list)
	}
	if p.why != "" {
		p.stale = nil
	}
}

func leaseWhere(at string) string {
	if at == "" {
		return "absent"
	}
	return "at " + git.ShortID(at, 7)
}

func forgetAndRunAgain(work string) string {
	return "wt sync push " + work + " --stack --forget, then a fresh --no-push run"
}

func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// unsentLayers is why the layers the runs name that the push does not send
// stop it: each such layer, unchanged by the runs or sent by an earlier push
// of the record, must stand at the tip the newest run naming it left it at
// and level with its remote branch, or the layers pushed above it would leave
// the stack split on the remote. A layer a run left out is named by the push
// instead, and one with nowhere to push or no remote branch yet has nothing
// to be level with.
func unsentLayers(ctx *kit.Context, rec *wtsync.PushRecord, merged []wtsync.MergedItem, conflicts []wtsync.PushConflict) []string {
	main := ctx.Repo.MainRoot
	own := wtsync.ReadOwnOrUnknown(main, ctx.Config.MainBranch)
	var reasons []string
	seen := map[string]bool{}
	runs := rec.Runs()
	for i := len(runs) - 1; i >= 0; i-- {
		for _, l := range runs[i].Layers {
			if seen[l.Branch] {
				continue
			}
			seen[l.Branch] = true
			// Left out by the newest run that names it: held, or moved with no
			// safe push. What an older run left out does not count.
			leftNow := l.Result == wtsync.MoveHeld || slices.ContainsFunc(runs[i].Left, func(p wtsync.PushLeft) bool { return p.Branch == l.Branch })
			conflicted := slices.ContainsFunc(conflicts, func(c wtsync.PushConflict) bool { return c.Branch == l.Branch })
			if l.Branch == rec.Branch || leftNow || conflicted || slices.ContainsFunc(merged, func(m wtsync.MergedItem) bool { return m.Branch == l.Branch }) {
				continue
			}
			way := "push " + l.Branch + " first, then push again, or wt sync push " + rec.Work + " --stack --forget drops the record"
			if why := unsentLayer(ctx, own, l, "the run of "+runLabel(runs[i].Written),
				"the runs left it unchanged, so it was already off its remote branch before them", way); why != "" {
				reasons = append(reasons, why)
			}
		}
	}
	return reasons
}

// unsentLayer is why layer l, which a push of the layers above it does not
// send, stops that push, "" when it does not: it is no longer at the tip
// run left it at, or it differs from its remote branch, so the push would
// leave the stack split on the remote. A layer with nowhere to push, or no
// remote branch yet, has nothing on the remote to split; one whose remote
// branch cannot be read is refused. unchanged says why an unchanged layer is
// off its remote, and way how to go on.
func unsentLayer(ctx *kit.Context, own *wtsync.Own, l wtsync.PushLayer, run, unchanged, way string) string {
	local, err := git.Run(ctx.Repo.MainRoot, "rev-parse", "--verify", "refs/heads/"+l.Branch)
	if err != nil || local != l.Tip {
		return fmt.Sprintf("%s is no longer at %s, where %s left it", l.Branch, git.ShortID(l.Tip, 7), run)
	}
	tracking, none, why := own.Destination(l.Branch)
	switch {
	case none:
		return ""
	case why != "":
		return fmt.Sprintf("%s cannot be compared with its remote branch (%s), so the push cannot show the stack stays whole on the remote", l.Branch, why)
	}
	remote, ok, rerr := ctx.Repo.ResolveRef(tracking)
	if rerr != nil {
		return fmt.Sprintf("%s cannot be compared with %s: %v", l.Branch, strings.TrimPrefix(tracking, "refs/remotes/"), rerr)
	}
	if !ok || remote == local {
		return ""
	}
	what := "a run moved it and the record holds no push for it"
	if l.Result == wtsync.MoveUnchanged {
		what = unchanged
	}
	return fmt.Sprintf("%s differs from %s, and the push does not send it (%s): pushing the layers above it would leave the stack split on the remote; %s",
		l.Branch, strings.TrimPrefix(tracking, "refs/remotes/"), what, way)
}

// runsWithoutLayers is the runs of the record, by label, that did not keep
// the line's layers.
func runsWithoutLayers(rec *wtsync.PushRecord) []string {
	var out []string
	for _, run := range rec.Runs() {
		if !run.LayersRead {
			out = append(out, "the run of "+runLabel(run.Written))
		}
	}
	return out
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
