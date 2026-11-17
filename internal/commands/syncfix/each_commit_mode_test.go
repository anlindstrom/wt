package syncfix

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

func tipsOptions() synccmd.RunOptions {
	opts := synccmdtest.NoAgents()
	opts.EachCommit = "tips"
	return opts
}

// A run under --each-commit=tips that stopped at a layer's tip goes on under
// tips after a fix amended into that tip: resume takes no flag, runs the
// check again at the amended commit, checks the tips above it and nothing
// between, and finishes the line.
func TestSyncResumeAfterATipsStopKeepsTips(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, logPath := synccmdtest.CheckedLine(t)
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, tipsOptions()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	res, err := syncResumeJSON(t, ctx, gittest.StackTmp, opts)
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume stopped: %v", err)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Result != syncrun.ResultRebased || p.EachCommit != "tips" {
		t.Fatalf("participant %+v", p)
	}
	want := []string{"feat(s1): a2.txt", "feat(s2): b2.txt", "feat(s2): b2.txt", "chore(s3): bump to 1.0.1", "feat(s5): e3.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q", got, want)
	}
	if busy, _ := wtsync.RebaseInProgress(tmp); busy {
		t.Error("the rebase did not finish")
	}
	if got := testkit.GitOut(t, tmp, "show", "HEAD:fix.txt"); got != "fixed" {
		t.Errorf("the fix is not in the line: %q", got)
	}
}

// Undo after a tips stop restores the line, every layer, the worktrees and
// leaves no marker behind and the safety ref on the old tip; the stop itself
// moved nothing but the line's own rebase.
func TestSyncUndoAfterATipsStopRestoresEverything(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, _ := synccmdtest.CheckedLine(t)
	main := ctx.Repo.MainRoot
	stash := testkit.GitOut(t, main, "stash", "list")
	dirty := testkit.GitOut(t, s.Paths["s3"], "status", "--porcelain")
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, tipsOptions()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	undoBack(t, main, s, s.Layers...)
	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	undoBack(t, main, s, append([]string{gittest.StackTmp}, s.Layers...)...)
	if got := testkit.GitOut(t, s.Paths["s3"], "status", "--porcelain"); got != dirty {
		t.Errorf("s3's worktree: %q, was %q", got, dirty)
	}
	if got := testkit.GitOut(t, main, "stash", "list"); got != stash {
		t.Errorf("the stash changed: %q", got)
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", wtsync.StackMapPrefix); refs != "" {
		t.Errorf("markers left behind:\n%s", refs)
	}
	// wt keeps the safety ref after an undo; it must still pin the old tip.
	if got := testkit.GitOut(t, main, "rev-parse", "refs/wt-sync/tmp/stack/99"); got != s.Tips[gittest.StackTmp] {
		t.Errorf("the safety ref is at %s, want the old tip %s", got, s.Tips[gittest.StackTmp])
	}
	if busy, _ := wtsync.RebaseInProgress(s.Paths[gittest.StackTmp]); busy {
		t.Error("a rebase is still in progress")
	}
}
