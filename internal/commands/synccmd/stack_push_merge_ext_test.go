package synccmd_test

import (
	"bytes"
	"encoding/json"
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

const epochA, epochB = int64(1_000), int64(2_000)

// movedLine is the issue's line: run A moves every layer that is not held
// (s1, s2, s4 and the line), run B, after trunk moved, only s4 and the line.
type movedLine struct {
	*pushedLine
	// atA is each branch's tip after run A, atB after run B.
	atA, atB map[string]string
	// leases are what the remote held for each branch before either run.
	leases map[string]string
	// pushes0 is what the remote's hook had counted before any push of ours.
	pushes0 int
}

func (m *movedLine) pushes(t *testing.T) int { return pushCount(t, m.hooks) - m.pushes0 }

var movedBranches = []string{"s1", "s2", "s4", gittest.StackTmp}

func (p *pushedLine) tips(t *testing.T) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, b := range movedBranches {
		m[b] = p.localTip(t, b)
	}
	return m
}

// runAt is a --no-push run of the line at its own epoch, with the layers
// given (all when none); a later run first has trunk move on and fetches it.
func (p *pushedLine) runAt(t *testing.T, epoch int64, layers ...string) {
	t.Helper()
	main := p.ctx.Repo.MainRoot
	if epoch != epochA {
		commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
		testkit.GitIn(t, main, "fetch", "-q", "origin")
	}
	opts := synccmdtest.At(synccmdtest.NoAgents(), epoch)
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{Layers: layers}
	if _, human, _ := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); !strings.Contains(human, gittest.StackTmp) {
		t.Fatalf("the run at %d did not run\n%s", epoch, human)
	}
}

func newMovedLine(t *testing.T) *movedLine {
	t.Helper()
	p := newPushedLine(t)
	m := &movedLine{pushedLine: p, leases: map[string]string{}}
	for _, b := range movedBranches {
		m.leases[b] = refIn(t, p.bare, b)
	}
	p.runAt(t, epochA)
	m.atA = p.tips(t)
	for _, b := range movedBranches {
		if m.atA[b] == m.leases[b] {
			t.Fatalf("run A did not move %s", b)
		}
	}
	p.runAt(t, epochB, "s4")
	m.pushes0 = pushCount(t, p.hooks)
	m.atB = p.tips(t)
	for _, b := range []string{"s1", "s2"} {
		if m.atB[b] != m.atA[b] {
			t.Fatalf("run B moved %s, which it was not to", b)
		}
	}
	for _, b := range []string{"s4", gittest.StackTmp} {
		if m.atB[b] == m.atA[b] {
			t.Fatalf("run B did not move %s", b)
		}
	}
	return m
}

// Run A moved s1, s2, s4 and the line; run B only s4 and the line. The push
// sends all four in one atomic push, s1 and s2 at A's tips and s4 and the
// line at B's, every one over the lease from before A.
func TestPushLaterAddsUpTheUnpushedRunsOfALine(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newMovedLine(t)
	human, err := p.pushLater(t, false)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("push: %v\n%s", err, human)
	}
	for _, b := range movedBranches {
		if got := refIn(t, p.bare, b); got != p.atB[b] {
			t.Errorf("remote %s is at %s, want %s\n%s", b, got, p.atB[b], human)
		}
	}
	if n := p.pushes(t); n != 1 {
		t.Errorf("the remote received %d pushes, want one atomic push\n%s", n, human)
	}
	if h, err := p.pushLater(t, false); err == nil || !strings.Contains(err.Error(), "nothing recorded to push") {
		t.Errorf("a second push: err %v, want nothing recorded: the push sent everything\n%s", err, h)
	}
}

// --dry-run says, for each branch, its lease → new and the run each came
// from: the lease of the oldest run's, the tip of the newest's.
func TestPushLaterDryRunNamesTheRunEachBranchCameFrom(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newMovedLine(t)
	before := bareRefs(t, p.bare)
	human, err := p.pushLater(t, true)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("dry run: %v\n%s", err, human)
	}
	if bareRefs(t, p.bare) != before || p.pushes(t) != 0 {
		t.Fatalf("--dry-run pushed\n%s", human)
	}
	lines := map[string]string{}
	for _, l := range strings.Split(human, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "would push "); ok {
			lines[strings.Fields(rest)[0]] = rest
		}
	}
	for _, b := range movedBranches {
		l, ok := lines[b]
		if !ok || !strings.Contains(l, p.leases[b][:7]+" → "+p.atB[b][:7]) {
			t.Errorf("--dry-run does not say %s's lease → new: %q\n%s", b, l, human)
		}
	}
	// s1 and s2 came from A alone; s4 and the line from both.
	if !strings.Contains(lines["s1"], "run 1000") || strings.Contains(lines["s1"], "run 2000") {
		t.Errorf("s1 should name run 1000 only: %q", lines["s1"])
	}
	if !strings.Contains(lines["s4"], "run 2000") || !strings.Contains(lines["s4"], "run 1000") {
		t.Errorf("s4 should name both runs (lease from 1000, tip from 2000): %q", lines["s4"])
	}
}

// wt sync undo takes back run B and only B's contribution: A's tips are what
// the line is at and what the push sends.
func TestPushLaterAfterUndoOfTheNewestRunSendsTheEarlierOnes(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newMovedLine(t)
	var undone strings.Builder
	undo := syncfix.UndoOptions{VerbOptions: synccmdtest.NoAgents().VerbOptions}
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, undo, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if !strings.Contains(undone.String(), "earlier run") {
		t.Errorf("undo does not say earlier runs are still recorded\n%s", undone.String())
	}
	for _, b := range movedBranches {
		if got := p.localTip(t, b); got != p.atA[b] {
			t.Fatalf("undo left %s at %s, want run A's tip %s", b, got, p.atA[b])
		}
	}
	human, err := p.pushLater(t, false)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("push: %v\n%s", err, human)
	}
	for _, b := range movedBranches {
		if got := refIn(t, p.bare, b); got != p.atA[b] {
			t.Errorf("remote %s is at %s, want run A's tip %s\n%s", b, got, p.atA[b], human)
		}
	}
}

// A local branch worked on after run B is refused before anything moves,
// whichever run moved it last.
func TestPushLaterRefusesABranchMovedAfterTheNewestRun(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, b := range []string{"s1", "s4"} {
		t.Run(b, func(t *testing.T) {
			t.Parallel()
			p := newMovedLine(t)
			testkit.CommitOn(t, p.ctx.Repo.MainRoot, b, "worked on since", time.Now())
			before := bareRefs(t, p.bare)
			human, err := p.pushLater(t, false)
			if err == nil || !strings.Contains(human+err.Error(), b) {
				t.Fatalf("err %v, want a refusal naming %s\n%s", err, b, human)
			}
			if bareRefs(t, p.bare) != before || p.pushes(t) != 0 {
				t.Errorf("something was pushed\n%s", human)
			}
		})
	}
}

// --forget drops the line's record and nothing else: no ref moves, nothing is
// pushed, and the push has nothing recorded after it.
func TestPushLaterForgetDropsOnlyTheRecord(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newMovedLine(t)
	main := p.ctx.Repo.MainRoot
	refs := func() string { return testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)") }
	localBefore, bareBefore := refs(), bareRefs(t, p.bare)

	var human strings.Builder
	opts := syncfix.PushLaterOptions{Forget: true, DryRun: true}
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human); err != nil || !strings.Contains(human.String(), "would drop") {
		t.Fatalf("--forget --dry-run: %v, want what it would drop\n%s", err, human.String())
	}
	if _, err := p.pushLater(t, true); err != nil && strings.Contains(err.Error(), "nothing recorded") {
		t.Fatalf("--forget --dry-run dropped the record: %v", err)
	}

	human.Reset()
	opts.DryRun = false
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human); err != nil {
		t.Fatalf("--forget: %v\n%s", err, human.String())
	}
	if !strings.Contains(human.String(), "dropped") || !strings.Contains(human.String(), "2 runs") {
		t.Errorf("--forget does not say what it dropped\n%s", human.String())
	}
	if refs() != localBefore || bareRefs(t, p.bare) != bareBefore || p.pushes(t) != 0 {
		t.Errorf("--forget moved a ref or pushed\n%s", human.String())
	}
	if h, err := p.pushLater(t, false); err == nil || !strings.Contains(err.Error(), "nothing recorded to push") {
		t.Errorf("after --forget: err %v, want nothing recorded\n%s", err, h)
	}
	// A line with no record: nothing to drop, said, and not a failure.
	human.Reset()
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, syncfix.PushLaterOptions{Forget: true}, &human); err != nil || !strings.Contains(human.String(), "nothing recorded") {
		t.Errorf("--forget with no record: %v\n%s", err, human.String())
	}
}

// A record a build before this one wrote (one run, its fields at the top) is
// pushed as it always was.
func TestPushLaterPushesARecordOfTheOldShape(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	p.noPushRun(t)
	common := testkit.GitOut(t, p.ctx.Repo.MainRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	raw, err := os.ReadFile(filepath.Join(common, "wt-sync-push", "tmp%2Fstack.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"earlier"`, `"runs"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("a single run's record is not in the old shape: it holds %s", key)
		}
	}
	human, err := p.pushLater(t, false)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("push: %v\n%s", err, human)
	}
	for _, b := range movedBranches {
		if got := refIn(t, p.bare, b); got != p.localTip(t, b) {
			t.Errorf("remote %s is at %s\n%s", b, got, human)
		}
	}
}

// A newer run that moved branches but is not pushable (a step failed or is
// owed) adds nothing, and leaves what the earlier runs kept: only a push that
// sent everything, --forget, or an undo of the last contribution deletes it.
func TestKeepStackPushKeepsEarlierRunsWhenANewerRunIsNotPushable(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	rec := &wtsync.PushRecord{Branch: gittest.StackTmp, Work: gittest.StackTmp, Epoch: 7, Written: time.Now(), Head: s.Tips[gittest.StackTmp]}
	if _, err := wtsync.AddPushRun(main, rec); err != nil {
		t.Fatal(err)
	}
	// The run moved the line, and no target of it can be pushed.
	st := &syncrun.Stack{Branch: gittest.StackTmp, Head: s.Tips[gittest.StackTmp]}
	var out bytes.Buffer
	syncrun.KeepStackPush(ctx, &out, st, nil, "0123456789abcdef0123456789abcdef01234567")
	if got, ok, err := wtsync.ReadPushRecord(main, gittest.StackTmp); err != nil || !ok || got.Epoch != 7 {
		t.Errorf("the earlier run's record is gone: %+v %v %v\n%s", got, ok, err, out.String())
	}
}

// --forget removes a record that cannot be read, which is when it is needed,
// says so, and --dry-run says it would.
func TestPushLaterForgetRemovesAnUnreadableRecord(t *testing.T) {
	t.Parallel()
	skipShort(t)
	p := newPushedLine(t)
	p.noPushRun(t)
	common := testkit.GitOut(t, p.ctx.Repo.MainRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	file := filepath.Join(common, "wt-sync-push", "tmp%2Fstack.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var human strings.Builder
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, syncfix.PushLaterOptions{Forget: true, DryRun: true}, &human); err != nil ||
		!strings.Contains(human.String(), "would remove") || !strings.Contains(human.String(), "could not be read") {
		t.Fatalf("--forget --dry-run: %v, want it to say it would remove a record it cannot read\n%s", err, human.String())
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("--dry-run removed the file: %v", err)
	}
	var out bytes.Buffer
	human.Reset()
	opts := syncfix.PushLaterOptions{Forget: true, Journal: syncrun.NewSyncRunJournal(&out, "sync push")}
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, opts, &human); err != nil {
		t.Fatalf("--forget: %v\n%s", err, human.String())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the unreadable record is still there: %v", err)
	}
	if !strings.Contains(human.String(), "could not be read") {
		t.Errorf("--forget does not say the contents could not be read\n%s", human.String())
	}
	var res struct {
		Worktrees []struct {
			DroppedRecord *struct {
				Unreadable *string `json:"unreadable"`
				Dropped    bool    `json:"dropped"`
			} `json:"droppedRecord"`
		} `json:"worktrees"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || len(res.Worktrees) != 1 || res.Worktrees[0].DroppedRecord == nil ||
		res.Worktrees[0].DroppedRecord.Unreadable == nil || !res.Worktrees[0].DroppedRecord.Dropped {
		t.Errorf("--json does not report the unreadable record: %v\n%s", err, out.String())
	}
	// The line records again.
	if _, err := wtsync.AddPushRun(p.ctx.Repo.MainRoot, &wtsync.PushRecord{Branch: gittest.StackTmp, Epoch: 9, Written: time.Now()}); err != nil {
		t.Errorf("a new run cannot record after --forget: %v", err)
	}
}
