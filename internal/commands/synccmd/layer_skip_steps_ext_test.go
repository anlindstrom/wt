package synccmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// stepsTipChecks is what the check runs at when steps settles the owed
// generator under tips: each layer's tip, f1's and f3's again after the fold
// that put the generator's output into their owners, and the line's top.
var stepsTipChecks = []string{"chore(f1):_bump_to_1.1.0", "chore(f1):_bump_to_1.1.0", "feat(f2):_more", "chore(f3):_bump_to_1.1.1",
	"chore(f3):_bump_to_1.1.1", "feat(f4):_docs", "chore(f5):_bump_to_1.1.2", "chore:_regenerate_the_spec", "fixup!_feat(f2):_notes"}

// stepsAfterTips is a FoldLine whose generator was blocked in a run under
// --each-commit=tips, which left it owed at every layer; the block is gone,
// and checks is the log of the recording each_commit check, emptied.
func stepsAfterTips(t *testing.T) (ctx *kit.Context, checks string) {
	t.Helper()
	dir := t.TempDir()
	block, checks := filepath.Join(dir, "docker-down"), filepath.Join(dir, "checks.log")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := strings.Replace(gittest.FoldYAML("max-plus-patch"), "run: ./gen.sh", "run: test ! -e "+block+" && ./gen.sh", 1)
	yaml = strings.Replace(yaml, "run: test ! -e broken.marker", "run: git log -1 --format=%s >> '"+checks+"'", 1)
	ctx, s := synccmdtest.FoldLine(t, yaml)
	testkit.GitIn(t, s.Paths["f3"], "checkout", "--", ".")
	opts := synccmdtest.NoAgents()
	opts.Now = func() time.Time { return time.Unix(0, 400) }
	opts.EachCommit = "tips"
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts); err == nil || !strings.Contains(err.Error(), "owed") {
		t.Fatalf("err %v, want the step owed\n%s", err, human)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(checks); err != nil {
		t.Fatal(err)
	}
	return ctx, checks
}

func stepsRun(t *testing.T, ctx *kit.Context, each string, epoch int64, all ...bool) string {
	t.Helper()
	opts := synccmdtest.NoAgents()
	opts.Now = func() time.Time { return time.Unix(0, epoch) }
	opts.Steps = &synccmd.StepsOptions{EachCommit: each, All: len(all) > 0 && all[0]}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	if err != nil {
		t.Fatalf("steps: %v\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.FoldTmp); p.Result != syncrun.ResultRebased {
		t.Fatalf("steps: %+v\n%s", p, human)
	}
	return human
}

// wt sync steps keeps the --each-commit of the run that owed its steps: after
// a run under tips, the check runs at each layer's tip, not at every commit,
// and the header says where the mode came from. --each-commit=all overrides it.
func TestSyncStepsKeepsTheEachCommitOfTheRunThatOwedThem(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, checks := stepsAfterTips(t)
	owed, err := wtsync.ReadOwed(ctx.Repo.MainRoot, gittest.FoldTmp)
	if err != nil || owed == nil {
		t.Fatalf("owed %+v %v", owed, err)
	}
	human := stepsRun(t, ctx, "", 500)
	if !strings.Contains(human, "each_commit: tips, as the run that owed these steps\n") {
		t.Errorf("the header does not name the mode:\n%s", human)
	}
	if got := ranAt(t, checks); !slices.Equal(got, stepsTipChecks) {
		t.Errorf("checked %q, want %q\n%s", got, stepsTipChecks, human)
	}
	if err := os.Remove(checks); err != nil {
		t.Fatal(err)
	}
	// The steps are owed again for a second look: put the record back.
	if err := wtsync.WriteOwed(ctx.Repo.MainRoot, owed); err != nil {
		t.Fatal(err)
	}
	human = stepsRun(t, ctx, "all", 600)
	if !strings.Contains(human, "each_commit: all, as --each-commit says\n") {
		t.Errorf("the header does not name the flag:\n%s", human)
	}
	for _, c := range []string{"feat(f2):_notes", "feat(f3):_c"} {
		if !slices.Contains(ranAt(t, checks), c) {
			t.Errorf("the check did not run at %s under all: %q\n%s", c, ranAt(t, checks), human)
		}
	}
}

// A record written before the mode was kept names none: steps runs as it
// always did, and says so in one line.
func TestSyncStepsFallsBackToAllForAnOldOwedRecord(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, checks := stepsAfterTips(t)
	main := ctx.Repo.MainRoot
	o, err := wtsync.ReadOwed(main, gittest.FoldTmp)
	if err != nil || o == nil {
		t.Fatalf("owed %+v %v", o, err)
	}
	o.EachCommit = ""
	if err := wtsync.WriteOwed(main, o); err != nil {
		t.Fatal(err)
	}
	human := stepsRun(t, ctx, "", 500)
	if want := "each_commit: all, the default: the record of what was owed names no mode (an older wt wrote it)\n"; !strings.Contains(human, want) {
		t.Errorf("missing %q in\n%s", want, human)
	}
	if n := len(ranAt(t, checks)); n < 7 {
		t.Errorf("the check ran %d times, want every commit's under all", n)
	}
}

// A run under targets folded fixups; steps folds none, so what it can honestly
// keep is the tips, and the header says that is what it did.
func TestSyncStepsTurnsRecordedTargetsIntoTipsAndSaysSo(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, checks := stepsAfterTips(t)
	main := ctx.Repo.MainRoot
	o, err := wtsync.ReadOwed(main, gittest.FoldTmp)
	if err != nil || o == nil {
		t.Fatalf("owed %+v %v", o, err)
	}
	o.EachCommit = "targets"
	if err := wtsync.WriteOwed(main, o); err != nil {
		t.Fatal(err)
	}
	human := stepsRun(t, ctx, "", 500)
	want := "each_commit: tips, as the run that owed these steps; it is targets, and wt sync steps folds no fixup, so each layer's tip is the target\n"
	if !strings.Contains(human, want) {
		t.Errorf("missing %q in\n%s", want, human)
	}
	if got := ranAt(t, checks); !slices.Equal(got, stepsTipChecks) {
		t.Errorf("checked %q, want %q\n%s", got, stepsTipChecks, human)
	}
}

// steps --all runs every declared step but still keeps the recorded mode.
func TestSyncStepsAllKeepsTheRecordedMode(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, checks := stepsAfterTips(t)
	human := stepsRun(t, ctx, "", 500, true)
	if !strings.Contains(human, "each_commit: tips, as the run that owed these steps\n") {
		t.Errorf("the header does not name the recorded mode:\n%s", human)
	}
	if got := ranAt(t, checks); !slices.Equal(got, stepsTipChecks) {
		t.Errorf("checked %q, want %q\n%s", got, stepsTipChecks, human)
	}
}
