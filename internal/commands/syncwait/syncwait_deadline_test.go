package syncwait

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/runlog"
)

// A deadline that passes while an old run's log is replayed still asks
// whether a newer run holds the worktree: the old run's end is not its code.
func TestATimeoutDoesNotReportAnOlderRunWhileANewerOneHoldsTheWorktree(t *testing.T) {
	log := logFile(t, strings.Repeat("x", 8*1024*1024))
	if err := os.WriteFile(runlog.EndPath(log), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pointerStart := time.Now().Add(-time.Hour).Truncate(time.Second)
	gitDir := run(t, 4242, pointerStart, log)
	newer := 5151
	lockStart := time.Now().Truncate(time.Second)
	body := "pid=" + strconv.Itoa(newer) + "\nstart=" + strconv.FormatInt(lockStart.Unix(), 10) + "\nowner=t\n"
	if err := os.WriteFile(filepath.Join(gitDir, "wt-sync.lock"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{Timeout: 200 * time.Millisecond, TimeoutLabel: "200ms", Poll: 10 * time.Millisecond,
		Resolve: func(string) (string, string, error) { return gitDir, "w", nil },
		Running: func(p int, _ time.Time) bool { return p == newer }}
	err := waits(t, 5*time.Second, "w", opts, slowWriter{20 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "a newer run") {
		t.Errorf("got %v (exit %d), want a newer run, not the older run's 0", err, exitCode(err))
	}
}

// A deadline that passes with an end file that holds no code says so, as a
// poll does, rather than calling the run still running.
func TestATimeoutReportsAnUnreadableEndFile(t *testing.T) {
	log := logFile(t, strings.Repeat("x", 8*1024*1024))
	if err := os.WriteFile(runlog.EndPath(log), []byte("not a code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir := run(t, 4242, time.Now().Add(-time.Hour), log)
	opts := Options{Timeout: 200 * time.Millisecond, TimeoutLabel: "200ms", Poll: 10 * time.Millisecond,
		Resolve: func(string) (string, string, error) { return gitDir, "w", nil },
		Running: func(int, time.Time) bool { return true }}
	err := waits(t, 5*time.Second, "w", opts, slowWriter{20 * time.Millisecond})
	if is124(err) || err == nil || !strings.Contains(err.Error(), "holds no exit code") {
		t.Errorf("got %v (exit %d), want the end file's error, not 124", err, exitCode(err))
	}
}
