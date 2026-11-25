package synccmd_test

import (
	"bytes"
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

// wt#210: a --stack run handed over at a conflict and resumed with --no-push
// leaves a push record that holds every layer the resume moved, also when
// layers were held, and the later push sends all of them and nothing else.
func TestResumeWithHeldLayersKeepsEveryMovedLayerInThePushRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories, a worktree each and a bare remote")
	}
	p := newPushedLineWith(t, "conflicts: []\n")
	main := p.ctx.Repo.MainRoot
	remoteBefore := bareRefs(t, p.bare)
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	if _, human, err := synccmdtest.StackRun(t, p.ctx, gittest.StackTmp, opts); err == nil || !strings.Contains(err.Error(), "needs you") {
		t.Fatalf("err %v, want a handover at the conflict\n%s", err, human)
	}
	tmp := p.s.Paths[gittest.StackTmp]
	testkit.WriteFile(t, tmp, "v.txt", "1.0.6\n")
	testkit.GitIn(t, tmp, "add", "v.txt")
	ropts := syncfix.ResumeOptions{VerbOptions: syncrun.VerbOptions{
		Agents: []agents.Agent{}, Now: func() time.Time { return time.Unix(5, 0) },
	}, StepOptions: syncrun.StepOptions{Probes: synccmdtest.DockerUp()}}
	ropts.Push = syncrun.PushNever
	var resumed bytes.Buffer
	if err := syncfix.SyncResume(p.ctx, gittest.StackTmp, ropts, &resumed); err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("resume: err %v, want the two layers a worktree session holds named as held\n%s", err, resumed.String())
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if testkit.GitOut(t, main, "rev-parse", b) == p.s.Tips[b] {
			t.Fatalf("%s did not move\n%s", b, resumed.String())
		}
	}

	rec, ok, rerr := wtsync.ReadPushRecord(main, gittest.StackTmp)
	if rerr != nil || !ok {
		t.Fatalf("no push record after the resume: %v\n%s", rerr, resumed.String())
	}
	if want, got := "s1,s2,s4,"+gittest.StackTmp, strings.Join(rec.Branches(), ","); got != want {
		t.Errorf("the record holds %s, want %s", got, want)
	}
	if !strings.Contains(resumed.String(), "↪ 3 branches left unpushed, with their leases: wt sync push tmp/stack --stack pushes them later") {
		t.Errorf("the resume does not say what is left for a later push:\n%s", resumed.String())
	}

	var pushed bytes.Buffer
	popts := syncfix.PushLaterOptions{}
	popts.Push = syncrun.PushAlways
	if err := syncfix.SyncPushLater(p.ctx, gittest.StackTmp, popts, &pushed); err != nil {
		t.Fatalf("push: %v\n%s", err, pushed.String())
	}
	for _, b := range []string{"s1", "s2", "s4"} {
		if got, want := refIn(t, p.bare, b), testkit.GitOut(t, main, "rev-parse", b); got != want || got == p.s.Tips[b] {
			t.Errorf("origin %s is at %s, want the moved tip %s\n%s", b, got, want, pushed.String())
		}
	}
	for _, b := range []string{"s3", "s5"} {
		if got, want := refIn(t, p.bare, b), p.s.Tips[b]; got != want {
			t.Errorf("origin %s was moved to %s although the run held it (was %s)", b, got, want)
		}
	}
	if bareRefs(t, p.bare) == remoteBefore {
		t.Error("the push moved nothing")
	}
}
