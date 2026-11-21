package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// stashUses returns "file:line" for every string literal in the non-test Go
// files under root that is the git word "stash" (alone, or as the first word
// of a command line). In the file named by allowed, only the classifier's
// own comparison, `sub == "stash"`, is let through: any other use there is
// reported too.
func stashUses(t *testing.T, root, allowed string) []string {
	t.Helper()
	var found []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "vendor" || n == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		isAllowedFile := filepath.ToSlash(rel) == allowed
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		classifier := map[*ast.BasicLit]bool{}
		if isAllowedFile {
			ast.Inspect(file, func(n ast.Node) bool {
				if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.EQL {
					if x, ok := b.X.(*ast.Ident); ok && x.Name == "sub" {
						if lit, ok := b.Y.(*ast.BasicLit); ok {
							classifier[lit] = true
						}
					}
				}
				return true
			})
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && classifier[lit] {
				return true
			}
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if first, _, _ := strings.Cut(strings.TrimSpace(s), " "); first == "stash" {
				found = append(found, filepath.ToSlash(rel)+":"+strconv.Itoa(fset.Position(lit.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// wt never touches the stash: it is shared by every worktree of a repository
// and a stash entry is work nothing else pins. The scenario tests check that
// a run leaves the stash list as it was; this one catches a new git call that
// no scenario reaches. The reaper's classifier names "stash" to tell the
// read-only list and show from the rest, and is the only place that may.
func TestNoCodePassesStashToGit(t *testing.T) {
	t.Parallel()
	if got := stashUses(t, filepath.Join("..", ".."), "internal/git/reaper.go"); len(got) != 0 {
		t.Fatalf("non-test code names git's stash: %v", got)
	}
}

func TestTheStashScanSeesAStashCallItWasNotToldToAllow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := "package x\n\nfunc f(run func(...string)) { run(\"stash\", \"push\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stashUses(t, dir, "reaper.go"); len(got) != 1 || got[0] != "x.go:3" {
		t.Fatalf("got %v, want x.go:3", got)
	}
	if got := stashUses(t, dir, "x.go"); len(got) != 1 {
		t.Fatalf("a call in the allowed file must be reported too: %v", got)
	}
}

func TestTheStashScanOnlyLetsTheClassifierThrough(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := "package x\n\nfunc ro(sub string) bool {\n\tswitch {\n\tcase sub == \"stash\":\n\t\treturn true\n\t}\n\treturn false\n}\n\nfunc bad(run func(...string)) { run(\"stash\", \"push\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "reaper.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stashUses(t, dir, "reaper.go"); len(got) != 1 || got[0] != "reaper.go:11" {
		t.Fatalf("got %v, want only the call at reaper.go:11", got)
	}
}
