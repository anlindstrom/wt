package commands

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A ref, a checkout or a worktree is moved by wtsync, behind its guards: the
// lock, the safety ref, the compare-and-swap on the old tip, the check that
// HEAD is still on the branch. The command packages decide and report; they
// do not write. This test turns that convention into a check: a non-test
// file under internal/commands that runs a git write fails it, unless the
// call is on the list below, which names each site (the file and its
// function), how many calls it holds and why each is guarded where it
// stands. A second write in a listed function fails too.
//
// A call whose arguments cannot be read as literals (a slice built
// elsewhere, args...) is unknown, and unknown is not passed: it fails unless
// it is listed with the kind "unread" and the reason it was reviewed.
//
// Reads are not looked at, and neither are the packages that exist for tests.
var gitWriteAllowlist = []struct {
	file, fn, kind string
	count          int
	why            string
}{
	{"syncrun/own_remote.go", "FastForwardOwn", "merge", 1, "merge --ff-only in the branch's own checkout, with the run's lock held, after repo.OnBranch confirmed HEAD is on the branch; git touches nothing when it cannot fast-forward"},
	{"syncrun/trunk_sync.go", "SyncLocalTrunk", "update-ref", 1, "trunk with no checkout: update-ref with the old value as the guard, only after trunkOwners found no checkout and no operation holding it"},
	{"syncrun/trunk_sync.go", "SyncLocalTrunk", "merge", 1, "trunk checked out in one worktree: merge --ff-only after repo.OnBranch confirmed that checkout is still on trunk"},
	{"refs/refs_sweep.go", "contain", "unread", 1, "git for-each-ref --merged with the refs as arguments: a read"},
	{"syncrun/own_remote.go", "FetchOwn", "unread", 1, "git fetch of the remote and its refspecs: writes remote-tracking refs, no local branch and no checkout"},
	{"syncrun/own_remote.go", "onRemote", "unread", 1, "git ls-remote --heads: a read"},
	{"syncrun/own_remote.go", "OwnCandidates", "unread", 1, "git for-each-ref with the patterns as arguments: a read"},
	{"syncrun/sync_push.go", "OfferPush", "unread", 1, "git push built by wtsync.Push.Args, force-with-lease and force-if-includes: writes the remote's ref only, no local branch and no checkout"},
	{"admin/pr.go", "checkoutPullRef", "checkout", 1, "git checkout <branch> in a worktree wt created a moment before, so it holds nothing to lose"},
}

// gitPath is the import the scan follows.
const gitPath = "github.com/anlindstrom/wt/internal/git"

// gitCalls are the functions of internal/git that run git with its
// arguments, and the index of the first of those arguments.
var gitCalls = map[string]int{"Run": 1, "RunTimeout": 2, "Exec": 1, "Lines": 1}

// flagged are the subcommands whose flag or sub-verb decides whether they
// write.
var flagged = []string{"branch", "worktree", "tag", "update-ref", "stash", "reset", "checkout", "switch", "rebase", "read-tree"}

// writeVerb is the git write a call's literal arguments name, "" for none.
func writeVerb(lits []string) string {
	has := func(opts ...string) bool {
		return slices.ContainsFunc(lits, func(l string) bool { return slices.Contains(opts, l) })
	}
	for _, l := range lits {
		switch l {
		case "update-ref", "reset", "checkout", "switch", "rebase", "read-tree", "stash", "merge", "cherry-pick", "restore", "clean":
			return l
		case "branch":
			if has("-f", "-D", "-d", "-M", "-m", "--force", "--delete") {
				return "branch"
			}
		case "tag":
			if has("-f", "-d", "-a", "-m", "--force", "--delete") {
				return "tag"
			}
		case "worktree":
			if has("remove", "move", "prune") {
				return "worktree"
			}
		}
	}
	return ""
}

// gitWrites is every git write, and every call it cannot read, in the
// non-test Go files under root, as "file function kind" with file relative to
// root, once per call.
func gitWrites(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == "testkit" || strings.HasSuffix(name, "test") || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		locals := map[string]bool{}
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == gitPath {
				local := "git"
				if imp.Name != nil {
					local = imp.Name.Name
				}
				locals[local] = true
			}
		}
		if len(locals) == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			var root ast.Node
			var fn string
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				root, fn = d.Body, d.Name.Name
			case *ast.GenDecl:
				// A package-level initializer runs at start-up: its site is
				// "var" and the names it declares.
				if d.Tok != token.VAR {
					continue
				}
				root, fn = d, "var"
				for _, sp := range d.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						fn += " " + n.Name
					}
				}
			default:
				continue
			}
			ast.Inspect(root, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				first, known := gitCalls[sel.Sel.Name]
				if !ok || !locals[pkg.Name] || !known {
					return true
				}
				if kind := callKind(call, first); kind != "" {
					found = append(found, rel+" "+fn+" "+kind)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// callKind is the write a git call makes ("update-ref", ...), "unread" when
// its arguments are not all there to read, "" for a read.
func callKind(call *ast.CallExpr, first int) string {
	if call.Ellipsis.IsValid() || len(call.Args) <= first {
		return "unread"
	}
	var lits []string
	computed := false
	for i, a := range call.Args[first:] {
		lit, ok := a.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			if i == 0 {
				return "unread" // the subcommand itself is not a literal
			}
			computed = true
			continue
		}
		if s, err := strconv.Unquote(lit.Value); err == nil {
			lits = append(lits, s)
		}
	}
	if verb := writeVerb(lits); verb != "" {
		return verb
	}
	// A subcommand whose write or read depends on a flag or sub-verb that is
	// not a literal here is unknown.
	if computed && slices.ContainsFunc(lits, func(l string) bool { return slices.Contains(flagged, l) }) {
		return "unread"
	}
	return ""
}

func TestCommandsWriteGitOnlyThroughTheAllowlist(t *testing.T) {
	t.Parallel()
	count := map[string]int{}
	for _, f := range gitWrites(t, ".") {
		count[f]++
	}
	listed := map[string]bool{}
	for _, a := range gitWriteAllowlist {
		key := a.file + " " + a.fn + " " + a.kind
		listed[key] = true
		if a.why == "" || a.count < 1 {
			t.Errorf("%s is listed without a reason or a count", key)
		}
		if count[key] != a.count {
			t.Errorf("%s: the list says %d, the code has %d: review the difference, then fix the list", key, a.count, count[key])
		}
	}
	for key, n := range count {
		if !listed[key] {
			t.Errorf("%s (%d): a git write, or a git call that cannot be read, in the command layer. Moves of refs, checkouts and worktrees go through wtsync, behind its guards; if this is guarded where it stands, list it in gitWriteAllowlist with the guard", key, n)
		}
	}
}

// The scan finds a write, a second write in a function, an aliased import
// and a call it cannot read, and does not take a read or a word for one.
func TestGitWritesFindsWhatItShould(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := `package x
import (
	g "github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/git"
)
func f(p string, args []string) {
	git.Run(p, "branch", "-D", "old")
	git.Run(p, "worktree", "remove", p)
	git.Run(p, "worktree", "remove", p)
	git.Run(p, "rev-parse", "HEAD")
	git.Run(p, "branch", "--show-current")
	other.Run(p, "reset", "--hard")
	count(3, "checkout")
	g.Run(p, "reset", "--hard")
	git.Run(p, args...)
	git.Exec(opts, cmd, "status")
	git.Run(p, "branch", flag, b)
	git.Run(p, "worktree", op, p)
	git.Run(p, "branch", "-D", b)
	git.Run(p, "tag", "-d", b)
	git.Run(p, "rev-parse", ref)
}

var _, _ = git.Run(".", "reset", "--hard")
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := gitWrites(t, dir)
	slices.Sort(got)
	want := []string{"x.go f branch", "x.go f branch", "x.go f reset", "x.go f tag", "x.go f unread", "x.go f unread", "x.go f unread", "x.go f unread", "x.go f worktree", "x.go f worktree", "x.go var _ _ reset"}
	if !slices.Equal(got, want) {
		t.Fatalf("found %v, want %v", got, want)
	}
}
