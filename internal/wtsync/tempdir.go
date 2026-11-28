package wtsync

import (
	"io/fs"
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
	// closing is set once a signal is ending the process.
	closing bool
}{dirs: map[string]bool{}}

// CloseTempDirs is for the handler of a signal, ahead of RemoveTempDirs: no
// temporary directory is made from here on, and a caller asking for one does
// not come back.
func CloseTempDirs() {
	tempDirs.Lock()
	defer tempDirs.Unlock()
	tempDirs.closing = true
}

// tempDir makes a private directory under the system's temporary directory,
// registered until cleanup removes it.
func tempDir(pattern string) (dir string, cleanup func(), err error) { return tempDirIn("", pattern) }

// tempDirIn is tempDir under parent; "" is the system's.
func tempDirIn(parent, pattern string) (dir string, cleanup func(), err error) {
	// Made and registered, and removed before it is forgotten, so an
	// interrupt between the two finds it.
	tempDirs.Lock()
	if tempDirs.closing {
		tempDirs.Unlock()
		select {}
	}
	defer tempDirs.Unlock()
	dir, err = os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", nil, err
	}
	tempDirs.dirs[dir] = true
	return dir, func() {
		_ = removeTree(dir)
		tempDirs.Lock()
		delete(tempDirs.dirs, dir)
		tempDirs.Unlock()
	}, nil
}

// handlerTempDir is tempDir for the handler of a signal, outside the
// registry it has closed.
func handlerTempDir(pattern string) (dir string, cleanup func(), err error) {
	if dir, err = os.MkdirTemp("", pattern); err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// whileTempDir runs op, which writes into the temporary directory dir, with
// its removal held off. Once an interrupt has removed it the caller does not
// come back, as the process is on its way out: what it would write next would
// make the directory again.
func whileTempDir(dir string, op func() error) error {
	tempDirs.Lock()
	if !tempDirs.dirs[dir] {
		tempDirs.Unlock()
		select {}
	}
	defer tempDirs.Unlock()
	return op()
}

// removeTree removes dir and all in it, the directories something left
// read-only too, as Go's module cache is.
func removeTree(dir string) error {
	makeWritable(dir)
	return os.RemoveAll(dir)
}

func makeWritable(dir string) {
	_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			if info, ierr := e.Info(); ierr == nil && info.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(path, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
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
		if err := removeTree(dir); err == nil {
			n++
		}
		delete(tempDirs.dirs, dir)
	}
	return n
}
