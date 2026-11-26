package synccmd_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

const moveLines = gittest.MovePath + ":2-2"

func linesShape() gittest.MoveShape {
	return gittest.MoveShape{TwoHunks: true, Move: true, Lines: []string{moveLines}}
}

// jsonOf is v as --json writes it.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// One hunk of a file the source changes twice folds into the target; the
// source keeps the other hunk and its other file. The look predicts the
// layers and the move, with its range, that the run then makes and reports;
// the tip's tree is the old one, and undo puts every branch back.
func TestMoveLinesFoldsOneHunkAndUndoPutsItBack(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, linesShape(), "", false)
	main := ctx.Repo.MainRoot
	testkit.WriteFile(t, s.Path, "untracked.txt", "mine\n")
	before := refsOf(t, main, moveBranches(s))
	oldTree := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^{tree}")
	look := &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}
	var seen bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, gittest.MoveTmp, synccmd.SyncOptions{NoFetch: true, Stack: look}, &seen); err != nil {
		t.Fatalf("look: %v\n%s", err, seen.String())
	}
	if !strings.Contains(seen.String(), "moves "+moveLines+" of ") {
		t.Errorf("the look does not name the range:\n%s", seen.String())
	}
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: look})
	if o.Fold == nil || o.Fold.Refusal != nil || len(o.Fold.Folds) != 1 {
		t.Fatalf("fold %+v", o.Fold)
	}
	wantLines := `"lines":[{"path":"door/refuse.go","from":2,"to":2}]`
	if got := jsonOf(t, o.Fold.Folds[0].Move); !strings.Contains(got, wantLines) || !strings.Contains(got, `"paths":[]`) {
		t.Errorf("the look's move is %s", got)
	}

	opts := targetsFold()
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	l1 := testkit.GitOut(t, main, "cat-file", "-p", "l1:"+gittest.MovePath)
	if !strings.Contains(l1, "line 2, refused") || strings.Contains(l1, "line 10, refused") {
		t.Errorf("layer 1 has\n%s\nwant line 2 moved and line 10 not", l1)
	}
	sourceDiff := testkit.GitOut(t, main, "diff-tree", "--no-commit-id", "-p", "-r", "l3", "--", gittest.MovePath)
	if !strings.Contains(sourceDiff, "+line 10, refused") || strings.Contains(sourceDiff, "+line 2, refused") {
		t.Errorf("the source now changes\n%s\nwant line 10 alone", sourceDiff)
	}
	if got := changedIn(t, main, "l3"); !slices.Equal(got, []string{gittest.MovePath, "feature.txt"}) {
		t.Errorf("the source now changes %q", got)
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^{tree}"); got != oldTree {
		t.Errorf("the tip's tree is %s, want the old tip's %s", got, oldTree)
	}
	if got := testkit.GitOut(t, s.Path, "status", "--porcelain"); got != "?? untracked.txt" {
		t.Errorf("the worktree's status is %q, want only the untracked file", got)
	}
	for _, want := range []string{gittest.MoveTarget, gittest.MoveSource} {
		if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Contains(got, want) {
			t.Errorf("no check ran at %q: checked %q", want, got)
		}
	}
	if !strings.Contains(human, "moves "+moveLines+" of ") {
		t.Errorf("the run does not name the range:\n%s", human)
	}
	p := synccmdtest.Participant(t, res, gittest.MoveTmp)
	if p.Fold == nil || len(p.Fold.Moves) != 1 || p.Fold.Moves[0].Kept == "" {
		t.Fatalf("json fold %+v, want one move with its kept ref", p.Fold)
	}
	if got := jsonOf(t, p.Fold.Moves[0]); !strings.Contains(got, wantLines) || !strings.Contains(got, `"paths":[]`) {
		t.Errorf("the run's move is %s, want the look's lines", got)
	}
	for _, l := range s.Layers {
		moved := testkit.GitOut(t, main, "rev-parse", l) != before[l]
		if slices.Contains(o.Fold.Repointed, l) != moved {
			t.Errorf("the look re-points %q, %s moved: %v", o.Fold.Repointed, l, moved)
		}
	}

	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.MoveTmp, syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("undo left %v, want %v\n%s", got, before, undone.String())
	}
	if got := testkit.GitOut(t, s.Path, "rev-parse", "HEAD"); got != before[gittest.MoveTmp] {
		t.Errorf("undo left the worktree at %s", got)
	}
	if got := testkit.GitOut(t, s.Path, "status", "--porcelain"); got != "?? untracked.txt" {
		t.Errorf("after undo the status is %q, want only the untracked file", got)
	}
}

// A range that splits a hunk refuses the run before anything moves, in the
// look's words: no ref, file or kept part changes.
func TestMoveLinesSplittingAHunkRefusesAndMovesNothing(t *testing.T) {
	t.Parallel()
	skipShort(t)
	shape := linesShape()
	shape.Lines = []string{gittest.MovePath + ":1-1"}
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, shape, "", false)
	main := ctx.Repo.MainRoot
	before := refsOf(t, main, moveBranches(s))
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}})
	if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "door/refuse.go:@@ -1,5 +1,5 @@") {
		t.Fatalf("fold %+v", o.Fold)
	}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error()+human, *o.Fold.Refusal) {
		t.Errorf("the run refuses with %v, not the look's %q\n%s", err, *o.Fold.Refusal, human)
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("a refused move moved %v, want %v", got, before)
	}
	if st := testkit.GitOut(t, s.Path, "status", "--porcelain"); st != "" {
		t.Errorf("the worktree is not clean: %s", st)
	}
	if kept := keptMoves(t, main); len(kept) != 0 {
		t.Errorf("a refused move kept %q", kept)
	}
	if got := synccmdtest.CheckedSubjects(t, logPath); len(got) != 0 {
		t.Errorf("a refused move ran checks at %q", got)
	}
}

// A run that hands over below the move resumes it from the kept part, ranges
// and all, and still checks the move's target.
func TestMoveLinesResumeKeepsTheMove(t *testing.T) {
	t.Parallel()
	skipShort(t)
	shape := gittest.MoveShape{Move: true, Lines: []string{moveLines}, Below: true}
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, shape, "['door/**']", true)
	b := readMoveBefore(t, ctx, s)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want a check stop\n%s", err, human)
	}
	var resumed bytes.Buffer
	if err := syncfix.SyncResume(ctx, gittest.MoveTmp, syncfix.ResumeOptions{VerbOptions: targetsFold().VerbOptions}, &resumed); err != nil {
		t.Fatalf("resume: %v\n%s", err, resumed.String())
	}
	l1 := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "l1:"+gittest.MovePath)
	if got := testkit.GitOut(t, ctx.Repo.MainRoot, "cat-file", "-p", l1); !strings.Contains(got, "line 2, refused") {
		t.Errorf("layer 1 has\n%s", got)
	}
	movedAndUndone(t, ctx, s, b, logPath, human+resumed.String(), l1,
		[]string{"feature.txt"}, []string{gittest.MoveTarget, gittest.MoveSource})
}
