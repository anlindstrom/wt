package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// liftedLine is a stack line whose top commit, in layer s5, changes x.txt
// and y.txt, and a trunk that took that commit reworked, under its subject
// and author: trunk's copy writes x.txt its own way, and y.txt too when
// both is set. It returns the stack's commit, trunk's, and every branch's
// tip before the run.
func liftedLine(t *testing.T, both bool) (ctx *kit.Context, s *gittest.StackLine, lifted, trunk string, tips map[string]string) {
	t.Helper()
	ctx, s = synccmdtest.StackLine(t, "")
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	const subject = "feat(s5): lift the signup check"
	testkit.WriteFile(t, tmp, "x.txt", "stack's x\n")
	testkit.WriteFile(t, tmp, "y.txt", "stack's y\n")
	testkit.GitIn(t, tmp, "add", "x.txt", "y.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", subject)
	lifted = testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	testkit.GitIn(t, s.Paths["s5"], "merge", "-q", "--ff-only", lifted)
	testkit.WriteFile(t, main, "x.txt", "trunk's x\n")
	testkit.GitIn(t, main, "add", "x.txt")
	if both {
		testkit.WriteFile(t, main, "y.txt", "trunk's y\n")
		testkit.GitIn(t, main, "add", "y.txt")
	}
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", subject)
	trunk = testkit.GitOut(t, main, "rev-parse", "HEAD")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	tips = map[string]string{gittest.StackTmp: lifted}
	for _, b := range s.Layers {
		tips[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	return ctx, s, lifted, trunk, tips
}

// runToLiftedStop runs the line, which stops at the lifted commit: the run's
// look names trunk's copy, and the stop offers the drop by that commit.
func runToLiftedStop(t *testing.T, ctx *kit.Context, s *gittest.StackLine, lifted, trunk string) {
	t.Helper()
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want the stop at the lifted commit\n%s", err, human)
	}
	look := "may already be on trunk as " + git.ShortID(trunk, 7)
	offer := "wt sync resume tmp/stack --drop " + git.ShortID(lifted, 7)
	if !strings.Contains(human, look) || !strings.Contains(human, offer) {
		t.Errorf("want the look's %q and the stop's %q in\n%s", look, offer, human)
	}
	gitDir, err := wtsync.GitDir(s.Paths[gittest.StackTmp])
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok || st.StoppedCommit != lifted {
		t.Fatalf("the run stopped at %+v (%v %v), want the lifted commit %s, which the look named", st.StoppedCommit, ok, err, lifted)
	}
	plan := readFile(t, wtsync.PlanPath(gitDir))
	if !strings.Contains(plan, offer) || !strings.Contains(plan, git.ShortID(trunk, 7)) {
		t.Errorf("the plan does not offer the drop:\n%s", plan)
	}
}

func dropResume(drop string) syncfix.ResumeOptions {
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	ropts.Drop = drop
	return ropts
}

func undoAndCheck(t *testing.T, ctx *kit.Context, tips map[string]string) {
	t.Helper()
	uopts := syncfix.UndoOptions{}
	uopts.Agents = []agents.Agent{}
	uopts.Now = func() time.Time { return time.Unix(9, 0) }
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.StackTmp, uopts, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for b, want := range tips {
		if got := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", b); got != want {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, want, undone.String())
		}
	}
}

// wt#214: the stop at a commit trunk carries reworked offers the drop, and
// nothing is dropped unless it is taken by that commit. A drop named by
// another commit is refused with the stop as it was. Taken, it keeps the
// stack's change to y.txt, which trunk's copy does not touch, takes trunk's
// x.txt, saves what the person had written in x.txt at the stop as a blob,
// and the line goes on. Undo puts every branch back.
func TestDropAtAReworkedStopKeepsWhatTrunkDoesNotTouch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, lifted, trunk, tips := liftedLine(t, false)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	runToLiftedStop(t, ctx, s, lifted, trunk)

	var refused bytes.Buffer
	err := syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(s.Tips["s1"]), &refused)
	if err == nil || !strings.Contains(err.Error(), "not the commit the rebase stopped at") {
		t.Fatalf("a drop of another commit: err %v\n%s", err, refused.String())
	}
	if st := testkit.GitOut(t, tmp, "status", "--porcelain=v1", "--", "x.txt"); st != "AA x.txt" {
		t.Fatalf("the refused drop touched x.txt: %q", st)
	}

	testkit.WriteFile(t, tmp, "x.txt", "half merged by hand\n")
	var human bytes.Buffer
	err = syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(git.ShortID(lifted, 7)), &human)
	if err != nil && strings.Contains(human.String(), "no layer moved") {
		t.Fatalf("resume: %v\n%s", err, human.String())
	}
	out := human.String()
	if !strings.Contains(out, "took trunk's x.txt") || !strings.Contains(out, "kept its y.txt") {
		t.Errorf("the resume does not say what the drop took and kept:\n%s", out)
	}
	blob := regexp.MustCompile(`x\.txt as you left it is kept as ([0-9a-f]{7,})`).FindStringSubmatch(out)
	if blob == nil {
		t.Fatalf("the person's x.txt is not named as kept:\n%s", out)
	}
	if got := testkit.GitOut(t, main, "cat-file", "-p", blob[1]); got != "half merged by hand" {
		t.Errorf("the kept blob holds %q", got)
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":x.txt"); got != "trunk's x" {
		t.Errorf("the line's x.txt is %q, want trunk's", got)
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":y.txt"); got != "stack's y" {
		t.Errorf("the line's y.txt is %q, want the stack's", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == tips[b] {
			t.Errorf("layer %s did not move\n%s", b, out)
		}
	}
	undoAndCheck(t, ctx, tips)
	if got := readFile(t, filepath.Join(tmp, "x.txt")); got != "stack's x\n" {
		t.Errorf("x.txt in the line's worktree is %q after the undo", got)
	}
}

// When trunk's copy touches every file the commit does, the drop leaves
// nothing of it: the commit is dropped as resolved to trunk's side, the
// layers that held it count one commit fewer and move, and undo puts every
// branch back.
func TestDropAtAReworkedStopDropsACommitTrunkFullyTook(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, lifted, trunk, tips := liftedLine(t, true)
	main := ctx.Repo.MainRoot
	runToLiftedStop(t, ctx, s, lifted, trunk)
	var human bytes.Buffer
	err := syncfix.SyncResume(ctx, gittest.StackTmp, dropResume(lifted), &human)
	if err != nil && strings.Contains(human.String(), "no layer moved") {
		t.Fatalf("resume: %v\n%s", err, human.String())
	}
	if n := testkit.GitOut(t, main, "rev-list", "--count", "--grep", "lift the signup check", "origin/main.."+gittest.StackTmp); n != "0" {
		t.Errorf("the line still carries the stack's copy (%s)\n%s", n, human.String())
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":y.txt"); got != "trunk's y" {
		t.Errorf("the line's y.txt is %q, want trunk's", got)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == tips[b] {
			t.Errorf("layer %s did not move\n%s", b, human.String())
		}
	}
	undoAndCheck(t, ctx, tips)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
