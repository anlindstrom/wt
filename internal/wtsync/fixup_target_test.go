package wtsync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/gittest"
)

// fixupRepo is trunk "main" at base, and feature with "feat: one" and
// "feat: two" above it; trunk then takes "feat: one" patch for patch. extra
// commits are added to feature after, as subject.
func fixupRepo(t *testing.T, extra ...string) (dir, base string, commits []string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = gittest.NewRepo(t, parent, "r")
	commit := func(file, subject string) {
		gittest.WriteFile(t, filepath.Join(dir, file), subject+"\n")
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", subject)
	}
	commit("base.txt", "base")
	base = gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	commit("one.txt", "feat: one")
	one := gitIn(t, dir, "rev-parse", "HEAD")
	commit("two.txt", "feat: two")
	for i, subject := range extra {
		commit("x"+string(rune('a'+i))+".txt", subject)
	}
	gitIn(t, dir, "checkout", "-q", "main")
	commit("trunk.txt", "trunk own")
	gitIn(t, dir, "cherry-pick", one)
	gitIn(t, dir, "tag", "carried", one)
	out := gitIn(t, dir, "rev-list", "--reverse", base+"..feature")
	return dir, base, strings.Fields(out)
}

func TestFixupOfCarriedMatchesGitsTargetResolution(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		extra  []string
		refuse string // what the refusal must mention; "" accepts
		upstrm bool
	}{
		{"a fixup of a commit that is replayed", []string{"fixup! feat: two"}, "", false},
		{"a fixup of a carried commit", []string{"fixup! feat: one"}, "already carries", false},
		{"whitespace after the prefix", []string{"fixup!   feat: one"}, "already carries", false},
		{"a tag naming a carried commit", []string{"fixup! carried"}, "already carries", false},
		{"a subject that matches two commits", []string{"feat: two", "fixup! feat: two"}, "more than one", false},
		{"a target that is nowhere", []string{"fixup! nothing like it"}, "no commit", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir, _, commits := fixupRepo(t, c.extra...)
			why, err := FixupOfCarried(dir, "main", "", "feature", commits)
			if err != nil {
				t.Fatal(err)
			}
			if c.refuse == "" && why != "" || c.refuse != "" && !strings.Contains(why, c.refuse) {
				t.Errorf("refusal %q, want one mentioning %q", why, c.refuse)
			}
		})
	}
}

// A subject is looked for before a revision: with "release" both a commit's
// subject and a tag on a later one, the fixup is of the subject's commit, here
// below the replayed range.
func TestFixupOfCarriedResolvesASubjectBeforeARevision(t *testing.T) {
	t.Parallel()
	dir, _, commits := fixupRepo(t, "release", "feat: three", "fixup! release")
	gitIn(t, dir, "tag", "release", commits[3])
	why, err := FixupOfCarried(dir, "main", commits[2], "feature", commits)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(why, `is for "release" (`+git.ShortID(commits[2], 7)+`)`) {
		t.Errorf("refusal %q, want one for the commit with the subject", why)
	}
}

// With an explicit upstream, a commit the target already reaches stays in
// the replayed range as an empty pick, and a fixup of it would amend the
// destination: B -> A, main = A, feature A -> C -> fixup! A, onto main above B.
func TestFixupOfCarriedSeesACommitTheTargetReaches(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := gittest.NewRepo(t, parent, "r")
	commit := func(file, subject string) string {
		gittest.WriteFile(t, filepath.Join(dir, file), subject+"\n")
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", subject)
		return gitIn(t, dir, "rev-parse", "HEAD")
	}
	b := commit("b.txt", "B")
	a := commit("a.txt", "A")
	commit("c.txt", "C")
	commit("a.txt", "fixup! A")
	gitIn(t, dir, "branch", "feature")
	gitIn(t, dir, "reset", "-q", "--hard", a)
	why, err := FixupOfCarried(dir, "main", b, "feature", strings.Fields(gitIn(t, dir, "rev-list", "--reverse", b+"..feature")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(why, "already carries") {
		t.Errorf("refusal %q", why)
	}
}

// A layer's marker must sit on the destination: a rebase that ended below
// it (a fixup amended the destination itself, say) moves no layer.
func TestCheckLayersRequiresTheDestinationBelowEveryTip(t *testing.T) {
	t.Parallel()
	dir, _, commits := fixupRepo(t)
	head := commits[len(commits)-1]
	gitIn(t, dir, "update-ref", "refs/wt-stack-map/1/1", head)
	n := gitIn(t, dir, "rev-list", "--count", "main.."+head)
	count := 0
	for _, c := range n {
		count = count*10 + int(c-'0')
	}
	// main carries feat: one by cherry-pick, so main is not an ancestor of head.
	_, err := CheckLayers(dir, head, "main", []LayerPlan{{Branch: "s1", Ref: "refs/wt-stack-map/1/1", Expect: count}})
	if err == nil || !strings.Contains(err.Error(), "descend") {
		t.Fatalf("err %v, want a refusal: the destination is not below the tip", err)
	}
}
