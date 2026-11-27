package wtsync

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three conflict shapes of the server's settings.gradle on one branch:
// trunk adds a blank line and a module line after the include line and edits
// the include line; the branch adds modules inside it (1), a line of its own
// (2) and one more module inside it (3). wt sync rebase finishes with no stop
// left for a person, and every module of both sides is in the result.
func TestRebaseListUnionResolvesTheServerGradleShapes(t *testing.T) {
	t.Parallel()
	read := func(dir, name string) string { return string(readFixture(t, dir, name)) }
	main := func(s string) string { // the include line, for an edit
		for _, l := range strings.Split(s, "\n") {
			if strings.HasPrefix(l, "include 'app'") {
				return l
			}
		}
		t.Fatal("no include line")
		return ""
	}
	base := read("inline_next_to_blank", "base")
	baseLine := main(base)
	trunk1 := read("inline_next_to_blank", "trunk") // adds blank + metrics
	trunk2 := strings.Replace(trunk1, "'device-client', 'gui'", "'device-client', 'trunk-extra', 'gui'", 1)
	b1 := read("inline_next_to_blank", "branch")
	b2 := b1 + "include 'devtools'\n"
	b3 := strings.Replace(b2, "'relay'", "'relay', 'sdk-client'", 1)

	dir := repoWith(t, map[string]string{"settings.gradle": base},
		[]map[string]string{{"settings.gradle": trunk1}, {"settings.gradle": trunk2}},
		[]map[string]string{{"settings.gradle": b1}, {"settings.gradle": b2}, {"settings.gradle": b3}})
	yaml := "conflicts:\n  - paths: [settings.gradle]\n    strategy: list-union\n    line: '^include '\n"
	if err := os.WriteFile(filepath.Join(dir, ".wt-sync.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "declare")
	gitIn(t, dir, "remote", "add", "origin", dir)
	gitIn(t, dir, "fetch", "-q", "origin")
	gitIn(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	cfg, err := LoadFromTrunk(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	wt := featureWorktree(t, dir).Path
	old := gitIn(t, wt, "rev-parse", "HEAD")

	var log bytes.Buffer
	res, err := Rebase(dir, cfg, trunkReq(wt, 7), &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if res.Restored || res.Replayed != 3 {
		t.Fatalf("result %+v\n%s", res, log.String())
	}
	for _, s := range res.Stops {
		for _, f := range s.Files {
			if !f.Resolved {
				t.Errorf("stop %d left %s for a person", s.Index, f.Path)
			}
		}
	}
	if ok, _ := RebaseInProgress(wt); ok {
		t.Fatal("rebase still in progress")
	}
	got, _ := os.ReadFile(filepath.Join(wt, "settings.gradle"))
	line := strings.Replace(baseLine, "'device-client', 'gui'", "'device-client', 'trunk-extra', 'gui'", 1)
	line = strings.TrimSuffix(line, "'main-api-cli'") + "'main-api-cli', 'bus-client', 'bus-cli', 'bus-sync', 'bus-integration-tests', 'sdk-client'"
	want := strings.Replace(trunk2, main(trunk2), line, 1) + "include 'devtools'\n"
	if string(got) != want {
		t.Errorf("settings.gradle:\n%s\nwant:\n%s", got, want)
	}
	if gitIn(t, dir, "rev-parse", "refs/wt-sync/feature/7") != old {
		t.Error("safety ref does not pin the old tip")
	}
	if st := gitIn(t, wt, "status", "--porcelain"); st != "" {
		t.Errorf("worktree not clean: %q", st)
	}
}
