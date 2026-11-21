package main

import (
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncfix"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/spf13/cobra"
)

func newSyncUndoCmd() *cobra.Command {
	var force, yes, asJSON bool
	undo := &cobra.Command{
		Use:   "undo <work>",
		Short: "Put back every ref the last run on this worktree moved",
		Long: "Find the newest run that touched this worktree's branch and reset every\n" +
			"branch that run rewrote back to its safety ref, restoring a stack as a\n" +
			"whole rather than one branch at a time. A rebase wt sync rebase handed over\n" +
			"is aborted and its plan file removed, which puts that branch back. After\n" +
			"wt sync rebase --stack it puts back the line and every layer the run\n" +
			"moved, with the worktrees that moved with them; a layer the run held is\n" +
			"not part of it, whatever its worktree holds now.\n\n" +
			"Refused, and nothing undone: a checkout involved is dirty, has a busy\n" +
			"agent session in it, or is mid-rebase with no handover from wt sync rebase; a\n" +
			"branch that has moved since the run, whose commits the reset would\n" +
			"discard (--force pins those at a fresh safety ref and rewinds anyway); a\n" +
			"branch with a later run, which has to be undone first and which --force\n" +
			"does not override. Nothing is aborted until every branch has passed, and\n" +
			"every abort runs before any branch is reset. If it stops partway it reports\n" +
			"what it did put back; a handover aborted but not rewound says where the\n" +
			"branch still is.\n" +
			"Running it again after it already restored a branch reports that branch\n" +
			"already at its old tip.\n\n" +
			"From a --stack --edit run's edit stop, or any stop after it, the commits\n" +
			"the abort discards are what you made of the edited commit and the line\n" +
			"replayed onto it: they are kept under a fresh safety ref, which the line\n" +
			"put back names, without --force. A commit made by hand at a later stop\n" +
			"is refused as at any other.\n\n" +
			"An agent session idle in a checkout is named and you are asked first\n" +
			"(--yes skips; with no terminal it goes ahead), and each branch put back\n" +
			"under one ends with a wt: line to pass on to it. The session wt itself\n" +
			"runs under is not counted.\n\n" +
			"--json prints one result object on stdout and the rest on stderr, as\n" +
			"wt sync rebase --json does.\n\n" +
			"Also spelled wt sync <work> --undo, with the same flags.",
		Example: "  wt sync undo login-crash            # back to the safety refs\n" +
			"  wt sync undo login-crash --force    # even if the branch moved since\n" +
			"  wt sync login-crash --undo --force  # the same, spelled on wt sync\n" +
			"  wt sync login-crash --undo --yes    # undo --yes, spelled on wt sync\n" +
			"  wt sync undo login-crash --json     # the result as one JSON object",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWork,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVerbContext(cmd, args, "sync undo", asJSON, func(ctx *kit.Context, j *syncrun.RunJournal) error {
				return syncUndo(cmd, args[0], ctx, force, yes, j)
			})
		},
	}
	undo.Flags().BoolVarP(&force, "force", "f", false, "undo a branch that has moved since the run, pinning its tip first")
	undo.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask first when a session is idle in a checkout")
	undo.Flags().BoolVar(&asJSON, "json", false, "print one result object on stdout, the rest on stderr")
	return undo
}

// syncUndo is wt sync undo, whichever way it was spelled.
func syncUndo(cmd *cobra.Command, work string, ctx *kit.Context, force, yes bool, j *syncrun.RunJournal) error {
	opts := syncfix.UndoOptions{Force: force, Journal: j}
	if canAsk(cmd) && !yes {
		opts.Confirm = confirmAsk(newPrompter(cmd.InOrStdin(), cmd.OutOrStdout()), "undo")
	}
	return syncfix.SyncUndo(ctx, work, opts, cmd.OutOrStdout())
}
