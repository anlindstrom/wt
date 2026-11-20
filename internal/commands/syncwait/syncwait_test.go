package syncwait

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/runlog"
)

// run is a worktree's git dir holding a pointer to a log.
func run(t *testing.T, pid int, start time.Time, log string) (gitDir string) {
	t.Helper()
	gitDir = t.TempDir()
	b, err := json.Marshal(runlog.Pointer{Log: log, PID: pid, Start: start.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, runlog.PointerName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return gitDir
}

func logFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type slowWriter struct{ delay time.Duration }

func (s slowWriter) Write(p []byte) (int, error) { time.Sleep(s.delay); return len(p), nil }

// follows runs follow and returns what it returned within d, or fails.
func follows(t *testing.T, d time.Duration, gitDir string, opts Options, w io.Writer) error {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- follow(gitDir, "w", "w", opts, time.Now(), w) }()
	select {
	case err := <-res:
		return err
	case <-time.After(d):
		t.Fatalf("wait did not end within %s", d)
		return nil
	}
}

func is124(err error) bool {
	var e kit.ExitError
	return errors.As(err, &e) && e.Code == TimeoutStatus
}

// A log that has a great deal to print, to a slow reader, still ends at the
// timeout.
func TestTheTimeoutBoundsDrainingALongLog(t *testing.T) {
	log := logFile(t, strings.Repeat("x", 8*1024*1024))
	gitDir := run(t, os.Getpid(), time.Now().Add(-time.Minute), log)
	opts := Options{Timeout: 300 * time.Millisecond, TimeoutLabel: "300ms", Poll: 10 * time.Millisecond,
		Running: func(int, time.Time) bool { return true }}
	if err := follows(t, 3*time.Second, gitDir, opts, slowWriter{20 * time.Millisecond}); !is124(err) {
		t.Errorf("got %v, want exit 124", err)
	}
}

// A probe that takes longer than the timeout does not hold the wait past it.
func TestTheTimeoutBoundsASlowProbe(t *testing.T) {
	log := logFile(t, "x\n")
	gitDir := run(t, os.Getpid(), time.Now().Add(-time.Minute), log)
	release := make(chan struct{})
	defer close(release)
	opts := Options{Timeout: 300 * time.Millisecond, TimeoutLabel: "300ms", Poll: 10 * time.Millisecond,
		Running: func(int, time.Time) bool { <-release; return true }}
	if err := follows(t, 3*time.Second, gitDir, opts, io.Discard); !is124(err) {
		t.Errorf("got %v, want exit 124", err)
	}
}

// A poll's sleep does not carry the wait past the timeout.
func TestTheTimeoutCapsThePollSleep(t *testing.T) {
	log := logFile(t, "x\n")
	gitDir := run(t, os.Getpid(), time.Now().Add(-time.Minute), log)
	opts := Options{Timeout: 200 * time.Millisecond, TimeoutLabel: "200ms", Poll: time.Hour,
		Running: func(int, time.Time) bool { return true }}
	if err := follows(t, 3*time.Second, gitDir, opts, io.Discard); !is124(err) {
		t.Errorf("got %v, want exit 124", err)
	}
}

// A lock with the pointer's pid, taken after the pointer's run began, held by
// a process that is not the pointer's: the pid was reused, so it is a newer
// run, never the old run's end.
func TestAReusedPidIsANewerRun(t *testing.T) {
	log := logFile(t, "x\n")
	if err := os.WriteFile(runlog.EndPath(log), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pid := 4242
	pointerStart := time.Now().Add(-time.Hour).Truncate(time.Second)
	gitDir := run(t, pid, pointerStart, log)
	lockStart := time.Now().Truncate(time.Second)
	body := "pid=" + strconv.Itoa(pid) + "\nstart=" + strconv.FormatInt(lockStart.Unix(), 10) + "\nowner=t\n"
	if err := os.WriteFile(filepath.Join(gitDir, "wt-sync.lock"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{Poll: 10 * time.Millisecond, Running: func(p int, notAfter time.Time) bool {
		// The process now named pid started after the pointer's run did.
		return p == pid && !notAfter.Before(lockStart)
	}}
	err := follows(t, 3*time.Second, gitDir, opts, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "a newer run") {
		t.Errorf("got %v, want a newer run", err)
	}
}

// The pointer's own run, still running, holds its own lock: not a newer run.
func TestTheWritersOwnLockIsNotANewerRun(t *testing.T) {
	log := logFile(t, "x\n")
	if err := os.WriteFile(runlog.EndPath(log), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pid := 4242
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	gitDir := run(t, pid, start, log)
	body := "pid=" + strconv.Itoa(pid) + "\nstart=" + strconv.FormatInt(start.Unix()+5, 10) + "\nowner=t\n"
	if err := os.WriteFile(filepath.Join(gitDir, "wt-sync.lock"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{Poll: 10 * time.Millisecond, Running: func(p int, _ time.Time) bool { return p == pid }}
	if err := follows(t, 3*time.Second, gitDir, opts, io.Discard); err != nil {
		t.Errorf("got %v, want the run's 0", err)
	}
}
