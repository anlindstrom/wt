package syncfix

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// A commit added at s2's check stop with git commit -C, taking the author,
// time and subject of s1's first commit, is counted in s2 and the layers
// above it, never in s1, which only looks like it: every layer moves, s1
// keeps its own commits, and undo puts every branch back.
func TestFoldOntoTrunkCountsAnAddedFixOnlyWhereItIs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, _ := synccmdtest.CheckedLine(t)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	opts := foldOntoTrunk()
	opts.EachCommit = "tips"
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	lookalike := testkit.GitOut(t, tmp, "log", "-1", "--format=%H", "--fixed-strings", "--grep", s.Subjects["s1"][0], "HEAD")
	if lookalike == "" {
		t.Fatal("s1's first commit is not on the rebased line")
	}
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-C", lookalike)
	ropts := noResumeAgents()
	ropts.Push = syncrun.PushNever
	var human bytes.Buffer
	err := SyncResume(ctx, gittest.StackTmp, ropts, &human)
	if err == nil || err.Error() != "not completed: tmp/stack (2 layers held)" {
		t.Fatalf("resume: err %v, want only the two held layers\n%s", err, human.String())
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == s.Tips[b] {
			t.Errorf("layer %s did not move\n%s", b, human.String())
		}
	}
	if got := testkit.GitOut(t, main, "log", "--format=%s", "main..s1"); got != strings.Join(reversed(s.Subjects["s1"]), "\n") {
		t.Errorf("s1 holds %q, want only its own commits", got)
	}
	if got := testkit.GitOut(t, main, "show", "s2:fix.txt"); got != "fixed" {
		t.Errorf("s2 lacks the fix: %q", got)
	}
	var undone bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s", b, got, s.Tips[b])
		}
	}
}

func reversed(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}
