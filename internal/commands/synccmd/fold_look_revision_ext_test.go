package synccmd_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// revisionLine is branch tmp/rev in its own worktree, on trunk's tip, with
// the layer low1 at "feat: a" and then each step as subject, file, content.
// The main checkout stays on trunk, so a revision names another commit there.
func revisionLine(t *testing.T, steps ...[3]string) (ctx *kit.Context, tmp string) {
	t.Helper()
	return revisionLineUnder(t, synccmdtest.StackYAML, steps...)
}

// revisionLineUnder is revisionLine with declaration on trunk.
func revisionLineUnder(t *testing.T, declaration string, steps ...[3]string) (ctx *kit.Context, tmp string) {
	t.Helper()
	ctx, _ = synccmdtest.OpenLine(t, func(main, wtDir string) {
		commit := func(msg, rel, content string) {
			gittest.WriteFile(t, filepath.Join(main, rel), content)
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", msg)
		}
		commit("base", ".wt-sync.yaml", declaration)
		gittest.Git(t, main, "checkout", "-q", "-b", "tmp/rev")
		commit("feat: a", "a.txt", "a\n")
		gittest.Git(t, main, "branch", "low1")
		for _, s := range steps {
			commit(s[0], s[1], s[2])
		}
		gittest.Git(t, main, "checkout", "-q", "main")
		gittest.Git(t, main, "remote", "add", "origin", main)
		gittest.Git(t, main, "fetch", "-q", "origin")
		tmp = filepath.Join(wtDir, "tmp_rev")
		gittest.Git(t, main, "worktree", "add", "-q", tmp, "tmp/rev")
	})
	return ctx, tmp
}

// lookWithNotices is the look at the in-place fold of work, and what it said
// while working.
func lookWithNotices(t *testing.T, ctx *kit.Context, work string) (look, notices string) {
	t.Helper()
	var out, progress bytes.Buffer
	opts := synccmd.SyncOptions{NoFetch: true, Progress: &progress, Stack: &synccmd.StackLook{Work: work, Autosquash: true}}
	if err := synccmd.SyncWorktree(ctx, work, opts, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String(), progress.String()
}

// A fixup naming its target by a revision of the line's worktree folds in the
// run, so the look names that target, with no refusal said early or late.
func TestFoldLookResolvesAFixupRevisionInTheLinesWorktree(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := revisionLine(t,
		[3]string{"feat: b", "b.txt", "b\n"},
		[3]string{"fixup! HEAD~1", "b.txt", "b fixed\n"})
	main := ctx.Repo.MainRoot
	target := testkit.GitOut(t, main, "rev-parse", "--short=7", "tmp/rev~1")
	look, notices := lookWithNotices(t, ctx, "tmp/rev")
	if strings.Contains(look, "refuse") || strings.Contains(notices, "refuse") {
		t.Errorf("the look refuses:\n%s%s", notices, look)
	}
	if want := "→ " + target + " feat: b"; !strings.Contains(look, want) {
		t.Errorf("the look lacks %q:\n%s", want, look)
	}
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/rev", opts); err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	if got := testkit.GitOut(t, tmp, "log", "--format=%s", "-2"); got != "feat: b\nfeat: a" {
		t.Errorf("the folded line ends %q", got)
	}
	if got := testkit.GitOut(t, tmp, "show", "HEAD:b.txt"); got != "b fixed" {
		t.Errorf("b.txt in feat: b is %q", got)
	}
}

// The context analysis resolves the revision there too: the look names the
// gap the run stops at.
func TestFoldLookFindsTheGapOfAFixupNamedByARevision(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := revisionLine(t,
		[3]string{"feat: b", "t.txt", "one\n"},
		[3]string{"feat: c", "c.txt", "c\n"},
		[3]string{"fixup! HEAD~3", "t.txt", "one\ntwo\n"})
	look, _ := lookWithNotices(t, ctx, "tmp/rev")
	if want := "fixup moved ahead of its context: t.txt:2 needs"; !strings.Contains(look, want) {
		t.Errorf("the look lacks %q:\n%s", want, look)
	}
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	fixup := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "tmp/rev")
	if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/rev", opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want the fixup handed over\n%s", err, human)
	}
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok || st.StoppedCommit != fixup || len(st.Context) != 1 || st.Context[0].Fixup != fixup {
		t.Errorf("state %v %v: stopped at %s with context %+v, want the fixup and its gap", ok, err, st.StoppedCommit, st.Context)
	}
}

// The look reads the run's declaration: a fixup a layer step may change the
// input of keeps its note.
func TestFoldLookAnalysesTheFixupsUnderTheDeclaration(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := revisionLineUnder(t, synccmdtest.StackYAML+"defer:\n  - run: \"true\"\n    paths: [t.txt]\n    commit: \"chore: gen\"\n    each: layer\n",
		[3]string{"feat: b", "b.txt", "b\n"},
		[3]string{"fixup! feat: a", "t.txt", "one\n"},
		[3]string{"fixup! feat: b", "t.txt", "one\ntwo\n"})
	look, _ := lookWithNotices(t, ctx, "tmp/rev")
	if want := "not analysed"; !strings.Contains(look, want) {
		t.Errorf("the look lacks %q:\n%s", want, look)
	}
}

// The in-place look says a fixup it proves applying does not stop the fold in
// place, and offers no retarget for it.
func TestFoldLookSaysAGapOfAFixupThatAppliesDoesNotStopThisFold(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _ := revisionLine(t,
		[3]string{"feat: f", "f.txt", "0\n"},
		[3]string{"feat: b", "b.txt", "b\n"},
		[3]string{"feat: c", "f.txt", "1\n"},
		[3]string{"feat: back", "f.txt", "0\n"},
		[3]string{"fixup! feat: f", "f.txt", "1\n"},
		[3]string{"fixup! feat: b", "f.txt", "2\n"})
	look, _ := lookWithNotices(t, ctx, "tmp/rev")
	if want := "the fold in place applies this fixup"; !strings.Contains(look, want) {
		t.Errorf("the look lacks %q:\n%s", want, look)
	}
	if strings.Contains(look, "the fold stops there") || strings.Contains(look, "move each hunk") {
		t.Errorf("the look predicts a stop for a fixup that applies:\n%s", look)
	}
}

// What a fixup names is read before the run asks and locks. When it names
// another commit by then, the record keeps no context, and resume reads it.
func TestRunKeepsNoContextForAFixupThatNamesAnotherCommitByTheLock(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := revisionLine(t,
		[3]string{"feat: b", "t.txt", "one\n"},
		[3]string{"fixup! mark", "t.txt", "one\ntwo\n"})
	main := ctx.Repo.MainRoot
	testkit.GitIn(t, main, "tag", "mark", "tmp/rev~1")
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	opts.Agents = []agents.Agent{{Name: "s", Cwd: tmp, Status: "idle"}}
	asked := false
	opts.Confirm = func([]string) bool {
		asked = true
		testkit.GitIn(t, main, "tag", "-f", "mark", "tmp/rev~2")
		return true
	}
	if _, human, err := synccmdtest.StackRun(t, ctx, "tmp/rev", opts); !asked || err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: asked %v, err %v, want the fixup handed over\n%s", asked, err, human)
	}
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	st, ok, err := wtsync.ReadState(gitDir)
	if err != nil || !ok {
		t.Fatalf("state %v %v", ok, err)
	}
	rec, ok, err := wtsync.ReadStackRecord(main, st.Epoch)
	if err != nil || !ok || rec.Context != nil {
		t.Errorf("record %v %v with context %+v, want none", ok, err, rec.Context)
	}
}
