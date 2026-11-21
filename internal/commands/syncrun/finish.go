package syncrun

import (
	"fmt"
	"io"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// FinishInput is what Finish needs from the caller, a run or a resume. Every
// field is the caller's to set; nothing has a shared default, because what a
// run and a resume put in them differs on purpose (the tip each found, the
// paths each knows were stopped on, whether wt drove the replay).
type FinishInput struct {
	// Work, Path and Branch name the worktree whose rebase finished; Epoch
	// is the run's.
	Work, Path, Branch string
	Epoch              int64
	// Res is the rebase's result as the caller has it.
	Res wtsync.Result
	// Headline is the "rebased N commits" line, Layered says the rebase
	// was of a stack line (the each-commit note is worded by it), and Extra
	// are the lines the caller adds under the note, each printed as given.
	Headline string
	Layered  bool
	Extra    []string
	// Adopt runs after the Extra lines are printed and returns the per-layer
	// steps a rebase finished by hand left unrun, which are owed like the
	// rebase's own. It prints what it has to say itself. Nil when the caller
	// has none.
	Adopt func() []wtsync.LayerStep
	// Complete builds CompleteRun's input. It is asked only once the
	// tracker says rebased.
	Complete func() CompleteInput
	// Layers finishes the stack when the rebase was of one, after the
	// deferred steps ran. restored says an edit run's line was put back;
	// failure is the failure the stack ended with, empty for none.
	Layers func() (restored bool, failure string)
	// Settle is what the caller does once the layers are settled and
	// before the report: loose layers, the tips an adopted finish left
	// unchecked. It returns the checks it owes. head and cerr are what
	// CompleteRun returned.
	Settle func(head string, cerr error, restored bool) (owed []string)
}

// FinishOutcome is what Finish came to.
type FinishOutcome struct {
	Head string
	// Ran is what each deferred step came to.
	Ran []wtsync.DeferredResult
	// Err is the finish's own failure (CompleteRun's): the rebase stands.
	Err error
	// Restored says an edit run's line was put back at the tip the run found.
	Restored bool
	// StackFailure is the failure the stack ended with, empty for none.
	StackFailure string
	// Owed is every step and check still owed, in the words the closing
	// error uses; OwedFailures names each one with the worktree.
	Owed         []string
	OwedFailures []string
}

// Finish is what a run and a resume both do once a rebase has finished, in
// this order: the headline and its notes; the deferred steps and the result
// ref (CompleteRun); what is owed, recorded for wt sync steps; the stack's
// layers; what the caller settles; the report of what the rebase did to its
// commits; and the journal's record of the finish. The push is the caller's
// to offer once every worktree is done.
func Finish(ctx *kit.Context, w io.Writer, cfg *wtsync.Config, t *RebaseTracker, j *RunJournal, in FinishInput) FinishOutcome {
	res := in.Res
	fmt.Fprintln(w, in.Headline)
	if note := wtsync.EachCommitNote(res.EachCommit, in.Layered); note != "" {
		fmt.Fprintf(w, "    %s\n", note)
	}
	for _, l := range in.Extra {
		fmt.Fprintln(w, l)
	}
	if in.Adopt != nil {
		res.LayerSteps = append(res.LayerSteps, in.Adopt()...)
	}
	head, ran, cerr := CompleteRun(ctx, w, cfg, t, in.Work, in.Path, in.Complete)
	if err := RecordOwed(ctx.Repo.MainRoot, in.Branch, in.Epoch, res, ran); err != nil {
		fmt.Fprintf(w, "  note: what is owed could not be recorded for wt sync steps: %v\n", err)
	}
	restored, stackFailure := in.Layers()
	tipOwed := in.Settle(head, cerr, restored)
	// What the rebase did to its commits is told once the layers are
	// settled, and not at all when the line was put back: the result it
	// describes is gone.
	if !restored {
		printReplayReport(w, res)
	}
	PrintUnchecked(w, res.Unchecked)
	owed := append(OwedChecks(res.Unchecked), owedLayerSteps(res.LayerSteps)...)
	owed = append(owed, owedSteps(ran)...)
	owed = append(owed, tipOwed...)
	j.SetSync(in.Branch, func(p *SyncParticipant) {
		p.SafetyRef, p.UndoCommand, p.Deferred = jsonout.Strp(res.Safety.Ref), UndoCommand(in.Work), DeferredSteps(ran)
		p.LayerSteps = LayerStepsOf(res.LayerSteps)
		if !restored {
			setReplayReport(p, res)
		}
	})
	return FinishOutcome{Head: head, Ran: ran, Err: cerr, Restored: restored, StackFailure: stackFailure, Owed: owed, OwedFailures: owedBy(in.Work, owed)}
}
