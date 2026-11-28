package wtsync

import (
	"slices"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A note about one file of a fixup does not hide the gap of another file the
// fold stops in: the run's resume check reads the gaps.
func TestContextGapsKeepTheStopsGapBesideANoteOnAnotherFile(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "x.txt", "0\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"fixup! feat: a", "u.txt", "u0\n", "x.txt", "1\n"},
		{"feat: d", "x.txt", "0\n"},
		{"fixup! feat: b", "u.txt", "u1\n", "x.txt", "2\n"},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := sha["fixup! feat: b"]
	if !slices.ContainsFunc(a.Gaps, func(g ContextGap) bool { return g.Fixup == last && g.Path == "x.txt" && g.Needs == sha["feat: d"] }) {
		t.Errorf("gaps %+v, unchecked %q; want x.txt of the fixup of feat: b needing feat: d", a.Gaps, a.Unchecked)
	}
	if got := outcomeOf(t, a, last); got.State != FixupNotAnalysed {
		t.Errorf("outcome %+v, want not analysed", got)
	}
	if stop := realFold(t, dir, commits); stop != last {
		t.Errorf("the fold stopped at %q, want the fixup of feat: b", stop)
	}
}

// A layer step can change what the target, or a commit before it, is applied
// on: no fixup behind such a commit is said to apply.
func TestAnalyseFixupsWithholdBehindACommitALayerStepMayChange(t *testing.T) {
	t.Parallel()
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "gen.txt", "0\n"},
		{"feat: a", "a.txt", "a\n"},
		{"fixup! feat: a", "doc.txt", "a\n"},
		{"feat: b", "gen.txt", "1\n"},
		{"fixup! feat: b", "notes.txt", "b\n"},
	})
	cfg, err := Parse([]byte("defer:\n  - run: ./gen.sh\n    paths: [gen.txt]\n    commit: \"chore: gen\"\n    each: layer\n"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := AnalyseFixups(dir, commits, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := states(a); len(got) != 2 || got[0] != FixupApplies || got[1] != FixupNotAnalysed {
		t.Errorf("states %q %+v; want the fixup below the owned file applying, the one at it not analysed", got, a.Fixups)
	}
}

// A conflict is unexplained only when no commit between target and fixup has
// the file as the fixup finds it.
func TestAnalyseFixupsReportAConflictNoCommitExplains(t *testing.T) {
	t.Parallel()
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: o", "o.txt", "o\n"},
		{"feat: a", "a.txt", "a\n"},
		{"fixup! feat: a", "n.txt", "one\n"},
	})
	gittest.Git(t, dir, "rm", "-q", "n.txt")
	gittest.Git(t, dir, "commit", "-q", "-m", "feat: drop")
	commits = append(commits, gitIn(t, dir, "rev-parse", "HEAD"), commitLine(t, dir, "fixup! feat: o", "n.txt", "three\n"))
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	stopped := commits[2]
	if got := outcomeOf(t, a, stopped); got.State != FixupConflicts {
		t.Errorf("outcome %+v, want conflicts", got)
	}
	if stop := realFold(t, dir, commits); stop != stopped {
		t.Errorf("the fold stopped at %q, want the fixup of feat: a", stop)
	}
}
