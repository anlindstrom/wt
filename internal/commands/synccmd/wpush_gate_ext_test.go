package synccmd_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// stopAfter is an AfterMove that stops the layer moves once branch moved.
func stopAfter(branch string) func(wtsync.LayerMove) error {
	return func(m wtsync.LayerMove) error {
		if m.Branch == branch {
			return errors.New("interrupted")
		}
		return nil
	}
}

// assertNothingPushed holds a stack push that must not have run: the remote
// is as it was, the output says the stack was not pushed, no record is kept
// for a later push, and the undo puts the line and every layer back.
func assertNothingPushed(t *testing.T, p *pushedLine, remote, human string) {
	t.Helper()
	main := p.ctx.Repo.MainRoot
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("the remote changed:\nbefore %s\nafter %s\n%s", remote, got, human)
	}
	if want := "✗ stack not pushed: the layer moves stopped before every layer moved"; !strings.Contains(human, want) {
		t.Errorf("missing %q in\n%s", want, human)
	}
	if _, ok, _ := wtsync.ReadPushRecord(main, gittest.StackTmp); ok {
		t.Errorf("a push record was kept for a line whose moves stopped")
	}
	uopts := syncfix.UndoOptions{}
	uopts.Agents = []agents.Agent{}
	uopts.Now = func() time.Time { return time.Unix(9, 0) }
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, uopts, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, p.s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != p.s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, p.s.Tips[b], undone.String())
		}
	}
}

// A --push run whose layer moves stop part way pushes nothing: s1 moved and
// the line moved, but the push of them without s2 and s4 would split the
// stack on the remote.
func TestRunWhoseMovesStopPushesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	remote := bareRefs(t, p.bare)
	opts := synccmdtest.PushAlways()
	opts.Stack = &synccmd.StackOptions{AfterMove: stopAfter("s1")}
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "layer moves stopped") {
		t.Fatalf("err %v, want the moves named as stopped\n%s", err, human)
	}
	if got := testkit.GitOut(t, p.ctx.Repo.MainRoot, "rev-parse", "s1"); got == p.s.Tips["s1"] {
		t.Fatalf("s1 did not move before the stop\n%s", human)
	}
	assertNothingPushed(t, p, remote, human)
}

// The same for a --push resume whose layer moves stop part way.
func TestResumeWhoseMovesStopPushesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want a handover at the conflict\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	remote := bareRefs(t, p.bare)
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushAlways
	ropts.AfterMove = stopAfter("s1")
	var out bytes.Buffer
	err := syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &out)
	if err == nil || !strings.Contains(err.Error(), "layer moves stopped") {
		t.Fatalf("err %v, want the moves named as stopped\n%s", err, out.String())
	}
	if got := testkit.GitOut(t, p.ctx.Repo.MainRoot, "rev-parse", "s1"); got == p.s.Tips["s1"] {
		t.Fatalf("s1 did not move before the stop\n%s", out.String())
	}
	assertNothingPushed(t, p, remote, out.String())
}

// A ref-only move (s2 has no worktree) cut between its ref and the rest of
// its move is recorded as under way, as a worktree move is: the resume after
// it refuses before anything moves, names s2 and the undo, pushes nothing
// with --push, keeps the record, and the undo puts every branch back.
func TestResumeRefusesAfterARefOnlyMoveWasCutShort(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	// The finish fails after the moves, so the handover stays for a retry.
	blocker := wtsync.ResultPrefix + gittest.StackTmp + "/99/x"
	testkit.GitIn(t, main, "update-ref", blocker, p.s.Tips[gittest.StackTmp])
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	ropts.Between = func(stage string, m wtsync.LayerMove) error {
		if stage == wtsync.StageAfterRef && m.Branch == "s2" {
			return errors.New("interrupted")
		}
		return nil
	}
	var cut bytes.Buffer
	_ = syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &cut)
	if got := testkit.GitOut(t, main, "rev-parse", "s2"); got == p.s.Tips["s2"] {
		t.Fatalf("s2's ref did not move before the cut\n%s", cut.String())
	}
	rec, ok, err := wtsync.ReadStackRecord(main, 99)
	if err != nil || !ok {
		t.Fatalf("no stack record after the cut: %v\n%s", err, cut.String())
	}
	if i := slices.IndexFunc(rec.Moves, func(m wtsync.LayerMove) bool { return m.Branch == "s2" }); i < 0 || rec.Moves[i].Result != wtsync.MoveMoving {
		t.Fatalf("s2's move is not recorded as under way: %+v", rec.Moves)
	}
	testkit.GitIn(t, main, "update-ref", "-d", blocker)
	refs := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	remote := bareRefs(t, p.bare)

	ropts.Between = nil
	ropts.Push = syncrun.PushAlways
	var again bytes.Buffer
	err = syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &again)
	if err == nil || !strings.Contains(again.String(), "layer s2 has a move that was cut short") || !strings.Contains(again.String(), "wt sync undo tmp/stack") {
		t.Fatalf("second resume: err %v, want a refusal naming s2 and the undo\n%s", err, again.String())
	}
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != refs {
		t.Errorf("the second resume moved branches:\nbefore %s\nafter %s", refs, got)
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("the second resume pushed:\nbefore %s\nafter %s", remote, got)
	}
	if _, ok, _ := wtsync.ReadStackRecord(main, 99); !ok {
		t.Fatal("the stack record is gone")
	}
	uopts := syncfix.UndoOptions{}
	uopts.Agents = []agents.Agent{}
	uopts.Now = func() time.Time { return time.Unix(9, 0) }
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, uopts, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, p.s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != p.s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, p.s.Tips[b], undone.String())
		}
	}
}
