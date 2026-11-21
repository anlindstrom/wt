package syncrun

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
	"github.com/anlindstrom/wt/internal/wtsync"
)

func tText(first, last string) string {
	return first + "\n" + strings.Repeat("mid\n", 10) + last + "\n"
}

// commitOn commits files on top of at, detached, and returns the commit.
func commitOn(t *testing.T, dir, at string, files map[string]string) string {
	t.Helper()
	gittest.Git(t, dir, "checkout", "-q", "--detach", at)
	for f, c := range files {
		gittest.WriteFile(t, filepath.Join(dir, f), c)
	}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "rewritten")
	return gittest.Git(t, dir, "rev-parse", "HEAD")
}

// fold.guard is a contract with other programs: the bytes it marshals to are
// fixed by what the tree guard found, whatever package computes it. The
// expected bytes are written out here, the reason included.
func TestGuardFoldRecordsTheGuardAsTheseExactBytes(t *testing.T) {
	t.Parallel()
	dir, base, onto, oldTip := foldRepo(t)
	good := newLine(t, dir, onto, tText("trunk", "feature"))
	lost := newLine(t, dir, onto, tText("trunk", "last"))
	inPlace := commitOn(t, dir, oldTip, map[string]string{"gen/out.txt": "generated\n"})
	tree7 := func(rev string) string { return gittest.Git(t, dir, "rev-parse", rev+"^{tree}")[:7] }

	// A conflicting merge, resolved at a stop with the merge's lines kept.
	cdir, cbase, conto, coldTip := conflictRepo(t)
	resolved := commitOn(t, cdir, conto, map[string]string{"t.txt": tText("mine", "feature")})

	cases := []struct {
		name         string
		dir          string
		base, onto   string
		old, new     string
		folds        []string
		stopped      []string
		fold         *StackFold
		wantWhy, out string
	}{
		{"moving, passed", dir, base, onto, oldTip, good, nil, nil, &StackFold{},
			"", `{"oldTip":"` + oldTip + `","newTip":"` + good + `","onto":"` + onto + `","excludedGlobs":[],"conflictAllowed":[],"outcome":"passed","reason":null}`},
		{"moving, failed", dir, base, onto, oldTip, lost, nil, nil, &StackFold{},
			"the fold changed the line's content", `{"oldTip":"` + oldTip + `","newTip":"` + lost + `","onto":"` + onto + `","excludedGlobs":[],"conflictAllowed":[],"outcome":"failed","reason":"tree ` +
				tree7(oldTip) + ` before, ` + tree7(lost) + ` after, t.txt differs from the old line merged onto the target"}`},
		{"in place, a step's path left out", dir, base, base, oldTip, inPlace, []string{"gen/**"}, nil, &StackFold{InPlace: true},
			"", `{"oldTip":"` + oldTip + `","newTip":"` + inPlace + `","onto":null,"excludedGlobs":["gen/**"],"conflictAllowed":[],"outcome":"passed","reason":null}`},
		{"moving, a conflicted file resolved at a stop", cdir, cbase, conto, coldTip, resolved, nil, []string{"t.txt"}, &StackFold{},
			"", `{"oldTip":"` + coldTip + `","newTip":"` + resolved + `","onto":"` + conto + `","excludedGlobs":[],"conflictAllowed":["t.txt"],"outcome":"passed","reason":null}`},
	}
	for _, c := range cases {
		f := &stackFinisher{w: io.Discard, main: c.dir, s: &Stack{Fold: c.fold},
			rec: &wtsync.StackRecord{Autosquash: true, Work: "w", Line: wtsync.Line{Base: c.base}},
			in:  StackFinish{Onto: c.onto, OldTip: c.old, NewTip: c.new, Folds: c.folds, Stopped: c.stopped}}
		why := f.guardFold()
		if (why == "") != (c.wantWhy == "") || !strings.Contains(why, c.wantWhy) {
			t.Fatalf("%s: why %q", c.name, why)
		}
		got, err := json.Marshal(f.s.Fold.Guard)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.out {
			t.Fatalf("%s:\n got %s\nwant %s", c.name, got, c.out)
		}
	}
}

// conflictRepo is foldRepo with the old line changing the first line too, so
// merging it onto the target conflicts in t.txt.
func conflictRepo(t *testing.T) (dir, base, onto, oldTip string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	commit := func(msg, content string) string {
		gittest.WriteFile(t, filepath.Join(dir, "t.txt"), content)
		gittest.Git(t, dir, "add", "-A")
		gittest.Git(t, dir, "commit", "-q", "-m", msg)
		return gittest.Git(t, dir, "rev-parse", "HEAD")
	}
	base = commit("base", tText("first", "last"))
	gittest.Git(t, dir, "checkout", "-q", "-b", "line")
	oldTip = commit("feature", tText("mine", "feature"))
	gittest.Git(t, dir, "checkout", "-q", "main")
	onto = commit("trunk", tText("trunk", "last"))
	return dir, base, onto, oldTip
}
