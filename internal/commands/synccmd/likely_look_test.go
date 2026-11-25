package synccmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/wtsync"
)

// The look names every likely stop and every file: a long stack's planner
// needs each one, not a count of what was left out.
func TestPrintLikelyNamesEveryStopOfEveryFile(t *testing.T) {
	t.Parallel()
	var likely []wtsync.LikelyStop
	for i := 1; i <= 20; i++ {
		likely = append(likely, wtsync.LikelyStop{Index: i, Total: 20, Commit: fmt.Sprintf("%040d", i), Subject: fmt.Sprintf("commit %d", i),
			Paths: []string{"shared.txt", fmt.Sprintf("file%d.txt", i)}})
	}
	var out bytes.Buffer
	printLikely(&out, "", likely)
	for i := 1; i <= 20; i++ {
		for _, want := range []string{fmt.Sprintf("%d/20  commit %d\n", i, i), fmt.Sprintf("file%d.txt\n", i)} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q in\n%s", want, out.String())
			}
		}
	}
	if strings.Contains(out.String(), " more") {
		t.Errorf("the look counts what it leaves out:\n%s", out.String())
	}
}
