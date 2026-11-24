package wtsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
)

// foldPreviewLine commits files one commit at a time in a fresh repository:
// each step is "subject" followed by path/content pairs. It returns the
// repository, the line after "base" (oldest first) and the commit of each
// subject.
func foldPreviewLine(t *testing.T, steps [][]string) (string, []string, map[string]string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	sha := map[string]string{}
	for _, s := range steps {
		for i := 1; i+1 < len(s); i += 2 {
			gittest.WriteFile(t, filepath.Join(dir, s[i]), s[i+1])
		}
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", s[0])
		sha[s[0]] = gitIn(t, dir, "rev-parse", "HEAD")
	}
	return dir, strings.Fields(gitIn(t, dir, "rev-list", "--reverse", sha["base"]+"..HEAD")), sha
}

// A fixup that lacks its context behind its target in several files, with
// the context in a fixup of the same target that conflicts there too, is
// not analysed, and the line counts what is at risk.
func TestContextGapsCountWhatAFixupNotAnalysedHasAtRisk(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "y.txt", "y\n", "z.txt", "z\n"},
		{"feat: a", "y.txt", "y\na\n", "z.txt", "z\na\n"},
		{"feat: c", "y.txt", "y\na\nc\n", "z.txt", "z\na\nc\n"},
		{"fixup! feat: a", "y.txt", "y\na\nc\nf1\n", "z.txt", "z\na\nc\nf1\n"},
		{"fixup! feat: a", "y.txt", "y\na\nc\nf2\n", "z.txt", "z\na\nc\nf2\n"},
	})
	_, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	first, second := commits[len(commits)-2], commits[len(commits)-1]
	want := git.ShortID(second, 7) + ` "fixup! feat: a": not analysed: 2 files (2 hunks) lack context behind ` + git.ShortID(sha["feat: a"], 7) +
		` that 1 later commit adds; the earlier fixup ` + git.ShortID(first, 7) + ` conflicts there too`
	if len(unchecked) != 1 || unchecked[0] != want {
		t.Errorf("unchecked %q, want [%q]", unchecked, want)
	}
}

// A fixup whose context only the fold of a fixup of a later commit adds is
// a gap at that later commit: it comes after the target, not before.
func TestContextGapsNameTheLaterCommitAFixupOfItAddsContextTo(t *testing.T) {
	t.Parallel()
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "y.txt", "y\n"},
		{"feat: a", "y.txt", "y\na\n"},
		{"feat: b", "y.txt", "y\na\nb\n"},
		{"fixup! feat: b", "y.txt", "y\na\nb\nb2\n"},
		{"fixup! feat: a", "y.txt", "y\na\nb\nb3\n"},
	})
	gaps, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchecked) != 0 {
		t.Errorf("unchecked %q, want none", unchecked)
	}
	if len(gaps) != 1 || gaps[0].Fixup != commits[len(commits)-1] || gaps[0].Path != "y.txt" || gaps[0].Needs != sha["feat: b"] {
		t.Errorf("gaps %+v, want the last fixup's y.txt needing feat: b", gaps)
	}
}

// The files the preview counts at risk for a fixup it cannot analyse are the
// files that fixup stops with: git's own autosquash, with the first fixup
// resolved to its target's side, stops at the second in the 2 files.
func TestContextGapsCountMatchesTheFilesTheStopHas(t *testing.T) {
	t.Parallel()
	dir, commits, _ := foldPreviewLine(t, [][]string{
		{"base", "y.txt", "y\n", "z.txt", "z\n"},
		{"feat: a", "y.txt", "y\na\n", "z.txt", "z\na\n"},
		{"feat: c", "y.txt", "y\na\nc\n", "z.txt", "z\na\nc\n"},
		{"fixup! feat: a", "y.txt", "y\na\nc\nf1\n", "z.txt", "z\na\nc\nf1\n"},
		{"fixup! feat: a", "y.txt", "y\na\nc\nf2\n", "z.txt", "z\na\nc\nf2\n"},
	})
	_, unchecked, err := ContextGaps(dir, commits)
	if err != nil || len(unchecked) != 1 || !strings.Contains(unchecked[0], ": not analysed: 2 files (2 hunks) lack context") {
		t.Fatalf("unchecked %q, err %v; want the second fixup counted at 2 files", unchecked, err)
	}
	base := gitIn(t, dir, "rev-parse", commits[0]+"^")
	if out, err := gittest.Try(t, dir, "rebase", "-q", "-i", "--autosquash", base); err == nil {
		t.Fatalf("the fold did not stop at the first fixup:\n%s", out)
	}
	gitIn(t, dir, "checkout", "--ours", "y.txt", "z.txt")
	gitIn(t, dir, "add", "y.txt", "z.txt")
	if out, err := gittest.Try(t, dir, "-c", "core.editor=true", "rebase", "--continue"); err == nil {
		t.Fatalf("the fold did not stop at the second fixup:\n%s", out)
	}
	if got := strings.Fields(gitIn(t, dir, "diff", "--name-only", "--diff-filter=U")); len(got) != 2 {
		t.Errorf("the second fixup stops with %q, want the 2 files the preview counted", got)
	}
}

// A clean fixup of a later commit is credited with context only when its
// folded tree has it. C changes line 6, the fixup of B changes line 4, and the
// fixup of A changes line 5 with both in its context: B's fold lacks C's line,
// so the fixup is not named as needing B, and is counted as not analysed.
func TestContextGapsDoNotCreditAFoldWithWhatOnlyALaterCommitAdds(t *testing.T) {
	t.Parallel()
	lines := func(l4, l5, l6 string) string {
		return "l1\nl2\nl3\n" + l4 + "\n" + l5 + "\n" + l6 + "\nl7\nl8\nl9\n"
	}
	dir, commits, sha := foldPreviewLine(t, [][]string{
		{"base", "f.txt", lines("l4", "l5", "l6"), "g.txt", "g\n"},
		{"feat: a", "a.txt", "a\n"},
		{"feat: b", "g.txt", "g\nb\n"},
		{"feat: c", "f.txt", lines("l4", "l5", "c6")},
		{"fixup! feat: b", "f.txt", lines("b4", "l5", "c6")},
		{"fixup! feat: a", "f.txt", lines("b4", "a5", "c6")},
	})
	gaps, unchecked, err := ContextGaps(dir, commits)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gaps {
		if g.Needs == sha["feat: b"] {
			t.Errorf("gap %+v names feat: b, whose fold lacks the line feat: c adds", g)
		}
	}
	if len(unchecked) != 1 || !strings.Contains(unchecked[0], "fixup! feat: a") || !strings.Contains(unchecked[0], "not analysed") {
		t.Errorf("gaps %+v, unchecked %q; want the fixup of feat: a counted as not analysed", gaps, unchecked)
	}
}
