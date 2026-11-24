package synccmd_test

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/gittest"
)

// layerSkipLine is the FoldLine with a recording check at the commits and a
// recording per-layer step at every layer end, neither of which changes a
// file: what they ran at is what a person sees. The line's fixup! targets
// f2's first commit, so f1, wholly below it, comes out as it went in.
func layerSkipLine(t *testing.T) (ctx *kit.Context, s *gittest.FoldLine, checks, steps string) {
	t.Helper()
	dir := t.TempDir()
	checks, steps = filepath.Join(dir, "checks.log"), filepath.Join(dir, "steps.log")
	yaml := "each_commit:\n  - run: git log -1 --format=%s >> '" + checks + "'\n" +
		"defer:\n  - run: git log -1 --format=%s >> '" + steps + "'\n    each: layer\n"
	ctx, s = synccmdtest.FoldLine(t, yaml)
	return ctx, s, checks, steps
}

func ranAt(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "_"))
}

// #207: an in-place fold whose only fixup targets layer 2 leaves layer 1
// as it was. Its check and its step do not run in any mode, one line says
// so, the JSON names the step untouched, every layer ends where a run that
// checked it would have put it, and undo puts every ref back.
func TestInPlaceFoldSkipsTheChecksAndStepsOfAnUnchangedLayer(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const f2more, f3bump, f4docs, f5bump = "feat(f2):_more", "chore(f3):_bump_to_1.1.1", "feat(f4):_docs", "chore(f5):_bump_to_1.1.2"
	for _, tc := range []struct {
		mode   string
		checks []string
	}{
		{"tips", []string{f2more, f3bump, f4docs, f5bump}},
		{"targets", []string{"feat(f2):_notes", f2more, f3bump, f4docs, f5bump}},
		{"all", []string{"feat(f2):_notes", f2more, "feat(f3):_c", f3bump, f4docs, f5bump}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			ctx, s, checks, steps := layerSkipLine(t)
			main := ctx.Repo.MainRoot
			branches := append(append([]string{}, s.Layers...), gittest.FoldTmp)
			before := refsOf(t, main, branches)
			opts := synccmdtest.NoAgents()
			opts.EachCommit = tc.mode
			opts.Stack = &synccmd.StackOptions{Autosquash: true}
			res, human, _ := synccmdtest.StackRun(t, ctx, gittest.FoldTmp, opts)
			if got := ranAt(t, checks); !slices.Equal(got, tc.checks) {
				t.Errorf("checked %q, want %q\n%s", got, tc.checks, human)
			}
			if got, want := ranAt(t, steps), []string{f2more, f3bump, f4docs, f5bump, f5bump}; !slices.Equal(got, want) {
				t.Errorf("step ran at %q, want %q\n%s", got, want, human)
			}
			if want := "layer 1 unchanged: its checks and steps skipped\n"; strings.Count(human, want) != 1 {
				t.Errorf("want %q once in\n%s", want, human)
			}
			var untouched []string
			for _, ls := range synccmdtest.Participant(t, res, gittest.FoldTmp).LayerSteps {
				if ls.Result == "untouched" {
					untouched = append(untouched, ls.Branch)
					if ls.Ran || ls.Layer != 1 {
						t.Errorf("untouched step %+v", ls)
					}
				}
			}
			if !slices.Equal(untouched, []string{"f1"}) {
				t.Errorf("untouched at %q, want [f1]", untouched)
			}
			after := refsOf(t, main, branches)
			if after["f1"] != before["f1"] || after["f2"] == before["f2"] || after[gittest.FoldTmp] == before[gittest.FoldTmp] {
				t.Errorf("refs before %v after %v", before, after)
			}
			var undone bytes.Buffer
			if err := syncfix.SyncUndo(ctx, gittest.FoldTmp, syncfix.UndoOptions{VerbOptions: opts.VerbOptions}, &undone); err != nil {
				t.Fatalf("undo: %v\n%s", err, undone.String())
			}
			if got := refsOf(t, main, branches); !maps.Equal(got, before) {
				t.Errorf("after undo %v, want %v\n%s", got, before, undone.String())
			}
		})
	}
}
