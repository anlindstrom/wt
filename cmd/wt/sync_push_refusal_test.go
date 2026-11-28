package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A push refused for what the recorded runs disagree on is said once on
// stdout and stderr together, exits 1, lists the branch under --dry-run and
// keeps the reason in --json.
func TestSyncPushSaysALeaseRefusalOnce(t *testing.T) {
	childMain()
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	bare := filepath.Join(t.TempDir(), "origin.git")
	testkit.GitIn(t, main, "init", "-q", "--bare", bare)
	testkit.GitIn(t, main, "push", "-q", bare, "refs/heads/s1:refs/heads/s1")
	testkit.GitIn(t, main, "remote", "set-url", "origin", bare)
	for epoch, lease := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		rec := &wtsync.PushRecord{Branch: gittest.StackTmp, Work: gittest.StackTmp, Epoch: int64(epoch + 1), Written: time.Now(), LayersRead: true,
			Items: []wtsync.PushRecordItem{{New: s.Tips["s1"], BranchPush: wtsync.BranchPush{Branch: "s1", Remote: "origin", RemoteBranch: "s1",
				Lease: lease, Endpoint: bare, Rule: "its own name"}}}}
		if _, err := wtsync.AddPushRun(main, rec); err != nil {
			t.Fatal(err)
		}
	}
	wt := func(args ...string) (stdout, stderr string, code int) {
		cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1") //nolint:gosec // G702: re-runs this test binary with its own test name
		cmd.Dir = main
		cmd.Env = append(os.Environ(), "WT_REFS_SIGNAL_ARGS="+strings.Join(args, "\x1f"))
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		var exit *exec.ExitError
		if err := cmd.Run(); errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), errOut.String(), code
	}
	const cause = "1 branch: the recorded runs' leases differ and the remote is not at the newest run's"

	out, errOut, code := wt("sync", "push", gittest.StackTmp, "--stack", "--yes")
	if both := out + errOut; code != 1 || strings.Count(both, "not pushed") != 1 || strings.Count(both, cause) != 1 {
		t.Errorf("exit %d, want 1 and the refusal once:\n%s", code, both)
	}
	out, errOut, code = wt("sync", "push", gittest.StackTmp, "--stack", "--dry-run")
	if both := out + errOut; code != 1 || !strings.Contains(both, "✗ s1: ") || strings.Count(both, cause) != 1 {
		t.Errorf("--dry-run: exit %d, want s1 listed and the refusal once:\n%s", code, both)
	}
	out, errOut, _ = wt("sync", "push", gittest.StackTmp, "--stack", "--yes", "--json")
	var res struct {
		Worktrees []struct {
			Reason *string `json:"reason"`
			Stack  *struct {
				Refusal *string `json:"refusal"`
				Push    *struct {
					Conflicted []struct{ Branch string } `json:"conflicted"`
				} `json:"push"`
			} `json:"stack"`
		} `json:"worktrees"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Worktrees) != 1 {
		t.Fatalf("--json: %v\n%s\n%s", err, out, errOut)
	}
	w := res.Worktrees[0]
	if w.Reason == nil || !strings.Contains(*w.Reason, cause) || w.Stack == nil || w.Stack.Refusal == nil || !strings.Contains(*w.Stack.Refusal, cause) ||
		w.Stack.Push == nil || len(w.Stack.Push.Conflicted) != 1 || w.Stack.Push.Conflicted[0].Branch != "s1" {
		t.Errorf("--json does not carry the refusal:\n%s", out)
	}
	if strings.Count(errOut, cause) != 1 {
		t.Errorf("--json: stderr says the refusal %d times:\n%s", strings.Count(errOut, cause), errOut)
	}
}
