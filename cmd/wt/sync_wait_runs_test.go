package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// twoBranches is a repository with two branches of two commits each, each in
// a worktree, and trunk moved on since they forked: a plain run moves both.
func twoBranches(t *testing.T, yaml string) (dir string, paths map[string]string) {
	t.Helper()
	dir = testkit.CommittedRepo(t, testkit.MinimalConf)
	gittest.Git(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	commit := func(name string) {
		gittest.WriteFile(t, filepath.Join(dir, name), name+"\n")
		gittest.Git(t, dir, "add", "-A")
		gittest.Git(t, dir, "commit", "-q", "--no-verify", "-m", "feat: "+name)
	}
	gittest.WriteFile(t, filepath.Join(dir, ".wt-sync.yaml"), yaml)
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "--no-verify", "-m", "chore: declare")
	paths = map[string]string{}
	for _, b := range []string{"feat/a", "feat/b"} {
		gittest.Git(t, dir, "checkout", "-q", "-b", b)
		commit(strings.TrimPrefix(b, "feat/") + "1.txt")
		commit(strings.TrimPrefix(b, "feat/") + "2.txt")
		gittest.Git(t, dir, "checkout", "-q", "main")
	}
	commit("trunk.txt")
	gittest.Git(t, dir, "remote", "add", "origin", dir)
	gittest.Git(t, dir, "fetch", "-q", "origin")
	for _, b := range []string{"feat/a", "feat/b"} {
		paths[b] = filepath.Join(wtDir, strings.ReplaceAll(b, "/", "_"))
		gittest.Git(t, dir, "worktree", "add", "-q", paths[b], b)
	}
	return dir, paths
}

const failCheck = "each_commit:\n  - run: test ! -e \"$WTGATE/fail\"\n"

// split is a run to its end with stdout and stderr apart.
func split(t *testing.T, dir, gate string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(builtWt(t), args...)
	cmd.Dir = dir
	cmd.Env = childEnv(gate)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
	}
	return o.String(), e.String(), cmd.ProcessState.ExitCode()
}

// A run that hands over at a failing check: wait shows the handover and
// exits 1 pointing at resume, and the resume is followed the same way to 0
// through a log and a pointer of its own. The final error line is in the
// log, and on stderr once.
func TestSyncWaitAfterAHandoverAndItsResume(t *testing.T) {
	t.Parallel()
	dir, paths := twoBranches(t, failCheck)
	gate := t.TempDir()
	open(t, gate, "fail")
	stdout, stderr, code := split(t, dir, gate, "sync", "rebase", "feat/a", "--yes", "--no-push", "--no-fetch")
	if code != 1 {
		t.Fatalf("run exited %d:\n%s%s", code, stdout, stderr)
	}
	logs := logsOf(t, dir)
	if len(logs) != 1 {
		t.Fatalf("logs %v", logs)
	}
	log := read(t, logs[0])
	if want := stdout + stderr; len(log) != len(want) {
		t.Errorf("log is not what the run printed\nlog:\n%s\nrun:\n%s", log, want)
	}
	line := "wt: " // the error line Execute prints
	if n := strings.Count(stderr, line); n != 1 {
		t.Fatalf("stderr has %d error lines:\n%s", n, stderr)
	}
	errLine := stderr[strings.Index(stderr, line):]
	if !strings.HasSuffix(log, errLine) {
		t.Errorf("the log does not end with the error line %q:\n%s", errLine, log)
	}

	out, code := wtOut(t, dir, gate, "sync", "wait", "feat/a")
	if code != 1 || !strings.Contains(out, "wt sync resume feat/a") || !strings.Contains(out, "wt-sync-plan.md") {
		t.Fatalf("wait exited %d:\n%s", code, out)
	}

	if err := os.Remove(filepath.Join(gate, "fail")); err != nil {
		t.Fatal(err)
	}
	if out, code := wtOut(t, dir, gate, "sync", "resume", "feat/a", "--yes", "--no-push"); code != 0 {
		t.Fatalf("resume exited %d:\n%s", code, out)
	}
	if len(logsOf(t, dir)) != 2 {
		t.Fatalf("the resume wrote no log of its own: %v", logsOf(t, dir))
	}
	out, code = wtOut(t, dir, gate, "sync", "wait", "feat/a")
	if code != 0 {
		t.Fatalf("wait after the resume exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "wt sync resume") {
		t.Errorf("the resume's wait still points at resume:\n%s", out)
	}
	_ = paths
}

// A run of two worktrees is one log; each worktree's pointer names it, and a
// wait on either ends with the command's code.
func TestSyncWaitOnEachWorktreeOfOneRun(t *testing.T) {
	t.Parallel()
	dir, paths := twoBranches(t, failCheck)
	gate := t.TempDir()
	if out, code := wtOut(t, dir, gate, "sync", "rebase", "feat/a", "feat/b", "--yes", "--no-push", "--no-fetch"); code != 0 {
		t.Fatalf("run exited %d:\n%s", code, out)
	}
	logs := logsOf(t, dir)
	if len(logs) != 1 {
		t.Fatalf("logs %v", logs)
	}
	for b, path := range paths {
		raw, err := os.ReadFile(filepath.Join(gitDirOfWork(t, path), "wt-sync-run.json"))
		if err != nil {
			t.Fatal(err)
		}
		var p struct{ Log string }
		if err := json.Unmarshal(raw, &p); err != nil || p.Log != logs[0] {
			t.Errorf("%s: pointer %s, want %s (%v)", b, raw, logs[0], err)
		}
		if out, code := wtOut(t, dir, gate, "sync", "wait", b); code != 0 {
			t.Errorf("wait %s exited %d:\n%s", b, code, out)
		}
	}
}

// A log that cannot be written leaves the run as it is: it finishes, moves
// what it moves and prints what it prints, plus one warning; a wait then
// finds no run.
func TestARunWhoseLogCannotBeWrittenFinishesAsBefore(t *testing.T) {
	t.Parallel()
	dir, paths := twoBranches(t, failCheck)
	gate := t.TempDir()
	gittest.WriteFile(t, filepath.Join(dir, ".git", "wt-sync-logs"), "in the way\n")
	stdout, stderr, code := split(t, dir, gate, "sync", "rebase", "feat/a", "--yes", "--no-push", "--no-fetch")
	if code != 0 {
		t.Fatalf("run exited %d:\n%s%s", code, stdout, stderr)
	}
	if n := strings.Count(stdout+stderr, "could not write the run log"); n != 1 {
		t.Errorf("%d warnings:\n%s%s", n, stdout, stderr)
	}
	trunk := gittest.Git(t, dir, "rev-parse", "main")
	if base := gittest.Git(t, dir, "merge-base", "feat/a", "main"); base != trunk {
		t.Errorf("feat/a was not rebased: merge base %s, trunk %s", base, trunk)
	}
	if _, err := os.Stat(filepath.Join(gitDirOfWork(t, paths["feat/a"]), "wt-sync-run.json")); err == nil {
		t.Error("a pointer was written")
	}
	if out, code := wtOut(t, dir, gate, "sync", "wait", "feat/a"); code != 1 || !strings.Contains(out, "no run has been seen") {
		t.Errorf("wait exited %d:\n%s", code, out)
	}
}

// With --json the result object stays on stdout, alone, and the log holds
// exactly what went to stderr.
func TestTheRunLogOfAJSONRunHoldsStderrOnly(t *testing.T) {
	t.Parallel()
	dir, _ := twoBranches(t, failCheck)
	gate := t.TempDir()
	stdout, stderr, code := split(t, dir, gate, "sync", "rebase", "feat/a", "--json", "--yes", "--no-push", "--no-fetch")
	if code != 0 {
		t.Fatalf("run exited %d:\n%s%s", code, stdout, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	logs := logsOf(t, dir)
	if len(logs) != 1 {
		t.Fatalf("logs %v", logs)
	}
	if got := read(t, logs[0]); got != stderr {
		t.Errorf("log is not stderr\nlog:\n%s\nstderr:\n%s", got, stderr)
	}
}

// A run killed outright, as a crash would leave it, writes no end file: a
// wait says it ended without saying how.
func TestSyncWaitOnARunThatWasKilled(t *testing.T) {
	t.Parallel()
	dir, tmp, gate := pacedFold(t)
	run := startWt(t, dir, gate, append(append([]string{}, runArgs...), tmp)...)
	run.sees(t, "▸ check")
	// The check script it leaves behind ends once its gates are open.
	t.Cleanup(func() { open(t, gate, "check"); open(t, gate, "step") })
	if err := run.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	run.exit(t)
	logs := logsOf(t, dir)
	if len(logs) != 1 {
		t.Fatalf("logs %v", logs)
	}
	if _, err := os.Stat(logs[0] + ".end"); err == nil {
		t.Fatal("an end file")
	}
	out, code := wtOut(t, dir, gate, "sync", "wait", tmp)
	if code != 1 || !strings.Contains(out, "ended without saying how") {
		t.Errorf("wait exited %d:\n%s", code, out)
	}
}

// A rebase across repositories is not logged: no log, no pointer, and a wait
// on a worktree it moved finds no run.
func TestARebaseAcrossRepositoriesWritesNoLog(t *testing.T) {
	t.Parallel()
	dir, paths := twoBranches(t, failCheck)
	gate := t.TempDir()
	cmd := exec.Command(builtWt(t), "sync", "rebase", "--all", "--yes", "--no-push", "--no-fetch")
	cmd.Dir = dir
	cmd.Env = childEnv(gate, "WT_ROOTS="+filepath.Dir(dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else if !strings.Contains(string(out), "feat/a") {
		t.Fatalf("the run did not reach the repository:\n%s", out)
	}
	if logs := logsOf(t, dir); len(logs) != 0 {
		t.Errorf("logs %v", logs)
	}
	if _, err := os.Stat(filepath.Join(gitDirOfWork(t, paths["feat/a"]), "wt-sync-run.json")); err == nil {
		t.Error("a pointer was written")
	}
}
