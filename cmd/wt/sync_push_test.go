package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// --forget never asks, so --yes has nothing to confirm and is accepted;
// --push-line is about a push and is refused.
func TestSyncPushForgetTakesYesAndRefusesPushLine(t *testing.T) {
	main, _ := testkit.RepoWithWorktree(t, func(parent string) string { return filepath.Join(parent, "thing") })
	t.Chdir(main)
	out, err := runCmd(t, "sync", "push", "feat_wt/thing", "--stack", "--forget", "--yes")
	if err != nil || !strings.Contains(out, "nothing recorded, nothing to drop") {
		t.Errorf("--forget --yes: %v, want the forget path\n%s", err, out)
	}
	_, err = runCmd(t, "sync", "push", "feat_wt/thing", "--stack", "--forget", "--push-line")
	if err == nil || err.Error() != "--forget pushes nothing: it does not take --push-line" {
		t.Errorf("--forget --push-line: %v, want the flag conflict", err)
	}
}
