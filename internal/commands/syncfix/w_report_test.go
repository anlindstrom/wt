package syncfix

import (
	"os"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// wt#211: an in-place fold of fixups that sit above the last layer, under
// --each-commit=targets with named layers and --no-side-branches, names none
// of the folded fixups after the rebase, in what it prints or in the JSON.
func TestInPlaceFoldOfFixupsAboveTheLastLayerNamesNoFoldedFixup(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s, logPath := synccmdtest.InPlaceCheckedLine(t, "")
	if err := os.WriteFile(logPath+".pass", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	foldFixups(t, s)
	testkit.WriteFile(t, tmp, "b1.txt", "fixed again\n")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
	wantTree := testkit.GitOut(t, main, "rev-parse", gittest.StackTmp+"^{tree}")

	opts := synccmdtest.NoAgents()
	opts.EachCommit = "targets"
	opts.Stack = &synccmd.StackOptions{Autosquash: true, NoSideBranches: true, Layers: s.Layers}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err != nil && !strings.Contains(err.Error(), "layers held") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if got := testkit.GitOut(t, main, "rev-parse", gittest.StackTmp+"^{tree}"); got != wantTree {
		t.Errorf("the fold changed the tip tree: %s, want %s", got, wantTree)
	}
	if left := testkit.GitOut(t, main, "log", "--format=%s", s.Base+".."+gittest.StackTmp); strings.Contains(left, "fixup!") {
		t.Errorf("a fixup is left:\n%s", left)
	}
	for _, bad := range []string{"not found after the rebase", "not confirmed folded", "dropped as empty: "} {
		for _, line := range strings.Split(human, "\n") {
			if strings.Contains(line, bad) && strings.Contains(line, "fixup!") {
				t.Errorf("a folded fixup is reported: %s", line)
			}
		}
	}
	for _, d := range synccmdtest.Participant(t, res, gittest.StackTmp).Emptied {
		if strings.HasPrefix(d.Subject, "fixup! ") {
			t.Errorf("emptied names a folded fixup: %+v", d)
		}
	}
}
