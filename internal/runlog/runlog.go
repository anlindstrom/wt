// Package runlog keeps a copy of what one wt sync invocation prints, so that
// wt sync wait can follow a run it did not start.
//
// The log is anchored to the invocation, not to a worktree's lock: one file
// per invocation, written from the first lock it takes to the end of the
// command, with the exit code in a separate end file beside it. Each worktree
// whose lock the invocation takes gets a pointer to the log. The log is a
// copy: nothing it does may change what the run prints, decides or leaves
// behind, so every failure to write it is one warning and no more.
package runlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anlindstrom/wt/internal/wtsync"
)

const (
	// LogsDir is the directory of the repository's common git dir that holds
	// the logs.
	LogsDir = "wt-sync-logs"
	// PointerName is the file in a worktree's own git dir that names the log
	// of the latest run that took its lock.
	PointerName = "wt-sync-run.json"
	// KeepFor is how long a log stays once its writer is gone.
	KeepFor = 7 * 24 * time.Hour
)

// Now is the clock the log's names and pointers use. It is the real one: a
// pointer's start time is compared with the process table.
var Now = time.Now

// Pointer says which log a worktree's latest run writes: the file, the pid of
// the writing process and when it started (Unix seconds), so that a reused
// pid is not mistaken for it.
type Pointer struct {
	Log   string `json:"log"`
	PID   int    `json:"pid"`
	Start int64  `json:"start"`
}

// Started is when the writer started.
func (p Pointer) Started() time.Time { return time.Unix(p.Start, 0) }

// EndPath is the end file of the log at path.
func EndPath(log string) string { return log + ".end" }

// ReadPointer reads the pointer in a worktree's own git dir; ok is false when
// there is none.
func ReadPointer(gitDir string) (p Pointer, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(gitDir, PointerName)) //nolint:gosec // G304: a file wt wrote in the worktree's own git dir
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil || p.Log == "" {
		return Pointer{}, false, fmt.Errorf("%s is not a run pointer", filepath.Join(gitDir, PointerName))
	}
	return p, true, nil
}

// ReadEnd reads the exit code in the end file of the log at path; ok is
// false when the run has not written one.
func ReadEnd(log string) (code int, ok bool, err error) {
	data, err := os.ReadFile(EndPath(log)) //nolint:gosec // G304: beside a log wt wrote
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	code, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false, fmt.Errorf("%s holds no exit code", EndPath(log))
	}
	return code, true, nil
}

// Log is the copy of one invocation's output.
type Log struct {
	mu    sync.Mutex
	warn  io.Writer
	start time.Time
	pid   int

	buf     []byte // what was printed before the first lock
	f       *os.File
	path    string
	logOff  bool // the log cannot be written; stop trying
	logWarn bool
	ptrOff  bool
	ended   bool
	opened  bool
}

var (
	activeMu sync.Mutex
	active   *Log
)

// Begin starts the log of this invocation and makes it the active one. warn is
// where a failure to write is said, once.
func Begin(warn io.Writer) *Log {
	l := &Log{warn: warn, start: Now(), pid: os.Getpid()}
	activeMu.Lock()
	active = l
	activeMu.Unlock()
	return l
}

// Active is the log of this invocation, nil when there is none.
func Active() *Log {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}

// Wrap is w with everything written to it copied into the log. The person's
// output is w's, byte for byte: a failing copy never reaches the caller. A
// terminal file keeps its descriptor visible, so colour is still decided by
// the terminal.
func (l *Log) Wrap(w io.Writer) io.Writer {
	t := &tee{l: l, w: w}
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		return &fdTee{tee: t, fd: f.Fd}
	}
	return t
}

type tee struct {
	l *Log
	w io.Writer
}

func (t *tee) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.l.Copy(p[:n])
	}
	return n, err
}

type fdTee struct {
	*tee
	fd func() uintptr
}

// Fd is the wrapped terminal's descriptor.
func (t *fdTee) Fd() uintptr { return t.fd() }

// Copy appends p to the log: held in memory until the first lock opens the
// file.
func (l *Log) Copy(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	if l.f == nil {
		if !l.opened {
			l.buf = append(l.buf, p...)
		}
		return
	}
	l.writeLocked(p)
}

func (l *Log) writeLocked(p []byte) {
	if l.logOff {
		return
	}
	if _, err := l.f.Write(p); err != nil {
		l.failLog(err)
	}
}

func (l *Log) failLog(err error) {
	l.logOff = true
	l.warnOnce(&l.logWarn, err)
}

func (l *Log) warnOnce(flag *bool, err error) {
	if *flag || l.warn == nil {
		*flag = true
		return
	}
	*flag = true
	fmt.Fprintf(l.warn, "wt: could not write the run log (%v); wt sync wait cannot follow this run\n", err)
}

// Locked records that this invocation has taken the lock of the worktree whose
// own git dir is gitDir: the log is opened if this is the first lock, and the
// worktree's pointer names it. commonDir is the repository's common git dir.
func (l *Log) Locked(commonDir, gitDir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	if !l.opened {
		l.opened = true
		l.open(commonDir)
	}
	if l.path == "" || l.logOff {
		// No log to point at: an older pointer would send a wait to an older
		// run, so it goes.
		_ = os.Remove(filepath.Join(gitDir, PointerName))
		return
	}
	if err := l.writePointer(gitDir); err != nil {
		_ = os.Remove(filepath.Join(gitDir, PointerName))
		l.warnOnce(&l.ptrOff, err)
	}
}

func (l *Log) open(commonDir string) {
	dir := filepath.Join(commonDir, LogsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		l.failLog(err)
		l.buf = nil
		return
	}
	prune(dir, l.start)
	path := filepath.Join(dir, l.start.UTC().Format("20060102T150405Z")+"-"+strconv.Itoa(l.pid)+".log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644) //nolint:gosec // G304: in the repository's own git dir
	if err != nil {
		l.failLog(err)
		l.buf = nil
		return
	}
	l.f, l.path = f, path
	if len(l.buf) > 0 {
		l.writeLocked(l.buf)
	}
	l.buf = nil
}

func (l *Log) writePointer(gitDir string) error {
	b, err := json.Marshal(Pointer{Log: l.path, PID: l.pid, Start: l.start.Unix()})
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(gitDir, PointerName), append(b, '\n'))
}

// writeAtomic writes data to path by writing a temporary file beside it and
// renaming it into place.
func writeAtomic(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil { //nolint:gosec // G306: read by wt sync wait, in the worktree's own git dir
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// End records how the command ended: the exit code, in the end file beside
// the log. Only the first call counts. With no log (no lock was taken) it
// writes nothing.
func (l *Log) End(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	l.ended = true
	if l.f == nil {
		return
	}
	_ = l.f.Close()
	l.f = nil
	if l.logOff {
		return
	}
	if err := writeAtomic(EndPath(l.path), []byte(strconv.Itoa(code)+"\n")); err != nil {
		l.warnOnce(&l.logWarn, err)
	}
}

// prune removes logs older than KeepFor whose writer is not running, with
// their end files. A log's name carries the writer's pid.
func prune(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < KeepFor {
			continue
		}
		start, pid, ok := parseName(name)
		if ok && wtsync.ProcessRunning(pid, start) {
			continue
		}
		path := filepath.Join(dir, name)
		_ = os.Remove(path)
		_ = os.Remove(EndPath(path))
	}
}

func parseName(name string) (start time.Time, pid int, ok bool) {
	base := strings.TrimSuffix(name, ".log")
	ts, p, found := strings.Cut(base, "-")
	if !found {
		return start, 0, false
	}
	start, err := time.Parse("20060102T150405Z", ts)
	if err != nil {
		return start, 0, false
	}
	pid, err = strconv.Atoi(p)
	return start, pid, err == nil
}

// Locked is Log.Locked on the active log, and nothing without one.
func Locked(commonDir, gitDir string) {
	if l := Active(); l != nil {
		l.Locked(commonDir, gitDir)
	}
}

// EndActive is Log.End on the active log, and nothing without one.
func EndActive(code int) {
	if l := Active(); l != nil {
		l.End(code)
	}
}

// CopyActive is Log.Copy on the active log, and nothing without one.
func CopyActive(p []byte) {
	if l := Active(); l != nil {
		l.Copy(p)
	}
}
