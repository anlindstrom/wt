package synccmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/provision/provisiontest"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A fold that outlives wtsync.LockExpiry still holds its worktree: a second
// run, even one starting more than 30 minutes after the lock was written,
// is refused while the first run's process lives, and moves nothing.
func TestSyncRebaseRefusesAWorktreeWhoseRunsProcessStillLives(t *testing.T) {
	t.Parallel()
	ctx, bump := provisiontest.RunFixture(t, false)
	holder := exec.Command("sleep", "300")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })

	gitDir, err := wtsync.GitDir(bump)
	if err != nil {
		t.Fatal(err)
	}
	// Written after the process started, as a run writes its lock.
	started := time.Now()
	lock := "pid=" + strconv.Itoa(holder.Process.Pid) + "\nstart=" + strconv.FormatInt(started.Unix(), 10) + "\nowner=x\n"
	if err := os.WriteFile(filepath.Join(gitDir, wtsync.LockName), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	headBefore := testkit.GitOut(t, bump, "rev-parse", "HEAD")
	refsBefore := testkit.GitOut(t, bump, "for-each-ref")

	opts := synccmdtest.NoAgents()
	opts.Now = func() time.Time { return started.Add(wtsync.LockExpiry + 5*time.Minute) }
	var out testkit.LockedBuffer
	err = synccmd.SyncRebase(ctx, []string{"bump"}, opts, &out)
	got := out.String()
	if err != nil {
		got += err.Error()
	}
	if err == nil || !strings.Contains(got, "pid "+strconv.Itoa(holder.Process.Pid)) || !strings.Contains(got, "still running") {
		t.Fatalf("second run was not refused for the live process: err=%v\n%s", err, got)
	}
	if after := testkit.GitOut(t, bump, "rev-parse", "HEAD"); after != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, after)
	}
	if after := testkit.GitOut(t, bump, "for-each-ref"); after != refsBefore {
		t.Errorf("refs changed:\n%s\n->\n%s", refsBefore, after)
	}
	if b, _ := os.ReadFile(filepath.Join(gitDir, wtsync.LockName)); string(b) != lock {
		t.Errorf("lock changed: %q", b)
	}
}
