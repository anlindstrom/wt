package main

import (
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/spf13/cobra"
)

func newSyncFixupCmd() *cobra.Command {
	var opts synccmd.FixupOptions
	fixup := &cobra.Command{
		Use:   "fixup <rev>",
		Short: "Commit what is staged as a fixup of one commit of the line",
		Long: "Commit what is staged in the worktree you stand in as a fixup! of <rev>,\n" +
			"a commit of its line, with a subject that names that commit and no other:\n" +
			"its subject, or its id where other commits share the subject or the\n" +
			"subject selects something else. git commit --fixup writes the subject\n" +
			"even then, and the fold refuses it later.\n\n" +
			"<rev> is read as git reads it here: an id, a branch, a tag, HEAD~2. The\n" +
			"commit holds exactly the staged version; unstaged changes stay. No\n" +
			"commit hook runs: the commit is built from the staged tree, signed when\n" +
			"commit.gpgSign says so, and the branch moves to it in one ref\n" +
			"transaction, only from the commit that was its tip and while HEAD is\n" +
			"still on it, with git's index lock held. A signal ends the command with\n" +
			"the locks let go. Nothing is rebased: the fold is wt sync rebase <work>\n" +
			"--stack --autosquash, and wt sync owner --into says beforehand whether a\n" +
			"change folds.\n\n" +
			"Refused, with nothing committed: nothing staged or unmerged paths; a\n" +
			"rebase, merge, cherry-pick, revert or bisect under way; a run's lock on\n" +
			"the worktree, or git's index.lock; no index, or GIT_INDEX_FILE, GIT_DIR\n" +
			"or another override of it set; no branch checked out, or one that is a\n" +
			"symbolic ref; a <rev> that is not on the line or is\n" +
			"itself a fixup!, squash!, amend! or move! commit; and a commit no fixup\n" +
			"subject selects.\n\n" +
			"--dry-run says what would be committed, or why not, and writes nothing.\n" +
			"--json prints one object on stdout (wt schema sync-fixup); a refusal\n" +
			"exits 1.",
		Example: "  wt sync fixup 1a2b3c4            # after git add\n" +
			"  wt sync fixup HEAD~3 --dry-run    # the subject and the files, nothing written",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Rev = args[0]
			ctx, err := openContext(cmd.ErrOrStderr())
			if err != nil {
				return synccmd.RefuseFixup(cmd.OutOrStdout(), opts, err)
			}
			return synccmd.SyncFixup(ctx, opts, cmd.OutOrStdout())
		},
	}
	fixup.Flags().BoolVar(&opts.DryRun, "dry-run", false, "say what would be committed, and write nothing")
	fixup.Flags().BoolVar(&opts.JSON, "json", false, "print one result object on stdout")
	return fixup
}
