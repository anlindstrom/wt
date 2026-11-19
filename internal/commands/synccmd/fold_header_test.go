package synccmd

import (
	"bytes"
	"strings"
	"testing"
)

// The trunk's name is data, not a format: a legal ref such as main%s prints
// as it is, in the fold's header and in the plain one that replaces it.
func TestFoldHeaderPrintsATrunkNamedWithAFormatVerbAsItIs(t *testing.T) {
	t.Parallel()
	for name, print := range map[string]func(*RunPlan){
		"fold":  func(r *RunPlan) { r.printFoldHeader("tmp/x", "0123456789abcdef") },
		"plain": func(r *RunPlan) { r.flushFoldHeader() },
	} {
		var w bytes.Buffer
		r := &RunPlan{w: &w, opts: RunOptions{Stack: &StackOptions{Autosquash: true}},
			foldHeader: &foldHeader{label: "wt sync rebase", onto: "origin/main%s", trunkSHA: "abc1234", fetched: "(as last fetched)"}}
		print(r)
		if !strings.Contains(w.String(), "origin/main%s abc1234") || strings.Contains(w.String(), "%!") {
			t.Errorf("%s header: %q", name, w.String())
		}
	}
}
