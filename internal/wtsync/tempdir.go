package wtsync

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// tempDirs is every temporary directory a simulation, a strategy or a run has
// open. The overview assesses worktrees in parallel, and an interrupt exits
// from the signal handler's goroutine before any of their deferred removals
// run; RemoveTempDirs is how the interrupt takes the directories with it.
var tempDirs = struct {
	sync.Mutex
	dirs map[string]bool
}{dirs: map[string]bool{}}

// tempDir makes a private directory under the system's temporary directory,
// registered until cleanup removes it.
func tempDir(pattern string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	tempDirs.Lock()
	tempDirs.dirs[dir] = true
	tempDirs.Unlock()
	return dir, func() {
		tempDirs.Lock()
		delete(tempDirs.dirs, dir)
		tempDirs.Unlock()
		_ = os.RemoveAll(dir)
	}, nil
}

// tempWorktrees is every temporary worktree a check has open, by its
// directory, with the repository's common git dir its registration is in.
var tempWorktrees = struct {
	sync.Mutex
	dirs map[string]string
}{dirs: map[string]string{}}

func registerTempWorktree(dir, common string) {
	tempWorktrees.Lock()
	tempWorktrees.dirs[dir] = common
	tempWorktrees.Unlock()
}

func unregisterTempWorktree(dir string) {
	tempWorktrees.Lock()
	delete(tempWorktrees.dirs, dir)
	tempWorktrees.Unlock()
}

// forgetTempWorktree removes the registration of the temporary worktree at
// dir from common/worktrees, without git: the one entry whose gitdir file
// names dir's .git, and nothing else. It is how a removal git refused, and
// an interrupt, which runs no git, clean up after themselves.
func forgetTempWorktree(dir, common string) {
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return
	}
	want := filepath.Clean(filepath.Join(dir, ".git"))
	for _, e := range entries {
		admin := filepath.Join(common, "worktrees", e.Name())
		b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil || filepath.Clean(strings.TrimSpace(string(b))) != want {
			continue
		}
		_ = os.RemoveAll(admin)
	}
}

// RemoveTempDirs removes the registration of every temporary worktree still
// open, then every temporary directory still registered, and reports how
// many directories it removed, for an interrupt that exits without running
// deferred cleanups. It runs after the gits are killed, and an assessment
// whose git died bails on the error rather than opening another.
func RemoveTempDirs() int {
	tempWorktrees.Lock()
	for dir, common := range tempWorktrees.dirs {
		forgetTempWorktree(dir, common)
	}
	tempWorktrees.dirs = map[string]string{}
	tempWorktrees.Unlock()
	tempDirs.Lock()
	defer tempDirs.Unlock()
	n := 0
	for dir := range tempDirs.dirs {
		if err := os.RemoveAll(dir); err == nil {
			n++
		}
		delete(tempDirs.dirs, dir)
	}
	return n
}
