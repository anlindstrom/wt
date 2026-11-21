package wtsync

// trackedStatus is git status --porcelain of the tracked files at path: ""
// when none differs from HEAD. It is the one place wtsync reads a worktree's
// status for a precondition (the dirty check of a rebase, of a deferred step,
// of a recheck), so they cannot drift apart. --no-optional-locks, because a
// plain status may refresh and rewrite the index, and a check must not touch
// the worktree it is checking; --untracked-files=no, because an untracked file
// never blocks a rebase and a script may have left one. A status that cannot
// be read is an error, never a clean status.
func trackedStatus(path string) (string, error) {
	return gitEnv(path, nil, nil, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no")
}
