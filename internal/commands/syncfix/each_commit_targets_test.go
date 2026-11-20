package syncfix

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

type targetsLine struct {
	ctx *kit.Context
	s   *gittest.StackLine
	// before is what every other worktree held, and the stash, before the run.
	before string
}

// workState is everything a person could lose in the worktrees a run is not
// to touch: the trunk checkout, s1's clean worktree, s3's dirty one and s5's
// busy one. Their status with every untracked file, the unstaged and staged
// diffs, the untracked files' bytes, and the stash with what it holds.
func workState(t *testing.T, l *targetsLine) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range []string{l.ctx.Repo.MainRoot, l.s.Paths["s1"], l.s.Paths["s3"], l.s.Paths["s5"]} {
		b.WriteString(dir + "\n")
		b.WriteString(testkit.GitOut(t, dir, "status", "--porcelain=v1", "-uall") + "\n")
		b.WriteString(testkit.GitOut(t, dir, "diff") + "\n")
		b.WriteString(testkit.GitOut(t, dir, "diff", "--cached") + "\n")
		for _, f := range strings.Fields(testkit.GitOut(t, dir, "ls-files", "--others", "--exclude-standard")) {
			b.WriteString(f + ": " + readFile(t, filepath.Join(dir, f)))
		}
	}
	b.WriteString(testkit.GitOut(t, l.ctx.Repo.MainRoot, "stash", "list") + "\n")
	b.WriteString(testkit.GitOut(t, l.ctx.Repo.MainRoot, "stash", "show", "-p", "--include-untracked", "stash@{0}"))
	return b.String()
}

// seedWork leaves a stash, and in each other worktree a staged file, an
// unstaged edit of a tracked file and an untracked file, for workState.
func seedWork(t *testing.T, l *targetsLine) {
	t.Helper()
	main := l.ctx.Repo.MainRoot
	testkit.WriteFile(t, main, "stashed.txt", "in the stash\n")
	testkit.GitIn(t, main, "stash", "push", "-q", "-u", "-m", "kept")
	for _, dir := range []string{main, l.s.Paths["s1"], l.s.Paths["s3"], l.s.Paths["s5"]} {
		testkit.WriteFile(t, dir, "staged-by-person.txt", "staged\n")
		testkit.GitIn(t, dir, "add", "staged-by-person.txt")
		testkit.WriteFile(t, dir, "untracked-by-person.txt", "untracked\n")
		tracked := testkit.GitOut(t, dir, "ls-files", "a1.txt", "t.txt", "e1.txt")
		if tracked == "" {
			t.Fatalf("%s tracks none of a1.txt, t.txt, e1.txt", dir)
		}
		file := strings.Fields(tracked)[0]
		testkit.WriteFile(t, dir, file, "unstaged edit by a person\n")
	}
}

// targetsStop runs the issue's fold under targets and returns the line it
// stopped at the fold target of: a fixup at the top for a commit of layer 2.
func targetsStop(t *testing.T) (*targetsLine, string) {
	t.Helper()
	ctx, s, logPath := synccmdtest.InPlaceCheckedLine(t, "")
	l := &targetsLine{ctx: ctx, s: s}
	seedWork(t, l)
	l.before = workState(t, l)
	tmp := s.Paths[gittest.StackTmp]
	if err := os.WriteFile(filepath.Join(tmp, "b1.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
	opts := synccmdtest.NoAgents()
	opts.EachCommit = "targets"
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if got := workState(t, l); got != l.before {
		t.Errorf("the stop changed what the other worktrees or the stash held:\n%s\nwas:\n%s", got, l.before)
	}
	return l, logPath
}

// A stop at a fold target is handed over like any check stop: an amend that
// adds the fix and a resume, which takes no flag, run the check at the
// amended target, go on under targets and finish the line.
func TestSyncResumeAfterATargetsStopKeepsTargets(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	l, logPath := targetsStop(t)
	tmp := l.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	res, err := syncResumeJSON(t, l.ctx, gittest.StackTmp, opts)
	if err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume stopped: %v", err)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultRebased || p.EachCommit != "targets" {
		t.Fatalf("participant %+v", p)
	}
	want := []string{"feat(s1): a2.txt", "feat(s2): b1.txt", "feat(s2): b1.txt", "feat(s2): b3.txt", "chore(s3): bump to 1.0.1", "feat(s4): d2.txt", "feat(s5): e3.txt"}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q", got, want)
	}
	if busy, _ := wtsync.RebaseInProgress(tmp); busy {
		t.Error("the rebase did not finish")
	}
	if got := testkit.GitOut(t, tmp, "show", "HEAD:fix.txt"); got != "fixed" {
		t.Errorf("the fix is not in the line: %q", got)
	}
	if got := workState(t, l); got != l.before {
		t.Errorf("the resume changed what the other worktrees or the stash held:\n%s\nwas:\n%s", got, l.before)
	}
}

// Undo after a stop at a target restores the line, every layer and the
// worktrees, leaves no marker and keeps the safety ref on the old tip.
func TestSyncUndoAfterATargetsStopRestoresEverything(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	l, _ := targetsStop(t)
	main := l.ctx.Repo.MainRoot
	// The fixup commit is the line's own; the tip undo goes back to is that.
	tip := testkit.GitOut(t, main, "rev-parse", "refs/wt-sync/tmp/stack/99")
	undoBack(t, main, l.s, l.s.Layers...)
	var out bytes.Buffer
	if err := SyncUndo(l.ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	undoBack(t, main, l.s, l.s.Layers...)
	if got := testkit.GitOut(t, main, "rev-parse", gittest.StackTmp); got != tip {
		t.Errorf("%s is at %s after the undo, want %s", gittest.StackTmp, got, tip)
	}
	if got := workState(t, l); got != l.before {
		t.Errorf("the undo changed what the other worktrees or the stash held:\n%s\nwas:\n%s", got, l.before)
	}
	// The line's own worktree is back on the old tip with nothing left over.
	tmp := l.s.Paths[gittest.StackTmp]
	if got := testkit.GitOut(t, tmp, "rev-parse", "HEAD"); got != tip {
		t.Errorf("%s's worktree is at %s after the undo, want %s", gittest.StackTmp, got, tip)
	}
	if got := testkit.GitOut(t, tmp, "status", "--porcelain=v1", "-uall"); got != "" {
		t.Errorf("%s's worktree is not clean after the undo:\n%s", gittest.StackTmp, got)
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", wtsync.StackMapPrefix); refs != "" {
		t.Errorf("markers left behind:\n%s", refs)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "refs/wt-sync/tmp/stack/99"); got != tip {
		t.Errorf("the safety ref is at %s, want %s", got, tip)
	}
	if busy, _ := wtsync.RebaseInProgress(l.s.Paths[gittest.StackTmp]); busy {
		t.Error("a rebase is still in progress")
	}
}

// A fold whose per-layer steps fold generated output into a layer's owner
// works under targets: the owner and the layer's tip are checked again, the
// run finishes, and every layer holds a tree that regenerates to itself.
func TestFoldUnderTargetsChecksTheFoldedOwnersAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	synccmdtest.FakeDocker(t)
	ctx, s := synccmdtest.FoldLine(t, gittest.FoldYAML("max-plus-patch"))
	main := ctx.Repo.MainRoot
	opts := runAt(200, &synccmd.StackOptions{Autosquash: true})
	opts.EachCommit = "targets"
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "1 layer held") {
		t.Fatalf("err %v, want f3 held\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.FoldTmp)
	if p.Result != syncrun.ResultRebased || p.EachCommit != "targets" {
		t.Fatalf("participant %+v\n%s", p, human)
	}
	// f1's owner and tip, f3's owner and tip, and f5's commit the step made:
	// every run here is owner or tip, so all five are checked again.
	if p.Rechecked != 5 {
		t.Errorf("%d folded commits checked again, want 5\n%s", p.Rechecked, human)
	}
	line := testkit.GitOut(t, main, "rev-parse", gittest.FoldTmp)
	for _, b := range []string{"f1", "f3"} {
		if !regenerates(t, main, deref(layersOfStack(p.Stack)[b].NewTip)) {
			t.Errorf("%s's tip does not regenerate to itself", b)
		}
	}
	if line == s.Tips[gittest.FoldTmp] {
		t.Error("the line did not move")
	}
}
