package wtsync

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

func numbered(n int, edit map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if s, ok := edit[i]; ok {
			fmt.Fprintln(&b, s)
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// A commit past the first stop that changes a file trunk changed is a likely
// stop; one on files trunk left alone is not. Git merges the likely one when
// the edits sit apart, which the replay above the stop shows.
func TestSimulateRebaseNamesTheCommitsAfterTheStopThatShareAFileWithTrunk(t *testing.T) {
	t.Parallel()
	dir := repoWith(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "f.txt": numbered(50, nil)},
		[]map[string]string{{"a.txt": "trunk\n", "f.txt": numbered(50, map[int]string{40: "trunk edit"})}},
		[]map[string]string{{"a.txt": "branch\n"}, {"f.txt": numbered(50, map[int]string{1: "branch edit"})}, {"b.txt": "b2\n"}})
	r, err := SimulateRebase(dir, "main", "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stop == nil || r.Stop.Index != 1 {
		t.Fatalf("stop %+v, want 1/3", r.Stop)
	}
	got := make([]string, 0, len(r.Likely))
	for _, l := range r.Likely {
		got = append(got, fmt.Sprintf("%d/%d %s %v", l.Index, l.Total, l.Subject, l.Paths))
	}
	if want := []string{"2/3 branch 2 [f.txt]"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("likely %v, want %v", got, want)
	}

	cutoff := gitIn(t, dir, "rev-parse", "feature~2")
	rest, err := SimulateRebaseTo(dir, Target{Onto: "main", Upstream: cutoff}, "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rest.Commits != 2 || rest.Stop != nil || len(rest.Stops) != 0 {
		t.Fatalf("the likely commit does not replay clean: %+v", rest)
	}
}

func TestSimulateRebaseHasNoLikelyStopsWithoutAPersonsStop(t *testing.T) {
	t.Parallel()
	dir := repoWith(t, map[string]string{"f.txt": numbered(50, nil)},
		[]map[string]string{{"f.txt": numbered(50, map[int]string{40: "trunk edit"})}},
		[]map[string]string{{"f.txt": numbered(50, map[int]string{1: "branch edit"})}})
	r, err := SimulateRebase(dir, "main", "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stop != nil || len(r.Likely) != 0 {
		t.Fatalf("replay %+v, want no stop and nothing likely", r)
	}
}

// commitOn checks out branch and commits content to path under subject, as
// author when one is given.
func commitOn(t *testing.T, dir, branch, path, content, subject, author string) {
	t.Helper()
	gitIn(t, dir, "checkout", "-q", branch)
	gittest.WriteFile(t, filepath.Join(dir, path), content)
	gitIn(t, dir, "add", "-A")
	args := []string{"commit", "-q", "-m", subject}
	if author != "" {
		args = append(args, "--author", author)
	}
	gitIn(t, dir, args...)
}

// Trunk's copy of a stack commit is a patch of its own, so git keeps the
// commit in the list; the replay names it and drops nothing. The same subject
// by another author is somebody else's commit.
func TestSimulateRebaseNamesACommitTrunkCarriesReworked(t *testing.T) {
	t.Parallel()
	dir := repoWith(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n"}, nil, nil)
	commitOn(t, dir, "main", "a.txt", "trunk rework\n", "fix: retire registrations", "")
	commitOn(t, dir, "main", "c.txt", "trunk c\n", "feat: other", "Someone Else <else@example.com>")
	commitOn(t, dir, "feature", "a.txt", "stack\n", "fix: retire registrations", "")
	commitOn(t, dir, "feature", "b.txt", "b2\n", "chore: own", "")
	commitOn(t, dir, "feature", "c.txt", "stack c\n", "feat: other", "")
	before := gitIn(t, dir, "rev-parse", "feature")
	r, err := SimulateRebase(dir, "main", "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Reworked) != 1 {
		t.Fatalf("reworked %+v, want the one commit by the same author", r.Reworked)
	}
	w := r.Reworked[0]
	if w.Index != 1 || w.Total != 3 || w.Subject != "fix: retire registrations" || w.TrunkCommit != gitIn(t, dir, "rev-parse", "main~1") {
		t.Errorf("reworked %+v", w)
	}
	if r.Stop == nil || r.Stop.Index != 1 {
		t.Errorf("the reworked commit is kept and replayed, so it stops: stop %+v", r.Stop)
	}
	if after := gitIn(t, dir, "rev-parse", "feature"); after != before {
		t.Errorf("the branch moved from %s to %s", before, after)
	}
}

// A declared rule does not hide a later overlap: the rule may refuse the
// conflict there, and only a person's stop is certain to resolve nothing.
func TestSimulateRebaseKeepsALikelyStopOnAFileADeclaredRuleClaims(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte("conflicts:\n  - paths: [v.txt]\n    strategy: owned-line\n    line: '^\\d'\n    rule: max-plus-patch\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir := repoWith(t, map[string]string{"a.txt": "a\n", "v.txt": "1.0.0\n"},
		[]map[string]string{{"a.txt": "trunk\n", "v.txt": "1.0.5\ntrunk-note\n"}},
		[]map[string]string{{"a.txt": "branch\n"}, {"v.txt": "1.0.1\nbranch-note\n"}})
	r, err := SimulateRebase(dir, "main", "feature", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stop == nil || r.Stop.Index != 1 {
		t.Fatalf("stop %+v, want 1/2", r.Stop)
	}
	if len(r.Likely) != 1 || r.Likely[0].Index != 2 || !reflect.DeepEqual(r.Likely[0].Paths, []string{"v.txt"}) {
		t.Fatalf("likely %+v, want 2/2 on v.txt", r.Likely)
	}
}
