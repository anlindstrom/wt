package testkit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
)

// gitAt runs git in dir as if at when: commits and reflog entries carry that
// time, which is how a test makes a backup old.
func gitAt(t *testing.T, dir string, when time.Time, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	stamp := fmt.Sprintf("%d +0000", when.Unix())
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+stamp, "GIT_AUTHOR_DATE="+stamp,
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// CommitOn puts one empty commit on branch, made at when, and returns it.
func CommitOn(t *testing.T, main, branch, msg string, when time.Time) string {
	t.Helper()
	tree := GitOut(t, main, "rev-parse", branch+"^{tree}")
	c := gitAt(t, main, when, "commit-tree", "-p", branch, "-m", msg, tree)
	gitAt(t, main, when, "update-ref", "refs/heads/"+branch, c)
	return c
}

// NoPRs is a pull request lookup that finds none.
var NoPRs = func([]string) (map[string]bool, error) { return map[string]bool{}, nil }

// BackupsRepo is a repository with origin and one of each kind of backup:
//
//	safe/contained  a branch at a commit main has
//	safe/old        a branch with a commit of its own, made and branched 30 days ago
//	safe/rebased    a branch cut today from a feature branch last committed 30 days ago
//	safe-tag          an annotated tag on main's first commit, tagged 30 days ago
//	safe/checked    a contained branch checked out in a worktree
//	release-safe    a contained branch with a protected name
//	feature           a branch no pattern matches
func BackupsRepo(t *testing.T) (*kit.Context, string) {
	t.Helper()
	ctx, main, _ := SweepRepo(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	first := GitOut(t, main, "rev-parse", "main")
	GitIn(t, main, "commit", "-q", "--allow-empty", "-m", "second")
	GitIn(t, main, "push", "-q", "origin", "main")
	GitIn(t, main, "branch", "safe/contained", first)
	gitAt(t, main, old, "branch", "safe/old", "main")
	CommitOn(t, main, "safe/old", "old work", old)
	gitAt(t, main, old, "branch", "feature", "main")
	CommitOn(t, main, "feature", "feature work", old)
	GitIn(t, main, "branch", "safe/rebased", "feature")
	CommitOn(t, main, "feature", "rebased away", time.Now())
	GitIn(t, main, "update-ref", "refs/heads/feature", GitOut(t, main, "rev-parse", "main"))
	gitAt(t, main, old, "tag", "-a", "-m", "safety", "safe-tag", first)
	GitIn(t, main, "branch", "safe/checked", first)
	GitIn(t, main, "worktree", "add", "-q", filepath.Join(t.TempDir(), "checked"), "safe/checked")
	GitIn(t, main, "branch", "release-safe", first)
	GitIn(t, main, "config", "branch.safe/contained.description", "mine")
	return ctx, main
}
