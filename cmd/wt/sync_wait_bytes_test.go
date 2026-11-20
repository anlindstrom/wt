package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/runlog"
)

var durations = regexp.MustCompile(`\d+(\.\d+)?(ms|s|m)\b`)

// clocks are what a clock decides in the JSON: a safety ref's epoch in
// nanoseconds and the millisecond timings.
var clocks = regexp.MustCompile(`(refs/wt-sync/\S+?/)\d{10,}|("[a-zA-Z]*[mM]s": )\d+`)

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
				return normaliseRun(s, [2]string{onDir, "<repo>"}, [2]string{offDir, "<repo>"},
					[2]string{filepath.Dir(onPaths["feat/a"]), "<wts>"}, [2]string{filepath.Dir(offPaths["feat/a"]), "<wts>"})
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

// slowestBlock is the timing summary's list of the slowest runs in the text.
var slowestBlock = regexp.MustCompile(`(?m)^( +slowest:\n)((?: +<d>  .*\n)+)`)

// normaliseRun is what a clock or a repository decides in a run's output,
// made the same, and nothing else: paths, the durations, the safety ref's
// epoch, and the order of timing entries that tie (all took no time). Each
// entry keeps its kind, its run and where it ran.
func normaliseRun(s string, paths ...[2]string) string {
	for _, r := range paths {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		out, _ := json.MarshalIndent(normaliseJSON(v), "", "  ")
		return string(out)
	}
	s = clocks.ReplaceAllString(s, "${1}${2}<n>")
	s = durations.ReplaceAllString(s, "<d>")
	return slowestBlock.ReplaceAllStringFunc(s, func(block string) string {
		m := slowestBlock.FindStringSubmatch(block)
		lines := strings.SplitAfter(m[2], "\n")
		sort.Strings(lines)
		return m[1] + strings.Join(lines, "")
	})
}

// normaliseJSON zeroes the millisecond timings and the safety ref's epoch, and
// sorts each "slowest" list by its entries' content.
func normaliseJSON(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			switch {
			case strings.HasSuffix(strings.ToLower(k), "ms"):
				if _, ok := e.(float64); ok {
					v[k] = 0.0
					continue
				}
			case k == "slowest":
				if list, ok := e.([]any); ok {
					for i := range list {
						list[i] = normaliseJSON(list[i])
					}
					sort.Slice(list, func(i, j int) bool {
						a, _ := json.Marshal(list[i])
						b, _ := json.Marshal(list[j])
						return string(a) < string(b)
					})
					v[k] = list
					continue
				}
			}
			v[k] = normaliseJSON(e)
		}
		return v
	case []any:
		for i := range v {
			v[i] = normaliseJSON(v[i])
		}
		return v
	case string:
		return clocks.ReplaceAllString(v, "${1}${2}<n>")
	}
	return v
}

// The comparison sees a timing entry that is missing, and one that names
// another commit.
func TestTheComparisonSeesAChangedTimingEntry(t *testing.T) {
	text := "    slowest:\n      3s  check  make  at aacaf52 feat: a1.txt\n      2s  check  make  at 9f931f1 feat: a2.txt\n"
	for name, other := range map[string]string{
		"a missing entry": "    slowest:\n      3s  check  make  at aacaf52 feat: a1.txt\n",
		"another commit":  strings.Replace(text, "9f931f1 feat: a2.txt", "1111111 feat: other", 1),
		"another kind":    strings.Replace(text, "2s  check", "2s  step", 1),
	} {
		if normaliseRun(text) == normaliseRun(other) {
			t.Errorf("%s was not seen", name)
		}
	}
	json := `{"slowest": [{"kind": "check", "run": "make", "at": "aacaf52 feat: a1.txt", "ms": 3}, {"kind": "check", "run": "make", "at": "9f931f1 feat: a2.txt", "ms": 2}]}`
	if normaliseRun(json) == normaliseRun(strings.Replace(json, "9f931f1 feat: a2.txt", "1111111 feat: other", 1)) {
		t.Error("a changed JSON entry was not seen")
	}
	// Entries that tie are in no fixed order: that is not a difference.
	tied := "    slowest:\n      0s  check  make  at aacaf52 feat: a1.txt\n      0s  check  make  at 9f931f1 feat: a2.txt\n"
	swapped := "    slowest:\n      0s  check  make  at 9f931f1 feat: a2.txt\n      0s  check  make  at aacaf52 feat: a1.txt\n"
	if normaliseRun(tied) != normaliseRun(swapped) {
		t.Error("the order of tied entries counted as a difference")
	}
}
