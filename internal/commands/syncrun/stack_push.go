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

// What came of one branch of a stack's push, for --json.
const (
	// PushedOK: the remote branch is at the new commit.
	PushedOK = "pushed"
	// PushedLeaseFailed: the remote branch is no longer at the commit the
	// run took the lease on; somebody pushed to it since it was fetched.
	PushedLeaseFailed = "leaseFailed"
	// PushedNotSent: nothing on its remote moved: another branch of the same
	// atomic push failed, or the remote refused the push as a whole.
	PushedNotSent = "notSent"
	// PushedUnknown: the push ended in a transport failure and the remote's
	// refs could not say where the branch is.
	PushedUnknown = "unknown"
)

// StackPush is, for a --stack run that pushed, what each pushed branch came
// to: every moved layer and the line's own branch that had somewhere to push,
// with the lease the run took and the commit it pushed. One atomic push per
// remote; null for a run that pushed nothing.
type StackPush struct {
	Branches []StackPushed `json:"branches"`
	// LineLeftOut is why the line's own branch is not in Branches, when it
	// was left out; null otherwise.
	LineLeftOut *string `json:"lineLeftOut"`
	// Conflicted are the branches a push from a record of several runs
	// refused for: the runs disagree about the branch's lease or where it
	// pushes. Empty for a run's own push and when none did.
	Conflicted []StackConflicted `json:"conflicted"`
}

// StackConflicted is a branch whose recorded runs cannot be one push.
type StackConflicted struct {
	Branch string `json:"branch"`
	Why    string `json:"why"`
}

// StackRunRef names a run: its epoch and when it was written.
type StackRunRef struct {
	Epoch int64     `json:"epoch"`
	Time  time.Time `json:"time"`
}

// StackPushed is one branch of a stack's push.
type StackPushed struct {
	Branch       string `json:"branch"`
	Remote       string `json:"remote"`
	RemoteBranch string `json:"remoteBranch"`
	// Old is the commit the push was leased on, null for a branch that must
	// not exist on the remote yet; New is the commit pushed.
	Old     *string `json:"old"`
	New     string  `json:"new"`
	Outcome string  `json:"outcome"`
	// Rule is what decided the remote branch: recorded, or its own name.
	Rule string `json:"rule"`
	// Why is, for an outcome other than pushed, what git said.
	Why *string `json:"why"`
	// From is, for the push of a record kept by several runs
	// (wt sync push --stack), the run the pushed tip is from: the newest that
	// moved the branch. Null for a run's own push.
	From *StackRunRef `json:"from"`
}

// LineLeftOutWhy is why a stack push leaves the line's own branch out, and the
// flag that includes it.
func LineLeftOutWhy(bp wtsync.BranchPush, work string) string {
	return fmt.Sprintf("pushing it would create %s/%s on %s; wt sync push %s --stack --push-line, or --push-line on the run, pushes it", bp.Remote, bp.RemoteBranch, bp.Remote, work)
}

// stackItem is one branch that goes into a stack's push.
type stackItem struct {
	wtsync.BranchPush
	New string
	// Top says it is the line's own branch.
	Top bool
}

// pushOf is where branch pushes, from the leases the record holds: none is
// read afresh, since a fresh reading is a lease on whatever was fetched since.
func pushOf(rec *wtsync.StackRecord, branch string) wtsync.BranchPush {
	for _, bp := range rec.Pushes {
		if bp.Branch == branch {
			return bp
		}
	}
	return wtsync.BranchPush{Branch: branch, Why: "the run recorded no lease for it"}
}

// PushesOf is where each layer of line and its own branch push, and the
// lease each push takes, read now: a run keeps it in its record before it
// moves anything.
func PushesOf(own *wtsync.Own, line wtsync.Line) []wtsync.BranchPush {
	out := make([]wtsync.BranchPush, 0, len(line.Layers)+1)
	for _, l := range line.Layers {
		out = append(out, own.BranchPush(l.Branch, false))
	}
	return append(out, own.BranchPush(line.Branch, true))
}

// HasPush reports that a stack push has something to say: a layer was moved
// and has somewhere safe to push, the line's own branch has, or the push is
// refused as a whole.
func (s *Stack) HasPush() bool {
	return len(s.items) > 0 || s.declined != "" || s.top != nil && s.top.Pushes()
}

// PushStack is the end of a --stack run or resume that pushes: it pushes the
// layers the run moved and the line's own branch, one atomic push per remote,
// each branch over a lease on the commit the run recorded for its remote
// branch. A held layer, and a layer with nowhere safe to push, are left out;
// they were named when the layers moved. Anything that would leave the stack
// half pushed refuses the whole push instead: the line was not left pushable
// (a check or step failed or is owed), the record has no leases, the line's
// own branch has nowhere safe to push, or a remote no longer means what it
// did. Nothing is pushed then, and the commands are printed as they are
// without a push. It pushes only when told to or when the person says yes;
// with --no-push, or with nobody to ask, it changes nothing. The targets
// returned are for OfferPush, with the mode to offer them in.
func PushStack(ctx *kit.Context, w io.Writer, mode PushMode, ask PushOptions, s *Stack, targets []PushTarget) (rest []PushTarget, restMode PushMode, pushed, failed []string) {
	rest, restMode = targets, mode
	asking := mode == PushAsk && ask.ConfirmPush != nil
	if s == nil || mode == PushNever || mode == PushAsk && !asking {
		return
	}
	// refuse is the whole push declined: the targets are printed, not asked
	// about or pushed, and a push that was asked for is a failure.
	refuse := func(why string) ([]PushTarget, PushMode, []string, []string) {
		fmt.Fprintf(w, "\n  ✗ stack not pushed: %s; nothing was pushed, and the push of each branch is the command printed for it\n", why)
		var f []string
		if mode == PushAlways {
			f = []string{s.work + " (stack not pushed)"}
		}
		return targets, PushNever, nil, f
	}
	var top *PushTarget
	for i := range targets {
		if targets[i].Branch == s.Branch {
			top = &targets[i]
		}
	}
	switch {
	case top == nil:
		// finishRebase did not offer the line: a deferred step failed, a
		// check is owed, or the completion errored.
		return refuse("the line " + s.Branch + " was not left pushable: a step or check of it failed or is owed")
	case s.declined != "":
		return refuse(s.declined)
	case s.top == nil || !s.top.Pushes():
		why := "no lease was recorded"
		if s.top != nil {
			why = s.top.Why
		}
		return refuse(s.Branch + " has no safe push: " + why)
	}
	head, err := git.Run(ctx.Repo.MainRoot, "rev-parse", "--verify", "refs/heads/"+top.Branch)
	if err != nil {
		return refuse("the tip of " + top.Branch + " could not be read: " + pushReason(err))
	}
	items, leftOut, why := s.pushItems(ctx, head, ask.PushLine)
	if why != "" {
		return refuse(why)
	}
	remotes := byRemote(items)
	if err := checkEndpoints(ctx.Repo.MainRoot, remotes); err != nil {
		return refuse(err.Error())
	}
	rest = slices.DeleteFunc(slices.Clone(targets), func(t PushTarget) bool { return t == *top })
	fmt.Fprintln(w)
	if leftOut {
		fmt.Fprintf(w, "  = %s left out: %s\n", s.Branch, LineLeftOutWhy(*s.top, s.work))
		if len(items) == 0 {
			return rest, restMode, nil, nil
		}
	}
	if asking {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Branch
		}
		if !ask.ConfirmPush(names) {
			// No to all of them: the commands are printed, and the line's own
			// branch is not asked about a second time.
			return targets, PushNever, nil, nil
		}
		// A fetch while the question waited may have moved a remote branch;
		// the push still goes to the endpoints recorded.
		if _, _, why := s.pushItems(ctx, head, ask.PushLine); why != "" {
			return refuse(why)
		}
	}
	results, allIn := pushRemotes(ctx, w, remotes, s.work)
	// In item order, whatever the remotes: layers bottom first, then the line.
	s.Push = &StackPush{Branches: []StackPushed{}, Conflicted: []StackConflicted{}}
	if leftOut {
		s.Push.LineLeftOut = jsonout.Strp(LineLeftOutWhy(*s.top, s.work))
	}
	for _, it := range items {
		s.Push.Branches = append(s.Push.Branches, results[it.Branch])
	}
	if results[s.Branch].Outcome == PushedOK {
		pushed = append(pushed, top.Work)
	}
	if !allIn {
		failed = append(failed, s.work+" (stack push failed)")
	}
	return rest, restMode, pushed, failed
}

// pushItems is what the push of a run sends, whether the line's own branch
// is left out because pushing it would create its remote branch, and why the
// push refuses, "" when it does not. It is the layers the run moved and the
// line's own branch; or, when the record holds earlier runs of the line that
// nobody pushed, everything the record holds, as wt sync push --stack sends
// it and under the same checks, so a layer only an earlier run moved is not
// left behind on the remote (wt#223). A line with a record this run is not in
// (it could not be added) refuses: this run's branches alone could leave
// what the record holds behind.
func (s *Stack) pushItems(ctx *kit.Context, head string, pushLine bool) (items []stackItem, leftOut bool, why string) {
	if s.kept == nil {
		if why := s.unkeptRecord(ctx); why != "" {
			return nil, false, why
		}
	}
	if s.kept != nil && len(s.kept.Earlier) > 0 {
		all, _, _, why := recordedItems(ctx, s.kept)
		if why != "" {
			return nil, false, why
		}
		items = slices.DeleteFunc(all, func(it stackItem) bool { return it.Top && it.Creates() && !pushLine })
		return items, len(items) < len(all), ""
	}
	items = slices.Clone(s.items)
	leftOut = s.top.Creates() && !pushLine
	if !leftOut {
		items = append(items, stackItem{BranchPush: *s.top, New: head, Top: true})
	}
	return items, leftOut, s.unchangedOffRemote(ctx)
}

// unkeptRecord is why a run whose own record was not kept may not push, ""
// when nothing is recorded for the line: the record earlier runs left holds
// pushes still owed, or cannot be read.
func (s *Stack) unkeptRecord(ctx *kit.Context) string {
	rec, ok, err := wtsync.ReadPushRecord(ctx.Repo.MainRoot, s.Branch)
	switch {
	case err != nil:
		return "the record of what earlier runs left unpushed cannot be read (" + err.Error() + "), so the push cannot show it sends every layer they moved"
	case !ok:
		return ""
	}
	return "this run could not be added to the record of what earlier runs left unpushed (" + strings.Join(rec.Branches(), ", ") +
		"), so pushing only this run's branches could leave theirs behind on the remote; wt sync undo " + s.work + " puts this run back, and wt sync push " + s.work + " --stack then pushes the record"
}

// unchangedOffRemote is why a layer the run left unchanged stops the push of
// the layers above it, "" when none does: one not level with its remote
// branch would leave the stack split on the remote (unsentLayer).
func (s *Stack) unchangedOffRemote(ctx *kit.Context) string {
	own := wtsync.ReadOwnOrUnknown(ctx.Repo.MainRoot, ctx.Config.MainBranch)
	for _, l := range s.Layers {
		if l.Result == nil || *l.Result != wtsync.MoveUnchanged {
			continue
		}
		pl := wtsync.PushLayer{Branch: l.Branch, Result: wtsync.MoveUnchanged, Tip: l.OldTip}
		way := "push " + l.Branch + " first, then wt sync push " + s.work + " --stack"
		if why := unsentLayer(ctx, own, pl, "this run", "the run left it unchanged, so it was already off its remote branch before it", way); why != "" {
			return why
		}
	}
	return ""
}

// checkEndpoints holds each remote to what the leases were recorded for: it
// has one push URL, and it is the one every branch of it was recorded with.
func checkEndpoints(main string, remotes map[string][]stackItem) error {
	for _, remote := range slices.Sorted(maps.Keys(remotes)) {
		now, err := wtsync.PushEndpoint(main, remote)
		if err != nil {
			return err
		}
		for _, it := range remotes[remote] {
			if it.Endpoint != now {
				return fmt.Errorf("%s pushed to %s when the run took its leases and pushes to %s now", remote, it.Endpoint, now)
			}
		}
	}
	return nil
}

// pushRemotes is one atomic push per remote, in remote name order: what came
// of each branch, and whether every one is at its new commit.
func pushRemotes(ctx *kit.Context, w io.Writer, remotes map[string][]stackItem, work string) (map[string]StackPushed, bool) {
	if len(remotes) > 1 {
		fmt.Fprintf(w, "  the branches push to %d remotes: one atomic push per remote; a push is atomic within its remote only\n", len(remotes))
	}
	results := map[string]StackPushed{}
	allIn := true
	for _, remote := range slices.Sorted(maps.Keys(remotes)) {
		got, ok := pushGroup(ctx, w, remote, remotes[remote], work)
		allIn = ok && allIn
		maps.Copy(results, got)
	}
	return results, allIn
}

func byRemote(items []stackItem) map[string][]stackItem {
	out := map[string][]stackItem{}
	for _, it := range items {
		out[it.Remote] = append(out[it.Remote], it)
	}
	return out
}

// pushGroup is the one atomic push to remote: every ref carries a lease on
// the commit the run recorded, and git moves all of them or none. It returns
// what came of each branch and whether every one is at its new commit.
func pushGroup(ctx *kit.Context, w io.Writer, remote string, group []stackItem, work string) (map[string]StackPushed, bool) {
	refs := make([]wtsync.AtomicRef, len(group))
	for i, it := range group {
		refs[i] = wtsync.AtomicRef{RemoteBranch: it.RemoteBranch, Lease: it.Lease, New: it.New}
	}
	noun := "branches"
	if len(group) == 1 {
		noun = "branch"
	}
	fmt.Fprintf(w, "  pushing %d %s to %s in one atomic push\n", len(group), noun, remote)
	out := map[string]StackPushed{}
	record := func(it stackItem, outcome, why string) {
		p := StackPushed{Branch: it.Branch, Remote: remote, RemoteBranch: it.RemoteBranch, New: it.New, Outcome: outcome, Rule: it.Rule}
		if it.Lease != "" {
			p.Old = jsonout.Strp(it.Lease)
		}
		if why != "" {
			p.Why = jsonout.Strp(why)
		}
		out[it.Branch] = p
	}
	// To the URL the leases were recorded for, which PushStack has checked the
	// remote still pushes to, and not to the remote's name.
	endpoint := group[0].Endpoint
	err := wtsync.PushAtomic(ctx.Repo.MainRoot, endpoint, refs, kit.NetworkTimeout)
	if err == nil {
		for _, it := range group {
			record(it, PushedOK, "")
			from := "new on " + remote
			if it.Lease != "" {
				from = git.ShortID(it.Lease, 7)
			}
			fmt.Fprintf(w, "  ✓ pushed %s  %s → %s\n", it.Branch, from, git.ShortID(it.New, 7))
			afterPushed(ctx, w, it)
		}
		return out, true
	}
	undo := fmt.Sprintf("every local branch stays where the rebase put it; wt sync undo %s puts back the line and every layer moved", work)
	reasons := refusals(err)
	if len(reasons) == 0 && !strings.Contains(err.Error(), "does not support --atomic") {
		reconcile(ctx, w, remote, endpoint, group, err, record, undo)
		return out, allAt(out, group)
	}
	for _, it := range group {
		switch r := reasons[it.RemoteBranch]; r {
		case "stale info":
			record(it, PushedLeaseFailed, "its remote branch is not at the commit the run recorded (git: stale info)")
			fmt.Fprintf(w, "  ✗ %s moved under its lease: %s on %s is no longer at %s, somebody pushed to it since it was fetched; nothing on %s was pushed\n",
				it.Branch, it.RemoteBranch, remote, leaseWord(it.Lease), remote)
		case "", "atomic push failed":
			record(it, PushedNotSent, pushReason(err))
		default:
			record(it, PushedNotSent, r)
			fmt.Fprintf(w, "  ✗ %s was refused by %s: %s; nothing on %s was pushed\n", it.Branch, remote, r, remote)
		}
	}
	if strings.Contains(err.Error(), "does not support --atomic") {
		fmt.Fprintf(w, "  ✗ %s does not accept an atomic push: nothing was pushed to it, and wt does not push a stack branch by branch\n", remote)
	}
	fmt.Fprintf(w, "      %s\n", undo)
	return out, false
}

func allAt(out map[string]StackPushed, group []stackItem) bool {
	return !slices.ContainsFunc(group, func(it stackItem) bool { return out[it.Branch].Outcome != PushedOK })
}

// reconcile is the answer to a push that failed with no word from the
// remote about any ref: a timeout or a dropped connection may come after the
// remote committed the transaction. The remote's own refs say where each
// branch is: at the new commit, at the lease, or neither, which is unknown.
func reconcile(ctx *kit.Context, w io.Writer, remote, endpoint string, group []stackItem, perr error, record func(stackItem, string, string), undo string) {
	names := make([]string, len(group))
	for i, it := range group {
		names[i] = it.RemoteBranch
	}
	got, rerr := wtsync.RemoteRefs(ctx.Repo.MainRoot, endpoint, names, kit.NetworkTimeout)
	if rerr != nil {
		fmt.Fprintf(w, "  ✗ the push to %s failed (%s) and %s could not be read back (%s): whether it moved anything is unknown; check with git ls-remote %s\n",
			remote, pushReason(perr), remote, pushReason(rerr), remote)
		for _, it := range group {
			record(it, PushedUnknown, "the push failed ("+pushReason(perr)+") and the remote could not be read back")
		}
		fmt.Fprintf(w, "      %s\n", undo)
		return
	}
	var in, notIn, unknown []string
	for _, it := range group {
		switch at := got[it.RemoteBranch]; at {
		case it.New:
			record(it, PushedOK, "")
			in = append(in, it.Branch)
			afterPushed(ctx, w, it)
		case it.Lease:
			record(it, PushedNotSent, pushReason(perr))
			notIn = append(notIn, it.Branch)
		default:
			record(it, PushedUnknown, "the push failed ("+pushReason(perr)+"); the remote has it at "+leaseWord(at)+", neither the lease nor the new commit")
			unknown = append(unknown, it.Branch)
		}
	}
	fmt.Fprintf(w, "  ✗ the push to %s failed: %s\n      read back from %s: ", remote, pushReason(perr), remote)
	var parts []string
	for _, p := range []struct {
		what string
		l    []string
	}{{"at the new commit", in}, {"unchanged", notIn}, {"neither (unknown)", unknown}} {
		if len(p.l) > 0 {
			parts = append(parts, strings.Join(p.l, ", ")+" "+p.what)
		}
	}
	fmt.Fprintf(w, "%s\n      %s\n", strings.Join(parts, "; "), undo)
}

// afterPushed is what a push to a URL leaves undone for a branch that is at
// its new commit on the remote: git moved no remote-tracking ref, and the
// line's own branch has no upstream yet. Whatever fails is said.
func afterPushed(ctx *kit.Context, w io.Writer, it stackItem) {
	if it.Tracking != "" {
		if err := wtsync.UpdateTracking(ctx.Repo.MainRoot, it.Tracking, it.New, it.Lease); err != nil {
			fmt.Fprintf(w, "      note: %s was pushed, but %s could not be moved to it: %s\n", it.Branch, strings.TrimPrefix(it.Tracking, "refs/remotes/"), pushReason(err))
		}
	}
	if it.Top && it.SetUpstream {
		if err := wtsync.SetUpstream(ctx.Repo.MainRoot, it.Remote, it.RemoteBranch, it.Branch); err != nil {
			fmt.Fprintf(w, "      note: %s was pushed, but its upstream could not be set: %s\n", it.Branch, pushReason(err))
		}
	}
}

func leaseWord(lease string) string {
	if lease == "" {
		return "absent"
	}
	return git.ShortID(lease, 7)
}

// refusals is, from git's report of a failed push, the reason it gives for
// each remote branch: the text in the parentheses of its " ! [rejected]"
// line.
func refusals(err error) map[string]string {
	out := map[string]string{}
	var gerr *git.Error
	if !errors.As(err, &gerr) {
		return out
	}
	for _, l := range strings.Split(gerr.Stderr, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "! [") {
			continue
		}
		_, rest, ok := strings.Cut(l, " -> ")
		if !ok {
			continue
		}
		name, reason, ok := strings.Cut(rest, " (")
		if !ok {
			continue
		}
		out[strings.TrimPrefix(strings.TrimSpace(name), "refs/heads/")] = strings.TrimSuffix(reason, ")")
	}
	return out
}
