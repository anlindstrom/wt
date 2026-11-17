package wtsync

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "list_union", dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The three conflicts the server's settings.gradle raised in a stack rebase.
// Blobs are the server repo's own, trimmed to nothing: only
// inline_trunk_changed_line's trunk is a fixture of the same shape (trunk
// added 'nbix-cli' to the line the branch added 'acc-client' to).
func TestListUnionRealSettingsGradleConflicts(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"inline_next_to_blank", "own_line_next_to_blank", "inline_trunk_changed_line"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			c := Conflict{
				Path:   "settings.gradle",
				Base:   readFixture(t, dir, "base"),
				Trunk:  readFixture(t, dir, "trunk"),
				Branch: readFixture(t, dir, "branch"),
			}
			out, err := includes().Resolve(c)
			if err != nil {
				t.Fatal(err)
			}
			if want := readFixture(t, dir, "want"); string(out) != string(want) {
				t.Errorf("got:\n%s\nwant:\n%s", out, want)
			}
			// No module goes missing from any side.
			for _, side := range [][]byte{c.Trunk, c.Branch} {
				items, err := includes().items("settings.gradle", strings.Split(string(side), "\n"))
				if err != nil {
					t.Fatal(err)
				}
				for _, it := range items {
					if !strings.Contains(string(out), it) {
						t.Errorf("%s is missing from the result", it)
					}
				}
			}
		})
	}
}

func TestListUnionItemsStayUniqueAcrossTheResult(t *testing.T) {
	t.Parallel()
	// The branch's own line repeats an item trunk already has on another line.
	base := "include 'a'\n"
	trunk := "include 'a'\n\ninclude 'x'\n"
	branch := "include 'a'\ninclude 'x', 'y'\n"
	out, err := includes().Resolve(conflict(base, trunk, branch))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(out), "'x'"); got != 1 {
		t.Errorf("'x' appears %d times in %q", got, out)
	}
	if !strings.Contains(string(out), "'y'") {
		t.Errorf("'y' lost: %q", out)
	}
}

func TestListUnionCollapseKeepsTrunkLinesAndPlacesAdditions(t *testing.T) {
	t.Parallel()
	s := includes()
	cases := []struct {
		name string
		b    Block
		want string
	}{
		{"addition goes to the best matching trunk line, first on a tie",
			Block{Branch: []string{"include 'a', 'b', 'n'"}, Trunk: []string{"include 'x', 'y'", "", "include 'a', 'b'", "include 'b'"}},
			"include 'x', 'y'||include 'a', 'b', 'n'|include 'b'"},
		{"a tie goes to the first line",
			Block{Branch: []string{"include 'a', 'b', 'n'"}, Trunk: []string{"include 'a'", "include 'b'"}},
			"include 'a', 'n'|include 'b'"},
		{"a line sharing nothing is kept after trunk's lines",
			Block{Branch: []string{"include 'own'"}, Trunk: []string{"", "include 'm'"}},
			"|include 'm'|include 'own'"},
	}
	for _, tc := range cases {
		got, err := s.collapse("f", tc.b)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if strings.Join(got, "|") != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, strings.Join(got, "|"), tc.want)
		}
	}
}

func TestListUnionStillRefusesOtherLinesAndEmptySides(t *testing.T) {
	t.Parallel()
	s := ListUnion{Line: regexp.MustCompile(`^include `), Delimiter: ","}
	for name, b := range map[string]Block{
		"other line on trunk's side": {Branch: []string{"include 'a'"}, Trunk: []string{"", "rootProject.name = 'x'"}},
		"other line on the branch":   {Branch: []string{"include 'a'", "foo"}, Trunk: []string{"include 'b'"}},
		"empty branch side":          {Trunk: []string{"include 'b'"}},
		"empty trunk side":           {Branch: []string{"include 'b'"}},
		"only blanks on a side":      {Branch: []string{""}, Trunk: []string{"include 'b'"}},
	} {
		if _, err := s.collapse("f", b); !IsRefusal(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestListUnionNeverAddsAnItemTheFileAlreadyHasElsewhere(t *testing.T) {
	t.Parallel()
	// 'z' sits on an unchanged list line outside the block.
	out, err := includes().Resolve(conflict(
		"include 'a'\nkeep\ninclude 'z'\n",
		"include 'a', 'm'\nkeep\ninclude 'z'\n",
		"include 'a', 'z'\nkeep\ninclude 'z'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(out), "'z'"); got != 1 {
		t.Errorf("'z' appears %d times in %q", got, out)
	}
	// 'z' arrives on trunk's line in a later block.
	base := "include 'a'\n1\n2\n3\n4\n5\ninclude 'b'\n"
	trunk := "include 'a', 'm'\n1\n2\n3\n4\n5\ninclude 'b', 'z'\n"
	branch := "include 'a', 'z'\n1\n2\n3\n4\n5\ninclude 'b', 'q'\n"
	out, err = includes().Resolve(conflict(base, trunk, branch))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(out), "'z'"); got != 1 {
		t.Errorf("later block: 'z' appears %d times in %q", got, out)
	}
	for _, it := range []string{"'m'", "'q'"} {
		if !strings.Contains(string(out), it) {
			t.Errorf("%s lost: %q", it, out)
		}
	}
}

func TestListUnionAppendsWithoutBreakingTheTrunkLine(t *testing.T) {
	t.Parallel()
	s := includes()
	for _, tc := range []struct {
		name string
		b    Block
		want string
	}{
		{"a trunk line with no items yet",
			Block{Base: []string{"include "}, Branch: []string{"include 'a', 'b'"}, Trunk: []string{"include "}},
			"include 'a', 'b'"},
		{"a trunk line that already ends with the delimiter",
			Block{Base: []string{"include 'a',"}, Branch: []string{"include 'a', 'b'"}, Trunk: []string{"include 'a',"}},
			"include 'a', 'b'"},
	} {
		got, err := s.collapse("f", tc.b)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if strings.Join(got, "\n") != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestListUnionEditedBaseLineFallbackNeedsASingleBranchLine(t *testing.T) {
	t.Parallel()
	got, err := includes().collapse("f", Block{
		Base:   []string{"include 'a'"},
		Branch: []string{"include 'n'", "include 'a', 'b'"},
		Trunk:  []string{"include 'a', 'c'"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "include 'a', 'c', 'b'|include 'n'"; strings.Join(got, "|") != want {
		t.Errorf("got %q want %q", strings.Join(got, "|"), want)
	}
}
