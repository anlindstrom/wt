package main

import (
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/spf13/cobra"
)

func newSyncStepsCmd() *cobra.Command {
	var yes, all, allowOwed, stack bool
	var form jsonForm
	var eachCommit string
	var layers []string
	var push func() syncrun.PushMode
	steps := &cobra.Command{
		Use:   "steps <work>",
		Short: "Run the deferred steps a run left owed, without rebasing onto trunk",
		Long: "Run the deferred steps of .wt-sync.yaml on a worktree as it stands, the\n" +
			"way a run would have, without rebasing it onto trunk: what an agent runs\n" +
			"once Docker answers again, or after fixing something by hand.\n\n" +
			"By default it runs what the last run of the worktree left owed: a step\n" +
			"whose need did not answer, or that failed. Each run records that for the\n" +
			"branch, and this clears what it settles. --all runs every declared step.\n" +
			"Nothing owed and no --all is a refusal that moves nothing.\n\n" +
			"The branch is rebased in place, onto its own base on trunk, with\n" +
			"everything a run does: the safety ref, the each_commit checks, the steps\n" +
			"and wt sync undo. It replays nothing new, so nothing moves but what the\n" +
			"steps write. A step runs at every layer whatever changed there. A step\n" +
			"with each: layer or fold: owner runs at the end of each layer and folds\n" +
			"its output into the commit that owns it, as in wt sync rebase --help; a\n" +
			"step without them runs once at the end and commits its output.\n\n" +
			"--stack takes the line of <work> as wt sync rebase --stack does: the\n" +
			"steps run at every layer tip, bottom first, the line's content must come\n" +
			"out the same outside the steps' paths, and the layers move after. Without\n" +
			"--stack it covers the one branch, whatever stack of worktrees it is in.\n\n" +
			"The each_commit checks run where the run that owed the steps ran them: the\n" +
			"record of what is owed keeps its --each-commit, and the first lines say so.\n" +
			"--each-commit all|tips|targets|off overrides it. targets means tips here:\n" +
			"steps folds no fixup, so each layer's tip is the only target. A record an\n" +
			"older wt wrote names no mode: it runs under all. --all runs every step\n" +
			"and keeps the recorded mode too; with no record to take one from it runs\n" +
			"under all.\n\n" +
			"The push and the questions are as for wt sync rebase (--push, --no-push,\n" +
			"--yes); a step still owed keeps the branch from being pushed. --json\n" +
			"prints one result object on stdout, as wt sync rebase --json does (a\n" +
			"summary, or every row with --full), with command sync steps.",
		Example: "  wt sync steps tmp/stack --stack    # what the last run owed, at every layer\n" +
			"  wt sync steps tmp/stack --stack --each-commit tips  # check each layer's tip only\n" +
			"  wt sync steps login-crash --all    # every declared step, on one branch\n" +
			"  wt sync steps tmp/stack --stack --json --no-push  # a tool, nothing pushed",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWork,
		RunE: func(cmd *cobra.Command, args []string) error {
			if eachCommit != "" && !validEachCommit(eachCommit) {
				return usagef("--each-commit takes all, tips, targets or off, not %q", eachCommit)
			}
			f := runFlags{yes: yes, noFetch: true, allowOwed: allowOwed, push: push(), json: form.on,
				stack: stack, layers: layers}
			if err := f.check(false); err != nil {
				return err
			}
			return withVerbContext(cmd, args, "sync steps", form, func(ctx *kit.Context, j *syncrun.RunJournal) error {
				opts, _ := runOptions(cmd, f, false)
				opts.Journal, opts.Stack = j, f.stackOptions()
				opts.Steps = &synccmd.StepsOptions{All: all, EachCommit: eachCommit}
				return synccmd.SyncRebase(ctx, args, opts, cmd.OutOrStdout())
			})
		},
	}
	push = addPushFlags(steps, "push when done, without asking", "neither push nor ask; print the push command")
	steps.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask first; push too, unless --no-push")
	steps.Flags().BoolVar(&all, "all", false, "run every declared step, not only what the last run left owed")
	steps.Flags().BoolVar(&stack, "stack", false, "run them at every layer tip of the line of <work>, and move the layers")
	steps.Flags().StringArrayVar(&layers, "layer", nil, "with --stack: take this branch as a layer, or every branch a pattern with * or ? matches (quote it); repeatable; none takes every branch on the line but those listed as skipped (backup names, and a branch with no commits of its own that has uncommitted changes in another worktree)")
	steps.Flags().StringVar(&eachCommit, "each-commit", "", "where the each_commit checks run: all, tips, targets (as tips: steps folds no fixup) or off; the default is the mode of the run that owed the steps")
	steps.Flags().BoolVar(&allowOwed, "allow-owed", false, "run even when a step's need does not answer; that step stays owed")
	form.addFlags(steps, "print one result object on stdout, the rest on stderr")
	return steps
}
