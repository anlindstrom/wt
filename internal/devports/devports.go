// Package devports reads what devports, a separate and optional tool, knows
// about the dev servers of a repository's checkouts: the address a checkout's
// server gets, and which checkouts have a server running now. wt never adds
// or removes a route; every call here only reads, is bounded by Deadline, and
// answers nothing when devports is absent or does not answer.
package devports

import "time"

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

// Addresses is the address each app with a domain in dir's .dev-ports.yaml
// would get; nil when there are none or devports does not answer.
func Addresses(dir string) []Address { return nil }

// Live is the routes whose servers are running now; ok is false when devports
// is absent or did not answer.
func Live() (routes []Route, ok bool) { return nil, false }

// ByCheckout groups route URLs by the real path of the checkout they belong to.
func ByCheckout(routes []Route) map[string][]string { return nil }

// Snapshot is the live routes read once, to be asked about each worktree.
type Snapshot struct{ by map[string][]string }

// Take reads the live routes once.
func Take() *Snapshot { return nil }

// Of is the addresses routed to the checkout at path.
func (s *Snapshot) Of(path string) []string { return nil }
