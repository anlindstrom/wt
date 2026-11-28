package syncfix

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// The summary of a stop, of a resume that stops again and of the resume that
// finishes: each keeps its outcome and, while there is one, the way on.
func TestTheSummaryOfAStopAndOfEachResumeKeepsTheWayOn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	r := newResumeStop(t)
	summary := func(command string, verb func(j *syncrun.RunJournal, human *bytes.Buffer) error) (syncrun.RunResult, *syncrun.SyncParticipant, error) {
		var out, human bytes.Buffer
		j := syncrun.NewSyncRunJournal(&out, command)
		j.Summarize()
		err := verb(j, &human)
		res := synccmdtest.DecodeSyncRun(t, out.Bytes())
		if res.Detail != "summary" {
			t.Fatalf("detail %q\n%s", res.Detail, human.String())
		}
		return res, synccmdtest.Participant(t, res, gittest.StackTmp), err
	}
	stopped := func(what string, res syncrun.RunResult, p *syncrun.SyncParticipant, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "needs you") {
			t.Fatalf("%s: %v", what, err)
		}
		if res.Outcome != jsonout.OutcomePartial || p.Result != syncrun.ResultHandedOver || p.Reason == nil || p.Recovery == nil ||
			p.PlanFile == nil || p.SafetyRef == nil || len(p.UndoCommand) == 0 {
			t.Errorf("%s: outcome %q participant %+v", what, res.Outcome, p)
		}
	}
	res, p, err := summary("sync rebase", func(j *syncrun.RunJournal, human *bytes.Buffer) error {
		opts := synccmdtest.NoAgents()
		opts.Stack, opts.Journal = &synccmd.StackOptions{Autosquash: true}, j
		return synccmd.SyncRebase(r.ctx, []string{gittest.StackTmp}, opts, human)
	})
	stopped("the stop", res, p, err)
	if p.Stack == nil || p.Stack.Counts.Layers != len(r.s.Layers) || len(p.Stack.Counts.LayerResults) != 0 {
		t.Errorf("the stop's stack: %+v", p.Stack)
	}

	final := map[string]string{"a1.txt": "s1 a1.txt FIXED\nmid1\n", "b1.txt": "s2 b1.txt FIXED\nmid2\n"}
	resolve := func() {
		gitDir, err := wtsync.GitDir(r.tmp)
		if err != nil {
			t.Fatal(err)
		}
		st, ok, err := wtsync.ReadState(gitDir)
		if err != nil || !ok {
			t.Fatalf("state: ok %v err %v", ok, err)
		}
		for _, f := range st.Left {
			testkit.WriteFile(t, r.tmp, f, final[f])
			testkit.GitIn(t, r.tmp, "add", f)
		}
	}
	again := 0
	for n := 0; ; n++ {
		if n > 8 {
			t.Fatalf("still stopping after %d resumes", n)
		}
		resolve()
		res, p, err = summary("sync resume", func(j *syncrun.RunJournal, human *bytes.Buffer) error {
			opts := noResumeAgents()
			opts.Journal = j
			return SyncResume(r.ctx, gittest.StackTmp, opts, human)
		})
		if err == nil || !strings.Contains(err.Error(), "needs you") {
			break
		}
		stopped("a resume that stops again", res, p, err)
		again++
	}
	if again == 0 {
		t.Fatalf("no resume stopped again, so none was looked at: %v", err)
	}
	// The fixture's dirty and busy worktrees hold their layers: the finish
	// fails for them, and the summary holds their rows.
	if err == nil || !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("the finish: %v", err)
	}
	if p.Result != syncrun.ResultRebased || p.Stack == nil || p.Stack.Counts.LayerResults["moved"] == 0 || len(p.UndoCommand) == 0 {
		t.Fatalf("the finish: outcome %q participant %+v stack %+v", res.Outcome, p, p.Stack)
	}
	held := 0
	for _, l := range p.Stack.Layers {
		if l.Held != nil && l.MoveCommand != nil {
			held++
		}
	}
	if held != 2 || p.Stack.Counts.LayerResults["held"] != 2 || len(p.Stack.Layers) >= p.Stack.Counts.Layers {
		t.Errorf("the finish holds %d held rows of %d, counts %+v", held, len(p.Stack.Layers), p.Stack.Counts)
	}
}
