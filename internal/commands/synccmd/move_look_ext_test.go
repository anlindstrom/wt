package synccmd_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// The look at a fold names the move! commit, what it moves from where, and
// that the run needs --each-commit=targets; --json says the same, as a fold
// of kind move. Every layer from the target up is re-pointed.
func TestMoveLookNamesTheMove(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.MoveLine(t, false, true)
	main := ctx.Repo.MainRoot
	look := &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}
	var out bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, gittest.MoveTmp, synccmd.SyncOptions{NoFetch: true, Stack: look}, &out); err != nil {
		t.Fatalf("look: %v\n%s", err, out.String())
	}
	source := testkit.GitOut(t, main, "rev-parse", "--short=7", "l3")
	for _, want := range []string{
		"1 move folds in place",
		"move! " + gittest.MoveTarget + "  → " + testkit.GitOut(t, main, "rev-parse", "--short=7", "l1") + " " + gittest.MoveTarget + "  (layer l1)",
		"moves " + gittest.MovePath + " of " + source + " " + gittest.MoveSource + "  (layer l3)",
		"--each-commit=targets",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the look does not say %q:\n%s", want, out.String())
		}
	}
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: look})
	f := o.Fold
	if f == nil || f.Refusal != nil || len(f.Folds) != 1 {
		t.Fatalf("fold %+v", f)
	}
	p := f.Folds[0]
	if p.Kind != "move" || p.TargetLayer != "l1" || p.Move == nil || p.Move.SourceLayer != "l3" || p.Move.Source != s.Tips["l3"] || !slices.Equal(p.Move.Paths, []string{gittest.MovePath}) {
		t.Errorf("pair %+v move %+v", p, p.Move)
	}
	if strings.Join(f.Repointed, " ") != "l1 l2 l3" || len(f.Unchanged) != 0 {
		t.Errorf("repointed %q unchanged %q", f.Repointed, f.Unchanged)
	}
}

// The look says the run's refusal of a move whose context a commit in
// between carries, in the run's words, and lists no fold.
func TestMoveLookSaysTheRefusal(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, _ := synccmdtest.MoveLine(t, true, true)
	between := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "--short=7", "l2")
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}})
	if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, between) || len(o.Fold.Folds) != 0 {
		t.Fatalf("fold %+v", o.Fold)
	}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error()+human, *o.Fold.Refusal) {
		t.Errorf("the run refuses with %v, not the look's %q\n%s", err, *o.Fold.Refusal, human)
	}
}

// The look of a fold with a move! commit lists the commits the targets run
// checks, in order, and counts them: the tip of the unchanged layer below the
// target only when a step is owed there.
func TestMoveLookListsTheStopsTheRunChecks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, owedBelow := range map[string]bool{"nothing owed": false, "owed below": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			log := filepath.Join(t.TempDir(), "checked.log")
			yaml := "each_commit:\n  - run: git log -1 --format=%s >> '" + log + "'\ndefer:\n  - run: 'true'\n    each: layer\n    paths: [none.txt]\n"
			ctx, _ := synccmdtest.OpenLine(t, func(main, wtDir string) {
				commit := func(msg string, files ...string) {
					for _, f := range files {
						gittest.WriteFile(t, filepath.Join(main, f), f+"\n")
					}
					gittest.Git(t, main, "add", "-A")
					gittest.Git(t, main, "commit", "-q", "--no-verify", "--allow-empty", "-m", msg)
				}
				gittest.WriteFile(t, filepath.Join(main, ".wt-sync.yaml"), "conflicts: []\n"+yaml)
				commit("base")
				gittest.Git(t, main, "checkout", "-q", "-b", "tmp/low")
				commit("feat: low", "low.txt")
				gittest.Git(t, main, "branch", "low1")
				commit("feat: mid", "mid.txt")
				gittest.Git(t, main, "branch", "mid1")
				commit("feat: top", "top.txt", "moved.txt")
				commit(wtsync.MoveMessage("feat: mid", "feat: top", []string{"moved.txt"}, nil))
				gittest.Git(t, main, "checkout", "-q", "main")
				gittest.Git(t, main, "remote", "add", "origin", main)
				gittest.Git(t, main, "fetch", "-q", "origin")
				gittest.Git(t, main, "worktree", "add", "-q", filepath.Join(wtDir, "tmp_low"), "tmp/low")
			})
			if owedBelow {
				owed := &wtsync.Owed{Branch: "tmp/low", Epoch: 1, Steps: []wtsync.OwedStep{{Step: "true", Layer: "low1", Why: "docker did not answer"}}}
				if err := wtsync.WriteOwed(ctx.Repo.MainRoot, owed); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true,
				Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}, &out); err != nil {
				t.Fatalf("look: %v\n%s", err, out.String())
			}
			var listed []string
			for _, line := range strings.Split(checkSection(out.String()), "\n")[1:] {
				if _, subject, ok := strings.Cut(strings.TrimSpace(line), "  "); ok && !strings.Contains(subject, "(not checked") {
					listed = append(listed, subject)
				}
			}
			opts := synccmdtest.NoAgents()
			opts.Stack = &synccmd.StackOptions{Autosquash: true}
			opts.EachCommit = "targets"
			if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts); err != nil {
				t.Fatalf("%v\n%s", err, human)
			}
			ran := strings.Split(strings.TrimSpace(read(t, log)), "\n")
			if !slices.Equal(listed, ran) || slices.Contains(ran, "feat: low") != owedBelow {
				t.Errorf("the look lists %q, the run checked %q\n%s", listed, ran, out.String())
			}
			bound := ""
			if owedBelow {
				bound = "up to "
			}
			if want := fmt.Sprintf("checks %s%d commits with the each_commit steps under --each-commit=targets", bound, len(ran)); !strings.Contains(out.String(), want) {
				t.Errorf("the look lacks %q:\n%s", want, out.String())
			}
		})
	}
}
