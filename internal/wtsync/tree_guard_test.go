package wtsync

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// guardRepo is a base of two files (t.txt of twelve lines, gen/out.txt), an
// old line that changes t.txt's last line, and a target that changes its
// first: the two hunks are independent, so merging the line onto the target
// keeps both. With conflict the old line changes the first line too, so the
// merge of t.txt conflicts.
type guardRepo struct {
	dir                string
	base, onto, oldTip string
}

func tText(first, last string) string {
	return first + "\n" + strings.Repeat("mid\n", 10) + last + "\n"
}

func newGuardRepo(t *testing.T, conflict bool) guardRepo {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := guardRepo{dir: gittest.NewRepo(t, parent, "r")}
	commit := func(msg string, files map[string]string) string {
		for f, c := range files {
			gittest.WriteFile(t, filepath.Join(g.dir, f), c)
		}
		gittest.Git(t, g.dir, "add", "-A")
		gittest.Git(t, g.dir, "commit", "-q", "--allow-empty", "-m", msg)
		return gittest.Git(t, g.dir, "rev-parse", "HEAD")
	}
	g.base = commit("base", map[string]string{"t.txt": tText("first", "last"), "gen/out.txt": "gen0\n"})
	gittest.Git(t, g.dir, "checkout", "-q", "-b", "line")
	first := "first"
	if conflict {
		first = "mine"
	}
	g.oldTip = commit("feature", map[string]string{"t.txt": tText(first, "feature")})
	gittest.Git(t, g.dir, "checkout", "-q", "main")
	g.onto = commit("trunk", map[string]string{"t.txt": tText("trunk", "last")})
	return g
}

// rewritten commits files on top of at, detached, and returns the commit.
func (g guardRepo) rewritten(t *testing.T, at string, files map[string]string) string {
	t.Helper()
	gittest.Git(t, g.dir, "checkout", "-q", "--detach", at)
	for f, c := range files {
		gittest.WriteFile(t, filepath.Join(g.dir, f), c)
	}
	gittest.Git(t, g.dir, "add", "-A")
	gittest.Git(t, g.dir, "commit", "-q", "--allow-empty", "-m", "rewritten")
	return gittest.Git(t, g.dir, "rev-parse", "HEAD")
}

func TestTreeGuardTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		conflict bool
		// build returns the new tip, and what the guard is told besides the commits.
		build         func(t *testing.T, g guardRepo) (newTip string, in GuardInput)
		wantWhy       string // a substring of Why; "" for passed
		wantExcluded  []string
		wantConflicts []string
	}{
		{
			name: "a fold in place that keeps the tree passes",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.base, map[string]string{"t.txt": tText("first", "feature")}), GuardInput{Onto: g.base}
			},
		},
		{
			name: "a fold in place that changes a file outside the steps is refused",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.base, map[string]string{"t.txt": tText("first", "feature"), "extra.txt": "x\n"}), GuardInput{Onto: g.base}
			},
			wantWhy: "extra.txt among them",
		},
		{
			name: "a fold in place may change what a step's path names, and says which glob it let through",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.base, map[string]string{"t.txt": tText("first", "feature"), "gen/out.txt": "gen1\n"}), GuardInput{Onto: g.base, Folds: []string{"gen/**"}}
			},
			wantExcluded: []string{"gen/**"},
		},
		{
			name: "a fold in place that changes a step's path and another file is refused on the other",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.base, map[string]string{"t.txt": tText("changed", "feature"), "gen/out.txt": "gen1\n"}), GuardInput{Onto: g.base, Folds: []string{"gen/**"}}
			},
			wantWhy:      "t.txt among them",
			wantExcluded: nil,
		},
		{
			name: "a moving fold that keeps both hunks passes",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "feature")}), GuardInput{Onto: g.onto}
			},
		},
		{
			name: "a moving fold that loses the line's hunk in a file the target changed is refused",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "last")}), GuardInput{Onto: g.onto}
			},
			wantWhy: "t.txt differs from the old line merged onto the target",
		},
		{
			name: "a moving fold whose tree equals the old line's has lost the target's change and is refused",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.onto, map[string]string{"t.txt": tText("first", "feature")}), GuardInput{Onto: g.onto}
			},
			wantWhy: "t.txt differs",
		},
		{
			name:     "a file a stop resolved may differ in its conflicting region, and is named when it does",
			conflict: true,
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.onto, map[string]string{"t.txt": tText("mine", "feature")}), GuardInput{Onto: g.onto, Stopped: []string{"t.txt"}}
			},
			wantConflicts: []string{"t.txt"},
		},
		{
			name:     "a resolved file that lost a line of the merge outside the conflict is refused",
			conflict: true,
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.onto, map[string]string{"t.txt": tText("mine", "last")}), GuardInput{Onto: g.onto, Stopped: []string{"t.txt"}}
			},
			wantWhy: "of the merge is not in the resolution",
		},
		{
			name:     "a conflicted file no stop resolved has no allowance",
			conflict: true,
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.onto, map[string]string{"t.txt": tText("mine", "feature")}), GuardInput{Onto: g.onto}
			},
			wantWhy: "t.txt differs from the old line merged onto the target",
		},
		{
			name: "a step's path is the step's, even in a moving fold",
			build: func(t *testing.T, g guardRepo) (string, GuardInput) {
				return g.rewritten(t, g.onto, map[string]string{"t.txt": tText("trunk", "feature"), "gen/out.txt": "gen2\n"}), GuardInput{Onto: g.onto, Folds: []string{"gen/**"}}
			},
			wantExcluded: []string{"gen/**"},
		},
		{
			name: "a tip git cannot read is refused with the trees it could not compare",
			build: func(_ *testing.T, g guardRepo) (string, GuardInput) {
				return strings.Repeat("0", 40), GuardInput{Onto: g.base}
			},
			wantWhy: "tree ",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := newGuardRepo(t, c.conflict)
			newTip, in := c.build(t, g)
			in.Base, in.OldTip, in.NewTip = g.base, g.oldTip, newTip
			got := TreeGuard(g.dir, in)
			if (c.wantWhy == "") != (got.Why == "") || !strings.Contains(got.Why, c.wantWhy) {
				t.Fatalf("Why = %q, want it to contain %q", got.Why, c.wantWhy)
			}
			if c.wantWhy == "" {
				if !slices.Equal(got.Excluded, c.wantExcluded) || !slices.Equal(got.ConflictAllowed, c.wantConflicts) {
					t.Fatalf("excluded %v conflict-allowed %v, want %v %v", got.Excluded, got.ConflictAllowed, c.wantExcluded, c.wantConflicts)
				}
			}
		})
	}
}

func TestGlobMatchingIsTheFirstPatternThatMatches(t *testing.T) {
	t.Parallel()
	if got := GlobMatching([]string{"a/**", "gen/**", "gen/*.txt"}, "gen/out.txt"); got != "gen/**" {
		t.Fatalf("got %q", got)
	}
	if got := GlobMatching([]string{"a/**"}, "gen/out.txt"); got != "" {
		t.Fatalf("got %q", got)
	}
}
