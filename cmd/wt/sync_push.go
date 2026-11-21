package main

import (
	"fmt"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/spf13/cobra"
)

func newSyncPushToCmd() *cobra.Command {
	var unset bool
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
			"wt pushes at the end of a rebase and at no other time. For a branch with\n" +
			"something to push now, this command prints the push to run by hand:\n" +
			"after a run left a branch unpushed, that line is what pushes it.\n\n" +
			"<remote>/<branch> is read against the remotes that exist, the longest\n" +
			"remote name first. Trunk is refused, and so is anything but a plain\n" +
			"branch name. The record is branch.<name>.wtPushTo in the repository's\n" +
			"git config, checked again each time it is read; --unset removes it.\n" +
			"<work> is the worktree you are in when left out, and . says the same\n" +
			"where a destination follows.",
		Example: "  wt sync push-to login-crash                       # where it pushes, and why\n" +
			"  wt sync push-to login-crash origin/login-crash-2  # record another branch\n" +
			"  wt sync push-to login-crash --unset               # forget what is recorded",
		Args:              cobra.MaximumNArgs(2),
		ValidArgsFunction: completeWork,
		RunE: withContext(func(cmd *cobra.Command, args []string, ctx *kit.Context) error {
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
