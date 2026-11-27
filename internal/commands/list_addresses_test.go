package commands

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/devports"
	"github.com/anlindstrom/wt/internal/devports/devportstest"
)

func listWith(t *testing.T) (out string, wt string) {
	t.Helper()
	main, wt := testkit.RepoWithWorktree(t, func(parent string) string {
		return filepath.Join(parent, "demo_wt", "feat_wt", "thing")
	})
	ctx, err := kit.Open(main, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := List(ctx, ListOptions{NoPR: true, NoSessions: true}, &buf, 0); err != nil {
		t.Fatal(err)
	}
	return buf.String(), wt
}

func routeOf(url, name, checkout string) [4]string {
	return [4]string{url, name, checkout, strconv.Itoa(os.Getpid())}
}

func rowOf(t *testing.T, out, branch string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, branch) {
			return l
		}
	}
	t.Fatalf("no row for %s:\n%s", branch, out)
	return ""
}

// The listing is asked once, and matched to the worktree by its checkout.
func TestListShowsTheAddressOfAWorktreeWithALiveRoute(t *testing.T) {
	main, wt := testkit.RepoWithWorktree(t, func(parent string) string {
		return filepath.Join(parent, "demo_wt", "feat_wt", "thing")
	})
	f := &devportstest.Fake{Routes: devportstest.RouteDoc(
		routeOf("https://thing.web.example.localhost", "thing", wt),
		routeOf("https://thing.admin.example.localhost", "thing", wt))}
	f.Install(t)
	ctx, _ := kit.Open(main, io.Discard)
	var buf bytes.Buffer
	if err := List(ctx, ListOptions{NoPR: true, NoSessions: true}, &buf, 0); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "ADDRESS") {
		t.Errorf("no ADDRESS column:\n%s", out)
	}
	row := rowOf(t, out, "feat_wt/thing")
	if !strings.Contains(row, "https://thing.web.example.localhost") || !strings.Contains(row, "https://thing.admin.example.localhost") {
		t.Errorf("row lacks its addresses: %q", row)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "route list --json" {
		t.Errorf("devports was called %v", calls)
	}
}

// devports names a route by the folder's basename, so fix_wt/thing and
// feat_wt/thing share the name thing. The route belongs to the checkout it
// says, never to the one that happens to share its name.
func TestListDoesNotShowARouteOwnedByAnotherCheckoutOfTheSameName(t *testing.T) {
	main, _ := testkit.RepoWithWorktree(t, func(parent string) string {
		return filepath.Join(parent, "demo_wt", "feat_wt", "thing")
	})
	other := filepath.Join(t.TempDir(), "demo_wt", "fix_wt", "thing")
	(&devportstest.Fake{Routes: devportstest.RouteDoc(
		routeOf("https://thing.web.example.localhost", "thing", other))}).Install(t)
	ctx, _ := kit.Open(main, io.Discard)
	var buf bytes.Buffer
	if err := List(ctx, ListOptions{NoPR: true, NoSessions: true}, &buf, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "https://") || strings.Contains(buf.String(), "ADDRESS") {
		t.Errorf("another checkout's server shown here:\n%s", buf.String())
	}
}

func TestListDoesNotShowAStaleRoute(t *testing.T) {
	main, wt := testkit.RepoWithWorktree(t, func(parent string) string {
		return filepath.Join(parent, "demo_wt", "feat_wt", "thing")
	})
	stale := [4]string{"https://thing.web.example.localhost", "thing", wt, "2147483646"}
	(&devportstest.Fake{Routes: devportstest.RouteDoc(stale)}).Install(t)
	ctx, _ := kit.Open(main, io.Discard)
	var buf bytes.Buffer
	if err := List(ctx, ListOptions{NoPR: true, NoSessions: true}, &buf, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "https://") {
		t.Errorf("a route whose owner is gone:\n%s", buf.String())
	}
}

func TestListIsUnchangedWhenDevportsIsAbsentFailingSlowOrHasNoRoutes(t *testing.T) {
	was := devports.Deadline
	devports.Deadline = 200 * time.Millisecond
	t.Cleanup(func() { devports.Deadline = was })
	git, _ := exec.LookPath("git")
	for name, install := range map[string]func(*testing.T){
		"absent":    func(t *testing.T) { t.Setenv("PATH", filepath.Dir(git)) },
		"failing":   (&devportstest.Fake{Exit: 1}).Install,
		"slow":      (&devportstest.Fake{Sleep: 5, Routes: devportstest.RouteDoc()}).Install,
		"no routes": (&devportstest.Fake{Routes: devportstest.RouteDoc()}).Install,
	} {
		t.Run(name, func(t *testing.T) {
			install(t)
			out, _ := listWith(t)
			if strings.Contains(out, "ADDRESS") || strings.Contains(out, "https://") {
				t.Errorf("listing changed:\n%s", out)
			}
		})
	}
}

// A listing that is handed the routes does not ask devports itself: --all
// reads them once for every repository.
func TestListUsesTheRoutesItIsHanded(t *testing.T) {
	main, wt := testkit.RepoWithWorktree(t, func(parent string) string {
		return filepath.Join(parent, "demo_wt", "feat_wt", "thing")
	})
	f := &devportstest.Fake{Routes: devportstest.RouteDoc(routeOf("https://thing.web.example.localhost", "thing", wt))}
	f.Install(t)
	routes := devports.Take()
	f2 := &devportstest.Fake{Exit: 1}
	f2.Install(t)
	ctx, _ := kit.Open(main, io.Discard)
	var buf bytes.Buffer
	if err := List(ctx, ListOptions{NoPR: true, NoSessions: true, Routes: routes}, &buf, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "https://thing.web.example.localhost") {
		t.Errorf("the handed route is not shown:\n%s", buf.String())
	}
	if calls := f2.Calls(); len(calls) != 0 {
		t.Errorf("devports asked again: %v", calls)
	}
}

// The ADDRESS column is left out, with a line saying so, where it would make
// a row wider than the terminal; --wide and a pipe show it.
func TestListLeavesTheAddressColumnOutWhereItDoesNotFit(t *testing.T) {
	main, wt := testkit.RepoWithWorktree(t, func(parent string) string {
		return filepath.Join(parent, "demo_wt", "feat_wt", "thing")
	})
	(&devportstest.Fake{Routes: devportstest.RouteDoc(routeOf("https://thing.web.example.localhost", "thing", wt))}).Install(t)
	ctx, _ := kit.Open(main, io.Discard)
	list := func(opts ListOptions, width int) string {
		opts.NoPR, opts.NoSessions = true, true
		var buf bytes.Buffer
		if err := List(ctx, opts, &buf, width); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	narrow := list(ListOptions{}, 70)
	if strings.Contains(narrow, "https://") || strings.Contains(narrow, "ADDRESS") {
		t.Errorf("the column is there at 70 columns:\n%s", narrow)
	}
	if !strings.Contains(narrow, "addresses left out at this width — `wt list --wide` shows them") {
		t.Errorf("no line saying so:\n%s", narrow)
	}
	if out := list(ListOptions{Wide: true}, 70); !strings.Contains(out, "https://thing.web.example.localhost") || strings.Contains(out, "left out") {
		t.Errorf("--wide:\n%s", out)
	}
	if out := list(ListOptions{}, 400); !strings.Contains(out, "https://thing.web.example.localhost") || strings.Contains(out, "left out") {
		t.Errorf("400 columns:\n%s", out)
	}
}
