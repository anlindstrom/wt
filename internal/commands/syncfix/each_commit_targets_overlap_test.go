package syncfix

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

// A layer of four commits, a fixup above it for the third, and two per-layer
// steps that each fold their generated output into the commit that owns it:
//
//	feat: a      owns gen/a.txt  (the first step's owner)
//	feat: b      owns gen/b.txt  (the second step's owner)
//	feat: t      takes the fixup (an interior target)
//	chore: bump  changes v.txt, so both outputs drift (the layer's tip)
//
// The first step's fold rewrites feat: t, and the second step's fold, which
// starts at feat: b, reaches the rewritten one.
func overlapLine(t *testing.T, logPath string) (*kit.Context, string) {
	t.Helper()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	testkit.GitIn(t, main, "config", "core.hooksPath", filepath.Join(main, ".git", "hooks"))
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	commit := func(msg string) {
		testkit.GitIn(t, main, "add", "-A")
		testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", msg)
	}
	gen := func(name string) string {
		return "#!/bin/sh\nset -e\nmkdir -p gen\n{ cat src/" + name + ".txt; cat v.txt; } > gen/" + name + ".txt\n"
	}
	for _, n := range []string{"a", "b"} {
		testkit.WriteFile(t, main, "gen-"+n+".sh", gen(n))
		if err := os.Chmod(filepath.Join(main, "gen-"+n+".sh"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testkit.WriteFile(t, main, "v.txt", "1.0.0\n")
	commit("base")
	testkit.GitIn(t, main, "checkout", "-q", "-b", "tmp/overlap")
	for _, n := range []string{"a", "b"} {
		testkit.WriteFile(t, main, "src/"+n+".txt", n+"\n")
		run := exec.Command("sh", "./gen-"+n+".sh")
		run.Dir = main
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("gen-%s.sh: %v\n%s", n, err, out)
		}
		commit("feat: " + n)
	}
	testkit.WriteFile(t, main, "docs/t.txt", "t\n")
	commit("feat: t")
	testkit.WriteFile(t, main, "v.txt", "1.0.1\n")
	commit("chore: bump")
	testkit.GitIn(t, main, "branch", "g1")
	testkit.WriteFile(t, main, "docs/t.txt", "t, fixed\n")
	commit("fixup! feat: t")
	testkit.GitIn(t, main, "checkout", "-q", "main")
	yaml := "defer:\n" +
		"  - run: ./gen-a.sh\n    paths: [gen/a.txt]\n    each: layer\n    fold: owner\n    commit: \"chore: regenerate a\"\n" +
		"  - run: ./gen-b.sh\n    paths: [gen/b.txt]\n    each: layer\n    fold: owner\n    commit: \"chore: regenerate b\"\n" +
		"each_commit:\n  - run: |\n" +
		"      s=$(git log -1 --format=%s); echo \"$s\" >> '" + logPath + "'\n" +
		"      if [ \"$s\" = 'feat: t' ] && [ ! -f '" + logPath + ".pass' ]; then echo 'feat: t needs the pass file'; exit 1; fi\n"
	testkit.WriteFile(t, main, ".wt-sync.yaml", yaml)
	commit("chore: declare")
	testkit.GitIn(t, main, "remote", "add", "origin", main)
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	testkit.GitIn(t, main, "worktree", "add", "-q", filepath.Join(wtDir, "tmp_overlap"), "tmp/overlap")
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, "tmp/overlap"
}

// Two steps whose folds overlap an interior fixup target: the second fold
// reaches the commit the first one rewrote, and it is still a target, so it
// is checked again after each fold, not only after the first.
func TestFoldUnderTargetsKeepsATargetThroughTwoOverlappingFolds(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	logPath := filepath.Join(t.TempDir(), "checked.log")
	testkit.WriteFile(t, filepath.Dir(logPath), "checked.log.pass", "")
	ctx, work := overlapLine(t, logPath)
	opts := runAt(200, &synccmd.StackOptions{Autosquash: true})
	opts.EachCommit = "targets"
	res, human, err := synccmdtest.StackRun(t, ctx, work, opts)
	if err != nil {
		t.Fatalf("err %v\n%s", err, human)
	}
	if p := synccmdtest.Participant(t, res, work); p.Result != "rebased" {
		t.Fatalf("participant %s\n%s", p.Result, human)
	}
	seen := map[string]int{}
	for _, subject := range synccmdtest.CheckedSubjects(t, logPath) {
		seen[subject]++
	}
	// feat: t is checked where the fixup folded into it, and again after each
	// of the two folds; the tip likewise. feat: a is the first fold's owner and
	// feat: b the second's: once each. Nothing else is a target.
	want := map[string]int{"feat: t": 3, "chore: bump": 3, "feat: a": 1, "feat: b": 1}
	for subject, n := range want {
		if seen[subject] != n {
			t.Errorf("%q was checked %d times, want %d (%v)\n%s", subject, seen[subject], n, seen, human)
		}
	}
	for subject := range seen {
		if _, ok := want[subject]; !ok && !strings.HasPrefix(subject, "chore: regenerate") {
			t.Errorf("checked at %q, which is neither an owner, a target nor a tip", subject)
		}
	}
}

// A resume has no record of the targets the run before it found. A run that
// stopped at its first check and was resumed does both folds in the resumed
// process, which takes the safe side: every commit a fold rewrote is checked
// again, the interior target included, and the summary says so.
func TestResumeUnderTargetsChecksEveryRewrittenCommitAfterAFold(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	logPath := filepath.Join(t.TempDir(), "checked.log")
	ctx, work := overlapLine(t, logPath)
	opts := runAt(200, &synccmd.StackOptions{Autosquash: true})
	opts.EachCommit = "targets"
	if _, human, err := synccmdtest.StackRun(t, ctx, work, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	testkit.WriteFile(t, filepath.Dir(logPath), "checked.log.pass", "")
	resume := noResumeAgents()
	resume.Push = syncrun.PushNever
	var out strings.Builder
	if err := SyncResume(ctx, work, resume, &out); err != nil {
		t.Fatalf("resume: %v\n%s", err, out.String())
	}
	seen := map[string]int{}
	for _, subject := range synccmdtest.CheckedSubjects(t, logPath) {
		seen[subject]++
	}
	// feat: t: the check that failed, again after the fix, and after each of
	// the two folds. feat: b, which is no target, is checked after both folds
	// too, since the resumed run does not know which commits are.
	for subject, n := range map[string]int{"feat: t": 4, "feat: b": 2, "feat: a": 1, "chore: bump": 3} {
		if seen[subject] != n {
			t.Errorf("%q was checked %d times, want %d (%v)\n%s", subject, seen[subject], n, seen, out.String())
		}
	}
	if !strings.Contains(out.String(), "after a fold the checks cover every rewritten commit") {
		t.Errorf("the summary does not say so:\n%s", out.String())
	}
}

// A look under --each-commit=targets goes through the per-layer folds the run
// makes: it checks the same commits in the same order, the rechecks after each
// fold too, and names each recheck by the commit of the line.
func TestTargetsLookChecksAgainAfterEachFoldAsTheRunDoes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	logPath := filepath.Join(t.TempDir(), "checked.log")
	testkit.WriteFile(t, filepath.Dir(logPath), "checked.log.pass", "")
	ctx, work := overlapLine(t, logPath)
	var out bytes.Buffer
	look := synccmd.SyncOptions{Stack: &synccmd.StackLook{Work: work, Autosquash: true, EachCommit: "targets"}, Probes: synccmdtest.DockerUp()}
	if err := synccmd.SyncJSON(ctx, look, &out); err != nil {
		t.Fatalf("look: %v\n%s", err, out.String())
	}
	testkit.ValidateJSON(t, "sync", out.Bytes())
	var o synccmd.SyncOverview
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	looked := synccmdtest.CheckedSubjects(t, logPath)
	var listed, rechecked []string
	for _, c := range o.Fold.Checks {
		listed = append(listed, *c.TargetSubject)
		if c.Kind == "recheck" && c.AfterStep != nil && c.Target != nil {
			rechecked = append(rechecked, *c.TargetSubject+" is "+testkit.GitOut(t, ctx.Repo.MainRoot, "log", "-1", "--format=%s", *c.Target))
		}
	}
	if !slices.Equal(listed, looked) || !slices.Contains(rechecked, "feat: t is feat: t") {
		t.Errorf("listed %q, ran %q, rechecks %q", listed, looked, rechecked)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	opts := runAt(200, &synccmd.StackOptions{Autosquash: true})
	opts.EachCommit = "targets"
	if _, human, err := synccmdtest.StackRun(t, ctx, work, opts); err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	if got := synccmdtest.CheckedSubjects(t, logPath); !slices.Equal(got, looked) {
		t.Errorf("the run checked %q, the look %q", got, looked)
	}
}
