package wtsync

import "testing"

// A check is named by the base name of its command's first word; two checks
// that would read the same are told apart by their place in the declaration.
func TestCheckLabelsNameEachCheckAndTellTwinsApart(t *testing.T) {
	t.Parallel()
	cfg := &Config{EachCommit: []EachCommit{{Run: "bin/check.sh --fast"}, {Run: "test -f a"}, {Run: "test -f b"}, {Run: "./gradlew compile"}}}
	for i, want := range []string{"check.sh", "test (#2)", "test (#3)", "gradlew"} {
		if got := checkLabel(cfg, cfg.EachCommit[i]); got != want {
			t.Errorf("check %d: %q, want %q", i+1, got, want)
		}
	}
}
