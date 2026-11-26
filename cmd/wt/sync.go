package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/reposel"
	"github.com/anlindstrom/wt/internal/runlog"
	"github.com/anlindstrom/wt/internal/wtsync"
)

func newSyncCmd() *cobra.Command {
	var noFetch bool
	sync := &cobra.Command{
		Use:   "sync [<work>]",
		Short: "Show what a rebase onto trunk would do to each worktree",
		Long: "Fetch trunk, then simulate rebasing every worktree onto origin/<trunk> in\n" +
			"the object store, applying the repository's declared strategies at every\n" +
			"stop, and print the outcome grouped by what to do about it. The fetch moves\n" +
			"only remote-tracking refs, origin/<trunk> and each branch's own remote;\n" +
			"nothing of yours is changed. --no-fetch compares with both as last fetched,\n" +
			"and so does a fetch that fails, saying why.\n" +
			"\n" +
			"The flow is look, act, finish. Sync is the whole of it: the fetch, the\n" +
			"simulation, the safety ref, the strategies at each stop, the deferred\n" +
			"steps, the push and the undo. rebase is the method, how the commits get\n" +
			"onto trunk, and the one method there is.\n" +
			"  look    wt sync                   every worktree, grouped; changes\n" +
			"                                    nothing of yours\n" +
			"          wt sync <work> --stack    the layer branches on its line, bottom\n" +
			"                                    first, and what holds each; with\n" +
			"                                    --autosquash, the fixups a fold\n" +
			"                                    moves ahead of their context\n" +
			"          wt sync <work> --alone    that worktree, and which of its stack a\n" +
			"                                    run of it alone would leave\n" +
			"          wt sync <work>            one worktree in full: every stop,\n" +
			"                                    file and key\n" +
			"          wt sync <work> --onto <ref>\n" +
			"                                    the same against <ref>, not trunk\n" +
			"  act     wt sync rebase <work>...  sync them by rebasing onto trunk,\n" +
			"                                    or the ref --onto names:\n" +
			"                                    safety ref, strategies at each stop,\n" +
			"                                    deferred steps; asks once when more\n" +
			"                                    than one worktree is involved or a\n" +
			"                                    session is idle in one (--yes skips)\n" +
			"          wt sync rebase            every worktree the table calls ready,\n" +
			"                                    asked first\n" +
			"          wt sync rebase <work> --if-ready\n" +
			"          wt up                     the same for the worktree you are in\n" +
			"                                    only if it goes through without\n" +
			"                                    needing you; else say why, touch\n" +
			"                                    nothing, and fail\n" +
			"  finish  wt sync resume <work>     continue the rebase a run left at a\n" +
			"                                    conflict that is yours, then push,\n" +
			"                                    asked first as for rebase\n" +
			"          wt sync push <work> --stack\n" +
			"                                    push a --stack line rebased with\n" +
			"                                    --no-push, over the run's leases\n" +
			"          wt sync undo <work>       put back every ref that run moved\n" +
			"          wt sync steps <work>      run the steps a run left owed, in\n" +
			"                                    place, without rebasing onto trunk\n" +
			"          wt sync move <work> [<path>...] --from <c> --into <c>\n" +
			"                                    write a move! commit, which the\n" +
			"                                    next fold makes\n" +
			"          wt sync doctor            what a run needs, and --fix / --prune\n" +
			"  keep    wt sync keep start        a launchd job that runs wt sync keep\n" +
			"                                    once every 30m: every ready worktree\n" +
			"                                    with nobody in it, then push\n" +
			"Nobody means no agent session, Claude's or Codex's.\n" +
			"\n" +
			"wt sync rebase with no worktree named takes every worktree under ready\n" +
			"except recipe?, lists what it leaves alone, and asks before moving even\n" +
			"one (--yes skips). --if-ready rebases what is ready and fails if anything\n" +
			"was not (wt sync rebase --help). Each verb's own flags, --yes (-y), --push,\n" +
			"--no-push and --force among them, are its own: wt sync rebase --help, wt\n" +
			"sync resume --help, wt sync undo --help.\n" +
			"\n" +
			"--all, --roots or --profile cover many repositories from anywhere: the\n" +
			"overview of each; wt sync rebase takes every ready worktree in each, planned\n" +
			"a few repositories at a time, asked once for all of them, then rebased\n" +
			"one repository after another. A repository whose trunk declares no\n" +
			".wt-sync.yaml is listed, not failed.\n" + selectionHelp + "\n" +
			"\n" +
			"--onto <ref> looks at the worktree named against <ref> instead of\n" +
			"trunk, and wt sync rebase --onto rebases it onto <ref>: an integration branch,\n" +
			"origin/<branch>, a tag or a commit. A remote-tracking ref is fetched\n" +
			"with trunk (--no-fetch takes it as last fetched); anything else is\n" +
			"used as it stands. --upstream <ref> with it takes only the commits\n" +
			"above that ref, git's rebase --onto <onto> <upstream>: a layer moved\n" +
			"off one branch onto another. The declaration is still read from trunk.\n" +
			"A target is chosen per piece of work, so --onto needs a worktree named\n" +
			"and takes no --all, --roots or --profile (wt sync rebase --help).\n" +
			"\n" +
			"Groups:\n" +
			"  ready      conflict-free or recipe, with nothing in the way: wt sync\n" +
			"             rebase <work>, or wt sync rebase for all of them but\n" +
			"             recipe?.\n" +
			"             Also one behind its own remote with nothing to rebase\n" +
			"             there: a run fast-forwards it\n" +
			"  needs you  contested, divergent, dirty, handed over, diverged from its\n" +
			"             own remote, or not assessed\n" +
			"  skipped    a busy session is in it, nothing is ahead of trunk, or no branch\n" +
			"\n" +
			"Classes:\n" +
			"  conflict-free\n" +
			"             rebases without a conflict\n" +
			"  recipe     every conflict at every stop is claimed by a strategy in\n" +
			"             .wt-sync.yaml; a run completes on its own. recipe? means a\n" +
			"             script owns a path and the replay could not be carried past\n" +
			"             it: a run may still stop later, and hands you a plan if it does\n" +
			"  contested  a conflict somewhere in the replay is nobody's; a run rebases\n" +
			"             up to it, stages what the strategies did resolve, and leaves a\n" +
			"             plan file \u2014 finish it and wt sync resume <work>. A branch\n" +
			"             with a stack above it is put back instead, so a stack is\n" +
			"             never half-applied\n" +
			"  divergent  branch and trunk moved apart structurally; never rebased\n" +
			"             automatically\n" +
			"  stale      nothing ahead of trunk; skipped\n" +
			"  current    already on trunk; not printed\n" +
			"  unknown    the assessment hit an error; the line under it says which\n" +
			"\n" +
			"Under each worktree: the stop that decides the class \u2014 the first one\n" +
			"that is yours, or the first of a run that resolves throughout \u2014 with its\n" +
			"files marked \u2713 resolved or \u2717 yours, then anything else worth knowing,\n" +
			"every list cut to a count. wt sync <work> prints the lists, one item per\n" +
			"line. A session named on a row is an agent session in that worktree,\n" +
			"Claude's or Codex's: a busy one keeps every verb off it. A Codex session\n" +
			"is a codex process working there (codex exec is always busy; an\n" +
			"interactive codex is idle once its last turn has finished) or a thread\n" +
			"the Codex app runs there with a turn open. One marked (idle) is waiting\n" +
			"for its person; a verb names it, asks first, and ends with a wt: line to\n" +
			"pass on to it. +N counts the other sessions there. The session wt itself\n" +
			"runs under is not counted. The lines under a row are advisory and never\n" +
			"change the class. A row whose run would check commits with the\n" +
			"each_commit steps says how many; wt sync <work> lists them. Nothing is\n" +
			"run to say so. A need a step that would run declares (needs: [docker])\n" +
			"is probed, and one that does not answer is named under the table.\n" +
			"\n" +
			".wt-sync.yaml on trunk declares all of this: the strategies, the\n" +
			"deferred and each_commit steps, their needs. docs/sync-declaration.md\n" +
			"describes every key.\n" +
			"\n" +
			"A branch is also compared with its own remote, the ref a push of it would\n" +
			"replace: what is recorded for it, else its own name on the remote git\n" +
			"pushes it to, never its upstream as such (wt sync push-to --help).\n" +
			"Behind it, a run fast-forwards the branch first, and the row is\n" +
			"judged as if it had. Diverged from it, with commits there the branch\n" +
			"never had, a run refuses: needs you (wt sync rebase --help).\n" +
			"\n" +
			"--json prints the overview as one object on stdout, for a tool driving\n" +
			"wt: every worktree with its group, class, stack and stops, current ones\n" +
			"included, the deferred and each_commit steps trunk declares, which a run\n" +
			"may perform, the commits a run would check, what each need's probe\n" +
			"answered, and a token for wt sync rebase --expect. With --all, --roots or\n" +
			"--profile it is an array, one object per repository. With a verb it is\n" +
			"that verb's result instead (wt sync rebase --help). wt schema sync and\n" +
			"wt schema sync-run print their JSON Schemas; docs/json.md explains them.",
		Example: "  wt sync --no-fetch --json     # every worktree as JSON, trunk as last fetched\n" +
			"  wt sync login-crash           # that worktree in full\n" +
			"  wt sync rebase . --if-ready   # rebase it only if it needs nothing from you\n" +
			"  wt sync login-crash --onto test_wt/integration  # against another branch\n" +
			"  wt sync rebase --all --if-ready  # every ready worktree, every repository\n" +
			"  wt sync --profile api         # the overview of a profile's repositories",
		ValidArgsFunction: completeWork,
	}
	var asJSON, stack, autosquash, alone, noSides bool
	var onto, upstream string
	var layers []string
	var sel selectionFlags
	sync.Args = cobra.MaximumNArgs(1)
	sync.RunE = func(cmd *cobra.Command, args []string) error {
		f := runFlags{noFetch: noFetch, json: asJSON, stack: stack, layers: layers, autosquash: autosquash,
			onto: onto, upstream: upstream, alone: alone, noSides: noSides}
		if err := f.check(true); err != nil {
			return err
		}
		if err := f.checkOnto(args, sel.selection()); err != nil {
			return err
		}
		if err := f.checkAlone(args, sel.selection()); err != nil {
			return err
		}
		if f.stack && (sel.selection().Any() || len(args) != 1) {
			return usagef("--stack reads the line of one worktree: name the one at its top (wt sync <work> --stack)")
		}
		if f.json {
			return syncOverviewJSON(cmd, args, sel.selection(), f)
		}
		if sel.selection().Any() {
			return syncAcross(cmd, args, false, sel.selection(), f)
		}
		// Lenient: a repository without worktree.conf still has worktrees
		// worth reporting on, and the trunk name falls back to origin/HEAD.
		ctx, err := openLenient(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		if ctx == nil {
			return kit.ErrNotInRepo
		}
		opts := synccmd.SyncOptions{NoFetch: noFetch, Onto: onto, Upstream: upstream, Alone: alone}
		if f.stack {
			opts.Stack = &synccmd.StackLook{Work: args[0], Layers: layers, Autosquash: autosquash, NoSideBranches: noSides}
		}
		if len(args) == 1 {
			return synccmd.SyncWorktree(ctx, args[0], opts, cmd.OutOrStdout())
		}
		return synccmd.Sync(ctx, opts, cmd.OutOrStdout())
	}
	sync.Flags().BoolVar(&noFetch, "no-fetch", false, "compare with origin/<trunk> as last fetched")
	sync.Flags().BoolVar(&asJSON, "json", false, "print the overview as one JSON object on stdout")
	sync.Flags().BoolVar(&stack, "stack", false, "list the layer branches on the line of the worktree named, and what holds each")
	sync.Flags().StringArrayVar(&layers, "layer", nil, "with --stack: take this branch as a layer, or every branch a pattern with * or ? matches (quote it); repeatable; none takes every branch on the line but those listed as skipped (backup names, and a branch with no commits of its own that has uncommitted changes in another worktree)")
	sync.Flags().BoolVar(&alone, "alone", false, "look at the one worktree named without the rest of its stack: which of it a run of it alone would leave")
	sync.Flags().BoolVar(&autosquash, "autosquash", false, "with --stack: name the fixups a fold moves ahead of their context")
	sync.Flags().BoolVar(&noSides, "no-side-branches", false, "with --stack: leave out the branches that fork from the line or sit above its top; a run then never moves them")
	addOntoFlags(sync, &onto, &upstream, "look at the worktree named against <ref> instead of trunk")
	sel.add(sync, true)
	sync.AddCommand(newSyncRebaseCmd(), newSyncResumeCmd(), newSyncUndoCmd(), newSyncPushCmd(), newSyncStepsCmd(), newSyncWaitCmd(), newSyncPushToCmd(), newSyncMoveCmd(), newSyncDoctorCmd(), newSyncKeepCmd())
	return sync
}

// runFlags is what a wt sync rebase or steps run, or the overview of wt sync,
// takes from its flags; the overview sets only the few it has.
type runFlags struct {
	yes, force, noFetch bool
	ifReady, noFFTrunk  bool
	// allowDiverged rebases a branch that has diverged from its own remote.
	allowDiverged bool
	// allowOwed rebases even when a step's need does not answer its probe:
	// that step is owed.
	allowOwed bool
	// alone rebases the one worktree named without the rest of its stack.
	alone bool
	push  syncrun.PushMode
	// pushLine includes a --stack line's own branch in the push when that
	// would create its remote branch.
	pushLine bool
	// json prints one object on stdout: the overview, or the run's result.
	json bool
	// expect is the overview's token a run must still match.
	expect string
	// stack is a stack held on the named worktree's line: its layers, and
	// for a run the one rebase of the line that moves them all.
	stack      bool
	layers     []string
	autosquash bool
	// noSides leaves the branches that hang off a --stack line alone.
	noSides bool
	// edit is the commit of the line a --stack run stops at for a person.
	edit string
	// eachCommit is where the each_commit checks run: all, tips or off; ""
	// is all.
	eachCommit string
	// onto is the ref the named worktrees are looked at against, or
	// rebased onto, instead of trunk; upstream where their commits start.
	onto, upstream string
}

// check refuses flags that do not go together. look is the overview of wt
// sync, which has no run to give --edit, --each-commit and the rest to.
func (f runFlags) check(look bool) error {
	switch {
	case len(f.layers) > 0 && !f.stack:
		return usagef("--layer needs --stack")
	case f.autosquash && !f.stack:
		return usagef("--autosquash needs --stack")
	case f.noSides && !f.stack:
		return usagef("--no-side-branches needs --stack")
	case f.edit != "" && !f.stack:
		return usagef("--edit needs --stack")
	case f.edit != "" && f.autosquash:
		return usagef("--edit and --autosquash are two runs: edit in place first (--stack --edit <rev>), then fold (--stack --autosquash)")
	case f.edit != "" && f.onto != "":
		return usagef("--edit and --onto are two runs: edit in place first (--stack --edit <rev>), then move (--stack --onto <ref>)")
	case f.eachCommit != "" && !validEachCommit(f.eachCommit):
		return usagef("--each-commit takes all, tips, targets or off, not %q", f.eachCommit)
	case f.eachCommit == wtsync.EachTargets && (!f.autosquash || f.onto != "" || f.edit != ""):
		return usagef("%s", wtsync.TargetsRefusal(f.edit != ""))
	case f.eachCommit == wtsync.EachTips && f.edit != "":
		return usagef("--each-commit=tips and --edit are two runs: an edit run's checks sit at the commits above the edit; use --each-commit=off, or edit first and check after")
	case look && f.alone && f.json:
		return usagef("--alone with --json needs wt sync rebase: the look has no JSON of it")
	case f.alone && f.edit != "":
		return usagef("--alone and --edit: --edit moves the layers by the amend, and --alone moves none; edit first without --alone")
	case f.alone && f.onto != "":
		return usagef("--alone and --onto: --onto already moves exactly the worktrees named and none of their stack; leave --alone out")
	case f.upstream != "" && f.onto == "":
		return usagef("--upstream needs --onto: it says where the commits rebased onto --onto's ref start")
	}
	return nil
}

// validEachCommit is whether mode is one --each-commit takes.
func validEachCommit(mode string) bool {
	_, err := wtsync.EachCommitMode(mode)
	return err == nil
}

// checkOnto refuses --onto where it has no piece of work to go with: a
// target is chosen per piece of work.
func (f runFlags) checkAlone(args []string, sel reposel.Selection) error {
	switch {
	case !f.alone:
		return nil
	case sel.Any():
		return usagef("--alone rebases one worktree of one repository; leave out --all, --roots and --profile")
	case len(args) != 1:
		return usagef("--alone rebases one worktree without the rest of its stack: name the one (wt sync rebase <work> --alone)")
	}
	return nil
}

func (f runFlags) checkOnto(args []string, sel reposel.Selection) error {
	switch {
	case f.onto == "":
		return nil
	case sel.Any():
		return usagef("--onto is chosen per piece of work; leave out --all, --roots and --profile, and name the worktree")
	case len(args) == 0:
		return usagef("--onto takes the worktree to rebase onto it: name it (wt sync <work> --onto <ref>)")
	}
	return nil
}

// addOntoFlags adds --onto and --upstream to cmd.
func addOntoFlags(cmd *cobra.Command, onto, upstream *string, ontoUsage string) {
	cmd.Flags().StringVar(onto, "onto", "", ontoUsage)
	cmd.Flags().StringVar(upstream, "upstream", "", "with --onto: only the commits above <ref> on the branch, git's rebase --onto <onto> <upstream>")
}

// stackOptions is the --stack run the flags ask for, nil for any other.
func (f runFlags) stackOptions() *synccmd.StackOptions {
	if !f.stack {
		return nil
	}
	return &synccmd.StackOptions{Layers: f.layers, Autosquash: f.autosquash, Edit: f.edit, NoSideBranches: f.noSides}
}

// syncOverviewJSON is wt sync --json: the overview as one object, or with
// --all, --roots or --profile an array of one per repository.
func syncOverviewJSON(cmd *cobra.Command, args []string, sel reposel.Selection, f runFlags) error {
	opts := synccmd.SyncOptions{NoFetch: f.noFetch, Onto: f.onto, Upstream: f.upstream}
	if f.stack {
		opts.Stack = &synccmd.StackLook{Work: args[0], Layers: f.layers, Autosquash: f.autosquash, NoSideBranches: f.noSides}
	}
	if f.onto != "" {
		opts.Work = args[0]
	}
	switch {
	case len(args) > 0 && !f.stack && f.onto == "":
		return usagef("--json prints the overview of every worktree; name none, or add the verb whose result you want")
	case sel.Any():
		return synccmd.SyncAllJSON(loadUserWarn(cmd.ErrOrStderr()), sel, opts, cmd.OutOrStdout())
	}
	ctx, err := openLenient(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if ctx == nil {
		return kit.ErrNotInRepo
	}
	return synccmd.SyncJSON(ctx, opts, cmd.OutOrStdout())
}

// rebaseCommand names wt sync rebase in its --json result.
const rebaseCommand = "sync rebase"

// noRunLog is a test hook: it skips the copy, to compare a run's output with
// and without it.
var noRunLog bool

// runLogged are the verbs whose output wt sync wait can follow.
var runLogged = map[string]bool{rebaseCommand: true, "sync steps": true, "sync resume": true}

// withVerbContext runs a wt sync verb in the repository it is in. With
// asJSON its result is one object on stdout, written however the verb ends,
// and everything it prints, questions included, goes to stderr; command
// names the verb in that object.
func withVerbContext(cmd *cobra.Command, args []string, command string, asJSON bool,
	fn func(ctx *kit.Context, j *syncrun.RunJournal) error) error {
	if runLogged[command] && !noRunLog {
		// The run's log is a copy of what it prints to the person: both
		// writers copy into it, the JSON result object (written on the
		// original stdout below) never does.
		log := runlog.Begin(cmd.ErrOrStderr())
		cmd.SetErr(log.Wrap(cmd.ErrOrStderr()))
		if !asJSON {
			cmd.SetOut(log.Wrap(cmd.OutOrStdout()))
		}
	}
	if !asJSON {
		return withContext(func(_ *cobra.Command, _ []string, ctx *kit.Context) error {
			return fn(ctx, nil)
		})(cmd, args)
	}
	journal := syncrun.NewSyncRunJournal(cmd.OutOrStdout(), command)
	cmd.SetOut(cmd.ErrOrStderr())
	ctx, err := openContext(cmd.ErrOrStderr())
	if err != nil {
		journal.Fail(err)
		return err
	}
	return fn(ctx, journal)
}

// runOptions is a run's options from its flags, and the prompter asking its
// questions, nil with no terminal. One prompter for every question, so an
// answer typed ahead for the push is not lost to the rebase question's reader.
//
// --yes answers yes to every question, the push included, unless --no-push
// says otherwise. A bulk run — nothing named, or many
// repositories — with nobody at a terminal to ask and no --yes rebases
// nothing, as a sweep deletes nothing: what nobody named and nobody
// confirmed does not move. A worktree named on the line goes ahead.
func runOptions(cmd *cobra.Command, f runFlags, bulk bool) (synccmd.RunOptions, *prompter) {
	opts := synccmd.RunOptions{NoFetch: f.noFetch, NoFFTrunk: f.noFFTrunk, IfReady: f.ifReady, Force: f.force,
		AllowDiverged: f.allowDiverged, Onto: f.onto, Upstream: f.upstream, EachCommit: f.eachCommit}
	opts.AllowOwed = f.allowOwed
	opts.Push = f.push
	opts.PushLine = f.pushLine
	switch {
	case f.yes:
		opts.Push = yesPush(f.push)
		return opts, nil
	case !canAsk(cmd):
		if bulk {
			opts.Confirm = noTerminal(cmd.OutOrStdout())
		}
		return opts, nil
	}
	p := newPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
	opts.Confirm = confirmAsk(p, "rebase")
	opts.ConfirmPush, opts.ChoosePush = confirmPush(p), choosePush(p)
	return opts, p
}

// noTerminal is the answer to a bulk run's question when nobody is there to
// give it: no, with how to say yes.
func noTerminal(w io.Writer) kit.Confirm[[]string] {
	return func(works []string) bool {
		fmt.Fprintf(w, "There is no terminal to ask. Pass --yes to rebase %s.\n", strings.Join(works, ", "))
		return false
	}
}

// syncAcross is wt sync or wt sync rebase with --all, --roots or --profile:
// the overview of every repository they name, or with rebase a run across all
// of them.
func syncAcross(cmd *cobra.Command, args []string, rebase bool, sel reposel.Selection, f runFlags) error {
	u := loadUserWarn(cmd.ErrOrStderr())
	switch {
	case len(args) > 0:
		return usagef("--all, --roots and --profile cover whole repositories; name no worktree with them")
	case f.json && rebase:
		return usagef("--json reports a run in one repository; leave out --all, --roots and --profile")
	case f.expect != "":
		return usagef("--expect holds a run in one repository to its overview; leave out --all, --roots and --profile")
	case !rebase:
		return synccmd.SyncAll(u, sel, synccmd.SyncOptions{NoFetch: f.noFetch}, cmd.OutOrStdout())
	}
	opts, p := runOptions(cmd, f, true)
	all := synccmd.SyncRebaseAllOptions{RunOptions: opts}
	switch {
	case p != nil:
		all.Ask = func(q string) bool { return p.yesNo(q, false) }
	case !f.yes:
		all.Ask = func(q string) bool {
			fmt.Fprintf(cmd.OutOrStdout(), "There is no terminal to ask. Pass --yes to %s.\n",
				strings.ToLower(strings.TrimSuffix(q, "?")))
			return false
		}
	}
	return synccmd.SyncRebaseAll(u, sel, all, cmd.OutOrStdout())
}

// confirmAsk asks the one question an acting command gets before it changes
// anything, defaulting to no. What it is about has been printed by then.
func confirmAsk(p *prompter, verb string) kit.Confirm[[]string] {
	return func(works []string) bool {
		return p.yesNo(verb+" "+strings.Join(works, ", ")+"?", false)
	}
}
