package synccmd_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// mappedLine is wt#197's stack: layers tmp/layer/a..c with no worktree, whose
// pull request branches are feat_wt/a..c on origin, and the line tmp/x, in a
// worktree, at the top layer's tip. origin is a bare repository holding
// feat_wt/a..c and trunk, and nothing named tmp/*.
type mappedLine struct {
	ctx  *kit.Context
	bare string
	tips map[string]string // each layer's and the line's tip before any run
}

var mappedLayers = []string{"a", "b", "c"}

const mappedLine_ = "tmp/x"

func newMappedLine(t *testing.T) *mappedLine {
	t.Helper()
	m := &mappedLine{tips: map[string]string{}}
	var main string
	ctx, main := synccmdtest.OpenLine(t, func(main, wtDir string) {
		gittest.WriteFile(t, filepath.Join(main, "t.txt"), "t\n")
		gittest.Git(t, main, "add", "-A")
		gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", "base")
		gittest.Git(t, main, "checkout", "-q", "-b", mappedLine_)
		for _, l := range mappedLayers {
			gittest.WriteFile(t, filepath.Join(main, l+".txt"), l+"\n")
			gittest.Git(t, main, "add", "-A")
			gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", "feat("+l+"): add "+l)
			gittest.Git(t, main, "branch", "tmp/layer/"+l)
			m.tips[l] = gittest.Git(t, main, "rev-parse", "HEAD")
		}
		m.tips[mappedLine_] = m.tips["c"]
		gittest.Git(t, main, "checkout", "-q", "main")
		gittest.WriteFile(t, filepath.Join(main, ".wt-sync.yaml"), "conflicts: []\n")
		gittest.Git(t, main, "add", "-A")
		gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", "chore: declare")
		gittest.WriteFile(t, filepath.Join(main, "trunk.txt"), "trunk\n")
		gittest.Git(t, main, "add", "-A")
		gittest.Git(t, main, "commit", "-q", "--no-verify", "-m", "feat: trunk's own")
		m.bare = filepath.Join(t.TempDir(), "origin.git")
		gittest.Git(t, main, "init", "-q", "--bare", m.bare)
		gittest.Git(t, main, "push", "-q", m.bare, "refs/heads/main:refs/heads/main")
		for _, l := range mappedLayers {
			gittest.Git(t, main, "push", "-q", m.bare, "refs/heads/tmp/layer/"+l+":refs/heads/feat_wt/"+l)
		}
		gittest.Git(t, main, "remote", "add", "origin", m.bare)
		gittest.Git(t, main, "fetch", "-q", "origin")
		gittest.Git(t, main, "worktree", "add", "-q", filepath.Join(wtDir, "x"), mappedLine_)
	})
	m.ctx = ctx
	// The mapping a person records by hand, until wt sync push-to --branch.
	for _, l := range mappedLayers {
		testkit.GitIn(t, main, "config", "branch.tmp/layer/"+l+".wtPushTo", "origin feat_wt/"+l)
	}
	return m
}

func (m *mappedLine) refs(t *testing.T) string { return bareRefs(t, m.bare) }

// noPushRun is the run, --no-push, with the layers named by a pattern.
func (m *mappedLine) noPushRun(t *testing.T) string {
	t.Helper()
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushNever
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
	_, human, err := synccmdtest.StackRun(t, m.ctx, mappedLine_, opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	return human
}

func (m *mappedLine) pushLater(t *testing.T, dryRun, pushLine bool) (string, error) {
	t.Helper()
	var human bytes.Buffer
	opts := syncfix.PushLaterOptions{DryRun: dryRun}
	opts.Push = syncrun.PushAlways
	opts.PushLine = pushLine
	err := syncfix.SyncPushLater(m.ctx, mappedLine_, opts, &human)
	return human.String(), err
}

func (m *mappedLine) local(t *testing.T, branch string) string {
	return testkit.GitOut(t, m.ctx.Repo.MainRoot, "rev-parse", "refs/heads/"+branch)
}

// The issue's shape: a --no-push run, then wt sync push --stack, moves
// feat_wt/a..c over their leases and creates nothing, the line's tmp branch
// included.
func TestPushLaterMovesMappedLayersAndCreatesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	m.noPushRun(t)
	before := m.refs(t)
	human, err := m.pushLater(t, false, false)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, human)
	}
	after := m.refs(t)
	if strings.Contains(after, "refs/heads/tmp/") {
		t.Errorf("the push created a tmp branch on origin:\n%s\n%s", after, human)
	}
	for _, l := range mappedLayers {
		if got, want := refIn(t, m.bare, "feat_wt/"+l), m.local(t, "tmp/layer/"+l); got != want || got == m.tips[l] {
			t.Errorf("feat_wt/%s is at %s, want the rebased tip %s (was %s)\n%s", l, got, want, m.tips[l], human)
		}
	}
	if got := strings.Count(after, "\n") - strings.Count(before, "\n"); got != 0 {
		t.Errorf("the set of remote refs changed:\nbefore %s\nafter %s", before, after)
	}
	if !strings.Contains(human, mappedLine_) || !strings.Contains(human, "--push-line") {
		t.Errorf("the push does not name the line as left out, with the flag that includes it:\n%s", human)
	}
}

// A lease that moved on one of the layers: nothing moves.
func TestPushLaterMappedLeaseMovedMovesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	m.noPushRun(t)
	other := filepath.Join(t.TempDir(), "other")
	testkit.GitIn(t, m.ctx.Repo.MainRoot, "clone", "-q", m.bare, other)
	testkit.GitIn(t, other, "checkout", "-q", "feat_wt/b")
	gittest.WriteFile(t, filepath.Join(other, "mate.txt"), "a teammate\n")
	testkit.GitIn(t, other, "add", "-A")
	testkit.GitIn(t, other, "commit", "-q", "--no-verify", "-m", "feat: teammate")
	testkit.GitIn(t, other, "push", "-q", "origin", "feat_wt/b")
	before := m.refs(t)
	human, err := m.pushLater(t, false, false)
	if err == nil {
		t.Fatalf("push went through over a moved lease:\n%s", human)
	}
	if got := m.refs(t); got != before {
		t.Errorf("remote refs moved:\nbefore %s\nafter %s\n%s", before, got, human)
	}
}

// The line's own branch, which would be new on origin, goes only with
// --push-line.
func TestPushLaterPushesTheLineOnlyWithPushLine(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	m.noPushRun(t)
	human, err := m.pushLater(t, false, true)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, human)
	}
	if got, want := refIn(t, m.bare, mappedLine_), m.local(t, mappedLine_); got != want {
		t.Errorf("origin %s is at %s, want %s\n%s", mappedLine_, got, want, human)
	}
	for _, l := range mappedLayers {
		if got, want := refIn(t, m.bare, "feat_wt/"+l), m.local(t, "tmp/layer/"+l); got != want {
			t.Errorf("feat_wt/%s is at %s, want %s", l, got, want)
		}
	}
}

// --push on the run itself: the same.
func TestPushRunMappedLayersLeavesTheLineOut(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	opts := synccmdtest.NoAgents()
	opts.Push = syncrun.PushAlways
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
	_, human, err := synccmdtest.StackRun(t, m.ctx, mappedLine_, opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	if got := m.refs(t); strings.Contains(got, "refs/heads/tmp/") {
		t.Errorf("the run created a tmp branch on origin:\n%s\n%s", got, human)
	}
	for _, l := range mappedLayers {
		if got, want := refIn(t, m.bare, "feat_wt/"+l), m.local(t, "tmp/layer/"+l); got != want || got == m.tips[l] {
			t.Errorf("feat_wt/%s is at %s, want the rebased tip %s\n%s", l, got, want, human)
		}
	}
	if !strings.Contains(human, "--push-line") {
		t.Errorf("the run does not name the line as left out:\n%s", human)
	}
}

// --dry-run says, per branch, where it pushes, the rule that decided it, the
// lease and the new commit.
func TestPushLaterDryRunNamesTheRule(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration: a stack over real repositories and a bare remote")
	}
	m := newMappedLine(t)
	m.noPushRun(t)
	before := m.refs(t)
	human, err := m.pushLater(t, true, true)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, human)
	}
	if m.refs(t) != before {
		t.Fatal("--dry-run moved a remote ref")
	}
	for _, l := range mappedLayers {
		want := "would push tmp/layer/" + l + "  origin/feat_wt/" + l + " (recorded)"
		if !strings.Contains(human, want) {
			t.Errorf("dry run lacks %q:\n%s", want, human)
		}
	}
	if !strings.Contains(human, "would push tmp/x  origin/tmp/x (its own name)") {
		t.Errorf("dry run does not say the line's rule:\n%s", human)
	}
}
