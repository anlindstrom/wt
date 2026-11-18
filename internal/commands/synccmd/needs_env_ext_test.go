package synccmd_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

const (
	envVar   = "WT_TEST_NEEDS_TOKEN"
	envValue = "tok-9d41c7e2-DISTINCTIVE-secret"
)

// unsetEnv removes envVar for the test, restoring it after.
func unsetEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envVar, "")
	if err := os.Unsetenv(envVar); err != nil {
		t.Fatal(err)
	}
}

// envLine is the fold line whose deferred step needs env:envVar.
func envLine(t *testing.T) (*kit.Context, *gittest.FoldLine) {
	t.Helper()
	yaml := strings.Replace(gittest.FoldYAML("max-plus-patch"), "needs: [docker]", "needs: [env:"+envVar+"]", 1)
	return synccmdtest.FoldLine(t, yaml)
}

// repoState is every ref, the status of every worktree and the stash: what
// a refused run must leave as it found it.
func repoState(t *testing.T, ctx *kit.Context, s *gittest.FoldLine) string {
	t.Helper()
	main := ctx.Repo.MainRoot
	var b strings.Builder
	b.WriteString(testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)"))
	b.WriteString("\nstash:" + testkit.GitOut(t, main, "stash", "list"))
	for _, name := range []string{"f1", "f3", gittest.FoldTmp} {
		if p := s.Paths[name]; p != "" {
			b.WriteString("\n" + name + ":" + testkit.GitOut(t, p, "status", "--porcelain=v1", "-uall") + testkit.GitOut(t, p, "diff", "HEAD"))
		}
	}
	return b.String()
}

// assertNoValue fails when envValue is in out or in any file under the
// repository's git directory or worktrees.
func assertNoValue(t *testing.T, ctx *kit.Context, s *gittest.FoldLine, out ...string) {
	t.Helper()
	for _, o := range out {
		if strings.Contains(o, envValue) {
			t.Errorf("the value is in output:\n%s", o)
		}
	}
	roots := []string{filepath.Join(ctx.Repo.MainRoot, ".git")}
	for _, p := range s.Paths {
		roots = append(roots, p)
	}
	for _, r := range roots {
		werr := filepath.WalkDir(r, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if b, rerr := os.ReadFile(path); rerr == nil && bytes.Contains(b, []byte(envValue)) {
				t.Errorf("the value is in %s", path)
			}
			return nil
		})
		if werr != nil {
			t.Errorf("reading %s: %v", r, werr)
		}
	}
}

func TestEnvNeedUnsetRefusesBeforeAnythingMoves(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	for name, set := range map[string]bool{"unset": false, "empty": true} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(envVar, "")
			if !set {
				unsetEnv(t)
			}
			ctx, s := envLine(t)
			testkit.GitIn(t, s.Paths["f3"], "checkout", "--", ".")
			before := repoState(t, ctx, s)
			opts := synccmdtest.NoAgents()
			opts.Now = func() time.Time { return time.Unix(0, 400) }
			_, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
			if err == nil {
				t.Fatalf("the run went on without the variable:\n%s", human)
			}
			all := err.Error() + human
			for _, want := range []string{"./gen.sh", "env:" + envVar, envVar + " is not set", "--allow-owed"} {
				if !strings.Contains(all, want) {
					t.Errorf("refusal lacks %q:\n%s", want, all)
				}
			}
			if after := repoState(t, ctx, s); after != before {
				t.Fatalf("the refusal moved something:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if refs := testkit.GitOut(t, ctx.Repo.MainRoot, "for-each-ref", "refs/wt-sync/"); refs != "" {
				t.Fatalf("a safety ref was written: %s", refs)
			}
		})
	}
}

func TestEnvNeedAllowOwedMovesAndStepsNamesTheVariable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	unsetEnv(t)
	ctx, s := envLine(t)
	main := ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.AllowOwed = true
	opts.Now = func() time.Time { return time.Unix(0, 400) }
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "owed") {
		t.Fatalf("err %v, want the step owed\n%s", err, human)
	}
	if !strings.Contains(human, "env:"+envVar) {
		t.Errorf("the allow-owed line does not name the variable:\n%s", human)
	}
	owed, rerr := wtsync.ReadOwed(main, gittest.FoldTmp)
	if rerr != nil || owed == nil || !owed.StepSet()["./gen.sh"] {
		t.Fatalf("owed on disk %+v %v", owed, rerr)
	}
	if base := testkit.GitOut(t, main, "merge-base", "origin/main", gittest.FoldTmp); base != testkit.GitOut(t, main, "rev-parse", "origin/main") {
		t.Fatal("the line did not move onto trunk")
	}
	_ = res

	// wt sync steps with the variable still unset refuses, naming it, and
	// moves nothing.
	before := repoState(t, ctx, s)
	sopts := synccmdtest.NoAgents()
	sopts.Now = func() time.Time { return time.Unix(0, 500) }
	sopts.Steps = &synccmd.StepsOptions{}
	_, human, err = synccmdtest.StackRun(t, ctx, gittest.FoldTmp, sopts)
	if err == nil || !strings.Contains(err.Error()+human, "env:"+envVar) {
		t.Fatalf("steps err %v\n%s", err, human)
	}
	if after := repoState(t, ctx, s); after != before {
		t.Fatal("a refused steps run moved something")
	}

	// With it set, the owed step runs and nothing is owed after.
	t.Setenv(envVar, envValue)
	sopts.Now = func() time.Time { return time.Unix(0, 600) }
	res, human2, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, sopts)
	if err != nil {
		t.Fatalf("steps with the variable set: %v\n%s", err, human2)
	}
	if p := synccmdtest.Participant(t, res, gittest.FoldTmp); p.Result != syncrun.ResultRebased {
		t.Fatalf("steps result %+v", p)
	}
	if owed, _ := wtsync.ReadOwed(main, gittest.FoldTmp); owed != nil && len(owed.StepSet()) > 0 {
		t.Fatalf("still owed: %+v", owed)
	}
	assertNoValue(t, ctx, s, human, human2)
}

func TestEnvNeedSetRunsTheStepAndTheValueIsNowhere(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	t.Setenv(envVar, envValue)
	ctx, s := envLine(t)
	testkit.GitIn(t, s.Paths["f3"], "checkout", "--", ".")
	opts := synccmdtest.NoAgents()
	opts.Now = func() time.Time { return time.Unix(0, 400) }
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, gittest.FoldTmp); p.Result != syncrun.ResultRebased {
		t.Fatalf("result %+v\n%s", p, human)
	}
	raw, merr := json.Marshal(res)
	if merr != nil {
		t.Fatal(merr)
	}
	assertNoValue(t, ctx, s, human, string(raw))
}

func TestOverviewAndDoctorShowAnEnvNeedWithoutItsValue(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	for _, set := range []bool{false, true} {
		if set {
			t.Setenv(envVar, envValue)
		} else {
			unsetEnv(t)
		}
		ctx, s := envLine(t)
		var out bytes.Buffer
		if err := synccmd.SyncDoctor(ctx, synccmd.DoctorOptions{}, &out); err != nil && set {
			t.Logf("doctor: %v", err)
		}
		want := "env:" + envVar
		if !strings.Contains(out.String(), want) {
			t.Errorf("set=%v: doctor shows no row for %s:\n%s", set, want, out.String())
		}
		state := "not set"
		if set {
			state = "set"
		}
		var row string
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.Contains(l, want) {
				row = l
			}
		}
		if !strings.Contains(row, state) || (set && strings.Contains(row, "not set")) {
			t.Errorf("set=%v: row %q", set, row)
		}
		ov := synccmdtest.OverviewJSON(t, ctx)
		var seen bool
		for _, n := range ov.Needs {
			if n.Need == want {
				seen = true
			}
		}
		if !seen {
			t.Errorf("set=%v: overview needs %+v", set, ov.Needs)
		}
		assertNoValue(t, ctx, s, out.String())
	}
}
