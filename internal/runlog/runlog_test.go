package runlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/anlindstrom/wt/internal/colour"
)

func newLog(t *testing.T) (*Log, *bytes.Buffer, string, string) {
	t.Helper()
	common := t.TempDir()
	gitDir := filepath.Join(common, "worktrees", "w")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	return &Log{warn: &warn, start: time.Now(), pid: os.Getpid()}, &warn, common, gitDir
}

// What was printed before the first lock is in the log, in order with what
// follows, and the end file holds the code.
func TestLogHoldsEverythingFromBeforeTheFirstLock(t *testing.T) {
	l, _, common, gitDir := newLog(t)
	var out bytes.Buffer
	w := l.Wrap(&out)
	_, _ = w.Write([]byte("before\n"))
	l.Locked(common, gitDir)
	_, _ = w.Write([]byte("after\n"))
	l.End(3)
	l.End(0) // the first end counts

	p, ok, err := ReadPointer(gitDir)
	if err != nil || !ok {
		t.Fatalf("pointer: %v %v", ok, err)
	}
	b, err := os.ReadFile(p.Log)
	if err != nil || string(b) != "before\nafter\n" {
		t.Errorf("log %q, %v", b, err)
	}
	if out.String() != "before\nafter\n" {
		t.Errorf("the person's output %q", out.String())
	}
	if code, ok, err := ReadEnd(p.Log); err != nil || !ok || code != 3 {
		t.Errorf("end %d %v %v", code, ok, err)
	}
}

// A run that takes no lock writes no log, and no end file.
func TestNoLockNoLog(t *testing.T) {
	l, _, common, _ := newLog(t)
	var out bytes.Buffer
	_, _ = l.Wrap(&out).Write([]byte("a look\n"))
	l.End(0)
	if _, err := os.Stat(filepath.Join(common, LogsDir)); err == nil {
		t.Error("a log directory was made")
	}
}

// A log that cannot be opened warns once, and the output is not touched.
func TestAnUnwritableLogWarnsOnce(t *testing.T) {
	l, warn, common, gitDir := newLog(t)
	if err := os.WriteFile(filepath.Join(common, LogsDir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := l.Wrap(&out)
	_, _ = w.Write([]byte("one\n"))
	l.Locked(common, gitDir)
	_, _ = w.Write([]byte("two\n"))
	l.Locked(common, gitDir)
	l.End(0)
	if out.String() != "one\ntwo\n" {
		t.Errorf("output %q", out.String())
	}
	if n := strings.Count(warn.String(), "could not write the run log"); n != 1 {
		t.Errorf("%d warnings: %q", n, warn.String())
	}
}

// A pointer that cannot be replaced is removed, so a wait does not follow an
// older run.
func TestAPointerThatCannotBeReplacedIsRemoved(t *testing.T) {
	l, warn, common, gitDir := newLog(t)
	if err := os.WriteFile(filepath.Join(gitDir, PointerName), []byte(`{"log":"/old","pid":1,"start":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The temporary file's name is taken by a directory.
	if err := os.Mkdir(filepath.Join(gitDir, PointerName+"."+strconv.Itoa(os.Getpid())+".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	l.Locked(common, gitDir)
	if _, ok, _ := ReadPointer(gitDir); ok {
		t.Error("the old pointer is still there")
	}
	if !strings.Contains(warn.String(), "wt sync wait cannot follow this run") {
		t.Errorf("no warning: %q", warn.String())
	}
}

// Logs older than a week whose writer is gone are removed with their end
// files when a log is created; younger ones stay.
func TestHousekeepingRemovesOldLogsOfEndedWriters(t *testing.T) {
	l, _, common, gitDir := newLog(t)
	dir := filepath.Join(common, LogsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	mk := func(name string, mod time.Time) string {
		p := filepath.Join(dir, name)
		for _, f := range []string{p, p + ".end"} {
			if err := os.WriteFile(f, []byte("0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(f, mod, mod); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	gone := mk("20200101T000000Z-999999.log", old)
	young := mk("20200101T000000Z-999998.log", time.Now())
	// A writer that still runs keeps its log however old it is: this one.
	running := mk(time.Now().UTC().Format("20060102T150405Z")+"-"+strconv.Itoa(os.Getpid())+".log", old)
	l.Locked(common, gitDir)
	for _, p := range []string{gone, gone + ".end"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was kept", p)
		}
	}
	for _, p := range []string{young, young + ".end", running} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
}

// Wrapping a terminal does not change whether it gets colour: the wrapper
// shows the terminal's descriptor, and a pipe is still a pipe.
func TestWrappingATerminalKeepsItsColourDecision(t *testing.T) {
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_ENTRYPOINT", "CODEX_THREAD_ID",
		"CODEX_SANDBOX", "CODEX_CI", "CURSOR_AGENT", "OPENCODE", "AGENT", "AI_AGENT", "CI", "NO_COLOR"} {
		t.Setenv(name, "")
	}
	t.Setenv("TERM", "xterm")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open a pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	l, _, _, _ := newLog(t)
	if !colour.Enabled(slave, "auto") {
		t.Fatal("control: a bare pseudo-terminal gets no colour")
	}
	if !colour.Enabled(l.Wrap(slave), "auto") {
		t.Error("a wrapped terminal lost its colour")
	}
	if colour.Enabled(l.Wrap(&bytes.Buffer{}), "auto") {
		t.Error("a wrapped buffer got colour")
	}
	t.Setenv("NO_COLOR", "1")
	if colour.Enabled(l.Wrap(slave), "auto") {
		t.Error("a wrapped terminal ignored NO_COLOR")
	}
}
