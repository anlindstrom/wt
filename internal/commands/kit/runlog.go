package kit

import "github.com/anlindstrom/wt/internal/runlog"

// NoteLock tells the run's log, when there is one, that this run has taken the
// lock of the worktree whose own git dir is gitDir: the worktree's pointer
// then names the log. A failure only costs wt sync wait its view of the run.
func (c *Context) NoteLock(gitDir string) {
	l := runlog.Active()
	if l == nil {
		return
	}
	if common, err := c.Repo.GitDir(); err == nil {
		l.Locked(common, gitDir)
	}
}
