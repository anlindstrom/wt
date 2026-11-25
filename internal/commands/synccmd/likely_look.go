package synccmd

import (
	"fmt"
	"io"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// printLikely is the commits after the first stop that change a file trunk
// changed too, grouped by file in replay order: a file that comes back at
// several commits is said so once, where a person resolving it can see it.
// They are likely, never certain: the look names the overlap, not a
// conflict, and git merges the edits that sit apart.
func printLikely(w io.Writer, indent string, likely []wtsync.LikelyStop) {
	if len(likely) == 0 {
		return
	}
	var files []string
	byFile := map[string][]wtsync.LikelyStop{}
	for _, l := range likely {
		for _, p := range l.Paths {
			if _, seen := byFile[p]; !seen {
				files = append(files, p)
			}
			byFile[p] = append(byFile[p], l)
		}
	}
	fmt.Fprintf(w, "%s⚠ likely to stop again, by overlap alone (git may still merge them): later commits that change a file trunk changed\n", indent)
	for _, p := range files {
		fmt.Fprintf(w, "%s  %s\n", indent, p)
		for _, l := range byFile[p] {
			fmt.Fprintf(w, "%s    %d/%d  %s\n", indent, l.Index, l.Total, kit.Truncate(kit.OneLine(l.Subject), 60))
		}
	}
}

// printReworked names each replayed commit that trunk carries under the same
// subject and author. A name match does not prove trunk holds the changes,
// so wt keeps the commit and replays it.
func printReworked(w io.Writer, indent string, reworked []wtsync.ReworkedCommit) {
	for _, r := range reworked {
		fmt.Fprintf(w, "%s⚠ %d/%d %q may already be on trunk as %s (same subject and author); wt keeps it and replays it, and it may stop where trunk reworked it\n",
			indent, r.Index, r.Total, kit.Truncate(kit.OneLine(r.Subject), 48), git.ShortID(r.TrunkCommit, 7))
	}
}
