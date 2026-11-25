package syncfix

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// wt#216: a fold onto trunk that stopped at a layer tip for a failed check
// accepts what the person amended into the commit at the stop, names the
// fixed path, and moves the layers.
func TestFoldOntoTrunkAcceptsAFixMadeAtACheckStop(t *testing.T) {
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
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	ropts := noResumeAgents()
	ropts.Push = syncrun.PushNever
	var human bytes.Buffer
	err := SyncResume(ctx, gittest.StackTmp, ropts, &human)
	if err == nil || err.Error() != "not completed: tmp/stack (2 layers held)" {
		t.Fatalf("resume: err %v, want only the two held layers\n%s", err, human.String())
	}
	if strings.Contains(human.String(), "no layer moved") || !strings.Contains(human.String(), "fix.txt taken as resolved at a stop") {
		t.Errorf("the closing lines do not accept and name fix.txt:\n%s", human.String())
	}
	if busy, _ := wtsync.RebaseInProgress(tmp); busy {
		t.Error("the rebase did not finish")
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":fix.txt"); got != "fixed" {
		t.Errorf("the line lacks the fix: %q", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == s.Tips[b] {
			t.Errorf("layer %s did not move\n%s", b, human.String())
		}
	}
}

// A commit added at the check stop, rather than an amend, is a fix the same
// way: the layer it ends and every layer above count it, the content check
// takes its path, and the layers move.
func TestFoldOntoTrunkAcceptsAFixCommitAddedAtACheckStop(t *testing.T) {
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
	var human bytes.Buffer
	err := SyncResume(ctx, gittest.StackTmp, ropts, &human)
	if err == nil || err.Error() != "not completed: tmp/stack (2 layers held)" {
		t.Fatalf("resume: err %v, want only the two held layers\n%s", err, human.String())
	}
	if !strings.Contains(human.String(), "fix.txt taken as resolved at a stop") {
		t.Errorf("the closing lines do not name fix.txt:\n%s", human.String())
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":fix.txt"); got != "fixed" {
		t.Errorf("the line lacks the fix: %q", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == s.Tips[b] {
			t.Errorf("layer %s did not move\n%s", b, human.String())
		}
	}
	if got := testkit.GitOut(t, main, "log", "-1", "--format=%s", "s2"); got != "fix: the check" {
		t.Errorf("s2 ends at %q, want the fix commit", got)
	}
}

// A fix that still fails its check is tried again: the stop comes back at the
// commit added for it, and the next attempt adds another commit or amends
// that one. Every added commit is counted, and the layers move.
func TestFoldOntoTrunkAcceptsFixesTriedAgainAtACheckStop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	for name, second := range map[string]func(t *testing.T, tmp string){
		"a second commit": func(t *testing.T, tmp string) {
			testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
			testkit.GitIn(t, tmp, "add", "fix.txt")
			testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "fix: the check, again")
		},
		"an amend of the first": func(t *testing.T, tmp string) {
			testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
			testkit.GitIn(t, tmp, "add", "fix.txt")
			testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, s, _ := synccmdtest.CheckedLine(t)
			main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
			opts := foldOntoTrunk()
			opts.EachCommit = "tips"
			if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
				t.Fatalf("err %v\n%s", err, human)
			}
			testkit.WriteFile(t, tmp, "attempt.txt", "not enough\n")
			testkit.GitIn(t, tmp, "add", "attempt.txt")
			testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "fix: first try")
			ropts := noResumeAgents()
			ropts.Push = syncrun.PushNever
			var first bytes.Buffer
			if err := SyncResume(ctx, gittest.StackTmp, ropts, &first); err == nil || !strings.Contains(err.Error(), "needs you") {
				t.Fatalf("first resume: err %v, want the check to stop it again\n%s", err, first.String())
			}
			second(t, tmp)
			var human bytes.Buffer
			err := SyncResume(ctx, gittest.StackTmp, ropts, &human)
			if err == nil || err.Error() != "not completed: tmp/stack (2 layers held)" {
				t.Fatalf("second resume: err %v, want only the two held layers\n%s", err, human.String())
			}
			for _, f := range []string{"attempt.txt", "fix.txt"} {
				if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":"+f); got == "" {
					t.Errorf("the line lacks %s", f)
				}
			}
			for _, b := range []string{"s1", "s2", "s4"} {
				if testkit.GitOut(t, main, "rev-parse", b) == s.Tips[b] {
					t.Errorf("layer %s did not move\n%s", b, human.String())
				}
			}
		})
	}
}
