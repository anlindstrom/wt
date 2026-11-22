package synccmd_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/testkit"
)

func recorded(t *testing.T, m *mappedLine, branch string) string {
	t.Helper()
	return strings.TrimSpace(testkit.GitOut(t, m.ctx.Repo.MainRoot, "config", "--get", "branch."+branch+".wtPushTo"))
}

func pushToBranch(m *mappedLine, branch, dest string, unset, dry bool) (string, error) {
	var out bytes.Buffer
	err := synccmd.SyncPushToBranch(m.ctx, branch, dest, unset, dry, &out)
	return out.String(), err
}

// The pattern form records one target per matching branch, prints each, and
// records nothing for what does not match.
func TestPushToBranchPatternRecordsEachMatch(t *testing.T) {
	t.Parallel()
	m := newMappedLine(t)
	main := m.ctx.Repo.MainRoot
	for _, l := range mappedLayers {
		testkit.GitIn(t, main, "config", "--unset", "branch.tmp/layer/"+l+".wtPushTo")
	}
	out, err := pushToBranch(m, "tmp/layer/*", "origin/feat_wt/*", false, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, l := range mappedLayers {
		if got := recorded(t, m, "tmp/layer/"+l); got != "origin feat_wt/"+l {
			t.Errorf("tmp/layer/%s records %q", l, got)
		}
		if !strings.Contains(out, "tmp/layer/"+l+" pushes to origin/feat_wt/"+l+" (recorded)") {
			t.Errorf("no line for %s:\n%s", l, out)
		}
	}
	if got := recordedOrEmpty(t, m, "tmp/x"); got != "" {
		t.Errorf("the line was recorded: %q", got)
	}
}

func recordedOrEmpty(t *testing.T, m *mappedLine, branch string) string {
	t.Helper()
	out := testkit.GitTry(t, m.ctx.Repo.MainRoot, "config", "--get", "branch."+branch+".wtPushTo")
	return strings.TrimSpace(out)
}

// A pattern with no match is refused and records nothing.
func TestPushToBranchPatternWithNoMatchIsRefused(t *testing.T) {
	t.Parallel()
	m := newMappedLine(t)
	_, err := pushToBranch(m, "nope/*", "origin/feat_wt/*", false, false)
	if err == nil || !strings.Contains(err.Error(), "nope/*") {
		t.Fatalf("err %v, want a refusal naming the pattern", err)
	}
}

// --dry-run says what it would record and records nothing.
func TestPushToBranchDryRunRecordsNothing(t *testing.T) {
	t.Parallel()
	m := newMappedLine(t)
	main := m.ctx.Repo.MainRoot
	testkit.GitIn(t, main, "config", "--unset", "branch.tmp/layer/a.wtPushTo")
	out, err := pushToBranch(m, "tmp/layer/a", "origin/feat_wt/other", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := recordedOrEmpty(t, m, "tmp/layer/a"); got != "" {
		t.Errorf("--dry-run recorded %q", got)
	}
	if !strings.Contains(out, "would record") || !strings.Contains(out, "origin/feat_wt/other") {
		t.Errorf("--dry-run says:\n%s", out)
	}
}

// --unset works with --branch, patterns included.
func TestPushToBranchUnset(t *testing.T) {
	t.Parallel()
	m := newMappedLine(t)
	if out, err := pushToBranch(m, "tmp/layer/*", "", true, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, l := range mappedLayers {
		if got := recordedOrEmpty(t, m, "tmp/layer/"+l); got != "" {
			t.Errorf("tmp/layer/%s still records %q", l, got)
		}
	}
}

// A destination that is trunk is refused for every match, and nothing is
// recorded for any of them.
func TestPushToBranchRefusesTrunkAndRecordsNone(t *testing.T) {
	t.Parallel()
	m := newMappedLine(t)
	main := m.ctx.Repo.MainRoot
	for _, l := range mappedLayers {
		testkit.GitIn(t, main, "config", "--unset", "branch.tmp/layer/"+l+".wtPushTo")
	}
	if _, err := pushToBranch(m, "tmp/layer/a", "origin/main", false, false); err == nil || !strings.Contains(err.Error(), "trunk") {
		t.Fatalf("err %v, want a refusal naming trunk", err)
	}
	if got := recordedOrEmpty(t, m, "tmp/layer/a"); got != "" {
		t.Errorf("recorded %q", got)
	}
}

// The issue's whole path: record with the pattern form, run --no-push, push
// later; feat_wt/a..c move and nothing else on origin does.
func TestPushToBranchThenStackPushMovesOnlyTheMappedBranches(t *testing.T) {
	t.Parallel()
	m := newMappedLine(t)
	main := m.ctx.Repo.MainRoot
	for _, l := range mappedLayers {
		testkit.GitIn(t, main, "config", "--unset", "branch.tmp/layer/"+l+".wtPushTo")
	}
	if out, err := pushToBranch(m, "tmp/layer/*", "origin/feat_wt/*", false, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m.noPushRun(t)
	before := m.refs(t)
	if human, err := m.pushLater(t, false, false); err != nil {
		t.Fatalf("push: %v\n%s", err, human)
	}
	names := func(refs string) []string {
		var n []string
		for _, l := range strings.Split(refs, "\n") {
			n = append(n, strings.Fields(l)[0])
		}
		return n
	}
	if got, want := strings.Join(names(m.refs(t)), " "), strings.Join(names(before), " "); got != want {
		t.Errorf("the remote's branches changed: %s, was %s", got, want)
	}
	for _, l := range mappedLayers {
		if got, want := refIn(t, m.bare, "feat_wt/"+l), m.local(t, "tmp/layer/"+l); got != want || got == m.tips[l] {
			t.Errorf("feat_wt/%s is at %s, want %s", l, got, want)
		}
	}
}
