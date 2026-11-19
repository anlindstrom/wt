package syncrun_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// interruptingWriter has a signal land while the summary is being written:
// the journal is asked to write its object from inside Write.
type interruptingWriter struct{ onWrite func() }

func (w interruptingWriter) Write(p []byte) (int, error) {
	w.onWrite()
	return len(p), nil
}

// A signal that lands while the summary is printed (a blocked terminal makes
// the window long) must still find the run's timings: the journal holds them
// before the record in flight is let go and before anything is printed.
func TestASignalWhileTheSummaryIsPrintedStillWritesTheTimings(t *testing.T) {
	t.Parallel()
	var out testkit.LockedBuffer
	j := syncrun.NewSyncRunJournal(&out, "sync rebase")
	j.Join("bump", "feat/bump", "/wt/bump", "abc", nil)
	rec := wtsync.NewTimings()
	rec.Add(wtsync.TimedCheck, "sleep 0.05", "abc1234 subject", 50*time.Millisecond)
	j.TrackTimings("feat/bump", rec)

	w := interruptingWriter{onWrite: func() { j.Interrupted("feat/bump", "interrupted") }}
	syncrun.ReportTimings(w, j, "feat/bump", rec, false)

	var r syncrun.RunResult
	if err := json.NewDecoder(strings.NewReader(out.String())).Decode(&r); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(r.Worktrees) != 1 || len(r.Worktrees[0].Timings.Runs) != 1 || r.Worktrees[0].Timings.Runs[0].Run != "sleep 0.05" {
		t.Errorf("timings %+v, want the check the run had finished", r.Worktrees[0].Timings)
	}
}
