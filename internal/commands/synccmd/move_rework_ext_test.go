package synccmd_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// moveBefore is what a MoveLine is before a run: every branch, the tip's
// tree, and what the tip has of the moved path.
type moveBefore struct {
	refs          map[string]string
	tree, tipBlob string
}

func readMoveBefore(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) moveBefore {
	t.Helper()
	main := ctx.Repo.MainRoot
	return moveBefore{refs: refsOf(t, main, moveBranches(s)),
		tree:    testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^{tree}"),
		tipBlob: testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+":"+gittest.MovePath)}
}

// movedAndUndone asserts a finished move: layer 1 has want of the moved
// path, the source's commit changes only sourceChanges, the tip's tree is the
// old one, no move! commit is left, and every check in checked ran. Then undo
// puts every branch and the worktree back.
func movedAndUndone(t *testing.T, ctx *kit.Context, s *gittest.MoveLine, b moveBefore, logPath, human, want string, sourceChanges, checked []string) {
	t.Helper()
	main := ctx.Repo.MainRoot
	if got := testkit.GitOut(t, main, "rev-parse", "l1:"+gittest.MovePath); got != want {
		t.Errorf("layer 1 has %s of %s, want %s\n%s", got, gittest.MovePath, want, human)
	}
	src := testkit.GitOut(t, main, "rev-list", "-1", "--fixed-strings", "--grep="+gittest.MoveSource, "l3")
	if got := changedIn(t, main, src); !slices.Equal(got, sourceChanges) {
		t.Errorf("the source now changes %q, want %q", got, sourceChanges)
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^{tree}"); got != b.tree {
		t.Errorf("the tip's tree is %s, want the old tip's %s", got, b.tree)
	}
	if subjects := testkit.GitOut(t, main, "log", "--format=%s", s.Base+".."+gittest.MoveTmp); strings.Contains(subjects, "move!") || strings.Contains(subjects, "fixup!") {
		t.Errorf("the line still has a fold commit:\n%s", subjects)
	}
	ran := synccmdtest.CheckedSubjects(t, logPath)
	for _, c := range checked {
		if !slices.Contains(ran, c) {
			t.Errorf("no check ran at %q: checked %q\n%s", c, ran, human)
		}
	}
	if st := testkit.GitOut(t, s.Path, "status", "--porcelain"); st != "" {
		t.Errorf("the worktree is not clean after the run: %s", st)
	}
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.MoveTmp, syncfix.UndoOptions{VerbOptions: targetsFold().VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, b.refs) {
		t.Errorf("undo left %v, want %v\n%s", got, b.refs, undone.String())
	}
	if st := testkit.GitOut(t, s.Path, "status", "--porcelain"); st != "" {
		t.Errorf("the worktree is not clean after undo: %s", st)
	}
}

// A resumed move keeps its target's check: the check, filtered to the moved
// path, fails once at a fold target below the move's and the run hands over;
// the resume still checks the move's target, which only the part makes
// call for the check.
func TestMoveResumedKeepsTheTargetsCheck(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, gittest.MoveShape{Move: true, Below: true}, "["+gittest.MovePath+"]", true)
	b := readMoveBefore(t, ctx, s)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want the check's stop\n%s", err, human)
	}
	var resumed bytes.Buffer
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) }}}
	ropts.Push = syncrun.PushNever
	if err := syncfix.SyncResume(ctx, gittest.MoveTmp, ropts, &resumed); err != nil {
		t.Fatalf("resume: %v\n%s\n%s", err, human, resumed.String())
	}
	movedAndUndone(t, ctx, s, b, logPath, human+resumed.String(), b.tipBlob, []string{"feature.txt"}, []string{gittest.MoveBelow, gittest.MoveTarget, gittest.MoveSource})
}

// A source that is not its layer's tip is checked at itself, not only at
// the tip above it.
func TestMoveChecksASourceThatIsNotItsLayersTip(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, gittest.MoveShape{Move: true, Interior: true}, "", false)
	b := readMoveBefore(t, ctx, s)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	movedAndUndone(t, ctx, s, b, logPath, human, b.tipBlob, []string{"feature.txt"}, []string{gittest.MoveTarget, gittest.MoveSource, gittest.MoveAfter})
}

// The part needs context a fixup of the target supplies: it applies after
// the target's own fixups, where the fold puts it, and the run goes ahead.
func TestMoveAppliesThePartAfterTheTargetsFixups(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, gittest.MoveShape{Move: true, TargetFixup: true}, "", false)
	b := readMoveBefore(t, ctx, s)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	movedAndUndone(t, ctx, s, b, logPath, human, b.tipBlob, []string{"feature.txt"}, []string{gittest.MoveTarget, gittest.MoveSource})
}

// A fixup of the source changes a line of the moved path far from the part:
// the source's replay absorbs the part before its fixup is folded, and the
// run goes ahead. Layer 1 has the part without the fixup's line, which stays
// the source's.
func TestMoveTakesAFixupOfTheSourceOnTheMovedPath(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, gittest.MoveShape{Move: true, SourceFixup: true}, "", false)
	main := ctx.Repo.MainRoot
	b := readMoveBefore(t, ctx, s)
	part := testkit.GitOut(t, main, "rev-parse", "l3:"+gittest.MovePath)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	movedAndUndone(t, ctx, s, b, logPath, human, part, []string{gittest.MovePath, "feature.txt"}, []string{gittest.MoveTarget, gittest.MoveSource})
}

// checkedBefore asserts that the checks ran at first, and later at then.
func checkedBefore(t *testing.T, logPath, first, then, human string) {
	t.Helper()
	ran := synccmdtest.CheckedSubjects(t, logPath)
	i, j := slices.Index(ran, first), slices.Index(ran, then)
	if i < 0 || j < 0 || i > j {
		t.Errorf("checked %q, want %q checked before %q\n%s", ran, first, then, human)
	}
}

// A source that is not its layer's tip, with the check filtered to the
// moved path, which the source no longer changes once the part is below
// it: the check it called for still runs at the source, before the commit
// above it, and at the target and the layer's tip.
func TestMoveChecksAnInteriorSourceForTheMovedPath(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, gittest.MoveShape{Move: true, Interior: true}, "["+gittest.MovePath+"]", false)
	b := readMoveBefore(t, ctx, s)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	checkedBefore(t, logPath, gittest.MoveSource, gittest.MoveAfter, human)
	movedAndUndone(t, ctx, s, b, logPath, human, b.tipBlob, []string{"feature.txt"}, []string{gittest.MoveTarget, gittest.MoveSource, gittest.MoveAfter})
}

// The run hands over at an interior source's own stop, where its version
// lift refuses; the resume still checks the source there, before the
// commit above it in its layer.
func TestMoveResumedAtTheSourcesStopStillChecksIt(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLineShaped(t, gittest.MoveShape{Move: true, Interior: true, LiftAtSource: true}, "", false)
	b := readMoveBefore(t, ctx, s)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error(), "needs you") || !strings.Contains(human, "v.txt") {
		t.Fatalf("err %v, want a handover at the source's lift\n%s", err, human)
	}
	if ran := synccmdtest.CheckedSubjects(t, logPath); slices.Contains(ran, gittest.MoveSource) {
		t.Fatalf("checked %q before the handover, want the source not yet checked", ran)
	}
	testkit.GitIn(t, s.Path, "add", "v.txt")
	var resumed bytes.Buffer
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) }}}
	ropts.Push = syncrun.PushNever
	if err := syncfix.SyncResume(ctx, gittest.MoveTmp, ropts, &resumed); err != nil {
		t.Fatalf("resume: %v\n%s\n%s", err, human, resumed.String())
	}
	human += resumed.String()
	checkedBefore(t, logPath, gittest.MoveSource, gittest.MoveAfter, human)
	movedAndUndone(t, ctx, s, b, logPath, human, b.tipBlob, []string{"feature.txt", "v.txt"}, []string{gittest.MoveTarget, gittest.MoveSource, gittest.MoveAfter})
}
