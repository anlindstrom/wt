package synccmd_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

func tipsFold() synccmd.RunOptions {
	opts := synccmdtest.NoAgents()
	opts.EachCommit = "tips"
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	return opts
}

// An owed step that changes the layer makes it a changed one: the layer's
// other step and its check run as in any run, since the owed one moves HEAD.
// f1 is below the fixup's target; the generator is owed there.
func TestAnOwedStepThatMovesAnUnchangedLayerDoesNotLeaveItsOtherStepsSkipped(t *testing.T) {
	t.Parallel()
	skipShort(t)
	dir := t.TempDir()
	checks, steps := filepath.Join(dir, "checks.log"), filepath.Join(dir, "steps.log")
	gen := "printf x > gen/extra.txt"
	yaml := "each_commit:\n  - run: git log -1 --format=%s >> '" + checks + "'\n" +
		"defer:\n  - run: " + gen + "\n    paths: [gen/**]\n    each: layer\n    fold: owner\n    commit: \"chore: extra\"\n" +
		"  - run: git log -1 --format=%s >> '" + steps + "'\n    each: layer\n"
	ctx, _ := synccmdtest.FoldLine(t, yaml)
	main := ctx.Repo.MainRoot
	owed := &wtsync.Owed{Branch: gittest.FoldTmp, Epoch: 1, EachCommit: "tips", Steps: []wtsync.OwedStep{{Step: gen, Layer: "f1", Why: "docker did not answer"}}}
	if err := wtsync.WriteOwed(main, owed); err != nil {
		t.Fatal(err)
	}
	_, human, _ := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, tipsFold())
	if got := ranAt(t, steps); len(got) == 0 || got[0] != "chore(f1):_bump_to_1.1.0" {
		t.Errorf("the other step ran at %q, want it to start at f1\n%s", got, human)
	}
	if got := ranAt(t, checks); len(got) == 0 || got[0] != "chore(f1):_bump_to_1.1.0" {
		t.Errorf("checked %q, want f1 checked first\n%s", got, human)
	}
	if got := testkit.GitOut(t, main, "show", "f1:gen/extra.txt"); got != "x" {
		t.Errorf("the owed generator's output is not in f1: %q", got)
	}
	if left, err := wtsync.ReadOwed(main, gittest.FoldTmp); err != nil || left != nil {
		t.Errorf("owed record %+v (%v), want it settled", left, err)
	}
}

// A step owed at a layer where none of the listed paths changed still runs:
// the paths gate does not settle a debt by calling it not due.
func TestAnOwedStepAtALayerWithNoMatchingPathStillRuns(t *testing.T) {
	t.Parallel()
	skipShort(t)
	dir := t.TempDir()
	steps := filepath.Join(dir, "steps.log")
	step := "git log -1 --format=%s >> '" + steps + "'"
	yaml := "defer:\n  - run: " + step + "\n    paths: [nothing/**]\n    each: layer\n"
	ctx, _ := synccmdtest.FoldLine(t, yaml)
	main := ctx.Repo.MainRoot
	owed := &wtsync.Owed{Branch: gittest.FoldTmp, Epoch: 1, EachCommit: "tips", Steps: []wtsync.OwedStep{{Step: step, Layer: "f1", Why: "left owed by steps --all"}}}
	if err := wtsync.WriteOwed(main, owed); err != nil {
		t.Fatal(err)
	}
	_, human, _ := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, tipsFold())
	if got, want := ranAt(t, steps), []string{"chore(f1):_bump_to_1.1.0"}; !slices.Equal(got, want) {
		t.Errorf("the owed step ran at %q, want %q\n%s", got, want, human)
	}
	if strings.Contains(human, "layer 1 unchanged: its checks and steps skipped") {
		t.Errorf("said layer 1 was skipped though a step ran there:\n%s", human)
	}
	if left, err := wtsync.ReadOwed(main, gittest.FoldTmp); err != nil || left != nil {
		t.Errorf("owed record %+v (%v), want it settled by the run", left, err)
	}
}
