package keepstate

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// When launchctl fails with an error (not "service not found"), KeeperCheck
// must report that launchd could not be queried instead of claiming the keeper
// is not loaded.
func TestKeeperCheckReportsLaunchctlErrorInsteadOfNotLoaded(t *testing.T) {
	t.Parallel()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Machine.GOOS = "darwin"
	ctx.Machine.Home = t.TempDir()
	fake := filepath.Join(t.TempDir(), "launchctl")
	gittest.WriteExecutable(t, fake, "#!/bin/sh\necho 'launchd unreachable' >&2\nexit 2\n")
	ctx.Machine.Launchctl = fake

	c := KeeperCheck(ctx, time.Now())
	if c.OK {
		t.Fatalf("KeeperCheck returned OK=true on launchctl error: %+v", c)
	}
	if !strings.Contains(c.Detail, "launchd") {
		t.Fatalf("detail %q does not mention launchd error", c.Detail)
	}
}

// A launchctl that hangs is given up on, and said to have given no answer.
func TestKeeperCheckGivesUpOnAHangingLaunchctl(t *testing.T) {
	t.Parallel()
	ctx, err := kit.Open(testkit.CommittedRepo(t, testkit.MinimalConf), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Machine.GOOS = "darwin"
	ctx.Machine.Home = t.TempDir()
	fake := filepath.Join(t.TempDir(), "launchctl")
	gittest.WriteExecutable(t, fake, "#!/bin/sh\nsleep 30\n")
	ctx.Machine.Launchctl = fake

	start := time.Now()
	c := KeeperCheck(ctx, start)
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("KeeperCheck waited %s for a hanging launchctl", took)
	}
	if c.OK || !strings.Contains(c.Detail, "did not answer within") {
		t.Errorf("KeeperCheck = %+v, want not OK, saying launchctl did not answer", c)
	}
}
