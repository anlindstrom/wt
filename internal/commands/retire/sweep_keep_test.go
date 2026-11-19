package retire

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// A merged branch whose name is a deliberate backup is never on the delete
// list, though its commits are on trunk: the branch marks a moment. A merged
// transient copy still goes.
func TestSweepKeepsAMergedDeliberateBackup(t *testing.T) {
	t.Parallel()
	ctx, main, _ := testkit.SweepRepo(t)
	for _, b := range []string{"backup/2026-11-01/huge-redo", "feat/x-backup", "safe/pre-rebase-abc"} {
		testkit.BranchWithWork(t, main, b, 1)
		testkit.LandOnMain(t, main, b)
	}
	p := PlanOf(t, ctx)
	if !slices.Equal(branchNames(p.Delete), []string{"safe/pre-rebase-abc"}) {
		t.Fatalf("delete = %v, want only the transient copy", branchNames(p.Delete))
	}
	kept := branchNames(p.CheckedOut)
	slices.Sort(kept)
	if !slices.Equal(kept, []string{"backup/2026-11-01/huge-redo", "feat/x-backup"}) {
		t.Fatalf("kept = %v", kept)
	}
	for _, b := range p.CheckedOut {
		if !slices.Equal(b.KeptCodes, []string{"deliberateBackup"}) {
			t.Errorf("%s kept codes = %v", b.Name, b.KeptCodes)
		}
	}
	if out := Rendered(p); !strings.Contains(out, "a deliberate backup") {
		t.Errorf("the plan must say why:\n%s", out)
	}
	var buf bytes.Buffer
	if err := Sweep(ctx, SweepOptions{Yes: true, Agents: []agents.Agent{}}, &buf); err != nil {
		t.Fatalf("Sweep: %v\n%s", err, buf.String())
	}
	if !ctx.Repo.BranchExists("backup/2026-11-01/huge-redo") || !ctx.Repo.BranchExists("feat/x-backup") ||
		ctx.Repo.BranchExists("safe/pre-rebase-abc") {
		t.Errorf("a sweep must keep the backups and delete the copy:\n%s", buf.String())
	}
}

// Its worktree is kept too: the row says why and nothing is removed.
func TestSweepKeepsTheWorktreeOfAMergedDeliberateBackup(t *testing.T) {
	t.Parallel()
	ctx, main, _ := testkit.SweepRepo(t)
	testkit.BranchWithWork(t, main, "backup/2026-11-01/redo", 1)
	testkit.LandOnMain(t, main, "backup/2026-11-01/redo")
	path := filepath.Join(t.TempDir(), "redo")
	testkit.GitIn(t, main, "worktree", "add", "-q", path, "backup/2026-11-01/redo")
	p := PlanOf(t, ctx)
	if len(p.Remove) != 0 || !slices.Equal(branchNames(p.CheckedOut), []string{"backup/2026-11-01/redo"}) ||
		!slices.Equal(p.CheckedOut[0].KeptCodes, []string{"deliberateBackup"}) {
		t.Fatalf("remove %v, kept %+v", WorktreeNames(p.Remove), p.CheckedOut)
	}
	var buf bytes.Buffer
	if err := Sweep(ctx, SweepOptions{Yes: true, Agents: []agents.Agent{}}, &buf); err != nil {
		t.Fatalf("Sweep: %v\n%s", err, buf.String())
	}
	if !testkit.Exists(path) || !ctx.Repo.BranchExists("backup/2026-11-01/redo") {
		t.Errorf("worktree and branch must stay:\n%s", buf.String())
	}
}
