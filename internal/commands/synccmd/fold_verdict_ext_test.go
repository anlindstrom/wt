package synccmd_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// needLine is fixedLine with its fixup in the top layer.
func needLine(t *testing.T, steps string, advance func(main string, commit func(file, msg string))) *kit.Context {
	t.Helper()
	return fixedLine(t, "top", steps, advance)
}

// fixedLine is a line on tmp/low, with the layer low1 (feat: low, low.txt) and
// feat: top above it, then a fixup of the commit of fix (low or top), each
// branch in a worktree. Trunk declares steps; advance then commits one file to
// trunk, if it wants to.
func fixedLine(t *testing.T, fix, steps string, advance func(main string, commit func(file, msg string))) *kit.Context {
	t.Helper()
	ctx, _ := synccmdtest.OpenLine(t, func(main, wtDir string) {
		write := func(rel, content string) { gittest.WriteFile(t, filepath.Join(main, rel), content) }
		commit := func(msg string) {
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		write(".wt-sync.yaml", "conflicts: []\n"+steps)
		commit("base")
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/low")
		write("low.txt", "low\n")
		commit("feat: low")
		gittest.Git(t, main, "branch", "low1")
		write("top.txt", "top\n")
		commit("feat: top")
		write(fix+".txt", fix+" fixed\n")
		commit("fixup! feat: " + fix)
		gittest.Git(t, main, "checkout", "-q", "main")
		advance(main, func(file, msg string) {
			write(file, file+"\n")
			commit(msg)
		})
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		for _, b := range []string{"tmp/low", "low1"} {
			gittest.Git(t, main, "worktree", "add", "-q", filepath.Join(wtDir, strings.ReplaceAll(b, "/", "_")), b)
		}
	})
	return ctx
}

// needLook is the look of the fold of needLine with docker down.
func needLook(t *testing.T, ctx *kit.Context) string {
	t.Helper()
	var look bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true, Probes: synccmdtest.DockerDown().Probes(),
		Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	return look.String()
}

// checkSection is the look's each_commit section, "" with none.
func checkSection(look string) string {
	_, after, ok := strings.Cut(look, " with the each_commit steps")
	if !ok {
		return ""
	}
	section, _, _ := strings.Cut(after, "▸")
	return section
}

// refusedForDocker runs needLine's worktree with docker down and checks it is
// refused for the need, moving nothing.
func refusedForDocker(t *testing.T, ctx *kit.Context, set func(*synccmd.RunOptions)) {
	t.Helper()
	old := tipsOf(t, ctx.Repo.MainRoot, "tmp/low", "low1")
	opts := synccmdtest.NoAgents()
	opts.Probes = synccmdtest.DockerDown().Probes()
	set(&opts)
	_, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
	if err == nil || !strings.Contains(human, "refused") || !strings.Contains(human, "needs docker") {
		t.Fatalf("the run was not refused for the need: %v\n%s", err, human)
	}
	for b, tip := range tipsOf(t, ctx.Repo.MainRoot, "tmp/low", "low1") {
		if tip != old[b] {
			t.Errorf("a refused run moved %s", b)
		}
	}
}

// A path only trunk changed is no part of a fold in place, guarded or not:
// its need neither warns nor refuses. A rebase onto trunk still counts it.
func TestInPlaceFoldNeedsNothingOfPathsOnlyTrunkChanged(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, guarded := range map[string]bool{"plain": false, "if-ready": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := needLine(t, "defer:\n  - run: 'true'\n    paths: [trunk.txt]\n    needs: [docker]\n",
				func(_ string, commit func(string, string)) { commit("trunk.txt", "feat: trunk's own") })
			if got := needLook(t, ctx); strings.Contains(got, "did not answer") {
				t.Errorf("the look warns of a need the fold never has:\n%s", got)
			}
			refusedForDocker(t, ctx, func(o *synccmd.RunOptions) { o.Stack = &synccmd.StackOptions{} })
			opts := synccmdtest.NoAgents()
			opts.Probes = synccmdtest.DockerDown().Probes()
			opts.Stack = &synccmd.StackOptions{Autosquash: true}
			opts.IfReady = guarded
			if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts); err != nil {
				t.Fatalf("%v\n%s", err, human)
			}
		})
	}
}

// A commit trunk has cherry-picked is still replayed by a fold on the line's
// own base, so its check's need counts.
func TestInPlaceFoldNeedsTheCheckOfACommitTrunkAlsoCarries(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := fixedLine(t, "low", "each_commit:\n  - run: 'true'\n    paths: [low.txt]\n    needs: [docker]\n",
		func(main string, commit func(string, string)) {
			commit("trunk.txt", "feat: trunk's own")
			gittest.Git(t, main, "cherry-pick", "low1")
		})
	if got := needLook(t, ctx); !strings.Contains(got, "docker did not answer") || !strings.Contains(checkSection(got), "feat: low") {
		t.Errorf("the look hides the check:\n%s", got)
	}
	refusedForDocker(t, ctx, func(o *synccmd.RunOptions) { o.Stack = &synccmd.StackOptions{Autosquash: true} })
}

// The look lists the checks the fold runs, a fixup checked as part of the
// commit it folds into, and says what decides the list.
func TestFoldLookListsTheChecksTheRunRuns(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := needLine(t, "each_commit:\n  - run: 'true'\n    paths: [low.txt, top.txt]\n", func(string, func(string, string)) {})
	got := needLook(t, ctx)
	for _, want := range []string{"checks 1 commit with the each_commit steps", "fixups folded in first", "feat: top",
		"feat: low  (not checked: the fold leaves layer low1 unchanged)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the look lacks %q:\n%s", want, got)
		}
	}
	if list := checkSection(got); strings.Contains(list, "fixup! feat: top") || !strings.Contains(list, "feat: top") {
		t.Errorf("the look lists the fixup as a check:\n%s", got)
	}
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	if n, p := strings.Count(human, "▸ check "), synccmdtest.Participant(t, res, "tmp/low"); n != 1 || p.Checked != 1 {
		t.Errorf("the run checked %d (logged %d), the look said 1:\n%s", p.Checked, n, human)
	}
	if !strings.Contains(human, " feat: top\n") {
		t.Errorf("the run did not check feat: top:\n%s", human)
	}
}

// currentDiverged is a line current with trunk whose own remote has a commit
// it never had, with a deferred step declared.
func currentDiverged(t *testing.T) *kit.Context {
	t.Helper()
	ctx := needLine(t, "defer:\n  - run: 'true'\n", func(string, func(string, string)) {})
	main := ctx.Repo.MainRoot
	wt, err := kit.Locate(ctx, "tmp/low")
	if err != nil {
		t.Fatal(err)
	}
	// Committed detached, so the branch's reflog never held it.
	testkit.GitIn(t, wt.Path, "checkout", "-q", "--detach", "HEAD~1")
	gittest.WriteFile(t, filepath.Join(wt.Path, "theirs.txt"), "theirs\n")
	testkit.GitIn(t, wt.Path, "add", "-A")
	testkit.GitIn(t, wt.Path, "commit", "-q", "--no-verify", "-m", "feat: theirs")
	testkit.GitIn(t, main, "update-ref", "refs/remotes/origin/tmp/low", testkit.GitOut(t, wt.Path, "rev-parse", "HEAD"))
	testkit.GitIn(t, wt.Path, "checkout", "-q", "tmp/low")
	testkit.GitIn(t, main, "config", "branch.tmp/low.remote", "origin")
	testkit.GitIn(t, main, "config", "branch.tmp/low.merge", "refs/heads/tmp/low")
	return ctx
}

func ownSkipReason(t *testing.T, res syncrun.RunResult) string {
	t.Helper()
	p := synccmdtest.Participant(t, res, "tmp/low")
	if p.OwnRemoteSync.SkippedReason == nil {
		return ""
	}
	return *p.OwnRemoteSync.SkippedReason
}

// A line current with trunk is still rewritten by a fold, onto its base or
// onto a target, so its own remote's commits keep the run off it until
// --allow-diverged: the look says so and the run does it.
func TestFoldOfACurrentLineDivergedFromItsRemoteNeedsAllowDiverged(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, onto := range map[string]string{"in place": "", "onto main": "main"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := currentDiverged(t)
			var look bytes.Buffer
			if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true, Onto: onto,
				Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}, &look); err != nil {
				t.Fatalf("%v\n%s", err, look.String())
			}
			for _, want := range []string{"refused: origin/tmp/low has 1 commit", "the run refuses the fold, and nothing moves"} {
				if onto == "" && !strings.Contains(look.String(), want) || !strings.Contains(look.String(), "refused: origin/tmp/low") {
					t.Errorf("the look lacks %q:\n%s", want, look.String())
				}
			}
			if strings.Contains(look.String(), "1 fixup folds") || strings.Contains(look.String(), "re-pointed") || strings.Contains(look.String(), "the rebase moves it") {
				t.Errorf("the refused look plans a fold:\n%s", look.String())
			}
			if onto == "" {
				o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}})
				f := o.Fold
				if f == nil || f.Refusal == nil || !strings.Contains(*f.Refusal, "origin/tmp/low has 1 commit") || len(f.Folds)+len(f.Unchanged)+len(f.Repointed) != 0 {
					t.Errorf("--json fold %+v", f)
				}
				plain := synccmdtest.OverviewJSON(t, ctx)
				if (o.Token == nil) != (plain.Token == nil) || o.Token != nil && *o.Token != *plain.Token {
					t.Errorf("the token changed: %v, %v", o.Token, plain.Token)
				}
				for _, row := range o.Worktrees {
					if row.Branch != nil && *row.Branch == "tmp/low" && row.Verdict != synccmd.VerdictSkip {
						t.Errorf("the survey's verdict changed: %s", row.Verdict)
					}
				}
			}
			old := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/low")
			opts := synccmdtest.NoAgents()
			opts.Onto = onto
			opts.Stack = &synccmd.StackOptions{Autosquash: true}
			res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
			if err == nil || !strings.Contains(human, "--allow-diverged") || testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/low") != old {
				t.Fatalf("not refused: %v\n%s", err, human)
			}
			if got := ownSkipReason(t, res); got != "diverged" {
				t.Errorf("skippedReason %q on the refusal", got)
			}
			opts.AllowDiverged = true
			res, human, err = synccmdtest.StackRun(t, ctx, "tmp/low", opts)
			if err != nil || testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/low") == old {
				t.Fatalf("--allow-diverged did not fold: %v\n%s", err, human)
			}
			if got := ownSkipReason(t, res); got != "" || !synccmdtest.Participant(t, res, "tmp/low").OwnRemoteSync.Allowed {
				t.Errorf("skippedReason %q, or not marked allowed, on the allowed fold", got)
			}
		})
	}
}

// A run that has nothing to rewrite still skips the diverged line, with no
// skippedReason.
func TestCurrentDivergedLineAPlainStackRunSkips(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := currentDiverged(t)
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", synccmdtest.NoAgents())
	if err != nil || !strings.Contains(human, "skipped: already on trunk") {
		t.Fatalf("not skipped: %v\n%s", err, human)
	}
	if got := ownSkipReason(t, res); got != "" {
		t.Errorf("skippedReason %q on an ordinary skip", got)
	}
}

// An --onto fold with no move! commit, current or behind, is not called
// skipped, and the command the look names carries the fold's flags.
func TestOntoFoldLookNamesTheCommandThatFolds(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, trunkMoved := range map[string]bool{"current": false, "behind": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := needLine(t, "", func(_ string, commit func(string, string)) {
				if trunkMoved {
					commit("trunk.txt", "feat: trunk's own")
				}
			})
			var look bytes.Buffer
			if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true, Onto: "main",
				Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}, &look); err != nil {
				t.Fatalf("%v\n%s", err, look.String())
			}
			if want := "wt sync rebase tmp/low --stack --autosquash --onto main folds the line"; !strings.Contains(look.String(), want) {
				t.Errorf("the look lacks %q:\n%s", want, look.String())
			}
			old := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/low")
			opts := synccmdtest.NoAgents()
			opts.Onto = "main"
			opts.Stack = &synccmd.StackOptions{Autosquash: true}
			if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts); err != nil || testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/low") == old {
				t.Fatalf("the run did not fold: %v\n%s", err, human)
			}
		})
	}
}

// An --onto look of a line the run refuses as a stack says that refusal and
// probes nothing, with or without a move! commit.
func TestOntoFoldLookAppliesTheRunsStackChecks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const need = "defer:\n  - run: 'true'\n    needs: [docker]\n"
	for name, c := range map[string]struct {
		steps, why string
		noLayer    bool
	}{
		"no layer":    {steps: need, why: "no layer branch", noLayer: true},
		"declaration": {steps: need + "  - run: 'true'\n    fold: owner\n    paths: [low.txt]\n", why: "each: layer"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := needLine(t, c.steps, func(string, func(string, string)) {})
			if c.noLayer {
				low, err := kit.Locate(ctx, "low1")
				if err != nil {
					t.Fatal(err)
				}
				testkit.GitIn(t, ctx.Repo.MainRoot, "worktree", "remove", low.Path)
				testkit.GitIn(t, ctx.Repo.MainRoot, "branch", "-q", "-D", "low1")
			}
			var look bytes.Buffer
			if err := synccmd.SyncWorktree(ctx, "tmp/low", synccmd.SyncOptions{NoFetch: true, Onto: "main", Probes: synccmdtest.DockerDown().Probes(),
				Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}}, &look); err != nil {
				t.Fatalf("%v\n%s", err, look.String())
			}
			got := look.String()
			if !strings.Contains(got, "refused: ") || !strings.Contains(got, c.why) || strings.Contains(got, "folds the line") || strings.Contains(got, "did not answer") {
				t.Errorf("the look does not say only the refusal %q:\n%s", c.why, got)
			}
			opts := synccmdtest.NoAgents()
			opts.Onto = "main"
			opts.Stack = &synccmd.StackOptions{Autosquash: true}
			var out, human bytes.Buffer
			opts.Journal = syncrun.NewSyncRunJournal(&out, "sync rebase")
			if err := synccmd.SyncRebase(ctx, []string{"tmp/low"}, opts, &human); err == nil || !strings.Contains(err.Error(), c.why) {
				t.Errorf("the run: %v\n%s", err, human.String())
			}
		})
	}
}

// wt sync steps rewrites a current line too. It takes no --allow-diverged, so
// its refusal names what does work.
func TestStepsRefuseACurrentLineDivergedFromItsRemote(t *testing.T) {
	t.Parallel()
	skipShort(t)
	for name, stack := range map[string]bool{"branch": false, "stack": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := currentDiverged(t)
			old := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/low")
			opts := synccmdtest.NoAgents()
			opts.Steps = &synccmd.StepsOptions{All: true}
			if stack {
				opts.Stack = &synccmd.StackOptions{}
			}
			var human bytes.Buffer
			opts.Journal = syncrun.NewSyncRunJournal(&bytes.Buffer{}, "sync steps")
			err := synccmd.SyncRebase(ctx, []string{"tmp/low"}, opts, &human)
			got := human.String()
			if err == nil || !strings.Contains(got, "pull it in, then run wt sync steps again") || strings.Contains(got, "--allow-diverged") {
				t.Fatalf("not refused with a remedy steps has: %v\n%s", err, got)
			}
			if testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/low") != old {
				t.Error("a refused run moved tmp/low")
			}
		})
	}
}

// wt sync rebase --stack --edit rewrites a current line as well.
func TestEditOfACurrentLineDivergedFromItsRemoteIsRefused(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := currentDiverged(t)
	main := ctx.Repo.MainRoot
	old := tipsOf(t, main, "tmp/low", "low1")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Edit: old["low1"]}
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
	if err == nil || !strings.Contains(human, "refused: tmp/low: origin/tmp/low has 1 commit") {
		t.Fatalf("not refused: %v\n%s", err, human)
	}
	if got := ownSkipReason(t, res); got != "diverged" {
		t.Errorf("skippedReason %q", got)
	}
	for b, tip := range tipsOf(t, main, "tmp/low", "low1") {
		if tip != old[b] {
			t.Errorf("a refused run moved %s", b)
		}
	}
}

// A check only an unchanged lower layer calls for is not run, so its need
// neither warns nor refuses.
func TestInPlaceFoldNeedsNothingOfAnUnchangedLayersCheck(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := needLine(t, "each_commit:\n  - run: 'true'\n    paths: [low.txt]\n    needs: [docker]\n", func(string, func(string, string)) {})
	got := needLook(t, ctx)
	if strings.Contains(got, "did not answer") || !strings.Contains(checkSection(got), "feat: low  (not checked") {
		t.Errorf("the look:\n%s", got)
	}
	opts := synccmdtest.NoAgents()
	opts.Probes = synccmdtest.DockerDown().Probes()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
	if err != nil || synccmdtest.Participant(t, res, "tmp/low").Checked != 0 {
		t.Fatalf("%v\n%s", err, human)
	}
}

// A step owed at an unchanged layer runs there whatever its paths, so its
// unmet need refuses the fold. The commit there is still not checked, in the
// look as in the run.
func TestInPlaceFoldNeedsAStepOwedAtAnUnchangedLayer(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const steps = "each_commit:\n  - run: 'true'\n    paths: [low.txt]\ndefer:\n  - run: 'true'\n    each: layer\n    paths: [trunk.txt]\n    needs: [docker]\n"
	ctx := needLine(t, steps, func(_ string, commit func(string, string)) { commit("trunk.txt", "feat: trunk's own") })
	owed := &wtsync.Owed{Branch: "tmp/low", Epoch: 1, Steps: []wtsync.OwedStep{{Step: "true", Layer: "low1", Why: "docker did not answer"}}}
	if err := wtsync.WriteOwed(ctx.Repo.MainRoot, owed); err != nil {
		t.Fatal(err)
	}
	got := needLook(t, ctx)
	if !strings.Contains(got, "docker did not answer") || !strings.Contains(checkSection(got), "feat: low  (not checked") {
		t.Errorf("the look:\n%s", got)
	}
	refusedForDocker(t, ctx, func(o *synccmd.RunOptions) { o.Stack = &synccmd.StackOptions{Autosquash: true} })
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
	if err != nil || synccmdtest.Participant(t, res, "tmp/low").Checked != 0 || !strings.Contains(human, "step true layer 1/2 low1") {
		t.Fatalf("%v\n%s", err, human)
	}
}

// wt sync steps --all forces every step, so each one's need counts.
func TestStepsAllNeedsEveryForcedStep(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := needLine(t, "defer:\n  - run: 'true'\n    paths: [nothing.txt]\n    needs: [docker]\n", func(string, func(string, string)) {})
	opts := synccmdtest.NoAgents()
	opts.Probes = synccmdtest.DockerDown().Probes()
	opts.Steps = &synccmd.StepsOptions{All: true}
	var out, human bytes.Buffer
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync steps")
	if err := synccmd.SyncRebase(ctx, []string{"tmp/low"}, opts, &human); err == nil || !strings.Contains(human.String(), "needs docker") {
		t.Fatalf("not refused for the need: %v\n%s", err, human.String())
	}
}

// A fixup that puts a watched file back leaves its commit nothing to check.
func TestFoldLookCountsWhatTheFoldedCommitChanges(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := synccmdtest.OpenLine(t, func(main, wtDir string) {
		write := func(rel, content string) { gittest.WriteFile(t, filepath.Join(main, rel), content) }
		commit := func(msg string) {
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		write(".wt-sync.yaml", "conflicts: []\neach_commit:\n  - run: 'true'\n    paths: [watched.txt]\n    needs: [docker]\n")
		commit("base")
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/low")
		write("low.txt", "low\n")
		commit("feat: low")
		gittest.Git(t, main, "branch", "low1")
		write("top.txt", "top\n")
		write("watched.txt", "watched\n")
		commit("feat: top")
		gittest.Git(t, main, "rm", "-q", "watched.txt")
		commit("fixup! feat: top")
		gittest.Git(t, main, "checkout", "-q", "main")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		gittest.Git(t, main, "worktree", "add", "-q", filepath.Join(wtDir, "tmp_low"), "tmp/low")
	})
	got := needLook(t, ctx)
	// The fixup itself changes the watched file, but is no commit of its own.
	if checkSection(got) != "" || strings.Contains(got, "did not answer") {
		t.Errorf("the look counts a check the fold never runs:\n%s", got)
	}
	opts := synccmdtest.NoAgents()
	opts.Probes = synccmdtest.DockerDown().Probes()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", opts)
	if err != nil || synccmdtest.Participant(t, res, "tmp/low").Checked != 0 {
		t.Fatalf("%v\n%s", err, human)
	}
}
