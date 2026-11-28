package synccmd_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/jsonout"
)

// summaryOf runs verb with a summarizing journal for command and returns its
// checked object. The line's dirty and busy worktrees hold their layers, which
// fails a run that moved the rest.
func summaryOf(t *testing.T, command string, verb func(j *syncrun.RunJournal, human *bytes.Buffer) error) (syncrun.RunResult, string) {
	t.Helper()
	var out, human bytes.Buffer
	j := syncrun.NewSyncRunJournal(&out, command)
	j.Summarize()
	if err := verb(j, &human); err != nil && !strings.Contains(err.Error(), "2 layers held") {
		t.Fatalf("%s: %v\n%s", command, err, human.String())
	}
	r := synccmdtest.DecodeSyncRun(t, out.Bytes())
	if r.Detail != "summary" || r.Command != command || r.Outcome != jsonout.OutcomeDone {
		t.Fatalf("%s: detail %q command %q outcome %q\n%s", command, r.Detail, r.Command, r.Outcome, human.String())
	}
	return r, human.String()
}

// rebase, push and undo in their summary form on a real line. The rebase
// leaves its push for later and names the command; running it pushes what it
// promised, after which nothing is left to name.
func TestTheSummaryOfARunNamesThePushItLeftForLater(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	p := newPushedLine(t)
	run, human := summaryOf(t, "sync rebase", func(j *syncrun.RunJournal, human *bytes.Buffer) error {
		opts := synccmdtest.NoAgents()
		opts.Push, opts.Stack, opts.Journal = syncrun.PushNever, &synccmd.StackOptions{}, j
		return synccmd.SyncRebase(p.ctx, []string{gittest.StackTmp}, opts, human)
	})
	s := synccmdtest.Participant(t, run, gittest.StackTmp).Stack
	if want := []string{"wt", "sync", "push", gittest.StackTmp, "--stack"}; !slices.Equal(s.PushLaterCommand, want) {
		t.Fatalf("pushLaterCommand %v, want %v\n%s", s.PushLaterCommand, want, human)
	}
	moved := s.Counts.LayerResults["moved"]
	if s.Counts.Layers != len(p.s.Layers) || moved == 0 || len(s.Layers) >= s.Counts.Layers {
		t.Errorf("counts %+v, %d rows", s.Counts, len(s.Layers))
	}

	push, human := summaryOf(t, "sync push", func(j *syncrun.RunJournal, human *bytes.Buffer) error {
		opts := syncfix.PushLaterOptions{Journal: j}
		opts.Push = syncrun.PushAlways
		return syncfix.SyncPushLater(p.ctx, s.PushLaterCommand[3], opts, human)
	})
	ps := synccmdtest.Participant(t, push, gittest.StackTmp).Stack
	if ps.Counts.Pushed < moved || ps.Push == nil || len(ps.Push.Branches) != 0 || ps.PushLaterCommand != nil {
		t.Errorf("push: counts %+v, push %+v, later %v\n%s", ps.Counts, ps.Push, ps.PushLaterCommand, human)
	}
	for _, b := range p.s.Layers {
		if remote, local := refIn(t, p.bare, b), p.localTip(t, b); remote != local {
			t.Errorf("origin %s is at %s, local at %s", b, remote, local)
		}
	}

	undo, human := summaryOf(t, "sync undo", func(j *syncrun.RunJournal, human *bytes.Buffer) error {
		return syncfix.SyncUndo(p.ctx, gittest.StackTmp, syncfix.UndoOptions{Journal: j, VerbOptions: synccmdtest.NoAgents().VerbOptions}, human)
	})
	if len(undo.Worktrees) < 2 {
		t.Errorf("undo lists %d participants, want every branch it put back\n%s", len(undo.Worktrees), human)
	}
}

func TestTheSummaryOfARunThatPushedNamesNoLaterPush(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	p := newPushedLine(t)
	run, human := summaryOf(t, "sync rebase", func(j *syncrun.RunJournal, human *bytes.Buffer) error {
		opts := synccmdtest.NoAgents()
		opts.Push, opts.Stack, opts.Journal = syncrun.PushAlways, &synccmd.StackOptions{}, j
		opts.PushLine = true
		return synccmd.SyncRebase(p.ctx, []string{gittest.StackTmp}, opts, human)
	})
	if s := synccmdtest.Participant(t, run, gittest.StackTmp).Stack; s.PushLaterCommand != nil || s.Counts.Pushed == 0 {
		t.Errorf("later %v counts %+v\n%s", s.PushLaterCommand, s.Counts, human)
	}
}

func TestTheSummaryOfStepsCountsWhatEachLayerCameTo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, _ := synccmdtest.StackLine(t, synccmdtest.StackYAML+"defer:\n  - run: 'true'\n    paths: ['**/*.txt']\n    each: layer\n")
	run, human := summaryOf(t, "sync steps", func(j *syncrun.RunJournal, human *bytes.Buffer) error {
		opts := synccmdtest.NoAgents()
		opts.Push, opts.Stack, opts.Journal = syncrun.PushNever, &synccmd.StackOptions{}, j
		opts.Steps = &synccmd.StepsOptions{All: true}
		return synccmd.SyncRebase(ctx, []string{gittest.StackTmp}, opts, human)
	})
	p := synccmdtest.Participant(t, run, gittest.StackTmp)
	total := 0
	for _, n := range p.Stack.Counts.LayerStepResults {
		total += n
	}
	// The top is at its last layer's tip, so the step stops once a layer.
	if total != p.Stack.Counts.Layers || len(p.LayerSteps) != 0 {
		t.Errorf("layer step counts %v, %d rows\n%s", p.Stack.Counts.LayerStepResults, len(p.LayerSteps), human)
	}
}
