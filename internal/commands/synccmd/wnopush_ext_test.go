package synccmd_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// newTwoStopLine is a pushedLine whose run stops twice: trunk adds a1.txt,
// which s1's first commit adds too, and bumps v.txt, which s3 bumps. Origin
// holds every layer and trunk, and not the line's own branch, as a tmp
// branch nobody pushed.
func newTwoStopLine(t *testing.T) *pushedLine {
	t.Helper()
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	testkit.WriteFile(t, main, "a1.txt", "trunk a1\n")
	testkit.GitIn(t, main, "add", "a1.txt")
	testkit.GitIn(t, main, "commit", "-q", "--no-verify", "-m", "feat: trunk's a1")
	testkit.GitIn(t, main, "push", "-q", "origin", "main")
	testkit.GitIn(t, p.bare, "branch", "-q", "-D", gittest.StackTmp)
	testkit.GitIn(t, main, "fetch", "-q", "--prune", "origin")
	return p
}

// wt#223, rework 1: a run that was not told --no-push (nobody to ask, so it
// pushes nothing) is resumed with --no-push, and that resume is cut short
// after it took the stop over, before the rebase went on. The --no-push is
// kept with the handover by then: a retry with a yes to the push question finishes the run and
// pushes nothing, and origin is as it was after both.
func TestResumeKeepsItsOwnNoPushThroughAnInterrupt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	remote := bareRefs(t, p.bare)
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, synccmdtest.NoAgents()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want a handover at v.txt\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")

	var cut syncfix.ResumeOptions
	cut.Push = syncrun.PushNever
	cut.AfterKeepStop = func() error { return errors.New("interrupted") }
	if _, human, err := resumeAs(t, p, cut); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("cut resume: err %v, want the interrupt\n%s", err, human)
	}
	if busy, _ := wtsync.RebaseInProgress(tmp); !busy {
		t.Fatal("the cut resume did not leave the rebase at its stop")
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Fatalf("the cut resume changed origin:\n%s\nwas\n%s", got, remote)
	}

	yes := syncfix.ResumeOptions{}
	yes.Push = syncrun.PushAsk
	yes.ConfirmPush = func([]string) bool { return true }
	res, human, err := resumeAs(t, p, yes)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("retry: err %v, want the two held layers named\n%s", err, human)
	}
	if testkit.GitOut(t, main, "rev-parse", "s1") == p.s.Tips["s1"] {
		t.Fatalf("the retry did not move s1\n%s", human)
	}
	if got := bareRefs(t, p.bare); got != remote {
		t.Errorf("the retry pushed what --no-push kept back: origin is\n%s\nwas\n%s\n%s", got, remote, human)
	}
	if line := synccmdtest.Participant(t, res, gittest.StackTmp); line.Pushed || line.Stack == nil || line.Stack.Push != nil {
		t.Errorf("the retry's JSON says it pushed\n%s", human)
	}
	if !strings.Contains(human, "  = not pushed: the run was told --no-push\n") {
		t.Errorf("the retry does not say why it did not push:\n%s", human)
	}
}

// resumeAs is wt sync resume of the line with opts, its JSON and what it
// printed.
func resumeAs(t *testing.T, p *pushedLine, opts syncfix.ResumeOptions) (syncrun.RunResult, string, error) {
	t.Helper()
	var out, human bytes.Buffer
	opts.Agents = []agents.Agent{}
	opts.Now = func() time.Time { return time.Unix(5, 0) }
	opts.Probes = synccmdtest.DockerUp()
	opts.Journal = syncrun.NewSyncRunJournal(&out, "sync resume")
	err := syncfix.SyncResume(p.ctx, gittest.StackTmp, opts, &human)
	return synccmdtest.DecodeSyncRun(t, out.Bytes()), human.String(), err
}

// yesToPush is a yes to the push question, as a person at the terminal
// gives it.
func yesToPush() syncfix.ResumeOptions {
	var o syncfix.ResumeOptions
	o.Push = syncrun.PushAsk
	o.ConfirmPush = func([]string) bool { return true }
	return o
}

// yesFlag is what --yes makes of the push on a resume.
func yesFlag() syncfix.ResumeOptions {
	var o syncfix.ResumeOptions
	o.Push, o.PushByYes = syncrun.PushAlways, true
	return o
}

// noPushRunResumed is a --stack --no-push run of the line stopped at a1.txt,
// resumed with a yes to the push question to its second stop at v.txt, and
// resumed again with last. Origin must not change before the last resume.
func (p *pushedLine) noPushRunResumed(t *testing.T, last syncfix.ResumeOptions) (syncrun.RunResult, string, error) {
	t.Helper()
	remoteBefore := bareRefs(t, p.bare)
	tmp := p.s.Paths[gittest.StackTmp]
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("run: err %v, want a handover at a1.txt\n%s", err, human)
	}
	if got := bareRefs(t, p.bare); got != remoteBefore {
		t.Fatalf("the --no-push run changed origin:\n%s\nwas\n%s", got, remoteBefore)
	}
	testkit.WriteFile(t, tmp, "a1.txt", "s1 a1.txt\n")
	testkit.GitIn(t, tmp, "add", "a1.txt")
	if _, human, err := resumeAs(t, p, yesToPush()); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("first resume: err %v, want a second handover at v.txt\n%s", err, human)
	}
	if got := bareRefs(t, p.bare); got != remoteBefore {
		t.Fatalf("the first resume changed origin:\n%s\nwas\n%s", got, remoteBefore)
	}
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	return resumeAs(t, p, last)
}

// wt#223: a --stack run started with --no-push that stops twice is resumed,
// each time with a yes to the push question: a person's, or --yes's. No
// resume pushes: origin's refs are as they were after every leg, the final
// JSON says nothing was pushed and holds no push, every moved layer keeps the
// push still owed, and the record keeps all of them for wt sync push --stack.
func TestResumeOfANoPushRunNeverPushes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	for name, last := range map[string]syncfix.ResumeOptions{"asked": yesToPush(), "--yes": yesFlag()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resumedWithoutPush(t, last)
		})
	}
}

// resumedWithoutPush is TestResumeOfANoPushRunNeverPushes with last as the
// last resume's push.
func resumedWithoutPush(t *testing.T, last syncfix.ResumeOptions) {
	p := newTwoStopLine(t)
	main := p.ctx.Repo.MainRoot
	remoteBefore := bareRefs(t, p.bare)
	res, human, err := p.noPushRunResumed(t, last)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("last resume: err %v, want the two layers a worktree holds named as held\n%s", err, human)
	}
	if got := bareRefs(t, p.bare); got != remoteBefore {
		t.Errorf("the last resume of a --no-push run pushed: origin is\n%s\nwas\n%s\n%s", got, remoteBefore, human)
	}
	if strings.Contains(human, "✓ pushed") || strings.Contains(human, "atomic push") {
		t.Errorf("the last resume says it pushed:\n%s", human)
	}
	if !strings.Contains(human, "  = not pushed: the run was told --no-push\n") {
		t.Errorf("the last resume does not say why it did not push:\n%s", human)
	}
	line := synccmdtest.Participant(t, res, gittest.StackTmp)
	if line.Pushed {
		t.Error("the JSON says the line was pushed")
	}
	if line.Stack == nil {
		t.Fatalf("no stack in the JSON\n%s", human)
	}
	if line.Stack.Push != nil {
		t.Errorf("the JSON holds a stack push %+v", *line.Stack.Push)
	}
	for _, l := range line.Stack.Layers {
		if l.Result != nil && *l.Result == wtsync.MoveMoved && l.PushCommand == nil {
			t.Errorf("moved layer %s has no push command, though nothing pushed it", l.Branch)
		}
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == p.s.Tips[b] {
			t.Fatalf("%s did not move\n%s", b, human)
		}
	}
	rec, ok, rerr := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if rerr != nil || !ok {
		t.Fatalf("no push record after the resume: %v\n%s", rerr, human)
	}
	if want, got := "s1,s2,s4,"+gittest.StackTmp, strings.Join(rec.Branches(), ","); got != want {
		t.Errorf("the record holds %s, want %s", got, want)
	}
	if !strings.Contains(human, "wt sync push tmp/stack --stack pushes them later") {
		t.Errorf("the resume does not say what is left for a later push:\n%s", human)
	}
}

// wt#223: run A, with --no-push, moves s1, s2 and s4 and keeps them for a
// later push; run B, a fold of a fixup of s5, moves only the line and leaves
// s1 to s4 as A put them. B's --push sends what A left too, over A's leases,
// so origin holds the whole stack as it stands, and the record is gone.
func TestRunPushSendsTheLayersAnEarlierRunLeftUnpushed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLine(t)
	main := p.ctx.Repo.MainRoot
	a := synccmdtest.At(synccmdtest.NoAgents(), 100)
	a.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, a); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("run A: err %v\n%s", err, human)
	}
	movedByA := map[string]string{}
	for _, b := range []string{"s1", "s2", "s4"} {
		movedByA[b] = testkit.GitOut(t, main, "rev-parse", b)
		if movedByA[b] == p.s.Tips[b] {
			t.Fatalf("run A did not move %s", b)
		}
	}
	fixup(t, p.s.Paths[gittest.StackTmp], "e1.txt", "feat(s5): e1.txt")
	b := synccmdtest.At(synccmdtest.PushAlways(), 200)
	b.Stack = &synccmd.StackOptions{Autosquash: true}
	res, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, b)
	if err != nil && !strings.Contains(err.Error(), "held") {
		t.Fatalf("run B: %v\n%s", err, human)
	}
	for br, tip := range movedByA {
		if got := testkit.GitOut(t, main, "rev-parse", br); got != tip {
			t.Fatalf("run B moved %s to %s, want it left where A put it\n%s", br, got, human)
		}
		if got := refIn(t, p.bare, br); got != tip {
			t.Errorf("origin %s is at %s, want A's tip %s\n%s", br, got, tip, human)
		}
	}
	if got, want := refIn(t, p.bare, gittest.StackTmp), testkit.GitOut(t, main, "rev-parse", gittest.StackTmp); got != want {
		t.Errorf("origin %s is at %s, want B's tip %s\n%s", gittest.StackTmp, got, want, human)
	}
	for _, br := range []string{"s3", "s5"} {
		if got := refIn(t, p.bare, br); got != p.s.Tips[br] {
			t.Errorf("origin %s moved to %s, though no run moved it", br, got)
		}
	}
	line := synccmdtest.Participant(t, res, gittest.StackTmp)
	if !line.Pushed || line.Stack == nil || line.Stack.Push == nil {
		t.Fatalf("the JSON does not hold the push: pushed %v\n%s", line.Pushed, human)
	}
	var sent []string
	for _, pb := range line.Stack.Push.Branches {
		if pb.Outcome == syncrun.PushedOK {
			sent = append(sent, pb.Branch)
		}
	}
	if got, want := strings.Join(sent, ","), "s1,s2,s4,"+gittest.StackTmp; got != want {
		t.Errorf("the JSON says %s were pushed, want %s", got, want)
	}
	if _, ok, _ := wtsync.ReadPushRecord(main, gittest.StackTmp); ok {
		t.Errorf("the record is still there after everything was pushed\n%s", human)
	}
}

// wt#223: --push on the last resume of the same run is the explicit way to
// push it: every layer the run moved is sent and carries no push command any
// more; the line's own branch, new on origin, is left out and still owed.
func TestExplicitPushOnAResumeOfANoPushRunPushesEveryMovedLayer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newTwoStopLine(t)
	main := p.ctx.Repo.MainRoot
	var push syncfix.ResumeOptions
	push.Push = syncrun.PushAlways
	res, human, err := p.noPushRunResumed(t, push)
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("last resume: err %v, want the two held layers named\n%s", err, human)
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if got, want := refIn(t, p.bare, b), testkit.GitOut(t, main, "rev-parse", b); got != want || got == p.s.Tips[b] {
			t.Errorf("origin %s is at %s, want the moved tip %s\n%s", b, got, want, human)
		}
	}
	for _, b := range []string{"s3", "s5"} {
		if got := refIn(t, p.bare, b); got != p.s.Tips[b] {
			t.Errorf("origin %s moved to %s, though the run held it", b, got)
		}
	}
	line := synccmdtest.Participant(t, res, gittest.StackTmp)
	if line.Pushed || line.PushCommand == nil {
		t.Errorf("the line, left out of the push, says pushed %v with push command %v", line.Pushed, line.PushCommand)
	}
	if line.Stack == nil || line.Stack.Push == nil {
		t.Fatalf("no stack push in the JSON\n%s", human)
	}
	for _, l := range line.Stack.Layers {
		if l.Result != nil && *l.Result == wtsync.MoveMoved && l.PushCommand != nil {
			t.Errorf("pushed layer %s still carries the push command %v", l.Branch, l.PushCommand)
		}
	}
	rec, ok, rerr := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if rerr != nil || !ok {
		t.Fatalf("the line's entry was not kept: %v", rerr)
	}
	if got := strings.Join(rec.Branches(), ","); got != gittest.StackTmp {
		t.Errorf("the record holds %s after the push, want only %s", got, gittest.StackTmp)
	}
}
