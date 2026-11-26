package keepstate_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// keeperRecord is the sync.keeper record wt sync doctor gives on a Mac whose
// launchctl is the script.
func keeperRecord(t *testing.T, script string) kit.CheckRecord {
	t.Helper()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Machine.GOOS = "darwin"
	ctx.Machine.Home = t.TempDir()
	fake := filepath.Join(t.TempDir(), "launchctl")
	gittest.WriteExecutable(t, fake, script)
	ctx.Machine.Launchctl = fake

	f := kit.NewFindings(io.Discard)
	synccmd.RecordSync(ctx, f)
	for _, c := range f.Records() {
		if c.ID == "sync.keeper" {
			return c
		}
	}
	t.Fatalf("no sync.keeper record in %+v", f.Records())
	return kit.CheckRecord{}
}

// A launchctl that fails with anything but "no such service" leaves the keeper
// unknown, never not loaded.
func TestKeeperCheckReportsLaunchctlErrorInsteadOfNotLoaded(t *testing.T) {
	t.Parallel()
	c := keeperRecord(t, "#!/bin/sh\necho 'launchd unreachable' >&2\nexit 2\n")
	if c.Status != kit.CheckUnknown || !strings.Contains(c.Detail, "launchd unreachable") {
		t.Errorf("sync.keeper = %+v, want unknown, saying what launchctl said", c)
	}
}

// A launchctl that hangs is given up on, and the keeper is unknown.
func TestKeeperCheckGivesUpOnAHangingLaunchctl(t *testing.T) {
	t.Parallel()
	start := time.Now()
	c := keeperRecord(t, "#!/bin/sh\nsleep 30\n")
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("the doctor waited %s for a hanging launchctl", took)
	}
	if c.Status != kit.CheckUnknown || !strings.Contains(c.Detail, "did not answer within") {
		t.Errorf("sync.keeper = %+v, want unknown, saying launchctl did not answer", c)
	}
}
