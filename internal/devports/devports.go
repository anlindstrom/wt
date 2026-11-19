// Package devports reads what devports, a separate and optional tool, knows
// about the dev servers of a repository's checkouts: the address a checkout's
// server gets, and which checkouts have a server running now. wt never adds
// or removes a route; every call here only reads, is bounded by Deadline, and
// answers nothing when devports is absent or does not answer.
package devports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Deadline bounds each call to devports.
var Deadline = 2 * time.Second

// Address is where one app of a checkout is reached when its dev server runs.
type Address struct {
	App string `json:"app"`
	URL string `json:"url"`
}

// Route is a live route as `devports route list --json` reports it.
type Route struct {
	URL      string
	Name     string
	Checkout string
	PID      int
}

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// run is devports with args in dir, until ctx is done. Its stdout, or an
// error when devports is not on the PATH, fails or is too slow.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath("devports")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: the binary is devports, the arguments are fixed
	cmd.Dir = dir
	// A killed devports must not keep the pipe, and with it this call, open
	// through a child of its own.
	cmd.WaitDelay = 100 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// domains is each app's domain as `devports config --json` reports it, run
// in dir, in the order it lists the apps. An app whose domain is null, or a
// devports that reports no domain at all, gives none: wt never reads
// .dev-ports.yaml to make up for one, because devports decodes it typed
// (anchors, merge keys, nulls) and wt would not.
func domains(ctx context.Context, dir string) []Address {
	out, err := run(ctx, dir, "config", "--json")
	if err != nil {
		return nil
	}
	var doc struct {
		Apps []struct {
			Name   string  `json:"name"`
			Domain *string `json:"domain"`
		} `json:"apps"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return nil
	}
	var apps []Address
	for _, a := range doc.Apps {
		if a.Domain != nil && *a.Domain != "" {
			apps = append(apps, Address{App: a.Name, URL: *a.Domain})
		}
	}
	return apps
}

// Addresses is the address each app with a domain, as devports reports them
// for the checkout at dir, would get; nil when there are none or devports does not answer. The name is
// devports' own (`devports route name`, run in dir), so the address is what
// devports will route when the server runs.
func Addresses(dir string) []Address {
	// One deadline for the calls together, so a wt new never waits longer.
	ctx, cancel := context.WithTimeout(context.Background(), Deadline)
	defer cancel()
	apps := domains(ctx, dir)
	if len(apps) == 0 {
		return nil
	}
	out, err := run(ctx, dir, "route", "name")
	name := strings.TrimSpace(string(out))
	if err != nil || !label.MatchString(name) {
		return nil
	}
	for i := range apps {
		apps[i].URL = "https://" + name + "." + apps[i].URL
	}
	return apps
}

// Live is the routes whose servers are running now; ok is false when devports
// is absent or did not answer. devports removes a route when its owner exits,
// on its next run; one whose owner is gone is left out here as well.
func Live() (routes []Route, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), Deadline)
	defer cancel()
	out, err := run(ctx, "", "route", "list", "--json")
	if err != nil {
		return nil, false
	}
	var doc struct {
		Routes []struct {
			URL      string  `json:"url"`
			Name     string  `json:"name"`
			Checkout *string `json:"checkout"`
			PID      int     `json:"pid"`
		} `json:"routes"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return nil, false
	}
	for _, r := range doc.Routes {
		if r.Checkout == nil || !alive(r.PID) {
			continue
		}
		routes = append(routes, Route{URL: r.URL, Name: r.Name, Checkout: *r.Checkout, PID: r.PID})
	}
	return routes, true
}

// alive is whether a process with this pid exists.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Snapshot is the live routes read once, to be asked about each worktree.
type Snapshot struct{ by map[string][]string }

// Take reads the live routes once, with one call to devports. The snapshot
// is empty, never nil, when devports is absent or did not answer, so that a
// caller holding one does not ask again: nil means not read yet.
func Take() *Snapshot {
	routes, _ := Live()
	return &Snapshot{by: ByCheckout(routes)}
}

// Of is the addresses routed to the checkout at path; none from a nil Snapshot.
func (s *Snapshot) Of(path string) []string {
	if s == nil {
		return nil
	}
	return s.by[RealPath(path)]
}

// ByCheckout groups route URLs by the real path of the checkout they belong
// to. A route is matched by its checkout and never by its name: two checkouts
// of one repository, feat_wt/x and fix_wt/x, share the name x.
func ByCheckout(routes []Route) map[string][]string {
	out := map[string][]string{}
	for _, r := range routes {
		if r.Checkout == "" {
			continue
		}
		k := RealPath(r.Checkout)
		out[k] = append(out[k], r.URL)
	}
	return out
}

// RealPath is p with its symlinks resolved, or p when that fails.
func RealPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
