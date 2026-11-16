package wtsync

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/gittest"
)

// retryRepo builds a repository whose trunk declares yaml (every LOG in it
// is replaced by a log file outside the repository) and whose feature
// branch has three commits: src/x.txt, doc.md, src/y.txt.
func retryRepo(t *testing.T, yaml string) (dir, wt string, cfg *Config, logPath string) {
	t.Helper()
	dir = repoWith(t, map[string]string{"a.txt": "a\n", "src/x.txt": "x\n", "doc.md": "d\n"},
		[]map[string]string{{"a.txt": "a2\n"}},
		[]map[string]string{{"src/x.txt": "x2\n"}, {"doc.md": "d2\n"}, {"src/y.txt": "y\n"}})
	logPath = filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-retry.log")
	gittestWrite(t, filepath.Join(dir, ".wt-sync.yaml"), strings.ReplaceAll(yaml, "LOG", "'"+logPath+"'"))
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "declare")
	gitIn(t, dir, "remote", "add", "origin", dir)
	gitIn(t, dir, "fetch", "-q", "origin")
	gitIn(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	cfg, err := LoadFromTrunk(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	return dir, featureWorktree(t, dir).Path, cfg, logPath
}

// assertCheckStop is the git state a failing check leaves, whatever the
// failure was: the rebase in progress at the first commit, the branch and
// the safety ref at the old tip. It returns the tracked-file status.
func assertCheckStop(t *testing.T, wt string, res Result, old string) string {
	t.Helper()
	if res.Left == nil || res.Left.Check == nil || res.Restored {
		t.Fatalf("no check handover: %+v", res)
	}
	if busy, _ := RebaseInProgress(wt); !busy {
		t.Error("the rebase is not left in progress")
	}
	if got := gitIn(t, wt, "log", "-1", "--format=%s"); got != "branch 1" {
		t.Errorf("HEAD is %q, not the failing commit", got)
	}
	if got := gitIn(t, wt, "rev-parse", "feature"); got != old {
		t.Errorf("the branch moved to %s from %s", got, old)
	}
	if got := gitIn(t, wt, "rev-parse", res.Safety.Ref); got != old {
		t.Errorf("safety ref %s at %s, want %s", res.Safety.Ref, got, old)
	}
	return gitIn(t, wt, "status", "--porcelain", "--untracked-files=no")
}

func logLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func TestEachCommitReadsRetry(t *testing.T) {
	t.Parallel()
	_, _, cfg, _ := retryRepo(t, "each_commit:\n  - run: make build\n    retry: make build --always-make\n  - run: make lint\n")
	if got := cfg.EachCommit[0].Retry; got != "make build --always-make" {
		t.Errorf("retry %q", got)
	}
	if got := cfg.EachCommit[1].Retry; got != "" {
		t.Errorf("a step with no retry has %q", got)
	}
}

// A run that fails and a retry that passes is a pass: the rebase goes on,
// the log says so, and the retry ran at every commit run failed at and only
// there, honouring paths.
func TestEachCommitRetryPassesTheCheckWhenRunFails(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, logPath := retryRepo(t,
		"each_commit:\n  - run: echo run >> LOG; exit 1\n    retry: echo retry >> LOG\n    paths: [\"src/**\"]\n")
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, trunkReq(wt, 31), &log)
	if err != nil || res.Left != nil || res.Restored || res.CheckFailed != nil {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if res.Checked != 2 {
		t.Errorf("checked %d, want 2 (doc.md is outside paths)", res.Checked)
	}
	if got, want := logLines(t, logPath), []string{"run", "retry", "run", "retry"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	if n := strings.Count(log.String(), "passed on retry"); n != 2 {
		t.Errorf("the log says passed on retry %d times, want 2:\n%s", n, log.String())
	}
	if busy, _ := RebaseInProgress(wt); busy {
		t.Error("the rebase is still in progress")
	}
}

// A run that passes never starts its retry.
func TestEachCommitRetryIsNotRunWhenRunPasses(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, logPath := retryRepo(t, "each_commit:\n  - run: echo run >> LOG\n    retry: echo retry >> LOG\n")
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, trunkReq(wt, 32), &log)
	if err != nil || res.Left != nil {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if got, want := logLines(t, logPath), []string{"run", "run", "run"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	if strings.Contains(log.String(), "retry") {
		t.Errorf("the log mentions a retry:\n%s", log.String())
	}
}

// Both failing stops exactly where a failing run alone stops: same commit,
// same HEAD, rebase in progress, branch untouched. The stop names both and
// shows the retry's output.
func TestEachCommitRetryFailingTooStopsLikeAFailingRun(t *testing.T) {
	t.Parallel()
	yaml := "each_commit:\n  - run: echo run-said-this; exit 3\n    paths: [\"src/**\"]\n"
	dirA, wtA, cfgA, _ := retryRepo(t, yaml)
	resA, err := Rebase(dirA, cfgA, trunkReq(wtA, 33), nil)
	if err != nil || resA.Left == nil || resA.Left.Check == nil {
		t.Fatalf("baseline: err %v res %+v", err, resA)
	}
	dirB, wtB, cfgB, _ := retryRepo(t, strings.Replace(yaml, "    paths", "    retry: echo retry-said-this; exit 4\n    paths", 1))
	oldB := gitIn(t, wtB, "rev-parse", "HEAD")
	var log bytes.Buffer
	resB, err := Rebase(dirB, cfgB, trunkReq(wtB, 33), &log)
	if err != nil || resB.Left == nil || resB.Left.Check == nil {
		t.Fatalf("err %v res %+v\n%s", err, resB, log.String())
	}
	c := resB.Left.Check
	if c.Subject != resA.Left.Check.Subject || resB.Left.Index != resA.Left.Index || resB.Left.Total != resA.Left.Total {
		t.Errorf("stopped at %q %d/%d, a failing run stops at %q %d/%d", c.Subject, resB.Left.Index, resB.Left.Total,
			resA.Left.Check.Subject, resA.Left.Index, resA.Left.Total)
	}
	if !strings.Contains(c.Err, "exit 3") || !strings.Contains(c.Err, "retry") || !strings.Contains(c.Err, "exit 4") {
		t.Errorf("the stop does not say run exit 3 and retry exit 4: %q", c.Err)
	}
	if !strings.Contains(c.Output, "retry-said-this") || strings.Contains(c.Output, "run-said-this") {
		t.Errorf("the stop shows %q, want the retry's output", c.Output)
	}
	if got := gitIn(t, wtB, "log", "-1", "--format=%s"); got != "branch 1" {
		t.Errorf("HEAD is %q", got)
	}
	if busy, _ := RebaseInProgress(wtB); !busy {
		t.Error("the rebase is not left in progress")
	}
	if got := gitIn(t, wtB, "rev-parse", "feature"); got != oldB {
		t.Errorf("the branch moved to %s from %s", got, oldB)
	}
	if status := gitIn(t, wtB, "status", "--porcelain", "--untracked-files=no"); status != "" {
		t.Errorf("tracked files are dirty: %s", status)
	}
	if line := CheckFailedLine("w", *c); !strings.Contains(line, "retry") {
		t.Errorf("the line a person reads omits the retry: %s", line)
	}
	if plan := renderCheckPlan(PlanInput{Work: "w", TrunkRef: "origin/main", Handover: Handover{Check: c}}); !strings.Contains(plan, "retry") {
		t.Errorf("the plan omits the retry:\n%s", plan)
	}
}

// A retry that changes a tracked file has failed, as a run does: the stop
// says so and the commit is not changed. A run that changed one is not given
// a retry on a dirty tree.
func TestEachCommitRetryMustNotChangeTrackedFiles(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, _ := retryRepo(t, "each_commit:\n  - run: exit 1\n    retry: echo retried >> LOG; echo touched > a.txt\n    paths: [\"src/**\"]\n")
	old := gitIn(t, wt, "rev-parse", "HEAD")
	res, err := Rebase(dir, cfg, trunkReq(wt, 34), nil)
	if err != nil {
		t.Fatal(err)
	}
	status := assertCheckStop(t, wt, res, old)
	if e := res.Left.Check.Err; !strings.Contains(e, "retry") || !strings.Contains(e, "changed tracked files: a.txt") {
		t.Errorf("err %q", e)
	}
	if committed := gitIn(t, wt, "show", "HEAD:a.txt"); committed == "touched" {
		t.Error("the retry's change reached the commit")
	}
	// The same file changed by a failing run alone leaves the same status.
	dirB, wtB, cfgB, _ := retryRepo(t, "each_commit:\n  - run: echo touched > a.txt; exit 1\n    paths: [\"src/**\"]\n")
	oldB := gitIn(t, wtB, "rev-parse", "HEAD")
	resB, err := Rebase(dirB, cfgB, trunkReq(wtB, 34), nil)
	if err != nil {
		t.Fatal(err)
	}
	if statusB := assertCheckStop(t, wtB, resB, oldB); statusB != status {
		t.Errorf("status %q after a retry that changed a file, %q after a run that did", status, statusB)
	}
}

// A run that changed a tracked file is not given a retry: the stop is its own.
func TestEachCommitRetryIsNotRunOnATreeRunChanged(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, logPath := retryRepo(t, "each_commit:\n  - run: echo touched > a.txt; exit 1\n    retry: echo retried >> LOG\n    paths: [\"src/**\"]\n")
	old := gitIn(t, wt, "rev-parse", "HEAD")
	res, err := Rebase(dir, cfg, trunkReq(wt, 35), nil)
	if err != nil {
		t.Fatal(err)
	}
	if status := assertCheckStop(t, wt, res, old); status != "M a.txt" && status != " M a.txt" {
		t.Errorf("status %q", status)
	}
	if got := logLines(t, logPath); len(got) != 0 {
		t.Errorf("retry ran on a tree run had changed: %v", got)
	}
	if res.Left.Check.Err != "exit 1" || res.Left.Check.Retried {
		t.Errorf("err %q retried %v", res.Left.Check.Err, res.Left.Check.Retried)
	}
}

// Untracked files a retry writes are left where they are, as for run: wt
// only guards tracked files.
func TestEachCommitRetryUntrackedFilesStayAsForRun(t *testing.T) {
	t.Parallel()
	dir, wt, cfg, _ := retryRepo(t, "each_commit:\n  - run: exit 1\n    retry: echo out > retry.out\n    paths: [\"src/**\"]\n")
	res, err := Rebase(dir, cfg, trunkReq(wt, 36), nil)
	if err != nil || res.Left != nil {
		t.Fatalf("err %v res %+v", err, res)
	}
	if got := gitIn(t, wt, "status", "--porcelain"); got != "?? retry.out" {
		t.Errorf("status %q, want only the untracked file", got)
	}
	if got := gitIn(t, wt, "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(got, "retry.out") {
		t.Error("retry.out is in the commit")
	}
}

// A retry that outlives the limit has failed: the stop is the one a failing
// run leaves, through the driver. Not parallel: it shortens the limit.
func TestEachCommitRetryTimingOutStopsLikeAFailingRun(t *testing.T) {
	old := EachCommitTimeout
	EachCommitTimeout = 500 * time.Millisecond
	t.Cleanup(func() { EachCommitTimeout = old })
	dir, wt, cfg, _ := retryRepo(t, "each_commit:\n  - run: exit 1\n    retry: sleep 30\n    paths: [\"src/**\"]\n")
	oldTip := gitIn(t, wt, "rev-parse", "HEAD")
	res, err := Rebase(dir, cfg, trunkReq(wt, 39), nil)
	if err != nil {
		t.Fatal(err)
	}
	if status := assertCheckStop(t, wt, res, oldTip); status != "" {
		t.Errorf("tracked changes left: %s", status)
	}
	if e := res.Left.Check.Err; !strings.Contains(e, "exit 1") || !strings.Contains(e, "timed out") {
		t.Errorf("err %q", e)
	}
}

// The retry has the time limit a check has: a retry that outlives it has
// failed, and run's failure is still named.
func TestRunEachCommitBoundsTheRetryByTheSameTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	start := time.Now()
	why, _, retried := runEachCommit(dir, EachCommit{Run: "exit 1", Retry: "sleep 30"}, 300*time.Millisecond)
	if !retried || !strings.Contains(why, "timed out") || !strings.Contains(why, "exit 1") {
		t.Errorf("why %q retried %v", why, retried)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

// The case of the issue: a stack held on one line, fixups folded in place,
// and a step whose run fails on a marker the previous check left behind
// (stale incremental output), which its retry clears. The run finishes with
// no stop, every layer lands, and nothing is left unsaid.
func TestRetryClearsStaleStateAcrossAStackOnOneLine(t *testing.T) {
	t.Parallel()
	s := stackLine(t, stackYAML+"each_commit:\n  - run: test ! -e stale.marker && touch stale.marker\n    retry: rm -f stale.marker && touch stale.marker\n")
	tmp := s.Paths[gittest.StackTmp]
	gittest.WriteFile(t, filepath.Join(tmp, "b1.txt"), "s2 b1.txt fixed\n")
	gitIn(t, tmp, "commit", "-q", "-am", "fixup! feat(s2): b1.txt")
	cfg, err := LoadFromTrunk(s.Dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Path: tmp, Branch: gittest.StackTmp, Trunk: "origin/main", Onto: line.Base, Epoch: 37,
		Stack: &StackRequest{Line: line, Autosquash: true}}
	var log bytes.Buffer
	res, err := Rebase(s.Dir, cfg, req, &log)
	if err != nil || res.Left != nil || res.Restored || res.CheckFailed != nil {
		t.Fatalf("err %v res %+v\n%s", err, res, log.String())
	}
	if res.Checked < 3 {
		t.Fatalf("checked %d:\n%s", res.Checked, log.String())
	}
	if n := strings.Count(log.String(), "passed on retry"); n != res.Checked-1 {
		t.Errorf("passed on retry %d times in %d checks (the first has no stale marker):\n%s", n, res.Checked, log.String())
	}
	if _, err := CheckLayers(s.Dir, res.NewTip, line.Base, res.Layers); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, tmp, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("tracked changes left: %s", got)
	}
}

// A fold whose recheck fails even after the retry is refused and rolled
// back: the layer's step is failed, not the rebase, and the branches, the
// layers' markers and the safety ref are where they were, and the worktree
// is clean. The step arms a marker in the git dir that the check, run at the
// folded commit, trips once (the replay, before any step, passes).
func TestFoldRecheckWhoseRetryFailsToo(t *testing.T) {
	t.Parallel()
	const arm = `"$(git rev-parse --git-dir)/ARM"`
	yaml := strings.Replace(gittest.FoldYAML("max-plus-patch"), "run: ./gen.sh", "run: ./gen.sh && touch "+arm, 1)
	yaml = strings.Replace(yaml, "run: test ! -e broken.marker",
		"run: 'if test -e "+arm+"; then rm "+arm+"; exit 1; fi'\n    retry: exit 7", 1)
	s := foldLine(t, yaml)
	// Every branch but the line's own, which a finished rebase moves.
	heads := func() string {
		return gitIn(t, s.Dir, "for-each-ref", "--exclude=refs/heads/"+gittest.FoldTmp, "refs/heads", "--format=%(refname) %(objectname)")
	}
	before := heads()
	old := gitIn(t, s.Paths[gittest.FoldTmp], "rev-parse", "HEAD")
	res, log := foldRebase(t, s, 38)
	if res.Left != nil || res.Restored {
		t.Fatalf("left %+v restored %v\n%s", res.Left, res.Restored, log)
	}
	f1 := stepsByBranch(res.LayerSteps)["f1"]
	if f1.Result != LayerFailed || f1.Err == nil || !strings.Contains(f1.Err.Error(), "retry") || !strings.Contains(f1.Err.Error(), "exit 7") {
		t.Fatalf("f1's step %+v\n%s", f1, log)
	}
	if after := heads(); after != before {
		t.Errorf("branches moved:\n%s\nwas\n%s", after, before)
	}
	if got := gitIn(t, s.Dir, "rev-parse", res.Safety.Ref); got != old || res.OldTip != old {
		t.Errorf("safety ref %s, old tip %s, want %s", got, res.OldTip, old)
	}
	if _, err := CheckLayers(s.Dir, res.NewTip, "origin/main", res.Layers); err != nil {
		t.Errorf("the layers are not consistent: %v", err)
	}
	if got := gitIn(t, s.Paths[gittest.FoldTmp], "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("tracked changes left: %s", got)
	}
	if got := gitIn(t, s.Dir, "rev-parse", gittest.FoldTmp); got != res.NewTip {
		t.Errorf("the line is at %s, not the new tip %s", got, res.NewTip)
	}
	if got := gitIn(t, s.Paths[gittest.FoldTmp], "rev-parse", "HEAD"); got != res.NewTip {
		t.Errorf("HEAD %s is not the new tip %s", got, res.NewTip)
	}
}
