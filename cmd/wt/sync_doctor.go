package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
)

func newSyncDoctorCmd() *cobra.Command {
	var fix, prune, asJSON bool
	var repoPath string
	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Check what a run needs, and clear up what old runs left behind",
		Long: "Check what wt sync rebase needs before the first run in a repository and\n" +
			"after anything changes: origin/<trunk> is fetched, .wt-sync.yaml parses,\n" +
			"every script strategy's run exists and is executable on trunk, no\n" +
			"pre-rebase or post-rewrite hook is active, no submodules or LFS paths\n" +
			"are declared, Docker answers when a step declares needs: [docker], and nothing\n" +
			"is left behind by an earlier run: no stale safety ref, no expired lock\n" +
			"(older than 30 minutes, its process gone: a lock whose process still\n" +
			"runs is live however old, and stopping that process frees it),\n" +
			"no worktree stopped mid-rebase. A worktree a run handed over is not a\n" +
			"leftover: the plan row names it, with wt sync resume and wt sync undo.\n" +
			"\n" +
			"Never fixes anything on its own. --fix turns on rerere.enabled and\n" +
			"removes expired locks; --prune deletes safety refs old runs no longer\n" +
			"need. A finding that would make a run refuse outright (a missing\n" +
			"trunk, declaration or script) makes this command exit non-zero; the\n" +
			"rest is advisory. docs/sync-declaration.md describes .wt-sync.yaml.\n" +
			"\n" +
			"With --json it prints wt doctor's object of check records, ids under\n" +
			"sync., and exits 1 when a record is fail. It runs with a broken\n" +
			"worktree.conf, which is then a record too. A check given no answer in\n" +
			"time is unknown. --json changes nothing, so it takes neither --fix nor\n" +
			"--prune.",
		Example: "  wt sync doctor          # what a run needs; changes nothing\n" +
			"  wt sync doctor --json   # the same as records, for tools\n" +
			"  wt sync doctor --repo ../app  # another repository\n" +
			"  wt sync doctor --fix    # turn on rerere, remove expired locks\n" +
			"  wt sync doctor --prune  # delete safety refs no run needs now",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON && (fix || prune) {
				return usagef("--json changes nothing, so it takes neither --fix nor --prune")
			}
			dir, err := repoDir(repoPath)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				// Lenient, as wt doctor is: a broken configuration is a record.
				ctx, err := kit.TryOpenLenient(dir, io.Discard)
				if err != nil {
					return unopened(out, "sync doctor", dir, err, true)
				}
				return synccmd.SyncDoctorJSON(ctx, out)
			}
			// Strict: --fix and --prune write, under the configuration as given.
			ctx, err := kit.Open(dir, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return synccmd.SyncDoctor(ctx, synccmd.DoctorOptions{Fix: fix, Prune: prune}, out)
		},
	}
	doctor.Flags().BoolVar(&fix, "fix", false, "turn on rerere.enabled and remove expired locks")
	doctor.Flags().BoolVar(&prune, "prune", false, "delete prunable safety refs")
	doctor.Flags().BoolVar(&asJSON, "json", false, "print one object of check records")
	addRepoFlag(doctor, &repoPath)
	return doctor
}
