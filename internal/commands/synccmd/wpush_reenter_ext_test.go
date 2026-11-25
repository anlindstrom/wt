package synccmd_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// wt#210: a resume whose finish failed before it removed the handover and
// whose layer moves were then cut short (s1 and s2 moved) is resumed again.
// The layers already moved stay moved and are reported so, and the push
// record holds every layer of the run.
func TestResumeAfterAFailedFinishAndCutMovesKeepsTheMovedLayers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want a handover at the conflict\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	// The finish cannot write the run's result ref: a ref beneath its name
	// is in the way, so the handover stays after the finish.
	blocker := wtsync.ResultPrefix + gittest.StackTmp + "/99/x"
	testkit.GitIn(t, main, "update-ref", blocker, p.s.Tips[gittest.StackTmp])
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	ropts.AfterMove = func(m wtsync.LayerMove) error {
		if m.Branch == "s2" {
			return errors.New("interrupted")
		}
		return nil
	}
	var cut bytes.Buffer
	if err := syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &cut); err == nil {
		t.Fatalf("the cut resume reported success\n%s", cut.String())
	}
	moved := map[string]string{}
	for _, b := range []string{"s1", "s2"} {
		moved[b] = testkit.GitOut(t, main, "rev-parse", b)
		if moved[b] == p.s.Tips[b] {
			t.Fatalf("%s did not move before the cut\n%s", b, cut.String())
		}
	}
	testkit.GitIn(t, main, "update-ref", "-d", blocker)

	ropts.AfterMove = nil
	var again bytes.Buffer
	if err := syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &again); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("second resume: err %v, want only the two session-held layers\n%s", err, again.String())
	}
	for _, b := range []string{"s1", "s2"} {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != moved[b] {
			t.Errorf("%s is at %s after the second resume, want %s", b, got, moved[b])
		}
		if !strings.Contains(again.String(), "✓ "+b+" ") || strings.Contains(again.String(), "⏸ "+b+" ") {
			t.Errorf("the second resume does not report %s as moved:\n%s", b, again.String())
		}
	}
	rec, ok, rerr := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if rerr != nil || !ok {
		t.Fatalf("no push record: %v\n%s", rerr, again.String())
	}
	if want, got := "s1,s2,s4,"+gittest.StackTmp, strings.Join(rec.Branches(), ","); got != want {
		t.Errorf("the record holds %s, want %s", got, want)
	}
	// The later push sends every layer the run moved, before the cut and
	// after it, and leaves the held ones where they were.
	var pushed bytes.Buffer
	popts := syncfix.PushLaterOptions{}
	popts.Push = syncrun.PushAlways
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, popts, &pushed); err != nil {
		t.Fatalf("push: %v\n%s", err, pushed.String())
	}
	for _, b := range []string{"s1", "s2", "s4", gittest.StackTmp} {
		if got, want := refIn(t, p.bare, b), testkit.GitOut(t, main, "rev-parse", b); got != want {
			t.Errorf("origin %s is at %s, want the run's tip %s\n%s", b, got, want, pushed.String())
		}
	}
	for _, b := range []string{"s3", "s5"} {
		if got := refIn(t, p.bare, b); got != p.s.Tips[b] {
			t.Errorf("held %s was pushed: origin at %s, want %s", b, got, p.s.Tips[b])
		}
	}
}

// The same run is still undone completely: every layer it moved, the two
// moved before the cut included, goes back to where it was.
func TestUndoAfterAFailedFinishAndCutMovesPutsBackEveryMovedLayer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	blocker := wtsync.ResultPrefix + gittest.StackTmp + "/99/x"
	testkit.GitIn(t, main, "update-ref", blocker, p.s.Tips[gittest.StackTmp])
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	ropts.AfterMove = func(m wtsync.LayerMove) error {
		if m.Branch == "s2" {
			return errors.New("interrupted")
		}
		return nil
	}
	var out bytes.Buffer
	_ = syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &out)
	testkit.GitIn(t, main, "update-ref", "-d", blocker)
	ropts.AfterMove = nil
	_ = syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &out)

	uopts := syncfix.UndoOptions{}
	uopts.Agents = []agents.Agent{}
	uopts.Now = func() time.Time { return time.Unix(9, 0) }
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, uopts, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, p.s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != p.s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s\n%s", b, got, p.s.Tips[b], undone.String())
		}
	}
}

// A move cut short between the branch's ref and its worktree's files leaves
// the layer's recovery metadata in the record. A resume that would run the
// moves again refuses, names the layer and the undo, and changes nothing;
// the undo then puts the layer back with its files.
func TestResumeRefusesToMoveAgainWhileAMoveIsIncomplete(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	blocker := wtsync.ResultPrefix + gittest.StackTmp + "/99/x"
	testkit.GitIn(t, main, "update-ref", blocker, p.s.Tips[gittest.StackTmp])
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	ropts.Between = func(stage string, m wtsync.LayerMove) error {
		if stage == wtsync.StageAfterRef && m.Branch == "s1" {
			return errors.New("interrupted")
		}
		return nil
	}
	var cut bytes.Buffer
	_ = syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &cut)
	rec, ok, err := wtsync.ReadStackRecord(main, 99)
	if err != nil || !ok {
		t.Fatalf("no stack record after the cut: %v\n%s", err, cut.String())
	}
	if len(rec.Moves) == 0 || rec.Moves[0].Branch != "s1" || rec.Moves[0].Result != wtsync.MoveMoving {
		t.Fatalf("s1's move is not recorded as under way: %+v", rec.Moves)
	}
	testkit.GitIn(t, main, "update-ref", "-d", blocker)
	refs := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")

	ropts.Between = nil
	var again bytes.Buffer
	err = syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &again)
	if err == nil || !strings.Contains(again.String()+err.Error(), "s1") || !strings.Contains(again.String(), "wt sync undo tmp/stack") {
		t.Fatalf("second resume: err %v, want a refusal naming s1 and the undo\n%s", err, again.String())
	}
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != refs {
		t.Errorf("the second resume moved branches:\nbefore %s\nafter %s", refs, got)
	}
	after, ok, _ := wtsync.ReadStackRecord(main, 99)
	if !ok || len(after.Moves) == 0 || after.Moves[0].Result != wtsync.MoveMoving {
		t.Fatalf("the recovery metadata was dropped: %+v", after)
	}

	uopts := syncfix.UndoOptions{}
	uopts.Agents = []agents.Agent{}
	uopts.Now = func() time.Time { return time.Unix(9, 0) }
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, uopts, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, p.s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != p.s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s", b, got, p.s.Tips[b])
		}
	}
	if got := testkit.GitOut(t, p.s.Paths["s1"], "status", "--porcelain=v1"); got != "" {
		t.Errorf("s1's worktree is not clean after the undo:\n%s", got)
	}
}
