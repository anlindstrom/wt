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
	"github.com/anlindstrom/wt/internal/wtsync"
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

// resumeWithOutput is wt sync resume of the stack line, its result as JSON and
// what it printed for a person.
func resumeWithOutput(t *testing.T, ctx *kit.Context) (syncrun.RunResult, string, error) {
	t.Helper()
	var out, human bytes.Buffer
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
	err := SyncResume(ctx, gittest.StackTmp, opts, &human)
	return synccmdtest.DecodeSyncRun(t, out.Bytes()), human.String(), err
}

// finishedFold checks a resume that finished the rebase: none in progress,
// the layers that could move moved, no warning in what it printed or in the
// JSON's emptied entries about a fixup that folded, and the commit named.
func finishedFold(t *testing.T, ctx *kit.Context, s *gittest.StackLine, was worktreeState, res syncrun.RunResult, human string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "layers held") {
		t.Fatalf("resume: err %v, want the two layers that could not move held\n%s", err, human)
	}
	if busy, _ := wtsync.RebaseInProgress(s.Paths[gittest.StackTmp]); busy {
		t.Fatalf("resume: the rebase is still in progress\n%s", human)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	layers := stackLayers(p.Stack)
	for _, b := range []string{"s1", "s2", "s4"} {
		if l := layers[b]; deref(l.Result) != "moved" || testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", b) != deref(l.NewTip) {
			t.Errorf("resume: layer %s %+v, want moved to its new tip", b, l)
		}
	}
	for _, d := range p.Emptied {
		if d.Unmatched {
			t.Errorf("resume: emptied entry %+v is unmatched, want only the commits trunk has", d)
		}
	}
	for _, bad := range []string{"not found after the rebase", "not confirmed folded"} {
		if strings.Contains(human, bad) {
			t.Errorf("resume: a folded fixup is reported as %q:\n%s", bad, human)
		}
	}
	var undo bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &undo); err != nil {
		t.Fatalf("%v\n%s", err, undo.String())
	}
	sameState(t, "after the undo from the end", was, snapshotOf(t, ctx, s))
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
	resolveT(t, s, tText("t2 mine", "last"))
	res, human, err := resumeWithOutput(t, ctx)
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("first resume: err %v, want the fixup's conflict handed over\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultHandedOver {
		t.Fatalf("first resume: %+v\n%s", p, human)
	}
	resolveT(t, s, tText("t2 mine", "last"))
	res, human, err = resumeWithOutput(t, ctx)
	finishedFold(t, ctx, s, was, res, human, err)
}

// A resume that folds, then hands over at a failed check, and is resumed to
// the end, does not call the fixup it folded missing: what the first resume
// saw run is kept through the check's handover.
func TestAFoldSeenBeforeACheckHandoverStaysConfirmed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	block := filepath.Join(t.TempDir(), "block")
	ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+"each_commit:\n  - run: test ! -e "+block+"\n")
	tmp := s.Paths[gittest.StackTmp]
	if err := os.WriteFile(filepath.Join(tmp, "t.txt"), []byte(tText("mine", "last: gone()")), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "feat: call gone()")
	if err := os.WriteFile(filepath.Join(tmp, "n.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "add", "n.txt")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "fixup! feat: call gone()")
	untrackedAndStash(t, ctx, s)
	was := snapshotOf(t, ctx, s)
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOntoTrunk())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultHandedOver {
		t.Fatalf("line %+v\n%s", p, human)
	}
	// The resume runs the pick and the fixup behind it; the check at the
	// folded commit fails.
	gittest.WriteFile(t, block, "")
	resolveT(t, s, tText("t2 mine", "last"))
	res, human, err = resumeWithOutput(t, ctx)
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("first resume: err %v, want the failed check handed over\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultHandedOver {
		t.Fatalf("first resume: %+v\n%s", p, human)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	res, human, err = resumeWithOutput(t, ctx)
	finishedFold(t, ctx, s, was, res, human, err)
}
