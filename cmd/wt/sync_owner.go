package main

import (
	"strings"

	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/wtsync"
	"github.com/spf13/cobra"
)

func newSyncOwnerCmd() *cobra.Command {
	var opts synccmd.OwnerOptions
	owner := &cobra.Command{
		Use:   "owner [<work>] <path>[:<from>-<to>]",
		Short: "Say which commit of the line owns some lines of a file",
		Long: "For the lines of a file, name the commits of the line that last changed\n" +
			"them, newest first, each with its layer: where a fixup! for a change\n" +
			"there belongs. Lines the line's base already has are listed as owned by\n" +
			"no commit of the line. The line is the one of the worktree you stand in,\n" +
			"or of <work>, read as wt sync <work> --stack reads it, against trunk as\n" +
			"last fetched. Nothing is changed.\n\n" +
			"The path is relative to the directory you are in, or with <work> to that\n" +
			"worktree's root. Without :<from>-<to> every line of the file is taken.\n" +
			"Line numbers are inclusive, in the file as committed at the line's tip:\n" +
			"with staged or unstaged changes in or beside those lines the answer\n" +
			"says so, and is still for the committed ones. An owner that is itself a\n" +
			"fixup! is listed with the commit a fold makes it part of.\n\n" +
			"--into <rev> also says whether a change to exactly those lines, committed\n" +
			"as a fixup of <rev>, folds in place (wt sync rebase <work> --stack\n" +
			"--autosquash); without :<from>-<to>, a change to every line. The change\n" +
			"is tried in the object store, replayed with every commit above it. The\n" +
			"answer is one of:\n" +
			"  folds          it applies at <rev> and the rest of the line replays\n" +
			"                 clean on it\n" +
			"  does not fold  a later commit, named, carries its context\n" +
			"  cannot tell    wt cannot say, with the reason: a conflict no later\n" +
			"                 commit explains, which .wt-sync.yaml may resolve; a\n" +
			"                 step or version rule that rewrites a file; a fixup on\n" +
			"                 the line naming its target by a revision (HEAD~1)\n" +
			"  refused        the run would not make the fold, with the reason: what\n" +
			"                 its look refuses (the main checkout, the declaration, a\n" +
			"                 busy session or one that cannot be listed, a run's lock,\n" +
			"                 a need that does not answer, a fixup), a move! commit on\n" +
			"                 the line, which needs --each-commit=targets, or a <rev>\n" +
			"                 no fixup can name\n" +
			"Uncommitted changes do not count, as the change is yet to be committed;\n" +
			"commit or set them aside before the run. The run's checks are not run.\n\n" +
			"Every answer exits 0. A refusal, such as a path the tip does not have or\n" +
			"an <rev> that is not on the line, exits 1. --json prints one object on\n" +
			"stdout for either (wt schema sync-owner).",
		Example: "  wt sync owner door/refuse.go:12-20\n" +
			"  wt sync owner door/refuse.go:12-20 --into 1a2b3c4   # would it fold there?\n" +
			"  wt sync owner login door.go --json                  # every line, another worktree",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Work = ""
			if len(args) == 2 {
				opts.Work = args[0]
			}
			lines, err := ownerLines(args[len(args)-1])
			if err != nil {
				return usagef("%w", err)
			}
			opts.Lines = lines
			ctx, err := openContext(cmd.ErrOrStderr())
			if err != nil {
				return synccmd.RefuseOwner(cmd.OutOrStdout(), opts, err)
			}
			return synccmd.SyncOwner(ctx, opts, cmd.OutOrStdout())
		},
	}
	owner.Flags().StringVar(&opts.Into, "into", "", "also say whether a change to the lines folds into `rev`, a commit of the line")
	owner.Flags().BoolVar(&opts.JSON, "json", false, "print one result object on stdout")
	return owner
}

// ownerLines reads <path> or <path>:<from>-<to>: what follows the last colon
// is a range only when it is digits and dashes, so a colon in a name stands.
func ownerLines(arg string) (wtsync.LineRange, error) {
	colon := strings.LastIndex(arg, ":")
	if colon < 0 || strings.Trim(arg[colon+1:], "0123456789-") != "" {
		return wtsync.LineRange{Path: arg}, nil
	}
	return wtsync.ParseLineRange(arg)
}
