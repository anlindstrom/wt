package wtsync

import (
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// Each distinct snapshot an edit run keeps gets a ref of its own, created
// only where none is: a second refusal in one run never overwrites the
// first one's, and keeping the same commit again reuses its ref.
func TestKeepEditSnapshotNeverOverwritesOne(t *testing.T) {
	t.Parallel()
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	gittest.WriteFile(t, dir+"/a.txt", "a\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "one")
	first := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.WriteFile(t, dir+"/a.txt", "b\n")
	gittest.Git(t, dir, "commit", "-q", "-am", "two")
	second := gittest.Git(t, dir, "rev-parse", "HEAD")
	r1, err := KeepEditSnapshot(dir, "line", 7, first)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := KeepEditSnapshot(dir, "line", 7, second)
	if err != nil {
		t.Fatal(err)
	}
	again, err := KeepEditSnapshot(dir, "line", 7, first)
	if err != nil {
		t.Fatal(err)
	}
	if r1 == r2 || again != r1 {
		t.Fatalf("refs %s, %s, %s: want two refs, the first reused", r1, r2, again)
	}
	if got := gittest.Git(t, dir, "rev-parse", r1); got != first {
		t.Errorf("%s holds %s, want %s", r1, got, first)
	}
	if got := gittest.Git(t, dir, "rev-parse", r2); got != second {
		t.Errorf("%s holds %s, want %s", r2, got, second)
	}
}
