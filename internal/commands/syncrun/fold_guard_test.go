package syncrun

import (
	"strings"
	"testing"
)

// A moving fold's pass line says it was compared with the old line merged
// onto the target, not with the old tip, and a failing check keeps its text.
func TestGuardPassedDescribesAMovingFoldInPlainWords(t *testing.T) {
	t.Parallel()
	dir, base, onto, oldTip := foldRepo(t)
	lines := func(first, last string) string { return first + "\n" + strings.Repeat("mid\n", 10) + last + "\n" }
	good := newLine(t, dir, onto, lines("trunk", "feature"))
	res := foldCheck(dir, base, onto, oldTip, good, []string{"gen/**"}, nil)
	if res.Why != "" || len(res.Excluded) != 0 || len(res.ConflictAllowed) != 0 {
		t.Fatalf("%+v", res)
	}
	g := &FoldGuard{OldTip: oldTip, NewTip: good, Onto: &onto}
	if got, want := guardPassed(g), "tip tree equal to the old line merged onto "+onto[:7]; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	g = &FoldGuard{OldTip: oldTip, NewTip: good, ExcludedGlobs: []string{"gen/**"}}
	if got, want := guardPassed(g), "tip tree equal to "+oldTip[:7]+" outside gen/**"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	lost := newLine(t, dir, onto, lines("trunk", "last"))
	if res := foldCheck(dir, base, onto, oldTip, lost, nil, nil); !strings.Contains(res.Why, "t.txt differs from the old line merged onto the target") {
		t.Errorf("a lost hunk: %q", res.Why)
	}
}
