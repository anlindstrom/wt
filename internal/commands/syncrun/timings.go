package syncrun

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/anlindstrom/wt/internal/wtsync"
)

// TimingsJSON is how long a run's each_commit checks and per-layer steps
// took, for --json: the run's wall time, what each check and step came to,
// and the slowest few runs. A resume's covers what that resume ran.
type TimingsJSON struct {
	// WallMs is the wall time of the run, or of the resume, in milliseconds.
	WallMs int64 `json:"wallMs"`
	// Runs is one row per check and per step that ran, in the order each first ran.
	Runs []TimingRunJSON `json:"runs"`
	// Slowest is the slowest runs of any check or step, slowest first.
	Slowest []TimingSlowJSON `json:"slowest"`
}

// TimingRunJSON is every run of one check or step.
type TimingRunJSON struct {
	// Kind is check for an each_commit step, step for a per-layer step.
	Kind string `json:"kind"`
	// Run is the step's run line from .wt-sync.yaml.
	Run string `json:"run"`
	// Count is how many times it ran, a retry counted with its run.
	Count int `json:"count"`
	// TotalMs is the time of all of them, in milliseconds.
	TotalMs int64 `json:"totalMs"`
}

// TimingSlowJSON is one slow run of a check or step.
type TimingSlowJSON struct {
	Kind string `json:"kind"`
	Run  string `json:"run"`
	// At is where it ran: a commit and its subject, or a layer and its branch.
	At string `json:"at"`
	Ms int64  `json:"ms"`
}

// MarshalJSON writes the lists as arrays, never null.
func (t TimingsJSON) MarshalJSON() ([]byte, error) {
	type plain TimingsJSON
	if t.Runs == nil {
		t.Runs = []TimingRunJSON{}
	}
	if t.Slowest == nil {
		t.Slowest = []TimingSlowJSON{}
	}
	return json.Marshal(plain(t))
}

// TimingsOf is the summary as --json reports it.
func TimingsOf(s wtsync.TimingSummary) TimingsJSON {
	j := TimingsJSON{WallMs: s.Wall.Milliseconds(), Runs: []TimingRunJSON{}, Slowest: []TimingSlowJSON{}}
	for _, r := range s.Rows {
		j.Runs = append(j.Runs, TimingRunJSON{Kind: r.Kind, Run: r.Run, Count: r.Count, TotalMs: r.Total.Milliseconds()})
	}
	for _, t := range s.Slowest {
		j.Slowest = append(j.Slowest, TimingSlowJSON{Kind: t.Kind, Run: t.Run, At: t.At, Ms: t.Elapsed.Milliseconds()})
	}
	return j
}

// PrintTimings tells where the time went: the wall time, each check and step
// with how many times it ran and its total, and the slowest runs. A resume
// says its numbers cover only what it ran. Nothing is printed when no check
// or step ran.
func PrintTimings(w io.Writer, s wtsync.TimingSummary, resumed bool) {
	if s.Empty() {
		return
	}
	if resumed {
		fmt.Fprintf(w, "  ⏱ this resume took %s (it covers only what the resume ran)\n", wtsync.FormatDuration(s.Wall))
	} else {
		fmt.Fprintf(w, "  ⏱ the run took %s\n", wtsync.FormatDuration(s.Wall))
	}
	for _, r := range s.Rows {
		fmt.Fprintf(w, "    %s  %s  ×%d  %s\n", r.Kind, r.Run, r.Count, wtsync.FormatDuration(r.Total))
	}
	fmt.Fprintf(w, "    slowest:\n")
	for _, t := range s.Slowest {
		fmt.Fprintf(w, "      %s  %s  %s  at %s\n", wtsync.FormatDuration(t.Elapsed), t.Kind, t.Run, t.At)
	}
}

// ReportTimings ends a run or a resume of branch: the summary on w, the same
// numbers in the journal. It reads rec once, so the two agree. The order
// matters to a signal: the journal gets the numbers first, then the record in
// flight is let go, and only then is anything printed, which may block.
func ReportTimings(w io.Writer, j *RunJournal, branch string, rec *wtsync.Timings, resumed bool) {
	s := rec.Summary()
	j.SetSync(branch, func(p *SyncParticipant) { p.Timings = TimingsOf(s) })
	j.TrackTimings(branch, nil)
	PrintTimings(w, s, resumed)
}

// TrackTimings makes rec the record of branch's rebase in flight, for a
// signal to read into the object; nil ends it. ReportTimings ends it too.
func (j *RunJournal) TrackTimings(branch string, rec *wtsync.Timings) {
	if j == nil {
		return
	}
	j.liveMu.Lock()
	defer j.liveMu.Unlock()
	if rec == nil {
		delete(j.live, branch)
		return
	}
	if j.live == nil {
		j.live = map[string]*wtsync.Timings{}
	}
	j.live[branch] = rec
}
