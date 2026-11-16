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
	if why := foldChanged(dir, base, onto, oldTip, good, nil); why != "" {
		t.Fatalf("a correct fold is refused: %s", why)
	}
	lost := newLine(t, dir, onto, lines("trunk", "last"))
	if why := foldChanged(dir, base, onto, oldTip, lost, nil); !strings.Contains(why, "t.txt") {
		t.Fatalf("a lost feature hunk passed: %q", why)
	}
}
