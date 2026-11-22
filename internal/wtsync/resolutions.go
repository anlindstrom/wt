package wtsync

import (
	"os"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// ResolutionsAtStop is what a person resolved at the conflict stop st handed
// over, as the index holds it when resume goes on: for each path that was
// unmerged when wt stopped and that the stop left them (Unmerged and Left),
// the mode and blob it is staged with, or neither for a path they resolved by
// deleting.
//
// It records only what it can prove is that conflict's resolution: the
// rebase is still at the stop wt left (AtHandoverStop: the same todo list,
// command count and stopped commit), the conflicted pick is still pending
// (REBASE_HEAD is the stopped commit, which a --continue by hand removes,
// whatever is inserted after it), and nothing is unmerged. Otherwise nothing
// is recorded and the fold is checked as before.
func ResolutionsAtStop(wtPath string, st State) []StopResolution {
	if st.ByHand || st.AtEdit || st.Check != nil || st.StoppedCommit == "" || !AtHandoverStop(wtPath, st) {
		return nil
	}
	if head := rebaseHead(wtPath); head == "" ||
		!strings.HasPrefix(head, st.StoppedCommit) && !strings.HasPrefix(st.StoppedCommit, head) {
		return nil
	}
	if unmerged, err := StagedConflicts(wtPath); err != nil || len(unmerged) > 0 {
		return nil
	}
	var out []StopResolution
	for _, path := range st.Left {
		if path == messagesPath || !slices.Contains(st.Unmerged, path) {
			continue
		}
		rec, err := git.Run(wtPath, "--literal-pathspecs", "ls-files", "--stage", "-z", "--", path)
		if err != nil {
			return nil
		}
		line, _, _ := strings.Cut(rec, "\x00")
		r := StopResolution{Commit: st.StoppedCommit, Path: path}
		if line != "" {
			// "<mode> <blob> <stage>\t<path>"
			f := strings.Fields(strings.SplitN(line, "\t", 2)[0])
			if len(f) != 3 || f[2] != "0" {
				return nil
			}
			r.Mode, r.Blob = f[0], f[1]
		}
		out = append(out, r)
	}
	return out
}

// rebaseHead is the worktree's own REBASE_HEAD, read from its file in the git
// dir, or "" when there is none. rev-parse would fall back to a branch or tag
// of that name, which a --continue by hand leaves in place of the pseudoref.
func rebaseHead(wtPath string) string {
	path, err := git.Run(wtPath, "rev-parse", "--path-format=absolute", "--git-path", "REBASE_HEAD")
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	if len(head) < 40 || strings.Trim(head, "0123456789abcdef") != "" {
		return ""
	}
	return head
}
