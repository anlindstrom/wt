package synccmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// wt#223, rework 1: run A, with --no-push, moves s1 and keeps it for a later
// push. Run B, with --push, holds s1 at A's tip (its worktree has changes)
// and cannot add itself to the record. Its push must not send B's layers
// alone, which would leave A's s1 behind on origin: it refuses, origin is as
// it was, and the record A kept is still there.
func TestRunPushRefusesWhenItsRunCannotJoinTheRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	a := synccmdtest.At(synccmdtest.NoAgents(), 100)
	a.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, a); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("run A: err %v\n%s", err, human)
	}
	s1 := testkit.GitOut(t, main, "rev-parse", "s1")
	if s1 == p.s.Tips["s1"] {
		t.Fatal("run A did not move s1")
	}
	testkit.WriteFile(t, p.s.Paths["s1"], "a1.txt", "work in progress\n")
	commitOnRemote(t, main, p.bare, "main", "trunk moved on", false)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	recA, ok, err := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if err != nil || !ok {
		t.Fatalf("run A kept no record: %v", err)
	}
	common := testkit.GitOut(t, main, "rev-parse", "--path-format=absolute", "--git-common-dir")
	dir := filepath.Join(common, "wt-sync-push")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	remote := bareRefs(t, p.bare)

	b := synccmdtest.At(synccmdtest.PushAlways(), 200)
	_, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, b)
	if !strings.Contains(human, "s1 held") {
		t.Fatalf("run B did not hold s1:\n%s", human)
	}
	if testkit.GitOut(t, main, "rev-parse", "s2") == recTip(recA, "s2") {
		t.Fatalf("run B did not move s2:\n%s", human)
	}
	if err == nil || !strings.Contains(err.Error(), "stack not pushed") {
		t.Errorf("run B: err %v, want the stack push refused\n%s", err, human)
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("run B pushed without A's s1: origin is\n%s\nwas\n%s\n%s", got, remote, human)
	}
	if !strings.Contains(human, "✗ stack not pushed: this run could not be added to the record of what earlier runs left unpushed") {
		t.Errorf("the refusal does not say why:\n%s", human)
	}
	if after, ok, _ := wtsync.ReadPushRecord(main, gittest.StackTmp); !ok || after.Rev != recA.Rev {
		t.Errorf("the record run A kept is not there as it was")
	}
}

// recTip is the tip rec's newest run left branch at, "" when it names none.
func recTip(rec *wtsync.PushRecord, branch string) string {
	for _, it := range rec.Items {
		if it.Branch == branch {
			return it.New
		}
	}
	return ""
}

// wt#223, rework 2: resume A of a run nobody told --no-push gets a yes to the
// push and reads the handover. Before A takes the lock, resume B of the same
// stop, with --no-push, keeps that with the handover and is cut short. A
// then finishes the run under the lock and pushes nothing: the push is
// decided from the handover as A reads it under the lock, not before.
func TestResumeDecidesThePushFromWhatItReadsUnderTheLock(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	remote := bareRefs(t, p.bare)
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.NoAgents()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want a handover at v.txt\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")

	a := yesFlag()
	ranB := false
	a.BeforeLock = func() {
		var b syncfix.ResumeOptions
		b.Push = syncrun.PushNever
		b.AfterKeepStop = func() error { return errors.New("interrupted") }
		if _, human, err := resumeAs(t, p, b); err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("resume B: err %v, want the interrupt\n%s", err, human)
		}
		if got := bareRefs(t, p.bare); got != remote {
			t.Fatalf("resume B changed origin:\n%s\nwas\n%s", got, remote)
		}
		ranB = true
	}
	res, human, err := resumeAs(t, p, a)
	if !ranB {
		t.Fatalf("resume A did not reach the lock: %v\n%s", err, human)
	}
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("resume A: err %v, want the two held layers named\n%s", err, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "s1") == p.s.Tips["s1"] {
		t.Fatalf("resume A did not move s1\n%s", human)
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("resume A pushed what B's --no-push kept back: origin is\n%s\nwas\n%s\n%s", got, remote, human)
	}
	if line := synccmdtest.Participant(t, res, gittest.StackTmp); line.Pushed || line.Stack == nil || line.Stack.Push != nil {
		t.Errorf("resume A's JSON says it pushed\n%s", human)
	}
}
