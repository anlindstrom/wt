package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

func runDoctorJSON(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := newRootCmd(args...)
	root.SetArgs(args)
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestDoctorJSONCleanAndBrokenRepo(t *testing.T) {
	testkit.UserConfigIn(t)
	clean := testkit.CommittedRepo(t, testkit.MinimalConf)

	out, errOut, err := runDoctorJSON(t, "doctor", "--repo", clean, "--json")
	if err != nil {
		t.Fatalf("clean doctor --json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if errOut != "" {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
	testkit.ValidateJSON(t, "doctor", []byte(out))
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if doc["schema"] != float64(1) || doc["command"] != "doctor" {
		t.Errorf("unexpected header: %#v", doc)
	}

	broken := testkit.FixtureRepo(t, "MAIN_BRANCH=\"main\"\nNO_SUCH_KEY=1\n")
	out, errOut, err = runDoctorJSON(t, "doctor", "--repo", broken, "--json")
	if err == nil || errorStatus(err) != 1 {
		t.Fatalf("broken doctor --json: err=%v (status %d), want exit 1\nstdout: %s", err, errorStatus(err), out)
	}
	if errOut != "" {
		t.Fatalf("unexpected stderr on broken doctor --json: %s", errOut)
	}
	testkit.ValidateJSON(t, "doctor", []byte(out))
	var brokenDoc struct {
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Owner  string `json:"owner"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &brokenDoc); err != nil {
		t.Fatal(err)
	}
	foundFail := false
	for _, c := range brokenDoc.Checks {
		if c.ID == "config.parse" && c.Status == "fail" && c.Owner == "wt" {
			foundFail = true
		}
	}
	if !foundFail {
		t.Errorf("expected config.parse fail check in:\n%s", out)
	}

	// wt sync doctor --json also handles broken worktree.conf leniently.
	out, errOut, err = runDoctorJSON(t, "sync", "doctor", "--repo", broken, "--json")
	if err == nil || errorStatus(err) != 1 {
		t.Fatalf("broken sync doctor --json: err=%v (status %d), want exit 1\nstdout: %s", err, errorStatus(err), out)
	}
	if errOut != "" {
		t.Fatalf("unexpected stderr on broken sync doctor --json: %s", errOut)
	}
	testkit.ValidateJSON(t, "doctor", []byte(out))
	if err := json.Unmarshal([]byte(out), &brokenDoc); err != nil {
		t.Fatal(err)
	}
	foundFail = false
	for _, c := range brokenDoc.Checks {
		if c.ID == "config.parse" && c.Status == "fail" && c.Owner == "wt" {
			foundFail = true
		}
	}
	if !foundFail {
		t.Errorf("expected config.parse fail check in sync doctor:\n%s", out)
	}
}

func TestDoctorAllJSONAcrossTwoRepos(t *testing.T) {
	testkit.UserConfigIn(t)
	root := testkit.RealTempDir(t)
	gittest.WithOrigin(t, testkit.ManagedRepo(t, root, "plain"))
	bad := testkit.ManagedRepo(t, root, "bad-sync")
	if err := os.WriteFile(filepath.Join(bad, ".wt-sync.yaml"), []byte("not_a_key: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, bad, "add", "-A")
	gittest.Git(t, bad, "commit", "-q", "-m", "bad declaration")
	gittest.WithOrigin(t, bad)
	t.Setenv("WT_ROOTS", root)

	out, errOut, err := runDoctorJSON(t, "doctor", "--all", "--json")
	if err == nil || errorStatus(err) != 1 {
		t.Fatalf("doctor --all --json: err=%v (status %d), want exit 1\nstdout: %s", err, errorStatus(err), out)
	}
	if errOut != "" {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
	testkit.ValidateJSON(t, "doctor", []byte(out))
	var doc struct {
		Repos []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Checks []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"repos"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Repos) != 2 {
		t.Fatalf("got %d repos, want 2:\n%s", len(doc.Repos), out)
	}
	foundSyncFail := false
	for _, r := range doc.Repos {
		if r.Name == "plain" {
			for _, c := range r.Checks {
				if strings.HasPrefix(c.ID, "sync.") || c.Status == "fail" {
					t.Errorf("plain declares no wt sync, got %s %s:\n%s", c.ID, c.Status, out)
				}
			}
		}
		if r.Name == "bad-sync" {
			for _, c := range r.Checks {
				if c.ID == "sync.declaration" && c.Status == "fail" {
					foundSyncFail = true
				}
			}
		}
	}
	if !foundSyncFail {
		t.Errorf("expected sync.declaration fail for bad-sync in:\n%s", out)
	}
}

// A repository whose trunk nobody can read says so as unknown, and unknown
// alone does not fail the command.
func TestDoctorAllJSONUnreadableDeclarationIsUnknown(t *testing.T) {
	testkit.UserConfigIn(t)
	root := testkit.RealTempDir(t)
	testkit.ManagedRepo(t, root, "no-origin")
	t.Setenv("WT_ROOTS", root)

	out, _, err := runDoctorJSON(t, "doctor", "--all", "--json")
	if err != nil {
		t.Fatalf("doctor --all --json: %v\n%s", err, out)
	}
	testkit.ValidateJSON(t, "doctor", []byte(out))
	var doc struct {
		Repos []struct {
			Checks []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"repos"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range doc.Repos {
		for _, c := range r.Checks {
			if strings.HasPrefix(c.ID, "sync.") {
				got = append(got, c.ID+" "+c.Status)
			}
		}
	}
	if want := "sync.declaration unknown"; strings.Join(got, ", ") != want {
		t.Errorf("sync records = %q, want %q:\n%s", got, want, out)
	}
}

func TestDoctorRepoUsageAndMissingRepository(t *testing.T) {
	testkit.UserConfigIn(t)
	_, _, err := runDoctorJSON(t, "doctor", "--repo", t.TempDir(), "--all")
	if err == nil || errorStatus(err) != 2 {
		t.Errorf("--repo with --all: err=%v (status %d), want usage error (2)", err, errorStatus(err))
	}
	for _, command := range [][]string{{"doctor"}, {"sync", "doctor"}} {
		out, _, err := runDoctorJSON(t, append(command, "--repo", t.TempDir(), "--json")...)
		if err == nil || errorStatus(err) != 1 {
			t.Errorf("%v on no repository: err=%v (status %d), want exit 1", command, err, errorStatus(err))
		}
		testkit.ValidateJSON(t, "doctor", []byte(out))
		if !strings.Contains(out, `"repo.open"`) {
			t.Errorf("%v on no repository: no repo.open record:\n%s", command, out)
		}
	}
}

func TestSyncDoctorJSONRefusesFixAndPrune(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, flag := range []string{"--fix", "--prune"} {
		_, _, err := runDoctorJSON(t, "sync", "doctor", "--json", flag)
		if err == nil || errorStatus(err) != 2 {
			t.Errorf("sync doctor --json %s: err=%v (status %d), want usage error (2)", flag, err, errorStatus(err))
		}
	}
}
