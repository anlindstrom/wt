package admin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/reposel"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// doctorRepos checks the roots and profiles every multi-repository command
// reads: each root is a directory and none sits inside another, and each
// profile entry is still the main checkout of a repository wt manages, inside
// one of the roots.
func doctorRepos(u *config.User, f *kit.Findings) {
	home, _ := os.UserHomeDir()
	roots, origin := reposel.RootsFor(u)
	f.Printf("Repositories (roots from %s):\n", origin)
	for i, r := range roots {
		id := "roots." + r.Name
		info, err := os.Stat(r.Path)
		switch {
		case err != nil && origin == "default":
			// Nobody chose a default: a machine without that folder is an
			// ordinary machine, not a mistake to fix.
			f.Unused("root %s: %s is not there (a default)", r.Name, kit.AbbreviateHome(r.Path, home))
			continue
		case err != nil:
			f.Fail(id, "root %s: %s is not there", r.Name, kit.AbbreviateHome(r.Path, home))
			continue
		case !info.IsDir():
			f.Fail(id, "root %s: %s is not a directory", r.Name, kit.AbbreviateHome(r.Path, home))
			continue
		}
		// One root inside another lists its repositories twice over, and a
		// repository there has two roots to be selected by.
		nested := false
		for j, other := range roots {
			if i != j && repo.Inside(other.Path, r.Path, true) && !repo.SamePath(other.Path, r.Path) {
				f.Fail(id, "root %s: %s is inside root %s", r.Name, kit.AbbreviateHome(r.Path, home), other.Name)
				nested = true
			}
		}
		switch {
		case nested:
		case reposel.IsCheckout(r.Path) && !reposel.Managed(r.Path):
			f.Fail(id, "root %s: %s is one repository, and wt does not manage it (wt init there)", r.Name, kit.AbbreviateHome(r.Path, home))
		case reposel.IsCheckout(r.Path):
			f.OK(id, "root %s: %s (one repository)", r.Name, kit.AbbreviateHome(r.Path, home))
		default:
			n := len(reposel.Discover([]reposel.Root{r}).Repos)
			f.OK(id, "root %s: %s (%s)", r.Name, kit.AbbreviateHome(r.Path, home), kit.Count(n, "repository", "repositories"))
		}
	}
	for _, p := range u.Profiles {
		id := "profiles." + p.Name
		if len(p.Repos) == 0 {
			f.Fail(id, "profile %s names no repositories", p.Name)
			continue
		}
		for _, path := range p.Repos {
			t := reposel.ProfileTarget(path, roots)
			switch {
			case t.Problem != "":
				f.Fail(id, "profile %s: %s is %s", p.Name, path, t.Problem)
			case t.Root == "":
				f.Fail(id, "profile %s: %s is outside every root", p.Name, path)
			default:
				f.OK(id, "profile %s: %s (in %s)", p.Name, kit.AbbreviateHome(t.Path, home), t.Root)
			}
		}
	}
}

// DoctorRepositories is doctor from outside every repository: only the roots
// and profiles can be checked there.
func DoctorRepositories(u *config.User, userErr error, w io.Writer) int {
	f := kit.NewFindings(w)
	doctorOutside(u, userErr, f)
	if f.Problems() == 0 {
		f.Printf("No problems found.\n")
	} else {
		f.Printf("%d problem(s) found.\n", f.Problems())
	}
	return f.Problems()
}

func doctorOutside(u *config.User, userErr error, f *kit.Findings) {
	if userErr != nil {
		f.Fail("config.user", "%v", userErr)
	}
	doctorRepos(u, f)
}

// checkedRepo is what one repository of wt doctor --all came to: its lines
// in the text form, and its records.
type checkedRepo struct {
	problems, warnings int
	lines              []string
	records            []kit.CheckRecord
}

// checkRepo runs wt doctor in one repository, and wt sync doctor where trunk
// declares wt sync, a declaration that does not parse included.
func checkRepo(t reposel.Target) (c checkedRepo) {
	unopened := func(status, why string) checkedRepo {
		f := kit.NewFindings(io.Discard)
		f.Add("repo.open", status, why)
		return checkedRepo{problems: 1, lines: []string{"  ! " + kit.OneLine(why)}, records: f.Records()}
	}
	if t.Problem != "" {
		return unopened(kit.CheckFail, t.Problem)
	}
	ctx, err := kit.TryOpenLenient(t.Path, io.Discard)
	switch {
	case errors.Is(err, git.ErrNotRepo):
		return unopened(kit.CheckFail, "not a git repository")
	case err != nil:
		return unopened(kit.StatusOf(err), err.Error())
	}
	var buf bytes.Buffer
	f := kit.NewFindings(&buf)
	err = doctor(ctx, f, false)
	c.problems = f.Problems()
	defer func() { c.records = f.Records() }()
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "  ! ") {
			c.lines = append(c.lines, l)
		}
	}
	if err != nil {
		c.problems++
		c.lines = append(c.lines, "  ! "+err.Error())
		f.Add("worktrees.list", kit.StatusOf(err), err.Error())
	}
	if ctx.ConfigError != nil {
		return c
	}
	_, unreadable, err := wtsync.ReadDeclaration(ctx.Repo.MainRoot, ctx.Config.MainBranch)
	switch {
	case unreadable:
		f.Add("sync.declaration", kit.CheckUnknown, err.Error())
		return c
	case errors.Is(err, wtsync.ErrUndeclared):
		return c
	}
	buf.Reset()
	before := len(f.Records())
	if err := synccmd.SyncDoctor(ctx, synccmd.DoctorOptions{Findings: f}, &buf); err != nil {
		c.problems++
		c.lines = append(c.lines, "  ! sync: "+kit.OneLine(err.Error()))
		if len(f.Records()) == before {
			f.Add("sync.doctor", kit.StatusOf(err), err.Error())
		}
	}
	for _, l := range strings.Split(buf.String(), "\n") {
		if fields := strings.Fields(l); len(fields) >= 2 && fields[1] == "warn" {
			c.warnings++
			c.lines = append(c.lines, "  ~ sync "+fields[0]+": "+strings.Join(fields[2:], " "))
		}
	}
	return c
}

// DoctorAll is wt doctor across repositories: in every one a selection names,
// a few at a time, wt doctor and — where trunk declares wt sync — wt sync
// doctor, each repository a line with what they found under it; then the
// roots and profiles, once. It fails when either found a problem; a sync
// warning is advice, as it is in wt sync doctor.
func DoctorAll(u *config.User, sel reposel.Selection, w io.Writer) error {
	set, err := reposel.Select(u, sel)
	if err != nil {
		return err
	}
	results := reposel.Each(set.Repos, reposel.Parallelism, checkRepo)
	home, _ := os.UserHomeDir()
	width := 0
	for _, t := range set.Repos {
		width = max(width, len(t.Name))
	}
	total := 0
	for i, c := range results {
		t := set.Repos[i]
		var verdict []string
		if c.problems > 0 {
			verdict = append(verdict, fmt.Sprintf("%d problem(s)", c.problems))
		}
		if c.warnings > 0 {
			verdict = append(verdict, fmt.Sprintf("%d sync warning(s)", c.warnings))
		}
		if len(verdict) == 0 {
			verdict = []string{"✓"}
		}
		fmt.Fprintf(w, "%-*s  %-26s  %s\n", width, t.Name, strings.Join(verdict, ", "), kit.AbbreviateHome(t.Path, home))
		for _, l := range c.lines {
			fmt.Fprintln(w, l)
		}
		total += c.problems
	}
	fmt.Fprintln(w)
	f := kit.NewFindings(w)
	doctorRepos(u, f)
	total += f.Problems()
	if total > 0 {
		return fmt.Errorf("%d problem(s) found; wt doctor in a repository says more", total)
	}
	fmt.Fprintln(w, "No problems found.")
	return nil
}

// DoctorAllJSON is DoctorAll as one JSON object: each repository's records,
// and those of the roots and profiles beside them.
func DoctorAllJSON(u *config.User, userErr error, sel reposel.Selection, w io.Writer) error {
	f := kit.NewFindings(io.Discard)
	set, err := reposel.Select(u, sel)
	if err != nil {
		f.Add("repos.select", kit.CheckFail, err.Error())
	}
	repos := make([]kit.DoctorRepo, 0, len(set.Repos))
	for i, c := range reposel.Each(set.Repos, reposel.Parallelism, checkRepo) {
		repos = append(repos, kit.DoctorRepo{Name: set.Repos[i].Name, Path: set.Repos[i].Path, Checks: c.records})
	}
	// Each repository reports the user file; with none, it is said here.
	if len(repos) > 0 {
		userErr = nil
	}
	doctorOutside(u, userErr, f)
	return kit.WriteDoctorJSON(w, "doctor", f.Records(), repos)
}
