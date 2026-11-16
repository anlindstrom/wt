package syncrun

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// foldRepo is a base with t.txt of twelve lines, a target that changes the
// first, and an old line that changes the last: the two hunks are
// independent, so merging the line onto the target keeps both.
func foldRepo(t *testing.T) (dir, base, onto, oldTip string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	lines := func(first, last string) string { return first + "\n" + strings.Repeat("mid\n", 10) + last + "\n" }
	write := func(f, c, msg string) string {
		gittest.WriteFile(t, filepath.Join(dir, f), c)
		gittest.Git(t, dir, "add", "-A")
		gittest.Git(t, dir, "commit", "-q", "-m", msg)
		return gittest.Git(t, dir, "rev-parse", "HEAD")
	}
	base = write("t.txt", lines("first", "last"), "base")
	gittest.Git(t, dir, "checkout", "-q", "-b", "line")
	oldTip = write("t.txt", lines("first", "feature"), "feature")
	gittest.Git(t, dir, "checkout", "-q", "main")
	onto = write("t.txt", lines("trunk", "last"), "trunk")
	return dir, base, onto, oldTip
}

// newLine commits content as t.txt on top of onto and returns the commit.
func newLine(t *testing.T, dir, onto, content string) string {
	t.Helper()
	gittest.Git(t, dir, "checkout", "-q", "--detach", onto)
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), content)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-am", "feature")
	return gittest.Git(t, dir, "rev-parse", "HEAD")
}

// A fold that moves the line onto a target keeps what merging the old line
// onto it gives, in every file that merges cleanly: a feature hunk lost in a
// file the target also changed is refused, not passed because the target
// changed the file.
func TestFoldChangedRefusesAFeatureHunkLostInAFileTheTargetChanged(t *testing.T) {
	t.Parallel()
	dir, base, onto, oldTip := foldRepo(t)
	lines := func(first, last string) string { return first + "\n" + strings.Repeat("mid\n", 10) + last + "\n" }
	good := newLine(t, dir, onto, lines("trunk", "feature"))
	if why := foldChanged(dir, base, onto, oldTip, good, nil, nil); why != "" {
		t.Fatalf("a correct fold is refused: %s", why)
	}
	lost := newLine(t, dir, onto, lines("trunk", "last"))
	if why := foldChanged(dir, base, onto, oldTip, lost, nil, nil); !strings.Contains(why, "t.txt") {
		t.Fatalf("a lost feature hunk passed: %q", why)
	}
}

// The old tree is no proof for a moving fold: one whose result restores the
// old line's tree has lost every change of the target.
func TestFoldChangedRefusesAMovingFoldThatRestoresTheOldTree(t *testing.T) {
	t.Parallel()
	dir, base, onto, oldTip := foldRepo(t)
	lines := func(first, last string) string { return first + "\n" + strings.Repeat("mid\n", 10) + last + "\n" }
	restored := newLine(t, dir, onto, lines("first", "feature"))
	if why := foldChanged(dir, base, onto, oldTip, restored, nil, nil); why == "" {
		t.Fatal("a moving fold that lost the target's change passed because its tree equals the old line's")
	}
}

// A file a stop resolved is the stop's, but only its conflicting region: a
// hunk of the line outside it that the resolution lost is refused, and one
// kept is accepted. A path no stop touched has no such allowance.
func TestFoldChangedHoldsTheUnownedHunksOfAResolvedFile(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	lines := func(first, last string) string { return first + "\n" + strings.Repeat("mid\n", 10) + last + "\n" }
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), lines("first", "last"))
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "base")
	base := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "checkout", "-q", "-b", "line")
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), lines("mine", "feature"))
	gittest.Git(t, dir, "commit", "-q", "-a", "-m", "feature")
	oldTip := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "checkout", "-q", "main")
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), lines("trunk", "last"))
	gittest.Git(t, dir, "commit", "-q", "-a", "-m", "trunk")
	onto := gittest.Git(t, dir, "rev-parse", "HEAD")
	good := newLine(t, dir, onto, lines("mine", "feature"))
	lost := newLine(t, dir, onto, lines("mine", "last"))
	stopped := []string{"t.txt"}
	if why := foldChanged(dir, base, onto, oldTip, good, nil, stopped); why != "" {
		t.Errorf("a resolution that keeps the line's other hunk is refused: %s", why)
	}
	if why := foldChanged(dir, base, onto, oldTip, lost, nil, stopped); !strings.Contains(why, "t.txt") {
		t.Errorf("a resolution that lost the line's other hunk passed: %q", why)
	}
	if why := foldChanged(dir, base, onto, oldTip, good, nil, nil); !strings.Contains(why, "t.txt") {
		t.Errorf("a file no stop touched was exempt: %q", why)
	}
}
