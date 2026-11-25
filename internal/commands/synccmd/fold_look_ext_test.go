package synccmd_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// Trunk-side words the look of a rebase onto trunk prints, which an in-place
// fold never meets (wt#212).
var trunkRebaseWords = []string{"contested", "behind", "rebases up to", "stops the replay", "conflict-free"}

// The look at a fold with no --onto describes the fold the run makes: which
// fixup folds into which commit and layer, which layers change, and nothing
// about a rebase onto trunk. The run then does what the look said.
func TestFoldLookWithoutOntoShowsTheInPlaceFold(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	tmp := s.Paths[gittest.StackTmp]
	fixup(t, tmp, "b1.txt", "feat(s2): b1.txt")
	fixup(t, tmp, "d1.txt", "feat(s4): d1.txt")
	main := ctx.Repo.MainRoot

	var look bytes.Buffer
	opts := synccmd.SyncOptions{NoFetch: true, Stack: &synccmd.StackLook{Work: gittest.StackTmp, Autosquash: true}}
	if err := synccmd.SyncWorktree(ctx, gittest.StackTmp, opts, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	got := look.String()
	for _, word := range trunkRebaseWords {
		if strings.Contains(got, word) {
			t.Errorf("the look of an in-place fold says %q:\n%s", word, got)
		}
	}
	for _, want := range []string{
		"2 fixups fold in place",
		"fixup! feat(s2): b1.txt  →",
		"feat(s2): b1.txt  (layer s2)",
		"fixup! feat(s4): d1.txt  →",
		"feat(s4): d1.txt  (layer s4)",
		"unchanged: s1",
		"re-pointed: s2, s3, s4, s5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the look lacks %q:\n%s", want, got)
		}
	}

	before := map[string]string{}
	for _, b := range append([]string{gittest.StackTmp}, s.Layers...) {
		before[b] = testkit.GitOut(t, main, "rev-parse", b)
	}
	var run bytes.Buffer
	runOpts := synccmdtest.NoAgents()
	runOpts.Stack = &synccmd.StackOptions{Autosquash: true}
	if err := synccmd.SyncRebase(ctx, []string{gittest.StackTmp}, runOpts, &run); err == nil || !strings.Contains(err.Error(), "layers held") {
		t.Fatalf("err %v\n%s", err, run.String())
	}
	if got := testkit.GitOut(t, main, "rev-parse", "s1"); got != before["s1"] {
		t.Errorf("s1 moved, the look said unchanged")
	}
	for _, b := range []string{"s2", "s4", gittest.StackTmp} {
		if testkit.GitOut(t, main, "rev-parse", b) == before[b] {
			t.Errorf("%s did not move, the look said re-pointed", b)
		}
	}
	if out := testkit.GitOut(t, main, "log", "--format=%s", s.Base+".."+gittest.StackTmp, "--grep=^fixup!"); out != "" {
		t.Errorf("a fixup was left: %s", out)
	}
	if !testkit.GitAncestor(t, main, s.Base, gittest.StackTmp) || testkit.GitAncestor(t, main, "main", gittest.StackTmp) {
		t.Error("the fold did not stay on the line's own base")
	}
}

// With --onto the fold also moves the line, so the look stays the look of
// that rebase: the assessment against the target, not the in-place fold.
func TestFoldLookWithOntoStaysTheRebaseLook(t *testing.T) {
	t.Parallel()
	skipShort(t)
	ctx, s := synccmdtest.StackLine(t, "")
	fixup(t, s.Paths[gittest.StackTmp], "b1.txt", "feat(s2): b1.txt")

	var look bytes.Buffer
	opts := synccmd.SyncOptions{NoFetch: true, Onto: "main", Stack: &synccmd.StackLook{Work: gittest.StackTmp, Autosquash: true}}
	if err := synccmd.SyncWorktree(ctx, gittest.StackTmp, opts, &look); err != nil {
		t.Fatalf("%v\n%s", err, look.String())
	}
	got := look.String()
	for _, want := range []string{"against main", "behind", "run     wt sync rebase"} {
		if !strings.Contains(got, want) {
			t.Errorf("the look onto a target lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fold in place") {
		t.Errorf("the look onto a target describes an in-place fold:\n%s", got)
	}
}
