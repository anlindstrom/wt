package wtsync

import (
	"maps"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
)

// realFold runs git's own autosquash of the line in dir, and is the commit it
// stops at, "" when it finishes.
func realFold(t *testing.T, dir string, commits []string) string {
	t.Helper()
	base := gitIn(t, dir, "rev-parse", commits[0]+"^")
	out, err := gittest.Try(t, dir, "rebase", "-q", "-i", "--autosquash", base)
	if err == nil {
		return ""
	}
	stop, stopErr := gittest.Try(t, dir, "rev-parse", "--verify", "REBASE_HEAD")
	if stopErr != nil {
		t.Fatalf("the fold failed without stopping at a commit:\n%s", out)
	}
	return strings.TrimSpace(stop)
}

// states is each outcome's state, in line order.
func states(a FixupAnalysis) []FixupState {
	var out []FixupState
	for _, f := range a.Fixups {
		out = append(out, f.State)
	}
	return out
}

// wt#237: a fixup of a file that an earlier fixup creates is analysed with
// that one folded in, and its edit lands in its own target.
func TestAnalyseFixupsFoldsInTheEarlierFixupThatCreatesTheFile(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"fixup! feat: a", "t.txt", "one\n"},
		{"fixup! feat: b", "t.txt", "one\ntwo\n"},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Unchecked) != 0 || len(a.Gaps) != 0 {
		t.Errorf("unchecked %q, gaps %+v; want neither", a.Unchecked, a.Gaps)
	}
	want := []FixupOutcome{
		{Fixup: sha["fixup! feat: a"], Subject: "fixup! feat: a", Target: sha["feat: a"], State: FixupApplies},
		{Fixup: sha["fixup! feat: b"], Subject: "fixup! feat: b", Target: sha["feat: b"], State: FixupApplies},
	}
	if len(a.Fixups) != 2 || a.Fixups[0] != want[0] || a.Fixups[1] != want[1] {
		t.Errorf("outcomes %+v, want %+v", a.Fixups, want)
	}

	if stop := realFold(t, dir, commits); stop != "" {
		t.Fatalf("the fold stopped at %s", git.ShortID(stop, 7))
	}
	for rev, content := range map[string]string{"HEAD~1": "one", "HEAD": "one\ntwo"} {
		if got := gitIn(t, dir, "show", rev+":t.txt"); got != content {
			t.Errorf("t.txt at %s is %q, want %q", rev, got, content)
		}
	}
	if got := gitIn(t, dir, "log", "--format=%s", "-2"); got != "feat: b\nfeat: a" {
		t.Errorf("the folded line ends %q", got)
	}
}

// The same pair where the later edit needs a line a later commit adds: a gap
// at that commit, and the real fold stops at the later fixup.
func TestAnalyseFixupsNameTheContextOfAFileAnEarlierFixupCreates(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"fixup! feat: a", "t.txt", "one\n"},
		{"feat: c", "t.txt", "one\ntwo\n"},
		{"fixup! feat: b", "t.txt", "one\nTWO\n"},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := states(a); len(got) != 2 || got[0] != FixupApplies || got[1] != FixupAheadOfContext {
		t.Errorf("states %q, want applies then ahead of context", got)
	}
	if len(a.Unchecked) != 0 || len(a.Gaps) != 1 || a.Gaps[0].Needs != sha["feat: c"] {
		t.Errorf("gaps %+v, unchecked %q; want one gap needing feat: c", a.Gaps, a.Unchecked)
	}
	if stop := realFold(t, dir, commits); stop != sha["fixup! feat: b"] {
		t.Errorf("the fold stopped at %q, want the fixup of feat: b", stop)
	}
}

// A binary conflict whose preimage a later commit has is not called
// unexplained, and a fixup the run reaches only after that stop is not said to
// apply.
func TestAnalyseFixupsDoNotCallABinaryWithALaterPreimageUnexplained(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "bin", "\x00a\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "bin", "\x00b\n"},
		{"fixup! feat: a", "bin", "\x00c\n"},
		{"fixup! feat: b", "b.txt", "b\n"},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := states(a); len(got) != 2 || got[0] != FixupNotAnalysed || got[1] != FixupNotAnalysed {
		t.Fatalf("states %q, want both not analysed", got)
	}
	first := sha["fixup! feat: a"]
	if want := git.ShortID(sha["feat: b"], 7) + ` "feat: b" has bin as the fixup changes it`; !strings.Contains(a.Fixups[0].Why, want) {
		t.Errorf("why %q, want %q", a.Fixups[0].Why, want)
	}
	if want := "the fold in place conflicts at " + git.ShortID(first, 7) + ` "fixup! feat: a" first`; a.Fixups[1].Why != want {
		t.Errorf("why %q, want %q", a.Fixups[1].Why, want)
	}
	if len(a.Gaps) != 0 || len(a.Unchecked) != 0 {
		t.Errorf("gaps %+v, unchecked %q; want neither", a.Gaps, a.Unchecked)
	}
	if stop := realFold(t, dir, commits); stop != first {
		t.Errorf("the fold stopped at %q, want the fixup of feat: a", stop)
	}
}

// notAnalysed is each fixup of want, by subject, not analysed for its reason.
func notAnalysed(t *testing.T, a FixupAnalysis, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, f := range a.Fixups {
		if f.State != FixupNotAnalysed {
			t.Errorf("%s: %s, want not analysed", f.Subject, f.State)
		}
		got[f.Subject] = f.Why
	}
	if !maps.Equal(got, want) {
		t.Errorf("reasons %q, want %q", got, want)
	}
}

// A fixup of a target the fold empties lands below it, so it is not said to
// apply there.
func TestAnalyseFixupsSayATargetTheFoldEmptiesIsDropped(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "f.txt", "0\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: x", "f.txt", "1\n"},
		{"feat: back", "f.txt", "0\n"},
		{"fixup! feat: a", "f.txt", "1\n"},
		{"fixup! feat: x", "c.txt", "c\n"},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Fixups) != 2 || a.Fixups[0].State != FixupApplies {
		t.Fatalf("outcomes %+v, want two, the first applying", a.Fixups)
	}
	a.Fixups = a.Fixups[1:]
	notAnalysed(t, a, map[string]string{
		"fixup! feat: x": "git drops its target " + git.ShortID(sha["feat: x"], 7) + " as empty, so it folds into the commit below",
	})
}

// The run refuses a line with a fixup that names no commit before anything is
// replayed, so no fixup of it is said to apply.
func TestAnalyseFixupsClaimNothingOnALineTheRunRefuses(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"fixup! feat: a", "c.txt", "c\n"},
		{"fixup! nothing", "d.txt", "d\n"},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	notAnalysed(t, a, map[string]string{
		"fixup! feat: a": "the run refuses the line: " + git.ShortID(sha["fixup! nothing"], 7) + ` "fixup! nothing" names no one commit earlier on it`,
		"fixup! nothing": "its subject names no one commit earlier on the line",
	})
}

// A line with a move! commit is not replayed, and every fixup says so.
func TestAnalyseFixupsDoNotReplayALineWithAMove(t *testing.T) {
	t.Parallel()
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "base.txt", "base\n"},
		{"feat: a", "a.txt", "a\n"},
		{"fixup! feat: a", "c.txt", "c\n"},
		{"fixup! feat: a", "d.txt", "d\n"},
	})
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "move! feat: a")
	commits = append(commits, gitIn(t, dir, "rev-parse", "HEAD"))
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Fixups) != 2 {
		t.Fatalf("outcomes %+v, want both fixups", a.Fixups)
	}
	for i, f := range a.Fixups {
		if f.Fixup != commits[i+1] || f.State != FixupNotAnalysed || f.Why != "the line has a move! commit, whose part wt does not fold in here" {
			t.Errorf("outcome %+v, want %s not analysed for the move", f, git.ShortID(commits[i+1], 7))
		}
	}
}

// wt#237: two fixups of different targets editing one method that neither
// creates are analysed, and each edit lands in its own target.
func TestAnalyseFixupsFollowTwoFixupsOfOneExistingMethod(t *testing.T) {
	t.Parallel()
	method := func(first, second string) string {
		return "class A {\n  int f() {\n    int x = " + first + ";\n    return " + second + ";\n  }\n}\n"
	}
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "A.java", method("0", "x")},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "b.txt", "b\n"},
		{"fixup! feat: a", "A.java", method("1", "x")},
		{"fixup! feat: b", "A.java", method("1", "x + 1")},
	})
	a, err := AnalyseFixups(dir, commits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := states(a); len(got) != 2 || got[0] != FixupApplies || got[1] != FixupApplies || len(a.Unchecked) != 0 || len(a.Gaps) != 0 {
		t.Errorf("states %q, gaps %+v, unchecked %q; want both applying", got, a.Gaps, a.Unchecked)
	}
	if stop := realFold(t, dir, commits); stop != "" {
		t.Fatalf("the fold stopped at %s", git.ShortID(stop, 7))
	}
	for rev, content := range map[string]string{"HEAD~1": method("1", "x"), "HEAD": method("1", "x + 1")} {
		if got := gitIn(t, dir, "show", rev+":A.java") + "\n"; got != content {
			t.Errorf("A.java at %s is %q, want %q", rev, got, content)
		}
	}
}
