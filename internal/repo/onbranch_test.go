package repo

import (
	"path/filepath"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A checkout is on a branch only when HEAD names it attached; a detached
// HEAD, another branch and a path git cannot read are all not on it, and the
// ref HEAD names comes back for the refusal that quotes it.
func TestOnBranch(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := gittest.NewRepo(t, parent, "r")
	gittest.WriteFile(t, filepath.Join(main, "f"), "x\n")
	gittest.Git(t, main, "add", "-A")
	gittest.Git(t, main, "commit", "-q", "-m", "base")
	other := linked(t, main, "feature/x")
	detached := linked(t, main, "feature/y")
	gittest.Git(t, detached, "checkout", "-q", "--detach")
	cases := []struct {
		name, path, branch, wantHead string
		wantOn                       bool
	}{
		{"the branch it is on", other, "feature/x", "refs/heads/feature/x", true},
		{"another branch", other, "main", "refs/heads/feature/x", false},
		{"a prefix of its name is not it", other, "feature", "refs/heads/feature/x", false},
		{"detached", detached, "feature/y", "", false},
		{"not a checkout", filepath.Join(parent, "nowhere"), "main", "", false},
	}
	for _, c := range cases {
		head, on := OnBranch(c.path, c.branch)
		if head != c.wantHead || on != c.wantOn {
			t.Errorf("%s: OnBranch = %q, %v; want %q, %v", c.name, head, on, c.wantHead, c.wantOn)
		}
	}
}
