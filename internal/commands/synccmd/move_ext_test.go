package synccmd_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// moveBranches is every layer of a MoveLine and its top.
func moveBranches(s *gittest.MoveLine) []string {
	return append(append([]string{}, s.Layers...), gittest.MoveTmp)
}

// keptMoves is every ref a move kept its part under, for tmp/move.
func keptMoves(t *testing.T, main string) []string {
	t.Helper()
	out := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname)", "refs/wt-sync-kept/"+gittest.MoveTmp+"/")
	var refs []string
	for _, r := range strings.Fields(out) {
		if strings.Contains(r, "-move-") {
			refs = append(refs, r)
		}
	}
	return refs
}

// changedIn is the paths commit rev changes against its parent.
func changedIn(t *testing.T, dir, rev string) []string {
	t.Helper()
	return strings.Fields(testkit.GitOut(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", rev))
}

// The issue's case: part of the feature commit in layer 3 belongs to the
// commit in layer 1. A move! commit at the top names it, and an in-place fold
// under --each-commit=targets moves it: layer 1's commit now carries the
// part, the feature commit no longer changes it, the tip's tree is the one
// the line had, the move! commit is gone, and the check ran at the target
// and at the source. The part is kept under its own ref, and the run's JSON
// names the move. Undo then puts every branch back byte for byte.
func TestMoveFoldsPartOfACommitIntoAnEarlierOneAndUndoPutsItBack(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLine(t, false, true)
	main := ctx.Repo.MainRoot
	before := refsOf(t, main, moveBranches(s))
	oldTree := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^{tree}")
	sourcePart := testkit.GitOut(t, main, "rev-parse", "l3:"+gittest.MovePath)

	opts := targetsFold()
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "l1:"+gittest.MovePath); got != sourcePart {
		t.Errorf("layer 1 has %s of %s, want the source's %s\n%s", got, gittest.MovePath, sourcePart, human)
	}
	if got := testkit.GitOut(t, main, "log", "-1", "--format=%s", "l1"); got != gittest.MoveTarget {
		t.Errorf("layer 1's tip is %q, want the target", got)
	}
	if got := changedIn(t, main, "l3"); !slices.Equal(got, []string{"feature.txt"}) {
		t.Errorf("the source now changes %q, want only feature.txt", got)
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^{tree}"); got != oldTree {
		t.Errorf("the tip's tree is %s, want the old tip's %s", got, oldTree)
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp); got != testkit.GitOut(t, main, "rev-parse", "l3") {
		t.Errorf("the move! commit is still on the line: tmp/move %s, l3 %s", got, testkit.GitOut(t, main, "rev-parse", "l3"))
	}
	if subjects := testkit.GitOut(t, main, "log", "--format=%s", s.Base+".."+gittest.MoveTmp); strings.Contains(subjects, "move!") {
		t.Errorf("the line still has a move! commit:\n%s", subjects)
	}
	checked := synccmdtest.CheckedSubjects(t, logPath)
	for _, want := range []string{gittest.MoveTarget, gittest.MoveSource} {
		if !slices.Contains(checked, want) {
			t.Errorf("no check ran at %q: checked %q\n%s", want, checked, human)
		}
	}
	if !strings.Contains(human, "moves "+gittest.MovePath+" of ") {
		t.Errorf("the run does not name the move:\n%s", human)
	}
	kept := keptMoves(t, main)
	if len(kept) != 1 {
		t.Fatalf("kept refs %q, want one", kept)
	}
	p := synccmdtest.Participant(t, res, gittest.MoveTmp)
	if p.Fold == nil || len(p.Fold.Moves) != 1 {
		t.Fatalf("json fold %+v, want one move", p.Fold)
	}
	m := p.Fold.Moves[0]
	if m.TargetSubject != gittest.MoveTarget || m.SourceSubject != gittest.MoveSource || !slices.Equal(m.Paths, []string{gittest.MovePath}) || m.Kept != kept[0] {
		t.Errorf("json move %+v, want %s from %s into %s kept under %s", m, gittest.MovePath, gittest.MoveSource, gittest.MoveTarget, kept[0])
	}
	if st := testkit.GitOut(t, s.Path, "status", "--porcelain"); st != "" {
		t.Errorf("the worktree is not clean after the run: %s", st)
	}

	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.MoveTmp, syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("undo left %v, want %v\n%s", got, before, undone.String())
	}
	if got := testkit.GitOut(t, s.Path, "rev-parse", "HEAD"); got != before[gittest.MoveTmp] {
		t.Errorf("undo left the worktree at %s, want %s", got, before[gittest.MoveTmp])
	}
	if st := testkit.GitOut(t, s.Path, "status", "--porcelain"); st != "" {
		t.Errorf("the worktree is not clean after undo: %s", st)
	}
}

// The commit between target and source changes the part's context: the part
// would not apply at the target, so the run refuses before anything moves,
// naming that commit. Every branch, the worktree and the refs stay as they
// were, and no part is kept.
func TestMoveRefusesWhenACommitBetweenCarriesThePartsContext(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := synccmdtest.MoveLine(t, true, true)
	main := ctx.Repo.MainRoot
	before := refsOf(t, main, moveBranches(s))
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	between := testkit.GitOut(t, main, "rev-parse", "--short=7", "l2")
	if err == nil || !strings.Contains(err.Error()+human, between) || !strings.Contains(err.Error()+human, gittest.MoveBetween) {
		t.Fatalf("err %v, want a refusal naming %s %q\n%s", err, between, gittest.MoveBetween, human)
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("a refused move moved %v, want %v", got, before)
	}
	if got := testkit.GitOut(t, s.Path, "rev-parse", "HEAD"); got != before[gittest.MoveTmp] {
		t.Errorf("the worktree is at %s, want %s", got, before[gittest.MoveTmp])
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

// A run with a move! commit is refused unless the checks run at the target
// and the source (--each-commit=targets), and with --onto; nothing moves.
func TestMoveRefusesARunWithoutTargetsChecks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.MoveLine(t, false, true)
	main := ctx.Repo.MainRoot
	before := refsOf(t, main, moveBranches(s))
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, opts)
	if err == nil || !strings.Contains(err.Error()+human, "--each-commit=targets") {
		t.Fatalf("err %v, want a refusal naming --each-commit=targets\n%s", err, human)
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("a refused move moved %v, want %v", got, before)
	}
}

// wt sync move writes the move! commit on the line and nothing else: an empty
// commit at the top whose subject names the target and whose body names the
// source and the path, by subject even when they were given by id, since a
// fold below rewrites ids. The worktree's files and index are untouched.
func TestSyncMoveWritesTheMoveCommit(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.MoveLine(t, false, false)
	main := ctx.Repo.MainRoot
	old := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp)
	testkit.WriteFile(t, s.Path, "untracked.txt", "mine\n")
	var out bytes.Buffer
	err := synccmd.SyncMove(ctx, gittest.MoveTmp, synccmd.MoveOptions{From: testkit.GitOut(t, main, "rev-parse", "--short", "l3"), Into: gittest.MoveTarget, Paths: []string{gittest.MovePath}}, &out)
	if err != nil {
		t.Fatalf("move: %v\n%s", err, out.String())
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^"); got != old {
		t.Errorf("the move! commit's parent is %s, want the old tip %s", got, old)
	}
	if got := testkit.GitOut(t, main, "log", "-1", "--format=%B", gittest.MoveTmp); strings.TrimSpace(got) != "move! "+gittest.MoveTarget+"\n\n"+strings.TrimSpace(gittest.MoveBody(gittest.MoveSource, gittest.MovePath)) {
		t.Errorf("the move! commit says\n%s", got)
	}
	if got := changedIn(t, main, gittest.MoveTmp); len(got) != 0 {
		t.Errorf("the move! commit changes %q, want nothing", got)
	}
	if got := testkit.GitOut(t, s.Path, "status", "--porcelain"); got != "?? untracked.txt" {
		t.Errorf("the worktree's status is %q, want only the untracked file", got)
	}
	if !strings.Contains(out.String(), "wt sync rebase "+gittest.MoveTmp+" --stack --autosquash --each-commit=targets") {
		t.Errorf("the verb does not say how to run the move:\n%s", out.String())
	}
}

// A source named by a subject two commits of the line share is refused,
// naming both, and nothing is written.
func TestSyncMoveRefusesASubjectThatNamesTwoCommits(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.MoveLine(t, false, false)
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, s.Path, "commit", "-q", "--no-verify", "--allow-empty", "-m", gittest.MoveSource)
	old := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp)
	var out bytes.Buffer
	err := synccmd.SyncMove(ctx, gittest.MoveTmp, synccmd.MoveOptions{From: gittest.MoveSource, Into: gittest.MoveTarget, Paths: []string{gittest.MovePath}}, &out)
	if err == nil || !strings.Contains(err.Error(), testkit.GitOut(t, main, "rev-parse", "--short=7", "l3")) || !strings.Contains(err.Error(), testkit.GitOut(t, main, "rev-parse", "--short=7", old)) {
		t.Fatalf("err %v, want a refusal naming both commits", err)
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp); got != old {
		t.Errorf("a refused move wrote %s", got)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
