package syncfix

import (
	"errors"
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

// A fixup naming its target by revision keeps its gap at a later stop of the
// run: resume refuses a resolution there that drops its hunk, though HEAD has
// moved since the run resolved the name.
func TestResumeKeepsTheGapOfARevisionFixupAtALaterStop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, tmp := twoStopLine(t, "fixup! HEAD~3")
	late := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/rev")
	resolve, state := stopTools(t, tmp)
	if _, out, err := synccmdtest.StackRun(t, ctx, "tmp/rev", runAt(600, &synccmd.StackOptions{Autosquash: true})); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want the first fixup handed over\n%s", err, out)
	}
	resolve("u.txt", "u\nu2\n")
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	if _, err := syncResumeJSON(t, ctx, "tmp/rev", opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume: err %v, want the second fixup handed over", err)
	}
	if st := state(); st.StoppedCommit != late || len(st.Context) != 1 || st.Context[0].Fixup != late {
		t.Fatalf("stopped at %s with context %+v, want the revision fixup and its gap", st.StoppedCommit, st.Context)
	}
	resolve("t.txt", "one\n")
	_, err := syncResumeJSON(t, ctx, "tmp/rev", opts)
	if err == nil || !strings.Contains(err.Error(), "the resolution drops 1 line of") || !strings.Contains(err.Error(), "nothing is resumed") {
		t.Errorf("resume: err %v, want the dropped line refused", err)
	}
}

// An analysis that fails in the run is read again by the resume, so a later
// stop is still checked. Not parallel: it swaps syncrun.AnalyseFixupsOf.
func TestResumeReadsAgainWhatTheRunCouldNotAnalyse(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, tmp := twoStopLine(t, "fixup! feat: b")
	late := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/rev")
	resolve, state := stopTools(t, tmp)
	was := syncrun.AnalyseFixupsOf
	t.Cleanup(func() { syncrun.AnalyseFixupsOf = was })
	syncrun.AnalyseFixupsOf = func(string, []string, *wtsync.Config) (wtsync.FixupAnalysis, error) {
		return wtsync.FixupAnalysis{}, errors.New("merge-tree: out of cheese")
	}
	if _, out, err := synccmdtest.StackRun(t, ctx, "tmp/rev", runAt(601, &synccmd.StackOptions{Autosquash: true})); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want the first fixup handed over\n%s", err, out)
	}
	syncrun.AnalyseFixupsOf = was
	resolve("u.txt", "u\nu2\n")
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	if _, err := syncResumeJSON(t, ctx, "tmp/rev", opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume: err %v, want the second fixup handed over", err)
	}
	if st := state(); st.StoppedCommit != late || len(st.Context) != 1 {
		t.Errorf("stopped at %s with context %+v, want the second fixup's gap", st.StoppedCommit, st.Context)
	}
}

// A dropped hunk of a gap in doubt is refused with no commit to move it to.
// Not parallel: it swaps syncrun.AnalyseFixupsOf.
func TestResumeOffersNoRetargetForADroppedHunkInDoubt(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	was := syncrun.AnalyseFixupsOf
	t.Cleanup(func() { syncrun.AnalyseFixupsOf = was })
	syncrun.AnalyseFixupsOf = func(dir string, commits []string, cfg *wtsync.Config) (wtsync.FixupAnalysis, error) {
		a, err := was(dir, commits, cfg)
		for i := range a.Gaps {
			a.Gaps[i].Doubt = "wt cannot tell"
		}
		return a, err
	}
	ctx, _, _ := handedAtFixup(t, 602, gittest.StateTS("size: 20,", ""))
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	_, err := syncResumeJSON(t, ctx, gittest.ContextTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "the resolution drops 1 line of") || !strings.Contains(err.Error(), "--allow-dropped") {
		t.Fatalf("resume: err %v, want the dropped line refused", err)
	}
	if strings.Contains(err.Error(), "into a fixup of") {
		t.Errorf("resume: %v, offers a retarget for a gap in doubt", err)
	}
}

// twoStopLine is branch tmp/rev in its own worktree: a fixup of "feat: a"
// that stops the fold, and then the fixup named second, which changes a file
// only "feat: c" adds.
func twoStopLine(t *testing.T, second string) (*kit.Context, string) {
	t.Helper()
	var tmp string
	ctx, _ := synccmdtest.OpenLine(t, func(main, wtDir string) {
		commit := func(msg string, files ...string) {
			for i := 0; i < len(files); i += 2 {
				gittest.WriteFile(t, filepath.Join(main, files[i]), files[i+1])
			}
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		commit("base", ".wt-sync.yaml", synccmdtest.StackYAML)
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/rev")
		commit("feat: a", "a.txt", "a\n")
		gittest.Git(t, main, "branch", "low1")
		commit("feat: b", "b.txt", "b\n")
		commit("feat: c", "t.txt", "one\n", "u.txt", "u\n")
		commit("fixup! feat: a", "u.txt", "u\nu2\n")
		commit(second, "t.txt", "one\ntwo\n")
		gittest.Git(t, main, "checkout", "-q", "main")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		tmp = filepath.Join(wtDir, "tmp_rev")
		gittest.Git(t, main, "worktree", "add", "-q", tmp, "tmp/rev")
	})
	return ctx, tmp
}

// stopTools stages a resolution in tmp, and reads the handover's state there.
func stopTools(t *testing.T, tmp string) (resolve func(path, content string), state func() wtsync.State) {
	t.Helper()
	resolve = func(path, content string) {
		t.Helper()
		gittest.WriteFile(t, filepath.Join(tmp, path), content)
		testkit.GitIn(t, tmp, "add", path)
	}
	state = func() wtsync.State {
		t.Helper()
		gitDir, err := wtsync.GitDir(tmp)
		if err != nil {
			t.Fatal(err)
		}
		st, ok, err := wtsync.ReadState(gitDir)
		if err != nil || !ok {
			t.Fatalf("state %v %v", ok, err)
		}
		return st
	}
	return resolve, state
}
