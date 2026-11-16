package syncfix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// foldOntoTrunk is --stack --autosquash --onto main.
func foldOntoTrunk() synccmd.RunOptions {
	opts := synccmdtest.Onto("main", "")
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	return opts
}

// foldFixups puts two fixups at the line's tip: one for a commit in layer
// s2 and one for a commit in s5.
func foldFixups(t *testing.T, s *gittest.StackLine) {
	t.Helper()
	tmp := s.Paths[gittest.StackTmp]
	for _, f := range [][2]string{{"b1.txt", "feat(s2): b1.txt"}, {"e1.txt", "feat(s5): e1.txt"}} {
		if err := os.WriteFile(filepath.Join(tmp, f[0]), []byte("fixed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! "+f[1])
	}
}

// worktreeState is what a run must leave alone: every branch's tip, the
// stash, and each worktree's HEAD, status and an untracked file.
type worktreeState struct{ refs, stash, status string }

func snapshotOf(t *testing.T, ctx *kit.Context, s *gittest.StackLine) worktreeState {
	t.Helper()
	main := ctx.Repo.MainRoot
	var st strings.Builder
	for _, b := range []string{"s1", "s3", "s5", gittest.StackTmp} {
		p := s.Paths[b]
		st.WriteString(b + ": " + testkit.GitOut(t, p, "rev-parse", "HEAD") + "\n" +
			testkit.GitOut(t, p, "status", "--porcelain=v1", "--untracked-files=all") + "\n")
	}
	return worktreeState{
		refs:   testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"),
		stash:  testkit.GitOut(t, main, "stash", "list", "--format=%H %gs"),
		status: st.String(),
	}
}

// untrackedAndStash gives s1's worktree an untracked file and the repository
// one stash entry.
func untrackedAndStash(t *testing.T, ctx *kit.Context, s *gittest.StackLine) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Paths["s1"], "scratch.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := ctx.Repo.MainRoot
	if err := os.WriteFile(filepath.Join(main, "stashed.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, main, "stash", "push", "-u", "-m", "keep-me")
}

func sameState(t *testing.T, when string, was, is worktreeState) {
	t.Helper()
	if was.refs != is.refs {
		t.Errorf("%s: branches differ\nwas:\n%s\nis:\n%s", when, was.refs, is.refs)
	}
	if was.stash != is.stash {
		t.Errorf("%s: the stash changed: %q then, %q now", when, was.stash, is.stash)
	}
	if was.status != is.status {
		t.Errorf("%s: worktrees differ\nwas:\n%s\nis:\n%s", when, was.status, is.status)
	}
}

// wt sync undo after a one-run fold onto trunk puts the line, every layer,
// the stash and the untracked file back exactly, and the held worktrees are
// as they were.
func TestSyncUndoPutsBackAFoldOntoTrunkFromTheEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	foldFixups(t, s)
	untrackedAndStash(t, ctx, s)
	was := snapshotOf(t, ctx, s)
	if _, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOntoTrunk()); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if snapshotOf(t, ctx, s).refs == was.refs {
		t.Fatal("the run moved nothing")
	}
	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	sameState(t, "after the undo", was, snapshotOf(t, ctx, s))
	if err := syscall.Kill(s.Process, 0); err != nil {
		t.Errorf("the process in s5 is gone: %v", err)
	}
	if refs := testkit.GitOut(t, ctx.Repo.MainRoot, "for-each-ref", wtsync.StackMapPrefix); refs != "" {
		t.Errorf("markers left: %s", refs)
	}
}

// A conflict in the middle of a fold onto trunk is handed over with the
// layers unmoved; undo from that stop puts everything back; and the same
// stop, resolved, resumes and moves the layers with the fold's content.
func TestFoldOntoTrunkHandsOverAtAConflictAndUndoOrResumeFinishesIt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	run := func(t *testing.T) (*kit.Context, *gittest.StackLine, worktreeState) {
		// Nothing declared for v.txt: s3's bump is a conflict that is yours.
		ctx, s := synccmdtest.StackLine(t, "conflicts: []\n")
		foldFixups(t, s)
		untrackedAndStash(t, ctx, s)
		was := snapshotOf(t, ctx, s)
		res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOntoTrunk())
		if err == nil || !strings.Contains(err.Error(), "needs you") {
			t.Fatalf("err %v\n%s", err, human)
		}
		if p := synccmdtest.Participant(t, res, gittest.StackTmp); p.Result != syncrun.ResultHandedOver {
			t.Fatalf("line %+v\n%s", p, human)
		}
		for _, b := range s.Layers {
			if got := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", b); got != s.Tips[b] {
				t.Errorf("%s moved to %s while the rebase waits", b, got)
			}
		}
		return ctx, s, was
	}
	t.Run("undo from the stop", func(t *testing.T) {
		t.Parallel()
		ctx, s, was := run(t)
		var out bytes.Buffer
		if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		sameState(t, "after the undo from the stop", was, snapshotOf(t, ctx, s))
	})
	t.Run("resume then undo", func(t *testing.T) {
		t.Parallel()
		ctx, s, was := run(t)
		main, tmp := ctx.Repo.MainRoot, s.Paths[gittest.StackTmp]
		if err := os.WriteFile(filepath.Join(tmp, "v.txt"), []byte("1.0.6\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testkit.GitIn(t, tmp, "add", "v.txt")
		opts := noResumeAgents()
		opts.Push = syncrun.PushNever
		resumed, err := syncResumeJSON(t, ctx, gittest.StackTmp, opts)
		if err == nil || !strings.Contains(err.Error(), "2 layers held") {
			t.Fatalf("err %v, want the resume to say two layers were held", err)
		}
		layers := stackLayers(synccmdtest.Participant(t, resumed, gittest.StackTmp).Stack)
		for _, b := range []string{"s1", "s2", "s4"} {
			l := layers[b]
			if deref(l.Result) != wtsync.MoveMoved || testkit.GitOut(t, main, "rev-parse", b) != deref(l.NewTip) {
				t.Errorf("%s %+v", b, l)
			}
		}
		for b, f := range map[string]string{"s2": "b1.txt", "s5": "e1.txt"} {
			if got := testkit.GitOut(t, main, "show", deref(layers[b].NewTip)+":"+f); got != "fixed" {
				t.Errorf("%s's new tip has %s %q, want the folded fix", b, f, got)
			}
		}
		if got := testkit.GitOut(t, main, "show", deref(layers["s4"].NewTip)+":v.txt"); got != "1.0.6" {
			t.Errorf("s4's new tip has v.txt %s, want the resolution", got)
		}
		if out := testkit.GitOut(t, main, "log", "--format=%s", "main.."+gittest.StackTmp, "--grep=^fixup!"); out != "" {
			t.Errorf("a fixup was left on the line: %s", out)
		}
		var out bytes.Buffer
		if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		sameState(t, "after the undo from the end", was, snapshotOf(t, ctx, s))
	})
}
