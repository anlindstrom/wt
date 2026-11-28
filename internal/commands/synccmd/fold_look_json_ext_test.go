package synccmd_test

import (
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/gittest"
)

// --json says the same: fold lists the fixups with their targets and the
// layers that change; with --onto it is null.
func TestFoldLookJSONNamesTheFoldInPlace(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	look := &synccmd.StackLook{Work: gittest.StackTmp, Autosquash: true}
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: look})
	f := o.Fold
	if f == nil || len(f.Folds) != 1 {
		t.Fatalf("fold %+v", f)
	}
	p := f.Folds[0]
	if p.FixupSubject != "fixup! feat(s2): b1.txt" || p.TargetSubject != "feat(s2): b1.txt" || p.TargetLayer != "s2" || p.FixupLayer != gittest.StackTmp {
		t.Errorf("pair %+v", p)
	}
	if strings.Join(f.Unchanged, " ") != "s1" || strings.Join(f.Repointed, " ") != "s2 s4" || len(f.Held) != 2 || f.Held[0].Branch != "s3" || f.Held[1].Branch != "s5" || f.Refusal != nil || f.Base != s.Base {
		t.Errorf("fold %+v", f)
	}
	o = synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Onto: "main", Work: gittest.StackTmp, Stack: look})
	if o.Fold != nil {
		t.Errorf("fold with --onto: %+v", o.Fold)
	}
	o = synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp}})
	if o.Fold != nil {
		t.Errorf("fold without --autosquash: %+v", o.Fold)
	}
}

// An unresolved fixup is the fold's refusal in --json, with no fold listed.
func TestFoldLookJSONCarriesTheRefusal(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, tmp := lowLine(t)
	fixup(t, tmp, "b.txt", "feat: nowhere")
	o := synccmdtest.OverviewJSONWith(t, ctx, synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: "tmp/low", Autosquash: true}})
	if o.Fold == nil || o.Fold.Refusal == nil || !strings.Contains(*o.Fold.Refusal, "names no commit earlier on the line") || len(o.Fold.Folds) != 0 {
		t.Errorf("fold %+v", o.Fold)
	}
	wantSummary(t, o.Fold, "none 0 0 0 0 0")
}
