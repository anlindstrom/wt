package wtsync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // Europe/Stockholm loads on any runner, so the clock-change tests always run

	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/repo"
)

// stubProbe replaces the process probe for one test. Not for parallel tests.
func stubProbe(t *testing.T, f func(pid int) (bool, time.Time, bool)) {
	t.Helper()
	old := probeProcess
	probeProcess = f
	t.Cleanup(func() { probeProcess = old })
}

func writeLockFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, LockName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lockBytes(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, LockName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOldLockOfARunningProcessIsRefusedAndLeftAlone(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now()
	if _, err := Acquire(dir, t0); err != nil { // this process, started before the lock
		t.Fatal(err)
	}
	before := lockBytes(t, dir)
	_, err := Acquire(dir, t0.Add(LockExpiry+time.Hour))
	var held *LockHeld
	if !errors.As(err, &held) || held.PID != os.Getpid() {
		t.Fatalf("Acquire = %v, want LockHeld for this pid", err)
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Fatalf("refusal %q does not say the process is still running", err)
	}
	if after := lockBytes(t, dir); after != before {
		t.Fatalf("lock changed: %q -> %q", before, after)
	}
}

func TestOldLockOfAGoneProcessIsBrokenAsToday(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLockFile(t, dir, "pid="+itoa(gittest.DeadPID(t))+"\nstart="+itoa(int(now.Add(-time.Hour).Unix()))+"\nowner=x\n")
	l, err := Acquire(dir, now)
	if err != nil {
		t.Fatalf("lock of a gone process was not broken: %v", err)
	}
	_ = l.Release()
}

func TestOldLockOfAReusedPidIsGone(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	lockStart := now.Add(-time.Hour)
	stubProbe(t, func(int) (bool, time.Time, bool) { return true, lockStart.Add(10 * time.Minute), true })
	writeLockFile(t, dir, "pid=4242\nstart="+itoa(int(lockStart.Unix()))+"\nowner=x\n")
	l, err := Acquire(dir, now)
	if err != nil {
		t.Fatalf("a pid that started after the lock was treated as its writer: %v", err)
	}
	_ = l.Release()
}

func TestOldLockWhoseProcessStartCannotBeReadIsRunning(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stubProbe(t, func(int) (bool, time.Time, bool) { return true, time.Time{}, false })
	writeLockFile(t, dir, "pid=4242\nstart="+itoa(int(now.Add(-time.Hour).Unix()))+"\nowner=x\n")
	_, err := Acquire(dir, now)
	var held *LockHeld
	if !errors.As(err, &held) {
		t.Fatalf("Acquire = %v, want LockHeld", err)
	}
}

func TestYoungLockIsNotProbed(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stubProbe(t, func(int) (bool, time.Time, bool) { t.Fatal("probed a young lock"); return false, time.Time{}, false })
	writeLockFile(t, dir, "pid=4242\nstart="+itoa(int(now.Add(-time.Minute).Unix()))+"\nowner=x\n")
	var held *LockHeld
	if _, err := Acquire(dir, now); !errors.As(err, &held) {
		t.Fatalf("Acquire = %v, want LockHeld", err)
	}
}

func TestOldLockNamingNoProcessExpiresWithoutAProbe(t *testing.T) {
	for _, body := range []string{"start=%d\nowner=x\n", "pid=0\nstart=%d\nowner=x\n", "pid=-3\nstart=%d\n", "pid=abc\nstart=%d\n"} {
		dir := t.TempDir()
		now := time.Now()
		stubProbe(t, func(int) (bool, time.Time, bool) {
			t.Fatal("probed a lock that names no process")
			return true, time.Time{}, true
		})
		writeLockFile(t, dir, strings.Replace(body, "%d", itoa(int(now.Add(-time.Hour).Unix())), 1))
		l, err := Acquire(dir, now)
		if err != nil {
			t.Fatalf("%q: not expired: %v", body, err)
		}
		_ = l.Release()
	}
}

func TestTakeOverDisplacesAnOldKeptLockOfALivingProcess(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now()
	l, err := Acquire(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	l.Keep()
	got, err := TakeOver(dir, t0.Add(LockExpiry+time.Hour), LeftLock{PID: l.PID, Started: l.Started.Unix()})
	if err != nil {
		t.Fatalf("TakeOver = %v", err)
	}
	_ = got.Release()
}

func doctorLocks(t *testing.T, now time.Time, lockAt time.Time, pid int) (Check, string) {
	t.Helper()
	dir, wt, _ := runRepo(t, nil, nil)
	gitDir, err := GitDir(wt)
	if err != nil {
		t.Fatal(err)
	}
	writeLockFile(t, gitDir, "pid="+itoa(pid)+"\nstart="+itoa(int(lockAt.Unix()))+"\nowner=x\n")
	checks, err := Doctor(dir, "main", []repo.Worktree{{Path: wt, Branch: "feature"}}, DoctorOptions{Now: now, Keep: 30 * 24 * time.Hour, Docker: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return findCheck(t, checks, "locks"), gitDir
}

func TestDoctorNamesAnOldLockOfARunningProcessLiveWithNoFix(t *testing.T) {
	t0 := time.Now()
	c, gitDir := doctorLocks(t, t0.Add(LockExpiry+time.Hour), t0, os.Getpid())
	if c.OK || c.Fix != nil {
		t.Fatalf("locks check OK=%v fix=%v, want a finding with no fix", c.OK, c.Fix != nil)
	}
	if !strings.Contains(c.Detail, "live") || !strings.Contains(c.Detail, "still running") || strings.Contains(c.Detail, "expired") {
		t.Fatalf("detail %q", c.Detail)
	}
	if _, ok, _ := ReadLock(gitDir); !ok {
		t.Fatal("lock gone")
	}
}

func TestDoctorFixRemovesAnOldLockOfAGoneProcess(t *testing.T) {
	now := time.Now()
	c, gitDir := doctorLocks(t, now, now.Add(-time.Hour), gittest.DeadPID(t))
	if c.Fix == nil || !strings.Contains(c.Detail, "expired") {
		t.Fatalf("check %+v", c)
	}
	if err := c.Fix(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadLock(gitDir); ok {
		t.Fatal("lock still present")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// ps prints local time with no offset, and an hour that occurs twice when the
// clocks go back cannot be told apart, so the probe asks ps for UTC.
func TestPSStartIsReadAndAskedForInUTC(t *testing.T) {
	// Not parallel: sets time.Local.
	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatalf("load Europe/Stockholm: %v", err)
	}
	old := time.Local
	time.Local = stockholm
	t.Cleanup(func() { time.Local = old })

	if !slices.Contains(psCommand(1).Env, "TZ=UTC") {
		t.Error("ps is not asked for UTC: its environment lacks TZ=UTC")
	}
	got, ok := parsePSStart("Sun Oct 25  0:15:00 2026")
	if want := time.Date(2026, 10, 25, 0, 15, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Fatalf("parsePSStart = %v %v, want %v", got, ok, want)
	}
}

// A process started at 02:15 CEST and the lock it wrote at 02:15:30 CEST, on
// the day the clocks go back: it is running, not a reused pid.
func TestProcessStartedInTheRepeatedHourIsRunning(t *testing.T) {
	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatalf("load Europe/Stockholm: %v", err)
	}
	old := time.Local
	time.Local = stockholm
	t.Cleanup(func() { time.Local = old })
	// The first 02:15:30, in CEST: time.Date would resolve the ambiguous
	// Stockholm time to the second one, in CET, an hour later.
	lockStart := time.Date(2026, 10, 25, 0, 15, 30, 0, time.UTC)
	started, ok := parsePSStart("Sun Oct 25  0:15:00 2026") // ps's UTC for 02:15:00 CEST
	if !ok {
		t.Fatal("unparsed")
	}
	stubProbe(t, func(int) (bool, time.Time, bool) { return true, started, true })
	if !ProcessRunning(4242, lockStart) {
		t.Fatalf("a process started at %v was judged to postdate a lock written at %v", started, lockStart)
	}
}

func TestAStuckPSCountsAsRunningAndReturnsWithinTheBound(t *testing.T) {
	oldCmd, oldTimeout := psCommand, psTimeout
	psCommand = func(int) *exec.Cmd { return exec.Command("sleep", "30") }
	psTimeout = 200 * time.Millisecond
	t.Cleanup(func() { psCommand, psTimeout = oldCmd, oldTimeout })

	began := time.Now()
	if !ProcessRunning(os.Getpid(), time.Now()) {
		t.Fatal("a ps that timed out was judged to mean the process is gone")
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("ProcessRunning took %v with ps stuck", took)
	}
}
