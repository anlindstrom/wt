package syncrun

import "encoding/json"

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
