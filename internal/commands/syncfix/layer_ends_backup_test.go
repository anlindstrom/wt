package syncfix

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A stack run leaves the backup branches on the line where they were,
// whatever the folder they sit in, and still moves every real layer.
func TestStackRunLeavesBackupBranchesOnTheLineAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, _, tips := endsLine(t, "conflicts: []\n", []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}}},
	}, []endsCommit{{"feat: top", "top.txt", "top\n", ""}})
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, main, "branch", "backup/2026-11-11/x", tips["e1"])
	testkit.GitIn(t, main, "branch", "feat/x-backup", tips["e2"])
	res, human, err := synccmdtest.StackRun(t, ctx, endsTmp, synccmdtest.Onto("main", ""))
	if err != nil {
		t.Fatalf("err %v\n%s", err, human)
	}
	layers := stackLayers(synccmdtest.Participant(t, res, endsTmp).Stack)
	for _, b := range []string{"backup/2026-11-11/x", "feat/x-backup"} {
		if _, ok := layers[b]; ok {
			t.Errorf("%s was taken as a layer", b)
		}
	}
	for b, want := range map[string]string{"backup/2026-11-11/x": tips["e1"], "feat/x-backup": tips["e2"]} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != want {
			t.Errorf("%s moved to %s, want %s", b, got, want)
		}
	}
	for _, b := range []string{"e1", "e2"} {
		l := layers[b]
		if deref(l.Result) != wtsync.MoveMoved || testkit.GitOut(t, main, "rev-parse", b) != deref(l.NewTip) {
			t.Errorf("%s: %+v", b, l)
		}
	}
	if !strings.Contains(human, "backup/2026-11-11/x") || !strings.Contains(human, "feat/x-backup") {
		t.Errorf("the look does not list the backups:\n%s", human)
	}
}

// With the default user config a kept backup (backup/<date>/<what>) and a
// transient copy (safety/<branch>-pre-fold-<n>) on the line are both left as
// they were by `wt sync rebase --stack`, and by `wt sync undo`; the real
// layers move and come back.
func TestStackRunAndUndoLeaveKeptAndTransientBackupsAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories")
	}
	ctx, _, tips := endsLine(t, "conflicts: []\n", []endsLayer{
		{"e1", []endsCommit{{"feat(e1): a", "a.txt", "a\n", ""}}},
		{"e2", []endsCommit{{"feat(e2): b", "b.txt", "b\n", ""}}},
	}, []endsCommit{{"feat: top", "top.txt", "top\n", ""}})
	main := ctx.Repo.MainRoot
	backups := map[string]string{"backup/2026-11-01/huge-redo": tips["e1"], "safety/x-pre-fold-1": tips["e2"]}
	for b, tip := range backups {
		testkit.GitIn(t, main, "branch", b, tip)
	}
	res, human, err := synccmdtest.StackRun(t, ctx, endsTmp, synccmdtest.Onto("main", ""))
	if err != nil {
		t.Fatalf("err %v\n%s", err, human)
	}
	layers := stackLayers(synccmdtest.Participant(t, res, endsTmp).Stack)
	for b, want := range backups {
		if _, ok := layers[b]; ok {
			t.Errorf("%s was taken as a layer", b)
		}
		if got := testkit.GitOut(t, main, "rev-parse", b); got != want {
			t.Errorf("after the run %s is at %s, want %s", b, got, want)
		}
	}
	for _, b := range []string{"e1", "e2"} {
		if deref(layers[b].Result) != wtsync.MoveMoved {
			t.Errorf("%s: %+v", b, layers[b])
		}
	}
	var out bytes.Buffer
	if err := SyncUndo(ctx, endsTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	for b, want := range backups {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != want {
			t.Errorf("after undo %s is at %s, want %s", b, got, want)
		}
	}
	for _, b := range []string{"e1", "e2"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tips[b] {
			t.Errorf("undo left %s at %s, want %s", b, got, tips[b])
		}
	}
}
