package syncfix

import (
	"bytes"
	"os"
	"path/filepath"
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

// A resume refused before its rebase reports the mode of its run, not the
// default.
func TestSyncResumeRefusedByANeedStillReportsItsRunsMode(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, _, _ := synccmdtest.CheckedLineWith(t, "each_commit:\n  - run: bin/check.sh\n    needs: [docker]\n")
	up := &synccmdtest.Probe{}
	opts := tipsOptions()
	opts.Probes = up.Probes()
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	down := noResumeAgents()
	down.Probes = synccmdtest.DockerDown().Probes()
	res, err := syncResumeJSON(t, ctx, gittest.StackTmp, down)
	if err == nil {
		t.Fatal("the resume was not refused")
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultRefused || p.EachCommit != "tips" {
		t.Errorf("participant result %s, eachCommit %q, want refused and tips", p.Result, p.EachCommit)
	}
}

// A run under off that a conflict handed over resumes without asking for the
// need of a check that is off.
func TestSyncResumeUnderOffDoesNotAskForTheNeedsOfDisabledChecks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, _ := synccmdtest.CheckedLineYAML(t, "conflicts: []\neach_commit:\n  - run: bin/check.sh\n    needs: [docker]\n")
	opts := synccmdtest.NoAgents()
	opts.EachCommit = "off"
	opts.Probes = (&synccmdtest.Probe{}).Probes()
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	docker := synccmdtest.DockerDown()
	resume := noResumeAgents()
	resume.Push = syncrun.PushNever
	resume.Probes = docker.Probes()
	res, err := syncResumeJSON(t, ctx, gittest.StackTmp, resume)
	if err != nil && strings.Contains(err.Error(), "docker") {
		t.Fatalf("refused for docker: %v", err)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result == syncrun.ResultRefused || p.EachCommit != "off" {
		t.Errorf("participant %s %q (%v)", p.Result, p.EachCommit, p.Reason)
	}
	if docker.Calls() != 0 {
		t.Errorf("docker was probed %d times", docker.Calls())
	}
}

// A fix that touches the paths of a check the layer did not call for makes
// the resume run that check too, as under all.
func TestSyncResumeAfterATipsStopRunsTheChecksTheFixCallsFor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	extra := filepath.Join(t.TempDir(), "extra.log")
	ctx, s, _ := synccmdtest.CheckedLineWith(t, "each_commit:\n  - run: bin/check.sh\n  - run: echo extra >> '"+extra+"'\n    paths: [extra.txt]\n")
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, tipsOptions()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.WriteFile(t, tmp, "extra.txt", "extra\n")
	testkit.GitIn(t, tmp, "add", "fix.txt", "extra.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	if _, err := syncResumeJSON(t, ctx, gittest.StackTmp, opts); err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume stopped: %v", err)
	}
	raw, _ := os.ReadFile(extra)
	if got := strings.TrimSpace(string(raw)); got != "extra" {
		t.Errorf("the extra check ran %q, want once", got)
	}
}

// A stack's resume describes the tips as each layer's, as the run did,
// whether or not per-layer steps are declared: in the summary at the end, and
// in the plan of a stop it hands over again.
func TestSyncResumeOfAStackWithoutLayerStepsNamesTheLayerTips(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, _ := synccmdtest.CheckedLine(t)
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, tipsOptions()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := s.Paths[gittest.StackTmp]
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	// Resumed without a fix: the check fails again, and the plan is rewritten.
	var out bytes.Buffer
	if err := SyncResume(ctx, gittest.StackTmp, opts, &out); err == nil {
		t.Fatalf("the resume went past a failing check:\n%s", out.String())
	}
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if plan := readFile(t, wtsync.PlanPath(gitDir)); !strings.Contains(plan, "the tip of each layer only") {
		t.Errorf("the plan of the second stop:\n%s", plan)
	}
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	out.Reset()
	if err := SyncResume(ctx, gittest.StackTmp, opts, &out); err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume stopped: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "the tip of each layer only") {
		t.Errorf("the summary does not name the layer tips:\n%s", out.String())
	}
}
