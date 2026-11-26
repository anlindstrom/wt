// Package admin implements the commands that look after worktrees already
// made, and the repositories they are made in: wt doctor, migrate, adopt,
// pr, hook and repos.
package admin

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/provision"
	"github.com/anlindstrom/wt/internal/commands/supersetws"
	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/naming"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/superset"
)

// Doctor reports configuration and worktree health, returning the number of
// problems found so the caller can set an exit code. It replaces
// setup_precheck.sh and additionally finds worktrees that are misplaced,
// unowned, or nested inside the main checkout. It writes nothing.
func Doctor(ctx *kit.Context, w io.Writer) (int, error) {
	f := kit.NewFindings(w)
	err := doctor(ctx, f, true)
	return f.Problems(), err
}

// DoctorFindings is Doctor into f.
func DoctorFindings(ctx *kit.Context, f *kit.Findings) error {
	return doctor(ctx, f, true)
}

// DoctorJSON is Doctor as one JSON object. A nil ctx is outside every
// repository, where the roots and profiles are all there is to check.
func DoctorJSON(ctx *kit.Context, u *config.User, userErr error, w io.Writer) error {
	f := kit.NewFindings(io.Discard)
	if ctx == nil {
		doctorOutside(u, userErr, f)
	} else if err := doctor(ctx, f, true); err != nil {
		f.Add("worktrees.list", kit.StatusOf(err), err.Error())
	}
	return kit.WriteDoctorJSON(w, "doctor", f.Records(), nil)
}

// doctor is Doctor, with or without the roots and profiles: wt doctor --all
// checks those once for every repository, not once in each.
func doctor(ctx *kit.Context, f *kit.Findings, withRepos bool) error {
	f.Printf("Repository: %s (%s)\n", ctx.Repo.Name, ctx.Repo.MainRoot)

	// A broken configuration is a finding, not a reason to stop: diagnosing it
	// is the whole point of this command. Everything below is still checked
	// against defaults so one bad key does not hide the rest.
	f.Printf("Configuration:\n")
	if ctx.ConfigError != nil {
		kit.ConfigProblems(ctx, f)
		if errors.Is(ctx.ConfigError, config.ErrNoConfig) {
			f.Printf("  (checking the rest against wt's defaults)\n")
		} else {
			f.Printf("  (checking the rest against the values that did parse)\n")
		}
	} else if ctx.Config.Detected {
		// Not a problem: a repository wt was never set up in runs on what
		// `wt init` would write, and says so here and in `wt config`.
		f.Note("config.file", kit.CheckWarn, "no config file; trunk %s and default type %s are detected — `wt init` writes one",
			ctx.Config.MainBranch, ctx.Config.DefaultType).WithFix("wt init")
	} else {
		f.OK("config.parse", "trunk %s, default type %s",
			ctx.Config.MainBranch, ctx.Config.DefaultType)
		// Worth a line only where the two differ: branches named one way and
		// folders another is a surprise wt should own up to, not hide.
		if suffix, origin := ctx.BranchSuffix(); suffix != ctx.Config.TypeSuffix {
			f.OK("config.branchSuffix", "branches carry %q (%s); worktree paths keep %q",
				suffix, origin, ctx.Config.TypeSuffix)
		}
		if names, origin := ctx.TypeNames(); len(names) > 0 {
			f.OK("config.typeNames", "branches call %s (%s); worktree paths keep the type",
				strings.Join(provision.TypeNamePairs(ctx), " "), origin)
		}
	}
	if ctx.UserError != nil {
		f.Fail("config.user", "%v", ctx.UserError)
	}

	f.Printf("Required tools:\n")
	if len(ctx.Config.RequiredBins) == 0 {
		f.Unused("none declared")
	}
	for _, bin := range ctx.Config.RequiredBins {
		if _, err := exec.LookPath(bin); err != nil {
			f.Fail("tools."+bin, "%s is declared in REQUIRED_BINS but not on PATH", bin)
		} else {
			f.OK("tools."+bin, "%s", bin)
		}
	}

	// No trunk at all is reported once, above, as the configuration's problem.
	if trunk := ctx.Config.MainBranch; trunk != "" {
		switch found, err := ctx.Repo.LookupBranch(trunk); {
		case err != nil:
			f.Fail("config.trunk", "could not look up MAIN_BRANCH %q: %v", trunk, err).As(kit.CheckUnknown)
		case !found:
			f.Fail("config.trunk", "MAIN_BRANCH %q does not exist locally", trunk)
		}
	}

	doctorSuperset(ctx, f)
	kit.DoctorGitHub(ctx, f)
	kit.DoctorTimeMachine(ctx, f)

	f.Printf("Worktrees:\n")
	worktrees, err := ctx.Repo.Worktrees()
	if err != nil {
		return err
	}
	sch := ctx.Scheme()
	for _, wt := range worktrees {
		if wt.IsMain {
			continue
		}
		// A worktree inside the main checkout pollutes the parent repo and
		// breaks tooling that walks it.
		if repo.Inside(ctx.Repo.MainRoot, wt.Path, false) {
			f.Fail("worktrees.nested", "%s is inside the main checkout", wt.Path)
			continue
		}
		typ, work, layout, ok := sch.ClassifyBranch(wt.Path, wt.Branch)
		if !ok {
			// A branch wt did not name, at a path wt did: `wt checkout` and
			// `wt pr checkout` make these, and the directory names the work.
			if _, work, atPath := sch.ClassifyPath(wt.Path); atPath {
				f.OK("worktrees.layout", "%s (%s, on %q)", wt.Path, work, wt.Branch)
				continue
			}
			if sch.UnderT3(wt.Path) {
				f.OK("worktrees.layout", "%s (T3 Code's layout, on %q)", wt.Path, wt.Branch)
				continue
			}
			f.Note("worktrees.unmanaged", kit.CheckOK, "%s is on %q, not managed by wt", wt.Path, wt.Branch)
			continue
		}
		switch layout {
		case naming.Canonical:
			f.OK("worktrees.layout", "%s", wt.Path)
		case naming.Superset:
			// Not a problem: Superset puts every workspace here and stores the
			// absolute path, so the migrate this used to prescribe would have
			// broken the workspace it was aimed at.
			f.OK("worktrees.layout", "%s (Superset's layout)", wt.Path)
		case naming.T3:
			// Not a problem either: T3 Code puts every worktree under its
			// own directory and its thread holds the path.
			f.OK("worktrees.layout", "%s (T3 Code's layout)", wt.Path)
		default:
			migrate := fmt.Sprintf("wt migrate %s/%s", typ, work)
			f.Fail("worktrees.layout", "%s is not at its canonical path (%s); run: %s",
				wt.Path, sch.Dir(typ, work), migrate).WithFix(migrate)
		}
	}

	if withRepos {
		doctorRepos(ctx.UserConfig(), f)
	}

	if f.Problems() == 0 {
		f.Printf("No problems found.\n")
		return nil
	}
	f.Printf("%d problem(s) found.\n", f.Problems())
	switch {
	case errors.Is(ctx.ConfigError, config.ErrNoConfig):
		// Nothing above suggested a migrate: without a configuration there is
		// no canonical path to be off, so name the one command that helps.
		f.Printf("Run `wt init` to create it, then run this again.\n")
	case ctx.ConfigError != nil:
		// Mutating commands stay strict about configuration, so advising a
		// migrate that will refuse would send you round in a circle.
		f.Printf("Fix the configuration first — the migrate commands above need it.\n")
	}
	return nil
}

// doctorSuperset reports whether a new worktree here would reach the Superset
// desktop app, and where it would stop if it would not.
func doctorSuperset(ctx *kit.Context, f *kit.Findings) {
	f.Printf("Superset:\n")
	// Nothing to probe when the user has not opted in, and no repository
	// setting overrides that.
	if !ctx.UserConfig().Superset {
		f.Unused("off in your wt config; turn it on with `wt config set %s true`",
			config.UserKeySuperset)
		return
	}
	mode := ctx.Config.SupersetRegister
	if mode == config.SupersetOff {
		f.Unused("%s=off; new worktrees are not registered", config.KeySupersetRegister)
		return
	}
	// Under =auto an inactive Superset is a remark; under =on the repository
	// asked for it, so the same sentence is counted as a problem.
	note := func(id, format string, args ...any) *kit.CheckRecord {
		if mode == config.SupersetOn {
			return f.Fail(id, format, args...)
		}
		return f.Note(id, kit.CheckWarn, format, args...)
	}
	// unanswered is a finding that, for want of an answer in time, is not
	// known, whatever the line counts as.
	unanswered := func(r *kit.CheckRecord, err error) {
		if kit.Unanswered(err) {
			r.As(kit.CheckUnknown)
		}
	}

	status := supersetws.Status(ctx)
	if !status.Installed() {
		note("superset.cli", "no superset on the PATH or at ~/.superset/bin; registration is inactive")
		return
	}
	version := status.CLI().Version()
	asked := kit.CheckOK
	if version == "" {
		version, asked = "version unknown", kit.CheckUnknown
	}
	f.Add("superset.cli", asked, status.Exe+" ("+version+")")
	switch {
	case status.Err != nil:
		unanswered(note("superset.host", "%s (%s) would not say whether its host service is running: %v", status.Exe, version, status.Err), status.Err)
		return
	case !status.Running:
		r := note("superset.host", "%s (%s) is installed but its host service is not running; start the Superset app", status.Exe, version)
		if status.Unanswered {
			r.As(kit.CheckUnknown)
		}
		return
	}
	// WORKAROUND(superset-stale-status): the Stale line, and the note that
	// the workaround can go, which is never a problem.
	if status.Stale {
		// The host answered, so there is nothing for the person to start.
		f.OK("superset.host", "%s (%s), host service running; `superset status` reports a stale manifest for pid %d, which is Superset's bug — nothing to start",
			status.Exe, version, status.PID)
	} else {
		f.OK("superset.host", "%s (%s), host service running", status.Exe, version)
	}
	if status.StaleStatusFixed(supersetws.HostCommand) {
		f.Note("superset.staleStatus", kit.CheckOK, "`superset status` now reports this host correctly (running, pid %d, not a \"superset-host\" process); wt's stale-status workaround can be removed: grep wt's source for %s",
			status.PID, superset.StaleStatusMarker)
	}

	projects, err := status.CLI().Projects()
	if err != nil {
		unanswered(note("superset.project", "could not list Superset projects: %v", err), err)
		return
	}
	project, ok := superset.ProjectAt(projects, ctx.Repo.MainRoot)
	if !ok {
		note("superset.project", "no Superset project for %s; registration is inactive", ctx.Repo.MainRoot)
		return
	}
	f.OK("superset.project", "project %q — new worktrees are registered as workspaces", project.Name)
}
