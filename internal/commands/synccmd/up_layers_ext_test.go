package synccmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/provision"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// layerStack is the shape that went wrong in the field: four branches on one
// line, bottom and top in worktrees and the two between them in none, with
// trunk then moving on. The branches are feat_wt/bottom, loose-one,
// loose-two and feat_wt/top, one commit each.
func layerStack(t *testing.T, withFold bool, prep ...func(*synccmdtest.TrunkFixture)) (f *synccmdtest.TrunkFixture, top string) {
	t.Helper()
	f = synccmdtest.NewTrunkFixture(t)
	for _, p := range prep {
		p(f)
	}
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
	if withFold {
		// A branch at a commit autosquash would fold: not one to move.
		if err := os.WriteFile(filepath.Join(top, "fix.txt"), []byte("fix\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testkit.GitIn(t, top, "add", "-A")
		testkit.GitIn(t, top, "commit", "-q", "-m", "fixup! two")
		testkit.GitIn(t, f.Main, "branch", "loose-fix", "feat_wt/top")
	}
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
	f, _ := layerStack(t, false)
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

// The run says which layer branches it moved and which it left, and why; a
// branch at a commit autosquash would fold stays where it was.
func TestUpJSONListsTheLayerBranchesItMovedAndLeft(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	f, _ := layerStack(t, true)
	oldFix := testkit.GitOut(t, f.Main, "rev-parse", "loose-fix")
	r, err := synccmdtest.UpJSON(t, f.Ctx, "top", synccmdtest.NoAgents())
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	got := map[string]syncrun.LayerBranch{}
	for _, p := range r.Worktrees {
		if p.Branch != "feat_wt/top" {
			if len(p.Layers) != 0 {
				t.Errorf("%s has layers %+v", p.Branch, p.Layers)
			}
			continue
		}
		for _, l := range p.Layers {
			got[l.Branch] = l
		}
	}
	for _, b := range []string{"loose-one", "loose-two"} {
		l := got[b]
		if l.Result != syncrun.LayerMoved || l.After == nil || *l.After != testkit.GitOut(t, f.Main, "rev-parse", b) || l.Reason != nil {
			t.Errorf("%s = %+v", b, l)
		}
	}
	fix := got["loose-fix"]
	if fix.Result != syncrun.LayerLeft || fix.After != nil || fix.Reason == nil || fix.Before != oldFix || testkit.GitOut(t, f.Main, "rev-parse", "loose-fix") != oldFix {
		t.Errorf("loose-fix = %+v", fix)
	}
}

// wt sync undo puts the layer branches back with the worktrees.
func TestSyncUndoPutsLayerBranchesBackWithTheStack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	f, _ := layerStack(t, false)
	was := map[string]string{}
	for _, b := range []string{"loose-one", "loose-two"} {
		was[b] = testkit.GitOut(t, f.Main, "rev-parse", b)
	}
	if err := synccmd.Up(f.Ctx, "top", synccmdtest.NoAgents(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	undo := syncfix.UndoOptions{VerbOptions: synccmdtest.NoAgents().VerbOptions}
	if err := syncfix.SyncUndo(f.Ctx, "top", undo, &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	for b, tip := range was {
		if got := testkit.GitOut(t, f.Main, "rev-parse", b); got != tip {
			t.Errorf("%s is at %s after undo, was %s:\n%s", b, got, tip, out.String())
		}
	}
}

// wt status lists the layer branches wt up would move, and its token covers
// them: a branch that appears between two members is a plan that changed.
func TestStatusListsLayersAndItsTokenCoversThem(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	f, _ := layerStack(t, true)
	p := synccmdtest.PlanOfWork(t, f.Ctx, "top")
	var names []string
	for _, m := range p.Stack {
		for _, l := range m.Layers {
			names = append(names, m.Branch+":"+l.Branch+":"+testkit.Str(l.Left))
		}
	}
	if len(names) != 3 || p.Stack[1].Layers[2].Left == nil {
		t.Fatalf("layers = %v, want loose-one and loose-two to move and loose-fix to be left on top", names)
	}
	testkit.GitIn(t, f.Main, "branch", "loose-late", "feat_wt/bottom")
	if q := synccmdtest.PlanOfWork(t, f.Ctx, "top"); testkit.Str(q.Token) == testkit.Str(p.Token) {
		t.Errorf("a new layer branch left the token as it was: %s", testkit.Str(p.Token))
	}
}

// Local trunk is never a layer: with commits that are not on origin and a
// branch cut from them, it sits on the line, and wt up must leave it.
func TestUpNeverMovesLocalTrunkAsALayer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	f, _ := layerStack(t, false, func(f *synccmdtest.TrunkFixture) {
		testkit.GitIn(t, f.Main, "commit", "-q", "--allow-empty", "-m", "local only")
		testkit.GitIn(t, f.Main, "checkout", "-q", "--detach")
	})
	was := testkit.GitOut(t, f.Main, "rev-parse", "refs/heads/main")
	r, err := synccmdtest.UpJSON(t, f.Ctx, "top", synccmdtest.NoAgents())
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := testkit.GitOut(t, f.Main, "rev-parse", "refs/heads/main"); got != was {
		t.Errorf("local main moved from %s to %s", was, got)
	}
	var main *syncrun.LayerBranch
	for _, p := range r.Worktrees {
		for i, l := range p.Layers {
			if l.Branch == "main" {
				main = &p.Layers[i]
			}
		}
	}
	if main == nil || main.Result != syncrun.LayerLeft || main.Reason == nil {
		t.Errorf("main = %+v, want it named as left", main)
	}
}

// A backup name is a backup however deep: backup/* matches backup/2026-11/feat
// for this, though a glob's * stops at a slash.
func TestUpLeavesNestedBackupBranches(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a sync run over real repositories and worktrees")
	}
	f, _ := layerStack(t, false)
	testkit.GitIn(t, f.Main, "branch", "backup/2026-11/feat", "loose-one")
	was := testkit.GitOut(t, f.Main, "rev-parse", "backup/2026-11/feat")
	r, err := synccmdtest.UpJSON(t, f.Ctx, "top", synccmdtest.NoAgents())
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := testkit.GitOut(t, f.Main, "rev-parse", "backup/2026-11/feat"); got != was {
		t.Errorf("the backup moved from %s to %s", was, got)
	}
	for _, p := range r.Worktrees {
		for _, l := range p.Layers {
			if l.Branch == "backup/2026-11/feat" && (l.Result != syncrun.LayerLeft || l.Reason == nil) {
				t.Errorf("backup = %+v, want it named as left", l)
			}
		}
	}
}
