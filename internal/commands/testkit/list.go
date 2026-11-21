package testkit

import (
	"path/filepath"
	"testing"
)

// RepoWithWorktree builds one repo and adds a worktree at the path returned by
// where(parent), so the caller cannot accidentally compute a path from a
// different repository than the one it mutates.
func RepoWithWorktree(t *testing.T, where func(parent string) string) (main, wt string) {
	t.Helper()
	main = FixtureRepo(t, MinimalConf)
	GitIn(t, main, "config", "user.email", "t@example.com")
	GitIn(t, main, "config", "user.name", "T")
	GitIn(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt = where(filepath.Dir(main))
	GitIn(t, main, "worktree", "add", "-q", "-b", "feat_wt/thing", wt)
	return main, wt
}
