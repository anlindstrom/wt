package syncfix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// confirmedLine is resolutionLine with a third fixup, of X, that rewrites
// X's last line again: the run stops at X, and again at that fixup.
func confirmedLine(t *testing.T) (*kit.Context, *gittest.StackLine, worktreeState) {
	t.Helper()
	ctx, s := synccmdtest.StackLine(t, "")
	tmp := s.Paths[gittest.StackTmp]
	if err := os.WriteFile(filepath.Join(tmp, "t.txt"), []byte(tText("mine", "last: gone()")), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "feat: call gone()")
	foldFixups(t, s)
	if err := os.WriteFile(filepath.Join(tmp, "t.txt"), []byte(tText("mine", "last: gone2()")), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat: call gone()")
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

// A stack folding three fixups that stops at a conflict in the first commit
// and again at the last fixup, resolved and resumed twice, calls none of the
// fixups that folded missing: wt saw each fold run. Undo puts every branch
// back exactly.
func TestAResumedFoldDoesNotCallAFoldedFixupMissing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, was := confirmedLine(t)
	resume := func() string {
		var out, human bytes.Buffer
		opts := noResumeAgents()
		opts.Push = syncrun.PushNever
		opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
		_ = SyncResume(ctx, gittest.StackTmp, opts, &human)
		return human.String()
	}
	resolveT(t, s, tText("t2 mine", "last"))
	first := resume()
	if !strings.Contains(first, "needs you") && !strings.Contains(first, "stop ") {
		t.Fatalf("the first resume did not stop at the fixup:\n%s", first)
	}
	resolveT(t, s, tText("t2 mine", "last"))
	last := resume()
	if strings.Contains(last, "not found after the rebase") {
		t.Errorf("the resume calls a folded fixup missing:\n%s", last)
	}
	var undo bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &undo); err != nil {
		t.Fatalf("%v\n%s", err, undo.String())
	}
	sameState(t, "after the undo from the end", was, snapshotOf(t, ctx, s))
}
