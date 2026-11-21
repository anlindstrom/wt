// Command checkskips refuses a skip in a *_test.go file that nothing explains.
//
//	go run ./internal/tools/checkskips [DIR]    # DIR defaults to the current directory
//
// A skip is a call X.Skip, X.Skipf or X.SkipNow on a testing type: a test that
// stops running without anyone noticing (a missing tzdata, a missing tool) is
// how a guard goes quiet. A skip is accepted when
//
//   - it sits in the body of an `if testing.Short() {` at any depth: the
//     documented way to leave out the slow tests (docs/testing.md), or
//   - a comment `//skip-ok: <reason>` is on the call's first line, the line
//     above it, or the line its closing parenthesis is on, and says why this
//     skip is there.
//
// Calls are found in the parsed syntax tree, so a comment or a string that
// contains the text of a skip never matches, and neither does a string that
// contains a reason comment. The package is the one imported as "testing"
// under whatever name; a local that shadows that name is not the package.
//
// There is no type-checker. X counts as a testing type when it is an
// identifier that the innermost enclosing function declares as a parameter of
// type *testing.T, *testing.B, *testing.F or testing.TB. A receiver the file
// does not show the type of (a struct field, a variable assigned from
// elsewhere) is not counted. A symlink given as DIR is resolved first.
//
// Exit 0: every skip is accepted. 1: one or more are not, one line each on
// stdout as path:line: text. 2: the call or the environment was wrong, or a
// file could not be read or parsed; nothing is then reported as checked.
// -h and --help print this text on stdout and exit 0.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const name = "checkskips"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(help)
		return
	}
	if len(args) > 1 {
		fmt.Fprintf(os.Stderr, "%s: at most one directory, got %d arguments\n", name, len(args))
		os.Exit(2)
	}
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	// G703 on both uses of dir: it is the command-line argument of a build
	// tool a developer runs on their own checkout.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil { //nolint:gosec // G703: see above
		dir = resolved
	}
	st, err := os.Stat(dir) //nolint:gosec // G703: see above
	if err != nil || !st.IsDir() {
		fmt.Fprintf(os.Stderr, "%s: not a directory: %s\n", name, dir)
		os.Exit(2)
	}
	found, err := scan(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(2)
	}
	for _, f := range found {
		fmt.Println(f)
	}
	if len(found) > 0 {
		fmt.Fprintf(os.Stderr, "%s: skips above are neither inside 'if testing.Short()' nor marked '//skip-ok: <reason>'\n", name)
		os.Exit(1)
	}
}

var help = func() string {
	// The doc comment above is the help: kept in one place.
	return `checkskips: refuse a skip in a *_test.go file that nothing explains.

  go run ./internal/tools/checkskips [DIR]    DIR defaults to the current directory

A skip (X.Skip, X.Skipf, X.SkipNow on a function parameter of type *testing.T,
*testing.B, *testing.F or testing.TB) is accepted when it is in the body of an
'if testing.Short() {', or when a comment '//skip-ok: <reason>' is on the
call's first line, the line above, or the line of its closing parenthesis.

Exit 0: all accepted. 1: some are not, one line each on stdout as path:line: text.
2: the call or the environment was wrong, or a file could not be read or parsed.
`
}()

// scan reports every unexplained skip under dir, sorted by path and line.
func scan(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error { //nolint:gosec // G703: see main
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if path != dir && (strings.HasPrefix(base, ".") || base == "testdata" || base == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	sort.Strings(files)
	var out []string
	for _, f := range files {
		found, err := checkFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

var skipMethods = map[string]bool{"Skip": true, "Skipf": true, "SkipNow": true}

// testingTypes are the types of package testing a test function receives.
var testingTypes = map[string]bool{"T": true, "B": true, "F": true, "TB": true}

func checkFile(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	local := testingName(f)
	if local == "" {
		return nil, nil
	}
	reasons := map[int]bool{} // lines that carry a //skip-ok: reason comment
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//skip-ok:") && strings.TrimSpace(strings.TrimPrefix(c.Text, "//skip-ok:")) != "" {
				reasons[fset.Position(c.Slash).Line] = true
			}
		}
	}
	w := &walker{fset: fset, path: path, local: local, reasons: reasons, out: new([]string)}
	ast.Walk(w, f)
	return *w.out, nil
}

// testingName is the name package testing is imported under in f: "testing",
// or its alias; "" when f does not import it (or only for its side effects).
func testingName(f *ast.File) string {
	for _, im := range f.Imports {
		if im.Path.Value != `"testing"` {
			continue
		}
		if im.Name == nil {
			return "testing"
		}
		if im.Name.Name == "_" || im.Name.Name == "." {
			return ""
		}
		return im.Name.Name
	}
	return ""
}

// walker visits a file keeping what the rules need to know about where it is:
// whether an `if testing.Short()` body encloses the node, and, per enclosing
// function, which parameter names are of a testing type.
type walker struct {
	fset    *token.FileSet
	path    string
	local   string
	reasons map[int]bool
	out     *[]string
	guarded bool
	funcs   []map[string]bool // innermost last: parameter name -> is a *testing.T/B/F or testing.TB
}

func (w *walker) with(guarded bool, funcs []map[string]bool) *walker {
	c := *w
	c.guarded = guarded
	c.funcs = funcs
	return &c
}

func (w *walker) Visit(n ast.Node) ast.Visitor {
	switch x := n.(type) {
	case *ast.FuncDecl:
		return w.with(w.guarded, append(w.funcs[:len(w.funcs):len(w.funcs)], w.params(x.Type)))
	case *ast.FuncLit:
		return w.with(w.guarded, append(w.funcs[:len(w.funcs):len(w.funcs)], w.params(x.Type)))
	case *ast.IfStmt:
		if w.isShortCall(x.Cond) {
			// The guard covers the body; the init, the condition and the else are not under it.
			if x.Init != nil {
				ast.Walk(w, x.Init)
			}
			ast.Walk(w, x.Cond)
			ast.Walk(w.with(true, w.funcs), x.Body)
			if x.Else != nil {
				ast.Walk(w, x.Else)
			}
			return nil
		}
	case *ast.CallExpr:
		w.call(x)
	}
	return w
}

// params maps each named parameter of a function to whether its type is one of
// package testing's (*testing.T, *testing.B, *testing.F, testing.TB).
func (w *walker) params(ft *ast.FuncType) map[string]bool {
	m := map[string]bool{}
	if ft.Params == nil {
		return m
	}
	for _, fld := range ft.Params.List {
		is := w.isTestingType(fld.Type)
		for _, nm := range fld.Names {
			m[nm.Name] = is
		}
	}
	return m
}

func (w *walker) isTestingType(e ast.Expr) bool {
	if st, ok := e.(*ast.StarExpr); ok {
		e = st.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || !testingTypes[sel.Sel.Name] {
		return false
	}
	return w.isTestingPkg(sel.X)
}

// isTestingPkg: e names the imported package testing, not a local that shadows it.
// Resolution is the parser's own: an identifier it could bind to a declaration
// in the file has an Obj; one that names an import has none.
func (w *walker) isTestingPkg(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == w.local && id.Obj == nil //nolint:staticcheck // SA1019: ast.Object is the parser's own scope resolution, enough to tell an import from a local; go/types would need every package loaded
}

// call reports a skip call that is neither guarded nor explained.
func (w *walker) call(x *ast.CallExpr) {
	fun := x.Fun
	for {
		p, ok := fun.(*ast.ParenExpr)
		if !ok {
			break
		}
		fun = p.X
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || !skipMethods[sel.Sel.Name] || w.guarded || !w.isTestingReceiver(sel.X) {
		return
	}
	start := w.fset.Position(x.Pos()).Line
	end := w.fset.Position(x.End()).Line
	if w.reasons[start] || w.reasons[start-1] || w.reasons[end] {
		return
	}
	*w.out = append(*w.out, fmt.Sprintf("%s:%d: %s.%s(...)", w.path, start, exprString(sel.X), sel.Sel.Name))
}

// isTestingReceiver: only a method of a testing type counts. The type of a
// receiver is not looked up (no type-checker); what counts is an identifier
// that the innermost enclosing function declares as a parameter of type
// *testing.T, *testing.B, *testing.F or testing.TB.
func (w *walker) isTestingReceiver(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	for i := len(w.funcs) - 1; i >= 0; i-- {
		if is, declared := w.funcs[i][id.Name]; declared {
			return is
		}
	}
	return false
}

// isShortCall: the expression is testing.Short() on the imported package,
// parentheses allowed.
func (w *walker) isShortCall(e ast.Expr) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Short" && w.isTestingPkg(sel.X)
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	}
	return "…"
}
