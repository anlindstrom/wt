package syncfix

import (
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/gittest"
)

// A resume that is refused, here because the conflict it waits on is not
// resolved, still says what the line is folded on: fold carries the base
// from the stack's record, with no guard yet, as the run that handed over did.
func TestResumeThatIsRefusedStillSaysWhatTheFoldIsOn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and worktrees")
	}
	ctx, s := synccmdtest.StackLine(t, "conflicts: []\n")
	foldFixups(t, s)
	res, human, err := synccmdtest.StackRun(t, ctx, gittest.StackTmp, foldOntoTrunk())
	if err == nil {
		t.Fatalf("the run was not handed over\n%s", human)
	}
	handed := synccmdtest.Participant(t, res, gittest.StackTmp).Fold
	if handed == nil || handed.InPlace || handed.Base != s.Base || handed.Guard != nil {
		t.Fatalf("handed over: fold %+v, want a moving fold on %s with no guard", handed, s.Base)
	}
	opts := noResumeAgents()
	resumed, err := syncResumeJSON(t, ctx, gittest.StackTmp, opts)
	if err == nil {
		t.Fatal("a resume with the conflict unresolved went through")
	}
	got := synccmdtest.Participant(t, resumed, gittest.StackTmp).Fold
	if got == nil || got.InPlace || got.Base != s.Base || got.Guard != nil {
		t.Errorf("refused resume: fold %+v, want a moving fold on %s with no guard", got, s.Base)
	}
}
