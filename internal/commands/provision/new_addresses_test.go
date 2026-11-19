package provision

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/devports"
	"github.com/anlindstrom/wt/internal/devports/devportstest"
)

// twoDomains is what a devports that reports domains says of two apps with one.
var twoDomains = devportstest.ConfigDoc(
	[2]string{"access-manager", "am.telcred.locally"},
	[2]string{"account-manager", "acm.telcred.locally"})

// repoWith is a repository to make a worktree in.
func repoWith(t *testing.T) *kit.Context {
	t.Helper()
	ctx, err := kit.Open(testkit.CommittedRepo(t, testkit.MinimalConf), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func newText(t *testing.T, ctx *kit.Context) (path, human string) {
	t.Helper()
	var buf bytes.Buffer
	path, err := New(ctx, "fix/sites-live", NewOptions{NoSetup: true}, &buf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return path, buf.String()
}

func TestNewPrintsTheAddressOfEachAppWithADomain(t *testing.T) {
	(&devportstest.Fake{Name: "sites-live", Config: twoDomains}).Install(t)
	_, human := newText(t, repoWith(t))
	for _, want := range []string{"https://sites-live.am.telcred.locally\n", "https://sites-live.acm.telcred.locally\n"} {
		if !strings.Contains(human, want) {
			t.Errorf("missing %q in:\n%s", want, human)
		}
	}
}

func TestNewPrintsNoAddressWithoutADomain(t *testing.T) {
	f := &devportstest.Fake{Name: "sites-live", Config: devportstest.ConfigDoc([2]string{"api", ""})}
	f.Install(t)
	_, human := newText(t, repoWith(t))
	if strings.Contains(human, "https://") {
		t.Errorf("an address with no domain:\n%s", human)
	}
}

// devports absent, failing or slow leaves the output of wt new as it was.
func TestNewIsUnchangedWhenDevportsIsAbsentFailingOrSlow(t *testing.T) {
	was := devports.Deadline
	devports.Deadline = 200 * time.Millisecond
	t.Cleanup(func() { devports.Deadline = was })
	_, plain := newText(t, repoWith(t))
	for name, install := range map[string]func(*testing.T){
		"absent":  func(t *testing.T) { t.Setenv("PATH", filepath.Dir(mustLookGit(t))) },
		"failing": (&devportstest.Fake{Name: "x", Config: twoDomains, Exit: 1}).Install,
		"slow":    (&devportstest.Fake{Name: "x", Config: twoDomains, Sleep: 5}).Install,
	} {
		t.Run(name, func(t *testing.T) {
			install(t)
			_, human := newText(t, repoWith(t))
			if strings.Contains(human, "https://") {
				t.Errorf("an address though devports is %s:\n%s", name, human)
			}
			if got, want := strings.Count(human, "\n"), strings.Count(plain, "\n"); got != want {
				t.Errorf("%d lines, %d without devports:\n%s", got, want, human)
			}
		})
	}
}

func TestNewPrintsNoAddressFromADevportsThatDoesNotReportDomains(t *testing.T) {
	(&devportstest.Fake{Name: "sites-live", Config: devportstest.OldConfigDoc("access-manager")}).Install(t)
	_, human := newText(t, repoWith(t))
	if strings.Contains(human, "https://") {
		t.Errorf("an address guessed:\n%s", human)
	}
}

func TestNewJSONHasTheAddresses(t *testing.T) {
	(&devportstest.Fake{Name: "sites-live", Config: twoDomains}).Install(t)
	var out, human bytes.Buffer
	err := NewJSON(repoWith(t), "fix/sites-live", NewOptions{NoSetup: true}, "", NewCreateJournal(&out, "new"), &human)
	if err != nil {
		t.Fatal(err)
	}
	testkit.ValidateJSON(t, "new", out.Bytes())
	var got struct {
		Addresses []devports.Address `json:"addresses"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []devports.Address{
		{App: "access-manager", URL: "https://sites-live.am.telcred.locally"},
		{App: "account-manager", URL: "https://sites-live.acm.telcred.locally"},
	}
	if !reflect.DeepEqual(got.Addresses, want) {
		t.Errorf("addresses %+v", got.Addresses)
	}
	if strings.Contains(human.String(), "https://") {
		t.Errorf("--json prints the addresses in its object, not as text:\n%s", human.String())
	}
}

func TestNewJSONHasNoAddressesAsAnEmptyArray(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(mustLookGit(t)))
	var out bytes.Buffer
	err := NewJSON(repoWith(t), "fix/x", NewOptions{NoSetup: true}, "", NewCreateJournal(&out, "new"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	testkit.ValidateJSON(t, "new", out.Bytes())
	if !strings.Contains(out.String(), `"addresses": []`) && !strings.Contains(out.String(), `"addresses":[]`) {
		t.Errorf("no empty addresses array:\n%s", out.String())
	}
}

func TestRefusedNewJSONHasEmptyAddresses(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(mustLookGit(t)))
	ctx := repoWith(t)
	var out bytes.Buffer
	_ = NewJSON(ctx, "nosuchtype/x", NewOptions{NoSetup: true}, "", NewCreateJournal(&out, "new"), io.Discard)
	testkit.ValidateJSON(t, "new", out.Bytes())
}

// mustLookGit is git's path, for a PATH that holds git and no devports.
func mustLookGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
