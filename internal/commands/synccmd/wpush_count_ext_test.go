package synccmd_test

import (
	"strings"
	"testing"
)

// wt#217: the line a --no-push run ends with counts the branches
// wt sync push --stack will send. The line's own branch, which would be new on
// origin, goes only with --push-line, and the line says so.
func TestNoPushLineCountsOnlyWhatThePushSends(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	human := m.noPushRun(t)
	const want = "↪ 3 branches left unpushed, with their leases: wt sync push tmp/x --stack pushes them later; the line's own branch, tmp/x, goes with --push-line\n"
	if !strings.Contains(human, want) {
		t.Errorf("the closing line is not %q:\n%s", want, human)
	}
}
