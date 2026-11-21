package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/gittest"
)

// These tests drive the built binary: a run in the background, and
// wt sync wait following it.

var (
	builtOnce sync.Once
	builtPath string
	builtErr  error
)

// builtWt is the wt binary, built once for the package.
func builtWt(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		// Under the suite's own temporary directory, which testkit.Main
		// removes when the tests end.
		dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-bin")
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G703: under the XDG_CONFIG_HOME testkit.Main made
			builtErr = err
			return
		}
		builtPath = filepath.Join(dir, "wt")
		out, err := exec.Command("go", "build", "-o", builtPath, ".").CombinedOutput()
		if err != nil {
			builtErr = &buildError{err, string(out)}
		}
	})
	if builtErr != nil {
		t.Fatal(builtErr)
	}
	return builtPath
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return "go build: " + e.err.Error() + "\n" + e.out }

// lockedBuffer is a buffer a running process writes while a test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// proc is a wt started in the background, its stdout and stderr in one pipe.
type proc struct {
	cmd  *exec.Cmd
	out  *lockedBuffer
	done chan struct{}
	code int
}

// childEnv is the environment of a wt run in a child process: the test's own
// gate directory, and the docker its steps need first on the PATH. A child's
// environment is its own, so the tests run side by side.
func childEnv(gate string, extra ...string) []string {
	env := append(os.Environ(), "WTGATE="+gate, "PATH="+filepath.Join(gate, "bin")+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return append(env, extra...)
}

func startWt(t *testing.T, dir string, gate string, args ...string) *proc {
	t.Helper()
	p := &proc{out: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd = exec.Command(builtWt(t), args...)
	p.cmd.Dir = dir
	p.cmd.Env = childEnv(gate)
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = p.cmd.Wait()
		p.code = p.cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	return p
}

// exit waits for the process to end and returns its exit code.
func (p *proc) exit(t *testing.T) int {
	t.Helper()
	select {
	case <-p.done:
		return p.code
	case <-time.After(5 * time.Minute):
		t.Fatalf("still running after 5m:\n%s", p.out.String())
		return -1
	}
}

// sees waits until the process has printed s.
func (p *proc) sees(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for !strings.Contains(p.out.String(), s) {
		select {
		case <-p.done:
			if strings.Contains(p.out.String(), s) {
				return
			}
			t.Fatalf("ended (%d) without printing %q:\n%s", p.code, s, p.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q after 5m:\n%s", s, p.out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wtOut runs wt to the end: its output (stdout and stderr in one) and code.
func wtOut(t *testing.T, dir string, gate string, args ...string) (string, int) {
	t.Helper()
	p := startWt(t, dir, gate, args...)
	code := p.exit(t)
	return p.out.String(), code
}

func open(t *testing.T, gate, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(gate, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

const waitCheck = `while [ -d "$WTGATE" ] && [ ! -e "$WTGATE/check" ]; do sleep 0.05; done`
const waitStep = `while [ -d "$WTGATE" ] && [ ! -e "$WTGATE/step" ]; do sleep 0.05; done; ./gen.sh`

// pacedFold is a FoldLine whose each_commit check and per-layer step wait for
// a file the test creates.
func pacedFold(t *testing.T) (dir, tmp, gate string) {
	dir, tmp, gate, _ = pacedFoldLine(t)
	return
}

func pacedFoldLine(t *testing.T) (dir, tmp, gate string, s *gittest.FoldLine) {
	t.Helper()
	gate = t.TempDir()
	synccmdtest.WriteFakeDocker(t, filepath.Join(gate, "bin"))
	yaml := gittest.FoldYAML("max-plus-patch")
	yaml = strings.Replace(yaml, "test ! -e broken.marker", waitCheck, 1)
	yaml = strings.Replace(yaml, "./gen.sh", waitStep, 1)
	_, s = synccmdtest.FoldLine(t, yaml)
	// f3 holds uncommitted work, which holds the layer back: not what these
	// tests are about.
	gittest.Git(t, s.Paths["f3"], "checkout", "--", ".")
	return s.Dir, gittest.FoldTmp, gate, s
}

func logsOf(t *testing.T, dir string) []string {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(dir, ".git", "wt-sync-logs", "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	return logs
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gitDirOfWork(t *testing.T, work string) string {
	t.Helper()
	return gittest.Git(t, work, "rev-parse", "--absolute-git-dir")
}

var runArgs = []string{"sync", "rebase", "--stack", "--yes", "--no-push", "--no-fetch"}

// The issue's case: a fold run in the background, paced by the test, and
// wt sync wait showing what the run prints as it goes, then its exit code.
func TestSyncWaitFollowsALongRunAndExitsWithItsCode(t *testing.T) {
	t.Parallel()
	dir, tmp, gate := pacedFold(t)
	run := startWt(t, dir, gate, append(append([]string{}, runArgs...), tmp)...)
	run.sees(t, "▸ check")

	wait := startWt(t, dir, gate, "sync", "wait", tmp)
	wait.sees(t, "▸ check")
	if strings.Contains(wait.out.String(), "▸ step") {
		t.Fatalf("step line before the check was let go:\n%s", wait.out.String())
	}
	open(t, gate, "check")
	wait.sees(t, "▸ step")
	open(t, gate, "step")
	if code := wait.exit(t); code != 0 {
		t.Fatalf("wait exited %d:\n%s", code, wait.out.String())
	}
	if code := run.exit(t); code != 0 {
		t.Fatalf("run exited %d:\n%s", code, run.out.String())
	}
	logs := logsOf(t, dir)
	if len(logs) != 1 {
		t.Fatalf("logs %v", logs)
	}
	if got, want := read(t, logs[0]), run.out.String(); got != want {
		t.Errorf("log is not what the run printed\nlog:\n%s\nrun:\n%s", got, want)
	}
	if !strings.Contains(wait.out.String(), strings.TrimSpace(read(t, logs[0]))) {
		t.Errorf("wait did not print the whole log:\n%s", wait.out.String())
	}
	if end := strings.TrimSpace(read(t, logs[0]+".end")); end != "0" {
		t.Errorf("end file %q", end)
	}
}

// A look and a refusal before any lock leave no log and no pointer.
func TestSyncLookAndRefusalWriteNoLog(t *testing.T) {
	t.Parallel()
	dir, tmp, gate := pacedFold(t)
	if _, code := wtOut(t, dir, gate, "sync", tmp); code != 0 {
		t.Fatalf("look exited %d", code)
	}
	if out, code := wtOut(t, dir, gate, "sync", "rebase", "no-such-work", "--yes", "--no-push"); code == 0 {
		t.Fatalf("a refusal exited 0:\n%s", out)
	}
	if logs := logsOf(t, dir); len(logs) != 0 {
		t.Errorf("logs %v", logs)
	}
	if _, err := os.Stat(filepath.Join(gitDirOfWork(t, dir), "wt-sync-run.json")); err == nil {
		t.Error("a pointer was written")
	}
}

// wt sync wait of a worktree no run was seen in says so, and exits 1.
func TestSyncWaitWithNoRunSeen(t *testing.T) {
	t.Parallel()
	dir, tmp, gate := pacedFold(t)
	out, code := wtOut(t, dir, gate, "sync", "wait", tmp)
	if code != 1 || !strings.Contains(out, "no run has been seen") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// --timeout bounds the wait: 124, the run untouched, and waiting again
// reaches the end.
func TestSyncWaitTimeoutLeavesTheRunAlone(t *testing.T) {
	t.Parallel()
	dir, tmp, gate := pacedFold(t)
	run := startWt(t, dir, gate, append(append([]string{}, runArgs...), tmp)...)
	run.sees(t, "▸ check")
	out, code := wtOut(t, dir, gate, "sync", "wait", tmp, "--timeout", "1s")
	if code != 124 || !strings.Contains(out, "still running after 1s; wt sync wait") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	open(t, gate, "check")
	open(t, gate, "step")
	if code := run.exit(t); code != 0 {
		t.Fatalf("run exited %d:\n%s", code, run.out.String())
	}
	if out, code := wtOut(t, dir, gate, "sync", "wait", tmp); code != 0 {
		t.Fatalf("wait after the end exited %d:\n%s", code, out)
	}
}

// pointerOf writes a pointer for a worktree by hand.
func writePointer(t *testing.T, gitDir, log string, pid int, start time.Time) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"log": log, "pid": pid, "start": start.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "wt-sync-run.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// deadPid is the pid of a process that has ended.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A pointer whose log never grows: --timeout ends the wait with 124.
// A pointer whose writer is gone with no end file: ended without saying how.
func TestSyncWaitOnAWriterThatIsGone(t *testing.T) {
	t.Parallel()
	dir, tmp, gate, s := pacedFoldLine(t)
	gd := gitDirOfWork(t, s.Paths[tmp])
	log := filepath.Join(gate, "x.log")
	if err := os.WriteFile(log, []byte("  ▸ check 1/9 abc1234 something\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writePointer(t, gd, log, deadPid(t), time.Now().Add(-time.Hour))
	out, code := wtOut(t, dir, gate, "sync", "wait", tmp)
	if code != 1 || !strings.Contains(out, "ended without saying how") || !strings.Contains(out, "wt sync doctor") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "▸ check 1/9") {
		t.Errorf("the log was not printed:\n%s", out)
	}

	// The writer is alive but the log never grows: the timeout ends it.
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	writePointer(t, gd, log, sleeper.Process.Pid, time.Now())
	out, code = wtOut(t, dir, gate, "sync", "wait", tmp, "--timeout", "1s")
	if code != 124 {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	// A completed run's pointer, and a newer run holding the lock that this
	// wait cannot follow: not the old run's 0.
	if err := os.WriteFile(log+".end", []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePointer(t, gd, log, deadPid(t), time.Now().Add(-time.Hour))
	lock := "pid=" + strconv.Itoa(sleeper.Process.Pid) + "\nstart=" + strconv.FormatInt(time.Now().Unix(), 10) + "\nowner=x\n"
	if err := os.WriteFile(filepath.Join(gd, "wt-sync.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = wtOut(t, dir, gate, "sync", "wait", tmp)
	if code != 1 || !strings.Contains(out, "newer run") || !strings.Contains(out, strconv.Itoa(sleeper.Process.Pid)) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// A run interrupted with SIGINT: the end file says 130, the recovery lines
// are in the log, and wait exits 130.
func TestSyncWaitAfterAnInterruptedRun(t *testing.T) {
	t.Parallel()
	dir, tmp, gate := pacedFold(t)
	run := startWt(t, dir, gate, append(append([]string{}, runArgs...), tmp)...)
	run.sees(t, "▸ check")
	if err := run.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if code := run.exit(t); code != 130 {
		t.Fatalf("run exited %d:\n%s", code, run.out.String())
	}
	logs := logsOf(t, dir)
	if len(logs) != 1 {
		t.Fatalf("logs %v", logs)
	}
	if end := strings.TrimSpace(read(t, logs[0]+".end")); end != "130" {
		t.Errorf("end file %q", end)
	}
	if got, want := read(t, logs[0]), run.out.String(); got != want {
		t.Errorf("log is not what the run printed\nlog:\n%s\nrun:\n%s", got, want)
	}
	out, code := wtOut(t, dir, gate, "sync", "wait", tmp)
	if code != 130 {
		t.Fatalf("wait exited %d:\n%s", code, out)
	}
}
