package syncfix

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

const (
	f2more, f3bump, f4docs, f5bump = "feat(f2):_more", "chore(f3):_bump_to_1.1.1", "feat(f4):_docs", "chore(f5):_bump_to_1.1.2"
)

// skipLine is the FoldLine with a recording check, which fails at f3's tip
// until failFile.pass exists, and a recording per-layer step. The line's
// fixup! targets f2's first commit, so f1 is left as it was.
func skipLine(t *testing.T) (ctx *kit.Context, s *gittest.FoldLine, checks, steps, pass string) {
	t.Helper()
	dir := t.TempDir()
	checks, steps, pass = filepath.Join(dir, "checks.log"), filepath.Join(dir, "steps.log"), filepath.Join(dir, "pass")
	yaml := "each_commit:\n  - run: |\n      git log -1 --format=%s >> '" + checks + "'\n" +
		"      if [ \"$(git log -1 --format=%s)\" = 'chore(f3): bump to 1.1.1' ] && [ ! -f '" + pass + "' ]; then exit 1; fi\n" +
		"defer:\n  - run: git log -1 --format=%s >> '" + steps + "'\n    each: layer\n"
	ctx, s = synccmdtest.FoldLine(t, yaml)
	return ctx, s, checks, steps, pass
}

func ranAt(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "_"))
}

// trees is each branch's history by tree, author and subject, one line per commit.
func trees(t *testing.T, main string, branches []string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, b := range branches {
		// Every commit of the branch by tree, author and subject:		// carries but its committer date and its parent's id, which a run's own
		// clock and the layers below it set.
		got[b] = testkit.GitOut(t, main, "log", "--format=%T %an %s", b)
	}
	return got
}

// A stop above a skipped layer, then wt sync resume, finishes with the layers
// a straight run leaves, and neither run ran f1's check or step. The stop and
// the resume leave the other worktrees and the stash as they were.
func TestSyncResumeAboveAnUnchangedLayerSkipsItAndFinishesAsAStraightRun(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	run := func() synccmd.RunOptions {
		opts := synccmdtest.NoAgents()
		opts.EachCommit = "tips"
		opts.Stack = &synccmd.StackOptions{Autosquash: true}
		return opts
	}
	// The straight run: the check passes at once.
	sctx, ss, schecks, ssteps, spass := skipLine(t)
	testkit.WriteFile(t, filepath.Dir(spass), "pass", "")
	if _, human, _ := synccmdtest.StackRun(t, sctx, gittest.FoldTmp, run()); !slices.Equal(ranAt(t, schecks), []string{f2more, f3bump, f4docs, f5bump}) {
		t.Fatalf("straight run checked %q\n%s", ranAt(t, schecks), human)
	}
	branches := append(append([]string{}, ss.Layers...), gittest.FoldTmp)
	want := trees(t, sctx.Repo.MainRoot, branches)
	_ = ssteps

	// The run that stops at f3's tip, above the skipped f1.
	ctx, s, checks, steps, pass := skipLine(t)
	main := ctx.Repo.MainRoot
	others := []string{s.Paths["f1"], s.Paths["f3"], main}
	state := func() string {
		var b strings.Builder
		for _, dir := range others {
			b.WriteString(dir + "\n" + testkit.GitOut(t, dir, "status", "--porcelain=v1", "-uall") + "\n" + testkit.GitOut(t, dir, "diff") + "\n")
		}
		return b.String() + testkit.GitOut(t, main, "stash", "list")
	}
	otherBefore := state()
	before := refsOf(t, main, branches)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, run())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want f3's check handed over\n%s", err, human)
	}
	if got := refsOf(t, main, branches); got["f1"] != before["f1"] {
		t.Errorf("f1 moved at the stop: %v", got)
	}
	safety := func() string {
		return testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/wt-sync/"+gittest.FoldTmp+"/")
	}
	wantSafety := "refs/wt-sync/" + gittest.FoldTmp + "/99 " + before[gittest.FoldTmp]
	if got := safety(); got != wantSafety {
		t.Errorf("safety refs at the stop %q, want %q", got, wantSafety)
	}
	if got := state(); got != otherBefore {
		t.Errorf("the stop changed the other worktrees or the stash:\n%s\nwas:\n%s", got, otherBefore)
	}
	testkit.WriteFile(t, filepath.Dir(pass), "pass", "")
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	if _, err := syncResumeJSON(t, ctx, gittest.FoldTmp, opts); err != nil && strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume stopped: %v", err)
	}
	if got, w := ranAt(t, checks), []string{f2more, f3bump, f3bump, f4docs, f5bump}; !slices.Equal(got, w) {
		t.Errorf("checked %q, want %q", got, w)
	}
	if got, w := ranAt(t, steps), []string{f2more, f3bump, f4docs, f5bump, f5bump}; !slices.Equal(got, w) {
		t.Errorf("step ran at %q, want %q", got, w)
	}
	if got := trees(t, main, branches); !equalMaps(got, want) {
		t.Errorf("layers after the resume %v, a straight run leaves %v", got, want)
	}
	if got := refsOf(t, main, branches); got["f1"] != before["f1"] {
		t.Errorf("f1 moved: %v", got)
	}
	if got := safety(); got != wantSafety {
		t.Errorf("safety refs after the resume %q, want %q", got, wantSafety)
	}
	if got := state(); got != otherBefore {
		t.Errorf("the resume changed the other worktrees or the stash:\n%s\nwas:\n%s", got, otherBefore)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func refsOf(t *testing.T, main string, branches []string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, b := range branches {
		got[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	return got
}

// amendStop is a fold stopped at f1's check, which fails there until the
// pass file exists: f1 has a step owed, so it is checked though the run leaves
// it as it was. amend changes the stopped commit by hand. branches are the
// layers and the line, and before their refs at the start.
type amendStop struct {
	ctx           *kit.Context
	s             *gittest.FoldLine
	checks, pass  string
	branches      []string
	before        map[string]string
	tmp           string
	amendedCommit string
}

func newAmendStop(t *testing.T, amend func(tmp string)) *amendStop {
	t.Helper()
	ctx, s, checks, steps, pass := skipLine(t)
	main := ctx.Repo.MainRoot
	body, err := os.ReadFile(filepath.Join(main, ".wt-sync.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	testkit.WriteFile(t, main, ".wt-sync.yaml", strings.ReplaceAll(string(body), "chore(f3): bump to 1.1.1", "chore(f1): bump to 1.1.0"))
	testkit.GitIn(t, main, "add", "-A")
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "chore: fail at f1")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	step := "git log -1 --format=%s >> '" + steps + "'"
	if err := wtsync.WriteOwed(main, &wtsync.Owed{Branch: gittest.FoldTmp, Epoch: 1, EachCommit: "tips",
		Steps: []wtsync.OwedStep{{Step: step, Layer: "f1", Why: "left owed"}}}); err != nil {
		t.Fatal(err)
	}
	a := &amendStop{ctx: ctx, s: s, checks: checks, pass: pass, tmp: s.Paths[gittest.FoldTmp],
		branches: append(append([]string{}, s.Layers...), gittest.FoldTmp)}
	a.before = refsOf(t, main, a.branches)
	opts := synccmdtest.NoAgents()
	opts.EachCommit = "tips"
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want f1's check handed over\n%s", err, human)
	}
	amend(a.tmp)
	a.amendedCommit = testkit.GitOut(t, a.tmp, "rev-parse", "HEAD")
	testkit.WriteFile(t, filepath.Dir(pass), "pass", "")
	return a
}

func (a *amendStop) resume(t *testing.T) (string, error) {
	t.Helper()
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	var human bytes.Buffer
	err := SyncResume(a.ctx, gittest.FoldTmp, opts, &human)
	return human.String(), err
}

// A hand-amend of f1's commit that adds a file outside the steps' paths is
// refused when the fold is finished, as the tree guard refuses any fold that
// changes content: the person is told, no layer moves, and wt sync undo puts
// every layer and the line back exactly.
func TestSyncResumeAfterAHandAmendThatChangesContentIsRefusedAndUndone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	a := newAmendStop(t, func(tmp string) {
		testkit.WriteFile(t, tmp, "amended.txt", "by a person\n")
		testkit.GitIn(t, tmp, "add", "amended.txt")
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "--no-edit")
	})
	main := a.ctx.Repo.MainRoot
	human, err := a.resume(t)
	if err == nil || !strings.Contains(err.Error(), "no layer moved") {
		t.Fatalf("resume: err %v, want the refusal\n%s", err, human)
	}
	for _, want := range []string{"no layer moved: the fold changed the line's content", "amended.txt among them", "wt sync undo tmp/fold puts it back"} {
		if !strings.Contains(human, want) {
			t.Errorf("missing %q in\n%s", want, human)
		}
	}
	for _, b := range a.s.Layers {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != a.before[b] {
			t.Errorf("%s moved to %s in a refused fold, was %s", b, got, a.before[b])
		}
	}
	var out bytes.Buffer
	if err := SyncUndo(a.ctx, gittest.FoldTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	if got := refsOf(t, main, a.branches); !equalMaps(got, a.before) {
		t.Errorf("after undo %v, want %v\n%s", got, a.before, out.String())
	}
	if got := testkit.GitOut(t, a.tmp, "status", "--porcelain=v1", "-uall"); got != "" {
		t.Errorf("the line's worktree is not clean after undo:\n%s", got)
	}
}

// A hand-amend that keeps the line's content (here the message of f1's commit)
// is accepted: nothing above it is the commit it was, so every layer's check
// runs, the resume finishes, and each layer branch the run moved is on the
// line, at the commit of its own tip.
func TestSyncResumeAfterAnAcceptedHandAmendSkipsNothingAbove(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	a := newAmendStop(t, func(tmp string) {
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "--amend", "-m", "chore(f1): bump to 1.1.0", "-m", "reworded by a person")
	})
	main := a.ctx.Repo.MainRoot
	human, err := a.resume(t)
	if err == nil || !strings.Contains(err.Error(), "1 layer held") || strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume: err %v, want only f3 held (its worktree has uncommitted changes)\n%s", err, human)
	}
	want := []string{"chore(f1):_bump_to_1.1.0", "chore(f1):_bump_to_1.1.0", "feat(f2):_more", "chore(f3):_bump_to_1.1.1", "feat(f4):_docs", "chore(f5):_bump_to_1.1.2"}
	if got := ranAt(t, a.checks); !slices.Equal(got, want) {
		t.Errorf("checked %q, want %q\n%s", got, want, human)
	}
	tip := testkit.GitOut(t, main, "rev-parse", gittest.FoldTmp)
	if got := testkit.GitOut(t, main, "rev-parse", "f1"); got != a.amendedCommit {
		t.Errorf("f1 is %s, want the amended commit %s", got, a.amendedCommit)
	}
	if got := testkit.GitOut(t, main, "log", "-1", "--format=%b", "f1"); got != "reworded by a person" {
		t.Errorf("f1's commit body is %q", got)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "f5"); got != tip {
		t.Errorf("f5 is %s, want the line's tip %s", got, tip)
	}
	for b, subject := range map[string]string{"f2": "feat(f2): more", "f4": "feat(f4): docs"} {
		if got := testkit.GitOut(t, main, "log", "-1", "--format=%s", b); got != subject {
			t.Errorf("%s is at %q, want %q", b, got, subject)
		}
	}
	for _, b := range []string{"f1", "f2", "f4", "f5"} {
		if err := exec.Command("git", "-C", main, "merge-base", "--is-ancestor", b, gittest.FoldTmp).Run(); err != nil {
			t.Errorf("%s is not on the line's tip: %v", b, err)
		}
	}
	if got := testkit.GitOut(t, main, "rev-parse", "f3"); got != a.before["f3"] {
		t.Errorf("held f3 moved to %s, was %s", got, a.before["f3"])
	}
	var out bytes.Buffer
	if err := SyncUndo(a.ctx, gittest.FoldTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	if got := refsOf(t, main, a.branches); !equalMaps(got, a.before) {
		t.Errorf("after undo %v, want %v\n%s", got, a.before, out.String())
	}
}
