package wtsync

// The kinds of side branch, as SideBranch.Kind says it.
const (
	SideForked = "forked" // forks from a commit of the line below its top
	SideAbove  = "above"  // carries the line's top
)

// SideBranch is a local branch that hangs off a line without being on it.
type SideBranch struct {
	Branch, Tip, Kind string
	Fork, ForkSubject string
	Count             int
	On, Base          string
	Commits           int
	Backup            bool
	Left              string
}

// ReadSides reads the branches that hang off line.
func ReadSides(dir string, line Line, backups []string) ([]SideBranch, error) {
	return nil, nil
}

// ForkMapRef names the marker of fork n (from 1) in the run of epoch.
func ForkMapRef(epoch int64, n int) string { return "" }

// CheckMarker checks one marker against its plan.
func CheckMarker(dir, head, onto string, p LayerPlan) (tip, why string, err error) {
	return "", "not read", nil
}
