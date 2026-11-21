package synccmd_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A stray is a branch put on the line of tmp/line in a worktree of its own.
// at is where its tip sits: tip (the line's tip, which is layer c's), b (layer
// b's), x (the commit between b and c, no layer's tip). state is what its
// worktree holds: dirty (a tracked change and an untracked file), untracked
// (an untracked file only), rebasing (a rebase in progress), none (clean).
type stray struct{ name, at, state string }

// strayLine is three layers tmp/layer/a..c on the line of tmp/line, whose
// commits are a, b, x, c, and the strays.
func strayLine(t *testing.T, strays ...stray) (ctx *kit.Context, paths map[string]string) {
	t.Helper()
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	testkit.GitIn(t, main, "config", "core.hooksPath", filepath.Join(main, ".git", "hooks"))
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	commit := func(file string) {
		gittest.WriteFile(t, filepath.Join(main, file), file+"\n")
		gittest.Git(t, main, "add", "-A")
		gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", "feat: "+file)
	}
	commit("base.txt")
	gittest.Git(t, main, "checkout", "-q", "-b", "tmp/line")
	for _, l := range []string{"a", "b", "x", "c"} {
		commit(l + ".txt")
		if l != "x" {
			gittest.Git(t, main, "branch", "tmp/layer/"+l)
		}
	}
	where := map[string]string{"tip": "tmp/line", "b": "tmp/layer/b", "x": "tmp/line~1"}
	for _, s := range strays {
		gittest.Git(t, main, "branch", s.name, where[s.at])
	}
	gittest.Git(t, main, "checkout", "-q", "main")
	commit("trunk.txt")
	gittest.WriteFile(t, filepath.Join(main, ".wt-sync.yaml"), synccmdtest.StackYAML)
	gittest.Git(t, main, "add", "-A")
	gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", "chore: declare")
	gittest.Git(t, main, "remote", "add", "origin", main)
	gittest.Git(t, main, "fetch", "-q", "origin")
	gittest.Git(t, main, "worktree", "add", "-q", filepath.Join(wtDir, "line"), "tmp/line")
	paths = map[string]string{}
	for _, s := range strays {
		p := filepath.Join(wtDir, strings.ReplaceAll(s.name, "/", "-"))
		paths[s.name] = p
		gittest.Git(t, main, "worktree", "add", "-q", p, s.name)
		switch s.state {
		case "dirty":
			gittest.WriteFile(t, filepath.Join(p, "a.txt"), "work in progress\n")
			gittest.WriteFile(t, filepath.Join(p, "scratch.txt"), "untracked\n")
		case "untracked":
			gittest.WriteFile(t, filepath.Join(p, "scratch.txt"), "untracked\n")
		case "rebasing":
			gitDir := testkit.GitOut(t, p, "rev-parse", "--absolute-git-dir")
			if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, err = kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, paths
}

func strayLook(t *testing.T, ctx *kit.Context, layers ...string) *syncrun.Stack {
	t.Helper()
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: "tmp/line", Layers: layers}})
	return o.Stack
}

func layerNames(s *syncrun.Stack) []string {
	var out []string
	for _, l := range s.Layers {
		out = append(out, l.Branch)
	}
	return out
}

func skippedReason(s *syncrun.Stack, branch string) (string, bool) {
	for _, k := range s.Skipped {
		if k.Branch == branch {
			return k.Reason, true
		}
	}
	return "", false
}

func worktreeSnap(t *testing.T, dir string) string {
	t.Helper()
	return testkit.GitOut(t, dir, "status", "--porcelain=v1", "-uall") + "\n" + testkit.GitOut(t, dir, "diff") + "\n" + testkit.GitOut(t, dir, "rev-parse", "HEAD")
}

func strayRefs(t *testing.T, main string, branches ...string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, b := range branches {
		m[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	return m
}

var strayAllRefs = []string{"tmp/line", "tmp/layer/a", "tmp/layer/b", "tmp/layer/c", "spike"}

// The issue's case: the spike branch sits on the line's tip with a tracked
// change and an untracked file in its worktree. Without --layer the look
// lists it as not taken and with why, a run moves exactly the three layers
// and the top, the spike branch and its worktree stay as they were, and undo
// puts everything back.
func TestSyncRebaseStackLeavesAStrayWithNoCommitsOfItsOwnAlone(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, paths := strayLine(t, stray{"spike", "tip", "dirty"})
	main := ctx.Repo.MainRoot
	spike := paths["spike"]
	before := strayRefs(t, main, strayAllRefs...)
	spikeBefore := worktreeSnap(t, spike)

	look := strayLook(t, ctx)
	if got := strings.Join(layerNames(look), " "); got != "tmp/layer/a tmp/layer/b tmp/layer/c" {
		t.Fatalf("the look takes %v, want exactly the three", got)
	}
	want := "no commits of its own, with uncommitted changes in " + spike + "; name it with --layer to move it"
	if reason, ok := skippedReason(look, "spike"); !ok || reason != want {
		t.Errorf("skipped spike: %q %v, want %q", reason, ok, want)
	}

	opts := synccmdtest.NoAgents()
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/line", opts)
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, "tmp/line")
	if got := strings.Join(slices.Sorted(mapKeys(layersOf(p.Stack))), " "); got != "tmp/layer/a tmp/layer/b tmp/layer/c" {
		t.Errorf("the run's layers: %v", got)
	}
	if reason, ok := skippedReason(p.Stack, "spike"); !ok || reason != want {
		t.Errorf("the run's skipped spike: %q %v, want %q", reason, ok, want)
	}
	for _, b := range []string{"tmp/line", "tmp/layer/a", "tmp/layer/b", "tmp/layer/c"} {
		if testkit.GitOut(t, main, "rev-parse", b) == before[b] {
			t.Errorf("%s did not move", b)
		}
	}
	if testkit.GitOut(t, main, "rev-parse", "spike") != before["spike"] || worktreeSnap(t, spike) != spikeBefore {
		t.Errorf("the spike branch or its worktree changed:\n%s", worktreeSnap(t, spike))
	}

	var undone bytes.Buffer
	if err := syncfix.SyncUndo(ctx, "tmp/line", syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for b, tip := range before {
		if testkit.GitOut(t, main, "rev-parse", b) != tip {
			t.Errorf("undo left %s elsewhere than %s", b, tip)
		}
	}
	if worktreeSnap(t, spike) != spikeBefore {
		t.Errorf("undo changed the spike worktree:\n%s", worktreeSnap(t, spike))
	}
	if b, err := os.ReadFile(filepath.Join(spike, "scratch.txt")); err != nil || string(b) != "untracked\n" {
		t.Errorf("the untracked file is gone or changed: %v", err)
	}
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// A stray on an intermediate layer's tip, the same commit as a clean layer's,
// is not taken either.
func TestSyncRebaseStackLeavesAStrayAtAnIntermediateLayerAlone(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := strayLine(t, stray{"spike", "b", "dirty"})
	look := strayLook(t, ctx)
	if got := strings.Join(layerNames(look), " "); got != "tmp/layer/a tmp/layer/b tmp/layer/c" {
		t.Errorf("the look takes %v", got)
	}
	if _, ok := skippedReason(look, "spike"); !ok {
		t.Errorf("spike is not listed as skipped: %+v", look.Skipped)
	}
}

// What the rule does not catch is taken as it always was: a dirty branch with
// a commit of its own, and a branch with none whose worktree has untracked
// files only or is mid-rebase. A dirty one is held, an untracked-only one is
// moved, and none is listed as skipped.
func TestSyncRebaseStackStillTakesWhatTheRuleDoesNotCatch(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := strayLine(t,
		stray{"wip", "x", "dirty"},
		stray{"scratch", "b", "untracked"},
		stray{"mid", "b", "rebasing"})
	main := ctx.Repo.MainRoot
	look := strayLook(t, ctx)
	for _, b := range []string{"wip", "scratch", "mid"} {
		if !slices.Contains(layerNames(look), b) {
			t.Errorf("%s is not taken: layers %v", b, layerNames(look))
		}
	}
	if len(look.Skipped) != 0 {
		t.Errorf("skipped %+v, want none", look.Skipped)
	}
	before := strayRefs(t, main, "wip", "scratch", "mid")
	res, human, _ := synccmdtest.StackRun(t, ctx, "tmp/line", synccmdtest.NoAgents())
	layers := layersOf(synccmdtest.Participant(t, res, "tmp/line").Stack)
	for b, result := range map[string]string{"wip": wtsync.MoveHeld, "mid": wtsync.MoveHeld, "scratch": ""} {
		l, ok := layers[b]
		if !ok {
			t.Fatalf("%s is not a layer of the run\n%s", b, human)
		}
		if result != "" && str(l.Result) != result {
			t.Errorf("%s: %s, want %s", b, str(l.Result), result)
		}
	}
	if str(layers["scratch"].Result) == wtsync.MoveHeld {
		t.Errorf("scratch, with untracked files only, was held: %s", str(layers["scratch"].Held))
	}
	for _, b := range []string{"wip", "mid"} {
		if testkit.GitOut(t, main, "rev-parse", b) != before[b] {
			t.Errorf("%s was held and moved", b)
		}
	}
	if testkit.GitOut(t, main, "rev-parse", "scratch") == before["scratch"] {
		t.Error("scratch, held by nothing, did not move")
	}
}

// A line whose only found branch is such a stray keeps it: taken and held, in
// the look and in the run.
func TestSyncRebaseStackKeepsAStrayThatIsTheOnlyLayer(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := strayLine(t, stray{"spike", "tip", "dirty"})
	main := ctx.Repo.MainRoot
	// Leave spike the only found branch.
	for _, b := range []string{"tmp/layer/a", "tmp/layer/b", "tmp/layer/c"} {
		testkit.GitIn(t, main, "branch", "-q", "-D", b)
	}
	look := strayLook(t, ctx)
	if got := layerNames(look); !slices.Equal(got, []string{"spike"}) || len(look.Skipped) != 0 {
		t.Errorf("the look: layers %v, skipped %+v, want spike taken", got, look.Skipped)
	}
	before := testkit.GitOut(t, main, "rev-parse", "spike")
	res, human, _ := synccmdtest.StackRun(t, ctx, "tmp/line", synccmdtest.NoAgents())
	l, ok := layersOf(synccmdtest.Participant(t, res, "tmp/line").Stack)["spike"]
	if !ok || str(l.Result) != wtsync.MoveHeld {
		t.Fatalf("spike in the run: %+v %v, want held\n%s", l, ok, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "spike") != before {
		t.Error("the held spike branch moved")
	}
}

// --layer takes the stray by name or by pattern, and the dirty worktree then
// holds it as always.
func TestSyncRebaseStackLayerStillTakesAStray(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, layers := range map[string][]string{
		"literal": {"tmp/layer/a", "tmp/layer/b", "tmp/layer/c", "spike"},
		"pattern": {"*"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := strayLine(t, stray{"spike", "tip", "dirty"})
			main := ctx.Repo.MainRoot
			look := strayLook(t, ctx, layers...)
			if !slices.Contains(layerNames(look), "spike") {
				t.Fatalf("the look takes %v, want spike among them", layerNames(look))
			}
			if _, ok := skippedReason(look, "spike"); ok {
				t.Error("spike is listed as skipped")
			}
			before := testkit.GitOut(t, main, "rev-parse", "spike")
			opts := synccmdtest.NoAgents()
			opts.Stack = &synccmd.StackOptions{Layers: layers}
			res, human, _ := synccmdtest.StackRun(t, ctx, "tmp/line", opts)
			l, ok := layersOf(synccmdtest.Participant(t, res, "tmp/line").Stack)["spike"]
			if !ok || str(l.Result) != wtsync.MoveHeld {
				t.Fatalf("spike in the run: %+v %v, want held\n%s", l, ok, human)
			}
			if testkit.GitOut(t, main, "rev-parse", "spike") != before {
				t.Error("the held spike branch moved")
			}
		})
	}
}

// Two dirty branches with no commits of their own that share a commit no
// other layer has (x, between b and c) are both taken: neither is judged
// against the other.
func TestSyncRebaseStackKeepsDirtyTwinsAtACommitNoLayerHas(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := strayLine(t, stray{"twin1", "x", "dirty"}, stray{"twin2", "x", "dirty"})
	look := strayLook(t, ctx)
	for _, b := range []string{"twin1", "twin2"} {
		if !slices.Contains(layerNames(look), b) {
			t.Errorf("%s is not taken: layers %v, skipped %+v", b, layerNames(look), look.Skipped)
		}
	}
	if len(look.Skipped) != 0 {
		t.Errorf("skipped %+v, want none", look.Skipped)
	}
}

// What a run with the rule does is what the same run does with --layer
// naming the branches the rule keeps: the branches, trees and subjects, and
// the per-layer step records in order (layer, step, result, the owner a
// step folded into and the commit it made). The stray sits on f1's tip, where
// the generator's output is folded: taken as a layer it would hold f1's
// commits when its name sorts first, and shift the layer numbering either
// way. The generator runs in the rebase (fold) and, on the line as it is, in
// wt sync steps --all (forced).
func TestSyncRebaseStackWithAStrayIsTheRunOfTheKeptLayers(t *testing.T) {
	t.Parallel()
	skipShort(t)
	kept := []string{"f1", "f2", "f3", "f4", "f5"}
	type outcome struct {
		refs  map[string]string
		steps []string
		moved []string
	}
	run := func(t *testing.T, strayName string, named, forced bool) outcome {
		ctx, s := synccmdtest.FoldLine(t, gittest.FoldYAML("max-plus-patch"))
		main := ctx.Repo.MainRoot
		testkit.GitIn(t, main, "branch", strayName, "f1")
		p := filepath.Join(filepath.Dir(s.Paths["f1"]), "stray")
		testkit.GitIn(t, main, "worktree", "add", "-q", p, strayName)
		gittest.WriteFile(t, filepath.Join(p, "src", "core.txt"), "edited\n")
		strayBefore := testkit.GitOut(t, main, "rev-parse", strayName)
		var o outcome
		// forced runs wt sync steps --all on the line as it is: no rebase
		// first, so the stray sits on f1's tip when the layers are read.
		runs := []*synccmd.StepsOptions{nil}
		if forced {
			runs = []*synccmd.StepsOptions{{All: true}}
		}
		for i, steps := range runs {
			opts := synccmdtest.NoAgents()
			opts.Now = func() time.Time { return time.Unix(0, int64(100+i)) }
			opts.Steps = steps
			opts.Stack = &synccmd.StackOptions{}
			if named {
				opts.Stack.Layers = kept
			}
			res, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
			if err != nil && !strings.Contains(err.Error(), "held") {
				t.Fatalf("%v\n%s", err, human)
			}
			part := synccmdtest.Participant(t, res, gittest.FoldTmp)
			for l := range layersOf(part.Stack) {
				o.moved = append(o.moved, l)
			}
			for _, st := range part.LayerSteps {
				if st.Branch == strayName {
					t.Errorf("the stray has a step record: %+v", st)
				}
				row := strings.Join([]string{res.Command, str(&st.Step), st.Branch, st.Result, str(st.OwnerSubject)}, " | ")
				row += " | " + itoa(st.Layer) + "/" + itoa(st.Layers) + " | ran " + boolStr(st.Ran)
				if st.Owner != nil {
					row += " | owner " + testkit.GitOut(t, main, "rev-parse", *st.Owner+"^{tree}")
				}
				if st.Commit != nil {
					row += " | commit " + testkit.GitOut(t, main, "log", "-1", "--format=%s", *st.Commit) + " " + testkit.GitOut(t, main, "rev-parse", *st.Commit+"^{tree}")
				}
				o.steps = append(o.steps, row)
			}
		}
		slices.Sort(o.moved)
		if slices.Contains(o.moved, strayName) {
			t.Errorf("the stray is a layer of the run: %v", o.moved)
		}
		if testkit.GitOut(t, main, "rev-parse", strayName) != strayBefore {
			t.Errorf("the stray moved")
		}
		o.refs = map[string]string{}
		for _, b := range append(slices.Clone(kept), gittest.FoldTmp) {
			o.refs[b] = testkit.GitOut(t, main, "rev-parse", b+"^{tree}") + " " + testkit.GitOut(t, main, "log", "--format=%s", "main.."+b)
		}
		return o
	}
	for _, mode := range []string{"fold", "forced"} {
		for _, strayName := range []string{"aaa-stray", "zzz-stray"} {
			t.Run(mode+"/"+strayName, func(t *testing.T) {
				t.Parallel()
				got := run(t, strayName, false, mode == "forced")
				want := run(t, strayName, true, mode == "forced")
				if len(want.steps) == 0 {
					t.Fatal("no per-layer step ran")
				}
				for b, w := range want.refs {
					if got.refs[b] != w {
						t.Errorf("%s: with the rule\n%s\nwith --layer naming the kept\n%s", b, got.refs[b], w)
					}
				}
				if !slices.Equal(got.steps, want.steps) {
					t.Errorf("step records: with the rule\n  %s\nwith --layer\n  %s", strings.Join(got.steps, "\n  "), strings.Join(want.steps, "\n  "))
				}
				if !slices.Equal(got.moved, want.moved) {
					t.Errorf("layers: with the rule %v, with --layer %v", got.moved, want.moved)
				}
			})
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func boolStr(b bool) string { return strconv.FormatBool(b) }
