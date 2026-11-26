package synccmd_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

const newBase = "newbase"

// baseRefuse is door/refuse.go as the base has it, with line n replaced.
func baseRefuse(n int) string {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		if i == n {
			fmt.Fprintf(&b, "line %d, from the new base\n", i)
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	return b.String()
}

// ontoMove is a targets fold that moves the line onto newbase.
func ontoMove() synccmd.RunOptions {
	opts := targetsFold()
	opts.Onto = newBase
	return opts
}

// replayedTree is the tree plain git gives the line replayed onto newbase,
// with the empty move! commit changing nothing: what a run with the move must
// end with.
func replayedTree(t *testing.T, main string, s *gittest.MoveLine) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "oracle")
	testkit.GitIn(t, main, "worktree", "add", "-q", "--detach", dir, gittest.MoveTmp)
	testkit.GitIn(t, dir, "rebase", "-q", "--onto", newBase, s.Base)
	tree := testkit.GitOut(t, dir, "rev-parse", "HEAD^{tree}")
	testkit.GitIn(t, main, "worktree", "remove", "--force", dir)
	return tree
}

// The part of the source moves into the target while the line is replayed onto
// a base that changed the same file elsewhere: the target gets the part, the
// source no longer changes it, the tip is the tree of the same replay without
// the move, checks run at both ends, the look said so first, and undo puts
// every branch and the worktree back.
func TestMoveOntoANewBaseDoesWhatTheLookSaidAndUndoPutsItBack(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, tc := range []struct {
		name     string
		shape    gittest.MoveShape
		baseLine int
	}{
		{"a whole path", gittest.MoveShape{Move: true}, 9},
		{"line ranges", gittest.MoveShape{Move: true, TwoHunks: true, Lines: []string{gittest.MovePath + ":2-2"}}, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, s, logPath := synccmdtest.MoveLineShaped(t, tc.shape, "", false)
			main := ctx.Repo.MainRoot
			onto := synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{gittest.MovePath: baseRefuse(tc.baseLine)})
			want := replayedTree(t, main, s)
			testkit.WriteFile(t, s.Path, "untracked.txt", "mine\n")
			before := refsOf(t, main, moveBranches(s))

			look := &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}
			var seen bytes.Buffer
			if err := synccmd.SyncWorktree(ctx, gittest.MoveTmp, synccmd.SyncOptions{NoFetch: true, Stack: look, Onto: newBase}, &seen); err != nil {
				t.Fatalf("look: %v\n%s", err, seen.String())
			}
			for _, text := range []string{
				"1 move folds as the line is replayed onto " + newBase + " " + onto[:7],
				"moves ",
				"wt sync rebase " + gittest.MoveTmp + " --stack --autosquash --onto " + newBase + " --each-commit=targets",
			} {
				if !strings.Contains(seen.String(), text) {
					t.Errorf("the look does not say %q:\n%s", text, seen.String())
				}
			}
			o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: look, Onto: newBase, Work: gittest.MoveTmp})
			f := o.Fold
			if f == nil || f.Refusal != nil || len(f.Folds) != 1 || f.Folds[0].Kind != "move" || f.Folds[0].TargetLayer != "l1" {
				t.Fatalf("look's fold %+v", f)
			}
			if strings.Join(f.Repointed, " ") != "l1 l2 l3" || len(f.Unchanged) != 0 {
				t.Errorf("repointed %q unchanged %q", f.Repointed, f.Unchanged)
			}

			res, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, ontoMove())
			if err != nil {
				t.Fatalf("run: %v\n%s", err, human)
			}
			if got := testkit.GitOut(t, main, "rev-parse", gittest.MoveTmp+"^{tree}"); got != want {
				t.Errorf("the tip's tree is %s, want the replay without the move %s\n%s", got, want, human)
			}
			for _, rev := range []string{"l1", "l2"} {
				got := testkit.GitOut(t, main, "show", rev+":"+gittest.MovePath)
				if !strings.Contains(got, "line 2, refused at the door") || !strings.Contains(got, "from the new base") {
					t.Errorf("%s lacks the part or the base's change:\n%s", rev, got)
				}
			}
			if tc.shape.TwoHunks {
				if got := testkit.GitOut(t, main, "show", "l1:"+gittest.MovePath); strings.Contains(got, "refused at the door too") {
					t.Errorf("the target took the hunk that stays with the source:\n%s", got)
				}
				diff := testkit.GitOut(t, main, "diff-tree", "--no-commit-id", "-p", "-r", "l3", "--", gittest.MovePath)
				if !strings.Contains(diff, "+line 10, refused") || strings.Contains(diff, "+line 2, refused") {
					t.Errorf("the source now changes\n%s\nwant line 10 alone", diff)
				}
			} else if got := changedIn(t, main, "l3"); !slices.Equal(got, []string{"feature.txt"}) {
				t.Errorf("the source now changes %q, want only feature.txt", got)
			}
			if got := testkit.GitOut(t, main, "merge-base", "l1", newBase); got != onto {
				t.Errorf("l1 is not on %s", newBase)
			}
			if subjects := testkit.GitOut(t, main, "log", "--format=%s", newBase+".."+gittest.MoveTmp); strings.Contains(subjects, "move!") {
				t.Errorf("the line still has a move! commit:\n%s", subjects)
			}
			checked := synccmdtest.CheckedSubjects(t, logPath)
			for _, w := range []string{gittest.MoveTarget, gittest.MoveSource} {
				if !slices.Contains(checked, w) {
					t.Errorf("no check ran at %q: %q", w, checked)
				}
			}
			p := synccmdtest.Participant(t, res, gittest.MoveTmp)
			if p.Fold == nil || p.Fold.InPlace || len(p.Fold.Moves) != 1 || p.Fold.Guard == nil || p.Fold.Guard.Outcome != "passed" {
				t.Fatalf("json fold %+v", p.Fold)
			}
			if !strings.Contains(human, "replayed onto "+onto[:7]+" without the move") {
				t.Errorf("the guard's line does not say what it compared:\n%s", human)
			}
			if kept := keptMoves(t, main); len(kept) != 1 || kept[0] != p.Fold.Moves[0].Kept {
				t.Errorf("kept %q", kept)
			}
			undoRestores(t, ctx, s, before)
		})
	}
}

// undoRestores runs wt sync undo and holds every branch, the worktree and its
// untracked file to before.
func undoRestores(t *testing.T, ctx *kit.Context, s *gittest.MoveLine, before map[string]string) {
	t.Helper()
	main := ctx.Repo.MainRoot
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, gittest.MoveTmp, syncfix.UndoOptions{VerbOptions: ontoMove().VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("undo left %v, want %v\n%s", got, before, undone.String())
	}
	if got := testkit.GitOut(t, s.Path, "rev-parse", "HEAD"); got != before[gittest.MoveTmp] {
		t.Errorf("undo left the worktree at %s", got)
	}
	if got := testkit.GitOut(t, s.Path, "status", "--porcelain"); got != "?? untracked.txt" {
		t.Errorf("status after undo %q, want only the untracked file", got)
	}
}

// cherryOnto commits rev on newbase, as if the base carried it.
func cherryOnto(t *testing.T, main, rev string) {
	t.Helper()
	testkit.GitIn(t, main, "checkout", "-q", newBase)
	testkit.GitIn(t, main, "cherry-pick", "--allow-empty", rev)
	testkit.GitIn(t, main, "checkout", "-q", "main")
}

// Everything the new base makes impossible is refused before anything moves,
// naming the base or the commit it carries, in the words the look gives.
func TestMoveOntoRefusesWhatTheNewBaseMakesImpossible(t *testing.T) {
	t.Parallel()
	skipShort(t)
	rebaseFirst := "rebase the line onto {base} without the move! commit first"
	for _, tc := range []struct {
		name  string
		shape gittest.MoveShape
		base  func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine)
		// want is what the refusal says; "{base}" is the new base's short id,
		// "{l1}" and "{l3}" the layers'.
		want []string
	}{
		{"a conflict with the source", gittest.MoveShape{Move: true}, func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{gittest.MovePath: baseRefuse(2)})
		}, []string{"{base}", "{l3}", "conflicts"}},
		{"a conflict elsewhere on the line", gittest.MoveShape{Move: true}, func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{"other.txt": "theirs\n"})
		}, []string{"{base}", gittest.MoveBetween, "conflicts"}},
		{"the part's context changed", gittest.MoveShape{Move: true}, func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{gittest.MovePath: baseRefuse(4)})
		}, []string{"{base}", "does not apply at the target", rebaseFirst}},
		{"the target's patch is on the base", gittest.MoveShape{Move: true}, func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{"new.txt": "new\n"})
			cherryOnto(t, ctx.Repo.MainRoot, "l1")
		}, []string{"{l1}", gittest.MoveTarget, "already carries", rebaseFirst}},
		{"the target is reachable from the base", gittest.MoveShape{Move: true}, func(t *testing.T, ctx *kit.Context, _ *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, "l1", map[string]string{"new.txt": "new\n"})
		}, []string{gittest.MoveTarget, "already carries", rebaseFirst}},
		{"the target's content is on the base", gittest.MoveShape{Move: true}, func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{"door/door.go": "package door\n", "new.txt": "new\n"})
		}, []string{"{l1}", gittest.MoveTarget, "already carries", rebaseFirst}},
		{"the source's patch is on the base", gittest.MoveShape{Move: true}, func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{"new.txt": "new\n"})
			cherryOnto(t, ctx.Repo.MainRoot, "l3")
		}, []string{"{l3}", gittest.MoveSource, "already carries", rebaseFirst}},
		{"a lifting rule's file", gittest.MoveShape{Move: true, LiftAtSource: true}, func(t *testing.T, ctx *kit.Context, s *gittest.MoveLine) {
			synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{"v.txt": "1.0.0\nnotes\nnotes\nnotes\nnotes\nnotes\nnotes\nmore\n"})
		}, []string{"{base}", "v.txt", "lifting rule"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, s, logPath := synccmdtest.MoveLineShaped(t, tc.shape, "", false)
			main := ctx.Repo.MainRoot
			short := func(rev string) string { return testkit.GitOut(t, main, "rev-parse", "--short=7", rev) }
			tc.base(t, ctx, s)
			fill := strings.NewReplacer("{base}", short(newBase), "{l1}", short("l1"), "{l3}", short("l3"))
			before := refsOf(t, main, moveBranches(s))

			look := &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}
			o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: look, Onto: newBase, Work: gittest.MoveTmp})
			if o.Fold == nil || o.Fold.Refusal == nil || len(o.Fold.Folds) != 0 {
				t.Fatalf("the look's fold %+v, want a refusal", o.Fold)
			}
			for _, w := range tc.want {
				if w = fill.Replace(w); !strings.Contains(*o.Fold.Refusal, w) {
					t.Errorf("the refusal does not say %q: %s", w, *o.Fold.Refusal)
				}
			}
			var seen bytes.Buffer
			if err := synccmd.SyncWorktree(ctx, gittest.MoveTmp, synccmd.SyncOptions{NoFetch: true, Stack: look, Onto: newBase}, &seen); err != nil {
				t.Fatalf("look: %v\n%s", err, seen.String())
			}
			if !strings.Contains(seen.String(), "refused: the fold:") {
				t.Errorf("the look's run line does not refuse:\n%s", seen.String())
			}

			_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, ontoMove())
			if err == nil || !strings.Contains(err.Error()+human, *o.Fold.Refusal) {
				t.Fatalf("the run refuses with %v, not the look's %q\n%s", err, *o.Fold.Refusal, human)
			}
			if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
				t.Errorf("a refused move moved %v, want %v", got, before)
			}
			if got := testkit.GitOut(t, s.Path, "rev-parse", "HEAD"); got != before[gittest.MoveTmp] {
				t.Errorf("the worktree is at %s", got)
			}
			if st := testkit.GitOut(t, s.Path, "status", "--porcelain"); st != "" {
				t.Errorf("the worktree is not clean: %s", st)
			}
			if refs := testkit.GitOut(t, main, "for-each-ref", "refs/wt-sync/", "refs/wt-sync-kept/"); refs != "" {
				t.Errorf("a refused move wrote refs:\n%s", refs)
			}
			if got := synccmdtest.CheckedSubjects(t, logPath); len(got) != 0 {
				t.Errorf("a refused move ran checks at %q", got)
			}
		})
	}
}

// A lifted file both the new base and the line change refuses the move even
// when its name holds a newline.
func TestMoveOntoRefusesALiftedFileWithANewlineInItsName(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const name = "ver\nsion.txt"
	var s *gittest.MoveLine
	ctx, main := synccmdtest.OpenLine(t, func(main, wtDir string) {
		yaml := "conflicts:\n  - paths: ['ver*']\n    strategy: owned-line\n    line: '^\\d'\n    rule: max-plus-patch\n"
		s = gittest.NewMoveLine(t, main, wtDir, yaml, false, false)
	})
	testkit.WriteFile(t, s.Path, name, "1.0.0\n")
	testkit.WriteFile(t, s.Path, "line.txt", "line\n")
	testkit.GitIn(t, s.Path, "add", "-A")
	testkit.GitIn(t, s.Path, "commit", "-q", "--no-verify", "-m", "feat: version")
	testkit.GitIn(t, s.Path, "commit", "-q", "--no-verify", "--allow-empty", "-m", "move! "+gittest.MoveTarget,
		"-m", gittest.MoveBody(gittest.MoveSource, gittest.MovePath))
	synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{name: "1.0.0\n"})
	before := refsOf(t, main, moveBranches(s))
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, ontoMove())
	if err == nil || !strings.Contains(err.Error()+human, "lifting rule") {
		t.Fatalf("err %v, want the lifting rule's refusal\n%s", err, human)
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("a refused move moved %v, want %v", got, before)
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", "refs/wt-sync/", "refs/wt-sync-kept/"); refs != "" {
		t.Errorf("a refused move wrote refs:\n%s", refs)
	}
}

// Under --onto a move still needs the checks at its target and source.
func TestMoveOntoRefusesARunWithoutTargetsChecks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.MoveLine(t, false, true)
	main := ctx.Repo.MainRoot
	synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{"new.txt": "new\n"})
	before := refsOf(t, main, moveBranches(s))
	opts := ontoMove()
	opts.EachCommit = ""
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, opts)
	if err == nil || !strings.Contains(err.Error()+human, "add --each-commit=targets") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if got := refsOf(t, main, moveBranches(s)); !mapsEqual(got, before) {
		t.Errorf("a refused move moved %v, want %v", got, before)
	}
}

// A check that fails at the target is amended and resumed: the tip is the
// replay without the move with that fix on it, and the content check passes.
func TestMoveOntoTakesAFixAmendedAtACheckStop(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.MoveLineShaped(t, gittest.MoveShape{Move: true}, "", true)
	main := ctx.Repo.MainRoot
	synccmdtest.Branch(t, ctx, newBase, s.Base, map[string]string{"new.txt": "new\n"})
	want := replayedTree(t, main, s)
	testkit.WriteFile(t, s.Path, "untracked.txt", "mine\n")
	before := refsOf(t, main, moveBranches(s))
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, ontoMove())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want the check's stop\n%s", err, human)
	}
	testkit.WriteFile(t, s.Path, "fixed.txt", "fixed\n")
	testkit.GitIn(t, s.Path, "add", "fixed.txt")
	testkit.GitIn(t, s.Path, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	var resumed bytes.Buffer
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) }}}
	ropts.Push = syncrun.PushNever
	if err := syncfix.SyncResume(ctx, gittest.MoveTmp, ropts, &resumed); err != nil {
		t.Fatalf("resume: %v\n%s\n%s", err, human, resumed.String())
	}
	extra := testkit.GitOut(t, main, "diff-tree", "-r", "--name-only", want, gittest.MoveTmp+"^{tree}")
	if extra != "fixed.txt" {
		t.Errorf("the tip differs from the replay without the move in %q, want only the fix\n%s", extra, resumed.String())
	}
	if !strings.Contains(resumed.String(), "without the move") {
		t.Errorf("the resume does not say the guard passed:\n%s", resumed.String())
	}
	undoRestores(t, ctx, s, before)
}
