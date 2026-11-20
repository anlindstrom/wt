package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/runlog"
)

var durations = regexp.MustCompile(`\d+(\.\d+)?(ms|s|m)\b`)

// clocks are what a clock decides in the JSON: a safety ref's epoch in
// nanoseconds and the millisecond timings.
var clocks = regexp.MustCompile(`(refs/wt-sync/\S+?/)\d{10,}|("[a-zA-Z]*[mM]s": )\d+`)

// slowestAt is where the slowest list of the JSON puts each check: checks
// that all took no time are listed in no fixed order.
var slowestAt = regexp.MustCompile(`"at": "[^"]*"`)

// slowest is the lines of the timing summary's "slowest" list: checks that
// all took 0 s are listed in no fixed order, so they are left out of the
// comparison (the rest of the summary is compared).
var slowest = regexp.MustCompile(`(?m)^ +<d>  (check|step) .* at .*\n`)

// runIn runs wt in dir in this process with the copy on or off and returns
// stdout and stderr apart, and the log it wrote ("" when none).
func runIn(t *testing.T, dir string, off bool, args ...string) (stdout, stderr, log string) {
	t.Helper()
	t.Chdir(dir)
	noRunLog = off
	t.Cleanup(func() { noRunLog = false })
	var o, e bytes.Buffer
	root := newRootCmd(args...)
	root.SetOut(&o)
	root.SetErr(&e)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v\n%s%s", args, err, o.String(), e.String())
	}
	runlog.EndActive(0)
	logs, _ := filepath.Glob(filepath.Join(dir, ".git", runlog.LogsDir, "*.log"))
	if len(logs) > 0 {
		b, err := os.ReadFile(logs[0])
		if err != nil {
			t.Fatal(err)
		}
		log = string(b)
	}
	return o.String(), e.String(), log
}

// The person's output is the same bytes with the copy on and with it off, for
// a text run and for a --json run, and the log is what was shown.
func TestTheCopyLeavesTheOutputUntouched(t *testing.T) {
	t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
	gate := t.TempDir()
	t.Setenv("WTGATE", gate)
	for _, tc := range []struct {
		name  string
		extra []string
	}{{"text", nil}, {"json", []string{"--json"}}} {
		t.Run(tc.name, func(t *testing.T) {
			onDir, onPaths := twoBranches(t, failCheck)
			offDir, offPaths := twoBranches(t, failCheck)
			norm := func(s string) string {
				for _, r := range [][2]string{{onDir, "<repo>"}, {offDir, "<repo>"},
					{filepath.Dir(onPaths["feat/a"]), "<wts>"}, {filepath.Dir(offPaths["feat/a"]), "<wts>"}} {
					s = strings.ReplaceAll(s, r[0], r[1])
				}
				s = clocks.ReplaceAllString(s, "${1}${2}<n>")
				s = slowestAt.ReplaceAllString(s, `"at": "<at>"`)
				return slowest.ReplaceAllString(durations.ReplaceAllString(s, "<d>"), "")
			}
			args := append([]string{"sync", "rebase", "feat/a", "--yes", "--no-push", "--no-fetch"}, tc.extra...)
			onOut, onErr, log := runIn(t, onDir, false, args...)
			offOut, offErr, none := runIn(t, offDir, true, args...)
			if none != "" {
				t.Fatal("the copy was off and a log was written")
			}
			if onOut == "" && onErr == "" {
				t.Fatal("the run printed nothing")
			}
			if norm(onOut) != norm(offOut) {
				t.Errorf("stdout differs\non:\n%s\noff:\n%s", norm(onOut), norm(offOut))
			}
			if norm(onErr) != norm(offErr) {
				t.Errorf("stderr differs\non:\n%s\noff:\n%s", norm(onErr), norm(offErr))
			}
			want := onOut + onErr
			if tc.name == "json" {
				want = onErr
			}
			if tc.name == "text" && len(log) != len(want) {
				t.Errorf("the log is not what was shown\nlog:\n%s\nshown:\n%s", log, want)
			}
			if tc.name == "json" && log != want {
				t.Errorf("the log is not stderr\nlog:\n%s\nstderr:\n%s", log, want)
			}
		})
	}
}
