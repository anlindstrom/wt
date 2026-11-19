package syncfix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// sideBranch cuts name from at with one commit of file, in a worktree of
// its own under the test's temporary directory when keep is set, and in
// none otherwise; it returns the branch's tip and the worktree's path.
func sideBranch(t *testing.T, main, name, at, file string, keep bool) (tip, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), strings.ReplaceAll(name, "/", "_"))
	testkit.GitIn(t, main, "worktree", "add", "-q", "-b", name, path, at)
	gittest.WriteFile(t, filepath.Join(path, file), name+" "+file+"\n")
	testkit.GitIn(t, path, "add", "-A")
	testkit.GitIn(t, path, "commit", "-q", "--no-verify", "-m", "feat("+strings.ReplaceAll(name, "/", "-")+"): "+file)
	tip = testkit.GitOut(t, path, "rev-parse", "HEAD")
	if !keep {
		testkit.GitIn(t, main, "worktree", "remove", "--force", path)
		path = ""
	}
	return tip, path
}

// sidesOf is the run's or the look's side branches by name.
func sidesOf(s *syncrun.Stack) map[string]syncrun.StackSide {
	out := map[string]syncrun.StackSide{}
	if s == nil {
		return out
	}
	for _, x := range s.SideBranches {
		out[x.Branch] = x
	}
	return out
}

// commitWith is the commit of from..to whose subject is subject, "" for none.
func commitWith(t *testing.T, main, from, to, subject string) string {
	t.Helper()
	for _, l := range strings.Split(testkit.GitOut(t, main, "log", "--format=%H %s", from+".."+to), "\n") {
		if sha, s, ok := strings.Cut(l, " "); ok && s == subject {
			return sha
		}
	}
	return ""
}

func forkMarks(t *testing.T, main string) string {
	t.Helper()
	return testkit.GitOut(t, main, "for-each-ref", "--format=%(refname)", wtsync.ForkMapPrefix)
}

// One --stack run carries what hangs off the line: a branch forked in the
// middle of s2 onto the rewrite of the commit it forks from, a branch
// stacked on it onto its new tip, and a branch above the top, in a clean
// worktree, onto the line's new top. wt sync undo puts every one back.
func TestStackCarriesTheSideBranchesWithTheLine(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	side, _ := sideBranch(t, main, "side", b2, "x1.txt", false)
	side2, _ := sideBranch(t, main, "side2", "side", "x2.txt", false)
	above, abovePath := sideBranch(t, main, "above", gittest.StackTmp, "y1.txt", true)
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the two held layers\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	sides := sidesOf(p.Stack)
	for _, b := range []string{"side", "side2", "above"} {
		if x := sides[b]; deref(x.Result) != wtsync.MoveMoved || testkit.GitOut(t, main, "rev-parse", b) != deref(x.NewTip) {
			t.Errorf("%s: %+v (%s)\n%s", b, x, deref(x.Held), human)
		}
	}
	newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt")
	if newB2 == "" || testkit.GitOut(t, main, "rev-parse", "side^") != newB2 {
		t.Errorf("side is on %s, want the rewrite of b2 %s\n%s", testkit.GitOut(t, main, "rev-parse", "side^"), newB2, human)
	}
	if n := testkit.GitOut(t, main, "rev-list", "--count", "origin/main..side"); n != "5" {
		t.Errorf("side has %s commits above trunk, want a1 a2 b1 b2 and its own", n)
	}
	if testkit.GitOut(t, main, "rev-parse", "side2^") != testkit.GitOut(t, main, "rev-parse", "side") {
		t.Error("side2 is not on side's new tip")
	}
	if testkit.GitOut(t, main, "rev-parse", "above^") != testkit.GitOut(t, main, "rev-parse", gittest.StackTmp) {
		t.Error("above is not on the line's new top")
	}
	if head := testkit.GitOut(t, abovePath, "rev-parse", "HEAD"); head != testkit.GitOut(t, main, "rev-parse", "above") {
		t.Error("above's worktree did not follow its branch")
	}
	if st := testkit.GitOut(t, abovePath, "status", "--porcelain"); st != "" {
		t.Errorf("above's worktree is not clean: %s", st)
	}
	if m := forkMarks(t, main); m != "" {
		t.Errorf("fork markers left: %s", m)
	}

	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	for b, tip := range map[string]string{"side": side, "side2": side2, "above": above, gittest.StackTmp: s.Tips[gittest.StackTmp]} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, tip, out.String())
		}
	}
	if head := testkit.GitOut(t, abovePath, "rev-parse", "HEAD"); head != above {
		t.Error("above's worktree was not put back with its branch")
	}
}

// A side branch in a worktree with tracked changes is held with the
// command that moves it later, its real commits named; one stacked on it
// is held behind it. Nothing of either is touched.
func TestStackHoldsADirtySideBranchWithTheCommandThatMovesIt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	side, path := sideBranch(t, main, "side", b2, "x1.txt", true)
	gittest.WriteFile(t, filepath.Join(path, "x1.txt"), "work in progress\n")
	side2, _ := sideBranch(t, main, "side2", "side", "x2.txt", false)
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	if err == nil {
		t.Fatalf("the run held nothing:\n%s", human)
	}
	sides := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)
	newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt")
	want := "wt sync rebase 'side' --onto " + newB2 + " --upstream " + b2
	if x := sides["side"]; deref(x.Result) != wtsync.MoveHeld || deref(x.MoveCommand) != want {
		t.Errorf("side: %+v, want held with %q\n%s", x, want, human)
	}
	if !strings.Contains(human, want) {
		t.Errorf("the run does not print %q:\n%s", want, human)
	}
	want2 := "git rebase --no-update-refs --onto 'side' " + side + " 'side2'"
	if x := sides["side2"]; deref(x.Result) != wtsync.MoveHeld || deref(x.MoveCommand) != want2 {
		t.Errorf("side2: %+v, want held with %q\n%s", x, want2, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "side") != side || testkit.GitOut(t, main, "rev-parse", "side2") != side2 {
		t.Error("a held side branch moved")
	}
	if got, err := os.ReadFile(filepath.Join(path, "x1.txt")); err != nil || string(got) != "work in progress\n" {
		t.Errorf("side's change is gone: %q %v", got, err)
	}
}

// A backup is listed and never moved, forked mid-line or above the top:
// its commit is the same after the run and after the undo.
func TestStackListsABackupAndNeverMovesIt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	tips := map[string]string{}
	tips["backup/2026-11-01/huge-redo"], _ = sideBranch(t, main, "backup/2026-11-01/huge-redo", b2, "x1.txt", false)
	tips["safety/x-pre-fold-1"], _ = sideBranch(t, main, "safety/x-pre-fold-1", gittest.StackTmp, "y1.txt", false)
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	sides := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)
	for b, tip := range tips {
		x, ok := sides[b]
		if !ok || !x.Backup || deref(x.Result) != wtsync.MoveHeld || x.MoveCommand != nil {
			t.Errorf("%s: %+v, want a backup listed and left, with no command\n%s", b, x, human)
		}
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
			t.Errorf("%s moved to %s", b, got)
		}
	}
	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	for b, tip := range tips {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
			t.Errorf("%s is at %s after the undo, want %s", b, got, tip)
		}
	}
}

// A branch forked at s2's tip, which trunk already carries and the rebase
// drops, goes where git's list put that commit's place: after b2, the last
// commit below it that is left.
func TestStackCarriesABranchForkedAtACommitTheRebaseDrops(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	sideBranch(t, main, "side", s.Tips["s2"], "x1.txt", false)
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	x := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)["side"]
	newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt")
	if deref(x.Result) != wtsync.MoveMoved || deref(x.NewBase) != newB2 || testkit.GitOut(t, main, "rev-parse", "side^") != newB2 {
		t.Errorf("side: %+v, want moved onto the rewrite of b2 %s\n%s", x, newB2, human)
	}
	if got := testkit.GitOut(t, main, "show", "side:b3.txt"); got != "s2 b3.txt" {
		t.Errorf("side's b3.txt is %q: trunk's copy of it should be there", got)
	}
}

// --no-side-branches keeps what a run did before it carried them: none is
// listed, marked or moved.
func TestStackWithNoSideBranchesLeavesThemAsBefore(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	side, _ := sideBranch(t, main, "side", s.Tips["s2"]+"~1", "x1.txt", false)
	above, _ := sideBranch(t, main, "above", gittest.StackTmp, "y1.txt", false)
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{NoSideBranches: true}
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Stack == nil || len(p.Stack.SideBranches) != 0 {
		t.Errorf("side branches listed: %+v", p.Stack)
	}
	if testkit.GitOut(t, main, "rev-parse", "side") != side || testkit.GitOut(t, main, "rev-parse", "above") != above {
		t.Error("a side branch moved")
	}
	if strings.Contains(human, "side branch") {
		t.Errorf("the run names side branches:\n%s", human)
	}
	if m := forkMarks(t, main); m != "" {
		t.Errorf("fork markers: %s", m)
	}
}

// A line stopped at a conflict that is yours: the side branches wait with
// the layers, and wt sync resume carries them once it finishes the rebase.
func TestSyncResumeCarriesTheSideBranches(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "conflicts: []\n")
	main := ctx.Repo.MainRoot
	side, _ := sideBranch(t, main, "side", s.Tips["s2"]+"~1", "x1.txt", false)
	above, _ := sideBranch(t, main, "above", gittest.StackTmp, "y1.txt", false)
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	if err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "side") != side || testkit.GitOut(t, main, "rev-parse", "above") != above {
		t.Fatal("a side branch moved before the rebase finished")
	}
	tmp := s.Paths[gittest.StackTmp]
	if err := os.WriteFile(filepath.Join(tmp, "v.txt"), []byte("1.0.6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.GitIn(t, tmp, "add", "v.txt")
	opts := noResumeAgents()
	opts.Push = syncrun.PushNever
	resumed, _ := syncResumeJSON(t, ctx, gittest.StackTmp, opts)
	sides := sidesOf(synccmdtest.Participant(t, resumed, gittest.StackTmp).Stack)
	for _, b := range []string{"side", "above"} {
		if x := sides[b]; deref(x.Result) != wtsync.MoveMoved {
			t.Errorf("%s: %+v", b, x)
		}
	}
	if newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt"); testkit.GitOut(t, main, "rev-parse", "side^") != newB2 {
		t.Error("side is not on the rewrite of b2")
	}
	if testkit.GitOut(t, main, "rev-parse", "above^") != testkit.GitOut(t, main, "rev-parse", gittest.StackTmp) {
		t.Error("above is not on the line's new top")
	}
}

// A side branch whose own commit conflicts with where it goes is held with
// the command, and that command is a run of its own: it stops at the
// conflict, wt sync resume finishes it, and the branch lands where the
// command said.
func TestStackHoldsAConflictingSideBranchAndItsCommandResumes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	path := filepath.Join(t.TempDir(), "side")
	testkit.GitIn(t, main, "worktree", "add", "-q", "-b", "side", path, b2)
	// Trunk changed t.txt's first line; so does side.
	gittest.WriteFile(t, filepath.Join(path, "t.txt"), strings.Replace(testkit.GitOut(t, path, "show", "HEAD:t.txt")+"\n", "t\n", "side\n", 1))
	testkit.GitIn(t, path, "commit", "-q", "--no-verify", "-am", "feat(side): t.txt")
	side := testkit.GitOut(t, path, "rev-parse", "HEAD")
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	x := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)["side"]
	newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt")
	want := "wt sync rebase 'side' --onto " + newB2 + " --upstream " + b2
	if deref(x.Result) != wtsync.MoveHeld || deref(x.MoveCommand) != want || !strings.Contains(deref(x.Held), "t.txt") {
		t.Fatalf("side: %+v, want held at its conflict with %q\n%s", x, want, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "side") != side {
		t.Fatal("side moved")
	}
	opts := synccmdtest.Onto(newB2, b2)
	opts.Now = synccmdtest.NoAgents().Now
	var out bytes.Buffer
	if err := synccmd.SyncRebase(ctx, []string{"side"}, opts, &out); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("the command: err %v, want it handed over\n%s", err, out.String())
	}
	gittest.WriteFile(t, filepath.Join(path, "t.txt"), "side\n"+strings.Repeat("filler\n", 10)+"last\n")
	testkit.GitIn(t, path, "add", "t.txt")
	ropts := noResumeAgents()
	ropts.Push = syncrun.PushNever
	if _, err := syncResumeJSON(t, ctx, "side", ropts); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if testkit.GitOut(t, main, "rev-parse", "side^") != newB2 {
		t.Error("side is not where the command put it")
	}
	if got := testkit.GitOut(t, main, "show", "side:t.txt"); !strings.HasPrefix(got, "side\n") {
		t.Errorf("side's t.txt is %q", got)
	}
}

// The look lists what hangs off the line: where each forks, what it is on,
// what holds it, and whether the run would carry it.
func TestSyncStackLookListsTheSideBranches(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	side, path := sideBranch(t, main, "side", b2, "x1.txt", true)
	gittest.WriteFile(t, filepath.Join(path, "x1.txt"), "work in progress\n")
	sideBranch(t, main, "side2", "side", "x2.txt", false)
	sideBranch(t, main, "above", gittest.StackTmp, "y1.txt", false)
	sideBranch(t, main, "backup/x", gittest.StackTmp, "z1.txt", false)
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}})
	sides := sidesOf(o.Stack)
	if x := sides["side"]; x.Kind != syncrun.SideForked || x.Fork != b2 || x.ForkSubject != "feat(s2): b2.txt" || x.OldTip != side ||
		x.State != wtsync.HolderDirty || x.Carry || !strings.Contains(deref(x.Left), "dirty") || x.Result != nil {
		t.Errorf("side: %+v", x)
	}
	if x := sides["side2"]; deref(x.On) != "side" || x.Carry || !strings.Contains(deref(x.Left), "side") {
		t.Errorf("side2: %+v", x)
	}
	if x := sides["above"]; x.Kind != syncrun.SideAbove || !x.Carry || x.Left != nil || x.State != wtsync.HolderNone {
		t.Errorf("above: %+v", x)
	}
	if x := sides["backup/x"]; !x.Backup || x.Carry {
		t.Errorf("backup/x: %+v", x)
	}
	var out bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, gittest.StackTmp, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"side branch", "side2", "above", "backup/x"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the look lacks %q:\n%s", want, out.String())
		}
	}
}

// The case of the issue, end to end: a line with fixups written against
// its top, an each_commit step, a branch cut in the middle of a layer with
// another stacked on it, one cut at a fixup the fold moves away, and one
// above the top in a worktree of its own. One run folds the line onto
// trunk and carries every one of them onto the commits the fold made,
// fixups folded in below them; wt sync undo puts every branch and worktree
// back.
func TestStackFoldOntoTrunkCarriesEverySideBranch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, synccmdtest.StackYAML+"each_commit:\n  - run: test -f t.txt\n")
	main := ctx.Repo.MainRoot
	tmp := s.Paths[gittest.StackTmp]
	fix := func(file, subject string) string {
		gittest.WriteFile(t, filepath.Join(tmp, file), "fixed\n")
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-am", "fixup! "+subject)
		return testkit.GitOut(t, tmp, "rev-parse", "HEAD")
	}
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	first := fix("b1.txt", "feat(s2): b1.txt")
	fix("e1.txt", "feat(s5): e1.txt")
	old := map[string]string{}
	old["list-drag"], _ = sideBranch(t, main, "list-drag", b2, "x1.txt", false)
	old["ui-check"], _ = sideBranch(t, main, "ui-check", "list-drag", "x2.txt", false)
	old["at-fixup"], _ = sideBranch(t, main, "at-fixup", first, "w1.txt", false)
	var pass2 string
	old["ui-pass2"], pass2 = sideBranch(t, main, "ui-pass2", gittest.StackTmp, "y1.txt", true)
	old[gittest.StackTmp] = testkit.GitOut(t, main, "rev-parse", gittest.StackTmp)

	opts := synccmdtest.Onto("main", "")
	opts.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the two held layers\n%s", err, human)
	}
	p := synccmdtest.Participant(t, res, gittest.StackTmp)
	if p.Result != syncrun.ResultRebased || p.Checked == 0 {
		t.Fatalf("line %+v\n%s", p, human)
	}
	sides := sidesOf(p.Stack)
	for _, b := range []string{"list-drag", "ui-check", "at-fixup", "ui-pass2"} {
		if x := sides[b]; deref(x.Result) != wtsync.MoveMoved {
			t.Errorf("%s: %+v (%s)\n%s", b, x, deref(x.Held), human)
		}
	}
	if out := testkit.GitOut(t, main, "log", "--format=%s", "main..ui-check", "main..at-fixup", "main..ui-pass2", "--grep=^fixup!"); out != "" {
		t.Errorf("a fixup is left under a side branch: %s", out)
	}
	newB2 := commitWith(t, main, "main", gittest.StackTmp, "feat(s2): b2.txt")
	if testkit.GitOut(t, main, "rev-parse", "list-drag^") != newB2 {
		t.Errorf("list-drag is not on the fold's b2\n%s", human)
	}
	if got := testkit.GitOut(t, main, "show", "list-drag:b1.txt"); got != "fixed" {
		t.Errorf("list-drag's b1.txt is %q, want the fixup folded in below it", got)
	}
	if testkit.GitOut(t, main, "rev-parse", "ui-check^") != testkit.GitOut(t, main, "rev-parse", "list-drag") {
		t.Error("ui-check is not on list-drag's new tip")
	}
	if got := testkit.GitOut(t, main, "log", "-1", "--format=%s", "at-fixup^"); got != "feat(s5): e3.txt" {
		t.Errorf("at-fixup is on %q, want s5's tip, where the commit below its fork ends", got)
	}
	if testkit.GitOut(t, main, "rev-parse", "ui-pass2^") != testkit.GitOut(t, main, "rev-parse", gittest.StackTmp) {
		t.Error("ui-pass2 is not on the line's new top")
	}
	if testkit.GitOut(t, pass2, "rev-parse", "HEAD") != testkit.GitOut(t, main, "rev-parse", "ui-pass2") {
		t.Error("ui-pass2's worktree did not follow")
	}
	if strings.Contains(human, "wt sync rebase ui-pass2") {
		t.Errorf("the run tells you to move ui-pass2, which it carried:\n%s", human)
	}

	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	for b, tip := range old {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
			t.Errorf("%s is at %s after the undo, want %s", b, got, tip)
		}
	}
	if testkit.GitOut(t, pass2, "rev-parse", "HEAD") != old["ui-pass2"] {
		t.Error("ui-pass2's worktree was not put back")
	}
	if m := forkMarks(t, main); m != "" {
		t.Errorf("fork markers left: %s", m)
	}
}

// With --upstream below where trunk leaves, the line holds trunk's own
// commits: trunk, a branch cut from them, and a protected name are never
// side branches, and none of them moves. A real side branch still does.
func TestStackNeverTakesTrunkOrItsHistoryAsASideBranch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	m1 := strings.Split(testkit.GitOut(t, main, "rev-list", "--reverse", s.Base+"..main"), "\n")[0]
	top := filepath.Join(t.TempDir(), "top")
	testkit.GitIn(t, main, "worktree", "add", "-q", "-b", "top", top, m1)
	commit := func(file string) string {
		gittest.WriteFile(t, filepath.Join(top, file), file+"\n")
		testkit.GitIn(t, top, "add", "-A")
		testkit.GitIn(t, top, "commit", "-q", "--no-verify", "-m", "feat(top): "+file)
		return testkit.GitOut(t, top, "rev-parse", "HEAD")
	}
	x := commit("x.txt")
	testkit.GitIn(t, main, "branch", "lx", x)
	commit("y.txt")
	untouched := map[string]string{"main": testkit.GitOut(t, main, "rev-parse", "main")}
	untouched["trunk-fork"], _ = sideBranch(t, main, "trunk-fork", m1, "f1.txt", false)
	untouched["release/2"], _ = sideBranch(t, main, "release/2", "lx", "r1.txt", false)
	realTip, _ := sideBranch(t, main, "real", "lx", "q1.txt", false)
	synccmdtest.Branch(t, ctx, "integ", s.Base, map[string]string{"k.txt": "k\n"})
	res, human, _ := synccmdtest.StackRun(t, ctx, "top", synccmdtest.Onto("integ", s.Base))
	p := synccmdtest.Participant(t, res, "top")
	if p.Result != syncrun.ResultRebased {
		t.Fatalf("line %+v\n%s", p, human)
	}
	sides := sidesOf(p.Stack)
	for b, tip := range untouched {
		if _, ok := sides[b]; ok {
			t.Errorf("%s is listed as a side branch\n%s", b, human)
		}
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
			t.Errorf("%s moved to %s, was %s", b, got, tip)
		}
	}
	if x := sides["real"]; deref(x.Result) != wtsync.MoveMoved || testkit.GitOut(t, main, "rev-parse", "real") == realTip {
		t.Errorf("real: %+v\n%s", x, human)
	}
}

// A side branch with a merge commit of its own is held with no command: a
// rebase would drop the merge. It says to move it by hand.
func TestStackGivesNoCommandForASideBranchWithAMerge(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	sideBranch(t, main, "m2", s.Tips["s1"], "x2.txt", false)
	_, path := sideBranch(t, main, "m1", s.Tips["s1"], "x1.txt", true)
	testkit.GitIn(t, path, "merge", "-q", "--no-edit", "--no-verify", "m2")
	m1 := testkit.GitOut(t, path, "rev-parse", "HEAD")
	testkit.GitIn(t, main, "worktree", "remove", "--force", path)
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	x := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)["m1"]
	if deref(x.Result) != wtsync.MoveHeld || x.MoveCommand != nil || !strings.Contains(deref(x.Held), "by hand") {
		t.Errorf("m1: %+v, want held to be moved by hand, with no command\n%s", x, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "m1") != m1 {
		t.Error("m1 moved")
	}
}

// The commands a held side branch is given run as printed: the branch name
// quoted for the shell, and a git rebase that moves no other branch.
func TestStackQuotesItsCommandsAndUpdatesNoOtherRef(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	name := "q$(id)"
	path := filepath.Join(t.TempDir(), "q")
	testkit.GitIn(t, main, "worktree", "add", "-q", "-b", name, path, b2)
	gittest.WriteFile(t, filepath.Join(path, "t.txt"), strings.Replace(testkit.GitOut(t, path, "show", "HEAD:t.txt")+"\n", "t\n", "q\n", 1))
	testkit.GitIn(t, path, "commit", "-q", "--no-verify", "-am", "feat(q): t.txt")
	testkit.GitIn(t, main, "worktree", "remove", "--force", path)
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	x := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)[name]
	newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt")
	want := "git rebase --no-update-refs --onto " + newB2 + " " + b2 + " 'q$(id)'"
	if deref(x.MoveCommand) != want {
		t.Errorf("%s: command %q, want %q\n%s", name, deref(x.MoveCommand), want, human)
	}
}

// A held side branch checked out in the main checkout, which wt sync rebase
// does not take, is given a git rebase run there.
func TestStackGivesASideBranchInTheMainCheckoutAGitCommand(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	side, _ := sideBranch(t, main, "side", b2, "x1.txt", false)
	testkit.GitIn(t, main, "checkout", "-q", "side")
	gittest.WriteFile(t, filepath.Join(main, "x1.txt"), "work in progress\n")
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	x := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)["side"]
	newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt")
	want := "git -C '" + main + "' rebase --no-update-refs --onto " + newB2 + " " + b2 + " 'side'"
	if deref(x.Result) != wtsync.MoveHeld || deref(x.MoveCommand) != want {
		t.Errorf("side: %+v, want held with %q\n%s", x, want, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "side") != side {
		t.Error("side moved")
	}
}

// The text look follows --no-side-branches and --alone as the run does.
func TestSyncStackTextLookFollowsNoSideBranchesAndAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	sideBranch(t, ctx.Repo.MainRoot, "side", s.Tips["s2"]+"~1", "x1.txt", false)
	look := func(opts synccmd.SyncOptions) string {
		var out bytes.Buffer
		if err := synccmd.SyncWorktree(ctx, gittest.StackTmp, opts, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		return out.String()
	}
	if out := look(synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp, NoSideBranches: true}}); strings.Contains(out, "side branch") {
		t.Errorf("--no-side-branches still lists them:\n%s", out)
	}
	if out := look(synccmd.SyncOptions{NoFetch: true, Alone: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}}); !strings.Contains(out, "held: --alone") {
		t.Errorf("--alone lists a side branch as carried:\n%s", out)
	}
}

// --alone holds every side branch with its command, read after the
// rebase's drops: a fork above a commit the rebase empties (trunk made the
// same change in a commit of its own) still gets one.
func TestStackAloneGivesACommandPastAnEmptiedCommit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	gittest.WriteFile(t, filepath.Join(main, "c1.txt"), "s3 c1.txt\n")
	gittest.WriteFile(t, filepath.Join(main, "z.txt"), "z\n")
	testkit.GitIn(t, main, "add", "-A")
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "feat: trunk's c1 and z")
	testkit.GitIn(t, main, "fetch", "-q", "origin")
	sideBranch(t, main, "side", s.Tips["s3"], "x1.txt", false)
	opts := synccmdtest.NoAgents()
	opts.Alone = true
	opts.Stack = &synccmd.StackOptions{}
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, opts)
	if err != nil {
		t.Fatalf("%v\n%s", err, human)
	}
	if !strings.Contains(human, "feat(s3): c1.txt [layer s3]") {
		t.Fatalf("the rebase did not empty s3's c1.txt:\n%s", human)
	}
	x := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)["side"]
	newV := commitWith(t, main, "origin/main", gittest.StackTmp, "chore(s3): bump to 1.0.1")
	if newV == "" || deref(x.Result) != wtsync.MoveHeld || !strings.Contains(deref(x.MoveCommand), "--onto "+newV) {
		t.Errorf("side: %+v, want held with a command onto the rebased bump %s\n%s", x, newV, human)
	}
}

// A side branch that shares its name with a tag is still read, carried and
// put back as that branch: the tag is untouched and no heads/<name> branch
// appears.
func TestStackCarriesASideBranchNamedLikeATag(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	b2 := testkit.GitOut(t, main, "rev-parse", s.Tips["s2"]+"~1")
	topic, _ := sideBranch(t, main, "topic", b2, "x1.txt", false)
	testkit.GitIn(t, main, "tag", "topic", s.Base)
	res, human, _ := synccmdtest.StackRun(t, ctx, gittest.StackTmp, synccmdtest.NoAgents())
	sides := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)
	newB2 := commitWith(t, main, "origin/main", gittest.StackTmp, "feat(s2): b2.txt")
	if x, ok := sides["topic"]; !ok || deref(x.Result) != wtsync.MoveMoved {
		t.Errorf("topic: %+v (listed %v), want moved\n%s", x, ok, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "refs/heads/topic^") != newB2 {
		t.Error("the branch topic is not on the rewrite of b2")
	}
	if refs := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/heads/"); refs != "" {
		t.Errorf("a heads/ branch appeared: %s", refs)
	}
	if testkit.GitOut(t, main, "rev-parse", "refs/tags/topic") != s.Base {
		t.Error("the tag moved")
	}
	var out bytes.Buffer
	if err := SyncUndo(ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	if testkit.GitOut(t, main, "rev-parse", "refs/heads/topic") != topic {
		t.Errorf("undo did not put topic back\n%s", out.String())
	}
}
