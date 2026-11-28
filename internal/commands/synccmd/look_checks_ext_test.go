package synccmd_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/gittest"
)

func targetsLookOptions(work string) synccmd.SyncOptions {
	opts := synccmd.SyncOptions{Stack: &synccmd.StackLook{Work: work, Autosquash: true, EachCommit: "targets"}}
	opts.Probes = synccmdtest.DockerUp()
	return opts
}

// targetsLookJSON is wt sync <work> --stack --autosquash
// --each-commit=targets --json, its object checked against the schema.
func targetsLookJSON(t *testing.T, ctx *kit.Context, opts synccmd.SyncOptions) (synccmd.SyncOverview, error) {
	t.Helper()
	var out bytes.Buffer
	err := synccmd.SyncJSON(ctx, opts, &out)
	testkit.ValidateJSON(t, "sync", out.Bytes())
	var o synccmd.SyncOverview
	if uerr := json.Unmarshal(out.Bytes(), &o); uerr != nil {
		t.Fatalf("%v\n%s", uerr, out.String())
	}
	t.Cleanup(func() {
		if o.Fold == nil {
			return
		}
		for _, c := range o.Fold.Checks {
			for _, s := range c.Steps {
				if s.Log != nil {
					_ = os.RemoveAll(filepath.Dir(*s.Log))
				}
			}
		}
	})
	return o, err
}

// lookSteps is each step of the look's checks as "subject: result".
func lookSteps(o synccmd.SyncOverview) []string {
	var out []string
	if o.Fold == nil {
		return nil
	}
	for _, c := range o.Fold.Checks {
		subject := ""
		if c.TargetSubject != nil {
			subject = *c.TargetSubject
		}
		for _, s := range c.Steps {
			out = append(out, c.Kind+" "+subject+": "+s.Result)
		}
	}
	return out
}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var exit kit.ExitError
	var usage *config.InputError
	got := 0
	switch {
	case errors.As(err, &exit):
		got = exit.Code
	case errors.As(err, &usage):
		got = 2
	case err != nil:
		got = 1
	}
	if got != status {
		t.Errorf("exit %d (%v), want %d", got, err, status)
	}
}

// sourceState is everything a look must leave as it was: every ref, the
// worktrees, each file of the common git dir but the objects, and the status
// of the line's worktree.
func sourceState(t testing.TB, main, line string) string {
	t.Helper()
	var b strings.Builder
	for _, args := range [][]string{{"for-each-ref"}, {"worktree", "list", "--porcelain"}} {
		b.WriteString(gittest.Git(t, main, args...) + "\n")
	}
	b.WriteString(gittest.Git(t, line, "--no-optional-locks", "status", "--porcelain", "--untracked-files=all") + "\n")
	common := filepath.Join(main, ".git")
	err := filepath.WalkDir(common, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if path == filepath.Join(common, "objects") {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %x\n", path, sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// twoFixups is InPlaceCheckedLine's line with a fixup for a commit of layer 2
// and one for layer 4, and a check that appends the subject it ran at to the
// log. Its second step fails at layer 2's target once the fixup is in it, having edited a tracked
// file, rewritten the first step's untracked output and left a file of its
// own; anywhere else it passes only if none of that is left.
func twoFixups(t *testing.T) (ctx *kit.Context, s *gittest.StackLine, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "checked.log")
	yaml := synccmdtest.StackYAML + "each_commit:\n" +
		"  - run: test -f gen.out || echo good > gen.out\n" +
		"  - run: |\n" +
		"      s=$(git log -1 --format=%s); echo \"$s\" >> '" + logPath + "'\n" +
		"      if [ -f '" + logPath + ".block' ]; then touch '" + logPath + ".blocked'; while [ -f '" + logPath + ".block' ]; do sleep 0.05; done; fi\n" +
		"      grep -q good gen.out && [ ! -e leftover ] && git diff --quiet || { echo 'the checkout was not put back'; exit 7; }\n" +
		"      if [ \"$s\" = 'feat(s4): d1.txt' ]; then grep -q fixed d1.txt || exit 8; fi\n" +
		"      if [ \"$s\" = 'feat(s2): b1.txt' ] && grep -q fixed b1.txt; then echo bad > gen.out; touch leftover; echo x >> a1.txt; seq 1 40; echo 'b1 is broken'; exit 1; fi\n"
	ctx, s = synccmdtest.StackLine(t, yaml)
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	fixup(t, tmp, "d1.txt", "feat(s4): d1.txt")
	return ctx, s, logPath
}

// The look runs every stop, past the failure, on a checkout put back as it
// was; it lists the failure with its whole output and exits 4, in text and in
// --json. Nothing of the repository changes, not even while a check runs, and
// origin is not fetched. The run then stops at the same target and step.
func TestTargetsLookReportsEveryStopAndTheRunStopsAtTheSameOne(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, logPath := twoFixups(t)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	// Trunk moves on: a fetch would show.
	testkit.GitIn(t, main, "commit", "-q", "--allow-empty", "--no-verify", "-m", "feat: later on trunk")
	before := sourceState(t, main, tmp)

	testkit.WriteFile(t, filepath.Dir(logPath), "checked.log.block", "")
	during := make(chan string, 1)
	go func() {
		for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(logPath + ".blocked"); err == nil {
				break
			}
		}
		during <- sourceState(t, main, tmp)
		_ = os.Remove(logPath + ".block")
	}()
	o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
	wantStatus(t, err, synccmd.CheckFailedStatus)
	if got := <-during; got != before {
		t.Errorf("while a check ran:\n%s\nbefore:\n%s", got, before)
	}
	if got := sourceState(t, main, tmp); got != before {
		t.Errorf("after the look:\n%s\nbefore:\n%s", got, before)
	}
	if o.Error != nil || o.Fetched {
		t.Errorf("error %v, fetched %v", o.Error, o.Fetched)
	}
	steps := lookSteps(o)
	if len(steps) < 6 || steps[0] != "stop feat(s2): b1.txt: pass" || steps[1] != "stop feat(s2): b1.txt: fail" {
		t.Fatalf("steps %q", steps)
	}
	for _, step := range steps[2:] {
		if !strings.HasSuffix(step, ": pass") {
			t.Errorf("after the failure: %q", steps)
		}
	}
	first := o.Fold.Checks[0]
	if first.Target == nil || *first.Target != o.Fold.Folds[0].Target || !slices.Equal(first.Fixups, []string{o.Fold.Folds[0].Fixup}) || first.Layer != "s2" {
		t.Errorf("first visit %+v, folds %+v", first, o.Fold.Folds)
	}
	if !slices.ContainsFunc(o.Fold.Checks, func(c synccmd.FoldLookCheck) bool {
		return c.Target != nil && *c.Target == o.Fold.Folds[1].Target
	}) {
		t.Errorf("the second target was not reached: %q", steps)
	}
	failed := first.Steps[1]
	if failed.Why == nil || *failed.Why != "exit 1" || failed.Log == nil {
		t.Fatalf("failed step %+v", failed)
	}
	if log := read(t, *failed.Log); !strings.HasPrefix(log, "1\n2\n") || !strings.Contains(log, "b1 is broken") {
		t.Errorf("the log is not the whole output:\n%s", log)
	}

	var text bytes.Buffer
	err = synccmd.SyncWorktree(ctx, gittest.StackTmp, targetsLookOptions(gittest.StackTmp), &text)
	wantStatus(t, err, synccmd.CheckFailedStatus)
	for _, want := range []string{"--each-commit=targets", "✗ s=$(git log", "fail: exit 1", "log " + os.TempDir(), "1 failed"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("the look does not say %q:\n%s", want, text.String())
		}
	}

	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, []string{"feat(s2): b1.txt"}) {
		t.Errorf("the run checked %q\n%s", got, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Reason == nil || !strings.Contains(*p.Reason+human, "b1 is broken") {
		t.Errorf("the run stopped elsewhere: %+v\n%s", p, human)
	}
}

// A clean line exits 0, and the look checked what the run then checks, in its
// order: past unchanged layers and paths a step does not list, and at a
// move's target and source.
func TestTargetsLookChecksWhatTheRunChecks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	inPlace := func(paths string) func(*testing.T) (*kit.Context, string, string) {
		return func(t *testing.T) (*kit.Context, string, string) {
			ctx, s, logPath := synccmdtest.InPlaceCheckedLine(t, paths)
			passing(t, logPath)
			fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
			return ctx, gittest.StackTmp, logPath
		}
	}
	for _, tc := range []struct {
		name string
		mk   func(*testing.T) (*kit.Context, string, string)
	}{
		{"every path", inPlace("")},
		{"no path of the target", inPlace("[a1.txt]")},
		{"a path of one tip", inPlace("[d2.txt]")},
		{"move", func(t *testing.T) (*kit.Context, string, string) {
			ctx, _, logPath := synccmdtest.MoveLine(t, false, true)
			return ctx, gittest.MoveTmp, logPath
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, work, logPath := tc.mk(t)
			o, err := targetsLookJSON(t, ctx, targetsLookOptions(work))
			if err != nil || o.Error != nil {
				t.Fatalf("err %v, error %v", err, o.Error)
			}
			looked := synccmdtest.CheckedSubjects(t, logPath)
			var listed []string
			for _, step := range lookSteps(o) {
				listed = append(listed, strings.TrimSuffix(strings.TrimPrefix(step, "stop "), ": pass"))
			}
			if !slices.Equal(listed, looked) {
				t.Errorf("listed %q, ran %q", listed, looked)
			}
			_ = os.Remove(logPath)
			if _, human, err := synccmdtest.StackRun(t, ctx, work, targetsFold()); err != nil && !strings.Contains(err.Error(), "held") {
				t.Fatalf("run: %v\n%s", err, human)
			}
			if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, looked) {
				t.Errorf("the run checked %q, the look %q", got, looked)
			}
		})
	}
}

// What is not a pass: each ends with its own status, and says why where the
// object's error is the place.
func TestTargetsLookOutcomes(t *testing.T) {
	t.Parallel()
	skipShort(t)
	line := func(t *testing.T, eachCommit string) (*kit.Context, *gittest.StackLine) {
		ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+eachCommit)
		fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
		return ctx, s
	}
	t.Run("onto without a move", func(t *testing.T) {
		t.Parallel()
		ctx, _ := line(t, "each_commit:\n  - run: 'true'\n")
		opts := targetsLookOptions(gittest.StackTmp)
		opts.Onto, opts.Work = "main", gittest.StackTmp
		o, err := targetsLookJSON(t, ctx, opts)
		wantStatus(t, err, 2)
		if o.Error == nil || !strings.Contains(*o.Error, "move!") {
			t.Errorf("error %v", o.Error)
		}
		wantStatus(t, synccmd.SyncWorktree(ctx, gittest.StackTmp, opts, &bytes.Buffer{}), 2)
	})
	t.Run("a need of a step its run does not reach", func(t *testing.T) {
		t.Parallel()
		// Layer 1 lies below the fold and stays as it is, though its own
		// worktree's rebase onto trunk would run the step.
		ctx, _ := line(t, "each_commit:\n  - run: 'true'\n    paths: [a1.txt]\n    needs: [docker]\n")
		opts := targetsLookOptions(gittest.StackTmp)
		opts.Probes = synccmdtest.DockerDown().Probes()
		o, err := targetsLookJSON(t, ctx, opts)
		if err != nil || o.Fold.Refusal != nil || len(o.Needs) != 0 {
			t.Errorf("err %v, refusal %v, needs %+v", err, o.Fold.Refusal, o.Needs)
		}
	})
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		ctx, _ := line(t, "each_commit:\n  - run: 'true'\n    needs: [docker]\n")
		opts := targetsLookOptions(gittest.StackTmp)
		opts.Probes = synccmdtest.DockerDown().Probes()
		o, err := targetsLookJSON(t, ctx, opts)
		wantStatus(t, err, 1)
		if o.Error != nil || o.Fold.Refusal == nil || o.Fold.Checks == nil || len(o.Fold.Checks) != 0 || len(o.Needs) != 1 {
			t.Errorf("error %v, fold %+v, needs %+v", o.Error, o.Fold, o.Needs)
		}
	})
	t.Run("a fixup named by the reflog", func(t *testing.T) {
		t.Parallel()
		ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+"each_commit:\n  - run: 'true'\n")
		tmp := s.Paths[gittest.StackTmp]
		fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
		fixup(t, tmp, "d1.txt", "HEAD@{1}")
		o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
		wantStatus(t, err, 1)
		if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "HEAD@{1}") || len(o.Fold.Checks) != 0 {
			t.Errorf("fold %+v, error %v", o.Fold, o.Error)
		}
	})
	t.Run("owed", func(t *testing.T) {
		t.Parallel()
		ctx, _ := line(t, "each_commit:\n  - run: bin/not-there.sh\n")
		o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
		wantStatus(t, err, 1)
		if steps := lookSteps(o); o.Error != nil || len(steps) == 0 || !strings.HasSuffix(steps[0], ": owed") {
			t.Errorf("error %v, steps %q", o.Error, steps)
		}
	})
	t.Run("a hook", func(t *testing.T) {
		t.Parallel()
		ctx, _ := line(t, "each_commit:\n  - run: 'true'\n")
		hooks := filepath.Join(ctx.Repo.MainRoot, ".git", "hooks")
		testkit.GitIn(t, ctx.Repo.MainRoot, "config", "core.hooksPath", hooks)
		ran := filepath.Join(t.TempDir(), "ran")
		gittest.WriteExecutable(t, filepath.Join(hooks, "pre-rebase"), "#!/bin/sh\ntouch '"+ran+"'\nexit 1\n")
		o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
		wantStatus(t, err, 1)
		if o.Error == nil || !strings.Contains(*o.Error, "pre-rebase") || len(o.Fold.Checks) == 0 {
			t.Errorf("error %v, steps %q", o.Error, lookSteps(o))
		}
		if _, err := os.Stat(ran); err == nil {
			t.Error("the hook ran")
		}
	})
	t.Run("a per-layer step that fails, with no check declared", func(t *testing.T) {
		t.Parallel()
		ctx, _ := line(t, "defer:\n  - run: echo cannot generate; exit 3\n    paths: [b1.txt]\n    each: layer\n    fold: owner\n")
		o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
		wantStatus(t, err, synccmd.CheckFailedStatus)
		if len(o.Fold.Checks) == 0 {
			t.Fatalf("no checks, error %v", o.Error)
		}
		c := o.Fold.Checks[0]
		if c.Kind != "layerStep" || c.Layer != "s2" || c.Target != nil || c.Steps[0].Result != "fail" || c.Steps[0].Log == nil || !strings.Contains(read(t, *c.Steps[0].Log), "cannot generate") {
			t.Errorf("visit %+v", c)
		}
	})
	t.Run("a checkout that cannot be put back", func(t *testing.T) {
		t.Parallel()
		ctx, _ := line(t, "each_commit:\n  - run: test -e locked || { touch locked; chmod 000 locked; }\n"+
			"  - run: |\n      test \"$(git log -1 --format=%s)\" != 'feat(s2): b1.txt'\n")
		o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
		wantStatus(t, err, synccmd.CheckFailedStatus)
		steps := lookSteps(o)
		if len(steps) < 3 || !strings.HasSuffix(steps[1], ": fail") || !strings.HasSuffix(steps[2], ": indeterminate") {
			t.Errorf("steps %q", steps)
		}
	})
}

// A conflict that is yours ends the replay: alone it exits 1, after a failed
// check 4, and the error says the later stops were not reached.
func TestTargetsLookAtAConflict(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for _, tc := range []struct {
		name, check, result string
		status              int
	}{
		{"alone", "'true'", "pass", 1},
		{"after a failure", "'false'", "fail", synccmd.CheckFailedStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+"each_commit:\n  - run: "+tc.check+"\n")
			// A fixup that folds into layer 2, then one for a commit of layer
			// 4 changing a file only layer 5 adds, which conflicts there.
			tmp := s.Paths[gittest.StackTmp]
			fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
			fixup(t, tmp, "e1.txt", "feat(s4): d1.txt")
			o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
			wantStatus(t, err, tc.status)
			if o.Error == nil || !strings.Contains(*o.Error, "not reached") {
				t.Errorf("error %v, steps %q", o.Error, lookSteps(o))
			}
			// The stops before the conflict, and none after it.
			want := []string{"stop feat(s2): b1.txt: " + tc.result, "stop feat(s2): b3.txt: " + tc.result, "stop chore(s3): bump to 1.0.1: " + tc.result}
			if got := lookSteps(o); !slices.Equal(got, want) {
				t.Errorf("visits %q, want %q", got, want)
			}
		})
	}
}

// An environment that points git at the source reaches nothing the look
// writes.
func TestTargetsLookIgnoresAGitEnvironment(t *testing.T) {
	// Not parallel: sets GIT_DIR and GIT_INDEX_FILE.
	skipShort(t)
	ctx, s, logPath := synccmdtest.InPlaceCheckedLine(t, "")
	passing(t, logPath)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	before := sourceState(t, main, tmp)
	gitDir := testkit.GitOut(t, tmp, "rev-parse", "--absolute-git-dir")
	t.Setenv("GIT_DIR", gitDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(gitDir, "index"))
	o, err := targetsLookJSON(t, ctx, targetsLookOptions(gittest.StackTmp))
	for _, name := range []string{"GIT_DIR", "GIT_INDEX_FILE"} {
		if uerr := os.Unsetenv(name); uerr != nil {
			t.Fatal(uerr)
		}
	}
	if err != nil || len(lookSteps(o)) == 0 {
		t.Errorf("err %v, error %v", err, o.Error)
	}
	if got := sourceState(t, main, tmp); got != before {
		t.Errorf("after the look:\n%s\nbefore:\n%s", got, before)
	}
}

func TestTargetsLookInterrupted(t *testing.T) {
	t.Parallel()
	interruptedLook(t, syscall.SIGINT)
}

func TestTargetsLookTerminated(t *testing.T) {
	t.Parallel()
	interruptedLook(t, syscall.SIGTERM)
}

// An interrupt while a check runs takes the check and the scratch copy with
// it, keeps the failed checks' output and leaves the repository as it was.
func interruptedLook(t *testing.T, sig syscall.Signal) {
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
		// The fixture's own process outlives a child that exits.
		if holder, err := strconv.Atoi(field("holder")); err == nil {
			_ = syscall.Kill(holder, syscall.SIGKILL)
		}
		if status != 128+int(sig) {
			t.Errorf("status %d:\n%s", status, out)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(read(t, field("pid"))))
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			t.Errorf("the check %d is still running", pid)
		}
		if left, _ := filepath.Glob(filepath.Join(field("tmp"), "wt-sync-look-*")); len(left) != 0 {
			t.Errorf("scratch left: %v", left)
		}
		_, said, ok := strings.Cut(out, "the output of the checks that failed is in ")
		logs, _, _ := strings.Cut(said, "\n")
		if entries, err := os.ReadDir(logs); !ok || err != nil || len(entries) != 1 {
			t.Errorf("logs %q: %v %v\n%s", logs, entries, err, out)
		}
		if got := sourceState(t, field("main"), field("line")); got != read(t, field("before")) {
			t.Errorf("after the interrupt:\n%s", got)
		}
		return
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	yaml := "each_commit:\n  - run: |\n" +
		"      if [ \"$(git log -1 --format=%s)\" = 'feat(s2): b1.txt' ]; then exit 1; fi\n" +
		"      echo $$ > '" + pidFile + ".tmp' && mv '" + pidFile + ".tmp' '" + pidFile + "'; exec sleep 60\n"
	ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+yaml)
	main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	before := filepath.Join(t.TempDir(), "before")
	testkit.WriteFile(t, filepath.Dir(before), "before", sourceState(t, main, tmp))
	fmt.Printf("\npid=%s\ntmp=%s\nmain=%s\nline=%s\nbefore=%s\nholder=%d\n", pidFile, os.TempDir(), main, tmp, before, s.Process)
	go func() {
		for {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = syscall.Kill(os.Getpid(), sig)
	}()
	err := synccmd.SyncWorktree(ctx, gittest.StackTmp, targetsLookOptions(gittest.StackTmp), os.Stdout)
	t.Fatalf("the look ended: %v", err)
}
