package synccmd_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// noPushRun is the run a unit makes: the line rebased with --no-push. The
// layers s3 and s5 are held, so the run itself ends in a failure.
func (p *pushedLine) noPushRun(t *testing.T) string {
	t.Helper()
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the run to say two layers were held\n%s", err, human)
	}
	return human
}

// pushLater is wt sync push <line> --stack --yes (or with --dry-run).
func (p *pushedLine) pushLater(t *testing.T, dryRun bool) (string, error) {
	t.Helper()
	var human bytes.Buffer
	opts := syncfix.PushLaterOptions{DryRun: dryRun}
	opts.Push = syncrun.PushAlways
	err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human)
	return human.String(), err
}

func (p *pushedLine) localTip(t *testing.T, b string) string {
	t.Helper()
	return testkit.GitOut(t, p.ctx.Repo.MainRoot, "rev-parse", "refs/heads/"+b)
}

// A --no-push run, then the push: every moved layer and the line go in one
// atomic push, over the leases the run took, and the record is gone.
func TestPushLaterSendsEveryLayerInOnePushAndForgetsTheRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	before := bareRefs(t, p.bare)
	p.noPushRun(t)
	if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != 0 {
		t.Fatal("--no-push pushed")
	}
	human, err := p.pushLater(t, false)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("push: %v\n%s", err, human)
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), p.localTip(t, b); got != want || got == p.s.Tips[b] {
			t.Errorf("remote %s is at %s, want the run's new tip %s (old %s)\n%s", b, got, want, p.s.Tips[b], human)
		}
	}
	for _, b := range []string{"s3", "s5"} {
		if got := refIn(t, p.bare, b); got != p.s.Tips[b] {
			t.Errorf("held %s was pushed: remote at %s", b, got)
		}
		if !strings.Contains(human, b) {
			t.Errorf("the push does not name the held %s\n%s", b, human)
		}
	}
	if n := pushCount(t, p.hooks); n != 1 {
		t.Errorf("the remote received %d pushes, want one atomic push\n%s", n, human)
	}
	after := bareRefs(t, p.bare)
	if human2, err := p.pushLater(t, false); err == nil || !strings.Contains(err.Error(), "nothing recorded to push") {
		t.Errorf("a second push: err %v, want nothing recorded\n%s", err, human2)
	}
	if bareRefs(t, p.bare) != after {
		t.Error("a push with no record moved the remote")
	}
}

// A remote tip that moved since the run: the atomic push fails, nothing on
// the remote moves, the layer is named and the record is kept.
func TestPushLaterMovesNothingWhenARemoteTipMoved(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	p.noPushRun(t)
	commitOnRemote(t, p.ctx.Repo.MainRoot, p.bare, "s2", "theirs", false)
	before := bareRefs(t, p.bare)
	hooks := pushCount(t, p.hooks)
	human, err := p.pushLater(t, false)
	if err == nil {
		t.Fatalf("the push succeeded over a moved remote tip\n%s", human)
	}
	if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != hooks {
		t.Errorf("the remote changed under a failed atomic push\n%s", human)
	}
	if !strings.Contains(human, "s2") || !strings.Contains(human, "moved") {
		t.Errorf("the push does not say s2 moved under its lease\n%s", human)
	}
	// The record is kept: the same push fails the same way, not for lack of one.
	human, err = p.pushLater(t, false)
	if err == nil || strings.Contains(err.Error(), "nothing recorded") {
		t.Errorf("the record was dropped by a failed push: err %v\n%s", err, human)
	}
}

// A local layer that moved since the run is refused before any push, named.
func TestPushLaterRefusesALayerMovedLocallyBeforePushingAnything(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	p.noPushRun(t)
	testkit.CommitOn(t, p.ctx.Repo.MainRoot, "s2", "worked on since", time.Now())
	before := bareRefs(t, p.bare)
	human, err := p.pushLater(t, false)
	if err == nil || !strings.Contains(human+err.Error(), "s2") {
		t.Fatalf("err %v, want a refusal naming s2\n%s", err, human)
	}
	if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != 0 {
		t.Errorf("something was pushed\n%s", human)
	}
}

// wt sync undo after a --no-push run takes the record with it.
func TestPushLaterHasNothingAfterUndo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	p.noPushRun(t)
	var undone strings.Builder
	undo := syncfix.UndoOptions{VerbOptions: synccmdtest.NoAgents().VerbOptions}
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, undo, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	before := bareRefs(t, p.bare)
	human, err := p.pushLater(t, false)
	if err == nil || !strings.Contains(err.Error(), "nothing recorded to push") {
		t.Errorf("err %v, want nothing recorded\n%s", err, human)
	}
	if bareRefs(t, p.bare) != before {
		t.Error("the remote moved")
	}
}

// A second run of the same line replaces the record: the push sends the
// second run's tips, with the second run's leases.
func TestPushLaterTakesTheLatestRunsRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	p.noPushRun(t)
	first := map[string]string{}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		first[b] = p.localTip(t, b)
	}
	commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
	// The runs of these tests do not fetch.
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	// Another epoch: the runs of these tests share a fixed clock.
	opts = synccmdtest.At(opts, time.Now().UnixNano())
	if _, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); !strings.Contains(human, "s1") {
		t.Fatalf("the second run did not run\n%s", human)
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if p.localTip(t, b) == first[b] {
			t.Fatalf("%s did not move in the second run", b)
		}
	}
	human, _ := p.pushLater(t, false)
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), p.localTip(t, b); got != want {
			t.Errorf("remote %s is at %s, want the second run's tip %s\n%s", b, got, want, human)
		}
	}
}

// A line nothing was recorded for is refused, and nothing moves.
func TestPushLaterRefusesALineWithNoRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	before := bareRefs(t, p.bare)
	human, err := p.pushLater(t, false)
	if err == nil || !strings.Contains(err.Error(), "nothing recorded to push") || !strings.Contains(err.Error(), "wt sync rebase --stack") {
		t.Errorf("err %v, want nothing recorded, pointing at wt sync rebase --stack\n%s", err, human)
	}
	if bareRefs(t, p.bare) != before {
		t.Error("the remote moved")
	}
}

// --dry-run says what each branch would push, lease → new, and moves nothing,
// keeps the record, and --json gives the shape of a run's stack.push.
func TestPushLaterDryRunAndJSON(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	p.noPushRun(t)
	before := bareRefs(t, p.bare)
	human, err := p.pushLater(t, true)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("dry run: %v\n%s", err, human)
	}
	if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != 0 {
		t.Fatalf("--dry-run pushed\n%s", human)
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		want := b + "  origin/" + b
		if !strings.Contains(human, b) || !strings.Contains(human, p.s.Tips[b][:7]+" → "+p.localTip(t, b)[:7]) {
			t.Errorf("--dry-run does not say %s's lease → new (%s)\n%s", b, want, human)
		}
	}

	var out, h bytes.Buffer
	opts := syncfix.PushLaterOptions{Journal: syncrun.NewSyncRunJournal(&out, "sync push")}
	opts.Push = syncrun.PushAlways
	_ = syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &h)
	row := synccmdtest.Participant(t, synccmdtest.DecodeSyncRun(t, out.Bytes()), gittest.StackTmp)
	if row.Stack == nil || row.Stack.Push == nil {
		t.Fatalf("no stack.push in the result\n%s\n%s", out.String(), h.String())
	}
	var order []string
	for _, b := range row.Stack.Push.Branches {
		order = append(order, b.Branch)
		if b.Outcome != syncrun.PushedOK || b.Old == nil || *b.Old != p.s.Tips[b.Branch] || b.New != refIn(t, p.bare, b.Branch) {
			t.Errorf("%s: %+v", b.Branch, b)
		}
	}
	if strings.Join(order, " ") != "s1 s2 s4 tmp/stack" {
		t.Errorf("branches %v, want the moved layers bottom first, then the line", order)
	}
}

// pushLaterAsking is the push with the question asked of confirm.
func (p *pushedLine) pushLaterAsking(t *testing.T, confirm func([]string) bool) (string, error) {
	t.Helper()
	var human bytes.Buffer
	opts := syncfix.PushLaterOptions{}
	opts.Push, opts.ConfirmPush = syncrun.PushAsk, confirm
	err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human)
	return human.String(), err
}

// A record replaced while the question waits is not the one that was asked
// about: nothing is pushed, and the new record stays.
func TestPushLaterRefusesARecordReplacedWhileTheQuestionWaits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	p.noPushRun(t)
	before := bareRefs(t, p.bare)
	var replaced *wtsync.PushRecord
	human, err := p.pushLaterAsking(t, func([]string) bool {
		rec, ok, rerr := wtsync.ReadPushRecord(main, gittest.StackTmp)
		if rerr != nil || !ok {
			t.Errorf("no record to replace: %v", rerr)
			return true
		}
		rec.Epoch += 1000
		rec.Written = rec.Written.Add(time.Hour)
		if werr := wtsync.WritePushRecord(main, rec); werr != nil {
			t.Error(werr)
		}
		replaced = rec
		return true
	})
	if err == nil {
		t.Fatalf("the push went on over a replaced record\n%s", human)
	}
	if bareRefs(t, p.bare) != before || pushCount(t, p.hooks) != 0 {
		t.Errorf("something was pushed\n%s", human)
	}
	now, ok, rerr := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if rerr != nil || !ok || replaced == nil || now.Epoch != replaced.Epoch {
		t.Errorf("the replacement record did not stay: %+v %v %v", now, ok, rerr)
	}
}

// A push holds the line's lock while it waits for its answer, so an undo then
// is refused and cannot take the line back from under it.
func TestPushLaterHoldsTheLineLockWhileItAsks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	p.noPushRun(t)
	var undoErr error
	human, err := p.pushLaterAsking(t, func([]string) bool {
		var undone strings.Builder
		undo := syncfix.UndoOptions{VerbOptions: synccmdtest.NoAgents().VerbOptions}
		undoErr = syncfix.SyncUndo(p.ctx, gittest.StackTmp, undo, &undone)
		return true
	})
	if undoErr == nil {
		t.Error("an undo ran while the push was asking")
	}
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("push: %v\n%s", err, human)
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), p.localTip(t, b); got != want {
			t.Errorf("remote %s is at %s, want the run's tip %s\n%s", b, got, want, human)
		}
	}
}

// A run that moves nothing (refused, restored, nothing to rebase) leaves the
// record an earlier run kept; one that moved a layer replaces it. Deleting a
// record early is what costs, so a record only goes when a run moved on.
func TestKeepStackPushLeavesTheRecordOfARunThatMovedNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a worktree each")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	rec := &wtsync.PushRecord{Branch: gittest.StackTmp, Work: gittest.StackTmp, Epoch: 7, Written: time.Now(), Head: s.Tips[gittest.StackTmp]}
	if err := wtsync.WritePushRecord(main, rec); err != nil {
		t.Fatal(err)
	}
	refused := "a new tip failed its check"
	targets := []syncrun.PushTarget{{Work: gittest.StackTmp, Branch: gittest.StackTmp}}
	for name, st := range map[string]*syncrun.Stack{
		"refused":  {Branch: gittest.StackTmp, Head: s.Tips[gittest.StackTmp], Refusal: &refused},
		"restored": {Branch: gittest.StackTmp, Head: s.Tips[gittest.StackTmp], Restored: true},
		// The line is where the run found it and no layer moved.
		"nothing moved": {Branch: gittest.StackTmp, Head: s.Tips[gittest.StackTmp]},
	} {
		var out bytes.Buffer
		syncrun.KeepStackPush(ctx, &out, st, targets, st.Head)
		if got, ok, err := wtsync.ReadPushRecord(main, gittest.StackTmp); err != nil || !ok || got.Epoch != 7 {
			t.Errorf("%s: the earlier run's record is gone: %+v %v %v\n%s", name, got, ok, err, out.String())
		}
	}
}

// One remote that takes its push and another that does not is a partial
// result, with the error.
func TestPushLaterJSONIsPartialWhenOneRemoteFails(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and two bare remotes")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	backup := p.addRemote(t, "backup")
	testkit.GitIn(t, main, "config", "branch.s2.pushRemote", "backup")
	p.noPushRun(t)
	commitOnRemote(t, main, backup, "s2", "theirs", false)
	var out, human bytes.Buffer
	opts := syncfix.PushLaterOptions{Journal: syncrun.NewSyncRunJournal(&out, "sync push")}
	opts.Push = syncrun.PushAlways
	err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human)
	if err == nil {
		t.Fatalf("a failed lease on backup did not fail the push\n%s", human.String())
	}
	if got, want := refIn(t, p.bare, "s1"), p.localTip(t, "s1"); got != want {
		t.Fatalf("origin did not take its push: s1 at %s, want %s\n%s", got, want, human.String())
	}
	res := synccmdtest.DecodeSyncRun(t, out.Bytes())
	if res.Outcome != "partial" || res.Error == nil {
		t.Errorf("outcome %q, error %v, want partial with the error", res.Outcome, res.Error)
	}
}

// A line with no record is a refused row with its reason, in --json too.
func TestPushLaterJSONRefusesALineWithNoRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	var out, human bytes.Buffer
	opts := syncfix.PushLaterOptions{Journal: syncrun.NewSyncRunJournal(&out, "sync push")}
	opts.Push = syncrun.PushAlways
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human); err == nil {
		t.Fatal("no record was not refused")
	}
	row := synccmdtest.Participant(t, synccmdtest.DecodeSyncRun(t, out.Bytes()), gittest.StackTmp)
	if row.Result != syncrun.ResultRefused || row.Reason == nil || !strings.Contains(*row.Reason, "nothing recorded to push") {
		t.Errorf("row %q, reason %v, want refused with the reason", row.Result, row.Reason)
	}
}

// A run keeps the line's lock until its push record is saved: a second run
// that comes in once the first is done with every worktree, before its push,
// has its record stay, and the first run does not overwrite it.
func TestPushLaterRecordOfALaterRunIsNotOverwrittenByAnEarlierOne(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	var second error
	opts.Stack = &synccmd.StackOptions{BeforePush: func() {
		// Held between the release of the line and the push: trunk moves and a
		// second run of the line goes through, as another process would.
		commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
		testkit.GitIn(t, main, "fetch", "-q", "origin")
		later := synccmdtest.At(synccmdtest.NoAgents(), time.Now().UnixNano())
		later.Push = syncrun.PushNever
		_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, later)
		if !strings.Contains(human, "s1") {
			second = fmt.Errorf("the second run did not run: %w\n%s", err, human)
		}
	}}
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the run to say two layers were held\n%s", err, human)
	}
	if second != nil {
		t.Fatal(second)
	}
	human, _ := p.pushLater(t, false)
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), p.localTip(t, b); got != want {
			t.Errorf("remote %s is at %s, want the later run's tip %s: the earlier run's record replaced it\n%s", b, got, want, human)
		}
	}
}

// --dry-run changes nothing, locks included: an expired lock a dead process
// left is still there, as it was, after it.
func TestPushLaterDryRunLeavesAnExpiredLockAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	p.noPushRun(t)
	gitDir, err := wtsync.GitDir(p.s.Paths[gittest.StackTmp])
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(gitDir, wtsync.LockName)
	old := time.Now().Add(-2 * wtsync.LockExpiry).Unix()
	body := fmt.Sprintf("pid=%d\nstart=%d\nowner=ghost\n", gittest.DeadPID(t), old)
	if err := os.WriteFile(lock, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	human, err := p.pushLater(t, true)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("dry run: %v\n%s", err, human)
	}
	if got, rerr := os.ReadFile(lock); rerr != nil || string(got) != body {
		t.Errorf("--dry-run touched the lock: %q %v, want %q", got, rerr, body)
	}
}
