package wtsync

// Dropped is one commit a finished rebase set out to replay and is reported
// on: by its id before the rebase, its subject, and, for a stack held on one
// line, the layer it was in.
type Dropped struct {
	Commit  string
	Subject string
	Layer   string
	// Unmatched says the commit could not be told apart from another
	// commit of the same author, author date and subject, of which fewer
	// came out than went in.
	Unmatched bool
}

// ReplayReport is what a finished rebase did to the commits it replayed
// that its caller should be told of.
type ReplayReport struct {
	Emptied, TestsOnly []Dropped
}

// ReplayPlan is what a rebase driven by wt planned for the commits it
// replays, read from the list git started it with.
type ReplayPlan struct {
	// Folds are the full ids of the commits the list folds into another.
	Folds []string `json:"folds"`
	// Subjects is the subject a replayed commit carries where an amend!
	// commit changes it, by the commit's full id.
	Subjects map[string]string `json:"subjects,omitempty"`
	// Confirmed says wt drove the rebase from its start to its end, so the
	// folds were carried out as planned.
	Confirmed bool `json:"confirmed,omitempty"`
}

// ReplayInput is a finished rebase, for ReportReplay.
type ReplayInput struct {
	// Cutoff and Old are the commits the rebase set out to replay:
	// Cutoff...Old, the right side, merges left out. Onto and New are what
	// it came to: Onto..New.
	Cutoff, Old, Onto, New string
	// TestPaths are the globs of the test files; none reports no TestsOnly.
	TestPaths []string
	// Line is the stack held on the line, when it was; its layers name each
	// commit.
	Line *Line
	// Plan is what wt planned; nil when no record of it exists.
	Plan *ReplayPlan
	// Autosquash leaves out the fixup!, squash! and amend! commits: they are
	// meant to vanish into their target.
	Autosquash bool
	// Skip are full ids of commits to leave out: the one an --edit run
	// amends.
	Skip []string
}

// ReportReplay compares a finished rebase with the commits it set out to
// replay.
func ReportReplay(dir string, in ReplayInput) (ReplayReport, error) { return ReplayReport{}, nil }
