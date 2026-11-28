package synccmd_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// lineUnderTest is a StackLine wt opens.
type lineUnderTest struct {
	ctx *kit.Context
	s   *gittest.StackLine
}

// foldedLine is StackLine declaring yaml, with a fixup for layer 2's first
// commit.
func foldedLine(t *testing.T, yaml string) lineUnderTest {
	t.Helper()
	ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+yaml)
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	return lineUnderTest{ctx, s}
}

// A check sees none of the user's global configuration: no remote, no
// worktree and no hooks path of it.
func TestTargetsLookKeepsTheGlobalConfigOut(t *testing.T) {
	// Not parallel: sets GIT_CONFIG_GLOBAL.
	skipShort(t)
	l := foldedLine(t, "each_commit:\n  - run: |\n"+
		"      test -z \"$(git config --get-regexp '^(remote\\.|core\\.worktree)')\" && test \"$(git config core.hookspath)\" = /dev/null\n")
	global := filepath.Join(t.TempDir(), "gitconfig")
	testkit.WriteFile(t, filepath.Dir(global), "gitconfig", "[user]\n\tname = T\n\temail = t@example.com\n"+
		"[remote \"elsewhere\"]\n\turl = /nonexistent\n[core]\n\tworktree = "+l.s.Paths[gittest.StackTmp]+"\n\thooksPath = /nonexistent\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	if err != nil {
		t.Errorf("err %v, error %v, steps %q", err, o.Error, lookSteps(o))
	}
}

// A need of a variable the checks are not given does not answer, though the
// look itself has it.
func TestTargetsLookProbesAnEnvironmentNeedAsItsChecksSeeIt(t *testing.T) {
	// Not parallel: sets GIT_CONFIG_PARAMETERS.
	skipShort(t)
	l := foldedLine(t, "each_commit:\n  - run: 'true'\n    needs: ['env:GIT_CONFIG_PARAMETERS']\n")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'wt.test=1'")
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, 1)
	if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "env:GIT_CONFIG_PARAMETERS") {
		t.Errorf("fold %+v", o.Fold)
	}
}

// The merge a conflict strategy runs, in a directory of its own, is kept to
// the scratch as every other git of the replay is.
func TestTargetsLookKeepsAStrategysMergeToTheScratch(t *testing.T) {
	// Not parallel: puts a git on the PATH.
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+"each_commit:\n  - run: 'true'\n")
	tmp := s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.5\n")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! feat(s2): b1.txt")
	found, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "merges")
	gittest.WriteExecutable(t, filepath.Join(bin, "git"), "#!/bin/sh\n"+
		"case \"$1\" in merge-file) echo \"$(pwd) params=${GIT_CONFIG_PARAMETERS-unset}\" >> '"+log+"';; esac\n"+
		"exec '"+found+"' \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'wt.test=1'")
	o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
	if err != nil {
		t.Fatalf("err %v, error %v", err, o.Error)
	}
	inScratch := 0
	for _, line := range strings.Split(read(t, log), "\n") {
		if strings.Contains(line, "wt-sync-look-") {
			inScratch++
			if !strings.HasSuffix(line, "params=unset") {
				t.Errorf("merge %q", line)
			}
		}
	}
	if inScratch == 0 {
		t.Errorf("no merge ran in the scratch:\n%s", read(t, log))
	}
}

// A per-layer step that fails, and a recheck after one that fails, leave
// nothing for a later check to read.
func TestTargetsLookPutsTheCheckoutBackAfterALayerStepFails(t *testing.T) {
	t.Parallel()
	skipShort(t)
	check := "each_commit:\n  - run: |\n" +
		"      test -f cache.out || echo good > cache.out\n" +
		"      grep -q good cache.out || exit 9\n" +
		"      if [ \"$(git log -1 --format=%s)\" = 'chore: gen' ]; then echo bad > cache.out; exit 1; fi\n"
	for _, tc := range []struct{ name, step string }{
		{"the step itself", "  - run: echo bad > cache.out; exit 3\n    paths: [b1.txt]\n    each: layer\n    commit: 'chore: gen'\n"},
		{"its recheck", "  - run: echo x > gen.txt\n    paths: [b1.txt, gen.txt]\n    each: layer\n    commit: 'chore: gen'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := foldedLine(t, check+"defer:\n"+tc.step)
			o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
			wantStatus(t, err, synccmd.CheckFailedStatus)
			failed := 0
			for _, step := range lookSteps(o) {
				if strings.HasPrefix(step, "stop ") && !strings.HasSuffix(step, ": pass") {
					t.Errorf("a later check read what the failed step left: %q", lookSteps(o))
				}
				if strings.HasSuffix(step, ": fail") {
					failed++
				}
			}
			if failed == 0 {
				t.Errorf("nothing failed: %q", lookSteps(o))
			}
		})
	}
}

// Once the checkout cannot be put back, nothing later is known: not what
// passed, nor what is owed, nor a per-layer step that failed.
func TestTargetsLookKnowsNothingAfterACheckoutItCannotPutBack(t *testing.T) {
	t.Parallel()
	skipShort(t)
	l := foldedLine(t, "each_commit:\n"+
		"  - run: test -e locked || { touch locked; chmod 000 locked; }\n"+
		"  - run: |\n      test \"$(git log -1 --format=%s)\" != 'feat(s2): b1.txt'\n"+
		"  - run: bin/not-there.sh\n"+
		"defer:\n  - run: exit 3\n    paths: [b1.txt]\n    each: layer\n    commit: 'chore: gen'\n")
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, synccmd.CheckFailedStatus)
	steps := lookSteps(o)
	at := 0
	for at < len(steps) && !strings.HasSuffix(steps[at], ": fail") {
		at++
	}
	layerSteps := 0
	for _, step := range steps[min(at+1, len(steps)):] {
		if strings.HasPrefix(step, "layerStep ") {
			layerSteps++
		}
		if !strings.HasSuffix(step, ": indeterminate") && !strings.HasSuffix(step, ": notRun") {
			t.Errorf("after the failure: %q", steps)
			break
		}
	}
	if layerSteps == 0 {
		t.Errorf("no per-layer step after the failure: %q", steps)
	}
}

// Only a hook that could refuse or change what the run does fails the look:
// every other active one is named as not run.
func TestTargetsLookHooks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	step := func(run string) string {
		return "defer:\n  - run: " + run + "\n    paths: [b1.txt, gen.txt]\n    each: layer\n    commit: 'chore: gen'\n"
	}
	for _, tc := range []struct {
		name, hook, yaml string
		configured       bool
		status           int
		named            bool
	}{
		{name: "post-checkout", hook: "post-checkout", named: true},
		{name: "pre-commit with nothing committing", hook: "pre-commit"},
		{name: "pre-commit with a step that changes nothing", hook: "pre-commit", yaml: step("'true'"), named: true},
		{name: "pre-commit with a step committing", hook: "pre-commit", yaml: step("echo gen > gen.txt"), status: 1, named: true},
		{name: "a configured pre-rebase", hook: "pre-rebase", configured: true, status: 1, named: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := foldedLine(t, "each_commit:\n  - run: 'true'\n"+tc.yaml)
			main := l.ctx.Repo.MainRoot
			if tc.configured {
				testkit.GitIn(t, main, "config", "hook.guard.command", "false")
				testkit.GitIn(t, main, "config", "hook.guard.event", tc.hook)
			} else {
				gittest.WriteExecutable(t, filepath.Join(main, ".git", "hooks", tc.hook), "#!/bin/sh\ngit lfs "+tc.hook+" \"$@\"\n")
			}
			var text bytes.Buffer
			err := synccmd.SyncWorktree(l.ctx, gittest.StackTmp, targetsLookOptions(gittest.StackTmp), &text)
			wantStatus(t, err, tc.status)
			named := strings.Contains(text.String(), "did not run the "+tc.hook) || strings.Contains(text.String(), "not run here: "+tc.hook)
			if named != tc.named {
				t.Errorf("named %v, want %v:\n%s", named, tc.named, text.String())
			}
			var out bytes.Buffer
			wantStatus(t, synccmd.SyncJSON(l.ctx, targetsLookOptions(gittest.StackTmp), &out), tc.status)
			listed := `"hooksNotRun":[]`
			if named {
				listed = `"hooksNotRun":["` + tc.hook + `"]`
			}
			if got := strings.Join(strings.Fields(out.String()), ""); !strings.Contains(got, listed) {
				t.Errorf("want %s in:\n%s", listed, out.String())
			}
		})
	}
}

// The text look reads the declaration and its scripts from one trunk commit,
// though trunk moves while it looks.
func TestTargetsLookReadsOneTrunkCommit(t *testing.T) {
	t.Parallel()
	skipShort(t)
	l := foldedLine(t, "each_commit:\n  - run: bin/late.sh\n    needs: [docker]\n")
	main := l.ctx.Repo.MainRoot
	opts := targetsLookOptions(gittest.StackTmp)
	opts.Probes = map[string]func() error{wtsync.NeedDocker: func() error {
		testkit.WriteFile(t, main, "bin/late.sh", "#!/bin/sh\n")
		testkit.GitIn(t, main, "add", "bin/late.sh")
		testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "chore: add the script")
		testkit.GitIn(t, main, "update-ref", "refs/remotes/origin/main", "HEAD")
		return nil
	}}
	var text bytes.Buffer
	wantStatus(t, synccmd.SyncWorktree(l.ctx, gittest.StackTmp, opts, &text), 1)
	if !strings.Contains(text.String(), "neither the tree nor trunk") {
		t.Errorf("the look read a later trunk:\n%s", text.String())
	}
}

// A run another process holds the worktree for is refused, and so is the
// look's replay of it.
func TestTargetsLookIsRefusedByALiveLock(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ran := filepath.Join(t.TempDir(), "ran")
	l := foldedLine(t, "each_commit:\n  - run: touch '"+ran+"'\n")
	gitDir := testkit.GitOut(t, l.s.Paths[gittest.StackTmp], "rev-parse", "--absolute-git-dir")
	lock, err := wtsync.Acquire(gitDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, 1)
	if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "locked") || len(o.Fold.Checks) != 0 {
		t.Errorf("fold %+v", o.Fold)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a check ran")
	}
}

// A replaced object is replaced in the replay too.
func TestTargetsLookSeesReplacedObjects(t *testing.T) {
	t.Parallel()
	skipShort(t)
	dir := t.TempDir()
	testkit.WriteFile(t, dir, "original", "original\n")
	testkit.WriteFile(t, dir, "replacement", "replaced\n")
	// The blob "original\n" hashes to this in every repository.
	blob := "$(echo original | git hash-object --stdin)"
	l := foldedLine(t, "each_commit:\n  - run: '! git cat-file blob "+blob+" | grep -q replaced'\n")
	main := l.ctx.Repo.MainRoot
	object := func(name string) string {
		return testkit.GitOut(t, main, "hash-object", "-w", filepath.Join(dir, name))
	}
	testkit.GitIn(t, main, "replace", object("original"), object("replacement"))
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, synccmd.CheckFailedStatus)
	if o.Error != nil {
		t.Errorf("error %v, steps %q", *o.Error, lookSteps(o))
	}
}

// A failure whose output cannot be kept is still a failure in the report.
func TestTargetsLookKeepsAFailureItCannotLog(t *testing.T) {
	// Not parallel: sets TMPDIR.
	skipShort(t)
	temp := t.TempDir()
	l := foldedLine(t, "defer:\n  - run: chmod 555 '"+temp+"'; exit 3\n    paths: [b1.txt]\n    each: layer\n    commit: 'chore: gen'\n")
	t.Setenv("TMPDIR", temp)
	t.Cleanup(func() { _ = os.Chmod(temp, 0o755) })
	o, err := targetsLookJSON(t, l.ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, synccmd.CheckFailedStatus)
	if steps := lookSteps(o); len(steps) == 0 || !strings.HasSuffix(steps[0], ": fail") || o.Error == nil || !strings.Contains(*o.Error, "output") {
		t.Errorf("steps %q, error %v", steps, o.Error)
	}
}

func TestTargetsLookInterruptedWithAReadOnlyCache(t *testing.T) {
	t.Parallel()
	interruptedScratch(t, "each_commit:\n  - run: |\n"+
		"      mkdir -p cache/x && touch cache/x/f && chmod -R a-w cache && touch \"$READY\" && exec sleep 60\n", "")
}

func TestTargetsLookInterruptedWhileSnapshotting(t *testing.T) {
	t.Parallel()
	interruptedScratch(t, "each_commit:\n  - run: |\n"+
		"      mkdir -p big && cd big && seq 1 30000 | xargs touch\n  - run: 'true'\n", "wt-sync-look-*/snapshot*/big")
}

// interruptedScratch interrupts a look, once the check made the file READY
// names or a path matching appears under the temporary directory, and expects
// no scratch copy left.
func interruptedScratch(t *testing.T, yaml, appears string) {
	t.Helper()
	if testing.Short() {
		t.Skip("interrupts a look with a signal")
	}
	if os.Getenv(testkit.InterruptChild) == "" {
		out, status := testkit.InterruptedChild(t)
		field := func(name string) string {
			_, after, ok := strings.Cut(out, "\n"+name+"=")
			if !ok {
				t.Fatalf("no %s:\n%s", name, out)
			}
			v, _, _ := strings.Cut(after, "\n")
			return v
		}
		if pid, err := strconv.Atoi(field("holder")); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		if status != 128+int(syscall.SIGINT) {
			t.Errorf("status %d:\n%s", status, out)
		}
		left, _ := filepath.Glob(filepath.Join(field("tmp"), "wt-sync-look-*"))
		if len(left) != 0 {
			t.Errorf("scratch left: %v", left)
		}
		for _, dir := range left {
			_ = exec.Command("chmod", "-R", "u+w", dir).Run()
		}
		return
	}
	ready := filepath.Join(t.TempDir(), "ready")
	if err := os.Setenv("READY", ready); err != nil {
		t.Fatal(err)
	}
	l := foldedLine(t, yaml)
	fmt.Printf("\nholder=%d\ntmp=%s\n", l.s.Process, os.TempDir())
	want := ready
	if appears != "" {
		want = filepath.Join(os.TempDir(), appears)
	}
	go func() {
		for {
			if found, _ := filepath.Glob(want); len(found) > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	err := synccmd.SyncWorktree(l.ctx, gittest.StackTmp, targetsLookOptions(gittest.StackTmp), &bytes.Buffer{})
	t.Fatalf("the look ended: %v", err)
}
