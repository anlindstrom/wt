package wtsync

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// commitLine adds one commit writing content to path, and is its id.
func commitLine(t *testing.T, dir, subject, path, content string) string {
	t.Helper()
	gittest.WriteFile(t, filepath.Join(dir, path), content)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", subject)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

func outcomeOf(t *testing.T, a FixupAnalysis, fixup string) FixupOutcome {
	t.Helper()
	for _, f := range a.Fixups {
		if f.Fixup == fixup {
			return f
		}
	}
	t.Fatalf("no outcome for %s in %+v", fixup, a.Fixups)
	return FixupOutcome{}
}

// git orders a fixup of a fixup's id after the later fixups of the first
// target, which the analysis does not replay: nothing is said to apply.
func TestAnalyseFixupsWithholdALineWithAFixupOfAFixup(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"fixup! feat: a", "a.txt", "a\nf\n"},
	})
	g := commitLine(t, dir, "fixup! "+sha["fixup! feat: a"], "t.txt", "one\n")
	h := commitLine(t, dir, "fixup! feat: a", "t.txt", "one\ntwo\n")
	commits = append(commits, g, h)

	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range a.Fixups {
		if f.State != FixupNotAnalysed || f.Why == "" {
			t.Errorf("%s: %s %q, want not analysed with a reason", f.Subject, f.State, f.Why)
		}
	}
	// The stop still has its gap, at the commit the line has the context at.
	if len(a.Gaps) != 1 || a.Gaps[0].Fixup != h || a.Gaps[0].Needs != sha["fixup! feat: a"] {
		t.Errorf("gaps %+v, want the last fixup's, at the fixup the line has the context at", a.Gaps)
	}
	if stop := realFold(t, dir, commits); stop != h {
		t.Errorf("the fold stopped at %q, want the last fixup", stop)
	}
}

// A subject is looked for before a revision, as autosquash does: a tag named
// like a subject does not take the fixup.
func TestAnalyseFixupsFindTheTargetBySubjectBeforeRevision(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"release", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
	})
	gitIn(t, dir, "tag", "release", sha["feat: b"])
	commits = append(commits, commitLine(t, dir, "fixup! release", "t.txt", "one\n"))

	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Fixups) != 1 || a.Fixups[0].Target != sha["release"] || a.Fixups[0].State != FixupApplies {
		t.Fatalf("outcomes %+v, want applying at the commit with that subject", a.Fixups)
	}
	if stop := realFold(t, dir, commits); stop != "" {
		t.Fatalf("the fold stopped at %s", stop)
	}
	if got := gitIn(t, dir, "show", "HEAD~1:t.txt"); got != "one" {
		t.Errorf("t.txt in the first folded commit is %q", got)
	}
}

// A gap names the first commit on the line with the context, as it did, and
// says where the fold has it when that is another: an earlier fixup moved
// ahead of a commit changes what the commit's tree has.
func TestAnalyseFixupsSayWhereTheFoldHasAGapsContext(t *testing.T) {
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
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Gaps) != 1 || a.Gaps[0].Needs != sha["feat: c"] || !strings.Contains(a.Gaps[0].Doubt, `"feat: d"`) {
		t.Fatalf("gaps %+v, unchecked %q; want one needing feat: c, the fold having it at feat: d", a.Gaps, a.Unchecked)
	}
	if stop := realFold(t, dir, commits); stop != sha["fixup! feat: b"] {
		t.Fatalf("the fold stopped at %q, want the fixup of feat: b", stop)
	}
	if out, err := gittest.Try(t, dir, "rebase", "--skip"); err != nil {
		t.Fatalf("skip: %v\n%s", err, out)
	}
	if got := firstWithRun(t, dir, "HEAD~2", "x.txt", []string{"a"}); got != "feat: d" {
		t.Errorf("the real fold first has the context again at %q", got)
	}
}

// A declared version lift can rewrite what a later fixup is merged onto: from
// the commit it may lift at, nothing is said to apply.
func TestAnalyseFixupsWithholdFromWhereAVersionMayBeLifted(t *testing.T) {
	t.Parallel()
	dir, wt, cfg := liftRepo(t, nil, nil)
	for _, c := range [][3]string{
		{"feat: a", "a.txt", "a2\n"},
		{"feat: bump", "app.yaml", appYAML("1.2.3", "1.5.1")},
		{"feat: c", "b.txt", "c\n"},
		{"fixup! feat: c", "app.yaml", appYAML("2.0.0", "1.5.1")},
		{"feat: back", "app.yaml", appYAML("1.2.2", "1.5.1")},
		{"fixup! feat: a", "app.yaml", appYAML("1.2.3", "1.5.1")},
	} {
		commitLine(t, wt, c[0], c[1], c[2])
	}
	line, err := ReadLine(dir, "feature", "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	late := line.Commits[3]

	plain, err := AnalyseFixups(dir, line.Commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeOf(t, plain, late); got.State != FixupApplies {
		t.Fatalf("with no declaration: %+v, want it applying", got)
	}
	a, err := AnalyseFixups(dir, line.Commits, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeOf(t, a, late); got.State != FixupNotAnalysed || got.Why == "" {
		t.Errorf("with the declaration: %+v, want not analysed", got)
	}
	req := Request{Path: wt, Branch: "feature", Trunk: "origin/main", Onto: line.Base, Epoch: 7,
		Stack: &StackRequest{Line: line, Autosquash: true}}
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, req, &log)
	if err != nil || !slices.ContainsFunc(res.Stops, func(s StopResult) bool { return s.Commit == late }) {
		t.Errorf("err %v, stops %+v: want the driver to stop at that fixup\n%s", err, res.Stops, log.String())
	}
}

// A fixup of a fixup elsewhere on the line does not hide a gap it has no part
// in: the run's resume check reads the gaps.
func TestContextGapsKeepAGapBesideAnUnrelatedFixupOfAFixup(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "t.txt", "one\n"},
		{"fixup! feat: a", "t.txt", "one\ntwo\n"},
		{"feat: c", "c.txt", "c\n"},
		{"fixup! feat: c", "c.txt", "c\nf\n"},
	})
	commits = append(commits, commitLine(t, dir, "fixup! "+sha["fixup! feat: c"], "c.txt", "c\nf\ng\n"))
	gaps, unchecked, err := ContextGaps(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Fixup != sha["fixup! feat: a"] || gaps[0].Needs != sha["feat: b"] {
		t.Errorf("gaps %+v, unchecked %q; want the fixup of feat: a needing feat: b", gaps, unchecked)
	}
	if stop := realFold(t, dir, commits); stop != sha["fixup! feat: a"] {
		t.Errorf("the fold stopped at %q, want the fixup of feat: a", stop)
	}
}

// A commit an earlier fold empties is dropped by the run, which goes on: the
// context a later commit adds is still found.
func TestContextGapsLookPastACommitTheFoldDrops(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "f.txt", "0\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"feat: c", "f.txt", "1\n"},
		{"feat: d", "t.txt", "one\n"},
		{"feat: back", "f.txt", "0\n"},
		{"fixup! feat: a", "f.txt", "1\n"},
		{"fixup! feat: b", "t.txt", "one\ntwo\n"},
	})
	gaps, unchecked, err := ContextGaps(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Fixup != sha["fixup! feat: b"] || gaps[0].Needs != sha["feat: d"] {
		t.Errorf("gaps %+v, unchecked %q; want the fixup of feat: b needing feat: d", gaps, unchecked)
	}
	base := gitIn(t, dir, "rev-parse", commits[0]+"^")
	if out, err := gittest.Try(t, dir, "rebase", "-q", "-i", "--autosquash", "--empty=drop", base); err == nil {
		t.Fatalf("the fold did not stop:\n%s", out)
	}
	if stop := gitIn(t, dir, "rev-parse", "REBASE_HEAD"); stop != sha["fixup! feat: b"] {
		t.Errorf("the fold stopped at %s, want the fixup of feat: b", stop)
	}
}

// The files of one fixup are looked for in one replay of the commits after its
// target, which ends once each has its answer.
func TestContextGapsReplayOnceForAFixupOfSeveralFiles(t *testing.T) {
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "x.txt", "x\n", "y.txt", "y\n"},
		{"feat: c", "c.txt", "c\n"},
		{"feat: d", "d.txt", "d\n"},
		{"fixup! feat: a", "x.txt", "x\nx2\n", "y.txt", "y\ny2\n"},
	})
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	gaps, _, err := ContextGaps(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 2 || gaps[0].Needs != sha["feat: b"] || gaps[1].Needs != sha["feat: b"] {
		t.Fatalf("gaps %+v, want both files needing feat: b", gaps)
	}
	ran, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	// The fold to its stop (two), the stop looked at, and feat: b.
	if got := bytes.Count(ran, []byte("trace: built-in: git merge-tree")); got != 4 {
		t.Errorf("%d merges, want 4", got)
	}
}

// The run looks for a version to lift at the commits it picks, not at a fixup
// that merges clean.
func TestAnalyseFixupsExpectNoLiftAtACleanFixup(t *testing.T) {
	t.Parallel()
	dir, wt, cfg := liftRepo(t, nil, nil)
	commitLine(t, wt, "feat: a", "a.txt", "a2\n")
	commitLine(t, wt, "feat: port", "app.yaml", appYAMLPort("9090", "1.2.2", "1.5.1"))
	fixup := commitLine(t, wt, "fixup! feat: a", "app.yaml", appYAMLPort("9090", "1.2.2", "1.5.2"))
	line, err := ReadLine(dir, "feature", "origin/main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := AnalyseFixups(dir, line.Commits, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeOf(t, a, fixup); got.State != FixupApplies {
		t.Errorf("%+v, want it applying", got)
	}
	req := Request{Path: wt, Branch: "feature", Trunk: "origin/main", Onto: line.Base, Epoch: 8,
		Stack: &StackRequest{Line: line, Autosquash: true}}
	var log bytes.Buffer
	res, err := Rebase(dir, cfg, req, &log)
	if err != nil || res.Left != nil || len(res.Stops) != 0 {
		t.Errorf("err %v, left %+v, stops %+v: want a fold with no stop\n%s", err, res.Left, res.Stops, log.String())
	}
}
