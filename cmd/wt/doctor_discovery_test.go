package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
)

// gitThat puts before the real git on the PATH one that runs then instead
// for a command line containing words.
func gitThat(t *testing.T, words, then string) {
	t.Helper()
	found, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gittest.WriteExecutable(t, filepath.Join(dir, "git"), "#!/bin/sh\n"+
		"case \" $* \" in *\" "+words+" \"*) "+then+" ;; esac\n"+
		"exec '"+found+"' \"$@\"\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// doctorRecords runs a doctor with --json and gives its records, each as
// "id status", and their details.
func doctorRecords(t *testing.T, args ...string) (records []string, details string, status int) {
	t.Helper()
	out, _, err := runDoctorJSON(t, append(args, "--json")...)
	if err != nil {
		status = errorStatus(err)
	}
	testkit.ValidateJSON(t, "doctor", []byte(out))
	var doc struct {
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one object: %v\n%s", err, out)
	}
	for _, c := range doc.Checks {
		records = append(records, c.ID+" "+c.Status)
		details += c.Detail + "\n"
	}
	return records, details, status
}

// A git that fails to open the repository is a finding in git's own words,
// never a repository that is not there.
func TestDoctorJSONKeepsGitsDiscoveryError(t *testing.T) {
	testkit.UserConfigIn(t)
	repo := testkit.CommittedRepo(t, testkit.MinimalConf)
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[broken\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	for _, args := range [][]string{{"doctor"}, {"doctor", "--repo", repo}, {"sync", "doctor"}} {
		records, details, status := doctorRecords(t, args...)
		if got := strings.Join(records, ", "); got != "repo.open fail" || status != 1 {
			t.Errorf("%v: records %q, exit %d; want repo.open fail, exit 1", args, got, status)
		}
		if !strings.Contains(details, "bad config") {
			t.Errorf("%v: detail %q does not say what git said", args, details)
		}
	}
}

// A git that does not answer while the repository is opened is unknown.
func TestDoctorJSONUnansweredDiscoveryIsUnknown(t *testing.T) {
	testkit.UserConfigIn(t)
	repo := testkit.CommittedRepo(t, testkit.MinimalConf)
	t.Chdir(repo)
	gitThat(t, "worktree list", "exec sleep 30")
	was := git.GitTimeout
	git.GitTimeout = 2 * time.Second
	t.Cleanup(func() { git.GitTimeout = was })

	for _, args := range [][]string{{"doctor"}, {"doctor", "--repo", repo}, {"sync", "doctor"}} {
		records, _, status := doctorRecords(t, args...)
		if got := strings.Join(records, ", "); got != "repo.open unknown" || status != 0 {
			t.Errorf("%v: records %q, exit %d; want repo.open unknown, exit 0", args, got, status)
		}
	}
}

// A trunk git could not look up is unknown, not a branch that is missing.
func TestDoctorJSONUnreadableTrunkIsUnknown(t *testing.T) {
	testkit.UserConfigIn(t)
	repo := testkit.CommittedRepo(t, testkit.MinimalConf)
	gitThat(t, "show-ref", "echo 'fatal: cannot read refs' >&2; exit 128")

	records, _, status := doctorRecords(t, "doctor", "--repo", repo)
	got := strings.Join(records, ", ")
	if !strings.Contains(got, "config.trunk unknown") || status != 0 {
		t.Errorf("records %q, exit %d; want config.trunk unknown, exit 0", got, status)
	}
}
