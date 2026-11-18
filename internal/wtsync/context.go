package wtsync

// ContextGap is one hunk of a fixup!, squash! or amend! commit that does not
// apply where autosquash puts it, right behind its target: the hunk's
// context only a later commit of the line adds.
type ContextGap struct {
	Fixup, FixupSubject   string
	Target, TargetSubject string
	// Path and Lines are the hunk: its file, and the lines it changes in
	// the fixup, "13" or "13-15".
	Path, Lines string
	// Needs is the first commit after the target, in line order, that
	// carries the hunk's context.
	Needs, NeedsSubject string
}

// Line is what a person is told about the gap.
func (g ContextGap) Line() string { return "" }

// ContextGaps is every hunk of the line's fixups that lacks its context at
// its target. commits is the line, oldest first.
func ContextGaps(dir string, commits []string) ([]ContextGap, []string, error) {
	return nil, nil, nil
}
