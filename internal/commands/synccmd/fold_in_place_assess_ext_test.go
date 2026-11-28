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

// collidingLine is a line on tmp/low, with the layer low1, that is divergent
// from trunk: both changed the path /a of spec.json, which openapi refuses. A
// fixup folds into the layer's commit. tmp/other is a worktree a run would
// take, so the overview has a token.
func collidingLine(t *testing.T) (ctx *kit.Context) {
	t.Helper()
	ctx, _ = synccmdtest.OpenLine(t, func(main, wtDir string) {
		write := func(rel, content string) { gittest.WriteFile(t, filepath.Join(main, rel), content) }
		commit := func(msg string) {
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		spec := func(version, paths string) {
			write("spec.json", `{"openapi":"3.1.0","info":{"title":"T","version":"`+version+`"},"tags":[],"paths":`+paths+`,"components":{"schemas":{}}}`)
		}
		spec("1.0.0", `{"/a":{"get":{}}}`)
		write(".wt-sync.yaml", "conflicts:\n  - paths: [spec.json]\n    strategy: openapi\n")
		commit("base")
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/low")
		spec("1.0.1", `{"/a":{"get":{}},"/b":{"get":{}}}`)
		write("b.txt", "b\n")
		commit("feat: b")
		gittest.Git(t, main, "branch", "low1")
		spec("1.0.2", `{"/a":{"get":{"x":2}},"/b":{"get":{}}}`)
		commit("feat: a")
		write("b.txt", "b fixed\n")
		commit("fixup! feat: b")
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/other", "main")
		write("other.txt", "other\n")
		commit("feat: other")
		gittest.Git(t, main, "checkout", "-q", "main")
		spec("1.0.5", `{"/a":{"get":{"x":1}},"/t":{"get":{}}}`)
		commit("feat: trunk's own")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		for _, b := range []string{"tmp/low", "tmp/other"} {
			gittest.Git(t, main, "worktree", "add", "-q", filepath.Join(wtDir, strings.ReplaceAll(b, "/", "_")), b)
		}
	})
	return ctx
}

// A fold in place never meets trunk: a line divergent from it is folded.
func TestInPlaceFoldOfALineDivergentFromTrunk(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := collidingLine(t)
	main := ctx.Repo.MainRoot
	got := foldLook(t, ctx, "tmp/low", false)
	for _, want := range []string{"folds the line in place", "re-pointed: low1"} {
		if !strings.Contains(got, want) {
			t.Errorf("the look lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "refused") {
		t.Errorf("the look refuses the fold:\n%s", got)
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
}

// A fold that is assessed against trunk, or the --onto target, still refuses
// the divergent line: with --onto, --if-ready or --expect.
func TestFoldAssessedAgainstTrunkStillRefusesADivergentLine(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := collidingLine(t)
	var look bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true, Onto: "main",
		Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	if !strings.Contains(look.String(), "refused: divergent") {
		t.Errorf("the --onto look does not refuse:\n%s", look.String())
	}
	token := synccmdtest.OverviewJSON(t, ctx).Token
	if token == nil {
		t.Fatal("no token")
	}
	old := tipsOf(t, ctx.Repo.MainRoot, "tmp/low", "low1")
	for name, set := range map[string]func(*synccmd.RunOptions){
		"onto":     func(o *synccmd.RunOptions) { o.Onto = "main" },
		"if-ready": func(o *synccmd.RunOptions) { o.IfReady = true },
		"expect":   func(o *synccmd.RunOptions) { o.Expect = *token },
	} {
		opts := synccmdtest.NoAgents()
		opts.Stack = &synccmd.StackOptions{Autosquash: true}
		set(&opts)
		_, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
		if err == nil || !strings.Contains(human, "refused: tmp/low: divergent") {
			t.Errorf("%s: err %v\n%s", name, err, human)
		}
	}
	for b, tip := range tipsOf(t, ctx.Repo.MainRoot, "tmp/low", "low1") {
		if tip != old[b] {
			t.Errorf("a refused run moved %s", b)
		}
	}
}

// A lower layer checked out in its own worktree is still part of the line a
// fold checks, whether or not trunk has moved: for a need only its commit has,
// the look lists the commit and says a run refuses, and the run does.
func TestInPlaceFoldLookChecksTheCommitsBelowACheckedOutLayer(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, trunkMoved := range map[string]bool{"behind trunk": true, "current with trunk": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := fixedLine(t, "low", "each_commit:\n  - run: 'true'\n    paths: [low.txt]\n    needs: [docker]\n", func(_ string, commit func(string, string)) {
				if trunkMoved {
					commit("trunk.txt", "feat: trunk's own")
				}
			})
			got := needLook(t, ctx)
			for _, want := range []string{"docker did not answer", "a run refuses the worktrees whose steps need it"} {
				if !strings.Contains(got, want) {
					t.Errorf("the look lacks %q:\n%s", want, got)
				}
			}
			if list := checkSection(got); !strings.Contains(list, "feat: low") || strings.Contains(list, "not checked") {
				t.Errorf("the each_commit section lacks the lower commit:\n%s", got)
			}
			// The need refuses the fold in both looks, so neither plans a move.
			wantAll(t, got, "refused: ", "needs docker")
			if strings.Contains(got, "1 fixup folds") || strings.Contains(got, "the rebase moves it") {
				t.Errorf("the refused look plans the fold:\n%s", got)
			}
			o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Probes: synccmdtest.DockerDown().Probes(),
				Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}})
			if f := o.Fold; f == nil || f.Refusal == nil || !strings.Contains(*f.Refusal, "needs docker") || len(f.Folds) != 0 {
				t.Errorf("--json fold %+v", o.Fold)
			}
			refusedForDocker(t, ctx, func(o *synccmd.RunOptions) { o.Stack = &synccmd.StackOptions{Autosquash: true} })
		})
	}
}

// A look of a move with --onto keeps its fold detail for a line with no layer:
// the refusal of such a line is said by the look of a fold in place.
func TestTargetedMoveLookOfALineWithNoLayerShowsItsFold(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := divergedLine(t, "t.txt", "feat: b", 0)
	testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--allow-empty", "-m", "move! feat: x")
	testkit.GitIn(t, ctx.Repo.MainRoot, "branch", "-q", "-D", "low1")
	var look bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true, Onto: "main",
		Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	if !strings.Contains(look.String(), "tmp/low  folds onto main") {
		t.Errorf("the look lacks its fold detail:\n%s", look.String())
	}
}
