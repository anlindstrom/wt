package main

import (
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/spf13/cobra"
)

func newSyncResumeCmd() *cobra.Command {
	var yes, allowOwed, adopt, allowDropped, asJSON bool
	var push func() syncrun.PushMode
	resume := &cobra.Command{
		Use:   "resume <work>",
		Short: "Continue the rebase a run left at a conflict that was yours",
		Long: "Pick up the rebase wt sync rebase left in this worktree. The plan file in\n" +
			".git/worktrees/<name>/wt-sync-plan.md says what landed, what the declared\n" +
			"strategies already resolved and must not be re-opened, and what is yours.\n" +
			"Resolve those, git add them, then run this.\n\n" +
			"Before continuing it checks that the rebase in progress is the one the run\n" +
			"left, that nothing is unmerged, that nothing tracked is changed but\n" +
			"unstaged, that no file a strategy resolved was hand-merged, and that no\n" +
			"busy agent session is in the worktree. Any of those is a refusal that changes\n" +
			"nothing: this command never resets the worktree, because your own work is\n" +
			"in it, and a file a strategy owns is never yours to merge: that refusal\n" +
			"points at wt sync undo. Then it drives the rest of the rebase, runs the\n" +
			"deferred steps, pins the result ref and ends with the push, asked or not\n" +
			"as for wt sync rebase (--push, --no-push). A later conflict that is yours is\n" +
			"handed over again with a fresh plan file. An agent session idle in the\n" +
			"worktree is named first and you are asked (with no terminal it goes\n" +
			"ahead) before anything is verified, and the finish ends with a wt: line\n" +
			"to pass on to it. The session wt itself runs under is not counted.\n" +
			"--yes answers yes to both, that session and the push (--no-push keeps\n" +
			"the push out).\n\n" +
			"A rebase handed over at an each_commit check that failed waits at that\n" +
			"commit with nothing unmerged: amend the fix in (git commit --amend), or\n" +
			"add a commit, leaving nothing uncommitted, and this runs the check\n" +
			"again at HEAD, going on only when it passes. A step that needs\n" +
			"something that does not answer refuses the resume before it moves\n" +
			"anything, as for wt sync rebase; --allow-owed goes on and owes it.\n\n" +
			"Carrying on yourself with git rebase --continue is fine: a later stop you\n" +
			"left it at goes through the strategies, and a rebase you finished runs\n" +
			"only what comes after it, naming the strategy-resolved files it could not\n" +
			"re-check. Ctrl-C leaves the rebase and its plan as they are; run this\n" +
			"again. wt sync undo <work> aborts a handed-over rebase and puts the branch\n" +
			"back instead.\n\n" +
			"A --stack run handed over moves its layers here, once the rebase is\n" +
			"finished, exactly as the run would have.\n\n" +
			"A rebase you finished by hand and then committed on top of is refused:\n" +
			"resume cannot tell your commits from a deferred step's. --adopt takes it\n" +
			"over when the branch's reflog proves that shape: the rebase's own finish,\n" +
			"from the tip the run started at onto what it rebased onto, then only\n" +
			"commits added on top, no reset, amend, merge or rebase, and nothing\n" +
			"uncommitted. It keeps the tip under refs/wt-sync-kept/<branch>/<epoch>-adopted\n" +
			"before anything moves, moves the layers by the run's own markers (never by\n" +
			"subject), leaves the commits made after the rebase on top and names them,\n" +
			"runs the deferred steps, and runs the each_commit checks at every layer's\n" +
			"tip (none under --each-commit=off). A check that fails, and a per-layer\n" +
			"step the hand finish skipped, is owed, and nothing is pushed. A plain\n" +
			"resume that refuses this shape says --adopt is there. wt sync undo after it\n" +
			"puts the line and its layers back as before the run; the kept ref stays.\n" +
			"Not for a --stack --edit run or a fold.\n\n" +
			"At a stop of a fold (--stack --autosquash) at a fixup moved ahead of its\n" +
			"context, written above the commits that add what it changes, the plan\n" +
			"says so and suggests wt sync undo and moving the hunk into a fixup of the\n" +
			"commit it names. Before anything replays this checks the fixup line by\n" +
			"line: for each line it adds, your resolution plus what the commits still\n" +
			"to be replayed add and remove must give as many copies as the old tip\n" +
			"has, renames followed; a binary file is compared by blob. A resolution\n" +
			"that drops one is refused, naming the path and the lines, and nothing\n" +
			"changes: fix it and run this again. A check that cannot run refuses\n" +
			"with its reason. What it cannot tell (blank lines and braces, a file it\n" +
			"cannot follow) is printed as not checked. --allow-dropped goes on past\n" +
			"either when the drop is meant; the fold's content check at the end\n" +
			"still holds the layers to the old line, except in the files you resolved\n" +
			"at a conflict stop wt handed over: those are taken as you resolved them\n" +
			"(a change to one after the stop is refused), and the run names them.\n\n" +
			"At the stop --stack --edit asked for, HEAD is that commit: amend it, or\n" +
			"add commits, leaving nothing uncommitted, and this replays the rest of\n" +
			"the line onto what HEAD is then. Before the line moves its content is\n" +
			"held to the old line with your change merged in; a difference outside\n" +
			"it refuses, puts the rebase back and keeps what it came to under\n" +
			"refs/wt-sync-kept/<branch>/<epoch>.\n\n" +
			"--json prints one result object on stdout and the rest on stderr, as\n" +
			"wt sync rebase --json does.",
		Example: "  wt sync resume login-crash            # continue what the run handed you\n" +
			"  wt sync resume fix/login-crash --json # by branch; the result as JSON\n" +
			"  wt sync resume login-crash --no-push  # then print the push command\n" +
			"  wt sync resume login-crash --yes      # yes to everything, push included\n" +
			"  wt sync resume login-crash --adopt    # take over a rebase finished by hand\n" +
			"  wt sync resume tmp/stack --allow-dropped  # past a fixup hunk dropped on purpose\n" +
			"  wt sync resume login-crash --push     # then push, without asking",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWork,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVerbContext(cmd, args, "sync resume", asJSON, func(ctx *kit.Context, j *syncrun.RunJournal) error {
				return syncResume(cmd, args[0], ctx, yes, allowOwed, adopt, allowDropped, push(), j)
			})
		},
	}
	push = addPushFlags(resume, "push when done, without asking",
		"neither push nor ask; print the push command")
	resume.Flags().BoolVarP(&yes, "yes", "y", false, "yes to every question: an idle session, and the push")
	resume.Flags().BoolVar(&allowOwed, "allow-owed", false, "resume even when a step's need does not answer; the step is owed, to run later")
	resume.Flags().BoolVar(&adopt, "adopt", false, "take over a rebase finished by hand with commits made on top, when its reflog proves that")
	resume.Flags().BoolVar(&allowDropped, "allow-dropped", false, "go on past a resolution that drops lines of a fixup moved ahead of its context, when the drop is meant")
	resume.Flags().BoolVar(&asJSON, "json", false, "print one result object on stdout, the rest on stderr")
	return resume
}

// syncResume is wt sync resume.
func syncResume(cmd *cobra.Command, work string, ctx *kit.Context, yes, allowOwed, adopt, allowDropped bool, push syncrun.PushMode, j *syncrun.RunJournal) error {
	opts := syncfix.ResumeOptions{Journal: j, Adopt: adopt, AllowDropped: allowDropped}
	opts.AllowOwed = allowOwed
	opts.Push = push
	// --yes answers yes to every question, the push included, unless
	// --no-push says otherwise.
	if yes {
		opts.Push = yesPush(push)
	}
	if canAsk(cmd) && !yes {
		p := newPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
		opts.Confirm = confirmAsk(p, "resume")
		opts.ConfirmPush, opts.ChoosePush = confirmPush(p), choosePush(p)
	}
	return syncfix.SyncResume(ctx, work, opts, cmd.OutOrStdout())
}
