package syncfix

import (
	"bytes"
	"os"
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

type resumeStop struct {
	ctx          *kit.Context
	s            *gittest.StackLine
	main, tmp    string
	logPath      string
	branches     []string
	tipsBefore   map[string]string
	stashBefore  string
	treeBefore   string
	statusBefore string
}

// The case of anlindstrom/wt#135 in a stack held on one line: the fold
// stops at a fixup that conflicts, the person resolves it, and the resume
// meets a second fixup's conflict, with a2 and b1 between them under an
// each_commit step. Besides the fixture's own dirty and busy worktrees, the
// line's worktree has an untracked file and s1's worktree a stash entry.
func newResumeStop(t *testing.T) *resumeStop {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "checks.log")
	yaml := synccmdtest.StackYAML + "each_commit:\n  - run: git log -1 --format=%s >> '" + logPath + "'\n    paths: [a2.txt, b1.txt]\n"
	ctx, s := synccmdtest.StackLine(t, yaml)
	main := ctx.Repo.MainRoot
	tmp := s.Paths[gittest.StackTmp]
	commit := func(msg, file, content string) {
		testkit.WriteFile(t, tmp, file, content)
		testkit.GitIn(t, tmp, "add", "-A")
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", msg)
	}
	commit("feat(s6): mid a1", "a1.txt", "s1 a1.txt\nmid1\n")
	commit("feat(s6): mid b1", "b1.txt", "s2 b1.txt\nmid2\n")
	commit("fixup! feat(s1): a1.txt", "a1.txt", "s1 a1.txt FIXED\nmid1\n")
	commit("fixup! feat(s2): b1.txt", "b1.txt", "s2 b1.txt FIXED\nmid2\n")
	testkit.WriteFile(t, tmp, "untracked.txt", "mine\n")
	testkit.WriteFile(t, s.Paths["s1"], "a1.txt", "stashed work\n")
	testkit.GitIn(t, s.Paths["s1"], "stash", "push", "-q", "-m", "keep")

	// What must be exactly as it was after everything.
	branches := append([]string{gittest.StackTmp}, s.Layers...)
	tipsBefore := map[string]string{}
	for _, b := range branches {
		tipsBefore[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	stashBefore := testkit.GitOut(t, main, "stash", "list", "--format=%H %gs")
	if stashBefore == "" {
		t.Fatal("the fixture's stash is empty")
	}
	treeBefore := testkit.GitOut(t, tmp, "rev-parse", "HEAD^{tree}")
	statusBefore := testkit.GitOut(t, tmp, "status", "--porcelain", "--untracked-files=all")

	return &resumeStop{ctx: ctx, s: s, main: main, tmp: tmp, logPath: logPath, branches: branches,
		tipsBefore: tipsBefore, stashBefore: stashBefore, treeBefore: treeBefore, statusBefore: statusBefore}
}

func TestSyncResumeThatMeetsAFixupConflictHandsItOverAndUndoRestoresEverything(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	r := newResumeStop(t)
	ctx, s, main, tmp, branches := r.ctx, r.s, r.main, r.tmp, r.branches
	tipsBefore, stashBefore, treeBefore, statusBefore := r.tipsBefore, r.stashBefore, r.treeBefore, r.statusBefore
	_ = s
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	var human bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{gittest.StackTmp}, opts, &human); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("first run: %v\n%s", err, human.String())
	}
	gitDir, err := wtsync.GitDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	stopOf := func() wtsync.State {
		t.Helper()
		st, ok, err := wtsync.ReadState(gitDir)
		if err != nil || !ok {
			t.Fatalf("state: ok %v err %v", ok, err)
		}
		return st
	}
	first := stopOf()
	if first.Stop != 2 {
		t.Fatalf("first stop %d, want 2: a1's fixup", first.Stop)
	}
	resolve := func(file, content string) {
		testkit.WriteFile(t, tmp, file, content)
		testkit.GitIn(t, tmp, "add", file)
	}
	resolve("a1.txt", "s1 a1.txt FIXED\nmid1\n")
	human.Reset()
	if err := SyncResume(ctx, gittest.StackTmp, noResumeAgents(), &human); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume: %v\n%s", err, human.String())
	}
	if strings.Contains(human.String(), "no record") {
		t.Fatalf("resume lost the record of the pick:\n%s", human.String())
	}

	// The fixup's conflict is a stop like any other: state and plan are
	// the new stop's, with the file handed over.
	second := stopOf()
	if second.Stop <= first.Stop || second.Head == first.Head {
		t.Fatalf("state is stop %d at %s, the first stop was %d at %s", second.Stop, second.Head, first.Stop, first.Head)
	}
	if head := testkit.GitOut(t, tmp, "rev-parse", "HEAD"); second.Head != head {
		t.Errorf("state head %s, HEAD %s", second.Head, head)
	}
	if !contains(second.Left, "b1.txt") {
		t.Errorf("state hands over %v, want b1.txt", second.Left)
	}
	plan := readFile(t, wtsync.PlanPath(gitDir))
	if !strings.Contains(plan, "b1.txt") || !strings.Contains(plan, "fixup! feat(s2): b1.txt") {
		t.Errorf("plan is not the fixup stop's:\n%s", plan)
	}

	// Undo from the second stop puts back every branch, the stash and the
	// untracked file.
	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	for _, b := range branches {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tipsBefore[b] {
			t.Errorf("%s is %s after the undo, want %s", b, got, tipsBefore[b])
		}
	}
	if got := testkit.GitOut(t, main, "stash", "list", "--format=%H %gs"); got != stashBefore {
		t.Errorf("stash %q, want %q", got, stashBefore)
	}
	if got := testkit.GitOut(t, tmp, "rev-parse", "HEAD^{tree}"); got != treeBefore {
		t.Errorf("tree %s after the undo, want %s", got, treeBefore)
	}
	if got := testkit.GitOut(t, tmp, "status", "--porcelain", "--untracked-files=all"); got != statusBefore {
		t.Errorf("status %q after the undo, want %q", got, statusBefore)
	}
	if b, err := os.ReadFile(filepath.Join(tmp, "untracked.txt")); err != nil || string(b) != "mine\n" {
		t.Errorf("untracked.txt: %q %v", b, err)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// A resume from the second stop goes on with no "not re-checked" note, every
// commit a resume passed is checked, and the line ends with the content the
// person resolved to.
func TestSyncResumeFromAFixupConflictStopFinishesAndChecksWhatItPassed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	r := newResumeStop(t)
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	var human bytes.Buffer
	if err := synccmd.SyncRebase(r.ctx, []string{gittest.StackTmp}, opts, &human); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("first run: %v\n%s", err, human.String())
	}
	final := map[string]string{"a1.txt": "s1 a1.txt FIXED\nmid1\n", "b1.txt": "s2 b1.txt FIXED\nmid2\n"}
	resume := &bytes.Buffer{}
	for n := 0; ; n++ {
		if n > 8 {
			t.Fatalf("still stopping after %d resumes:\n%s", n, resume.String())
		}
		gitDir, err := wtsync.GitDir(r.tmp)
		if err != nil {
			t.Fatal(err)
		}
		st, ok, err := wtsync.ReadState(gitDir)
		if err != nil || !ok {
			t.Fatalf("state: ok %v err %v\n%s", ok, err, resume.String())
		}
		for _, f := range st.Left {
			testkit.WriteFile(t, r.tmp, f, final[f])
			testkit.GitIn(t, r.tmp, "add", f)
		}
		ropts := noResumeAgents()
		ropts.Push = syncrun.PushNever
		resume.Reset()
		err = SyncResume(r.ctx, gittest.StackTmp, ropts, resume)
		if strings.Contains(resume.String(), "not re-checked") {
			t.Fatalf("a resume skipped the checks of what it passed:\n%s", resume.String())
		}
		// The fixture's dirty and busy layers stay held: the line is done.
		if err == nil || strings.Contains(err.Error(), "layers held") {
			break
		}
		if !strings.Contains(err.Error(), "needs you") {
			t.Fatalf("resume: %v\n%s", err, resume.String())
		}
	}
	if got, want := strings.Join(strings.Fields(readFile(t, r.logPath)), " "), "feat(s1): a2.txt feat(s2): b1.txt"; got != want {
		t.Errorf("checked at %q, want %q", got, want)
	}
	for f, want := range final {
		if got := testkit.GitOut(t, r.tmp, "show", "HEAD:"+f); got+"\n" != want {
			t.Errorf("%s is %q, want %q", f, got, want)
		}
	}
	if busy, _ := wtsync.RebaseInProgress(r.tmp); busy {
		t.Error("the rebase is still in progress")
	}
}
