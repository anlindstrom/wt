package syncfix

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// editCase is anlindstrom/wt#130: a stack held on one line, a defect in a
// low commit A, a commit B two layers up that already fixes it, and an
// each_commit step that logs every commit it checks. The line's worktree
// has an untracked file and the repository a stash entry, which nothing may
// touch.
type editCase struct {
	ctx      *kit.Context
	s        *gittest.EditLine
	main     string
	tmp      string
	gitDir   string
	logPath  string
	branches []string
	// What the run must leave exactly as it was, or put back.
	tips    map[string]string
	stash   string
	tree    string
	status  string
	commits []string // the line's commits from its base, oldest first
}

// logStep is the each_commit step the cases declare by default: it logs
// the subject of every commit it checks to %LOG%.
const logStep = "each_commit:\n  - run: git log -1 --format=%s >> '%LOG%'\n"

func newEditCase(t *testing.T, emptied bool) *editCase {
	t.Helper()
	return newEditCaseWith(t, emptied, logStep)
}

// newEditCaseWith declares steps on trunk after conflicts: [], with %LOG%
// the path of the check log; "" declares no step at all.
func newEditCaseWith(t *testing.T, emptied bool, steps string) *editCase {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "checks.log")
	yaml := "conflicts: []\n" + strings.ReplaceAll(steps, "%LOG%", logPath)
	main := testkit.CommittedRepo(t, testkit.MinimalConf)
	testkit.GitIn(t, main, "config", "core.hooksPath", filepath.Join(main, ".git", "hooks"))
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := gittest.NewEditLine(t, main, wtDir, yaml, emptied)
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	testkit.WriteFile(t, s.Path, "untracked.txt", "mine\n")
	testkit.WriteFile(t, main, "readme.txt", "stashed work\n")
	testkit.GitIn(t, main, "stash", "push", "-q", "-m", "keep")
	gitDir, err := wtsync.GitDir(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	c := &editCase{ctx: ctx, s: s, main: main, tmp: s.Path, gitDir: gitDir, logPath: logPath,
		branches: append([]string{gittest.EditTmp}, s.Layers...), tips: map[string]string{}}
	for _, b := range c.branches {
		c.tips[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	c.stash = testkit.GitOut(t, main, "stash", "list", "--format=%H %gs")
	c.tree = testkit.GitOut(t, s.Path, "rev-parse", "HEAD^{tree}")
	c.status = testkit.GitOut(t, s.Path, "status", "--porcelain", "--untracked-files=all")
	c.commits = strings.Fields(testkit.GitOut(t, main, "rev-list", "--reverse", s.Base+".."+gittest.EditTmp))
	return c
}

// run starts wt sync rebase tmp/edit --stack --edit <rev>.
func (c *editCase) run(t *testing.T, rev string) (syncrun.RunResult, string, error) {
	t.Helper()
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{Edit: rev}
	return synccmdtest.StackRun(t, c.ctx, gittest.EditTmp, opts)
}

func (c *editCase) resume(t *testing.T) (syncrun.RunResult, string, error) {
	t.Helper()
	var out, human bytes.Buffer
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
	err := SyncResume(c.ctx, gittest.EditTmp, opts, &human)
	return synccmdtest.DecodeSyncRun(t, out.Bytes()), human.String(), err
}

func (c *editCase) undo(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	if err := SyncUndo(c.ctx, gittest.EditTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	return out.String()
}

func (c *editCase) state(t *testing.T) wtsync.State {
	t.Helper()
	st, ok, err := wtsync.ReadState(c.gitDir)
	if err != nil || !ok {
		t.Fatalf("state: ok %v err %v", ok, err)
	}
	return st
}

// amendA amends A, at the edit stop, with lib.txt fixed and extra files.
func (c *editCase) amendA(t *testing.T, extra map[string]string) string {
	t.Helper()
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = old\n")
	for f, content := range extra {
		testkit.WriteFile(t, c.tmp, f, content)
	}
	testkit.GitIn(t, c.tmp, "add", "-A", "--", ".", ":!untracked.txt")
	testkit.GitIn(t, c.tmp, "commit", "-q", "--amend", "--no-edit", "--no-verify")
	return testkit.GitOut(t, c.tmp, "rev-parse", "HEAD")
}

// back checks that every branch, the stash, the line's tree, its status and
// the untracked file are exactly as the fixture left them, and that no
// rebase, handover or marker is left.
func (c *editCase) back(t *testing.T) {
	t.Helper()
	for _, b := range c.branches {
		if got := testkit.GitOut(t, c.main, "rev-parse", b); got != c.tips[b] {
			t.Errorf("%s is at %s, want %s", b, got, c.tips[b])
		}
	}
	if got := testkit.GitOut(t, c.main, "stash", "list", "--format=%H %gs"); got != c.stash {
		t.Errorf("stash %q, want %q", got, c.stash)
	}
	if got := testkit.GitOut(t, c.tmp, "rev-parse", "HEAD^{tree}"); got != c.tree {
		t.Errorf("tree %s, want %s", got, c.tree)
	}
	if got := testkit.GitOut(t, c.tmp, "status", "--porcelain", "--untracked-files=all"); got != c.status {
		t.Errorf("status %q, want %q", got, c.status)
	}
	if b, err := os.ReadFile(filepath.Join(c.tmp, "untracked.txt")); err != nil || string(b) != "mine\n" {
		t.Errorf("untracked.txt: %q %v", b, err)
	}
	if busy, _ := wtsync.RebaseInProgress(c.tmp); busy {
		t.Error("a rebase is still in progress")
	}
	if _, ok, _ := wtsync.ReadState(c.gitDir); ok {
		t.Error("the handover is still there")
	}
	if refs := testkit.GitOut(t, c.main, "for-each-ref", wtsync.StackMapPrefix); refs != "" {
		t.Errorf("markers left: %s", refs)
	}
}

// keptRef is the ref a line of out says a commit is kept under, and the
// commit it names there.
func keptRef(t *testing.T, out string) (ref, short string) {
	t.Helper()
	m := regexp.MustCompile(`([0-9a-f]{7}) kept under (refs/\S+?),`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("nothing is said to be kept:\n%s", out)
	}
	return m[2], m[1]
}

// lineOf is the subjects and patch ids of base..tip, oldest first.
func lineOf(t *testing.T, dir, base, tip string) (subjects, patches []string) {
	t.Helper()
	for _, sha := range strings.Fields(testkit.GitOut(t, dir, "rev-list", "--reverse", base+".."+tip)) {
		subjects = append(subjects, testkit.GitOut(t, dir, "log", "-1", "--format=%s", sha))
		patches = append(patches, patchID(t, dir, sha))
	}
	return subjects, patches
}

func patchID(t *testing.T, dir, sha string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", "git diff-tree -p --no-color "+sha+" | git patch-id --stable")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(string(out), " ")
	return id
}

// The case end to end: the run stops at A for a person, who amends the fix
// into it; the resume meets B's conflict with the moved fix, which the
// person resolves to B's own part; the second resume finishes and moves
// every layer. Only A and B differ from before, subject for subject and
// patch for patch, the line's content is what it was, every commit from A
// on is checked once, and the stash and the untracked file are untouched.
func TestSyncRebaseStackEditMovesAFixDownToAnEarlierCommit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	for name, steps := range map[string]string{"checked": logStep, "no steps": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			editMovesAFixDown(t, newEditCaseWith(t, false, steps), steps != "")
		})
	}
}

func editMovesAFixDown(t *testing.T, c *editCase, checked bool) {
	subjectsBefore, patchesBefore := lineOf(t, c.main, c.s.Base, gittest.EditTmp)
	layerSubjects := map[string]string{}
	for _, l := range c.s.Layers {
		layerSubjects[l] = testkit.GitOut(t, c.main, "log", "-1", "--format=%s", l)
	}

	res, human, err := c.run(t, git7(c.s.A))
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: %v\n%s", err, human)
	}
	stop := synccmdtest.Participant(t, res, gittest.EditTmp)
	if stop.Result != syncrun.ResultHandedOver {
		t.Fatalf("line %+v\n%s", stop, human)
	}
	if stop.Stack == nil || stop.Stack.Edit == nil || stop.Stack.Edit.Commit != c.s.A || stop.Stack.Edit.Amended != "" ||
		stop.Stack.Edit.Subject != "feat(e2): add the lib" {
		t.Errorf("at the stop, stack.edit is %+v, want A with nothing amended yet", stop.Stack)
	}
	if head := testkit.GitOut(t, c.tmp, "rev-parse", "HEAD"); head != c.s.A {
		t.Fatalf("the run stopped at %s, want A %s\n%s", head, c.s.A, human)
	}
	plan := readFile(t, wtsync.PlanPath(c.gitDir))
	for _, want := range []string{git7(c.s.A), "git commit --amend", "wt sync resume " + gittest.EditTmp} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan does not say %q:\n%s", want, plan)
		}
	}
	if b, _ := os.ReadFile(c.logPath); len(b) != 0 {
		t.Errorf("a commit below the edit was checked:\n%s", b)
	}
	for _, b := range c.branches {
		if got := testkit.GitOut(t, c.main, "rev-parse", b); got != c.tips[b] {
			t.Errorf("%s moved to %s at the edit stop", b, got)
		}
	}

	amended := c.amendA(t, nil)
	_, human, err = c.resume(t)
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume at the edit: %v\n%s", err, human)
	}
	if st := c.state(t); !slices.Contains(st.Left, "lib.txt") {
		t.Fatalf("handed over %v, want B's lib.txt\n%s", st.Left, human)
	}
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt")
	done, human, err := c.resume(t)
	if err != nil {
		t.Fatalf("resume at B: %v\n%s", err, human)
	}

	if got := testkit.GitOut(t, c.tmp, "rev-parse", "HEAD^{tree}"); got != c.tree {
		t.Errorf("the line's tree is %s, want the old %s: B already carried the fix", got, c.tree)
	}
	subjects, patches := lineOf(t, c.main, c.s.Base, gittest.EditTmp)
	if !slices.Equal(subjects, subjectsBefore) {
		t.Fatalf("subjects %q, want %q", subjects, subjectsBefore)
	}
	posA, posB := slices.Index(c.commits, c.s.A), slices.Index(c.commits, c.s.B)
	for i := range patches {
		changed := patches[i] != patchesBefore[i]
		if want := i == posA || i == posB; changed != want {
			t.Errorf("%q changed %v, want %v", subjects[i], changed, want)
		}
	}
	newA := testkit.GitOut(t, c.main, "rev-list", "--reverse", c.s.Base+".."+gittest.EditTmp)
	if strings.Fields(newA)[posA] != amended {
		t.Errorf("A on the line is %s, want the amended %s", strings.Fields(newA)[posA], amended)
	}
	for _, l := range c.s.Layers {
		tip := testkit.GitOut(t, c.main, "rev-parse", l)
		if l != "e1" && tip == c.tips[l] {
			t.Errorf("%s was not moved", l)
		}
		if got := testkit.GitOut(t, c.main, "log", "-1", "--format=%s", l); got != layerSubjects[l] {
			t.Errorf("%s's tip is %q, want %q", l, got, layerSubjects[l])
		}
		if testkit.GitOut(t, c.main, "merge-base", "--is-ancestor", tip, gittest.EditTmp) != "" {
			t.Errorf("%s is not on the line", l)
		}
		if l != "e1" {
			if got := testkit.GitOut(t, c.main, "show", tip+":lib.txt"); !strings.HasPrefix(got, "value = 2") {
				t.Errorf("%s carries lib.txt %q, want the fix", l, got)
			}
		}
	}
	if testkit.GitOut(t, c.main, "rev-parse", "e1") != c.tips["e1"] {
		t.Error("e1, below the edit, moved")
	}
	if checked {
		got := strings.Split(strings.TrimSpace(readFile(t, c.logPath)), "\n")
		if want := subjectsBefore[posA:]; !slices.Equal(got, want) {
			t.Errorf("checked %q, want every commit from A on once: %q", got, want)
		}
	}
	p := synccmdtest.Participant(t, done, gittest.EditTmp)
	if p.Stack == nil || p.Stack.Edit == nil {
		t.Fatalf("stack %+v", p.Stack)
	}
	e := p.Stack.Edit
	if e.Commit != c.s.A || e.Amended != amended || !slices.Equal(e.Paths, []string{"lib.txt"}) || len(e.Emptied) != 0 {
		t.Errorf("edit %+v, want A amended to %s changing lib.txt", e, amended)
	}
	if !strings.Contains(human, "lib.txt") || !strings.Contains(human, git7(c.s.A)) {
		t.Errorf("the end does not name the edited commit and its paths:\n%s", human)
	}
	if got := testkit.GitOut(t, c.main, "stash", "list", "--format=%H %gs"); got != c.stash {
		t.Errorf("stash %q, want %q", got, c.stash)
	}
	if b, err := os.ReadFile(filepath.Join(c.tmp, "untracked.txt")); err != nil || string(b) != "mine\n" {
		t.Errorf("untracked.txt: %q %v", b, err)
	}
}

func git7(sha string) string { return sha[:7] }

// Undo from the edit stop, after the amend, puts back the line, every layer,
// the stash and the untracked file, and keeps the amended commit under a
// safety ref it names.
func TestSyncUndoFromTheEditStopKeepsTheAmend(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	amended := c.amendA(t, nil)
	out := c.undo(t)
	c.back(t)
	ref, short := keptRef(t, out)
	if got := testkit.GitOut(t, c.main, "rev-parse", ref); got != amended || !strings.HasPrefix(amended, short) {
		t.Errorf("%s holds %s (said %s), want the amended %s", ref, got, short, amended)
	}
}

// Undo from B's conflict stop, past the edit, puts everything back too, and
// the amended commit stays reachable from the ref it names.
func TestSyncUndoFromAStopAfterTheEditKeepsTheAmend(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	amended := c.amendA(t, nil)
	if _, human, err := c.resume(t); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("resume: %v\n%s", err, human)
	}
	out := c.undo(t)
	c.back(t)
	ref, _ := keptRef(t, out)
	if testkit.GitOut(t, c.main, "merge-base", "--is-ancestor", amended, ref) != "" {
		t.Errorf("the amended %s is not reachable from %s", amended, ref)
	}
}

// An amend that changes a file B did not carry is the allowed difference:
// the line ends with the old content plus that change, and the end names
// every path the amend changed.
func TestSyncRebaseStackEditReportsWhatTheAmendAddedAsAllowed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, map[string]string{"extra.txt": "extra\n"})
	if _, human, err := c.resume(t); err == nil {
		t.Fatalf("resume did not stop at B:\n%s", human)
	}
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt")
	done, human, err := c.resume(t)
	if err != nil {
		t.Fatalf("resume at B: %v\n%s", err, human)
	}
	if got := testkit.GitOut(t, c.main, "diff", "--name-only", c.tips[gittest.EditTmp], gittest.EditTmp); got != "extra.txt" {
		t.Errorf("the line differs from before in %q, want extra.txt alone", got)
	}
	e := synccmdtest.Participant(t, done, gittest.EditTmp).Stack.Edit
	if e == nil || !slices.Equal(e.Paths, []string{"extra.txt", "lib.txt"}) {
		t.Errorf("edit %+v, want the paths extra.txt and lib.txt", e)
	}
	for _, l := range []string{"e2", "e3", "e4"} {
		if got := testkit.GitOut(t, c.main, "show", l+":extra.txt"); got != "extra" {
			t.Errorf("%s has extra.txt %q", l, got)
		}
	}
}

// A change outside the amend, here one made while resolving B, is refused
// before the line or any layer moves: the rebase is put back, everything is
// where it was, and what was rebased is kept under a ref the refusal names.
func TestSyncRebaseStackEditRefusesAStrayChangeBeforeAnythingMoves(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	for name, steps := range map[string]string{"checked": logStep, "no steps": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			editRefusesAStrayChange(t, newEditCaseWith(t, false, steps))
		})
	}
}

func editRefusesAStrayChange(t *testing.T, c *editCase) {
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	if _, human, err := c.resume(t); err == nil {
		t.Fatalf("resume did not stop at B:\n%s", human)
	}
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.WriteFile(t, c.tmp, "one.txt", "stray\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt", "one.txt")
	res, human, err := c.resume(t)
	if err == nil || !strings.Contains(human, "one.txt") {
		t.Fatalf("resume: %v, want a refusal naming one.txt\n%s", err, human)
	}
	c.back(t)
	ref, _ := keptRef(t, human)
	if got := testkit.GitOut(t, c.main, "show", ref+":one.txt"); got != "stray" {
		t.Errorf("%s holds one.txt %q, want the stray change kept", ref, got)
	}
	if p := synccmdtest.Participant(t, res, gittest.EditTmp); p.Result != syncrun.ResultRestored {
		t.Errorf("result %s, want restored", p.Result)
	}
}

// A commit the amend empties is dropped and reported; a layer whose commits
// all empty ends where the layer below does.
func TestSyncRebaseStackEditDropsACommitTheAmendEmpties(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, true)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	done, human, err := c.resume(t)
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, human)
	}
	if got := testkit.GitOut(t, c.tmp, "rev-parse", "HEAD^{tree}"); got != c.tree {
		t.Errorf("the line's tree changed")
	}
	if testkit.GitOut(t, c.main, "rev-parse", "e3") != testkit.GitOut(t, c.main, "rev-parse", "e2") {
		t.Error("e3, emptied, does not end where e2 does")
	}
	if n := testkit.GitOut(t, c.main, "rev-list", "--count", c.s.Base+".."+gittest.EditTmp); n != "6" {
		t.Errorf("the line has %s commits, want 6: e3's one dropped", n)
	}
	p := synccmdtest.Participant(t, done, gittest.EditTmp)
	layers := stackLayers(p.Stack)
	if deref(layers["e3"].Mapped) != wtsync.MappedEmptied {
		t.Errorf("e3 mapped %q, want emptied", deref(layers["e3"].Mapped))
	}
	if e := p.Stack.Edit; e == nil || len(e.Emptied) != 1 || e.Emptied[0].Subject != "fix(e3): set the value" {
		t.Errorf("edit %+v, want e3's commit emptied", p.Stack.Edit)
	}
	if !strings.Contains(human, "fix(e3): set the value") {
		t.Errorf("the emptied commit is not named:\n%s", human)
	}
}

// --edit names exactly one commit of the line, or the run refuses before
// anything moves.
func TestSyncRebaseStackEditRefusesARevisionNotOnTheLine(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	for rev, want := range map[string]string{
		c.s.Base:   "not on the line",
		"main":     "not on the line",
		"nonesuch": "not a commit",
	} {
		_, human, err := c.run(t, rev)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--edit %s: %v, want %q\n%s", rev, err, want, human)
		}
		c.back(t)
	}
}

// A rebase a person finished by hand past the edit, with a stray change,
// never met the check inside the rebase: the resume holds the content
// before any layer moves.
func TestSyncResumeOfAnEditFinishedByHandHoldsItsContentBeforeTheLayersMove(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	if _, human, err := c.resume(t); err == nil {
		t.Fatalf("resume did not stop at B:\n%s", human)
	}
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.WriteFile(t, c.tmp, "one.txt", "stray\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt", "one.txt")
	testkit.GitIn(t, c.tmp, "-c", "core.editor=true", "rebase", "--continue")
	res, human, err := c.resume(t)
	if err == nil || !strings.Contains(err.Error(), "no layer moved") || !strings.Contains(human, "one.txt") {
		t.Fatalf("resume: %v, want no layer moved over one.txt\n%s", err, human)
	}
	for _, l := range c.s.Layers {
		if got := testkit.GitOut(t, c.main, "rev-parse", l); got != c.tips[l] {
			t.Errorf("%s moved to %s", l, got)
		}
	}
	if s := synccmdtest.Participant(t, res, gittest.EditTmp).Stack; s == nil || s.Refusal == nil {
		t.Errorf("stack %+v, want a refusal", s)
	}
}

// A resume interrupted just after it went on from the edit stop, before the
// next handover recorded the amend, left its note in the sequencer: the next
// resume still knows what the commit was amended to and finishes.
func TestSyncResumeInterruptedPastTheEditStillKnowsTheAmend(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	amended := c.amendA(t, nil)
	// What the interrupted resume did: the note, then the continue, which
	// stops at B's conflict with the handover still the edit stop's.
	dir := filepath.Join(c.gitDir, "rebase-merge")
	if err := os.WriteFile(filepath.Join(dir, "wt-sync-amended"), []byte(amended+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Try(t, c.tmp, "-c", "core.editor=true", "rebase", "--continue"); err == nil {
		t.Fatal("the continue did not stop at B")
	}
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt")
	done, human, err := c.resume(t)
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, human)
	}
	if e := synccmdtest.Participant(t, done, gittest.EditTmp).Stack.Edit; e == nil || e.Amended != amended {
		t.Errorf("edit %+v, want amended %s", e, amended)
	}
	if got := testkit.GitOut(t, c.main, "rev-parse", "e4"); got == c.tips["e4"] {
		t.Error("e4 did not move")
	}
}

// A file the old line and the amend both changed does not merge as one
// three-way merge, though the replay merges it commit by commit; a hunk lost
// there while a person resolves another file is refused, not let through as
// if that file had been a stop of theirs.
func TestSyncRebaseStackEditRefusesAHunkLostInAFileTheReplayMergedCleanly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, true)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, map[string]string{"four.txt": "mine\n"})
	if _, human, err := c.resume(t); err == nil || !slices.Contains(c.state(t).Left, "four.txt") {
		t.Fatalf("resume did not stop at four.txt: %v\n%s", err, human)
	}
	testkit.WriteFile(t, c.tmp, "four.txt", "four\n")
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\n")
	testkit.GitIn(t, c.tmp, "add", "four.txt", "lib.txt")
	_, human, err := c.resume(t)
	if err == nil || !strings.Contains(human, "lib.txt") {
		t.Fatalf("resume: %v, want a refusal naming lib.txt\n%s", err, human)
	}
	c.back(t)
	keptRef(t, human)
}

// Commits added at the edit stop, with nothing emptied, go into the edited
// commit's layer: it and every layer above gain them, and the run finishes.
func TestSyncRebaseStackEditTakesCommitsAddedAtTheStop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	testkit.WriteFile(t, c.tmp, "note.txt", "note\n")
	testkit.GitIn(t, c.tmp, "add", "note.txt")
	testkit.GitIn(t, c.tmp, "commit", "-q", "--no-verify", "-m", "feat(e2): note")
	if _, human, err := c.resume(t); err == nil {
		t.Fatalf("resume did not stop at B:\n%s", human)
	}
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt")
	_, human, err := c.resume(t)
	if err != nil {
		t.Fatalf("resume at B: %v\n%s", err, human)
	}
	for l, n := range map[string]string{"e1": "1", "e2": "4", "e3": "5", "e4": "7"} {
		if got := testkit.GitOut(t, c.main, "rev-list", "--count", c.s.Base+".."+l); got != n {
			t.Errorf("%s has %s commits, want %s", l, got, n)
		}
	}
	if got := testkit.GitOut(t, c.main, "log", "-1", "--format=%s", "e2"); got != "feat(e2): two" {
		t.Errorf("e2's tip is %q", got)
	}
}

// A check that fails at the edited commit is fixed by amending it again: the
// content is then held to that amend, not to the first.
func TestSyncRebaseStackEditFollowsTheAmendThatFixesItsCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCaseWith(t, false, "each_commit:\n  - run: test ! -f lib.txt || test -f lib.ok\n")
	// lib.ok on every commit from A up, so the old line passes the check.
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	if _, human, err := c.resume(t); err == nil || !c.handedOverAtCheck(t) {
		t.Fatalf("resume did not stop at A's check: %v\n%s", err, human)
	}
	testkit.WriteFile(t, c.tmp, "lib.ok", "ok\n")
	testkit.GitIn(t, c.tmp, "add", "lib.ok")
	testkit.GitIn(t, c.tmp, "commit", "-q", "--amend", "--no-edit", "--no-verify")
	again := testkit.GitOut(t, c.tmp, "rev-parse", "HEAD")
	for n := 0; ; n++ {
		res, human, err := c.resume(t)
		if err == nil {
			if e := synccmdtest.Participant(t, res, gittest.EditTmp).Stack.Edit; e == nil || e.Amended != again {
				t.Errorf("edit %+v, want amended %s", e, again)
			}
			break
		}
		st, ok, _ := wtsync.ReadState(c.gitDir)
		if n > 3 || !ok || !slices.Contains(st.Left, "lib.txt") {
			t.Fatalf("resume: %v\n%s", err, human)
		}
		testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
		testkit.GitIn(t, c.tmp, "add", "lib.txt")
	}
	if got := testkit.GitOut(t, c.main, "show", "e4:lib.ok"); got != "ok" {
		t.Errorf("e4 has lib.ok %q", got)
	}
}

// A layer check that refuses after a rebase finished by hand puts the line
// back where the run found it, and keeps what it came to.
func TestSyncResumeOfAnEditPutsTheLineBackWhenItsLayersAreRefused(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	c := newEditCase(t, false)
	if _, human, err := c.run(t, c.s.A); err == nil {
		t.Fatalf("the run did not stop:\n%s", human)
	}
	c.amendA(t, nil)
	if _, human, err := c.resume(t); err == nil {
		t.Fatalf("resume did not stop at B:\n%s", human)
	}
	epoch := c.state(t).Epoch
	testkit.WriteFile(t, c.tmp, "lib.txt", "value = 2\nname = new\n")
	testkit.GitIn(t, c.tmp, "add", "lib.txt")
	testkit.GitIn(t, c.tmp, "-c", "core.editor=true", "rebase", "--continue")
	// A marker that is not what the plan expects: the layer check refuses.
	testkit.GitIn(t, c.main, "update-ref", wtsync.StackMapRef(epoch, 2), c.tips["e1"])
	rebased := testkit.GitOut(t, c.tmp, "rev-parse", "HEAD")
	_, human, err := c.resume(t)
	if err == nil || !strings.Contains(err.Error(), "no layer moved") {
		t.Fatalf("resume: %v, want no layer moved\n%s", err, human)
	}
	for _, b := range c.branches {
		if got := testkit.GitOut(t, c.main, "rev-parse", b); got != c.tips[b] {
			t.Errorf("%s is at %s, want %s", b, got, c.tips[b])
		}
	}
	if got := testkit.GitOut(t, c.tmp, "status", "--porcelain", "--untracked-files=all"); got != c.status {
		t.Errorf("status %q, want %q", got, c.status)
	}
	ref, _ := keptRef(t, human)
	if got := testkit.GitOut(t, c.main, "rev-parse", ref); got != rebased {
		t.Errorf("%s holds %s, want the rebased line %s", ref, got, rebased)
	}
}

func (c *editCase) handedOverAtCheck(t *testing.T) bool {
	t.Helper()
	st, ok, err := wtsync.ReadState(c.gitDir)
	return err == nil && ok && st.Check != nil
}
