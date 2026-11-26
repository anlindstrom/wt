package main

import (
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/wtsync"
	"github.com/spf13/cobra"
)

func newSyncMoveCmd() *cobra.Command {
	var opts synccmd.MoveOptions
	var ranges []string
	move := &cobra.Command{
		Use:   "move <work> [<path>...] --from <commit> --into <commit>",
		Short: "Write a move! commit: part of a commit into an earlier one",
		Long: "Write the move! commit that moves part of one commit of the line\n" +
			"(--from) into an earlier one (--into), at the top of the line, and\n" +
			"nothing else: no file, no index and no other branch changes, and\n" +
			"nothing is rebased. The part is whole paths, line ranges (--lines), or\n" +
			"both. The fold makes the move: wt sync rebase <work> --stack\n" +
			"--autosquash --each-commit=targets applies the part at the earlier\n" +
			"commit, after its own fixups, takes it out of the later one, and drops\n" +
			"the move! commit. The tip's tree stays the line's.\n\n" +
			"A range is <path>:<from>-<to>: line numbers, inclusive, in the file as\n" +
			"--from leaves it. It takes every hunk of --from's diff of that file it\n" +
			"overlaps (git diff, three lines of context), and must cover each one's\n" +
			"changed lines whole. A hunk that only removes lines is named by the\n" +
			"line after them. A removal at the end of a file, a deleted or emptied\n" +
			"file and a binary file have no line to name: move the path whole. A\n" +
			"change of a file's mode stays with --from.\n\n" +
			"The commit is an empty one, which can also be written by hand:\n\n" +
			"  move! <into>\n\n" +
			"  Move-From: <from>\n" +
			"  Move-Path: <path>\n" +
			"  Move-Lines: <path>:<from>-<to>\n\n" +
			"with one Move-Path line per path and one Move-Lines line per range; a\n" +
			"path is named by one of the two, not both. --from and --into each take\n" +
			"a commit id or a whole subject, which must name one commit of the line;\n" +
			"a subject two commits share is refused, naming both. The commit is\n" +
			"written with each one's subject, which a fold below does not change,\n" +
			"or its full id where the subject is shared.\n\n" +
			"Before the branch moves the line is checked as the fold would take it,\n" +
			"and a move the fold would refuse is not written: a path the source does\n" +
			"not change, a range that splits a hunk or selects none, all of the\n" +
			"source in one move (make it a fixup! instead), a hunk another move!\n" +
			"commit takes, a part whose context only a commit in between has or that\n" +
			"would land elsewhere than the source has it, a commit in between or a\n" +
			"fold that does not merge clean with the part moved, or a fold that would\n" +
			"end in another tree. Several moves into different commits may take all\n" +
			"of a source between them; the fold then drops it as empty.\n\n" +
			"--json prints one object on stdout, for a commit written or refused\n" +
			"(wt schema sync-move); a refusal still exits non-zero.",
		Example: "  wt sync move login door/refuse.go --from 'feat: the door' --into 'feat: refusals'\n" +
			"  wt sync move login a.go b.go --from 1a2b3c4 --into 5d6e7f8\n" +
			"  wt sync move login --lines door.go:12-20 --lines door.go:48-48 --from 1a2b3c4 --into 5d6e7f8  # two hunks",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeWork,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Paths = args[1:]
			opts.Lines = nil
			for _, r := range ranges {
				lines, err := wtsync.ParseLineRange(r)
				if err != nil {
					return usagef("--lines %w", err)
				}
				opts.Lines = append(opts.Lines, lines)
			}
			if len(opts.Paths)+len(opts.Lines) == 0 {
				return usagef("wt sync move needs a path or --lines, such as: wt sync move %s --lines door.go:12-20 --from <commit> --into <commit>", args[0])
			}
			ctx, err := openContext(cmd.ErrOrStderr())
			if err != nil {
				return synccmd.RefuseMove(cmd.OutOrStdout(), args[0], opts, err)
			}
			return synccmd.SyncMove(ctx, args[0], opts, cmd.OutOrStdout())
		},
	}
	move.Flags().StringVar(&opts.From, "from", "", "the commit the part comes from: an id or its whole subject")
	move.Flags().StringVar(&opts.Into, "into", "", "the earlier commit the part goes into: an id or its whole subject")
	move.Flags().StringArrayVar(&ranges, "lines", nil, "move the hunks these lines cover, `path:from-to` as --from leaves the file; repeatable")
	move.Flags().BoolVar(&opts.JSON, "json", false, "print one result object on stdout")
	_ = move.MarkFlagRequired("from")
	_ = move.MarkFlagRequired("into")
	return move
}
