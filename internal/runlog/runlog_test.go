package runlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	holds(t, gitDir)
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
	holds(t, gitDir)
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
	holds(t, gitDir)
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
	holds(t, gitDir)
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

// holds is a lock file as this process holds one.
func holds(t *testing.T, gitDir string) {
	t.Helper()
	body := "pid=" + strconv.Itoa(os.Getpid()) + "\nstart=" + strconv.FormatInt(time.Now().Unix(), 10) + "\nowner=t\n"
	if err := os.WriteFile(filepath.Join(gitDir, "wt-sync.lock"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func within(t *testing.T, what string, d time.Duration, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %s", what, d)
	}
}

// An interrupt that arrives while housekeeping is probing a process must still
// get its recovery output copied and the end written: no probe runs under the
// log's lock.
func TestAnInterruptDuringHousekeepingIsNotBlocked(t *testing.T) {
	l, _, common, gitDir := newLog(t)
	holds(t, gitDir)
	dir := filepath.Join(common, LogsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "20200101T000000Z-4242.log")
	if err := os.WriteFile(old, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-9 * 24 * time.Hour)
	if err := os.Chtimes(old, when, when); err != nil {
		t.Fatal(err)
	}
	probing, release := make(chan struct{}), make(chan struct{})
	prev := running
	running = func(int, time.Time) bool { close(probing); <-release; return false }
	t.Cleanup(func() { running = prev })
	defer close(release)

	go l.Locked(common, gitDir)
	<-probing
	var shown bytes.Buffer
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		_, _ = l.Wrap(&shown).Write([]byte("\ninterrupted\n"))
		l.End(130)
	}()
	within(t, "the interrupt", 3*time.Second, handled)
	p, ok, _ := ReadPointer(gitDir)
	if !ok {
		t.Fatal("no pointer")
	}
	if b, _ := os.ReadFile(p.Log); string(b) != "\ninterrupted\n" {
		t.Errorf("log %q", b)
	}
	if code, ok, _ := ReadEnd(p.Log); !ok || code != 130 {
		t.Errorf("end %d %v", code, ok)
	}
}

// blockingWriter holds the write of "A" until released and records what it
// was shown, in order.
type blockingWriter struct {
	mu      sync.Mutex
	shown   bytes.Buffer
	started chan struct{}
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.shown.Write(p)
	b.mu.Unlock()
	if string(p) == "A" {
		close(b.started)
		<-b.release
	}
	return len(p), nil
}

// Two writers: what the log holds is in the order the person was shown it,
// and a write shown before the end is in the log.
func TestTheCopyKeepsTheOrderShownAndLosesNothingBeforeTheEnd(t *testing.T) {
	l, _, common, gitDir := newLog(t)
	holds(t, gitDir)
	l.Locked(common, gitDir)
	bw := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	w := l.Wrap(bw)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); _, _ = w.Write([]byte("A")) }()
	<-bw.started
	go func() { defer wg.Done(); _, _ = w.Write([]byte("B")) }()
	go func() { defer wg.Done(); time.Sleep(50 * time.Millisecond); l.End(0) }()
	time.Sleep(150 * time.Millisecond)
	close(bw.release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	within(t, "the writers", 3*time.Second, done)

	p, _, _ := ReadPointer(gitDir)
	got, _ := os.ReadFile(p.Log)
	bw.mu.Lock()
	shown := bw.shown.String()
	bw.mu.Unlock()
	if !strings.HasPrefix(shown, "AB") {
		t.Fatalf("shown %q", shown)
	}
	// B may land after the end (and not be copied); A never may.
	if !strings.HasPrefix(string(got), "A") || (len(got) > 1 && string(got) != "AB") {
		t.Errorf("log %q, shown %q", got, shown)
	}
}

// A pointer write in flight when the signal path releases the locks is
// finished first, so a successor's pointer is never overwritten, and no
// pointer is written after that.
func TestAnInFlightPointerWriteIsNotOverwritingItsSuccessor(t *testing.T) {
	l, _, common, gitDir := newLog(t)
	holds(t, gitDir)
	inFlight, release := make(chan struct{}), make(chan struct{})
	prev := afterLockCheck
	var once sync.Once
	afterLockCheck = func() { once.Do(func() { close(inFlight); <-release }) }
	t.Cleanup(func() { afterLockCheck = prev })

	written := make(chan struct{})
	go func() { l.Locked(common, gitDir); close(written) }()
	<-inFlight
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		l.Quiesce() // the signal path, before it releases the locks
		// A successor takes the freed lock and writes its pointer.
		b := []byte(`{"log":"/successor.log","pid":1,"start":1}`)
		if err := os.WriteFile(filepath.Join(gitDir, PointerName), b, 0o644); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	within(t, "the write", 3*time.Second, written)
	within(t, "the handler", 3*time.Second, handled)
	p, ok, _ := ReadPointer(gitDir)
	if !ok || p.Log != "/successor.log" {
		t.Errorf("pointer %+v", p)
	}
	// Nothing more is written once quiesced.
	l.Locked(common, gitDir)
	if p, _, _ := ReadPointer(gitDir); p.Log != "/successor.log" {
		t.Errorf("a pointer was written after the quiesce: %+v", p)
	}
}

// A pointer is written only while the worktree's lock is this process's.
func TestNoPointerIsWrittenWithoutTheLock(t *testing.T) {
	l, _, common, gitDir := newLog(t)
	l.Locked(common, gitDir) // no lock file at all
	if _, ok, _ := ReadPointer(gitDir); ok {
		t.Error("a pointer was written without the lock")
	}
}

// On a signal the end is final: a write queued on the copy when the end is
// published never reaches the person, so nothing is shown that the log does
// not hold.
func TestNothingIsShownAfterTheEndOfAnInterruptedRun(t *testing.T) {
	l, _, common, gitDir := newLog(t)
	holds(t, gitDir)
	l.Locked(common, gitDir)
	bw := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	w := l.Wrap(bw)

	go func() { _, _ = w.Write([]byte("A")) }()
	<-bw.started
	ended := make(chan struct{})
	go func() { l.EndForExit(130); close(ended) }() // the handler, first in the queue
	time.Sleep(50 * time.Millisecond)
	go func() { _, _ = w.Write([]byte("B")) }() // the run's own write, behind it
	time.Sleep(50 * time.Millisecond)
	close(bw.release)
	within(t, "the end", 3*time.Second, ended)
	time.Sleep(200 * time.Millisecond) // the process would have exited by now

	p, _, _ := ReadPointer(gitDir)
	got, _ := os.ReadFile(p.Log)
	bw.mu.Lock()
	shown := bw.shown.String()
	bw.mu.Unlock()
	if string(got) != shown {
		t.Errorf("the log holds %q but %q was shown", got, shown)
	}
	if code, ok, _ := ReadEnd(p.Log); !ok || code != 130 {
		t.Errorf("end %d %v", code, ok)
	}
}
