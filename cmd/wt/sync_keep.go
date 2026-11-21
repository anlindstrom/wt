package main

import (
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synckeep"
	"github.com/anlindstrom/wt/internal/commands/synckeep/keepstate"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/spf13/cobra"
)

func newSyncKeepCmd() *cobra.Command {
	keep := &cobra.Command{
		Use:   "keep",
		Short: "Keep the ready worktrees rebased on trunk, unattended",
		Long: "A keeper is wt sync --rebase --yes --push on a timer, with nobody\n" +
			"watching. Each pass fetches trunk and, when its tip has moved since the\n" +
			"last pass, rebases every worktree the table calls ready, pushes what\n" +
			"finished with nothing owed (--force-with-lease --force-if-includes), and\n" +
			"records what it did. Like a run, it fetches each branch's own remote\n" +
			"too, fast-forwards a branch that is behind it before rebasing, and\n" +
			"leaves one that has diverged from it. It is stricter than a run you\n" +
			"start: a worktree\n" +
			"with any agent session in it, idle or busy, is left alone, since nobody\n" +
			"is there to be asked on its behalf; recipe?, needs you and skipped are\n" +
			"left alone as a run with nothing named leaves them, and a stack goes\n" +
			"whole or not at all. A conflict that is yours is never handed over by a\n" +
			"keeper: the worktree was not ready, so it was not taken.\n" +
			"\n" +
			"  wt sync keep once     one pass, what the job runs; usable from cron\n" +
			"  wt sync keep start    install the job (macOS launchd), every 30m by default\n" +
			"  wt sync keep status   installed or not, the last pass, the next one\n" +
			"  wt sync keep stop     unload the job and remove its plist\n" +
			"\n" +
			"Every pass is appended to .git/wt-sync-keep.log in the main checkout:\n" +
			"the time, trunk before and after, what was rebased, pushed and left, and\n" +
			"the run's own output. The log is rotated at 1 MB, the previous file kept\n" +
			"as .log.1. .git/wt-sync-keep.json holds the last pass for wt sync's\n" +
			"kept-at line and wt sync doctor's keeper row; a pass that failed stays\n" +
			"there as a problem until a pass ends with nothing owed. Two passes never\n" +
			"overlap: the json names the running one, and a pass that finds it alive\n" +
			"says so and exits. A pass takes the same locks a run takes and is\n" +
			"refused by them the same way. Without a subcommand this is wt sync keep\n" +
			"status.",
		Example: "  wt sync keep            # is a keeper installed, and what did it last do\n" +
			"  wt sync keep once       # one pass now, by hand or from cron\n" +
			"  wt sync keep status     # the same as wt sync keep",
		Args: cobra.NoArgs,
		RunE: withContext(func(cmd *cobra.Command, _ []string, ctx *kit.Context) error {
			return synckeep.Status(ctx, time.Now(), cmd.OutOrStdout())
		}),
	}
	keep.AddCommand(newSyncKeepRunCmd(), newSyncKeepStartCmd(), newSyncKeepStatusCmd(), newSyncKeepStopCmd())
	return keep
}

func newSyncKeepRunCmd() *cobra.Command {
	var every time.Duration
	var noPush, noFFTrunk bool
	run := &cobra.Command{
		Use:     "once",
		Aliases: []string{"run"},
		Short:   "One pass: fetch, rebase what is ready and nobody is in, push",
		Long: "What the keeper's job runs every interval, and what to run from cron\n" +
			"where there is no launchd. Fetch trunk; when its tip is the one the last\n" +
			"pass recorded, say so and exit 0. Otherwise rebase every ready worktree\n" +
			"as wt sync --rebase --yes would, except that a worktree with any Claude\n" +
			"session in it is left alone and named with the session as the reason,\n" +
			"and push each worktree that finished with nothing owed, with\n" +
			"--force-with-lease --force-if-includes. A branch behind its own remote\n" +
			"is fast-forwarded before it is rebased, as wt sync rebase does it; one that\n" +
			"has diverged from it is left and named. Nothing is asked. The pass is\n" +
			"appended to .git/wt-sync-keep.log and its outcome written to\n" +
			".git/wt-sync-keep.json. The exit code is wt sync rebase's: non-zero when\n" +
			"anything was refused, restored, failed or owed, a push that did not go\n" +
			"through included; that push is then spelled out as a git command.\n" +
			"Every commit a pass makes, replayed or deferred, is unsigned, whatever\n" +
			"your git config says: a signer that asks has nobody to ask. A pass that\n" +
			"cannot list agent sessions (no claude on the PATH, or a listing that\n" +
			"fails) rebases nothing: it cannot tell who is in a worktree. The session\n" +
			"wt itself runs under is not counted, as wt sync rebase does not count it:\n" +
			"under launchd there is none, and a pass you run from inside one is\n" +
			"attended by you.\n" +
			"\n" +
			"A pass that failed does not record trunk, so it is tried again next\n" +
			"interval: a lock a person's run held, or a fetch that timed out, clears\n" +
			"itself that way, and a worktree already rebased is current and skipped.\n" +
			"A refused push is retried every pass until it goes through or the\n" +
			"branch moves; the git command in the log does it by hand. A new\n" +
			"worktree, or a branch that moved, waits for trunk to move. The last\n" +
			"failure stays in the status as a problem until a pass ends with nothing\n" +
			"owed and nothing left unpushed.\n" +
			"\n" +
			"The job passes its interval as --every, for the next-run line the\n" +
			"status shows; nothing else needs it. --no-push rebases only and prints\n" +
			"the push commands, into the log when the job runs it. After every fetch\n" +
			"local <trunk> is fast-forwarded as wt sync rebase does it; --no-ff-trunk,\n" +
			"or ff_trunk false in wt config, leaves it. A pass that finds\n" +
			"another still running exits saying so. Ctrl-C is a run's Ctrl-C: every\n" +
			"lock goes and a worktree caught mid-rebase is named with its way back.",
		Example: "  wt sync keep once              # one pass now\n" +
			"  wt sync keep once --no-push    # rebase only; print the push commands\n" +
			"  wt sync keep once --no-ff-trunk  # leave local trunk where it is",
		Args: cobra.NoArgs,
		RunE: withContext(func(cmd *cobra.Command, _ []string, ctx *kit.Context) error {
			opts := synckeep.KeepOptions{Every: every, Push: syncrun.PushAlways, NoFFTrunk: noFFTrunk}
			if noPush {
				opts.Push = syncrun.PushNever
			}
			return synckeep.Run(ctx, opts, cmd.OutOrStdout())
		}),
	}
	run.Flags().DurationVar(&every, "every", 0, "the job's interval, for the next-run line (default 30m)")
	_ = run.Flags().MarkHidden("every")
	run.Flags().BoolVar(&noPush, "no-push", false, "rebase only; print the push commands")
	run.Flags().BoolVar(&noFFTrunk, "no-ff-trunk", false, "leave local <trunk> where it is after the fetch")
	return run
}

func newSyncKeepStartCmd() *cobra.Command {
	var every time.Duration
	var noPush, dryRun, yes bool
	var sel selectionFlags
	start := &cobra.Command{
		Use:   "start",
		Short: "Install a launchd job that runs wt sync keep once on a timer",
		Long: "Write ~/Library/LaunchAgents/se.wt.sync-keep.<repo>-<hash>.plist, one per\n" +
			"main checkout, running this wt binary's sync keep once from that checkout\n" +
			"every --every (30m), with its output under the checkout's .git, and load\n" +
			"it with launchctl bootstrap. The first pass is one interval after this.\n" +
			"Refused when a job is already installed: wt sync keep status says so.\n" +
			"macOS only; elsewhere this exits 3 and wt sync keep once is for cron.\n" +
			"\n" +
			"A launchd job inherits nothing from your shell, so start captures what\n" +
			"the pass needs into the plist: PATH (git, and claude to see who is in a\n" +
			"worktree), HOME (~/.ssh/config, ~/.gitconfig), TMPDIR, and how git push\n" +
			"authenticates: SSH_AUTH_SOCK, or GIT_SSH_COMMAND. Run start from your\n" +
			"own shell and the job pushes through your ssh agent, as your git does;\n" +
			"with 1Password's agent that works while you are logged in, and it may\n" +
			"ask you to approve the key. Run it from an agent session and the job\n" +
			"gets that launcher's agent-ssh, which works only while fnox has the\n" +
			"agent key cached: start says so. GIT_SSH_COMMAND is captured verbatim,\n" +
			"so keep it free of inline credentials; no secret is written, and the\n" +
			"plist is readable by you alone. A push that fails is\n" +
			"named in the log with the command that does it by hand.\n" +
			"The plist also turns commit signing off for the job: every commit a\n" +
			"pass makes is unsigned, so a signer that asks never has to.\n" +
			"--no-push installs a job that rebases only and logs the push commands.\n" +
			"\n" +
			"--all, --roots or --profile install one job per repository, from\n" +
			"anywhere, each as start in that repository would. Only a repository\n" +
			"whose trunk has a .wt-sync.yaml gets one; the rest are listed as\n" +
			"skipped. One that has a job already keeps it as it is, interval and\n" +
			"all (stop it first to change that), so running this again installs\n" +
			"only what is missing. The plan is printed and you are asked once\n" +
			"(--yes skips; with no terminal and no --yes nothing is installed);\n" +
			"--dry-run prints the plan and installs nothing.\n" + selectionHelp,
		Example: "  wt sync keep start                # every 30m, pushing as this shell does\n" +
			"  wt sync keep start --profile api --every 1h  # a profile's, hourly\n" +
			"  wt sync keep start --no-push      # rebase only; the pushes are yours\n" +
			"  wt sync keep start --all --dry-run  # which repositories would get one\n" +
			"  wt sync keep start --roots work --yes  # a root's, without asking",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := synckeep.KeepStartOptions{Every: every, NoPush: noPush}
			if sel.selection().Any() {
				all := synckeep.KeepStartAllOptions{KeepStartOptions: opts, DryRun: dryRun, Yes: yes}
				if !yes && !dryRun && canAsk(cmd) {
					p := newPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
					all.Ask = func(q string) bool { return p.yesNo(q, false) }
				}
				return synckeep.StartAll(kit.Machine{}, loadUserWarn(cmd.ErrOrStderr()), sel.selection(), all, cmd.OutOrStdout())
			}
			if dryRun || yes {
				return usagef("--dry-run and --yes go with --all, --roots or --profile; start in one repository asks nothing")
			}
			return withContext(func(cmd *cobra.Command, _ []string, ctx *kit.Context) error {
				return synckeep.Start(ctx, opts, cmd.OutOrStdout())
			})(cmd, args)
		},
	}
	start.Flags().DurationVar(&every, "every", keepstate.KeepDefaultInterval, "how often the job runs")
	start.Flags().BoolVar(&noPush, "no-push", false, "the job rebases only and logs the push commands")
	start.Flags().BoolVar(&dryRun, "dry-run", false, "with --all, --roots or --profile: print the plan, install nothing")
	start.Flags().BoolVarP(&yes, "yes", "y", false, "with --all, --roots or --profile: do not ask first")
	start.MarkFlagsMutuallyExclusive("yes", "dry-run")
	sel.add(start, true)
	return start
}

func newSyncKeepStatusCmd() *cobra.Command {
	var sel selectionFlags
	status := &cobra.Command{
		Use:   "status",
		Short: "Whether a keeper is installed, its last pass and its next",
		Long: "Whether the job is installed (its plist present and loaded), its\n" +
			"interval, when the last pass ran and what it did, when the next one is\n" +
			"due, and where the log is. The same as wt sync keep with nothing after it.\n" +
			"\n" +
			"--all, --roots or --profile say it for many repositories from anywhere,\n" +
			"one line each as wt sync doctor's keeper row says it, or why the\n" +
			"repository is not kept. A line marked ! has a problem to look at.\n" +
			selectionHelp,
		Example: "  wt sync keep status     # installed or not, last pass, next pass\n" +
			"  wt sync keep            # the same\n" +
			"  wt sync keep status --all          # one line per repository\n" +
			"  wt sync keep status --roots work   # a root's repositories\n" +
			"  wt sync keep status --profile api  # a profile's",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sel.selection().Any() {
				return synckeep.StatusAll(kit.Machine{}, loadUserWarn(cmd.ErrOrStderr()), sel.selection(), time.Now(), cmd.OutOrStdout())
			}
			return withContext(func(cmd *cobra.Command, _ []string, ctx *kit.Context) error {
				return synckeep.Status(ctx, time.Now(), cmd.OutOrStdout())
			})(cmd, args)
		},
	}
	sel.add(status, true)
	return status
}

func newSyncKeepStopCmd() *cobra.Command {
	var dryRun bool
	var sel selectionFlags
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Unload the keeper's launchd job and remove its plist",
		Long: "launchctl bootout the job and remove its plist. The log and the json\n" +
			"stay, so wt sync still says when the repository was last kept, marked\n" +
			"keeper stopped. macOS only; elsewhere this exits 3.\n" +
			"\n" +
			"--all, --roots or --profile stop every job their repositories have,\n" +
			"from anywhere and without asking: wt sync keep start puts them back.\n" +
			"A repository with no job is listed and left alone. --dry-run lists\n" +
			"what would be stopped and stops nothing.\n" + selectionHelp,
		Example: "  wt sync keep stop       # no more passes; the log stays\n" +
			"  wt sync keep status     # afterwards: stopped\n" +
			"  wt sync keep stop --all --dry-run  # which keepers would stop\n" +
			"  wt sync keep stop --roots work     # a root's keepers\n" +
			"  wt sync keep stop --profile api    # a profile's",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sel.selection().Any() {
				return synckeep.StopAll(kit.Machine{}, loadUserWarn(cmd.ErrOrStderr()), sel.selection(), dryRun, cmd.OutOrStdout())
			}
			if dryRun {
				return usagef("--dry-run goes with --all, --roots or --profile")
			}
			return withContext(func(cmd *cobra.Command, _ []string, ctx *kit.Context) error {
				return synckeep.Stop(ctx, cmd.OutOrStdout())
			})(cmd, args)
		},
	}
	stop.Flags().BoolVar(&dryRun, "dry-run", false, "with --all, --roots or --profile: list, stop nothing")
	sel.add(stop, true)
	return stop
}
