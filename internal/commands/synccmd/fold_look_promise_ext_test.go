package synccmd_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// foldLook is the look at the in-place fold of work.
func foldLook(t *testing.T, ctx *kit.Context, work string, alone bool) string {
	t.Helper()
	var look bytes.Buffer
	opts := synccmd.SyncOptions{NoFetch: true, Alone: alone, Stack: &synccmd.StackLook{Work: work, Autosquash: true}}
	if err := synccmd.SyncWorktree(ctx, work, opts, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	return look.String()
}

func tipsOf(t *testing.T, main string, branches ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, b := range branches {
		out[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	return out
}

// lowLine is a line of two commits and a fixup of the first on branch
// tmp/low, in its own worktree, on top of trunk's tip, which declares the
// strategies: the line is current with trunk.
func lowLine(t *testing.T) (ctx *kit.Context, tmp string) {
	t.Helper()
	ctx, _ = synccmdtest.OpenLine(t, func(main, wtDir string) {
		write := func(rel, content string) { gittest.WriteFile(t, filepath.Join(main, rel), content) }
		commit := func(msg string) {
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		write("base.txt", "base\n")
		write(".wt-sync.yaml", synccmdtest.StackYAML)
		commit("base")
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/low")
		write("a.txt", "a\n")
		commit("feat: a")
		write("b.txt", "b\n")
		commit("feat: b")
		write("a.txt", "a fixed\n")
		commit("fixup! feat: a")
		gittest.Git(t, main, "branch", "low1", "tmp/low~2")
		gittest.Git(t, main, "checkout", "-q", "main")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		tmp = filepath.Join(wtDir, "tmp_low")
		gittest.Git(t, main, "worktree", "add", "-q", tmp, "tmp/low")
	})
	return ctx, tmp
}

// Layers a run holds, or leaves with --alone, are not promised a move: the
// look says they stay, and the run leaves exactly those where they are.
func TestFoldLookNamesTheLayersTheRunHolds(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	main := ctx.Repo.MainRoot
	got := foldLook(t, ctx, gittest.StackTmp, false)
	for _, want := range []string{"unchanged: s1", "re-pointed: s2, s4", "held where they are: s3 (", "s5 ("} {
		if !strings.Contains(got, want) {
			t.Errorf("the look lacks %q:\n%s", want, got)
		}
	}
	before := tipsOf(t, main, append([]string{gittest.StackTmp}, s.Layers...)...)
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err == nil {
		t.Fatalf("a run with two held layers succeeded\n%s", human)
	}
	after := tipsOf(t, main, append([]string{gittest.StackTmp}, s.Layers...)...)
	for b, moved := range map[string]bool{"s1": false, "s2": true, "s3": false, "s4": true, "s5": false, gittest.StackTmp: true} {
		if (after[b] != before[b]) != moved {
			t.Errorf("%s moved: %v, the look said %v", b, after[b] != before[b], moved)
		}
	}
}

func TestFoldLookWithAloneMovesNoLayer(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")
	main := ctx.Repo.MainRoot
	got := foldLook(t, ctx, gittest.StackTmp, true)
	if strings.Contains(got, "re-pointed: s") {
		t.Errorf("the look with --alone re-points a layer:\n%s", got)
	}
	if !strings.Contains(got, "--alone") {
		t.Errorf("the look does not say --alone leaves the layers:\n%s", got)
	}
	before := tipsOf(t, main, append([]string{gittest.StackTmp}, s.Layers...)...)
	opts := synccmdtest.NoAgents()
	opts.Alone = true
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts); err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	for b, tip := range tipsOf(t, main, s.Layers...) {
		if tip != before[b] {
			t.Errorf("%s moved with --alone", b)
		}
	}
	if testkit.GitOut(t, main, "rev-parse", gittest.StackTmp) == before[gittest.StackTmp] {
		t.Error("the run did not fold the line with --alone")
	}
}

// A line on trunk's tip is skipped by a plain run, and folded by an in-place
// fold: the look says the second.
func TestFoldLookProceedsWhereTheLineIsCurrentWithTrunk(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := lowLine(t)
	main := ctx.Repo.MainRoot
	got := foldLook(t, ctx, "tmp/low", false)
	if strings.Contains(got, "skipped") || !strings.Contains(got, "folds the line in place") {
		t.Errorf("the look does not promise the fold:\n%s", got)
	}
	old := tipsOf(t, main, "tmp/low", "low1")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts); err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	for b, tip := range tipsOf(t, main, "tmp/low", "low1") {
		if tip == old[b] {
			t.Errorf("%s did not move, the look said it folds", b)
		}
	}
	if out := testkit.GitOut(t, tmp, "log", "--format=%s", "--grep=^fixup!"); out != "" {
		t.Errorf("a fixup is left: %s", out)
	}
}

// A line behind its own remote is refused by a stack run, on trunk's tip or
// behind it: the look says so, where it would otherwise promise the fold.
func TestFoldLookRefusesALineBehindItsOwnRemote(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, behindTrunk := range map[string]bool{"on trunk's tip": false, "behind trunk": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refusedBehindOwnRemote(t, behindTrunk)
		})
	}
}

func refusedBehindOwnRemote(t *testing.T, behindTrunk bool) {
	t.Helper()
	ctx, tmp := lowLine(t)
	main := ctx.Repo.MainRoot
	if behindTrunk {
		testkit.GitIn(t, main, "commit", "-q", "--no-verify", "--allow-empty", "-m", "feat: trunk's own")
		testkit.GitIn(t, main, "fetch", "-q", "origin")
	}
	gittest.WriteFile(t, filepath.Join(tmp, "c.txt"), "c\n")
	testkit.GitIn(t, tmp, "add", "-A")
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "feat: c")
	testkit.GitIn(t, tmp, "update-ref", "refs/remotes/origin/tmp/low", testkit.GitOut(t, tmp, "rev-parse", "HEAD"))
	testkit.GitIn(t, tmp, "reset", "-q", "--hard", "HEAD~1")
	old := testkit.GitOut(t, main, "rev-parse", "tmp/low")
	got := foldLook(t, ctx, "tmp/low", false)
	if !strings.Contains(got, "refused: behind its own remote") || strings.Contains(got, "folds the line in place") {
		t.Errorf("the look does not refuse:\n%s", got)
	}
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts); err == nil {
		t.Fatalf("the run was not refused\n%s", human)
	}
	if testkit.GitOut(t, main, "rev-parse", "tmp/low") != old {
		t.Error("a refused run moved the line")
	}
}

// A fixup whose name finds no commit refuses the whole fold: the look says
// so, with the run's reason, and lists no fold it will not make.
func TestFoldLookRefusesAnUnresolvedFixup(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := lowLine(t)
	main := ctx.Repo.MainRoot
	fixup(t, tmp, "b.txt", "feat: nowhere")
	old := testkit.GitOut(t, main, "rev-parse", "tmp/low")
	got := foldLook(t, ctx, "tmp/low", false)
	for _, want := range []string{"refused:", `names no commit earlier on the line ("feat: nowhere")`} {
		if !strings.Contains(got, want) {
			t.Errorf("the look lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "folds the line in place") || strings.Contains(got, "fold in place") || strings.Contains(got, "re-pointed") {
		t.Errorf("the look promises a fold:\n%s", got)
	}
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	_, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
	if err == nil || !strings.Contains(err.Error()+human, "names no commit earlier on the line") {
		t.Fatalf("the run was not refused for it: %v\n%s", err, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "tmp/low") != old {
		t.Error("a refused run moved the line")
	}
}
