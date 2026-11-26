package wtsync

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// twoHunks is a MoveLine whose source changes lines 2 and 10 of
// door/refuse.go, two hunks, and a function that commits a move! commit at
// its top and returns the line's commits.
func twoHunks(t *testing.T) (*gittest.MoveLine, func(body string) []string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	s := gittest.NewMoveLineShaped(t, dir, parent, "", gittest.MoveShape{TwoHunks: true})
	return s, func(body string) []string {
		gitIn(t, s.Path, "commit", "-q", "--no-verify", "--allow-empty", "-m", "move! "+gittest.MoveTarget, "-m", body)
		return strings.Fields(gitIn(t, dir, "rev-list", "--reverse", s.Base+".."+gittest.MoveTmp))
	}
}

func fileAt(t *testing.T, dir, rev, path string) string {
	t.Helper()
	return gitIn(t, dir, "cat-file", "-p", rev+":"+path)
}

// A range moves the hunks it overlaps and no other: the part has the source's
// change to those lines only.
func TestPlanMovesTakesTheHunksARangeCovers(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		ranges []string
		has    []string
		lacks  []string
	}{
		{name: "the first hunk", ranges: []string{"door/refuse.go:2-2"}, has: []string{"line 2, refused at the door"}, lacks: []string{"line 10, refused"}},
		{name: "the second hunk", ranges: []string{"door/refuse.go:10-10"}, has: []string{"line 10, refused"}, lacks: []string{"line 2, refused"}},
		{name: "both, by two ranges", ranges: []string{"door/refuse.go:1-3", "door/refuse.go:10-10"}, has: []string{"line 2, refused", "line 10, refused"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, move := twoHunks(t)
			moves, why, err := PlanMoves(s.Dir, move(gittest.MoveLinesBody(gittest.MoveSource, c.ranges...)))
			if err != nil || why != "" || len(moves) != 1 {
				t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
			}
			got := fileAt(t, s.Dir, moves[0].Part, gittest.MovePath)
			for _, w := range c.has {
				if !strings.Contains(got, w) {
					t.Errorf("the part lacks %q:\n%s", w, got)
				}
			}
			for _, w := range c.lacks {
				if strings.Contains(got, w) {
					t.Errorf("the part has %q:\n%s", w, got)
				}
			}
			if changed := gitIn(t, s.Dir, "diff-tree", "--no-commit-id", "--name-only", "-r", moves[0].Part); changed != gittest.MovePath {
				t.Errorf("the part changes %q, want only %s", changed, gittest.MovePath)
			}
			if !strings.Contains(moves[0].Line(), c.ranges[0]) {
				t.Errorf("the move's line %q does not name %s", moves[0].Line(), c.ranges[0])
			}
		})
	}
}

func TestPlanMovesRefusesLines(t *testing.T) {
	t.Parallel()
	src := gittest.MoveSource
	for _, c := range []struct {
		name, body string
		refuse     []string
	}{
		{name: "a range that splits a hunk", body: gittest.MoveLinesBody(src, "door/refuse.go:1-1"),
			refuse: []string{"door/refuse.go:1-1", "door/refuse.go:@@ -1,5 +1,5 @@", "whole"}},
		{name: "a range in no hunk", body: gittest.MoveLinesBody(src, "door/refuse.go:40-50"),
			refuse: []string{"door/refuse.go:40-50", "no hunk"}},
		{name: "a path both whole and by lines", body: gittest.MoveBody(src, "door/refuse.go") + "Move-Lines: door/refuse.go:2-2\n",
			refuse: []string{"door/refuse.go", "not both"}},
		{name: "every hunk of every path", body: gittest.MoveLinesBody(src, "door/refuse.go:1-10", "feature.txt:1-1"),
			refuse: []string{"every hunk of every path", "fixup!"}},
		{name: "every hunk, one path whole", body: gittest.MoveBody(src, "feature.txt") + "Move-Lines: door/refuse.go:1-10\n",
			refuse: []string{"every hunk of every path", "fixup!"}},
		{name: "a path the source does not change", body: gittest.MoveLinesBody(src, "other.txt:1-1"),
			refuse: []string{"does not change other.txt"}},
		{name: "a range with no end", body: gittest.MoveLinesBody(src, "door/refuse.go:2"),
			refuse: []string{"door/refuse.go:2", "<path>:<from>-<to>"}},
		{name: "a range backwards", body: gittest.MoveLinesBody(src, "door/refuse.go:5-2"),
			refuse: []string{"door/refuse.go:5-2", "<path>:<from>-<to>"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, move := twoHunks(t)
			moves, why, err := PlanMoves(s.Dir, move(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if why == "" {
				t.Fatalf("not refused: %+v", moves)
			}
			for _, w := range c.refuse {
				if !strings.Contains(why, w) {
					t.Errorf("refusal %q does not say %q", why, w)
				}
			}
		})
	}
}

// Two move! commits that take the same hunk are refused, naming the hunk.
func TestPlanMovesRefusesAHunkTwoMovesTake(t *testing.T) {
	t.Parallel()
	s, move := twoHunks(t)
	move(gittest.MoveLinesBody(gittest.MoveSource, "door/refuse.go:2-2"))
	commits := move(gittest.MoveLinesBody(gittest.MoveSource, "door/refuse.go:1-3"))
	moves, why, err := PlanMoves(s.Dir, commits)
	if err != nil || why == "" || !strings.Contains(why, "door/refuse.go:@@ -1,5 +1,5 @@") || !strings.Contains(why, "also") {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
}

// edits is a line on a base with f.txt (lines 1 to 20), g.txt and b.bin: the
// target "t", then the source "s", which change makes, and a move! commit of
// ranges at the top.
func edits(t *testing.T, change func(t *testing.T, dir string), ranges ...string) (string, []string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	commit := func(msg ...string) {
		gitIn(t, dir, "add", "-A")
		args := []string{"commit", "-q", "--no-verify", "--allow-empty"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		gitIn(t, dir, args...)
	}
	gittest.WriteFile(t, filepath.Join(dir, "f.txt"), countedLines(20, nil))
	gittest.WriteFile(t, filepath.Join(dir, "g.txt"), "g\n")
	gittest.WriteFile(t, filepath.Join(dir, "b.bin"), "b\x00\n")
	commit("base")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), "t\n")
	commit("t")
	change(t, dir)
	gittest.WriteFile(t, filepath.Join(dir, "s.txt"), "s\n")
	commit("s")
	commit("move! t", gittest.MoveLinesBody("s", ranges...))
	return dir, strings.Fields(gitIn(t, dir, "rev-list", "--reverse", base+"..HEAD"))
}

// countedLines is "line 1" to "line n", without the lines in drop.
func countedLines(n int, drop map[int]bool) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if !drop[i] {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	return b.String()
}

// A hunk with no added line is taken by a range that includes the line after
// the deletion in the source's version; a range of its context alone splits
// it.
func TestPlanMovesTakesADeletionByTheLineAfterIt(t *testing.T) {
	t.Parallel()
	change := func(t *testing.T, dir string) {
		gittest.WriteFile(t, filepath.Join(dir, "f.txt"), strings.Replace(countedLines(20, map[int]bool{15: true}), "line 2\n", "line two\n", 1))
	}
	dir, commits := edits(t, change, "f.txt:15-15")
	moves, why, err := PlanMoves(dir, commits)
	if err != nil || why != "" || len(moves) != 1 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
	if got, want := fileAt(t, dir, moves[0].Part, "f.txt"), strings.TrimRight(countedLines(20, map[int]bool{15: true}), "\n"); got != want {
		t.Errorf("the part has\n%s\nwant the deletion alone", got)
	}
	dir, commits = edits(t, change, "f.txt:14-14")
	if _, why, err := PlanMoves(dir, commits); err != nil || !strings.Contains(why, "f.txt:@@ -12,7 +12,6 @@") {
		t.Errorf("refused %q, err %v, want the hunk split", why, err)
	}
}

// A deletion at the end of a file, a deleted or emptied file and a binary
// file cannot be taken by lines: the refusal says to move the path whole.
func TestPlanMovesRefusesWhatLinesCannotName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, ranges string
		change       func(t *testing.T, dir string)
	}{
		{name: "a deletion at the end", ranges: "f.txt:19-19", change: func(t *testing.T, dir string) {
			gittest.WriteFile(t, filepath.Join(dir, "f.txt"), countedLines(19, nil))
		}},
		{name: "a deleted file", ranges: "g.txt:1-1", change: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "g.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an emptied file", ranges: "g.txt:1-1", change: func(t *testing.T, dir string) {
			gittest.WriteFile(t, filepath.Join(dir, "g.txt"), "")
		}},
		{name: "a binary file", ranges: "b.bin:1-1", change: func(t *testing.T, dir string) {
			gittest.WriteFile(t, filepath.Join(dir, "b.bin"), "c\x00\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir, commits := edits(t, c.change, c.ranges)
			moves, why, err := PlanMoves(dir, commits)
			path, _, _ := strings.Cut(c.ranges, ":")
			if err != nil || !strings.Contains(why, path) || !strings.Contains(why, "Move-Path") {
				t.Errorf("moves %+v, refused %q, err %v", moves, why, err)
			}
		})
	}
}

// Two move! commits may share a source: each hunk goes to its own target, and
// the source keeps only what neither takes. Together they may take all of it,
// which no single fixup! could say.
func TestPlanMovesTwoMovesOfOneSource(t *testing.T) {
	t.Parallel()
	s, move := twoHunks(t)
	move(gittest.MoveLinesBody(gittest.MoveSource, "door/refuse.go:2-2"))
	gitIn(t, s.Path, "commit", "-q", "--no-verify", "--allow-empty", "-m", "move! "+gittest.MoveBetween,
		"-m", gittest.MoveLinesBody(gittest.MoveSource, "door/refuse.go:10-10"))
	commits := strings.Fields(gitIn(t, s.Dir, "rev-list", "--reverse", s.Base+".."+gittest.MoveTmp))
	moves, why, err := PlanMoves(s.Dir, commits)
	if err != nil || why != "" || len(moves) != 2 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
	// The second part sits on the first: line 2 is already in layer 2 by then.
	for i, want := range []struct {
		target string
		has    []string
		lacks  []string
	}{
		{target: "l1", has: []string{"line 2, refused at the door"}, lacks: []string{"line 10, refused"}},
		{target: "l2", has: []string{"line 2, refused at the door", "line 10, refused"}},
	} {
		m := moves[i]
		if m.Target != s.Tips[want.target] {
			t.Errorf("move %d goes into %s, want %s's %s", i, m.Target, want.target, s.Tips[want.target])
		}
		if changed := gitIn(t, s.Dir, "diff-tree", "--name-only", "-r", m.Target, m.Part); changed != gittest.MovePath {
			t.Errorf("part %d differs from %s in %q, want only %s", i, want.target, changed, gittest.MovePath)
		}
		got := fileAt(t, s.Dir, m.Part, gittest.MovePath)
		for _, w := range want.has {
			if !strings.Contains(got, w) {
				t.Errorf("part %d lacks %q:\n%s", i, w, got)
			}
		}
		for _, w := range want.lacks {
			if strings.Contains(got, w) {
				t.Errorf("part %d has %q:\n%s", i, w, got)
			}
		}
	}

	commits = move(gittest.MoveBody(gittest.MoveSource, "feature.txt"))
	if moves, why, err := PlanMoves(s.Dir, commits); err != nil || why != "" || len(moves) != 3 {
		t.Errorf("moves %+v, refused %q, err %v, want all three", moves, why, err)
	}
}

// A change of a file's mode stays with the source when its lines move.
func TestPlanMovesLeavesTheModeWithTheSource(t *testing.T) {
	t.Parallel()
	dir, commits := edits(t, func(t *testing.T, dir string) {
		gittest.WriteFile(t, filepath.Join(dir, "f.txt"), strings.Replace(countedLines(20, nil), "line 2\n", "line two\n", 1))
		if err := os.Chmod(filepath.Join(dir, "f.txt"), 0o755); err != nil { //nolint:gosec // G302: the executable bit is what is tested
			t.Fatal(err)
		}
	}, "f.txt:2-2")
	moves, why, err := PlanMoves(dir, commits)
	if err != nil || why != "" || len(moves) != 1 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
	if got := gitIn(t, dir, "ls-tree", moves[0].Part, "f.txt"); !strings.HasPrefix(got, "100644 ") {
		t.Errorf("the part takes the mode: %s", got)
	}
	if got := fileAt(t, dir, moves[0].Part, "f.txt"); !strings.Contains(got, "line two") {
		t.Errorf("the part lacks the line:\n%s", got)
	}
}

// Lines the source adds twice in one file, and a part of them, go to the
// place the source has them: the move is made exactly or refused.
func TestPlanMovesRepeatedContextKeepsItsPlace(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	block := "a\nb\nc\nd\ne\nf\ng\nh\n"
	commit := func(msg ...string) {
		gitIn(t, dir, "add", "-A")
		args := []string{"commit", "-q", "--no-verify", "--allow-empty"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		gitIn(t, dir, args...)
	}
	gittest.WriteFile(t, filepath.Join(dir, "f.txt"), strings.Repeat(block, 4))
	commit("base")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	commit("t")
	// Four lines more in the first block, and one in the third: the third's
	// hunk starts four lines further down than it would alone.
	changed := strings.Replace(block, "d\n", "d\n1\n2\n3\n4\n5\n6\n7\n8\n", 1) + block + strings.Replace(block, "d\n", "d\nthird\n", 1) + block
	gittest.WriteFile(t, filepath.Join(dir, "f.txt"), changed)
	gittest.WriteFile(t, filepath.Join(dir, "s.txt"), "s\n")
	commit("s")
	at := 1 + slices.Index(strings.Split(changed, "\n"), "third")
	commit("move! t", gittest.MoveLinesBody("s", fmt.Sprintf("f.txt:%d-%d", at, at)))
	moves, why, err := PlanMoves(dir, strings.Fields(gitIn(t, dir, "rev-list", "--reverse", base+"..HEAD")))
	if err != nil || why != "" || len(moves) != 1 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
	want := strings.TrimRight(block+block+strings.Replace(block, "d\n", "d\nthird\n", 1)+block, "\n")
	if got := fileAt(t, dir, moves[0].Part, "f.txt"); got != want {
		t.Errorf("the part has\n%s\nwant the third block changed", got)
	}
}

// The hunks are read the same whatever the user's diff settings print: here
// an empty context line left without its leading space.
func TestPlanMovesReadsHunksUnderTheUsersDiffSettings(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	gitIn(t, dir, "config", "diff.suppressBlankEmpty", "true")
	commit := func(msg ...string) {
		gitIn(t, dir, "add", "-A")
		args := []string{"commit", "-q", "--no-verify", "--allow-empty"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		gitIn(t, dir, args...)
	}
	gittest.WriteFile(t, filepath.Join(dir, "f.txt"), "a\n\nb\n\nc\n")
	commit("base")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	commit("t")
	gittest.WriteFile(t, filepath.Join(dir, "f.txt"), "a\n\nB\n\nc\n")
	gittest.WriteFile(t, filepath.Join(dir, "s.txt"), "s\n")
	commit("s")
	commit("move! t", gittest.MoveLinesBody("s", "f.txt:3-3"))
	moves, why, err := PlanMoves(dir, strings.Fields(gitIn(t, dir, "rev-list", "--reverse", base+"..HEAD")))
	if err != nil || why != "" || len(moves) != 1 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
}

// linesLine is a line over f.txt: base, a commit "t" that leaves the file
// alone, between's content of it when not empty, the source "s" with source's,
// and a move! of ranges into t. It returns the repository and the line.
func linesLine(t *testing.T, base, between, source string, ranges ...string) (string, []string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	commit := func(content string, msg ...string) {
		if content != "" {
			gittest.WriteFile(t, filepath.Join(dir, "f.txt"), content)
		}
		gitIn(t, dir, "add", "-A")
		args := []string{"commit", "-q", "--no-verify", "--allow-empty"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		gitIn(t, dir, args...)
	}
	commit(base, "base")
	start := gitIn(t, dir, "rev-parse", "HEAD")
	gittest.WriteFile(t, filepath.Join(dir, "t.txt"), "t\n")
	commit("", "t")
	if between != "" {
		commit(between, "x")
	}
	gittest.WriteFile(t, filepath.Join(dir, "s.txt"), "s\n")
	commit(source, "s")
	commit("", "move! t", gittest.MoveLinesBody("s", ranges...))
	return dir, strings.Fields(gitIn(t, dir, "rev-list", "--reverse", start+"..HEAD"))
}

// exactFile is rev's path byte for byte.
func exactFile(t *testing.T, dir, rev, path string) string {
	t.Helper()
	out, err := runGit(dir, nil, nil, "cat-file", "blob", rev+":"+path)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A commit between adds lines above three identical blocks, so the source's
// line numbers point one block further down at the target. The hunk still
// goes to the block the source changed.
func TestPlanMovesPutsAHunkInTheBlockTheSourceChanged(t *testing.T) {
	t.Parallel()
	block := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\n"
	changed := strings.Replace(block, "d\n", "D\n", 1)
	prefix := countedLines(12, nil)
	dir, commits := linesLine(t, block+block+block, prefix+block+block+block, prefix+changed+changed+block, "f.txt:16-16")
	moves, why, err := PlanMoves(dir, commits)
	if err != nil || why != "" || len(moves) != 1 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
	if got, want := exactFile(t, dir, moves[0].Part, "f.txt"), changed+block+block; got != want {
		t.Errorf("the target gets\n%s\nwant the first block changed", got)
	}
	if got := gitIn(t, dir, "diff-tree", "--name-only", "-r", moves[0].Target, moves[0].Part); got != "f.txt" {
		t.Errorf("the target differs in %q, want only f.txt", got)
	}
}

// What the source keeps may diff differently once the part is below it: the
// move is judged by the content, not by how git words the rest.
func TestPlanMovesAcceptsARestGitAlignsAnotherWay(t *testing.T) {
	t.Parallel()
	lines := func(l ...string) string { return strings.Join(l, "\n") + "\n" }
	base := lines("a", "c", "d", "a", "a", "b", "b", "", "b")
	source := lines("", "d", "b", "d", "a", "c", "d", "a", "a", "b", "b", "c", "", "b", "", "a", "d", "b")
	dir, commits := linesLine(t, base, "", source, "f.txt:1-4")
	moves, why, err := PlanMoves(dir, commits)
	if err != nil || why != "" || len(moves) != 1 {
		t.Fatalf("moves %+v, refused %q, err %v", moves, why, err)
	}
	if got, want := exactFile(t, dir, moves[0].Part, "f.txt"), lines("", "d", "b", "d")+base; got != want {
		t.Errorf("the target gets\n%q\nwant\n%q", got, want)
	}
}
