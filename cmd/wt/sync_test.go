package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/wtsync"
)

func TestConfirmPushDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"\n", false},
		{"y\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"no\n", false},
		{"q\n", false},
		{"", false}, // ^D declines
	} {
		var out bytes.Buffer
		got := confirmPush(newPrompter(strings.NewReader(tc.in), &out))([]string{"bump"})
		if got != tc.want {
			t.Errorf("answer %q: got %v; want %v", tc.in, got, tc.want)
		}
		if !strings.Contains(out.String(), "push bump with --force-with-lease? [y/N]") {
			t.Errorf("question %q", out.String())
		}
	}
	var out bytes.Buffer
	confirmPush(newPrompter(strings.NewReader("\n"), &out))([]string{"a", "b"})
	if !strings.Contains(out.String(), "push a, b with --force-with-lease? [y/N]") {
		t.Errorf("question %q", out.String())
	}
}

func TestConfirmAskNamesTheWorktreesAndDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"\n", false},
		{"y\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"", false}, // ^D declines
	} {
		var out bytes.Buffer
		got := confirmAsk(newPrompter(strings.NewReader(tc.in), &out), "rebase")([]string{"a", "b"})
		if got != tc.want {
			t.Errorf("answer %q: got %v; want %v", tc.in, got, tc.want)
		}
		if !strings.Contains(out.String(), "rebase a, b? [y/N]") {
			t.Errorf("question %q", out.String())
		}
	}
}

// A flag given without what it needs is refused by name, the way --push with
// --no-push is: one line, no usage. The overview (look) and a run take
// different flags, so the same flags are checked for each.
func TestRunFlagsRefuseFlagsThatDoNotGoTogether(t *testing.T) {
	for _, tc := range []struct {
		flags runFlags
		look  bool
		err   string
	}{
		{runFlags{}, true, ""},
		{runFlags{noFetch: true}, true, ""},
		{runFlags{yes: true, noFetch: true, push: syncrun.PushAlways}, false, ""},
		{runFlags{allowOwed: true}, false, ""},
		{runFlags{stack: true, layers: []string{"a"}, autosquash: true}, false, ""},
		{runFlags{stack: true, layers: []string{"a"}}, true, ""},
		{runFlags{layers: []string{"a"}}, false, "--layer needs --stack"},
		{runFlags{layers: []string{"a"}}, true, "--layer needs --stack"},
		{runFlags{autosquash: true}, false, "--autosquash needs --stack"},
		{runFlags{autosquash: true}, true, "--autosquash needs --stack"},
		{runFlags{noSides: true}, false, "--no-side-branches needs --stack"},
		{runFlags{noSides: true}, true, "--no-side-branches needs --stack"},
		{runFlags{alone: true, json: true}, true, "--alone with --json needs wt sync rebase: the look has no JSON of it"},
		{runFlags{alone: true, json: true}, false, ""},
		{runFlags{alone: true, stack: true, edit: "a1b2c3d"}, false, "--alone and --edit: --edit moves the layers by the amend, and --alone moves none; edit first without --alone"},
		{runFlags{alone: true, stack: true, autosquash: true}, false, ""},
		{runFlags{alone: true, onto: "main"}, false, "--alone and --onto: --onto already moves exactly the worktrees named and none of their stack; leave --alone out"},
		{runFlags{upstream: "a"}, true, "--upstream needs --onto: it says where the commits rebased onto --onto's ref start"},
		// The look names the fixups a fold would move ahead of their context.
		{runFlags{stack: true, autosquash: true}, true, ""},
		{runFlags{stack: true, autosquash: true, onto: "main"}, true, ""},
		{runFlags{stack: true, edit: "a1b2c3d"}, false, ""},
		{runFlags{edit: "a1b2c3d"}, false, "--edit needs --stack"},
		{runFlags{stack: true, edit: "a1b2c3d", autosquash: true}, false,
			"--edit and --autosquash are two runs: edit in place first (--stack --edit <rev>), then fold (--stack --autosquash)"},
		{runFlags{stack: true, edit: "a1b2c3d", onto: "main"}, false,
			"--edit and --onto are two runs: edit in place first (--stack --edit <rev>), then move (--stack --onto <ref>)"},
		{runFlags{eachCommit: "tips"}, false, ""},
		{runFlags{stack: true, eachCommit: "off", autosquash: true, onto: "main", ifReady: true}, false, ""},
		{runFlags{eachCommit: "all"}, false, ""},
		{runFlags{eachCommit: "sometimes"}, false, `--each-commit takes all, tips, targets or off, not "sometimes"`},
		{runFlags{stack: true, edit: "a1b2c3d", eachCommit: "tips"}, false,
			"--each-commit=tips and --edit are two runs: an edit run's checks sit at the commits above the edit; use --each-commit=off, or edit first and check after"},
		{runFlags{stack: true, edit: "a1b2c3d", eachCommit: "off"}, false, ""},
		{runFlags{stack: true, autosquash: true, eachCommit: "targets"}, false, ""},
		{runFlags{stack: true, eachCommit: "targets"}, false, wtsync.TargetsRefusal(false)},
		{runFlags{stack: true, autosquash: true, onto: "main", eachCommit: "targets"}, false, wtsync.TargetsRefusal(false)},
		{runFlags{stack: true, edit: "a1b2c3d", eachCommit: "targets"}, false, wtsync.TargetsRefusal(true)},
	} {
		if got := errString(tc.flags.check(tc.look)); got != tc.err {
			t.Errorf("%+v (look %v): error %q, want %q", tc.flags, tc.look, got, tc.err)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Each verb takes the count its subcommand declares, refused in its words;
// the overview takes at most one worktree.
func TestSyncTakesTheArgumentCountOfItsVerb(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"sync", "a", "b"}, "accepts at most 1 arg(s), received 2"},
		{[]string{"sync", "resume"}, "accepts 1 arg(s), received 0"},
		{[]string{"sync", "resume", "a", "b"}, "accepts 1 arg(s), received 2"},
		{[]string{"sync", "undo"}, "accepts 1 arg(s), received 0"},
		{[]string{"sync", "undo", "a", "b"}, "accepts 1 arg(s), received 2"},
	} {
		out, err := runCmd(t, tc.args...)
		if err == nil {
			t.Errorf("%v: want an error", tc.args)
			continue
		}
		if err.Error() != tc.err {
			t.Errorf("%v: error %q, want %q", tc.args, err.Error(), tc.err)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("%v: an argument mistake shows usage:\n%s", tc.args, out)
		}
	}
}

// rebase with nothing named is every ready worktree: no argument count
// refuses it, so it gets as far as opening the repository.
func TestSyncRebaseWithNothingNamedIsNotAnArgumentMistake(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"sync", "rebase"}, {"sync", "rebase", "--no-fetch"}} {
		out, err := runCmd(t, args...)
		if err == nil || err.Error() != "not a git repository" {
			t.Errorf("%v: error %v, want the verb's own not-a-repository error\n%s", args, err, out)
		}
	}
}

// Each verb takes every flag it has and opens the repository the verb's way
// (withContext, which refuses a directory outside a repository as "not a git
// repository") rather than the overview's lenient way, which says "not inside
// a git repository". An unknown or misplaced flag would fail before either, so
// the verb's own error is the flags parsed and the verb reached.
func TestSyncVerbsTakeTheirFlagsAndReachTheVerb(t *testing.T) {
	t.Chdir(t.TempDir())
	_, overview := runCmd(t, "sync", "a")
	if overview == nil || overview.Error() != "not inside a git repository" {
		t.Fatalf("overview outside a repository: %v", overview)
	}
	for _, args := range [][]string{
		{"sync", "rebase", "a"},
		{"sync", "rebase", "a", "b", "--yes", "--push", "--no-fetch"},
		{"sync", "rebase", "a", "-y", "--no-push", "--force", "--allow-diverged", "--allow-owed", "--no-ff-trunk"},
		{"sync", "rebase", "a", "--if-ready", "--expect", "1:0000", "--each-commit", "tips"},
		{"sync", "resume", "a", "--no-push"},
		{"sync", "resume", "a", "-y"},
		{"sync", "resume", "a", "--push", "--allow-owed", "--adopt", "--allow-dropped"},
		{"sync", "resume", "a", "--drop", "abc1234"},
		{"sync", "undo", "a", "--force"},
		{"sync", "undo", "a", "-y"},
	} {
		out, err := runCmd(t, args...)
		if err == nil || err.Error() != "not a git repository" {
			t.Errorf("%v: error %v, want the verb's own not-a-repository error\n%s", args, err, out)
		}
	}
}

// A flag mistake prints one line and no usage, as --push with --no-push does
// on the verb.
func TestSyncFlagMistakesDoNotShowUsage(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"sync", "a", "--layer", "x"},
		{"sync", "rebase", "a", "--layer", "x"},
		{"sync", "rebase", "a", "--autosquash"},
	} {
		out, err := runCmd(t, args...)
		if err == nil {
			t.Errorf("%v: want an error", args)
			continue
		}
		if strings.Contains(out, "Usage:") {
			t.Errorf("%v: a flag mistake does not show usage:\n%s", args, out)
		}
	}
}

// -y is --yes on every verb, as on wt remove, wt sweep and wt init.
func TestSyncVerbsTakeYForYes(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"sync", "rebase"}, {"sync", "resume"}, {"sync", "undo"}} {
		c, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		f := c.Flags().ShorthandLookup("y")
		if f == nil || f.Name != "yes" {
			t.Errorf("%s: -y is not --yes", c.CommandPath())
		}
	}
}

// sync rebase asks before the rebase and again before the push. Both answers can
// be typed ahead, so both questions have to read through one prompter.
func TestRunQuestionsShareOneReader(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(strings.NewReader("n\ny\n"), &out)
	if ok := confirmAsk(p, "rebase")([]string{"bump"}); ok {
		t.Error("the rebase question did not read its n")
	}
	if ok := confirmPush(p)([]string{"bump"}); !ok {
		t.Error("the push question lost the y typed ahead for it")
	}
	if want := "rebase bump? [y/N] push bump with --force-with-lease? [y/N] "; out.String() != want {
		t.Errorf("questions = %q, want %q", out.String(), want)
	}
}

// With nobody at a terminal, a bulk run rebases nothing without --yes; a
// named worktree goes ahead; --yes asks nothing, the push included.
func TestRunOptionsAskNobodyAndRefuseBulkWithoutATerminal(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)

	bulk, _ := runOptions(cmd, runFlags{}, true)
	if bulk.Confirm == nil {
		t.Fatal("a bulk run with no terminal must not go ahead unasked")
	}
	if ok := bulk.Confirm([]string{"bump"}); ok || !strings.Contains(out.String(), "Pass --yes to rebase bump.") {
		t.Errorf("want a no that says how to say yes: %v %q", ok, out.String())
	}
	if named, _ := runOptions(cmd, runFlags{}, false); named.Confirm != nil {
		t.Error("a named worktree with no terminal goes ahead")
	}
	yes, p := runOptions(cmd, runFlags{yes: true}, true)
	if yes.Confirm != nil || yes.ConfirmPush != nil || p != nil {
		t.Error("--yes asks nothing")
	}
	if yes.Push != syncrun.PushAlways {
		t.Error("--yes is yes to the push too")
	}
	if held, _ := runOptions(cmd, runFlags{yes: true, push: syncrun.PushNever}, false); held.Push != syncrun.PushNever {
		t.Error("--no-push keeps the push out, --yes or not")
	}
}

// Where a branch that tracks another name pushes is a choice between the two
// branches, never a default: Enter, a letter, or ^D leave it unpushed.
func TestChoosePushNamesBothBranchesAndTakesOnlyANumber(t *testing.T) {
	c := syncrun.PushChoice{Branch: "feat_wt/own_apikey", Upstream: "origin/own_apikey", Own: "origin/feat_wt/own_apikey"}
	for in, want := range map[string]string{
		"1\n": c.Upstream, "2\n": c.Own, "\n": "", "y\n": "", "3\n": "", "origin/own_apikey\n": "", "": "",
	} {
		var out bytes.Buffer
		got, err := choosePush(newPrompter(strings.NewReader(in), &out))(c)
		if err != nil || got != want {
			t.Errorf("answer %q: got %q, %v; want %q", in, got, err, want)
		}
		question := "feat_wt/own_apikey tracks origin/own_apikey, a branch of another name. Where does it push?\n" +
			"A push after a rebase is forced: it replaces what that branch has.\n" +
			"  1  origin/own_apikey, the branch it tracks\n  2  origin/feat_wt/own_apikey, its own name\n" +
			"1, 2, or Enter to leave it unpushed: "
		if out.String() != question {
			t.Errorf("question %q", out.String())
		}
	}
}
