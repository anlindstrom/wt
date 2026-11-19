package syncrun

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/wtsync"
)

// A branch a run gives no command, one with a merge commit of its own, is
// not promised one: the look says to move it by hand and nothing more.
func TestSideWherePromisesNoCommandForABranchMovedByHand(t *testing.T) {
	t.Parallel()
	s := &Stack{Branch: "top", Layers: []StackLayer{}}
	SetSides(s, []wtsync.SideBranch{{Branch: "m1", Kind: SideForked, Tip: "a", Fork: "b", Base: "b", Commits: 2, Merges: true,
		Left: "it has a merge commit (abc1234 merge), which a replay would drop: move it by hand"}}, nil, "")
	var out bytes.Buffer
	PrintLayers(&out, s)
	row := out.String()
	if !strings.Contains(row, "move it by hand") || strings.Contains(row, "prints the command") {
		t.Errorf("look %q, want it moved by hand and no command promised", row)
	}
}
