package wtsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A fixup the fold applies in place keeps the gap its line shows: a run onto
// another base may stop there, and resume checks the gaps.
func TestContextGapsKeepAGapTheFoldInPlaceDoesNotStopAt(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "f.txt", "0\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"feat: c", "f.txt", "1\n"},
		{"feat: back", "f.txt", "0\n"},
		{"fixup! feat: a", "f.txt", "1\n"},
		{"fixup! feat: b", "f.txt", "2\n"},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := sha["fixup! feat: b"]
	if len(a.Gaps) != 1 || a.Gaps[0].Fixup != last || a.Gaps[0].Needs != sha["feat: c"] {
		t.Errorf("gaps %+v, want the fixup of feat: b needing feat: c", a.Gaps)
	}
	if got := outcomeOf(t, a, last); got.State != FixupApplies {
		t.Errorf("outcome %+v, want it applying in place", got)
	}
}

// A gap whose commit the fold's order does not confirm offers no retarget.
func TestRetargetIsNotOfferedForADoubtfulGap(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "x.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"feat: c", "c.txt", "c\n"},
		{"fixup! feat: a", "x.txt", "b\n"},
		{"feat: d", "x.txt", "a\n"},
		{"fixup! feat: b", "x.txt", "z\n"},
	})
	gaps, unchecked, err := ContextGaps(dir, commits, nil)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("gaps %+v, err %v", gaps, err)
	}
	// needs keeps its meaning, the first on the line; the fold's own is said.
	g := gaps[0]
	if g.Needs != sha["feat: c"] || !strings.Contains(g.Doubt, `"feat: d"`) {
		t.Errorf("gap needs %s, doubt %q; want feat: c, with the fold having it at feat: d", g.NeedsSubject, g.Doubt)
	}
	if len(unchecked) != 1 || !strings.Contains(unchecked[0], g.Doubt) {
		t.Errorf("unchecked %q, want the doubt noted", unchecked)
	}
	if got := g.Retarget("tmp/line"); strings.Contains(got, "--fixup") || !strings.Contains(got, "wt sync undo tmp/line") {
		t.Errorf("retarget %q, want no fixup to commit, and the way back", got)
	}
	plan, err := RenderPlan(PlanInput{MainRoot: dir, Work: "tmp/line", Base: commits[0], Trunk: "HEAD",
		Handover: Handover{Index: 1, Total: 1, Commit: g.Fixup, Context: gaps}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan, "--fixup") || strings.Contains(plan, "retarget,") || !strings.Contains(plan, g.Doubt) {
		t.Errorf("the plan offers a retarget, or lacks the doubt:\n%s", plan)
	}
}

// A layer step commits at a layer's end, before the folds above it: a fixup
// of a file it owns is not said to apply.
func TestAnalyseFixupsWithholdWhatALayerStepMayChange(t *testing.T) {
	t.Parallel()
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "gen.txt", "0\n"},
		{"feat: b", "b.txt", "b\n"},
		{"fixup! feat: a", "gen.txt", "1\n"},
		{"fixup! feat: b", "gen.txt", "2\n"},
	})
	cfg, err := Parse([]byte("defer:\n  - run: ./gen.sh\n    paths: [gen.txt]\n    commit: \"chore: gen\"\n    each: layer\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		cfg  *Config
		want FixupState
	}{{"no step", nil, FixupApplies}, {"a layer step", cfg, FixupNotAnalysed}} {
		a, err := AnalyseFixups(dir, commits, c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := states(a); len(got) != 2 || got[1] != c.want {
			t.Errorf("%s: states %q, want the second %q", c.name, got, c.want)
		}
	}
}

// The paths of the fixups below a target are read once, however many hunks
// ask about them.
func TestContextGapsReadEachFixupsPathsOnce(t *testing.T) {
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"feat: g", "g.txt", "g\n"},
		{"fixup! feat: a", "g.txt", "g\ng2\n"},
		{"feat: c", "x.txt", "x\n", "y.txt", "y\n", "z.txt", "z\n"},
		{"fixup! feat: b", "x.txt", "x\nx2\n", "y.txt", "y\ny2\n", "z.txt", "z\nz2\n"},
	})
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	if gaps, _, err := ContextGaps(dir, commits, nil); err != nil || len(gaps) != 4 {
		t.Fatalf("gaps %+v, err %v", gaps, err)
	}
	ran, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(ran), "built-in: git diff-tree --no-commit-id --name-only"); got != 1 {
		t.Errorf("the paths of the one fixup below were read %d times", got)
	}
}

// A later commit that removes a file supplies what a fixup adding it needs:
// the conflict is not called unexplained.
func TestAnalyseFixupsDoNotCallAnAdditionALaterRemovalExplainsUnexplained(t *testing.T) {
	t.Parallel()
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"fixup! feat: a", "n.txt", "one\n"},
	})
	gittest.Git(t, dir, "rm", "-q", "n.txt")
	gittest.Git(t, dir, "commit", "-q", "-m", "feat: drop")
	commits = append(commits, gitIn(t, dir, "rev-parse", "HEAD"))
	second := commitLine(t, dir, "fixup! feat: a", "n.txt", "two\n")
	commits = append(commits, second)
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeOf(t, a, second); got.State != FixupNotAnalysed || !strings.Contains(got.Why, `"feat: drop"`) {
		t.Errorf("outcome %+v, want not analysed, naming feat: drop", got)
	}
	if stop := realFold(t, dir, commits); stop != second {
		t.Errorf("the fold stopped at %q, want the second fixup", stop)
	}
}
