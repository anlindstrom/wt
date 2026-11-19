package synccmd_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// A run given --alone moves the one branch named. A branch in no worktree
// that points at one of the commits it replays is not moved with it: it stays
// at its commit, is listed as left alone, and an undo leaves it there.
func TestSyncRunAloneLeavesBranchesInNoWorktree(t *testing.T) {
	t.Parallel()
	skipShort(t)
	f, _ := layerStack(t, false, func(f *synccmdtest.TrunkFixture) { f.Declare(t) })
	main := f.Main
	loose := []string{"loose-one", "loose-two"}
	was := map[string]string{}
	for _, b := range loose {
		was[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	oldTop := testkit.GitOut(t, main, "rev-parse", "feat_wt/top")
	same := func(when string) {
		t.Helper()
		for b, sha := range was {
			if got := testkit.GitOut(t, main, "rev-parse", b); got != sha {
				t.Errorf("%s: %s is at %s, want %s", when, b, got, sha)
			}
		}
	}

	var js bytes.Buffer
	opts := synccmdtest.NoAgents()
	opts.Alone, opts.Journal = true, syncrun.NewSyncRunJournal(&js, "sync rebase")
	var out bytes.Buffer
	if err := synccmd.SyncRebase(f.Ctx, []string{"top"}, opts, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := testkit.GitOut(t, main, "rev-parse", "feat_wt/top"); got == oldTop {
		t.Fatalf("the run did not rebase top:\n%s", out.String())
	}
	same("after the run")
	for _, b := range loose {
		if !strings.Contains(out.String(), "left alone: "+b) {
			t.Errorf("the output does not name %s as left alone:\n%s", b, out.String())
		}
	}
	if strings.Contains(out.String(), "moved loose-") {
		t.Errorf("a loose branch was moved:\n%s", out.String())
	}
	res := synccmdtest.DecodeSyncRun(t, js.Bytes())
	left := map[string]syncrun.LeftAlone{}
	for _, l := range res.LeftAlone {
		left[l.Branch] = l
	}
	for _, b := range loose {
		if l, ok := left[b]; !ok || l.Path != nil || l.Work != nil {
			t.Errorf("leftAlone has %s as %+v, want a branch in no worktree: %+v", b, l, res.LeftAlone)
		}
	}
	if _, ok := left["feat_wt/bottom"]; !ok {
		t.Errorf("leftAlone lost the stranded worktree: %+v", res.LeftAlone)
	}
	for _, p := range res.Worktrees {
		if len(p.Layers) != 0 {
			t.Errorf("%s records layers %+v under --alone", p.Branch, p.Layers)
		}
	}

	var undone bytes.Buffer
	if err := syncfix.SyncUndo(f.Ctx, "top", syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if got := testkit.GitOut(t, main, "rev-parse", "feat_wt/top"); got != oldTop {
		t.Errorf("undo left top at %s, want %s", got, oldTop)
	}
	same("after the undo")
}
