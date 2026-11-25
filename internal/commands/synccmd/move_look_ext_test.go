package synccmd_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// The look at a fold names the move! commit, what it moves from where, and
// that the run needs --each-commit=targets; --json says the same, as a fold
// of kind move. Every layer from the target up is re-pointed.
func TestMoveLookNamesTheMove(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s, _ := synccmdtest.MoveLine(t, false, true)
	main := ctx.Repo.MainRoot
	look := &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}
	var out bytes.Buffer
	if err := synccmd.SyncWorktree(ctx, gittest.MoveTmp, synccmd.SyncOptions{NoFetch: true, Stack: look}, &out); err != nil {
		t.Fatalf("look: %v\n%s", err, out.String())
	}
	source := testkit.GitOut(t, main, "rev-parse", "--short=7", "l3")
	for _, want := range []string{
		"1 move folds in place",
		"move! " + gittest.MoveTarget + "  → " + testkit.GitOut(t, main, "rev-parse", "--short=7", "l1") + " " + gittest.MoveTarget + "  (layer l1)",
		"moves " + gittest.MovePath + " of " + source + " " + gittest.MoveSource + "  (layer l3)",
		"--each-commit=targets",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the look does not say %q:\n%s", want, out.String())
		}
	}
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: look})
	f := o.Fold
	if f == nil || f.Refusal != nil || len(f.Folds) != 1 {
		t.Fatalf("fold %+v", f)
	}
	p := f.Folds[0]
	if p.Kind != "move" || p.TargetLayer != "l1" || p.Move == nil || p.Move.SourceLayer != "l3" || p.Move.Source != s.Tips["l3"] || !slices.Equal(p.Move.Paths, []string{gittest.MovePath}) {
		t.Errorf("pair %+v move %+v", p, p.Move)
	}
	if strings.Join(f.Repointed, " ") != "l1 l2 l3" || len(f.Unchanged) != 0 {
		t.Errorf("repointed %q unchanged %q", f.Repointed, f.Unchanged)
	}
}

// The look says the run's refusal of a move whose context a commit in
// between carries, in the run's words, and lists no fold.
func TestMoveLookSaysTheRefusal(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, _, _ := synccmdtest.MoveLine(t, true, true)
	between := testkit.GitOut(t, ctx.Repo.MainRoot, "rev-parse", "--short=7", "l2")
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.MoveTmp, Autosquash: true}})
	if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, between) || len(o.Fold.Folds) != 0 {
		t.Fatalf("fold %+v", o.Fold)
	}
	_, human, err := synccmdtest.StackRun(t, ctx, gittest.MoveTmp, targetsFold())
	if err == nil || !strings.Contains(err.Error()+human, *o.Fold.Refusal) {
		t.Errorf("the run refuses with %v, not the look's %q\n%s", err, *o.Fold.Refusal, human)
	}
}
