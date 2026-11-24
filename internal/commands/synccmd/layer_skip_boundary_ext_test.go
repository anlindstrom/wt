package synccmd_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A fixup for a commit in layer 1 rewrites layer 1 and every layer above:
// nothing is unchanged, so every layer's check and step runs and no line says
// a layer was skipped. The line keeps its tree, and undo puts every ref back.
func TestAFoldThatRewritesTheFirstLayerSkipsNothing(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, checks, steps := layerSkipLine(t)
	main := ctx.Repo.MainRoot
	fixup(t, s.Paths[gittest.FoldTmp], "src/a.txt", "feat(f1): a")
	branches := append(append([]string{}, s.Layers...), gittest.FoldTmp)
	before := refsOf(t, main, branches)
	_, human, _ := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, tipsFold())
	want := []string{"chore(f1):_bump_to_1.1.0", "feat(f2):_more", "chore(f3):_bump_to_1.1.1", "feat(f4):_docs", "chore(f5):_bump_to_1.1.2"}
	if got := ranAt(t, checks); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
	if got := ranAt(t, steps); !slices.Equal(got[:len(want)], want) {
		t.Errorf("step ran at %q, want it to start with %q\n%s", got, want, human)
	}
	if strings.Contains(human, "unchanged: ") {
		t.Errorf("a layer was said to be skipped:\n%s", human)
	}
	if after := refsOf(t, main, branches); after["f1"] == before["f1"] {
		t.Errorf("f1 did not move: %v", after)
	}
}

// A run that moves the base (--onto trunk, which is ahead) rewrites every
// commit: no layer's tip is the commit it was, so nothing is skipped.
func TestARunOntoANewBaseSkipsNothing(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, checks, steps := layerSkipLine(t)
	opts := foldOnto("origin/main")
	opts.EachCommit = "tips"
	_, human, _ := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	if strings.Contains(human, "unchanged: ") {
		t.Errorf("a layer was said to be skipped:\n%s", human)
	}
	if got := ranAt(t, steps); len(got) == 0 || got[0] != "feat(f1):_a" {
		t.Errorf("step ran at %q, want it to start at f1 (its bump is trunk's own, so its tip is feat(f1): a)\n%s", got, human)
	}
	if got := ranAt(t, checks); len(got) == 0 || got[0] != "feat(f1):_a" {
		t.Errorf("checked %q, want f1 checked first\n%s", got, human)
	}
}

// The run says once, at its end, which layers it skipped: here f1 and f3, with
// f2 not skipped because a step is owed there, and f4 rewritten by the fixup.
func TestTheSkippedLayersAreSaidOnceForTheWholeRun(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _, steps := layerSkipLine(t)
	tmp := s.Paths[gittest.FoldTmp]
	testkit.GitIn(t, tmp, "reset", "-q", "--hard", "HEAD~1") // the line's own fixup of f2
	fixup(t, tmp, "docs/d.txt", "feat(f4): docs")
	step := "git log -1 --format=%s >> '" + steps + "'"
	owed := &wtsync.Owed{Branch: gittest.FoldTmp, Epoch: 1, EachCommit: "tips", Steps: []wtsync.OwedStep{{Step: step, Layer: "f2", Why: "left owed"}}}
	if err := wtsync.WriteOwed(ctx.Repo.MainRoot, owed); err != nil {
		t.Fatal(err)
	}
	_, human, _ := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, tipsFold())
	if n := strings.Count(human, "unchanged: their checks and steps skipped"); n != 1 || !strings.Contains(human, "⏭ layers 1, 3 unchanged: their checks and steps skipped\n") {
		t.Errorf("want one line naming layers 1, 3 in\n%s", human)
	}
	if got := ranAt(t, steps)[0]; got != "feat(f2):_more" {
		t.Errorf("the owed step's first run was at %q, want f2's tip", got)
	}
}
