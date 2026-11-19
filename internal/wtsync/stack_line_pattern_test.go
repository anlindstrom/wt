package wtsync

import (
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

func layerNames(line Line) string {
	var out []string
	for _, l := range line.Layers {
		out = append(out, l.Branch)
	}
	return strings.Join(out, " ")
}

func skippedWhy(line Line, branch string) string {
	for _, k := range line.Skipped {
		if k.Branch == branch {
			return k.Why
		}
	}
	return ""
}

// A --layer value with * or ? takes every local branch it matches whose tip is
// on the line; one matched twice, by name and by pattern, is taken once.
func TestReadLinePatternTakesTheMatchedBranchesOnTheLine(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", []string{"s1", "s?"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := layerNames(line); got != "s1 s2 s3 s4 s5" {
		t.Errorf("layers %q, want the five, s1 once", got)
	}
	for _, l := range line.Layers {
		if !l.Named {
			t.Errorf("%s not marked named", l.Branch)
		}
	}
}

// A pattern never takes a backup name; it is listed with how to take it.
func TestReadLinePatternSkipsABackupName(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	gitIn(t, s.Dir, "branch", "safe/s3", s.Tips["s3"])
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", []string{"*3"}, []string{"safe/*"})
	if err != nil {
		t.Fatal(err)
	}
	if got := layerNames(line); got != "s3" {
		t.Errorf("layers %q, want s3 only", got)
	}
	if why := skippedWhy(line, "safe/s3"); why != "a backup name; name it with --layer to move it" {
		t.Errorf("skipped %+v", line.Skipped)
	}
	line, err = ReadLine(s.Dir, gittest.StackTmp, "origin/main", []string{"safe/s3"}, []string{"safe/*"})
	if err != nil || layerNames(line) != "safe/s3" {
		t.Errorf("a literal backup name must still be taken: %v %q", err, layerNames(line))
	}
}

// A pattern that takes no layer is refused, naming it.
func TestReadLinePatternThatTakesNothingIsRefused(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	gitIn(t, s.Dir, "branch", "else/x", s.Base)
	for _, p := range []string{"nothing/*", "else/*"} {
		_, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", []string{"s1", p}, nil)
		if err == nil || !strings.Contains(err.Error(), "--layer "+p) || !strings.Contains(err.Error(), "takes no layer") {
			t.Errorf("pattern %q: err %v, want it refused by name", p, err)
		}
	}
}

// A matched branch off the line is listed with why, not refused.
func TestReadLinePatternSkipsAMatchOffTheLine(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	gitIn(t, s.Dir, "branch", "s9", s.Base)
	line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", []string{"s?"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := layerNames(line); got != "s1 s2 s3 s4 s5" {
		t.Errorf("layers %q", got)
	}
	if why := skippedWhy(line, "s9"); !strings.Contains(why, "not on the line") {
		t.Errorf("skipped %+v", line.Skipped)
	}
}

// A branch named literally is a layer and never also listed as not taken,
// whichever flag comes first and whether a pattern matches it too.
func TestReadLineLiteralBackupNameIsNotAlsoSkipped(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	gitIn(t, s.Dir, "branch", "safe/s3", s.Tips["s3"])
	for _, layers := range [][]string{{"*3", "safe/s3"}, {"safe/s3", "*3"}} {
		line, err := ReadLine(s.Dir, gittest.StackTmp, "origin/main", layers, []string{"safe/*"})
		if err != nil {
			t.Fatal(err)
		}
		if got := layerNames(line); got != "s3 safe/s3" {
			t.Errorf("%v: layers %q, want s3 and safe/s3", layers, got)
		}
		if len(line.Skipped) != 0 {
			t.Errorf("%v: skipped %+v, want none", layers, line.Skipped)
		}
	}
}

// CheckLayerPatterns refuses a pattern that matches no local branch, with no
// line to read.
func TestCheckLayerPatternsRefusesAPatternMatchingNoBranch(t *testing.T) {
	t.Parallel()
	s := stackLine(t, "")
	if err := CheckLayerPatterns(s.Dir, []string{"s1", "s?"}); err != nil {
		t.Errorf("err %v, want a matching pattern and a name accepted", err)
	}
	err := CheckLayerPatterns(s.Dir, []string{"s1", "nothing/*"})
	if err == nil || !strings.Contains(err.Error(), "--layer nothing/*") {
		t.Errorf("err %v, want the pattern named", err)
	}
}
