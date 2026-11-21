package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// patternLine is the issue's case: three layers tmp/layer/a..c on the line of
// tmp/line, and a spike branch on the line's tip checked out, with tracked and
// untracked changes, in another worktree.
func patternLine(t *testing.T) (ctx *kit.Context, spikePath string) {
	t.Helper()
	ctx, paths := layeredLine(t, false, stray{"spike", "tip", "dirty"})
	return ctx, paths["spike"]
}

// --layer takes a glob: 'tmp/layer/*' takes the three layers and not the
// spike branch on the line's tip. A run moves exactly those three, leaves the
// spike branch and its worktree as they were, and undo puts it all back.
func TestSyncRebaseStackLayerPatternLeavesAStrayBranchAlone(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, spike := patternLine(t)
	main := ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}

	before := map[string]string{}
	for _, b := range []string{"tmp/line", "tmp/layer/a", "tmp/layer/b", "tmp/layer/c", "spike"} {
		before[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	snap := func(dir string) string { return worktreeSnap(t, dir) }
	spikeBefore := snap(spike)

	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: "tmp/line", Layers: []string{"tmp/layer/*"}}})
	var got []string
	for _, l := range o.Stack.Layers {
		got = append(got, l.Branch)
	}
	if strings.Join(got, " ") != "tmp/layer/a tmp/layer/b tmp/layer/c" {
		t.Fatalf("the look takes %v, want exactly the three", got)
	}

	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/line", opts)
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	layers := layersOf(synccmdtest.Participant(t, res, "tmp/line").Stack)
	if len(layers) != 3 {
		t.Fatalf("the run took %d layers: %v", len(layers), layers)
	}
	for _, b := range []string{"tmp/line", "tmp/layer/a", "tmp/layer/b", "tmp/layer/c"} {
		if testkit.GitOut(t, main, "rev-parse", b) == before[b] {
			t.Errorf("%s did not move", b)
		}
	}
	if testkit.GitOut(t, main, "rev-parse", "spike") != before["spike"] || snap(spike) != spikeBefore {
		t.Errorf("the spike branch or its worktree changed:\n%s", snap(spike))
	}

	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, "tmp/line", syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for b, tip := range before {
		if testkit.GitOut(t, main, "rev-parse", b) != tip {
			t.Errorf("undo left %s elsewhere than %s", b, tip)
		}
	}
	if snap(spike) != spikeBefore {
		t.Errorf("undo changed the spike worktree:\n%s", snap(spike))
	}
	if b, err := os.ReadFile(filepath.Join(spike, "scratch.txt")); err != nil || string(b) != "untracked\n" {
		t.Errorf("the untracked file is gone or changed: %v", err)
	}
}

// A pattern that takes no layer is refused before anything moves, naming it.
func TestSyncRebaseStackLayerPatternThatTakesNothingMovesNothing(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := patternLine(t)
	main := ctx.Repo.MainRoot
	line := testkit.GitOut(t, main, "rev-parse", "tmp/line")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Layers: []string{"nothing/*"}}
	_, human, err := synccmdtest.StackRun(t, ctx, "tmp/line", opts)
	if err == nil || !strings.Contains(err.Error(), "--layer nothing/*") {
		t.Fatalf("err %v, want the pattern named\n%s", err, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "tmp/line") != line {
		t.Error("the line moved")
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", wtsync.StackMapPrefix, "refs/wt-sync/"); refs != "" {
		t.Errorf("refs left behind: %s", refs)
	}
}

// A pattern that matches no branch at all is refused before local trunk is
// synchronized: with origin ahead and fetching on, trunk stays where it was.
func TestSyncRebaseStackLayerPatternRefusedBeforeTrunkMoves(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := patternLine(t)
	main := ctx.Repo.MainRoot
	// origin is ahead of local trunk: a clone pushes one more commit.
	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Git(t, main, "clone", "-q", "--bare", main, origin)
	gittest.Git(t, main, "remote", "set-url", "origin", origin)
	clone := filepath.Join(t.TempDir(), "clone")
	gittest.Git(t, main, "clone", "-q", origin, clone)
	gittest.Git(t, clone, "config", "user.email", "t@example.com")
	gittest.Git(t, clone, "config", "user.name", "T")
	gittest.WriteFile(t, filepath.Join(clone, "ahead.txt"), "ahead\n")
	gittest.Git(t, clone, "add", "-A")
	gittest.Git(t, clone, "commit", "-q", "--no-verify", "-m", "feat: ahead")
	gittest.Git(t, clone, "push", "-q", "origin", "main")
	trunk := testkit.GitOut(t, main, "rev-parse", "main")

	opts := synccmdtest.NoAgents()
	opts.NoFetch = false
	opts.Stack = &synccmd.StackOptions{Layers: []string{"nothing/*"}}
	_, human, err := synccmdtest.StackRun(t, ctx, "tmp/line", opts)
	if err == nil || !strings.Contains(err.Error(), "--layer nothing/*") {
		t.Fatalf("err %v, want the pattern named\n%s", err, human)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "main"); got != trunk {
		t.Errorf("local trunk moved from %s to %s", trunk, got)
	}
}

// A literal backup name is taken once: in the look, not also listed as not
// taken.
func TestSyncStackLookLiteralBackupLayerIsNotAlsoNotTaken(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := patternLine(t)
	main := ctx.Repo.MainRoot
	gittest.Git(t, main, "branch", "backup/b", "tmp/layer/b")
	for _, layers := range [][]string{{"*/b", "backup/b"}, {"backup/b", "*/b"}} {
		o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: "tmp/line", Layers: layers}})
		for _, k := range o.Stack.Skipped {
			if k.Branch == "backup/b" {
				t.Errorf("%v: backup/b is a layer and also listed as not taken", layers)
			}
		}
	}
}
