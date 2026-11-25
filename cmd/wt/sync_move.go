package main

import (
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/spf13/cobra"
)

func newSyncMoveCmd() *cobra.Command {
	var opts synccmd.MoveOptions
	move := &cobra.Command{
		Use:   "move <work> <path>... --from <commit> --into <commit>",
		Short: "Write a move! commit: part of a commit into an earlier one",
		Long: "Write the move! commit that moves whole paths of one commit of the\n" +
			"line (--from) into an earlier one (--into), at the top of the line,\n" +
			"and nothing else: no file, no index and no other branch changes, and\n" +
			"nothing is rebased. The fold makes the move: wt sync rebase <work>\n" +
			"--stack --autosquash --each-commit=targets applies the part at the\n" +
			"earlier commit, after its own fixups, takes it out of the later one,\n" +
			"and drops the move! commit. The tip's tree stays the line's.\n\n" +
			"The commit is an empty one, which can also be written by hand:\n\n" +
			"  move! <into>\n\n" +
			"  Move-From: <from>\n" +
			"  Move-Path: <path>\n\n" +
			"with one Move-Path line per path. --from and --into each take a commit\n" +
			"id or a whole subject, which must name one commit of the line; a\n" +
			"subject two commits share is refused, naming both. The commit is\n" +
			"written with each one's subject, which a fold below does not change,\n" +
			"or its full id where the subject is shared.\n\n" +
			"Before the branch moves the line is checked as the fold would take it,\n" +
			"and a move the fold would refuse is not written: a path the source\n" +
			"does not change, every path of it (make it a fixup! instead), a part\n" +
			"whose context only a commit in between has, a commit in between or a\n" +
			"fold that does not merge clean with the part moved, or a fold that\n" +
			"would end in another tree. Paths only for now: not parts of a file.",
		Example: "  wt sync move login door/refuse.go --from 'feat: the door' --into 'feat: refusals'\n" +
			"  wt sync move login a.go b.go --from 1a2b3c4 --into 5d6e7f8",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeWork,
		RunE: withContext(func(cmd *cobra.Command, args []string, ctx *kit.Context) error {
			opts.Paths = args[1:]
			return synccmd.SyncMove(ctx, args[0], opts, cmd.OutOrStdout())
		}),
	}
	move.Flags().StringVar(&opts.From, "from", "", "the commit the part comes from: an id or its whole subject")
	move.Flags().StringVar(&opts.Into, "into", "", "the earlier commit the part goes into: an id or its whole subject")
	_ = move.MarkFlagRequired("from")
	_ = move.MarkFlagRequired("into")
	return move
}
