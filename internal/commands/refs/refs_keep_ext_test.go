package refs_test

import (
	"slices"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/refs"
	"github.com/anlindstrom/wt/internal/commands/refs/refstest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/jsonout"
)

// keptRepo has a deliberate backup of each age: one main contains, one with
// work of its own made 30 days ago, one with work made today.
func keptRepo(t *testing.T) (ctx *kit.Context, main string) {
	t.Helper()
	c, main, _ := testkit.SweepRepo(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	first := testkit.GitOut(t, main, "rev-parse", "main")
	testkit.GitIn(t, main, "commit", "-q", "--allow-empty", "-m", "second")
	testkit.GitIn(t, main, "branch", "backup/2026-11-01/contained", first)
	testkit.GitAt(t, main, old, "branch", "backup/2026-11-01/old", "main")
	testkit.CommitOn(t, main, "backup/2026-11-01/old", "old work", old)
	testkit.GitIn(t, main, "branch", "backup-young", "main")
	testkit.CommitOn(t, main, "backup-young", "young work", time.Now())
	testkit.GitIn(t, main, "branch", "feat/x-backup", first)
	testkit.GitIn(t, main, "branch", "safe/contained", first)
	return c, main
}

// A deliberate backup is listed as kept, whatever its age or whether main
// has its commits, and no sweep moves it.
func TestRefsSweepNeverSelectsADeliberateBackup(t *testing.T) {
	t.Parallel()
	ctx, main := keptRepo(t)
	p, err := refs.RefsPlanJSON(t, ctx, refs.Options{NoFetch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backup/2026-11-01/contained", "backup/2026-11-01/old", "backup-young", "feat/x-backup"} {
		it := refs.RefPlanItem(t, p, "refs/heads/"+name)
		if it.Category != refs.RefKept || it.Selected || !slices.Contains(it.Kept, refs.KeptDeliberate) {
			t.Errorf("%s = %+v, want kept as a deliberate backup", name, it)
		}
	}
	if it := refs.RefPlanItem(t, p, "refs/heads/safe/contained"); it.Category != refs.RefBackupContained || !it.Selected {
		t.Errorf("a transient copy is still swept: %+v", it)
	}
	r, human, err := refstest.RefsSweepJSON(t, ctx, refs.Options{NoFetch: true, Expect: *p.Token})
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	for _, name := range []string{"backup/2026-11-01/contained", "backup/2026-11-01/old", "backup-young", "feat/x-backup"} {
		if it := refs.RefResultOf(t, r, "refs/heads/"+name); it.Result != refs.RefResultKept || it.Pinned || it.Deleted ||
			jsonout.Deref(it.Reason) != refs.KeptDeliberate || !ctx.Repo.BranchExists(name) {
			t.Errorf("%s = %+v", name, it)
		}
	}
	if ctx.Repo.BranchExists("safe/contained") {
		t.Error("safe/contained should be swept")
	}
	_ = main
}

// Naming a deliberate backup with --only refuses the lot: there is no way to
// sweep one.
func TestRefsSweepOnlyRefusesADeliberateBackup(t *testing.T) {
	t.Parallel()
	ctx, _ := keptRepo(t)
	r, _, err := refstest.RefsSweepJSON(t, ctx, refs.Options{NoFetch: true,
		Only: []string{"refs/heads/backup/2026-11-01/old", "refs/heads/safe/contained"}})
	if err == nil || r.Outcome != jsonout.OutcomeRefused || !ctx.Repo.BranchExists("backup/2026-11-01/old") ||
		!ctx.Repo.BranchExists("safe/contained") {
		t.Fatalf("err %v, result %+v", err, r)
	}
}

// Keep wins: a name both lists match is kept.
func TestRefsKeepWinsOverAnExplicitSweepList(t *testing.T) {
	t.Parallel()
	ctx, _ := keptRepo(t)
	ctx.User.RefSweepPatterns = []string{"backup/*", "safe/*"}
	p, err := refs.RefsPlanJSON(t, ctx, refs.Options{NoFetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if it := refs.RefPlanItem(t, p, "refs/heads/backup/2026-11-01/contained"); it.Category != refs.RefKept || it.Selected {
		t.Errorf("both lists match: %+v", it)
	}
	ctx.User.RefKeepPatterns = nil
	p, err = refs.RefsPlanJSON(t, ctx, refs.Options{NoFetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if it := refs.RefPlanItem(t, p, "refs/heads/backup/2026-11-01/contained"); !it.Selected {
		t.Errorf("with no keep list, the sweep list rules: %+v", it)
	}
}

// A prefold/* tag is a transient copy: it is swept.
func TestRefsSweepTakesAPrefoldTag(t *testing.T) {
	t.Parallel()
	ctx, main, _ := testkit.SweepRepo(t)
	first := testkit.GitOut(t, main, "rev-parse", "main")
	testkit.GitIn(t, main, "tag", "prefold/feat-x", first)
	p, err := refs.RefsPlanJSON(t, ctx, refs.Options{NoFetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if it := refs.RefPlanItem(t, p, "refs/tags/prefold/feat-x"); it.Category != refs.RefBackupContained || !it.Selected {
		t.Errorf("prefold tag = %+v", it)
	}
}
