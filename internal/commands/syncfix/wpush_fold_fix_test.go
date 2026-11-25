package syncfix

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// The declaration of a fold line whose check fails at f1's tip and nowhere
// else, with the generator folded into the commit that owns its output.
const foldFixYAML = "conflicts:\n  - paths: [v.txt]\n    strategy: owned-line\n    line: '^\\d'\n    rule: max-plus-patch\n" +
	"defer:\n  - run: ./gen.sh\n    paths: [gen/**]\n    each: layer\n    fold: owner\n    needs: [docker]\n" +
	"    commit: \"chore: regenerate the spec\"\n" +
	"each_commit:\n  - run: 'test \"$(git log -1 --format=%s)\" != \"chore(f1): bump to 1.1.0\"'\n    paths: [v.txt]\n"

// wt#216: a fix commit added at f1's check stop is then rewritten by f1's
// fold step, which folds the regenerated spec into f1's owner and replays the
// commits above it. The added commit is still counted, by who made it, when
// and its subject, so the layers move; undo puts every branch back.
func TestFoldOntoTrunkCountsAnAddedFixAFoldStepRewrote(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.FoldLine(t, foldFixYAML)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.FoldTmp]
	opts := synccmdtest.Onto("main", "")
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	opts.EachCommit = "tips"
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want the check stop at f1's tip\n%s", err, human)
	}
	testkit.WriteFile(t, tmp, "fix.txt", "fixed\n")
	testkit.GitIn(t, tmp, "add", "fix.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "fix: the check")
	added := testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	ropts := noResumeAgents()
	ropts.Push = syncrun.PushNever
	var human bytes.Buffer
	err := SyncResume(ctx, gittest.FoldTmp, ropts, &human)
	if err == nil || err.Error() != "not completed: tmp/fold (1 layer held)" {
		t.Fatalf("resume: err %v, want only f3 held\n%s", err, human.String())
	}
	if testkit.GitOut(t, main, "log", "--format=%H", gittest.FoldTmp) == "" ||
		strings.Contains(testkit.GitOut(t, main, "log", "--format=%H", gittest.FoldTmp), added) {
		t.Fatalf("the fold step did not rewrite the added commit; the case is not met\n%s", human.String())
	}
	for _, b := range []string{"f1", "f2", "f4", "f5"} {
		if testkit.GitOut(t, main, "rev-parse", b) == s.Tips[b] {
			t.Errorf("%s did not move\n%s", b, human.String())
		}
	}
	if !strings.Contains(testkit.GitOut(t, main, "log", "--format=%s", "f1"), "fix: the check") {
		t.Errorf("f1 does not end with the fix:\n%s", testkit.GitOut(t, main, "log", "--oneline", "f1"))
	}
	var undone bytes.Buffer
	if err := SyncUndo(ctx, gittest.FoldTmp, noAgentsUndo(), &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.FoldTmp}, s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s", b, got, s.Tips[b])
		}
	}
}
