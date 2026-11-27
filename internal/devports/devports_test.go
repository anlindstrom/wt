package devports_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/devports"
	"github.com/anlindstrom/wt/internal/devports/devportstest"
)

// twoApps is what a devports that reports domains says of a repository with
// two apps that have one and one that has none.
var twoApps = devportstest.ConfigDoc(
	[2]string{"web-app", "web.example.localhost"},
	[2]string{"api", ""},
	[2]string{"admin-app", "admin.example.localhost"})

func TestAddressesOneForEachAppWithADomain(t *testing.T) {
	(&devportstest.Fake{Name: "login-fix", Config: twoApps}).Install(t)
	got := devports.Addresses(t.TempDir())
	want := []devports.Address{
		{App: "web-app", URL: "https://login-fix.web.example.localhost"},
		{App: "admin-app", URL: "https://login-fix.admin.example.localhost"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A devports from before it reported domains has no domain key to read, and
// wt does not read the file itself to make up for it.
func TestAddressesNoneFromADevportsThatDoesNotReportDomains(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".dev-ports.yaml"), []byte("apps:\n  web-app:\n    domain: web.example.localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &devportstest.Fake{Name: "x", Config: devportstest.OldConfigDoc("web-app")}
	f.Install(t)
	if got := devports.Addresses(dir); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestAddressesNoneWithoutADomain(t *testing.T) {
	f := &devportstest.Fake{Name: "x", Config: devportstest.ConfigDoc([2]string{"api", ""})}
	f.Install(t)
	if got := devports.Addresses(t.TempDir()); len(got) != 0 {
		t.Errorf("no domain: %+v", got)
	}
	for _, c := range f.Calls() {
		if c == "route name" {
			t.Errorf("asked for a name with nothing to put it on: %v", f.Calls())
		}
	}
}

func TestAddressesNoneWhenDevportsFailsOrSaysNothingReadable(t *testing.T) {
	for name, f := range map[string]*devportstest.Fake{
		"failing":   {Name: "x", Config: twoApps, Exit: 1},
		"no name":   {Config: twoApps},
		"no config": {Name: "x"},
		"garbled":   {Name: "x", Config: "not json"},
	} {
		t.Run(name, func(t *testing.T) {
			f.Install(t)
			if got := devports.Addresses(t.TempDir()); len(got) != 0 {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestAddressesNoneWhenDevportsIsAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := devports.Addresses(t.TempDir()); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestAddressesNoneWhenDevportsIsSlow(t *testing.T) {
	was := devports.Deadline
	devports.Deadline = 200 * time.Millisecond
	t.Cleanup(func() { devports.Deadline = was })
	(&devportstest.Fake{Name: "x", Config: twoApps, Sleep: 5}).Install(t)
	start := time.Now()
	if got := devports.Addresses(t.TempDir()); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("waited %v for a slow devports", d)
	}
}

// Two calls that each answer inside the deadline are still one wait: the
// deadline is for the whole of Addresses, so the two together never take
// longer than it.
func TestAddressesShareOneDeadlineAcrossTheirCalls(t *testing.T) {
	was := devports.Deadline
	devports.Deadline = 2 * time.Second
	t.Cleanup(func() { devports.Deadline = was })
	(&devportstest.Fake{Name: "x", Config: twoApps, Sleep: 1.5}).Install(t)
	start := time.Now()
	got := devports.Addresses(t.TempDir())
	if len(got) != 0 {
		t.Errorf("got %+v after waiting past the deadline", got)
	}
	if d := time.Since(start); d > 2600*time.Millisecond {
		t.Errorf("waited %v for a 2s deadline", d)
	}
}

func TestLiveReadsTheRoutesInOneCall(t *testing.T) {
	f := &devportstest.Fake{Routes: devportstest.RouteDoc(
		[4]string{"https://x.web.example.localhost", "x", "/r_wt/feat_wt/x", "1"})}
	f.Install(t)
	routes, ok := devports.Live()
	if !ok || len(routes) != 1 || routes[0].Checkout != "/r_wt/feat_wt/x" || routes[0].URL != "https://x.web.example.localhost" {
		t.Fatalf("ok %v routes %+v", ok, routes)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "route list --json" {
		t.Errorf("calls %v", calls)
	}
}

func TestLiveNotOkWhenAbsentFailingSlowOrGarbled(t *testing.T) {
	was := devports.Deadline
	devports.Deadline = 200 * time.Millisecond
	t.Cleanup(func() { devports.Deadline = was })
	t.Run("absent", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, ok := devports.Live(); ok {
			t.Error("ok")
		}
	})
	t.Run("failing", func(t *testing.T) {
		(&devportstest.Fake{Exit: 1}).Install(t)
		if _, ok := devports.Live(); ok {
			t.Error("ok")
		}
	})
	t.Run("slow", func(t *testing.T) {
		(&devportstest.Fake{Sleep: 5}).Install(t)
		if _, ok := devports.Live(); ok {
			t.Error("ok")
		}
	})
	t.Run("garbled", func(t *testing.T) {
		(&devportstest.Fake{Routes: "not json"}).Install(t)
		if _, ok := devports.Live(); ok {
			t.Error("ok")
		}
	})
}

func TestLiveLeavesOutARouteWhoseOwnerIsGone(t *testing.T) {
	(&devportstest.Fake{Routes: devportstest.RouteDoc(
		[4]string{"https://a.am.l", "a", "/a", "2147483646"},
		[4]string{"https://b.am.l", "b", "/b", itoa(os.Getpid())})}).Install(t)
	routes, ok := devports.Live()
	if !ok || len(routes) != 1 || routes[0].Name != "b" {
		t.Errorf("ok %v routes %+v", ok, routes)
	}
}

func TestByCheckoutGroupsByRealPath(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got := devports.ByCheckout([]devports.Route{
		{URL: "https://a", Checkout: link}, {URL: "https://b", Checkout: target}, {URL: "https://c"},
	})
	key, _ := filepath.EvalSymlinks(target)
	if want := []string{"https://a", "https://b"}; !reflect.DeepEqual(got[key], want) || len(got) != 1 {
		t.Errorf("got %+v", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
