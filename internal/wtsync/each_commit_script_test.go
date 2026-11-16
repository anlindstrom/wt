package wtsync

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptRepo is a repository whose feature branch has three commits
// (src/x.txt, doc.md, src/y.txt) on a base that has no bin/check.sh, and
// whose trunk then declares one each_commit step running bin/check.sh over
// src/**, and adds the script when withScript.
func scriptRepo(t *testing.T, withScript bool) (dir, wt string, cfg *Config, logPath, base string) {
	t.Helper()
	dir = repoWith(t, map[string]string{"a.txt": "a\n", "src/x.txt": "x\n", "doc.md": "d\n"}, nil,
		[]map[string]string{{"src/x.txt": "x2\n"}, {"doc.md": "d2\n"}, {"src/y.txt": "y\n"}})
	base = gitIn(t, dir, "rev-parse", "feature~3")
	logPath = filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-script.log")
	gitIn(t, dir, "checkout", "-q", "main")
	if withScript {
		script := "#!/bin/sh\ngit log -1 --format=%s >> '" + logPath + "'\n"
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bin", "check.sh"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	yaml := "each_commit:\n  - run: bin/check.sh\n    paths: [\"src/**\"]\n"
	if err := os.WriteFile(filepath.Join(dir, ".wt-sync.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "declare")
	gitIn(t, dir, "remote", "add", "origin", dir)
	gitIn(t, dir, "fetch", "-q", "origin")
	gitIn(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	cfg, err := LoadFromTrunk(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	return dir, featureWorktree(t, dir).Path, cfg, logPath, base
}

// A rebase in place, onto the branch's own base, replays trees that predate
// the script trunk declares: the step is not run there and not failed with
// exit 127. It is recorded as not checked, said once, and the rebase
// finishes with the branch untouched but for the replay.
func TestEachCommitNeverStopsOnAScriptTheTreePredates(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, logPath, base := scriptRepo(t, true)
	old := gitIn(t, wt, "rev-parse", "HEAD")
	req := trunkReq(wt, 61)
	req.Onto = base
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, req, &log)
	if err != nil || res.Left != nil || res.Restored || res.CheckFailed != nil {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if res.Checked != 0 {
		t.Errorf("checked %d commits with a script that is not there", res.Checked)
	}
	if len(res.Unchecked) != 1 || res.Unchecked[0].Step != "bin/check.sh" ||
		!strings.Contains(res.Unchecked[0].Why, "bin/check.sh is not in the tree at ") {
		t.Fatalf("unchecked %+v", res.Unchecked)
	}
	if n := strings.Count(log.String(), "not checked"); n < 1 {
		t.Errorf("the progress does not say the step was not checked:\n%s", log.String())
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("the script ran")
	}
	if got := gitIn(t, wt, "rev-parse", "HEAD"); got != old {
		t.Errorf("an in-place replay moved HEAD from %s to %s", old, got)
	}
}

// A script on trunk that the branch's tree has, the usual case, still runs
// and still stops a failing commit: only a missing script is skipped.
func TestEachCommitStillRunsAScriptTheTreeHas(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, logPath, _ := scriptRepo(t, true)
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, trunkReq(wt, 62), &log)
	if err != nil || res.Left != nil || res.Restored {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if res.Checked != 2 || len(res.Unchecked) != 0 {
		t.Errorf("checked %d, unchecked %+v, want 2 and none", res.Checked, res.Unchecked)
	}
	if got := checkedSubjects(t, logPath); len(got) != 2 {
		t.Errorf("the script ran at %v", got)
	}
}

// A script that is in neither the replayed tree nor trunk is a declaration
// error: it is said once, as not checked, and owed.
func TestEachCommitScriptInNeitherTreeNorTrunkIsADeclarationError(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, _, base := scriptRepo(t, false)
	req := trunkReq(wt, 63)
	req.Onto = base
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, req, &log)
	if err != nil || res.Left != nil || res.Restored {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if len(res.Unchecked) != 1 || !strings.Contains(res.Unchecked[0].Why, "in neither the tree nor trunk") {
		t.Fatalf("unchecked %+v", res.Unchecked)
	}
	if n := strings.Count(log.String(), "✗ declaration:"); n != 1 {
		t.Errorf("the declaration error is said %d times, want once:\n%s", n, log.String())
	}
}

// A tool the worktree has but git does not track (a provisioned
// .venv/bin/pytest, node_modules/.bin/tsc) is there: the step runs, in an
// ordinary rebase as in a fold in place.
func TestEachCommitRunsAnUntrackedExecutableTheWorktreeHas(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, logPath, base := scriptRepo(t, false)
	script := "#!/bin/sh\ngit log -1 --format=%s >> '" + logPath + "'\n"
	if err := os.MkdirAll(filepath.Join(wt, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "bin", "check.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	req := trunkReq(wt, 64)
	req.Onto = base
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, req, &log)
	if err != nil || res.Left != nil || res.Restored {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if res.Checked != 1 || len(res.Unchecked) != 0 {
		t.Errorf("checked %d, unchecked %+v, want 1 and none (the first pick fast-forwards)\n%s", res.Checked, res.Unchecked, log.String())
	}
	if got := checkedSubjects(t, logPath); len(got) != 1 {
		t.Errorf("the tool ran at %v", got)
	}
}

// A step is judged when its turn comes: an earlier step that generates an
// executable (untracked, as checks may) makes a later step that runs it
// runnable, not skipped.
func TestEachCommitJudgesAScriptWhenItsStepRuns(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, logPath := retryRepo(t, "each_commit:\n"+
		"  - run: mkdir -p out && echo '#!/bin/sh' > out/check && echo 'echo ran >> LOG' >> out/check && chmod +x out/check\n"+
		"  - run: out/check\n")
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, trunkReq(wt, 65), &log)
	if err != nil || res.Left != nil || res.Restored {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if len(res.Unchecked) != 0 {
		t.Errorf("unchecked %+v\n%s", res.Unchecked, log.String())
	}
	if got := logLines(t, logPath); len(got) != 3 {
		t.Errorf("the generated tool ran %d times, want 3\n%s", len(got), log.String())
	}
}
