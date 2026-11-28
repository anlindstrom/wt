package synccmd_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/synccmd/synccmdtest"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// A layer added since the others were mapped gets the command that records
// where it pushes, built from theirs. Nothing is recorded, and --json says
// what it said before.
func TestRunNamesThePushTargetItsSiblingsImply(t *testing.T) {
	t.Parallel()
	skipShort(t)
	const want = "wt sync push-to --branch 'tmp/layer/d' 'origin/feat_wt/d'"
	for name, c := range map[string]struct {
		setup func(git func(args ...string))
		hint  bool
	}{
		"one pattern": {hint: true},
		"siblings that disagree": {setup: func(git func(...string)) {
			git("config", "branch.tmp/layer/b.wtPushTo", "origin other/b")
		}},
		"a same-name push that works": {setup: func(git func(...string)) {
			git("push", "-q", "origin", "refs/heads/tmp/layer/d:refs/heads/tmp/layer/d")
		}},
		"no such remote branch": {setup: func(git func(...string)) {
			git("push", "-q", "origin", ":refs/heads/feat_wt/d")
		}},
		"a destination another branch records": {setup: func(git func(...string)) {
			git("config", "branch.main.wtPushTo", "origin feat_wt/d")
		}},
		"a destination that is a local branch's name": {setup: func(git func(...string)) {
			git("branch", "feat_wt/d", "main")
		}},
		"an argument that reads as another remote": {setup: func(git func(...string)) {
			git("config", "remote.origin/feat_wt.url", "elsewhere")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newMappedLine(t)
			main := m.ctx.Repo.MainRoot
			git := func(args ...string) { testkit.GitIn(t, main, args...) }
			path := strings.TrimSpace(testkit.GitOut(t, main, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+mappedTop))
			testkit.WriteFile(t, path, "d.txt", "d\n")
			testkit.GitIn(t, path, "add", filepath.Join(path, "d.txt"))
			testkit.GitIn(t, path, "commit", "-q", "--no-verify", "-m", "feat(d): add d")
			git("branch", "tmp/layer/d", mappedTop)
			git("push", "-q", "origin", "refs/heads/tmp/layer/d:refs/heads/feat_wt/d")
			if c.setup != nil {
				c.setup(git)
			}
			config := testkit.GitOut(t, main, "config", "--local", "--list")
			opts := synccmdtest.NoAgents()
			opts.Push = syncrun.PushNever
			opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
			res, human, err := synccmdtest.StackRun(t, m.ctx, mappedTop, opts)
			if err != nil {
				t.Fatalf("run: %v\n%s", err, human)
			}
			// Said under the layer, and again where the run counts what is left.
			if got := strings.Contains(human, "push-to --branch"); got != c.hint || c.hint && strings.Count(human, want) != 2 {
				t.Errorf("hint %v, want %v (%s)\n%s", got, c.hint, want, human)
			}
			var left string
			for _, l := range strings.Split(human, "\n") {
				if strings.HasPrefix(l, "  ↪ ") {
					left = l
				}
			}
			summary := ""
			switch {
			case c.hint:
				summary = "; 1 more with nowhere to push: " + want
			case name != "a same-name push that works":
				summary = "; 1 more with nowhere to push: tmp/layer/d (wt sync push-to records where)"
			}
			if got := strings.Contains(left, "nowhere to push"); got != (summary != "") || !strings.Contains(left, summary) {
				t.Errorf("the count of what is left is %q, want %q in it\n%s", left, summary, human)
			}
			if got := testkit.GitOut(t, main, "config", "--local", "--list"); got != config {
				t.Errorf("the config changed:\n%s\nwas\n%s", got, config)
			}
			if !c.hint {
				return
			}
			for _, l := range synccmdtest.Participant(t, res, mappedTop).Stack.Layers {
				if l.Branch == "tmp/layer/d" && (l.NoPushReason == nil || *l.NoPushReason != "there is no remote branch of its own yet" || l.PushCommand != nil) {
					t.Errorf("--json changed for d: %+v", l)
				}
			}
		})
	}
}

// A run that pushes all its record holds still names the layers it moved that
// nothing says where to push.
func TestRunThatPushesEverythingStillNamesTheLayersWithNowhereToPush(t *testing.T) {
	t.Parallel()
	skipShort(t)
	m := newMappedLine(t)
	main := m.ctx.Repo.MainRoot
	path := strings.TrimSpace(testkit.GitOut(t, main, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+mappedTop))
	testkit.WriteFile(t, path, "d.txt", "d\n")
	testkit.GitIn(t, path, "add", "-A")
	testkit.GitIn(t, path, "commit", "-q", "--no-verify", "-m", "feat(d): add d")
	testkit.GitIn(t, main, "branch", "tmp/layer/d", mappedTop)
	testkit.GitIn(t, main, "push", "-q", "origin", "refs/heads/"+mappedTop+":refs/heads/"+mappedTop)
	opts := synccmdtest.PushAlways()
	opts.Stack = &synccmd.StackOptions{Layers: []string{"tmp/layer/*"}}
	_, human, err := synccmdtest.StackRun(t, m.ctx, mappedTop, opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, human)
	}
	if _, kept, _ := wtsync.ReadPushRecord(main, mappedTop); kept {
		t.Fatalf("the push left a record\n%s", human)
	}
	if want := "  ↪ 1 moved layer with nowhere to push: tmp/layer/d (wt sync push-to records where)\n"; !strings.Contains(human, want) {
		t.Errorf("want %q\n%s", want, human)
	}
}
