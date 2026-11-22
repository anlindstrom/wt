package syncfix

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// tText is t.txt of the stack fixture: a first line, ten filler lines, a last.
func tText(first, last string) string {
	return first + "\n" + strings.Repeat("filler\n", 10) + last + "\n"
}

// resolutionLine is a stack line with, above s5, a commit X that rewrites
// t.txt's first line (which trunk also rewrote: a stop that is yours) and
// its last line to call a function trunk no longer has, and two fixups
// after it. It is run as --stack --autosquash --onto main.
func resolutionLine(t *testing.T) (*kit.Context, *gittest.StackLine, worktreeState) {
	t.Helper()
	ctx, s := synccmdtest.StackLine(t, "")
	tmp := s.Paths[gittest.StackTmp]
	if err := os.WriteFile(filepath.Join(tmp, "t.txt"), []byte(tText("mine", "last: gone()")), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "feat: call gone()")
	foldFixups(t, s)
	untrackedAndStash(t, ctx, s)
	was := snapshotOf(t, ctx, s)
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOntoTrunk())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultHandedOver {
		t.Fatalf("line %+v\n%s", p, human)
	}
	return ctx, s, was
}

// resolveT is the person's resolution at the stop: both first lines, and the
// call to the function trunk deleted dropped from the last line, which the
// textual merge keeps.
func resolveT(t *testing.T, s *gittest.StackLine, content string) {
	t.Helper()
	tmp := s.Paths[gittest.StackTmp]
	if err := os.WriteFile(filepath.Join(tmp, "t.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "add", "t.txt")
}

func resumeFold(t *testing.T, ctx *kit.Context) (syncrun.RunResult, error) {
	t.Helper()
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	return syncResumeJSON(t, ctx, gittest.StackTmp, opts)
}

// A stop's resolution that drops a line the textual merge keeps is the
// person's: the fold's content check takes it, every layer moves, and undo
// puts the line back exactly.
func TestFoldTakesWhatAStopResolvedThatTheMergeWouldNotGive(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, was := resolutionLine(t)
	main := ctx.Repo.MainRoot
	resolveT(t, s, tText("t2 mine", "last"))
	resumed, err := resumeFold(t, ctx)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the resume to say two layers were held", err)
	}
	p := synccmdtest.Participant(t, resumed, gittest.StackTmp)
	g := p.Fold.Guard
	if g == nil || g.Outcome != "passed" {
		t.Fatalf("fold.guard %+v", g)
	}
	layers := stackLayers(p.Stack)
	for _, b := range []string{"s1", "s2", "s4"} {
		l := layers[b]
		if deref(l.Result) != "moved" || testkit.GitOut(t, main, "rev-parse", b) != deref(l.NewTip) {
			t.Errorf("%s %+v", b, l)
		}
	}
	if got := testkit.GitOut(t, main, "show", gittest.StackTmp+":t.txt"); !strings.HasSuffix(got, "\nlast") || strings.Contains(got, "gone()") {
		t.Errorf("the line's t.txt is %q, want the resolution", got)
	}
	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	sameState(t, "after the undo from the end", was, snapshotOf(t, ctx, s))
}

// Only a path that was unmerged when wt stopped is a person's resolution:
// another path the stop left them (a refused lift on a clean merge) is not.
func TestResolutionsAtStopAreOnlyThePathsThatWereUnmerged(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, _ := resolutionLine(t)
	tmp := s.Paths[gittest.StackTmp]
	resolveT(t, s, tText("t2 mine", "last"))
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok {
		t.Fatalf("state %v %v", ok, err)
	}
	st.Left = append(st.Left, "a1.txt") // merged cleanly, so never unmerged
	var got []string
	for _, r := range wtsync.ResolutionsAtStop(tmp, st) {
		got = append(got, r.Path)
	}
	if len(got) != 1 || got[0] != "t.txt" {
		t.Errorf("recorded %v, want only t.txt", got)
	}
	_ = ctx
}

// A break inserted after the conflict, continued by hand and then amended,
// is not the conflict's resolution: nothing is recorded, so the fold is
// checked as before and refuses what the merge does not give.
func TestAHandContinuedBreakDoesNotAuthorizeLaterEdits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	handContinuedBreak(t, false)
}

// A branch or tag named REBASE_HEAD at the stopped commit is not the
// rebase's own REBASE_HEAD, which the --continue by hand removed.
func TestARefNamedRebaseHeadDoesNotAuthorizeLaterEdits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	handContinuedBreak(t, true)
}

// handContinuedBreak inserts a break after the conflict, resolves and
// continues by hand, amends after the stop, and checks the fold refuses; with
// namedRef a tag named REBASE_HEAD points at the stopped commit first.
func handContinuedBreak(t *testing.T, namedRef bool) {
	t.Helper()
	ctx, s, _ := resolutionLine(t)
	tmp := s.Paths[gittest.StackTmp]
	todo := testkit.GitOut(t, tmp, "rev-parse", "--git-path", "rebase-merge/git-rebase-todo")
	if !filepath.IsAbs(todo) {
		todo = filepath.Join(tmp, todo)
	}
	b, err := os.ReadFile(todo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(todo, append([]byte("break\n"), b...), 0o644); err != nil { //nolint:gosec // the sequencer list of a test repository, from git rev-parse
		t.Fatal(err)
	}
	resolveT(t, s, tText("t2 mine", "last: gone()"))
	if namedRef {
		stopped := strings.TrimSpace(testkit.GitOut(t, tmp, "rev-parse", "REBASE_HEAD"))
		testkit.GitIn(t, tmp, "tag", "REBASE_HEAD", stopped)
	}
	cmd := exec.Command("git", "-c", "core.editor=true", "rebase", "--continue")
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// Amended afterwards: the content the issue's resolution has.
	if err := os.WriteFile(filepath.Join(tmp, "t.txt"), []byte(tText("t2 mine", "last")), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit", "-a")
	_, err = resumeFold(t, ctx)
	if err == nil || !strings.Contains(err.Error(), "no layer moved") {
		t.Fatalf("err %v, want the fold refused", err)
	}
}
