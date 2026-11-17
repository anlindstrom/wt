package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/provision"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// layerStack is the shape that went wrong in the field: four branches on one
// line, bottom and top in worktrees and the two between them in none, with
// trunk then moving on. The branches are feat_wt/bottom, loose-one,
// loose-two and feat_wt/top, one commit each.
func layerStack(t *testing.T) (f *synccmdtest.TrunkFixture, top string) {
	t.Helper()
	f = synccmdtest.NewTrunkFixture(t)
	var buf bytes.Buffer
	bottom, err := provision.New(f.Ctx, "feat/bottom", provision.NewOptions{NoSetup: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	top, err = provision.New(f.Ctx, "feat/top", provision.NewOptions{NoSetup: true, Base: "feat_wt/bottom"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testkit.GitIn(t, dir, "add", "-A")
		testkit.GitIn(t, dir, "commit", "-q", "-m", name)
	}
	commit(bottom, "one")
	// The top worktree carries the rest, with a branch left at each of the
	// two middle commits and no worktree for it.
	testkit.GitIn(t, top, "reset", "-q", "--hard", "feat_wt/bottom")
	commit(top, "two")
	testkit.GitIn(t, f.Main, "branch", "loose-one", "feat_wt/top")
	commit(top, "three")
	testkit.GitIn(t, f.Main, "branch", "loose-two", "feat_wt/top")
	commit(top, "four")
	f.AdvanceOrigin(t, 1)
	return f, top
}

// wt up moves the branches of a stack that have no worktree along with the
// ones that do: they end on the replayed commits, not on the old ones.
func TestUpMovesLayerBranchesBetweenTwoWorktrees(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	f, _ := layerStack(t)
	var out bytes.Buffer
	if err := synccmd.Up(f.Ctx, "top", synccmdtest.NoAgents(), &out); err != nil {
		t.Fatalf("up: %v\n%s", err, out.String())
	}
	for _, b := range []string{"feat_wt/bottom", "loose-one", "loose-two", "feat_wt/top"} {
		if !testkit.GitAncestor(t, f.Main, "origin/main", b) {
			t.Errorf("%s was left behind trunk:\n%s", b, out.String())
		}
	}
	for b, subject := range map[string]string{"loose-one": "two", "loose-two": "three"} {
		if got := testkit.GitOut(t, f.Main, "log", "-1", "--format=%s", b); got != subject {
			t.Errorf("%s ends at %q, want %q", b, got, subject)
		}
	}
}
