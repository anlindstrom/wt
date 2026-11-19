package wtsync

import (
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// What a timed run was.
const (
	TimedCheck = "check" // an each_commit step, at a commit, a tip or a recheck
	TimedStep  = "step"  // a per-layer step, at one layer
)

// slowestShown is how many of the slowest runs a summary names.
const slowestShown = 3

// Timed is one run of a check or a step: what ran, where, how long.
type Timed struct {
	Kind, Run string
	// At is where it ran: a commit's short id and subject, or a layer.
	At      string
	Elapsed time.Duration
}

// Timings is what a run (or a resume) ran and how long each took, filled as
// each check and step finishes. It is the one place the timing lives: the
// lines a run prints, its end summary and its JSON are all read from it. The
// zero value is not ready; use NewTimings. A nil *Timings records nothing.
type Timings struct {
	mu      sync.Mutex
	started time.Time
	entries []Timed
}

// NewTimings starts the clock of a run or a resume.
func NewTimings() *Timings { return &Timings{started: time.Now()} }

// Add records one finished run.
func (t *Timings) Add(kind, run, at string, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = append(t.entries, Timed{Kind: kind, Run: run, At: at, Elapsed: elapsed})
}

// TimingRow is every run of one check or step.
type TimingRow struct {
	Kind, Run string
	Count     int
	Total     time.Duration
}

// TimingSummary is a Timings read at one moment.
type TimingSummary struct {
	// Wall is the time since the clock started.
	Wall time.Duration
	// Rows is one per check and step, in the order each first ran.
	Rows []TimingRow
	// Slowest is the slowest runs of any, slowest first.
	Slowest []Timed
}

// Empty is whether nothing was timed.
func (s TimingSummary) Empty() bool { return len(s.Rows) == 0 }

// Summary reads the record now.
func (t *Timings) Summary() TimingSummary {
	var s TimingSummary
	if t == nil {
		return s
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s.Wall = time.Since(t.started)
	for _, e := range t.entries {
		i := slices.IndexFunc(s.Rows, func(r TimingRow) bool { return r.Kind == e.Kind && r.Run == e.Run })
		if i < 0 {
			s.Rows = append(s.Rows, TimingRow{Kind: e.Kind, Run: e.Run})
			i = len(s.Rows) - 1
		}
		s.Rows[i].Count++
		s.Rows[i].Total += e.Elapsed
	}
	s.Slowest = slices.Clone(t.entries)
	slices.SortStableFunc(s.Slowest, func(a, b Timed) int { return int(b.Elapsed - a.Elapsed) })
	if len(s.Slowest) > slowestShown {
		s.Slowest = s.Slowest[:slowestShown]
	}
	return s
}

// FormatDuration is how a duration is printed everywhere: whole seconds, as
// a deferred step's is.
func FormatDuration(d time.Duration) string { return d.Round(time.Second).String() }

// runTimed is runEachCommit, timed once: its run and its retry, if one ran,
// are one entry in rec, whichever caller asks, and elapsed is the time of
// both. at says where the check ran.
func runTimed(rec *Timings, at, wtPath string, s EachCommit, timeout time.Duration) (why, output string, retried bool, elapsed time.Duration) {
	start := time.Now()
	why, output, retried = runEachCommit(wtPath, s, timeout)
	elapsed = time.Since(start)
	rec.Add(TimedCheck, s.Run, at, elapsed)
	return why, output, retried, elapsed
}

// checkLabel names a check on the lines that report it: the base name of its
// command's first word. A check whose label another declared check shares is
// told apart by its place in the declaration.
func checkLabel(cfg *Config, s EachCommit) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s.Run), " ")
	label := path.Base(strings.Trim(first, `"'`))
	if cfg == nil {
		return label
	}
	at, twins := -1, 0
	for i, c := range cfg.EachCommit {
		f, _, _ := strings.Cut(strings.TrimSpace(c.Run), " ")
		if path.Base(strings.Trim(f, `"'`)) == label {
			twins++
		}
		if at < 0 && c.Run == s.Run {
			at = i
		}
	}
	if twins > 1 && at >= 0 {
		label += fmt.Sprintf(" (#%d)", at+1)
	}
	return label
}

// logCheck prints the first line of a check's result, with its duration: a
// pass names the check, a pass on retry and a failure carry the run.
func logCheck(w io.Writer, cfg *Config, s EachCommit, why string, retried bool, took time.Duration) {
	if w == nil {
		return
	}
	switch {
	case why != "":
		fmt.Fprintf(w, "  ✗ %s: %s  %s\n", s.Run, why, FormatDuration(took))
	case retried:
		fmt.Fprintf(w, "  ✓ %s passed on retry  %s\n", s.Run, FormatDuration(took))
	default:
		fmt.Fprintf(w, "  ✓ %s  %s\n", checkLabel(cfg, s), FormatDuration(took))
	}
}
