package wtsync

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// foldStopRepo is a line P, T, U and "fixup! T" (which changes the line T
// adds) rebased by git's autosquash onto a trunk that changed that line too:
// it stops at T. The plan is the fold and its target.
func foldStopRepo(t *testing.T) (dir string, fixup string, plan *ReplayPlan) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	write := func(content, subject string) string {
		file := "t.txt"
		if subject == "feat: p" {
			file = "p.txt"
		}
		gittest.WriteFile(t, filepath.Join(dir, file), content)
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", subject)
		return gitIn(t, dir, "rev-parse", "HEAD")
	}
	write("a\nb\nc\n", "base")
	gitIn(t, dir, "checkout", "-q", "-b", "feat")
	write("p\n", "feat: p")
	target := write("a\nb\nc\nT\n", "feat: t")
	write("a\nb\nc\nT\nU\n", "feat: u")
	fixup = write("a\nb\nc\nT2\nU\n", "fixup! feat: t")
	gitIn(t, dir, "checkout", "-q", "main")
	write("a\nb\nc\nZ\n", "trunk")
	gitIn(t, dir, "checkout", "-q", "feat")
	if out, err := gittest.Try(t, dir, "rebase", "-q", "-i", "--autosquash", "main"); err == nil {
		t.Fatalf("the rebase did not stop at the target:\n%s", out)
	}
	return dir, fixup, &ReplayPlan{Folds: []string{fixup}, Targets: map[string]string{fixup: target}}
}

// The fixup the rebase is stopped at counts as folded once the rebase ends
// when HEAD is its target's replay.
func TestAWatchCountsAFixupWhoseTargetWasPicked(t *testing.T) {
	t.Parallel()
	dir, fixup, plan := foldStopRepo(t)
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), "a\nb\nc\nZ\nT\n")
	gitIn(t, dir, "add", "t.txt")
	if out, err := gittest.Try(t, dir, "rebase", "--continue"); err == nil {
		t.Fatalf("the rebase did not stop at the fixup:\n%s", out)
	}
	w := startAt(dir, nil, plan)
	w.settle()
	if !slices.Equal(w.executed, []string{fixup}) {
		t.Errorf("executed %q, want the fixup", w.executed)
	}
}

// A target a person skipped is no target: the fixup then folds into the
// commit before it, and the watch does not count it.
func TestAWatchDoesNotCountAFixupWhoseTargetWasSkipped(t *testing.T) {
	t.Parallel()
	dir, _, plan := foldStopRepo(t)
	if out, err := gittest.Try(t, dir, "rebase", "--skip"); err == nil {
		t.Fatalf("the rebase did not stop at the fixup:\n%s", out)
	}
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), "a\nb\nc\nZ\nT2\n")
	gitIn(t, dir, "add", "t.txt")
	w := startAt(dir, nil, plan)
	w.settle()
	if len(w.executed) != 0 {
		t.Errorf("executed %q, want none: the fixup's target was skipped", w.executed)
	}
}
