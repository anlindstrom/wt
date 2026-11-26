package main

import (
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/synccmd"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/spf13/cobra"
)

func newSyncRebaseCmd() *cobra.Command {
	var noFetch, noFFTrunk, yes, ifReady, force, allowDiverged, allowOwed, asJSON, stack, autosquash, alone, noSides bool
	var expect, onto, upstream, edit, eachCommit string
	var layers []string
	var pushLine bool
	var sel selectionFlags
	var push func() syncrun.PushMode
	rebase := &cobra.Command{
		Use:   "rebase [<work>...]",
		Short: "Rebase named worktrees onto trunk, or the ref --onto names",
		Long: "Sync the named worktrees with trunk, by rebasing them onto it, or onto\n" +
			"the ref --onto names.\n\n" +
			"Fetch trunk once, then for each named worktree (and the rest of any\n" +
			"stack it belongs to, parents first): pin the old tip under\n" +
			"refs/wt-sync/<branch>/<epoch>, rebase with --no-update-refs --no-gpg-sign,\n" +
			"apply the declared strategy at every stop, and run the deferred steps\n" +
			"once at the end, committing their output when it changes tracked files.\n" +
			"A stop no strategy resolves is left in place, with what the strategies did\n" +
			"resolve staged and a plan file naming what is yours, for wt sync resume to\n" +
			"finish. A branch with a stack above it in the same run is put back instead,\n" +
			"so a stack is never half-applied. A failed deferred step is reported as\n" +
			"owed and never undoes the rebase.\n\n" +
			"A step under each_commit: in .wt-sync.yaml runs after every replayed\n" +
			"commit that changes one of its paths, at that commit once it is final,\n" +
			"each under its own 30-minute limit, with a line per commit. One that\n" +
			"fails, or changes a tracked file, stops the rebase there: HEAD is the\n" +
			"commit, nothing is unmerged, and a plan file names the check. Amend the\n" +
			"fix into the commit, or add one, and wt sync resume runs the check again\n" +
			"before it goes on. A stack parent is put back instead.\n\n" +
			"--each-commit=tips runs the each_commit checks only at the tip of each\n" +
			"layer of a --stack line (and the line's own tip), or at the branch's tip\n" +
			"without --stack: for a line whose commits do not compile one by one,\n" +
			"where a check at the first commit that touches a path could never\n" +
			"pass. A check is due at a tip when any commit replayed in that layer\n" +
			"touched its paths. It gives up bisectability for the run; the commits\n" +
			"between are not owed, and the plan and the end of the run say so.\n" +
			"--each-commit=targets checks at the tips and at each commit a fixup,\n" +
			"squash or amend! folded into: for a fold in place (--stack --autosquash,\n" +
			"without --onto) over a long line, where only the targets and the tips\n" +
			"differ from what was checked before, and for a fold with a move! commit,\n" +
			"in place or with --onto; it is refused for any other run.\n" +
			"--each-commit=off runs no check. all is the default. A resume goes on\n" +
			"with its run's mode. With --edit, tips is refused.\n\n" +
			"A step with a retry: command runs it at the same commit when its run\n" +
			"fails, under its own 30-minute limit. If the retry passes, so does the\n" +
			"check; if it fails too, the stop names both and shows the retry's output.\n" +
			"A run that left tracked files changed, or whose git status could not be\n" +
			"read, is not retried: the stop is the run's.\n\n" +
			"A step that declares needs: [docker] needs Docker, and needs: [env:NAME]\n" +
			"needs the variable NAME set and not empty in the environment the steps\n" +
			"inherit; its value is never read out. Before anything moves, the run\n" +
			"asks docker info (or looks the variable up) once, when a step that needs it may run: an\n" +
			"each_commit step with a commit to check, or a deferred step whose paths\n" +
			"the rebase may change. A worktree whose step needs something that did\n" +
			"not answer is refused, with what sits on it in a stack. --allow-owed\n" +
			"rebases it now anyway: the step is not run and is owed, to run by hand\n" +
			"later, and the worktree is not pushed. docs/sync-declaration.md\n" +
			"describes every key of .wt-sync.yaml.\n\n" +
			"After the fetch, local <trunk> is fast-forwarded to origin/<trunk>\n" +
			"when that is safe: checked out nowhere, or in a clean checkout with\n" +
			"nothing in progress and no session busy in it. Otherwise it is left,\n" +
			"and a line says why. --no-ff-trunk, or wt config set ff_trunk false,\n" +
			"leaves it alone.\n\n" +
			"With no worktree named, it takes every worktree the overview files under\n" +
			"ready: class conflict-free or recipe, nothing dirty, no session busy in\n" +
			"it, no handover waiting. recipe? is left out, since a run of it may stop\n" +
			"and hand you a plan you did not ask for, and so is everything under needs\n" +
			"you and skipped; what is left alone is listed with what holds it.\n\n" +
			"When it asks first, on a terminal (--yes never asks):\n" +
			"  one worktree named      only when an agent session is idle in it\n" +
			"  several named           once, for all of them\n" +
			"  nothing named, --all    once, even for one\n" +
			"With no terminal a named worktree goes ahead, and a run with nothing\n" +
			"named, or across repositories, rebases nothing unless --yes says so.\n\n" +
			"Before anything is rebased, each branch is compared with its own remote:\n" +
			"the ref a push of it would replace (what is recorded for it, else its\n" +
			"own name on the remote git pushes it to; never its upstream as such),\n" +
			"fetched with trunk.\n" +
			"  behind it    fast-forwarded first, then rebased, where that is safe:\n" +
			"               no tracked changes, nothing in progress, no session busy\n" +
			"               in it. Otherwise refused. With nothing to rebase once\n" +
			"               it is there, the fast-forward is all that happens\n" +
			"  diverged     the remote has commits the branch never had: refused.\n" +
			"               --allow-diverged rebases the branch as it stands, for\n" +
			"               a worktree you name; the push below still will not\n" +
			"               replace those commits\n" +
			"  rebased and not pushed yet, ahead, in sync, never pushed: goes on\n" +
			"A remote whose push lands somewhere a tracking ref cannot show (a\n" +
			"mirror, several push URLs, a push URL that is another repository) is\n" +
			"not checked, and a line says so. A branch whose own remote cannot be\n" +
			"fetched is refused by itself; trunk that cannot be fetched refuses the\n" +
			"run. The old tip is pinned before the fast-forward, so wt sync undo\n" +
			"takes back both, and with them any sign that the branch ever had the\n" +
			"remote's commits.\n\n" +
			"Refused, and never touched: a worktree with tracked changes, one a busy\n" +
			"agent session is in, Claude's or Codex's, class divergent, one whose\n" +
			"branch has diverged from its own remote, one an earlier run already\n" +
			"left waiting on you, and any repository whose\n" +
			"trunk declares no .wt-sync.yaml. An idle agent session is named before\n" +
			"anything moves under it, whether or not anyone is asked, and a finished\n" +
			"rebase ends with a wt: line to pass on to it. Sessions are listed again\n" +
			"at the lock: one busy by then, or new since, is refused. The session wt\n" +
			"itself runs under is not counted.\n\n" +
			"--force (-f) takes a named worktree past an agent session in it, busy or\n" +
			"idle: the session is named and told at the finish, and the run goes\n" +
			"ahead. It lifts nothing else, and a run with nothing named refuses it.\n\n" +
			"A worktree whose rebase finished with nothing owed is pushed at the end\n" +
			"with --force-with-lease --force-if-includes, which refuses to overwrite\n" +
			"commits on origin the branch has not seen: the check above stops the\n" +
			"rebase before that, also under --no-push. On a terminal you are asked\n" +
			"once, and Enter means no. --push and --yes push without asking;\n" +
			"--no-push, or a run with no terminal and neither of those, prints the\n" +
			"push command instead.\n\n" +
			"A branch recorded as pushing to a remote branch of another name is said\n" +
			"to, before the push. Its lease is the commit it was compared with, and\n" +
			"it is pushed only over a commit the branch itself was once at. One that\n" +
			"tracks a remote branch of another name with nothing recorded is asked\n" +
			"about once on a terminal, and the answer recorded. With nobody to ask it\n" +
			"is rebased and left unpushed, --push or --yes then ends not completed,\n" +
			"and wt sync push-to records the answer and prints the push to run by\n" +
			"hand: the next run finds the branch on trunk and pushes nothing.\n\n" +
			"Ctrl-C releases every lock the run holds and kills the step it was\n" +
			"running; a worktree caught mid-rebase is named along with the command\n" +
			"that puts it back.\n\n" +
			"--if-ready rebases what is ready and fails if anything was not: a named\n" +
			"worktree that is not ready is refused untouched (see wt sync --help).\n" +
			"--all, --roots and --profile run across repositories, asked once.\n\n" +
			"--json prints one result object on stdout and the progress, and any\n" +
			"question, on stderr, for a tool driving wt; with no terminal a run with\n" +
			"nothing named still needs --yes, and --no-push keeps the push out.\n" +
			"--expect <token> fetches, then refuses the run, touching nothing, when\n" +
			"what it would start on is no longer what wt sync --json reported. With\n" +
			"--allow-diverged it goes ahead only over the divergence that overview\n" +
			"showed: a remote that moved since is refused as changed. wt schema\n" +
			"sync-run prints its JSON Schema; docs/json.md explains it.\n\n" +
			"--onto <ref> rebases the worktrees named onto <ref> instead of trunk:\n" +
			"an integration branch, origin/<branch>, a tag or a commit, with\n" +
			"everything a run onto trunk has. A remote-tracking ref is fetched with\n" +
			"trunk, from the branch its remote's fetch refspec maps to it (--no-fetch\n" +
			"takes it as last fetched); a local branch, tag or\n" +
			"commit is used as it stands, never fetched or moved. The commits\n" +
			"replayed are the branch's that <ref> does not have; --upstream <ref2>\n" +
			"replays only those above <ref2>, git's rebase --onto <ref> <ref2>, which\n" +
			"moves a layer off one branch onto another. The declaration and its\n" +
			"scripts are read from trunk, local trunk is fast-forwarded as ever, and\n" +
			"a branch is classed against <ref>: one <ref> already has is skipped.\n" +
			"With --onto a run moves exactly the worktrees named, none of their\n" +
			"stack: each worktree stacked above one it moved is listed, bottom first,\n" +
			"with the wt sync rebase --onto --upstream line that moves it next.\n" +
			"--stack --onto rebases a line onto <ref>; with --autosquash it folds the\n" +
			"line's fixups in the same run. A target is chosen per piece\n" +
			"of work: --onto needs a worktree named, and takes no --all, --roots or\n" +
			"--profile. --expect with --onto takes the token of\n" +
			"wt sync <work> --onto <ref> --json, which covers the target.\n\n" +
			"--alone rebases the one worktree named as a throwaway copy of its\n" +
			"line: only its branch moves, onto trunk. The other worktrees stacked\n" +
			"on the same history are not rebased, not refused on and not touched,\n" +
			"dirt included; each is named, left on the old commits it was on, and\n" +
			"a worktree below it keeps the commits the copy replays, so the copy\n" +
			"carries them again. --json names them under leftAlone. wt sync undo\n" +
			"<work> puts back what the run moved, and only that. It takes one\n" +
			"worktree, and not --onto or --edit. With --stack the line is rebased\n" +
			"and folded as --stack does, and only the named branch moves: no layer\n" +
			"branch is moved, held or reset, no worktree of one touched; each is\n" +
			"named, left on its old commits. wt sync <work> --alone looks first.\n\n" +
			"--stack rebases a stack held on one line: <work> is the worktree whose\n" +
			"branch is the top of the line (a tmp branch at the top, or the top layer\n" +
			"itself), and the line is every commit from where it leaves trunk to its\n" +
			"tip. The layers are the local branches whose tips are on the line, bottom\n" +
			"first, or the ones --layer names (repeatable); a branch with a backup\n" +
			"name (wt refs sweep's patterns and ref_keep_patterns) is listed and\n" +
			"taken only by --layer naming it; so is a branch with no commits of its\n" +
			"own (its tip is the line's tip or another layer's) with uncommitted\n" +
			"changes to tracked files in another worktree, which --layer takes as\n" +
			"well. --layer takes a pattern too: a value with * or ? takes every local branch it matches\n" +
			"whose tip is on the line, matched as the backup\n" +
			"patterns are (* crosses a slash, no [...]); quote it so the shell does\n" +
			"not expand it. A pattern lists, and does not take, a backup name or a\n" +
			"branch off the line; one that takes no layer is refused. Several layers\n" +
			"are repeated flags, not one string. wt\n" +
			"sync <work> --stack lists them first, with what holds each worktree,\n" +
			"and moves nothing. The line is rebased once, with everything a run does:\n" +
			"the safety ref, the strategies, the checks, the deferred steps, the\n" +
			"handover and wt sync resume. git writes each layer's new tip as the\n" +
			"rebase passes it; every new tip is checked (its count from the new base\n" +
			"and its subject) before any layer moves, and one that fails moves none.\n" +
			"A layer whose tip trunk already carries ends at its last commit left; one\n" +
			"trunk carries whole ends where the layer below does; both are said.\n" +
			"Then each layer moves with update-ref, guarded by its old tip. One in a\n" +
			"worktree moves under that worktree's lock, its files following with\n" +
			"read-tree, only while it is clean, on its old tip, and nothing works in it: no\n" +
			"session, no process whose working directory is inside it (a dev server in\n" +
			"a terminal tab holds it). A held layer is named with the command that\n" +
			"moves it later, and the run fails saying how many were held. --force\n" +
			"also moves a layer only an idle session or a process holds, never a dirty\n" +
			"one. wt sync undo <work> puts back the line and every layer it moved.\n" +
			"Without --push each moved layer gets its push command, with a lease on\n" +
			"the commit last fetched for it, and is not pushed. With --push (or a yes\n" +
			"to the question) every moved layer and the line's branch go in one\n" +
			"git push --atomic per remote, each over that lease: the commit the\n" +
			"remote branch held when the run began, kept in the run's record. If a\n" +
			"lease fails, nothing on that remote moves, the run names the branch\n" +
			"that moved under it, and every local branch stays where the rebase put\n" +
			"it (wt sync undo still puts the line back). A held layer, and a layer\n" +
			"with nowhere safe to push, are named and left out; a remote that does\n" +
			"not take --atomic is refused, never pushed branch by branch. Layers\n" +
			"that push to different remotes get one atomic push each. A line with a\n" +
			"merge commit is refused before anything moves.\n\n" +
			"--stack also carries the side branches: the local branches cut from a\n" +
			"commit of the line that are not on it, those stacked on them, and those\n" +
			"above its top. wt sync <work> --stack lists each with where it forks.\n" +
			"The rebase marks where git puts each fork commit; once the layers have\n" +
			"moved, a side branch's own commits are replayed onto that commit (onto\n" +
			"the line's new tip for one above the top, onto the new tip of the one it\n" +
			"is stacked on), and it moves as a layer does, worktree rules, undo and\n" +
			"all. A fork commit the rebase dropped or folded is where git's list put\n" +
			"its place: after the last commit below it that is left. A branch with a\n" +
			"backup name is listed and never moved, and trunk, the branches wt\n" +
			"refs sweep protects, the --onto branch and anything cut from trunk's own\n" +
			"history are never side branches. One with a merge commit of its own is\n" +
			"held, to be moved by hand: a rebase would drop the merge. A held branch, or one whose\n" +
			"commits conflict where it goes, is named with the command that moves it\n" +
			"later (wt sync rebase <b> --onto <new> --upstream <old>), real commits\n" +
			"in it. --no-side-branches leaves them all alone, unread.\n\n" +
			"A deferred step with each: layer runs inside the rebase at the end of\n" +
			"every layer instead, bottom first: each layer branch of a --stack line,\n" +
			"the line's own commits above them, or the one branch of any other run.\n" +
			"With fold: owner its output goes into the layer's last commit that\n" +
			"changed the step's paths, proved to change nothing else, and the layers\n" +
			"above are replayed onto it; a layer with no such commit gets the step's\n" +
			"commit at its tip. A later pick that conflicts on such a file takes its\n" +
			"own side, for the layer's step to write again. A step that fails there\n" +
			"is owed from that layer up; wt sync steps <work> runs it later.\n\n" +
			"--stack --autosquash folds the line's fixup!, squash! and amend! commits\n" +
			"into their targets in place, onto the line's own base, and re-points every\n" +
			"layer: the way to fold review fixes into a stack without rebasing it. A\n" +
			"check runs at each target with its fixups folded in. The line's content\n" +
			"must come out the same outside the per-layer steps' paths, or no layer moves.\n" +
			"--autosquash alone folds in place: a check whose script the replayed tree\n" +
			"predates is not run there, and is owed. --stack --autosquash --onto <ref>\n" +
			"folds and rebases onto <ref> (trunk: main) in one run, so the checks run with\n" +
			"its scripts at each commit once; the content check then holds every file that\n" +
			"merges cleanly to what merging the old line onto <ref> gives; a file a stop\n" +
			"resolved is held to the rest of that merge. In place or onto <ref>, a\n" +
			"fixup that does not name exactly one commit to be replayed (the target\n" +
			"carries it, it is ambiguous or names nothing) is refused before anything\n" +
			"moves. A fixup written at the top of the line can change lines only a\n" +
			"later commit adds: folded behind its target it lacks their context.\n" +
			"Before anything replays the run names each such hunk, with the first\n" +
			"later commit that carries the context (fixup moved ahead of its context:\n" +
			"<path>:<lines> needs <sha> \"<subject>\", which comes later on the line),\n" +
			"and goes ahead; wt sync <work> --stack --autosquash says the same and\n" +
			"moves nothing. A stop there says so and suggests undo and retargeting the\n" +
			"hunk; wt sync resume refuses a resolution that drops it.\n\n" +
			"A move! commit (wt sync move --help) moves part of one commit, whole\n" +
			"paths or hunks, into an earlier one in the same fold: the part is applied\n" +
			"at the target after its own fixups, the later commit keeps only the rest,\n" +
			"and the move! commit is dropped. It needs --each-commit=targets, so the\n" +
			"checks run at the target and the source. With --onto the tip must be the\n" +
			"tree the same run gives without the move! commits; a new base that carries\n" +
			"a move's target or source, changes the part's context or conflicts with\n" +
			"the line refuses the run: rebase without the move! commit first.\n" +
			"Before anything moves the fold is replayed in the object store, and the\n" +
			"run is refused if the part does not apply at the target, a commit in\n" +
			"between conflicts with it, or the tip's tree would not be that one; the part is\n" +
			"kept under refs/wt-sync-kept/<branch>/<epoch>-move-<n>.\n\n" +
			"--stack --edit <rev> moves a change down the line to an earlier commit:\n" +
			"the fix a review asks of a low commit that a later one already carries.\n" +
			"The line is replayed in place, onto its own base, and stops at <rev>,\n" +
			"which must name one commit of the line, or nothing moves. Amend it\n" +
			"(git commit --amend) and wt sync resume replays the rest onto what you\n" +
			"made of it: a commit that already carried the change shrinks to what is\n" +
			"still its own, one that carried nothing else is dropped and reported, and\n" +
			"a conflict with the change is a stop like any other. Before the line or\n" +
			"any layer moves, its content must be what it was with your change merged\n" +
			"in, outside a stop's resolved files and the per-layer steps' paths, or\n" +
			"the rebase is put back and what it came to kept under\n" +
			"refs/wt-sync-kept/<branch>/<epoch>. The end names the commit and the\n" +
			"paths its amend changed. wt sync undo <work> from the edit stop or any\n" +
			"later one puts everything back and keeps the amend under a safety ref.\n" +
			"One --edit per run; to fold or move the line too, run that after.",
		Example: "  wt sync rebase login-crash api-tidy --yes --json --expect 1:0123abcd # a tool\n" +
			"  wt sync rebase login-crash --alone  # only this one; its stack is left as it is\n" +
			"  wt sync rebase tmp/ref --alone --stack --autosquash  # fold a copy; no layer moves\n" +
			"  wt sync rebase tmp/stack --stack --each-commit=tips  # check each layer's tip only\n" +
			"  wt sync rebase tmp/stack --stack --autosquash --each-commit=targets  # fold in place; check the targets and the tips\n" +
			"  wt sync rebase tmp/stack --stack   # the whole line once, then every layer\n" +
			"  wt sync rebase tmp/stack --stack --layer 'tmp/layer/*'   # the pattern selects the layers; side branches still ride along (--no-side-branches stops that)\n" +
			"  wt sync rebase tmp/stack --stack --layer ui --layer api   # two layers, as repeated flags\n" +
			"  wt sync rebase tmp/stack --stack --autosquash  # fold review fixes in place\n" +
			"  wt sync rebase tmp/stack --stack --autosquash --onto main  # fold and move\n" +
			"  wt sync rebase tmp/stack --stack --autosquash --onto main --each-commit=targets  # a move! commit, onto main\n" +
			"  wt sync rebase tmp/stack --stack --no-side-branches  # move the line, leave branches cut from it\n" +
			"  wt sync rebase tmp/stack --stack --edit 1a2b3c4  # stop there to amend\n" +
			"  wt sync rebase login-crash --force --allow-diverged  # past session, origin\n" +
			"  wt sync rebase login-crash --onto test_wt/integration  # onto that branch\n" +
			"  wt sync rebase ui --onto origin/main --upstream feat_wt/api  # move a layer\n" +
			"  wt sync rebase --all --no-fetch --push  # every ready one, everywhere, pushed\n" +
			"  wt sync rebase --profile api --if-ready --no-ff-trunk  # trunk left as it is\n" +
			"  wt sync rebase --roots work --no-push  # a few roots; print the pushes",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeWork,
		RunE: func(cmd *cobra.Command, args []string) error {
			f := runFlags{yes: yes, noFetch: noFetch, noFFTrunk: noFFTrunk, ifReady: ifReady, force: force,
				allowDiverged: allowDiverged, allowOwed: allowOwed, push: push(), json: asJSON, expect: expect,
				stack: stack, layers: layers, autosquash: autosquash, edit: edit, eachCommit: eachCommit, onto: onto, upstream: upstream, alone: alone, noSides: noSides, pushLine: pushLine}
			if err := f.check(false); err != nil {
				return err
			}
			if err := f.checkOnto(args, sel.selection()); err != nil {
				return err
			}
			if err := f.checkAlone(args, sel.selection()); err != nil {
				return err
			}
			if f.stack && (sel.selection().Any() || len(args) != 1) {
				return usagef("--stack rebases the line of one worktree: name the one at its top")
			}
			if sel.selection().Any() {
				return syncAcross(cmd, args, true, sel.selection(), f)
			}
			return withVerbContext(cmd, args, rebaseCommand, asJSON, func(ctx *kit.Context, j *syncrun.RunJournal) error {
				return syncRebase(cmd, args, ctx, f, j)
			})
		},
	}
	rebase.Flags().BoolVar(&noFetch, "no-fetch", false, "rebase onto origin/<trunk> as last fetched")
	rebase.Flags().BoolVar(&noFFTrunk, "no-ff-trunk", false, "leave local <trunk> where it is after the fetch")
	rebase.Flags().BoolVarP(&yes, "yes", "y", false, "yes to every question, the push included (--no-push keeps it out)")
	rebase.Flags().BoolVar(&ifReady, "if-ready", false, "rebase only what will go through without needing you, and fail on the rest")
	rebase.Flags().BoolVarP(&force, "force", "f", false, "rebase a named worktree even with an agent session in it")
	rebase.Flags().BoolVar(&allowDiverged, "allow-diverged", false, "rebase a branch that has diverged from its own remote, as it stands")
	rebase.Flags().BoolVar(&allowOwed, "allow-owed", false, "rebase now even when a step's need does not answer; the step is owed, to run later")
	rebase.Flags().BoolVar(&asJSON, "json", false, "print one result object on stdout, the progress on stderr")
	rebase.Flags().StringVar(&expect, "expect", "", "refuse unless the overview still matches this token from wt sync --json")
	rebase.Flags().BoolVar(&stack, "stack", false, "rebase the line of the worktree named once, and move every layer branch on it")
	rebase.Flags().StringArrayVar(&layers, "layer", nil, "with --stack: take this branch as a layer, or every branch a pattern with * or ? matches (quote it); repeatable; none takes every branch on the line but those listed as skipped (backup names, and a branch with no commits of its own that has uncommitted changes in another worktree)")
	rebase.Flags().BoolVar(&alone, "alone", false, "rebase the one worktree named without the rest of its stack: the others are not refused on, moved or touched")
	rebase.Flags().BoolVar(&autosquash, "autosquash", false, "with --stack: fold the line's fixups, in place; with --onto <ref>, as the line moves onto it")
	rebase.Flags().BoolVar(&noSides, "no-side-branches", false, "with --stack: leave the branches that fork from the line or sit above its top where they are, unread")
	rebase.Flags().StringVar(&eachCommit, "each-commit", "", "where the each_commit checks run: all (every commit that touches a step's paths, the default), tips (only each layer's tip), targets (the tips, and each commit a fixup folds into: for --stack --autosquash in place) or off")
	_ = rebase.RegisterFlagCompletionFunc("each-commit", cobra.FixedCompletions([]string{"all", "tips", "targets", "off"}, cobra.ShellCompDirectiveNoFileComp))
	rebase.Flags().StringVar(&edit, "edit", "", "with --stack: stop at this commit of the line for you to amend, and replay the rest onto it, in place")
	addOntoFlags(rebase, &onto, &upstream, "rebase the worktrees named onto <ref> instead of trunk: a branch, origin/<branch>, a tag or a commit")
	sel.add(rebase, true)
	rebase.Flags().BoolVar(&pushLine, "push-line", false, "with --stack and a push: include the line's own branch when that would create its remote branch (left out otherwise, and named)")
	push = addPushFlags(rebase, "push the worktrees that finish, without asking",
		"neither push nor ask; print the push command")
	return rebase
}

// syncRebase is wt sync rebase. j, when not nil,
// gets its result for --json.
func syncRebase(cmd *cobra.Command, works []string, ctx *kit.Context, f runFlags, j *syncrun.RunJournal) error {
	opts, _ := runOptions(cmd, f, len(works) == 0)
	opts.Journal, opts.Expect, opts.Stack, opts.Alone = j, f.expect, f.stackOptions(), f.alone
	return synccmd.SyncRebase(ctx, works, opts, cmd.OutOrStdout())
}
