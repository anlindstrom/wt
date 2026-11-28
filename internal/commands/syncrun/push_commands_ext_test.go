package syncrun_test

import (
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A printed command still names the work when pasted: quoted where a shell
// would read it, and behind -- where wt would read it as a flag.
func TestAPrintedPushCommandKeepsItsWorkName(t *testing.T) {
	t.Parallel()
	for work, want := range map[string]string{
		"login":        "wt sync push login --stack --push-line,",
		"=ls":          "wt sync push '=ls' --stack --push-line,",
		"review$draft": "wt sync push 'review$draft' --stack --push-line,",
		"-draft":       "wt sync push --stack --push-line -- -draft,",
	} {
		got := syncrun.LineLeftOutWhy(wtsync.BranchPush{Remote: "origin", RemoteBranch: "x"}, work)
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q in it", work, got, want)
		}
	}
}
