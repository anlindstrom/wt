package main

import (
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncwait"
	"github.com/spf13/cobra"
)

func newSyncWaitCmd() *cobra.Command {
	var timeout string
	wait := &cobra.Command{
		Use:   "wait [<work>]",
		Short: "Wait for the run in a worktree to end, showing what it prints",
		Long: "Follow the run in <work> (the worktree you are in by default) until it\n" +
			"ends: print what it printed, from its start and then as it goes (the\n" +
			"checks, the steps, the stops, the timing summary), and exit with the\n" +
			"code the run exits with. For a run in the background: a command in an\n" +
			"agent's shell is cut off after about ten minutes, and a rebase takes longer.\n\n" +
			"A run keeps a copy of what it prints under the repository's .git\n" +
			"(wt-sync-logs/), and each worktree it locks points at it: wt sync rebase,\n" +
			"steps and resume write one, a look and a refusal before any lock do not,\n" +
			"and nor does a rebase across repositories. A wait only reads: it takes\n" +
			"no lock and writes nothing, and ending it leaves the run as it is.\n\n" +
			"Exit: the run's code (0, or 1 for a handover or a failure, with a line\n" +
			"pointing at wt sync resume when it left a plan; 130 for Ctrl-C, 143 for\n" +
			"SIGTERM); 124 when --timeout passed with the run still going (a run that\n" +
			"has ended always exits with its own code: when the timeout passes while\n" +
			"the last of its log is still printing, the rest is left unprinted); 1\n" +
			"when no run was seen in\n" +
			"<work>, when a newer run holds it that wait cannot follow, or when the run\n" +
			"ended without saying how (killed, or a crash: wt sync doctor).",
		Example: "  wt sync rebase tmp/stack --stack --yes > run.log 2>&1 &  # in the background\n" +
			"  wt sync wait tmp/stack --timeout 9m   # shows progress; 124: run again\n" +
			"  wt sync wait                          # the worktree you are in\n" +
			"  wt sync wait tmp/stack && wt sync tmp/stack  # then look at the line",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeWork,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := syncwait.Options{}
			if timeout != "" {
				d, err := time.ParseDuration(timeout)
				if err != nil || d <= 0 {
					return usagef("--timeout takes a Go duration such as 9m or 90s, not %q", timeout)
				}
				opts.Timeout, opts.TimeoutLabel = d, timeout
			}
			work := ""
			if len(args) == 1 {
				work = args[0]
			}
			return withContext(func(cmd *cobra.Command, _ []string, ctx *kit.Context) error {
				return syncwait.Wait(ctx, work, opts, cmd.OutOrStdout())
			})(cmd, args)
		},
	}
	wait.Flags().StringVar(&timeout, "timeout", "", "stop waiting after this long (a Go duration, 9m) and exit 124, the run untouched; none waits for the end")
	return wait
}
