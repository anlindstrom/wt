package syncrun

import (
	"fmt"
	"io"

	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// droppedJSON is a list for --json: never nil, so a reader sees [] and not null.
func droppedJSON(ds []wtsync.Dropped) []DroppedJSON {
	out := []DroppedJSON{}
	for _, d := range ds {
		j := DroppedJSON{Commit: d.Commit, Subject: d.Subject, Unmatched: d.Unmatched}
		if d.Layer != "" {
			j.Layer = jsonout.Strp(d.Layer)
		}
		out = append(out, j)
	}
	return out
}

// setReplayReport records the commits a finished rebase emptied or left only
// tests of on its participant.
func setReplayReport(p *SyncParticipant, res wtsync.Result) {
	p.Emptied, p.TestsOnly = droppedJSON(res.Emptied), droppedJSON(res.TestsOnly)
}

// printReplayReport says, one line per commit, which commits a finished
// rebase emptied and which it left only tests of. It is a report: the rebase
// stands and nothing waits on it.
func printReplayReport(w io.Writer, res wtsync.Result) {
	line := func(d wtsync.Dropped, what, why string) {
		layer := ""
		if d.Layer != "" {
			layer = " [layer " + d.Layer + "]"
		}
		fmt.Fprintf(w, "  ⚠ %s: %s %s%s (%s)\n", what, git.ShortID(d.Commit, 7), d.Subject, layer, why)
	}
	for _, d := range res.Emptied {
		if d.Planned {
			line(d, "not confirmed folded", "it was planned to fold into its target and wt could not confirm that the rebase ran that fold; check the result for its change")
			continue
		}
		if d.Unmatched {
			line(d, "not found after the rebase", "wt cannot tell whether a commit of the result is this one: its author date or subject may have changed, or another commit shares them; check the result for it")
			continue
		}
		line(d, "dropped as empty", "trunk already has its change, or its conflict was resolved to trunk's side")
	}
	for _, d := range res.TestsOnly {
		line(d, "kept only its tests", "trunk already has the rest of its change")
	}
}
