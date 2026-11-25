package wtsync

// PartMove is a move! commit of a line: the part of Source it names, whole
// paths, folded into Target, which lies below.
type PartMove struct {
	Commit, Subject       string
	Target, TargetSubject string
	Source, SourceSubject string
	Paths                 []string
	// Part is the commit the run folds into Target: Target's tree with the
	// part applied, Target its parent. Kept is the ref it is kept under.
	Part, Kept string
}

// PlanMoves is every move! commit among commits, the line oldest first.
func PlanMoves(dir string, commits []string) ([]PartMove, string, error) {
	return nil, "", nil
}
