package main

import (
	"fmt"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/spf13/cobra"
)

func newSyncPushCmd() *cobra.Command {
	var stack, dryRun, yes, asJSON, pushLine, forget bool
	push := &cobra.Command{
		Use:   "push <work> --stack",
		Short: "Push a --stack line rebased earlier with --no-push, over the run's leases",
		Long: "Push, in one atomic push per remote, the layers and the line's own branch\n" +
			"that a wt sync rebase --stack run moved and left unpushed (--no-push, a no\n" +
			"to the question, or nobody to ask): for when the result is checked first\n" +
			"and pushed after. Each branch goes over a --force-with-lease on the commit\n" +
			"the run recorded for its remote branch, so nothing is fetched and nothing\n" +
			"a teammate pushed since is replaced: if any remote branch moved, nothing on\n" +
			"that remote moves and the branch is named.\n\n" +
			"The line's own branch is left out, and named, when pushing it would create\n" +
			"its remote branch and nothing recorded for it (wt sync push-to) says it\n" +
			"should: --push-line includes it. A layer that pushes under another name is\n" +
			"recorded the same way, for a branch with no worktree too: wt sync push-to\n" +
			"--branch.\n\n" +
			"The run keeps a record of this for the line, beside its other state, until\n" +
			"the push sends everything in it or wt sync undo puts the line back. Runs\n" +
			"that were not pushed add up: a run that moves something adds its layers\n" +
			"to the record, and the push sends each branch to the newest run's tip over\n" +
			"the oldest run's lease, so a later run of the top layers does not drop the\n" +
			"lower layers an earlier run moved. wt sync undo takes back the last run\n" +
			"only and says when earlier ones are still recorded. A line with no record\n" +
			"is refused. It is also refused, before anything is pushed, when a branch\n" +
			"of the record is no longer at the tip the newest run left it at (somebody\n" +
			"worked on it since), a remote no longer pushes to the URL the leases were\n" +
			"taken for, or the runs disagree about a branch: they push it to different\n" +
			"remote branches, or took different leases because the remote moved between\n" +
			"them. That branch is named; --forget and a fresh --no-push run get out of\n" +
			"it. Layers the run held, or that had nowhere safe to push, are left out and\n" +
			"named.\n\n" +
			"--forget drops the line's record, and only that: no ref moves and nothing\n" +
			"is pushed. It says what it dropped; with --dry-run it says what it would.\n" +
			"Use it after a push made by hand, which leaves leases out of date.\n\n" +
			"It asks first on a terminal (default no); --yes skips the question, and\n" +
			"with no terminal and no --yes nothing is pushed. --dry-run says what each\n" +
			"branch would push, lease \u2192 new, and, for a record of several runs, the\n" +
			"run each came from, and changes nothing. --json prints one\n" +
			"result object on stdout, as wt sync rebase --json does: the worktree's\n" +
			"stack.push is the push, as for a run that pushed.",
		Example: "  wt sync rebase login-crash --stack --no-push   # rebase, check, then\n" +
			"  wt sync push login-crash --stack --dry-run     # what it would push\n" +
			"  wt sync push login-crash --stack --yes         # push it\n" +
			"  wt sync push login-crash --stack --forget      # drop the record, push nothing",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWork,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !stack {
				return usagef("wt sync push pushes a --stack line: add --stack")
			}
			if forget && (yes || pushLine) {
				return usagef("--forget pushes nothing: it takes neither --yes nor --push-line")
			}
			return withVerbContext(cmd, args, "sync push", asJSON, func(ctx *kit.Context, j *syncrun.RunJournal) error {
				opts := syncfix.PushLaterOptions{DryRun: dryRun, Forget: forget, Journal: j}
				opts.Push = syncrun.PushAsk
				opts.PushLine = pushLine
				switch {
				case yes:
					opts.Push = syncrun.PushAlways
				case canAsk(cmd):
					opts.ConfirmPush = confirmPush(newPrompter(cmd.InOrStdin(), cmd.OutOrStdout()))
				}
				return syncfix.SyncPushLater(ctx, args[0], opts, cmd.OutOrStdout())
			})
		},
	}
	push.Flags().BoolVar(&stack, "stack", false, "push the line a --stack run left for later")
	push.Flags().BoolVar(&pushLine, "push-line", false, "include the line's own branch when that would create its remote branch")
	push.Flags().BoolVar(&forget, "forget", false, "drop the line's push record and nothing else: no ref moves, nothing is pushed")
	push.Flags().BoolVar(&dryRun, "dry-run", false, "say what would be pushed (or, with --forget, dropped) and change nothing")
	push.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask first")
	push.Flags().BoolVar(&asJSON, "json", false, "print one result object on stdout, the rest on stderr")
	return push
}

func newSyncPushToCmd() *cobra.Command {
	var unset, dryRun bool
	var branch string
	pushTo := &cobra.Command{
		Use:   syncrun.PushToName + " [<work>] [<remote>/<branch>]",
		Short: "Say or record where a worktree's branch pushes",
		Long: "Print where a push of the worktree's branch goes and what decides it;\n" +
			"with <remote>/<branch>, record that as where it goes. Nothing is pushed\n" +
			"and no remote is asked.\n\n" +
			"A branch pushes to its own name, on origin or on the remote git pushes\n" +
			"it to: branch.<name>.pushRemote, remote.pushDefault, a same-named\n" +
			"upstream on another remote. It pushes to a branch of another name only\n" +
			"when that is recorded for it. push.default and push refspecs say where\n" +
			"git's own push goes, and never where wt's forced one does.\n\n" +
			"A branch that tracks a remote branch of another name, with nothing\n" +
			"recorded, is the one case wt does not decide: that remote branch may be\n" +
			"its own, as after a rename, or the one it was cut from. wt up and wt sync\n" +
			"rebase ask once and record the answer; with nobody to ask they leave the\n" +
			"branch unpushed and name this command. A branch that tracks trunk, a\n" +
			"local branch, or the remote branch of another local branch is not asked\n" +
			"about: it pushes under its own name.\n\n" +
			"wt migrate, wt adopt --relocate and wt remove record where a branch\n" +
			"pushed when they rename one that pushed under its own name, so its\n" +
			"pushes keep going where its pull request is. wt new gives its branch no\n" +
			"upstream.\n\n" +
			"wt pushes at the end of a rebase, and for a --stack line rebased with\n" +
			"--no-push, when wt sync push <work> --stack is run; at no other time.\n" +
			"For a branch with something to push now, this command prints the push to\n" +
			"run by hand: after a run left a branch unpushed, that line is what\n" +
			"pushes it.\n\n" +
			"<remote>/<branch> is read against the remotes that exist, the longest\n" +
			"remote name first. Trunk is refused, and so is anything but a plain\n" +
			"branch name. The record is branch.<name>.wtPushTo in the repository's\n" +
			"git config, checked again each time it is read; --unset removes it.\n" +
			"<work> is the worktree you are in when left out, and . says the same\n" +
			"where a destination follows.\n\n" +
			"--branch names a local branch instead of a worktree, for a branch with\n" +
			"none (a stack's layers). A pattern with * or ? (quote it; * crosses a\n" +
			"slash, as for --layer) records one destination per branch it matches, its\n" +
			"* standing in the destination's: --branch 'tmp/layer/*' 'origin/feat/*'.\n" +
			"Each mapping is printed. A pattern matching no branch is refused, and a\n" +
			"refusal of any match records none. --unset works with it; --dry-run says\n" +
			"what would be recorded.",
		Example: "  wt sync push-to login-crash                       # where it pushes, and why\n" +
			"  wt sync push-to login-crash origin/login-crash-2  # record another branch\n" +
			"  wt sync push-to login-crash --unset               # forget what is recorded\n" +
			"  wt sync push-to --branch 'tmp/layer/*' 'origin/feat_wt/*'  # layers with no worktree",
		Args:              cobra.MaximumNArgs(2),
		ValidArgsFunction: completeWork,
		RunE: withContext(func(cmd *cobra.Command, args []string, ctx *kit.Context) error {
			if branch != "" {
				if len(args) > 1 {
					return usagef("--branch takes the destination alone: wt sync push-to --branch <branch> [<remote>/<branch>]")
				}
				dest := ""
				if len(args) == 1 {
					dest = args[0]
				}
				if unset && dest != "" {
					return usagef("--unset takes no <remote>/<branch>: it forgets the one recorded")
				}
				return synccmd.SyncPushToBranch(ctx, branch, dest, unset, dryRun, cmd.OutOrStdout())
			}
			if dryRun {
				return usagef("--dry-run is for --branch")
			}
			work, dest := ".", ""
			if len(args) > 0 {
				work = args[0]
			}
			if len(args) > 1 {
				dest = args[1]
			}
			if unset && dest != "" {
				return usagef("--unset takes no <remote>/<branch>: it forgets the one recorded")
			}
			return synccmd.SyncPushTo(ctx, work, dest, unset, cmd.OutOrStdout())
		}),
	}
	pushTo.Flags().BoolVar(&unset, "unset", false, "forget the destination recorded for the branch")
	pushTo.Flags().StringVar(&branch, "branch", "", "a local branch, with a worktree or not, or a pattern with * or ? (quote it) in place of <work>")
	pushTo.Flags().BoolVar(&dryRun, "dry-run", false, "with --branch: say what would be recorded and record nothing")
	return pushTo
}

// confirmPush asks the question a run or a resume ends with. It defaults to
// no, like every question wt asks: a push is the one step that reaches the
// remote, so Enter must not be what takes it there.
func confirmPush(p *prompter) kit.Confirm[[]string] {
	return func(works []string) bool {
		return p.yesNo("push "+strings.Join(works, ", ")+" with --force-with-lease?", false)
	}
}

// choosePush asks where a branch that tracks a remote branch of another name
// pushes. Enter, or anything but one of the two numbers, is neither: the
// branch is left unpushed and nothing is recorded.
func choosePush(p *prompter) func(syncrun.PushChoice) (string, error) {
	return func(c syncrun.PushChoice) (string, error) {
		_, _ = fmt.Fprintf(p.out, "%s tracks %s, a branch of another name. Where does it push?\n"+
			"A push after a rebase is forced: it replaces what that branch has.\n"+
			"  1  %s, the branch it tracks\n  2  %s, its own name\n1, 2, or Enter to leave it unpushed: ",
			c.Branch, c.Upstream, c.Upstream, c.Own)
		switch line, _ := p.line(); line {
		case "1":
			return c.Upstream, nil
		case "2":
			return c.Own, nil
		}
		return "", nil
	}
}

// addPushFlags declares --push and --no-push on cmd, refused together, and
// returns the choice they make once cmd has parsed its flags.
func addPushFlags(cmd *cobra.Command, pushUsage, noPushUsage string) func() syncrun.PushMode {
	var push, noPush bool
	cmd.Flags().BoolVar(&push, "push", false, pushUsage)
	cmd.Flags().BoolVar(&noPush, "no-push", false, noPushUsage)
	cmd.MarkFlagsMutuallyExclusive("push", "no-push")
	return func() syncrun.PushMode { return pushMode(push, noPush) }
}

// yesPush is the push --yes makes: yes, like every other question it
// answers, unless --no-push was given.
func yesPush(mode syncrun.PushMode) syncrun.PushMode {
	if mode == syncrun.PushNever {
		return syncrun.PushNever
	}
	return syncrun.PushAlways
}

// pushMode is --push and --no-push as one choice; cobra has already refused
// the two together.
func pushMode(push, noPush bool) syncrun.PushMode {
	switch {
	case push:
		return syncrun.PushAlways
	case noPush:
		return syncrun.PushNever
	}
	return syncrun.PushAsk
}
