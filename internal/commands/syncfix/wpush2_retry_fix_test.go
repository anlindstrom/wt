package syncfix

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// A resume cut right after it kept the commit added at a check stop with the
// handover, before the rebase went on, is resumed again after the person
// amended that commit once more. The fix is counted once, as the amend the
// second resume found, and the layers move; undo puts every branch back.
func TestResumeRetriedAfterKeepingAnAddedFixCountsItOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, _ := synccmdtest.CheckedLine(t)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	opts := foldOntoTrunk()
	opts.EachCommit = "tips"
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "fix: the check")
	ropts := noResumeAgents()
	ropts.Push = syncrun.PushNever
	ropts.AfterKeepStop = func() error { return errors.New("interrupted") }
	var cut bytes.Buffer
	if err := SyncResume(ctx, gittest.StackTmp, ropts, &cut); err == nil || err.Error() != "interrupted" {
		t.Fatalf("first resume: err %v, want the interrupt\n%s", err, cut.String())
	}
	refs := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	if got := testkit.GitOut(t, tmp, "log", "-1", "--format=%s"); got != "fix: the check" {
		t.Fatalf("the interrupt moved the rebase on: HEAD is %q", got)
	}

	testkit.WriteFile(t, tmp, "fix.txt", "fixed again\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != refs {
		t.Fatalf("branches moved between the resumes:\nbefore %s\nafter %s", refs, got)
	}
	ropts.AfterKeepStop = nil
	var human bytes.Buffer
	err := SyncResume(ctx, gittest.StackTmp, ropts, &human)
	if err == nil || err.Error() != "not completed: tmp/stack (2 layers held)" {
		t.Fatalf("second resume: err %v, want only the two held layers\n%s", err, human.String())
	}
	if strings.Contains(human.String(), "no layer moved") || !strings.Contains(human.String(), "fix.txt taken as resolved at a stop") {
		t.Errorf("the closing lines do not accept and name fix.txt:\n%s", human.String())
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":fix.txt"); got != "fixed again" {
		t.Errorf("the line lacks the amended fix: %q", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == s.Tips[b] {
			t.Errorf("layer %s did not move\n%s", b, human.String())
		}
	}
	if got := testkit.GitOut(t, main, "log", "-1", "--format=%s", "s2"); got != "fix: the check" {
		t.Errorf("s2 ends at %q, want the fix commit", got)
	}
	var undone bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s", b, got, s.Tips[b])
		}
	}
}
