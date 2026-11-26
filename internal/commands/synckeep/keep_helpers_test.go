package synckeep

import (
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synckeep/keepstate"
)

// onMachine gives ctx a machine of the test's own: the platform the
// keeper's start, stop and status see, whatever machine runs the test, an
// empty home, so that no plist of the person running it is found, and a
// fakeLaunchctl, whose recorded calls are in the file returned.
func onMachine(t *testing.T, ctx *kit.Context, goos string) (calls string) {
	t.Helper()
	ctx.Machine.GOOS, ctx.Machine.Home = goos, t.TempDir()
	ctx.Machine.Launchctl, calls = fakeLaunchctl(t)
	return calls
}

// keepFiles is the keeper's log and state file for the repository.
func keepFiles(t *testing.T, ctx *kit.Context) (log, state string) {
	t.Helper()
	_, log, state, err := keepstate.KeepPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return log, state
}

// readKeep is the keeper's state, failing the test when there is none.
func readKeep(t *testing.T, ctx *kit.Context) keepstate.KeepState {
	t.Helper()
	_, statePath := keepFiles(t, ctx)
	st, ok, err := keepstate.ReadKeepState(statePath)
	if err != nil || !ok {
		t.Fatalf("state file: ok %v err %v", ok, err)
	}
	return st
}
