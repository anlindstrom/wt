package synccmd_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// refsOf is every layer and the line, as the repository has them.
func refsOf(t *testing.T, main string, branches []string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, b := range branches {
		got[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	return got
}

// A stack line whose base is behind trunk by a version bump that conflicts
// with a line commit, folded in place: the header says the fold is in place
// on the line's own base and where the declaration is read from, and prints
// no "contested" stop for the conflict with trunk, which an in-place fold
// never meets. The fold finishes, and undo puts every ref back.
func TestFoldInPlaceHeaderSaysSoAndPrintsNoTrunkStop(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "conflicts: []\n")
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	main := ctx.Repo.MainRoot
	branches := append(append([]string{}, s.Layers...), gittest.StackTmp)
	before := refsOf(t, main, branches)
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v\n%s", err, human)
	}
	base := s.Base[:7]
	for _, want := range []string{
		"wt sync rebase  folds tmp/stack in place on its own base " + base + "\n",
		"the declaration and its scripts are read from trunk, origin/main ",
		"✓ rebased 12 commits onto its own base " + base,
	} {
		if !strings.Contains(human, want) {
			t.Errorf("missing %q in\n%s", want, human)
		}
	}
	for _, bad := range []string{"wt sync rebase  onto origin/main", "contested at"} {
		if strings.Contains(human, bad) {
			t.Errorf("an in-place fold printed %q\n%s", bad, human)
		}
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Fold == nil || !p.Fold.InPlace || p.Fold.Base != s.Base {
		t.Fatalf("fold %+v, want in place on %s", p.Fold, s.Base)
	}
	if after := refsOf(t, main, branches); after[gittest.StackTmp] == before[gittest.StackTmp] || after["s2"] == before["s2"] {
		t.Fatalf("the fold moved nothing: %v", after)
	}
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.StackTmp, syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for b, want := range before {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != want {
			t.Errorf("after undo %s is %s, want %s\n%s", b, got, want, undone.String())
		}
	}
}

// A plain --stack rebase onto trunk keeps its header and the contested stop
// it will meet.
func TestPlainStackRebaseKeepsTheOntoTrunkHeaderAndContestedLine(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := synccmdtest.StackLine(t, "conflicts: []\n")
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	for _, want := range []string{"wt sync rebase  onto origin/main ", "⚠ contested at "} {
		if !strings.Contains(human, want) {
			t.Errorf("missing %q in\n%s", want, human)
		}
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Fold != nil {
		t.Errorf("fold %+v for a run that does not fold", p.Fold)
	}
}

// A fold that passes the tree guard says what it compared: the old tip,
// and the declared step paths it left out when a changed file matched one.
func TestFoldSaysWhatTheTreeGuardComparedWhenNothingWasExcluded(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	old := testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if want := "  ✓ tip tree equal to " + old[:7] + "\n"; !strings.Contains(human, want) {
		t.Errorf("missing %q in\n%s", want, human)
	}
	g := synccmdtest.Participant(t, res, gittest.StackTmp).Fold
	if g == nil || g.Guard == nil {
		t.Fatalf("fold %+v", g)
	}
	if g.Guard.Outcome != "passed" || g.Guard.OldTip != old || g.Guard.Onto != nil ||
		len(g.Guard.ExcludedGlobs) != 0 || len(g.Guard.ConflictAllowed) != 0 || g.Guard.Reason != nil {
		t.Errorf("guard %+v", g.Guard)
	}
}

func TestFoldSaysWhichDeclaredStepPathsTheTreeGuardLeftOut(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.FoldLine(t, gittest.FoldYAML("max-plus-patch"))
	old := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", gittest.FoldTmp)
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	_ = s
	if want := "  ✓ tip tree equal to " + old[:7] + " outside gen/**\n"; !strings.Contains(human, want) {
		t.Errorf("missing %q in\n%s", want, human)
	}
	g := synccmdtest.Participant(t, res, gittest.FoldTmp).Fold
	if g == nil || g.Guard == nil || g.Guard.Outcome != "passed" || len(g.Guard.ExcludedGlobs) != 1 || g.Guard.ExcludedGlobs[0] != "gen/**" {
		t.Fatalf("fold %+v", g)
	}
}

// A run that stops before it has read the line still says what it was
// running: the plain header, as before, not none.
func TestFoldThatFailsBeforeTheLineIsReadStillPrintsAHeader(t *testing.T) {
	t.Parallel()
	skipShort(t)
	// A fold: owner step without each: layer is refused before the line is read.
	yaml := strings.Replace(gittest.FoldYAML("max-plus-patch"), "    each: layer\n", "", 1)
	ctx, _ := synccmdtest.FoldLine(t, yaml)
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "fold: owner without each: layer") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if !strings.HasPrefix(human, "wt sync rebase  onto origin/main ") {
		t.Errorf("the run printed no header:\n%s", human)
	}
}
