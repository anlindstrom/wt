package syncfix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A step that folds its output into the owner of it in a lower layer leaves
// the upper layer's pick empty: that one drop is counted once, and the
// layers end where the rule says.
func TestStackOntoADropAStepCausedIsCountedOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	yaml := "conflicts: []\ndefer:\n  - run: ./gen.sh\n    paths: [gen/**]\n    each: layer\n    fold: owner\n    commit: \"chore: regenerate\"\n"
	ctx, tmp, _ := endsLineWith(t, yaml, map[string]string{"gen/x": "zero\n"}, map[string]string{"gen.sh": "#!/bin/sh\nmkdir -p gen\nprintf 'two\\n' > gen/x\n"},
		[]endsLayer{
			{"e1", []endsCommit{{"feat(e1): a", "gen/x", "one\n", ""}}},
			{"e2", []endsCommit{{"feat(e2): b", "gen/x", "two\n", ""}}},
			{"e3", []endsCommit{{"feat(e3): c", "c.txt", "c\n", ""}}},
		}, nil)
	runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a",
		"e3":    "feat(e1): a | feat(e3): c",
		endsTmp: "feat(e1): a | feat(e3): c",
	}, nil)
}

// Two commits of one layer with the same author, date and subject, the second
// emptied by take-trunk: wt cannot tell which of them was dropped, so the
// layer's count is not lowered, nothing moves, and the run says why.
func TestStackOntoTwinCommitsOfALayerAreNotToldApartAndNothingMoves(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	const date = "2020-01-01T00:00:00"
	ctx, tmp, tips := endsLine(t, takeLockYAML, []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"chore(e2): twin", "x.txt", "x\n", date}, {"chore(e2): twin", "lock.txt", "lock e2\n", date}}},
		{"e3", []endsCommit{{"feat(e3): c", "c.txt", "c\n", ""}}},
	}, nil)
	main := ctx.Repo.MainRoot
	was := endsState(t, ctx, tmp)
	res, human, err := synccmdtest.StackRun(t, ctx, endsTmp, foldOntoTrunk())
	if err == nil || !strings.Contains(err.Error(), "no layer moved") {
		t.Fatalf("err %v, want no layer moved\n%s", err, human)
	}
	if !strings.Contains(human, "cannot tell from another commit with the same author, date and subject") {
		t.Errorf("the run does not say why:\n%s", human)
	}
	if p := synccmdtest.Participant(t, res, endsTmp); p.Stack == nil || p.Stack.Refusal == nil {
		t.Errorf("stack %+v, want a refusal", p.Stack)
	}
	for _, b := range []string{"e1", "e2", "e3"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tips[b] {
			t.Errorf("%s moved to %s", b, got)
		}
	}
	var out bytes.Buffer
	if err := SyncUndo(ctx, endsTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	sameState(t, "after the undo", was, endsState(t, ctx, tmp))
}

// stepYAML declares a take-trunk lockfile and a per-layer step that folds
// gen.sh's output into the commit that owns gen/x.
const stepYAML = takeLockYAML + "defer:\n  - run: ./gen.sh\n    paths: [gen/**]\n    each: layer\n    fold: owner\n    commit: \"chore: regenerate\"\n"

// A step's fold leaves a pick of the middle layer empty, and a take-trunk
// drop ends the top layer: the first layer's end is the step's, the top
// layer's is the rule's, and every layer moves.
func TestStackOntoAStepsDropAndATakeTrunkDropEndEachLayerOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, _ := endsLineWith(t, stepYAML, map[string]string{"gen/x": "zero\n"}, map[string]string{"gen.sh": "#!/bin/sh\nmkdir -p gen\nprintf 'two\\n' > gen/x\n"},
		[]endsLayer{
			{"e1", []endsCommit{{"feat(e1): a", "gen/x", "one\n", ""}}},
			{"e2", []endsCommit{{"feat(e2): b", "gen/x", "two\n", ""}}},
			{"e3", []endsCommit{{"feat(e3): c", "c.txt", "c\n", ""}, {"chore(e3): reconcile the lockfile", "lock.txt", "lock e3\n", ""}}},
		}, nil)
	runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a",
		"e3":    "feat(e1): a | feat(e3): c",
		endsTmp: "feat(e1): a | feat(e3): c",
	}, map[string]string{"e3": wtsync.MappedTipDropped})
}

// A layer with surviving commits whose tip is a folded fixup is reported as
// folded, not as a tip trunk carries.
func TestStackOntoALayerEndingOnAFoldedFixupSaysItWasFolded(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, _ := endsLine(t, "conflicts: []\n", []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}, {"fixup! feat(e1): a", "a.txt", "fixed\n", ""}}},
	}, nil)
	human := runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a | feat(e2): b",
		endsTmp: "feat(e1): a | feat(e2): b",
	}, map[string]string{"e2": wtsync.MappedTipDropped})
	if !strings.Contains(human, "tip folded: the fixup went into an earlier commit; ends at ") || strings.Contains(human, "tip dropped: main carries it") {
		t.Errorf("e2's line does not say its fixup was folded:\n%s", human)
	}
}

// A step that commits its output at every layer's end, and a take-trunk drop
// of the middle layer's last pick: every layer still moves, each ending where
// the rule says.
func TestStackOntoAStepCommitEndsALayerThatLostItsLastPick(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	yaml := takeLockYAML + "defer:\n  - run: ./gen.sh\n    paths: [gen/**]\n    each: layer\n    commit: \"chore: regenerate\"\n"
	ctx, tmp, _ := endsLineWith(t, yaml, nil, map[string]string{"gen.sh": "#!/bin/sh\nmkdir -p gen\ntouch gen/$(git rev-list --count HEAD)\n"},
		[]endsLayer{
			{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
			{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}, {"chore(e2): reconcile the lockfile", "lock.txt", "lock e2\n", ""}}},
			{"e3", []endsCommit{{"feat(e3): c", "c.txt", "c\n", ""}}},
		}, nil)
	runEnds(t, ctx, tmp, map[string]string{
		"e1":    "feat(e1): a",
		"e2":    "feat(e1): a | feat(e2): b",
		"e3":    "feat(e1): a | feat(e2): b | feat(e3): c",
		endsTmp: "feat(e1): a | feat(e2): b | feat(e3): c",
	}, map[string]string{"e2": wtsync.MappedTipDropped})
}

// handedOverLine is a line whose lockfile commit conflicts with trunk and is
// nobody's to resolve: the run hands over at it, the worktree mid-rebase.
func handedOverLine(t *testing.T) (ctx *kit.Context, tmp string, tips map[string]string) {
	t.Helper()
	ctx, tmp, tips = endsLine(t, "conflicts: []\n", []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}, {"chore(e2): reconcile the lockfile", "lock.txt", "lock e2\n", ""}}},
		{"e3", []endsCommit{{"feat(e3): c", "c.txt", "c\n", ""}}},
	}, nil)
	if _, human, err := synccmdtest.StackRun(t, ctx, endsTmp, foldOntoTrunk()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want a handover at the lockfile\n%s", err, human)
	}
	return ctx, tmp, tips
}

// A stop resolved to trunk's side and resumed through wt: the commit that is
// left empty is a drop wt carried out, so the layer ends at its last commit
// that stays and every layer moves.
func TestResumeAfterAStopResolvedToTrunksSideReconcilesTheDrop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, _ := handedOverLine(t)
	main := ctx.Repo.MainRoot
	testkit.WriteFile(t, tmp, "lock.txt", "lock trunk\n")
	testkit.GitIn(t, tmp, "add", "lock.txt")
	var out bytes.Buffer
	if err := SyncResume(ctx, endsTmp, noResumeAgents(), &out); err != nil {
		t.Fatalf("resume: %v\n%s", err, out.String())
	}
	for b, want := range map[string]string{
		"e1": "feat(e1): a", "e2": "feat(e1): a | feat(e2): b", "e3": "feat(e1): a | feat(e2): b | feat(e3): c",
	} {
		if got := endsSubjects(t, main, b); got != want {
			t.Errorf("%s: commits\n got %s\nwant %s\n%s", b, got, want, out.String())
		}
	}
}

// A rebase a person advanced past the run's stop by hand (here a skip of the
// stopped commit, which then finished the rest) is not one wt drove: no
// drop is taken out of the plan, the check refuses, nothing moves, and the
// run says the replay was advanced by hand.
func TestResumeAfterTheStopWasSkippedByHandRefusesAndMovesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, tips := handedOverLine(t)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, tmp, "rebase", "--skip")
	var out bytes.Buffer
	err := SyncResume(ctx, endsTmp, noResumeAgents(), &out)
	if err == nil || !strings.Contains(out.String(), "no layer moved") || !strings.Contains(out.String(), "advanced by hand") {
		t.Fatalf("err %v, want a refusal that says the replay was advanced by hand\n%s", err, out.String())
	}
	for _, b := range []string{"e1", "e2", "e3"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tips[b] {
			t.Errorf("%s moved to %s", b, got)
		}
	}
}

// A stop resolved to trunk's side, but with the rebase's remaining list edited
// by hand first (here an exec added): the rest of the replay is not the one wt
// planned, so no drop is taken out of the plan, the check refuses, nothing
// moves, and the run says the remaining replay was edited by hand.
func TestResumeAfterTheRemainingListWasEditedRefusesAndMovesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, tmp, tips := handedOverLine(t)
	main := ctx.Repo.MainRoot
	testkit.WriteFile(t, tmp, "lock.txt", "lock trunk\n")
	testkit.GitIn(t, tmp, "add", "lock.txt")
	todo := testkit.GitOut(t, tmp, "rev-parse", "--git-path", "rebase-merge/git-rebase-todo")
	if !filepath.IsAbs(todo) {
		todo = filepath.Join(tmp, todo)
	}
	f, err := os.OpenFile(todo, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("exec true\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = SyncResume(ctx, endsTmp, noResumeAgents(), &out)
	if err == nil || !strings.Contains(out.String(), "no layer moved") || !strings.Contains(out.String(), "remaining replay was edited by hand") {
		t.Fatalf("err %v, want a refusal that says the remaining replay was edited by hand\n%s", err, out.String())
	}
	for _, b := range []string{"e1", "e2", "e3"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tips[b] {
			t.Errorf("%s moved to %s", b, got)
		}
	}
}
