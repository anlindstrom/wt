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

// A move cut after the worktree's files followed the ref, before the move
// was recorded as made, leaves s1 recorded as moving with its ref, index and
// files all at the new tip. A resume refuses and moves nothing; the undo then puts back the line and s1, files and all.
func TestUndoPutsBackALayerInterruptedAfterItsFilesMoved(t *testing.T) {
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
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	ropts.Between = func(stage string, m wtsync.LayerMove) error {
		if stage == wtsync.StageAfterFiles && m.Branch == "s1" {
			return errors.New("interrupted")
		}
		return nil
	}
	var cut bytes.Buffer
	_ = syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &cut)
	rec, ok, err := wtsync.ReadStackRecord(main, 99)
	if err != nil || !ok || len(rec.Moves) == 0 || rec.Moves[0].Branch != "s1" || rec.Moves[0].Result != wtsync.MoveMoving {
		t.Fatalf("s1's move is not recorded as under way: %+v %v %v\n%s", rec, ok, err, cut.String())
	}
	s1 := rec.Moves[0]
	if got := testkit.GitOut(t, main, "rev-parse", "s1"); got != s1.New {
		t.Fatalf("s1 is at %s, want its new tip %s", got, s1.New)
	}
	if got := testkit.GitOut(t, p.s.Paths["s1"], "status", "--porcelain=v1"); got != "" {
		t.Fatalf("s1's worktree is not clean at its new tip:\n%s", got)
	}

	refs := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	ropts.Between = nil
	var again bytes.Buffer
	if err := syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &again); err == nil {
		t.Fatalf("a second resume went on over the cut move\n%s", again.String())
	}
	if got := testkit.GitOut(t, main, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != refs {
		t.Errorf("the second resume moved branches:\nbefore %s\nafter %s", refs, got)
	}

	uopts := syncfix.UndoOptions{}
	uopts.Agents = []agents.Agent{}
	uopts.Now = func() time.Time { return time.Unix(9, 0) }
	var undone bytes.Buffer
	if err := syncfix.SyncUndo(p.ctx, gittest.StackTmp, uopts, &undone); err != nil {
		t.Fatalf("undo: %v\n%s", err, undone.String())
	}
	if !strings.Contains(undone.String(), "s1") {
		t.Errorf("the undo does not report s1:\n%s", undone.String())
	}
	for _, b := range append([]string{gittest.StackTmp}, p.s.Layers...) {
		if got := testkit.GitOut(t, main, "rev-parse", b); got != p.s.Tips[b] {
			t.Errorf("%s is at %s after the undo, want %s", b, got, p.s.Tips[b])
		}
	}
	if got := testkit.GitOut(t, p.s.Paths["s1"], "status", "--porcelain=v1"); got != "" {
		t.Errorf("s1's worktree is not clean after the undo:\n%s", got)
	}
	if got := testkit.GitOut(t, p.s.Paths["s1"], "diff", "--quiet", p.s.Tips["s1"]); got != "" {
		t.Errorf("s1's files are not its old tip's: %s", got)
	}
	if _, ok, _ := wtsync.ReadStackRecord(main, 99); ok {
		t.Error("the run's stack record is left after the undo")
	}
}
