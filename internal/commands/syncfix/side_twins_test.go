package syncfix

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// twinLine is the shape of the sites line: the fixture's line with two
// commits more on its top, a branch above the top (list-drag), one forked
// off it (ui-check), two branches at one commit above that (ui-pass2, which
// has an upstream, and chore/live, a throwaway cut from it, in a worktree
// of its own), and one stacked on the pair (pass3).
type twinLine struct {
	ctx                           *kit.Context
	s                             *gittest.StackLine
	top, drag, check, twin, pass3 string
	livePath                      string
}

func newTwinLine(t *testing.T) *twinLine {
	t.Helper()
	ctx, s := synccmdtest.StackLine(t, "")
	main := ctx.Repo.MainRoot
	l := &twinLine{ctx: ctx, s: s}
	tmp := s.Paths[gittest.StackTmp]
	for _, f := range []string{"f1.txt", "f2.txt"} {
		gittest.WriteFile(t, filepath.Join(tmp, f), f+"\n")
		testkit.GitIn(t, tmp, "add", "-A")
		testkit.GitIn(t, tmp, "commit", "-q", "--no-verify", "-m", "feat(top): "+f)
	}
	l.top = testkit.GitOut(t, main, "rev-parse", gittest.StackTmp)
	l.drag, _ = sideBranch(t, main, "list-drag", gittest.StackTmp, "y1.txt", false)
	l.check, _ = sideBranch(t, main, "ui-check", "list-drag", "y2.txt", false)
	l.twin, _ = sideBranch(t, main, "ui-pass2", "ui-check", "y3.txt", false)
	testkit.GitIn(t, main, "config", "branch.ui-pass2.remote", "origin")
	testkit.GitIn(t, main, "config", "branch.ui-pass2.merge", "refs/heads/ui-pass2")
	l.livePath = filepath.Join(t.TempDir(), "live")
	testkit.GitIn(t, main, "worktree", "add", "-q", "-b", "chore/live", l.livePath, "ui-pass2")
	l.pass3, _ = sideBranch(t, main, "pass3", "ui-pass2", "y4.txt", false)
	return l
}

func lookText(t *testing.T, ctx *kit.Context) string {
	t.Helper()
	var out bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, gittest.StackTmp, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}}, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// rowOf is the first row of the look's tables, under a heading, that
// starts with name.
func rowOf(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "    ") && strings.HasPrefix(strings.TrimSpace(l), name+" ") {
			return l
		}
	}
	return ""
}

// The look lists the named top as the last layer, marked as the top, with
// its own commit count, in text and in --json; the layers themselves stay
// the branches a run moves.
func TestSyncStackLookListsTheTopAsTheLastLayer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	l := newTwinLine(t)
	o := synccmdtest.OverviewJSONWith(t, l.ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}})
	if o.Stack == nil || o.Stack.Top == nil {
		t.Fatalf("no top in the look: %+v", o.Stack)
	}
	top := o.Stack.Top
	if top.Branch != gittest.StackTmp || top.OldTip != l.top || top.Commits != 14 || top.Subject != "feat(top): f2.txt" ||
		deref(top.Worktree) != l.s.Paths[gittest.StackTmp] {
		t.Errorf("top %+v, want tmp/stack at %s with 14 commits", top, l.top)
	}
	if n := len(o.Stack.Layers); n != 5 || o.Stack.Layers[4].Branch != "s5" {
		t.Errorf("layers %+v, want s1..s5 and no more", o.Stack.Layers)
	}
	out := lookText(t, l.ctx)
	lines := strings.Split(out, "\n")
	s5 := slicesIndex(lines, rowOf(out, "s5"))
	row := rowOf(out, gittest.StackTmp)
	if s5 < 0 || row == "" || slicesIndex(lines, row) != s5+1 {
		t.Fatalf("the top is not the row after s5:\n%s", out)
	}
	if !strings.Contains(row, "  14  ") || !strings.Contains(row, "the top") || !strings.Contains(row, "feat(top): f2.txt") {
		t.Errorf("top row %q, want its 14 commits, its subject, marked the top", row)
	}
}

func slicesIndex(lines []string, l string) int {
	for i, x := range lines {
		if l != "" && x == l {
			return i
		}
	}
	return -1
}

// Two branches at one commit are siblings in the look: each says it is the
// same commit as the other, neither is on the other with 0 commits, and
// what is stacked above them is on the one with an upstream. A process in
// the throwaway's worktree holds both, said once and short, with what frees
// it.
func TestSyncStackLookShowsTwinsAsSiblingsHeldTogether(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	l := newTwinLine(t)
	pid := gittest.StartIn(t, l.livePath)
	o := synccmdtest.OverviewJSONWith(t, l.ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}})
	sides := sidesOf(o.Stack)
	pass2, live, pass3 := sides["ui-pass2"], sides["chore/live"], sides["pass3"]
	if deref(pass2.On) != "ui-check" || deref(live.On) != "ui-check" || pass2.Commits != 1 || live.Commits != 1 {
		t.Errorf("ui-pass2 %+v, chore/live %+v: want both on ui-check, one commit each", pass2, live)
	}
	if strings.Join(pass2.SameTip, " ") != "chore/live" || strings.Join(live.SameTip, " ") != "ui-pass2" {
		t.Errorf("sameTip: ui-pass2 %v, chore/live %v", pass2.SameTip, live.SameTip)
	}
	why := fmt.Sprintf("a process in its worktree: sleep (pid %d)", pid)
	if live.Carry || deref(live.Left) != why {
		t.Errorf("chore/live left %q, want %q", deref(live.Left), why)
	}
	if pass2.Carry || !strings.Contains(deref(pass2.Left), "chore/live") {
		t.Errorf("ui-pass2 %+v, want held with chore/live", pass2)
	}
	if deref(pass3.On) != "ui-pass2" || pass3.Carry {
		t.Errorf("pass3 %+v, want on ui-pass2 and held", pass3)
	}
	out := lookText(t, l.ctx)
	for name, other := range map[string]string{"ui-pass2": "chore/live", "chore/live": "ui-pass2"} {
		row := rowOf(out, name)
		if !strings.Contains(row, "same commit as "+other) || strings.Contains(row, "0 commits") || strings.Contains(row, "on "+other) {
			t.Errorf("%s row %q, want it the same commit as %s and on neither", name, row, other)
		}
	}
	row := rowOf(out, "chore/live")
	if strings.Count(row, "pid") != 1 || !strings.Contains(row, why) || !strings.Contains(row, "stop it, and a run carries it") {
		t.Errorf("chore/live row %q, want %q once, and what frees it", row, why)
	}
	if strings.Contains(row, "process: pid") {
		t.Errorf("chore/live row %q still says the raw state", row)
	}
}

// A run carries both twins onto one new commit and what is stacked on them
// onto it; wt sync undo puts every one back, the worktree with its branch.
func TestStackCarriesTwinsOntoOneCommitAndUndoPutsThemBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	l := newTwinLine(t)
	main := l.ctx.Repo.MainRoot
	res, human, err := synccmdtest.StackRun(t, l.ctx, gittest.StackTmp, synccmdtest.NoAgents())
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("err %v, want the fixture's two held layers\n%s", err, human)
	}
	sides := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)
	for _, b := range []string{"list-drag", "ui-check", "ui-pass2", "chore/live", "pass3"} {
		if x := sides[b]; deref(x.Result) != wtsync.MoveMoved {
			t.Errorf("%s: %+v (%s)\n%s", b, x, deref(x.Held), human)
		}
	}
	pass2 := testkit.GitOut(t, main, "rev-parse", "ui-pass2")
	if live := testkit.GitOut(t, main, "rev-parse", "chore/live"); live != pass2 || pass2 == l.twin {
		t.Errorf("ui-pass2 %s, chore/live %s: want one new commit\n%s", pass2, live, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "ui-pass2^") != testkit.GitOut(t, main, "rev-parse", "ui-check") {
		t.Error("the twins are not on ui-check's new tip")
	}
	if testkit.GitOut(t, main, "rev-parse", "pass3^") != pass2 {
		t.Error("pass3 is not on the twins' new commit")
	}
	if head := testkit.GitOut(t, l.livePath, "rev-parse", "HEAD"); head != pass2 {
		t.Error("chore/live's worktree did not follow its branch")
	}
	var out bytes.Buffer
	if err := SyncUndo(l.ctx, gittest.StackTmp, noAgentsUndo(), &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	for b, tip := range map[string]string{"list-drag": l.drag, "ui-check": l.check, "ui-pass2": l.twin, "chore/live": l.twin, "pass3": l.pass3, gittest.StackTmp: l.top} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, tip, out.String())
		}
	}
	if head := testkit.GitOut(t, l.livePath, "rev-parse", "HEAD"); head != l.twin {
		t.Error("chore/live's worktree was not put back")
	}
	if st := testkit.GitOut(t, l.livePath, "status", "--porcelain"); st != "" {
		t.Errorf("chore/live's worktree is not clean after the undo: %s", st)
	}
}

// A process in one twin's worktree holds both: neither moves, what is on
// them is held behind them, and the run prints for each the exact command
// that moves it later.
func TestStackHoldsTwinsTogetherWithTheCommandThatMovesEach(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	l := newTwinLine(t)
	main := l.ctx.Repo.MainRoot
	pid := gittest.StartIn(t, l.livePath)
	res, human, err := synccmdtest.StackRun(t, l.ctx, gittest.StackTmp, synccmdtest.NoAgents())
	if err == nil {
		t.Fatalf("the run held nothing:\n%s", human)
	}
	sides := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)
	for _, b := range []string{"list-drag", "ui-check"} {
		if x := sides[b]; deref(x.Result) != wtsync.MoveMoved {
			t.Errorf("%s: %+v (%s)\n%s", b, x, deref(x.Held), human)
		}
	}
	check := testkit.GitOut(t, main, "rev-parse", "ui-check")
	// chore/live goes onto ui-pass2 once that has moved: the same commit.
	wantLive := "wt sync rebase 'chore/live' --onto 'ui-pass2' --upstream " + l.twin
	wantPass2 := "git rebase --no-update-refs --onto " + check + " " + l.check + " 'ui-pass2'"
	for b, want := range map[string]string{"chore/live": wantLive, "ui-pass2": wantPass2} {
		if x := sides[b]; deref(x.Result) != wtsync.MoveHeld || deref(x.MoveCommand) != want {
			t.Errorf("%s: %+v (%s), want held with %q\n%s", b, x, deref(x.Held), want, human)
		}
		if !strings.Contains(human, want) {
			t.Errorf("the run does not print %q:\n%s", want, human)
		}
	}
	if x := sides["pass3"]; deref(x.Result) != wtsync.MoveHeld {
		t.Errorf("pass3: %+v, want held behind the twins", x)
	}
	held := fmt.Sprintf("a process in its worktree: sleep (pid %d)", pid)
	if x := sides["chore/live"]; !strings.Contains(deref(x.Held), held) {
		t.Errorf("chore/live held %q, want %q", deref(x.Held), held)
	}
	for b, tip := range map[string]string{"ui-pass2": l.twin, "chore/live": l.twin, "pass3": l.pass3} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != tip {
			t.Errorf("%s moved to %s", b, got)
		}
	}
}

// A twin that turns dirty after the branch it moves with has moved is held
// with a command onto that branch from its own tip: nothing of it replayed
// twice, nothing of it dropped. Its change and its commit are untouched.
func TestStackHoldsATwinThatTurnsDirtyWithACommandOntoItsSibling(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	l := newTwinLine(t)
	main := l.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Stack = &synccmd.StackOptions{AfterMove: func(m wtsync.LayerMove) error {
		if m.Branch == "ui-pass2" {
			gittest.WriteFile(t, filepath.Join(l.livePath, "y3.txt"), "work in progress\n")
		}
		return nil
	}}
	res, human, _ := synccmdtest.StackRun(t, l.ctx, gittest.StackTmp, opts)
	sides := sidesOf(synccmdtest.Participant(t, res, gittest.StackTmp).Stack)
	if x := sides["ui-pass2"]; deref(x.Result) != wtsync.MoveMoved {
		t.Fatalf("ui-pass2: %+v (%s)\n%s", x, deref(x.Held), human)
	}
	want := "wt sync rebase 'chore/live' --onto 'ui-pass2' --upstream " + l.twin
	if x := sides["chore/live"]; deref(x.Result) != wtsync.MoveHeld || deref(x.MoveCommand) != want {
		t.Errorf("chore/live: %+v (%s), want held with %q\n%s", x, deref(x.Held), want, human)
	}
	if got := testkit.GitOut(t, main, "rev-parse", "chore/live"); got != l.twin {
		t.Errorf("chore/live moved to %s", got)
	}
	if st := strings.TrimSpace(testkit.GitOut(t, l.livePath, "status", "--porcelain")); st != "M y3.txt" {
		t.Errorf("chore/live's change: %q", st)
	}
}
