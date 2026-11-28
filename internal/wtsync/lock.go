package wtsync

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anlindstrom/wt/internal/git"
)

// LockName is the file a run holds in the worktree's own git dir
// (.git/worktrees/<name>/), so a second run, or the watcher, sees it.
const LockName = "wt-sync.lock"

// LockExpiry is how long a lock is believed while its process is gone. A run
// that died without releasing must not block its worktree forever. A lock
// whose process still runs never expires (see LockLive): a long fold holds
// its worktree for as long as it runs.
const LockExpiry = 30 * time.Minute

// Lock is a held or observed lock.
type Lock struct {
	Path    string
	PID     int
	Started time.Time
	Owner   string
	// kept is set by Keep and read by Release, both under held's mutex. A
	// kept lock is the run's deliberate leftover, so Release must leave the
	// file alone however the caller reaches it — a deferred release two
	// call frames up included.
	kept bool
}

// held is every lock this process has acquired and not yet released. A
// signal handler runs on its own goroutine with no access to the caller's
// bookkeeping, so the locks are tracked here instead of being threaded
// through every caller down to it.
var held = struct {
	sync.Mutex
	locks map[string]*Lock
}{locks: map[string]*Lock{}}

// HeldLocks is every lock this process currently holds, in no order.
func HeldLocks() []*Lock {
	held.Lock()
	defer held.Unlock()
	out := make([]*Lock, 0, len(held.locks))
	for _, l := range held.locks {
		out = append(out, l)
	}
	return out
}

// LockHeld is the error for a live lock held by someone else. Running is set
// when the lock is older than LockExpiry and held only because its process
// still runs.
type LockHeld struct {
	Lock
	Running bool
}

func (e *LockHeld) Error() string {
	msg := fmt.Sprintf("locked by pid %d (%s) since %s", e.PID, e.Owner, e.Started.Format(time.RFC3339))
	if e.Running {
		msg += "; that process is still running, and stopping it frees the worktree"
	}
	return msg
}

// probeProcess reads one process: whether it exists, and when it started when
// that can be read. Tests replace it.
var probeProcess = osProbe

// osProbe asks the kernel whether pid exists (signal 0: success, or EPERM for
// a process of another user) and ps when it started. ps -o lstart= prints the
// start, asked for in UTC, whole seconds, in the same fixed layout on macOS and
// Linux (procps); a sysctl or /proc would each serve one of the two only.
func osProbe(pid int) (exists bool, started time.Time, startKnown bool) {
	err := syscall.Kill(pid, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return false, time.Time{}, false
	case err != nil && !errors.Is(err, syscall.EPERM):
		return true, time.Time{}, false
	}
	out, perr := psStart(pid)
	if perr != nil {
		// ps exits non-zero for a pid that went since the signal.
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return false, time.Time{}, false
		}
		return true, time.Time{}, false
	}
	t, ok := parsePSStart(out)
	return true, t, ok
}

// psCommand is the ps call that reads a process's start. Tests replace it.
var psCommand = func(pid int) *exec.Cmd {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	// ps prints local time with no offset, and an hour that occurs twice
	// when the clocks go back cannot be told apart: ask for UTC.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "TZ=UTC")
	return cmd
}

// psTimeout bounds that call.
var psTimeout = 5 * time.Second

// psStart runs ps under psTimeout: it runs under the git dir's flock, which a
// hung ps would hold against every Acquire, TakeOver, Release and doctor. A
// timeout is an error, so the start counts as unknown.
func psStart(pid int) (string, error) {
	cmd := psCommand(pid)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	timedOut, _, err := git.RunBounded(psTimeout, cmd)
	if timedOut {
		return "", errors.New("ps timed out")
	}
	return stdout.String(), err
}

// parsePSStart reads what ps -o lstart= printed under TZ=UTC.
func parsePSStart(out string) (time.Time, bool) {
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(out), " "), time.UTC)
	return t, err == nil
}

// ProcessRunning reports whether a process with pid that started no later
// than notAfter still runs on this machine. A process that started after
// notAfter reused the pid and is somebody else. Whatever cannot be told (the
// start time unreadable) counts as running: the cost is a refused run, never
// two runs at once. notAfter is whole seconds in a lock, and ps's start is
// too, so a second of slack is allowed.
func ProcessRunning(pid int, notAfter time.Time) bool {
	if pid <= 0 {
		return false
	}
	exists, started, known := probeProcess(pid)
	if !exists {
		return false
	}
	return !known || !started.After(notAfter.Add(time.Second))
}

// LockLive reports whether the lock still holds its worktree at now: it is
// younger than LockExpiry, or its process still runs, whatever its age. A
// lock that names no process is never probed and keeps the age-only rule.
func LockLive(l *Lock, now time.Time) bool {
	if now.Sub(l.Started) < LockExpiry {
		return true
	}
	return ProcessRunning(l.PID, l.Started)
}

// lockHeld is the LockHeld for a lock LockLive found live at now.
func lockHeld(l *Lock, now time.Time) *LockHeld {
	return &LockHeld{Lock: *l, Running: now.Sub(l.Started) >= LockExpiry}
}

// Held is the LockHeld Acquire would refuse with for the worktree whose git
// dir is gitDir, or why its lock cannot be read; nil when none holds it.
// Nothing is written.
func Held(gitDir string, now time.Time) error {
	l, ok, err := ReadLock(gitDir)
	if err != nil || !ok || !LockLive(l, now) {
		return err
	}
	return lockHeld(l, now)
}

// GitDir is the worktree's own git dir, absolute.
func GitDir(wtPath string) (string, error) {
	return gitEnv(wtPath, nil, nil, "rev-parse", "--absolute-git-dir")
}

// afterExpiredRead runs once Acquire has found the lock expired and before it
// moves it aside. Tests set it to stand another contender in that gap.
var afterExpiredRead func()

// Acquire takes the lock atomically: the content is written to a private
// temp file and hard-linked into place, so a contender never sees a
// half-written lock. An existing lock younger than LockExpiry is respected;
// an older one is broken (see breakLock) and the link tried again.
func Acquire(gitDir string, now time.Time) (*Lock, error) {
	path := filepath.Join(gitDir, LockName)
	owner := "?"
	if u, err := user.Current(); err == nil {
		owner = u.Username
	}
	// Started is truncated to the second the file records. Release compares
	// it against what ReadLock parses back, so keeping the caller's
	// nanoseconds here would make every release a silent no-op and leave
	// the lock behind for LockExpiry.
	l := &Lock{Path: path, PID: os.Getpid(), Started: time.Unix(now.Unix(), 0), Owner: owner}
	tmp := fmt.Sprintf("%s.%d", path, l.PID)
	body := fmt.Sprintf("pid=%d\nstart=%d\nowner=%s\n", l.PID, now.Unix(), owner)
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return nil, err
	}
	defer os.Remove(tmp) //nolint:errcheck // best effort: the temp file is ours alone
	for attempt := 0; attempt < 2; attempt++ {
		err := os.Link(tmp, path)
		if err == nil {
			held.Lock()
			held.locks[path] = l
			held.Unlock()
			return l, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		existing, ok, rerr := ReadLock(gitDir)
		if rerr != nil {
			return nil, rerr
		}
		if !ok {
			// Gone between the link and the read: link again.
			continue
		}
		if LockLive(existing, now) {
			return nil, lockHeld(existing, now)
		}
		if afterExpiredRead != nil {
			afterExpiredRead()
		}
		// Expired. Another contender may have broken it since the read and
		// linked a fresh one, which breakLock judges again and leaves; the
		// next link then finds it.
		if _, err := breakLock(gitDir, func(cur *Lock) bool { return !LockLive(cur, now) }); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not acquire %s", path)
}

// Keep detaches the lock from this process without removing the file: a run
// that leaves a worktree mid-rebase leaves its lock behind, so a second run
// does not start where somebody has to finish first. It is not a forever
// lock — LockExpiry still frees it — and the sidecar, not this, is the
// durable marker that a run is waiting.
//
// A kept lock is also proof against Release: a caller that keeps a lock and
// then releases it, or that keeps one under a defer it did not write, must
// not silently delete the file the next run has to respect.
func (l *Lock) Keep() {
	held.Lock()
	delete(held.locks, l.Path)
	l.kept = true
	held.Unlock()
}

// TakeOver acquires the lock, displacing the one a run left behind when it
// handed a stop over. It tries an ordinary Acquire first and only displaces
// a lock whose pid and start time are exactly what the run recorded in its
// sidecar, so a live run, or any lock that is not this handover's, is
// respected the way Acquire respects it.
func TakeOver(gitDir string, now time.Time, prev LeftLock) (*Lock, error) {
	l, err := Acquire(gitDir, now)
	if err == nil {
		return l, nil
	}
	var busy *LockHeld
	if !errors.As(err, &busy) || prev.PID == 0 || busy.PID != prev.PID || busy.Started.Unix() != prev.Started {
		return nil, err
	}
	if _, err := breakLock(gitDir, func(cur *Lock) bool {
		return cur.PID == prev.PID && cur.Started.Unix() == prev.Started
	}); err != nil {
		return nil, err
	}
	return Acquire(gitDir, now)
}

// breakLock moves the lock aside when may says it can go. It reads the lock
// again and moves it while holding an exclusive flock on the git dir, which
// every move or removal of a lock somebody else may hold takes: the lock
// judged is then the lock moved, never a fresh one a contender linked in
// between. Linking a new lock needs no flock, since a link cannot replace a
// lock that is there. The kernel drops the flock with its process, so a
// contender that dies here leaves nothing behind.
func breakLock(gitDir string, may func(*Lock) bool) (broke bool, err error) {
	unlock, err := lockGitDir(gitDir)
	if errors.Is(err, os.ErrNotExist) {
		// The worktree went, and its git dir and lock with it.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unlock()
	cur, ok, err := ReadLock(gitDir)
	if err != nil || !ok || !may(cur) {
		return false, err
	}
	stale := fmt.Sprintf("%s.stale.%d", cur.Path, os.Getpid())
	if err := os.Rename(cur.Path, stale); err != nil {
		return false, err
	}
	_ = os.Remove(stale)
	return true, nil
}

// lockGitDir waits for the flock breakLock and Release hold on the git dir,
// and returns the func that lets it go. Each holds it only for a read and a
// rename or removal.
func lockGitDir(gitDir string) (func(), error) {
	f, err := os.Open(gitDir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // an fd fits an int
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // as above
		_ = f.Close()
	}, nil
}

// Release removes the lock, but only while it is still this acquisition's: a
// displaced owner must not delete its replacement's lock. PID alone is not
// enough to tell the two apart when the same process re-acquires its own
// expired lock, so Started (set once, at Acquire) is compared too. A lock
// Keep was called on is never removed.
func (l *Lock) Release() error {
	held.Lock()
	if held.locks[l.Path] == l {
		delete(held.locks, l.Path)
	}
	kept := l.kept
	held.Unlock()
	if kept {
		return nil
	}
	unlock, err := lockGitDir(filepath.Dir(l.Path))
	if errors.Is(err, os.ErrNotExist) {
		// The worktree went, and its git dir and lock with it.
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	cur, ok, err := ReadLock(filepath.Dir(l.Path))
	if err != nil {
		return err
	}
	if !ok || cur.PID != l.PID || !cur.Started.Equal(l.Started) {
		return nil
	}
	err = os.Remove(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ReadLock parses an existing lock file; ok is false when there is none.
func ReadLock(gitDir string) (*Lock, bool, error) {
	path := filepath.Join(gitDir, LockName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	l := &Lock{Path: path}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "pid":
			l.PID, _ = strconv.Atoi(v)
		case "start":
			sec, _ := strconv.ParseInt(v, 10, 64)
			l.Started = time.Unix(sec, 0)
		case "owner":
			l.Owner = v
		}
	}
	return l, true, nil
}
