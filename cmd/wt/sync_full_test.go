package main

import (
	"errors"
	"strings"
	"testing"
)

// --full widens what --json prints, so every verb that prints a run's result
// takes it, and none takes it alone.
func TestFullGoesWithJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, verb := range [][]string{
		{"sync", "rebase", "a"}, {"sync", "rebase", "--all"}, {"sync", "resume", "a"}, {"sync", "undo", "a"},
		{"sync", "steps", "a"}, {"sync", "push", "a", "--stack"},
	} {
		_, err := runCmd(t, append(append([]string{}, verb...), "--full")...)
		var u usageError
		if err == nil || !errors.As(err, &u) || !strings.Contains(err.Error(), "--full goes with --json") {
			t.Errorf("%v --full: %v", verb, err)
		}
	}
	for _, verb := range [][]string{
		{"sync", "rebase", "a"}, {"sync", "resume", "a"}, {"sync", "undo", "a"},
		{"sync", "steps", "a"}, {"sync", "push", "a", "--stack"},
	} {
		for want, flags := range map[string][]string{`"detail":"summary"`: {"--json"}, `"detail":"full"`: {"--json", "--full"}} {
			out, err := runCmd(t, append(append([]string{}, verb...), flags...)...)
			if err == nil || !strings.Contains(out, want) || !strings.Contains(out, `"outcome":"refused"`) {
				t.Errorf("%v %v outside a repository: want %s and the refusal, got %v\n%s", verb, flags, want, err, out)
			}
		}
	}
}
