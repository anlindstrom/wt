package syncrun

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/wtsync"
)

// Each way a commit goes missing from a rebase keeps its own line, so that a
// commit wt cannot account for is never passed off as one it can.
func TestPrintReplayReportKeepsEachLossItsOwnLine(t *testing.T) {
	t.Parallel()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name string
		d    wtsync.Dropped
		want string
		not  []string
	}{
		{"unconfirmed fold", wtsync.Dropped{Commit: sha, Subject: "fixup! feat: a", Unmatched: true, Planned: true},
			"⚠ not confirmed folded: 0123456 fixup! feat: a (", []string{"not found after", "dropped as empty"}},
		{"certain empty drop", wtsync.Dropped{Commit: sha, Subject: "chore: bump", Layer: "s3"},
			"⚠ dropped as empty: 0123456 chore: bump [layer s3] (", []string{"not found after", "not confirmed folded"}},
		{"doubted vanish", wtsync.Dropped{Commit: sha, Subject: "feat: b", Unmatched: true},
			"⚠ not found after the rebase: 0123456 feat: b (", []string{"dropped as empty", "not confirmed folded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printReplayReport(&out, wtsync.Result{Emptied: []wtsync.Dropped{tc.d}})
			if !strings.Contains(out.String(), "  "+tc.want) {
				t.Errorf("printed %q, want it to contain %q", out.String(), tc.want)
			}
			for _, n := range tc.not {
				if strings.Contains(out.String(), n) {
					t.Errorf("printed %q, want no %q", out.String(), n)
				}
			}
		})
	}
}
