package syncfix

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// amendedCheckStop is a fold onto trunk stopped at a failed check, the fix
// amended in, and the run's result ref blocked so the finish of the resume
// fails after its moves and leaves the handover for a retry. It returns the
// run's epoch and a func that lifts the block.
func amendedCheckStop(t *testing.T) (*kit.Context, *gittest.StackLine, int64, func()) {
	t.Helper()
	ctx, s, _ := synccmdtest.CheckedLine(t)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	opts := foldOntoTrunk()
	opts.EachCommit = "tips"
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok {
		t.Fatalf("no handover: %v", err)
	}
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	blocker := wtsync.ResultPrefix + gittest.StackTmp + "/" + strconv.FormatInt(st.Epoch, 10) + "/x"
	testkit.GitIn(t, main, "update-ref", blocker, s.Tips[gittest.StackTmp])
	return ctx, s, st.Epoch, func() { testkit.GitIn(t, main, "update-ref", "-d", blocker) }
}

// undoesAll is the undo of the run putting the line and every layer back,
// with the line's worktree clean.
func undoesAll(t *testing.T, ctx *kit.Context, s *gittest.StackLine) {
	t.Helper()
	main := ctx.Repo.MainRoot
	var undone bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, s.Tips[b], undone.String())
		}
	}
	for _, b := range []string{gittest.StackTmp, "s1"} {
		if got := testkit.GitOut(t, s.Paths[b], "status", "--porcelain=v1"); got != "" {
			t.Errorf("%s's worktree is not clean after the undo:\n%s", b, got)
		}
	}
}

// A check-stop fix is kept with the handover before the rebase goes on: a
// resume whose finish failed and whose moves stopped after s1 is retried,
// the fold still takes the fix, s1 stays moved and the rest move, and undo
// puts it all back.
func TestRetryAfterAFailedFinishKeepsTheCheckStopFix(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, _, unblock := amendedCheckStop(t)
	main := ctx.Repo.MainRoot
	ropts := noResumeAgents()
	ropts.Push = syncrun.PushNever
	ropts.AfterMove = func(m wtsync.LayerMove) error {
		if m.Branch == "s1" {
			return errors.New("interrupted")
		}
		return nil
	}
	var first bytes.Buffer
	if err := SyncResume(ctx, gittest.StackTmp, ropts, &first); err == nil {
		t.Fatalf("the blocked finish reported success\n%s", first.String())
	}
	s1 := testkit.GitOut(t, main, "rev-parse", "s1")
	if s1 == s.Tips["s1"] {
		t.Fatalf("s1 did not move before the stop\n%s", first.String())
	}
	unblock()
	ropts.AfterMove = nil
	var again bytes.Buffer
	err := SyncResume(ctx, gittest.StackTmp, ropts, &again)
	if err == nil || err.Error() != "not completed: tmp/stack (2 layers held)" {
		t.Fatalf("retry: err %v, want only the two held layers\n%s", err, again.String())
	}
	if strings.Contains(again.String(), "no layer moved") || !strings.Contains(again.String(), "fix.txt taken as resolved at a stop") {
		t.Errorf("the retry does not take the fix:\n%s", again.String())
	}
	if got := testkit.GitOut(t, main, "rev-parse", "s1"); got != s1 {
		t.Errorf("s1 is at %s after the retry, want %s", got, s1)
	}
	for _, b := range []string{"s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == s.Tips[b] {
			t.Errorf("%s did not move in the retry\n%s", b, again.String())
		}
	}
	undoesAll(t, ctx, s)
}

// The same failed finish with s1's worktree move cut between its ref and its
// files: the retry refuses on the cut move before any check of the fold,
// keeps the stack's record, and undo puts s1 back with its files.
func TestRetryAfterAFailedFinishAndACutMoveKeepsTheRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, epoch, unblock := amendedCheckStop(t)
	main := ctx.Repo.MainRoot
	ropts := noResumeAgents()
	ropts.Push = syncrun.PushNever
	ropts.Between = func(stage string, m wtsync.LayerMove) error {
		if stage == wtsync.StageAfterRef && m.Branch == "s1" {
			return errors.New("interrupted")
		}
		return nil
	}
	var first bytes.Buffer
	_ = SyncResume(ctx, gittest.StackTmp, ropts, &first)
	unblock()
	ropts.Between = nil
	var again bytes.Buffer
	err := SyncResume(ctx, gittest.StackTmp, ropts, &again)
	if err == nil || !strings.Contains(again.String(), "layer s1 has a move that was cut short") {
		t.Fatalf("retry: err %v, want the refusal naming s1\n%s", err, again.String())
	}
	if strings.Contains(again.String(), "the fold changed the line's content") {
		t.Errorf("the retry refused on the fold, not on the cut move:\n%s", again.String())
	}
	rec, ok, rerr := wtsync.ReadStackRecord(main, epoch)
	if rerr != nil || !ok || len(rec.Moves) == 0 || rec.Moves[0].Result != wtsync.MoveMoving {
		t.Fatalf("the record undo needs is gone or changed: %v %+v", rerr, rec)
	}
	undoesAll(t, ctx, s)
}
