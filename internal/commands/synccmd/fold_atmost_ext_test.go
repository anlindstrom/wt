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

// lineStep is one thing builtLine does to the repository.
type lineStep func(g gitLine)

type gitLine struct {
	t    *testing.T
	main string
}

func (g gitLine) write(file, content string) {
	gittest.WriteFile(g.t, filepath.Join(g.main, file), content)
}

func (g gitLine) commit(msg string, files ...string) {
	for _, f := range files {
		g.write(f, msg+"\n")
	}
	gittest.Git(g.t, g.main, "add", "-A")
	gittest.Git(g.t, g.main, "commit", "-q", "--no-verify", "--allow-empty", "-m", msg)
}

func (g gitLine) git(args ...string) { gittest.Git(g.t, g.main, args...) }

// builtLine is a line on tmp/low in its own worktree, as line makes it, over a
// trunk that declares yaml and then does trunk.
func builtLine(t *testing.T, yaml string, line, trunk lineStep) *kit.Context {
	t.Helper()
	ctx, _ := synccmdtest.OpenLine(t, func(main, wtDir string) {
		g := gitLine{t, main}
		g.write(".wt-sync.yaml", yaml)
		g.commit("base")
		g.git("checkout", "-q", "-b", "tmp/low")
		line(g)
		g.git("checkout", "-q", "main")
		if trunk != nil {
			trunk(g)
		}
		g.git("remote", "add", "origin", main)
		g.git("fetch", "-q", "origin")
		g.git("worktree", "add", "-q", filepath.Join(wtDir, "tmp_low"), "tmp/low")
	})
	return ctx
}

// lookOf is the fold look of tmp/low with docker down.
func lookOf(t *testing.T, ctx *kit.Context, opts synccmd.SyncOptions) string {
	t.Helper()
	opts.NoFetch, opts.Probes = true, synccmdtest.DockerDown().Probes()
	if opts.Stack == nil {
		opts.Stack = &synccmd.StackLook{}
	}
	opts.Stack.Work, opts.Stack.Autosquash = "tmp/low", true
	var look bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, "tmp/low", opts, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	return look.String()
}

// refusedRun runs wt sync with opts and docker down, and wants it refused for
// why.
func refusedRun(t *testing.T, ctx *kit.Context, why string, opts synccmd.RunOptions) {
	t.Helper()
	opts.Probes = synccmdtest.DockerDown().Probes()
	var out, human bytes.Buffer
	command := "sync rebase"
	if opts.Steps != nil {
		command = "sync steps"
	}
	opts.Journal = syncrun.NewSyncRunJournal(&out, command)
	err := synccmd.SyncRebase(ctx, []string{"tmp/low"}, opts, &human)
	if err == nil || !strings.Contains(human.String()+err.Error(), why) {
		t.Fatalf("not refused for %q: %v\n%s", why, err, human.String())
	}
}

func foldRun() synccmd.RunOptions {
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	return opts
}

func wantAll(t *testing.T, look string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(look, want) {
			t.Errorf("the look lacks %q:\n%s", want, look)
		}
	}
}

// twoLayers is a line of feat: low (the layer low1), feat: top and its fixup.
func twoLayers(g gitLine) {
	g.commit("feat: low", "low.txt")
	g.git("branch", "low1")
	g.commit("feat: top", "top.txt")
	g.commit("fixup! feat: top", "top.txt")
}

func owe(t *testing.T, ctx *kit.Context, layer string) {
	t.Helper()
	owed := &wtsync.Owed{Branch: "tmp/low", Epoch: 1, Steps: []wtsync.OwedStep{{Step: "true", Layer: layer, Why: "docker did not answer"}}}
	if err := wtsync.WriteOwed(ctx.Repo.MainRoot, owed); err != nil {
		t.Fatal(err)
	}
}

// The command the look names keeps the options that hold refs back.
func TestFoldLookCommandKeepsTheLooksOptions(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := builtLine(t, "conflicts: []\n", twoLayers, nil)
	got := lookOf(t, ctx, synccmd.SyncOptions{Stack: &synccmd.StackLook{Layers: []string{"low1"}, Alone: true, NoSideBranches: true}})
	wantAll(t, got, "wt sync rebase tmp/low --stack --autosquash --layer 'low1' --alone --no-side-branches folds the line")
}

// wt sync steps runs its steps on a branch behind its own remote, so their
// needs are probed there too.
func TestStepsAllProbesOnABranchBehindItsRemote(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := builtLine(t, "conflicts: []\ndefer:\n  - run: 'true'\n    needs: [docker]\n", func(gitLine) {}, nil)
	main := ctx.Repo.MainRoot
	ahead := testkit.GitOut(t, main, "commit-tree", "tmp/low^{tree}", "-p", "tmp/low", "-m", "feat: theirs")
	testkit.GitIn(t, main, "update-ref", "refs/remotes/origin/tmp/low", ahead)
	testkit.GitIn(t, main, "config", "branch.tmp/low.remote", "origin")
	testkit.GitIn(t, main, "config", "branch.tmp/low.merge", "refs/heads/tmp/low")
	opts := synccmdtest.NoAgents()
	opts.Steps = &synccmd.StepsOptions{All: true}
	refusedRun(t, ctx, "needs docker", opts)
}

// A step owed at a layer with no commit of its own still runs at its marker.
func TestFoldNeedsAStepOwedAtALayerWithNoCommit(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := builtLine(t, "conflicts: []\ndefer:\n  - run: 'true'\n    each: layer\n    paths: [none.txt]\n    needs: [docker]\n",
		func(g gitLine) {
			g.commit("feat: low", "low.txt")
			g.git("branch", "low1")
			g.git("branch", "low2")
			g.commit("feat: top", "top.txt")
			g.commit("fixup! feat: top", "top.txt")
		}, nil)
	owe(t, ctx, "low2")
	wantAll(t, lookOf(t, ctx, synccmd.SyncOptions{}), "docker did not answer")
	refusedRun(t, ctx, "needs docker", foldRun())
}

// A step owed at a lower layer may commit there, so what is above may be
// replayed and checked.
func TestFoldNeedsTheChecksAboveAnOwedStep(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const yaml = "conflicts: []\neach_commit:\n  - run: 'true'\n    paths: [mid.txt]\n    needs: [docker]\ndefer:\n  - run: 'true'\n    each: layer\n    paths: [none.txt]\n"
	ctx := builtLine(t, yaml, func(g gitLine) {
		g.commit("feat: low", "low.txt")
		g.git("branch", "low1")
		g.commit("feat: mid", "mid.txt")
		g.commit("feat: top", "top.txt")
		g.commit("fixup! feat: top", "top.txt")
	}, nil)
	if got := lookOf(t, ctx, synccmd.SyncOptions{}); strings.Contains(got, "did not answer") {
		t.Errorf("with nothing owed, mid is unchanged:\n%s", got)
	}
	owe(t, ctx, "low1")
	wantAll(t, lookOf(t, ctx, synccmd.SyncOptions{}), "docker did not answer", "checks up to 1 commit")
	refusedRun(t, ctx, "needs docker", foldRun())
}

// A pick a version lift edits is not one git leaves alone.
func TestFoldNeedsAStepOfALayerALiftEdits(t *testing.T) {
	t.Parallel()
	skipShort(t)
	yaml := synccmdtest.StackYAML + "defer:\n  - run: 'true'\n    each: layer\n    paths: [v.txt]\n    needs: [docker]\n"
	ctx := builtLine(t, yaml, func(g gitLine) {
		g.commit("feat: low")
		g.write("v.txt", "1.0.1\n")
		g.commit("chore: bump")
		g.git("branch", "low1")
		g.commit("feat: top", "top.txt")
		g.commit("fixup! feat: top", "top.txt")
	}, nil)
	wantAll(t, lookOf(t, ctx, synccmd.SyncOptions{}), "docker did not answer")
	refusedRun(t, ctx, "needs docker", foldRun())
}

// An --onto fold with a fixup that names nothing is refused in the look as in
// the run, move! commit or none.
func TestOntoFoldLookRefusesAFixupThatNamesNothing(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := builtLine(t, "conflicts: []\n", func(g gitLine) {
		twoLayers(g)
		g.commit("fixup! absent", "top.txt")
	}, nil)
	got := lookOf(t, ctx, synccmd.SyncOptions{Onto: "main"})
	wantAll(t, got, "refused: ", "names no commit earlier on the line")
	if strings.Contains(got, "folds the line") || strings.Contains(got, "the rebase moves it") {
		t.Errorf("the refused look promises a fold:\n%s", got)
	}
	opts := foldRun()
	opts.Onto = "main"
	refusedRun(t, ctx, "names no commit earlier on the line", opts)
}

// A targets run's stop at a layer's end is the last commit git keeps, and a
// fold onto another commit may find less to check than the line holds.
func TestOntoFoldLookNamesNoDroppedCommitAndBoundsItsCount(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := builtLine(t, "conflicts: []\neach_commit:\n  - run: 'true'\n", func(g gitLine) {
		g.commit("feat: low", "low.txt")
		g.commit("feat: carried", "carried.txt")
		g.git("branch", "low1")
		g.commit("feat: mid", "mid.txt")
		g.commit("feat: top", "top.txt", "moved.txt")
		g.commit(wtsync.MoveMessage("feat: mid", "feat: top", []string{"moved.txt"}, nil))
	}, func(g gitLine) {
		g.commit("feat: trunk's own", "trunk.txt")
		g.git("cherry-pick", "low1")
	})
	got := lookOf(t, ctx, synccmd.SyncOptions{Onto: "main"})
	list := checkSection(got)
	if !strings.Contains(got, "checks up to ") || !strings.Contains(list, "feat: low") || strings.Contains(list, "feat: carried") {
		t.Errorf("the section:\n%s", got)
	}
}

// A commit made empty on purpose is still checked by a step with no paths.
func TestFoldLookListsAnEmptyCommitItChecks(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := builtLine(t, "conflicts: []\neach_commit:\n  - run: 'true'\n", func(g gitLine) {
		g.commit("feat: low", "low.txt")
		g.git("branch", "low1")
		g.commit("feat: top", "top.txt")
		g.commit("chore: empty")
		g.commit("fixup! feat: top", "top.txt")
	}, nil)
	got := lookOf(t, ctx, synccmd.SyncOptions{})
	wantAll(t, checkSection(got), "feat: top", "chore: empty")
	res, human, err := synccmdtest.StackRun(t, ctx, "tmp/low", foldRun())
	if err != nil || synccmdtest.Participant(t, res, "tmp/low").Checked != 2 || !strings.Contains(got, "checks 2 commits") {
		t.Fatalf("%v\n%s\n%s", err, got, human)
	}
}

// A check whose script a commit does not hold yet may not run there.
func TestFoldLookBoundsTheCountForAMissingScript(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx := builtLine(t, "conflicts: []\neach_commit:\n  - run: bin/check.sh\n", twoLayers,
		func(g gitLine) { g.commit("chore: add the script", "bin/check.sh") })
	wantAll(t, lookOf(t, ctx, synccmd.SyncOptions{}), "checks up to 1 commit")
}
