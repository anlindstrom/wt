# Architecture

For a person or an agent about to change wt: how the code is laid out, how the
main commands run through it, what wt writes and where, and which promises
every change keeps. The other docs hold the detail; this one says where things
are and why they sit there.

| For | Read |
|---|---|
| what `--json` prints, field by field | [json.md](json.md) |
| `.wt-sync.yaml`, its keys and how a sync run treats them | [sync-declaration.md](sync-declaration.md) |
| `worktree.conf`, branch names, your own settings | [configuration.md](configuration.md) |
| every check `wt remove` and `wt sweep` make | [remove.md](remove.md) |
| the layout on disk, and the rules for tools building on it | [worktree-conventions.md](worktree-conventions.md) |
| what to run for which change | [testing.md](testing.md) |

## The layers

```
cmd/wt                cobra: commands, flags, help, examples, exit codes
  │
  ▼
internal/commands/*   one package per command family; kit is what they share
  │
  ▼
internal/wtsync, repo, git, config, ...   the domain: no cobra, no flags
```

Dependencies point down only. `cmd/wt` builds the command tree and turns flags
into an options struct; the command package does the work against a
`kit.Context`; the domain packages know nothing of commands. A domain package
never imports `internal/commands`, and `internal/git` imports nothing of wt's.
`go list -f '{{.Imports}}' ./internal/<pkg>` shows the edges.

### `cmd/wt`

One file per command (`new.go`, `remove.go`, `up.go`, `sync.go`, ...). Each
`new<Name>Cmd` declares `Use`, `Short`, `Long`, `Example` and the flags, and its
`RunE` calls one function in a command package. `root.go` assembles the tree
(`newRootCmd`), opens the context (`openContext`, `withContext`, `openLenient`)
and maps an error to the exit code (`Execute`, `errorStatus`). `width.go`,
`colour.go` and `tty.go` are help wrapping, colour and the prompts. The help
text lives here and nowhere else.

`wt cd`, `wt exec` and `wt attach` start in the shell, since a binary cannot
change its caller's directory. `wt shell` prints `shell/wt.sh` (embedded by
`shell/embed.go`), which defines a `wt` function: `cd` and `exec` resolve the
pattern with `wt find` and act in the shell, `attach` asks
`wt attach --for-shell` what to run, and every other command goes to the
binary unchanged (`command wt "$@"`), and so to cobra.

### The context

`kit.Open` (through `openContext`) builds the `kit.Context` a command runs in:

- **`Repo.Root` and `Repo.MainRoot`** (`internal/repo/repo.go`). `Root` is the
  worktree you stand in; `MainRoot` is the main checkout, where branch and
  worktree operations run and where the common git dir is. The two are the
  same in the main checkout.
- **The repository's configuration** (`kit.loadFor`, `config.Load`): the
  first checkout that has one, `Root` then `MainRoot`, and in it
  `worktree.toml` before `worktree.conf` (so a `worktree.toml` in `Root` wins
  over the main checkout's `worktree.conf`, and next to a `worktree.toml` a
  `worktree.conf` is not read); with neither anywhere, detected defaults
  (`Config.Detected`). A branch that changes its configuration file is
  honoured where it is checked out.
- **Your own settings** (`config.User`, `~/.config/wt/config.toml`) decide
  the integrations; a repository cannot switch one on, only decline (its
  `SUPERSET_REGISTER=off`). For branch names the order is the repository's
  file where it set the key, then yours, then the default
  (`Config.BranchSuffixFor`, `TypeNamesFor` in `internal/config/precedence.go`).
  `wt config` prints each value with where it came from.

### `internal/commands/*`

| Package | Owns |
|---|---|
| `kit` | what every command shares: `Context` (repo, config, user config, warnings), finding worktrees from `<work>` and `<pattern>`, the GitHub client and its cache, tables, signal handling, `NoteLock` for the run log |
| `provision` | `wt new`, `checkout`, `setup`, `init`, `config`, `port`, and the provisioning a new worktree gets |
| `retire` | `wt remove` and `wt sweep`: take a worktree away, or quarantine it, once nothing in it would be lost |
| `quarantinecmd` | `wt restore` and `wt purge`, which end a quarantine |
| `refs` | `wt refs sweep`, `swept`, `restore`, `purge`: the pin store under `refs/wt-swept/` |
| `synccmd` | `wt sync` (the overview), `wt sync rebase`, `wt up`, `push-to`, `wt sync doctor` |
| `syncrun` | what a run is made of that rebase, up, resume and the keeper share: trunk's fast-forward, own remotes, the push, sessions, finishing a rebase and a stack, the run's `--json` journal |
| `syncfix` | `wt sync resume` and `wt sync undo` |
| `syncwait` | `wt sync wait`: follows a run's log; reads only |
| `synckeep`, `synckeep/keepstate` | `wt sync keep`, the launchd keeper; `keepstate` is its state file, lock and job, below `synccmd` so the overview can read them |
| `session` | `wt session` and `wt attach` |
| `admin` | `wt doctor`, `migrate`, `adopt`, `pr`, `hook`, `repos` |
| `supersetws` | registering a worktree as a Superset workspace, and deleting it |
| `commands` (the root package) | `wt list` and `wt status`, and the tests that cross command packages |

Inside the family: `synccmd` imports `syncrun`, `retire` and `keepstate`;
`syncfix` imports `syncrun`; `synckeep` imports `synccmd`; `admin` imports
`provision`, `retire` and `synccmd`. `syncrun` imports no sibling but `kit`.

### The domain packages

| Package | Owns |
|---|---|
| `wtsync` | the sync engine: parsing `.wt-sync.yaml` (`Parse`, `LoadFromRef`), the simulation (`SimulateRebase`, `Assess`), the strategies, the rebase driver (`Rebase`, `Resume`), stacks (`ReadLine`, `ReadHolder`), steps (`RunDeferred`, `each_commit.go`), safety refs, the lock, the handover, `Undo`, `Doctor` |
| `repo` | discovers a repository and its worktrees; every fact comes from git, never from a path's shape |
| `git` | a thin wrapper over the git CLI, and the reaper that stops what wt started when wt is killed |
| `config` | `bin/worktree/worktree.conf` (or `worktree.toml`) and the user's `~/.config/wt/config.toml` |
| `naming` | work name ↔ branch ↔ path (`Scheme.Branch`, `Scheme.Dir`); by default the path tail is the branch name, but a branch suffix or type names of its own (`WORKTREE_BRANCH_SUFFIX`, `type_names`) make them differ: `feature/login` can live at `feat_wt/login` |
| `agents` | the Claude and Codex sessions and other processes in a worktree, and stopping them |
| `quarantine` | moving a worktree out of the way by renames only, with `recovery.json` as its journal |
| `refpins` | the store under `refs/wt-swept/` |
| `runlog` | the copy of what one `wt sync` invocation prints, and each locked worktree's pointer to it |
| `jsonout` | the shared `--json` vocabulary: steps, outcomes, problems, the journal, the plan token |
| `reposel` | roots, profiles and the repositories a `--all`, `--roots` or `--profile` command runs across |
| `github` | pull requests through `gh`; writes nothing to GitHub, but `CheckoutInto` runs `gh pr checkout`, which changes the local checkout and sets up its branch, remote and push config |
| `superset`, `devports`, `timemachine` | the optional outside tools, each bounded and silent when absent |
| `find`, `textwrap`, `colour`, `about` | fuzzy matching, wrapping, colour, `wt about` and the embedded what's-new |

`wtsync` imports `agents`, `config`, `git` and `repo`; `repo` imports
`config` and `git`; `runlog` imports `wtsync` for `ProcessRunning`.

Outside `internal/`: `schema/` embeds the JSON Schemas (`schema.All`), `shell/`
the shell layer `wt shell` prints, `compat/` the `worktree_functions.sh`
contract for `wt shell --compat`, `mods/wt` the Claude Code mod, `test/` the
bats suites.

## The flows

### `wt new`, `wt checkout`, `wt remove`

`wt new` (`cmd/wt/new.go`) hands four functions to `runCreate`: the plain
run, `--dry-run`, `--dry-run --json` and `--json`. The plain run is
`provision.New` (`internal/commands/provision/new.go`): `kit.ParseWork` turns
the spec into type, work and branch, `ctx.Scheme().Dir` gives the path, and,
without `--base`, `new_base.go` fetches trunk (`fetchTrunk`), picks local trunk
or `origin/<trunk>` (`chooseTrunkBase`) and moves local trunk up only when no
worktree has it (`bringTrunkUp`); `AddAndProvision` runs `ctx.Repo.AddWorktree`, excludes the folder from Time
Machine, then `setup` (developer config copied, caches cloned, `provision.sh`,
submodules, the build command) and `supersetws.Register`. `wt checkout` is the
same with `provision.Checkout` and `CheckoutAdd` putting the worktree on an
existing branch. The JSON forms plan with `PlanNew` / `PlanCheckout`, and
`--expect` compares the plan's token before anything is made; every side effect
reports through a `CreateJournal`.

`wt remove` (`cmd/wt/remove.go`) calls `retire.Remove`, which reaches
`removeWorktreeResult` in `internal/commands/retire/remove.go`: `planFor`
reads everything that could be lost (uncommitted and untracked files, commits
on no other ref, sessions and processes in the worktree, a git worktree lock,
whether the branch has landed), `Plan.Render` prints it, a `--expect` token is
checked, `refusal` refuses what `--force` does not cover, a confirmation
re-reads the plan and refuses if it changed, and only then `plan.run`. A merged
branch it deletes is pinned first (`refpins.BranchPins`), so `wt refs restore`
can bring it back. `wt sweep` (`retire/sweep.go`) does the same for every
landed worktree. [remove.md](remove.md) lists every check.

### `wt sync`, the overview

`synccmd.Sync` (`internal/commands/synccmd/sync.go`) fetches trunk, reads the
declaration from origin/<trunk> (`SyncDeclaration`, `wtsync.LoadFromTrunk`:
never from a working tree, since it names executables) and assesses every
worktree with `assessAll`, a few at a time. Each worktree is assessed by
`wtsync.AssessOwn` / `AssessTo` (`internal/wtsync/triage.go`), which runs
`SimulateRebaseTo`
(`internal/wtsync/replay.go`): the rebase replayed in the object store, the
strategies applied at every stop, nothing of yours touched. The class
(conflict-free, recipe, contested, divergent, ...) and group come from the
assessment (`SectionOf`, `ClassLabel`). `SyncJSON` / `OverviewOf` prints it as one
object, through `SurveyRepo`, which also reads the stack relations the text
leaves out.

### `wt sync rebase`, plain

`synccmd.SyncRebase` → `RunSync` → `RunPlan.run`
(`internal/commands/synccmd/sync_run.go`), in this order:

1. usage checks (`--each-commit`, `--onto`, `--alone`, `--stack`, `--layer`
   patterns via `wtsync.CheckLayerPatterns`), and `declare` reads the
   declaration;
2. `--expect` against the overview's token; `syncTrunk` fetches and
   fast-forwards local trunk when safe;
3. with nothing named, `selectReady` takes the ready worktrees;
   `selectBranches`, `readStack`, `readOwed`;
4. `triage` asks the one question (idle sessions, more than one worktree);
5. `lockAll` takes `wt-sync.lock` in the git dir of every worktree the run
   rebases (local trunk's fast-forward in step 2 is not under it);
6. `rebaseOne` per branch: `syncrun.TrackedRebase` around `wtsync.Rebase`,
   timings reported by `syncrun.ReportTimings`;
7. the follow-ups, the counts line, and `syncrun.OfferPush`.

`wtsync.Rebase` (`internal/wtsync/rebase.go`) writes the safety ref
(`WriteSafety`), plans version lifts and checks, writes the rebase's todo with
its own marks, runs `git rebase --interactive --empty=drop --no-update-refs
--no-gpg-sign`, and `driver.drive` loops: at each stop the strategies resolve
what they own; a stop nobody owns becomes a handover (`driver.handover`);
`driver.finish` ends it. The tail after a finished rebase (deferred steps
through `wtsync.RunAfterSteps`, the result ref, owed steps) is
`syncrun.Finish` (its `CompleteRun` plus the stack's layers, what is owed and
the journal's record), shared with resume. `wt up` is `synccmd.Up`: the same run
with the one worktree named and `--if-ready`, and it also works in a repository
with no `.wt-sync.yaml`.

A run that names one worktree can still move more than that one, in two ways,
both without `--stack`:

- **A stack of worktrees.** `wtsync.Parents` relates the branches checked out
  in worktrees: each one's parent is the nearest other worktree branch that is
  its ancestor. `selectBranches` widens each named branch to its whole stack
  (`wtsync.Members`), says so (`… is a stack with …: rebasing all of them`),
  and rebases them parents first, each in its own worktree. This is what
  `wt up` does in a worktree whose branch sits on, or under, another
  worktree's. `--alone` takes the named one only.
- **Loose layers.** Branches in no worktree whose tips are on the commits a
  rebase replays (`wtsync.ReadLoose`, `RunPlan.readLoose`) are moved to the
  replay of their commits afterwards (`moveLoose`) and come back with undo,
  but only when the rebase replayed the line one for one: the same number of
  commits, in order, with the same subjects. A rebase that drops a commit
  trunk already carries or folds one leaves every loose layer where it was,
  with the reason (`LooseLine.Move`); commits a deferred step adds afterwards
  are not part of that comparison. A backup
  name, a `fixup!` tip or a line with a merge is left and named.

`--stack` is the other model: one worktree's line, whose layers are branches
on it (in a worktree or not), moved by one rebase of the top. A `--stack`,
`--onto` or `wt sync steps` run turns the stack of worktrees off.

### A stack run (`--stack`)

1. **Read the line.** `readLine` (`synccmd/stack.go`) calls `wtsync.ReadLine`
   (`internal/wtsync/stack_line.go`): every local branch whose tip is on the
   line, or the `--layer` names and patterns, minus backup names; without
   `--layer`, `skipStrays` moves to Skipped a branch with no commits of its own
   that has uncommitted changes in another worktree. `readSides` adds the side
   branches (`wtsync.ReadSides`).
2. **Plan.** `RunPlan.readStack` reads each layer's holder
   (`syncrun.ReadHolders` → `wtsync.ReadHolder`), builds the `syncrun.Stack`
   (`StackOf`, `SetSides`), the context gaps a fold would open, and prints it
   (`PrintLayers`).
3. **Rebase.** One `wtsync.Rebase` of the top branch. Its todo marks each
   layer's tip; the run writes the new tips under `refs/wt-stack-map/<epoch>/<n>`
   (and side branches under `refs/wt-stack-fork/`), keeps its record (`wtsync.ReadStackRecord`) in
   `wt-sync-stack/<epoch>.json` in the common git dir, and per-layer steps
   (`each: layer`, `fold: owner`) run at each layer's tip inside the rebase.
   `--autosquash` folds fixups; `--each-commit=targets` checks at each layer
   tip and each fold target.
4. **Finish.** `syncrun.FinishStack` (`internal/commands/syncrun/stack.go`;
   its phases are `guardEdit`, `guardFold`, `verifyTips`, `moveLayers`,
   `reportLayers`, `moveSides`) checks every new tip, runs the tree guard
   (`wtsync.TreeGuard`) on a fold, and moves each layer
   branch; a layer checked out in a worktree is moved there too, its files
   with it. A layer a holder holds is left, and named.
5. **Push.** With `--push` (or a yes), `syncrun.PushStack` pushes the moved
   layers and the line's branch in one `git push --atomic` per remote, each
   ref over a `--force-with-lease` on the commit the record holds for it
   (`StackRecord.Pushes`, read from `Own` before anything moved, so a resume
   leases on the run's value, not on a later fetch). What it leaves out goes to
   `OfferPush` as before, less the line's own branch where a push of it would
   create a remote branch nothing asked for (the run saw no remote branch
   at that remote, name and push URL, `BranchPush.SawAt`; nothing recorded; no
   `--push-line`); with `--no-push` or nobody to ask it pushes
   nothing. A run or resume that leaves a record ends with `push: wt sync push
   <work> --stack` (`syncrun.SayPushLater`). A moved layer with nowhere to push
   whose siblings' records follow one pattern is told the `wt sync push-to`
   command (`Own.SiblingPushTo`); nothing is recorded, and the line that counts what is
   left names every moved layer nothing says where to push. A record whose runs
   disagree on a destination ends pointing at `--dry-run` instead. It
   refuses the whole push, printing the per-branch commands, when the line was
   not left pushable, the record has no leases, the line's branch has no safe
   push, a layer the run left unchanged is not level with its remote branch
   (`unsentLayer`, as the later push checks it), or a remote's push URL is no
   longer the one recorded. After a
   transport failure it reads the pushed refs back (`wtsync.RemoteRefs`)
   before saying where each branch is. The push and the read-back go to the
   endpoint URL the record holds, not the remote's name, and wt moves the
   remote-tracking refs itself.
6. **Push later.** A run or resume that ends with branches unpushed
   (`--no-push`, a no, nobody to ask) calls `syncrun.KeepStackPush`, which keeps
   a `wtsync.PushRecord` for the line: each branch's `BranchPush` (remote,
   lease, endpoint, tracking ref) and the tip the run left it at, the layers it
   left out, and what it did with every layer (`PushRecord.Layers`: moved,
   unchanged or held, with the tip it left). A stack whose layer moves stopped
   part way is declined (`Stack.declined`, set by `FinishStack`): it pushes
   nothing and keeps no record. A run writes it while it still holds the line's lock
   (`finishRebase`, or the resume before its push), from the tip it left, and
   `syncrun.SettleStackPush` takes out what its push sent, only while it is
   still the record that run wrote. Unpushed runs add up: `wtsync.AddPushRun`
   keeps the earlier runs' contributions (`PushRecord.Earlier`, oldest first;
   the record's own fields are the newest run's, so a record of one run is the
   shape older builds wrote), and `PushRecord.Merged` is the push they make:
   per branch the newest run's tip over the oldest run's lease, or over the
   newest's where the leases differ (`MergedItem.LeasesDiffer`), and a
   `PushConflict` when the runs disagree on the remote branch or the endpoint.
   A push reads the remote of every branch whose leases differ before it pushes
   anything (`recordedPush.settleLeases`) and goes on only where each is at the
   newest lease, which it is then still leased on. A run that moved nothing, or that is not
   pushable (a step failed or owed), leaves the record as it is; every write gives it a new
   `Rev`, which `ReplacePushRecord` compares with the epoch and time, so a settle made from
   an older read is refused; a push that sends everything removes it, and one that sends some
   takes those branches out of every run. `wt sync undo` takes out the undone
   run's contribution only (`ForgetPushRecordOfRun`, by epoch). No run reads it.
   `wt sync push <work> --stack` (`syncfix.SyncPushLater` →
   `syncrun.PushRecorded`) holds the line's worktree lock, as a run and undo do (not with
   `--dry-run`, which only reads, the remotes included: `predictPushes` runs
   `git push --atomic --dry-run` per remote, with the push's leases), from reading the record to replacing it (`wtsync.ReplacePushRecord`, only
   while it is still the record pushed from), re-reads it after the question,
   and refuses with no record, with a branch no longer at
   the newest run's recorded tip, with a conflicted branch or a differing lease
   the remote is not at, with a run that
   kept no layers (an older build), with a layer it does not send that is not
   at its recorded tip or not level with its remote branch (`unsentLayers`), or when
   `checkEndpoints` fails, all before pushing, and
   otherwise goes through the same `pushRemotes` as step 5 over the recorded
   leases, fetching nothing. A refusal over conflicted branches is printed once
   (`saidRefusal`).
   A layer that pushes under another name is pushed to what `wt sync push-to
   --branch` recorded for it (`synccmd.SyncPushToBranch`; the record is read for
   a branch with no worktree like any other). The line's own branch is left out
   of both pushes when `BranchPush.Creates` (no remote branch yet, nothing
   recorded), unless `PushOptions.PushLine` (`--push-line`).
   `--forget` (`syncfix.forgetPushRecord`) drops the record under the records'
   flock and does nothing else; it removes a record it cannot read too.

### Stop, resume, undo

A stop nobody owns leaves the worktree mid-rebase with what the strategies
resolved staged: `syncrun.HandOver` writes `wt-sync-plan.md` (for the person)
and `wt-sync-state.json` (for wt) into the worktree's git dir and keeps the
lock (`Lock.Keep`). In a stack of worktrees, a parent's stop is put back
instead, so no child is left on commits its parent no longer has. That is per
branch, not for the whole stack: a refusal before anything moved refuses the
whole stack (`refuseStack`), but once rebases have begun a failure refuses
only the branches above it (`refuseAbove`), so parents and siblings already
rebased stay rebased while a child is handed over.

`wt sync resume` is `syncfix.SyncResume`: it checks the worktree is what the
run left, takes the lock over (`wtsync.TakeOver`, which only displaces the
lock the state file names), and drives `wtsync.Resume` to the end, then the
same `syncrun.Finish` as a run.

`wt sync undo` is `syncfix.SyncUndo` → `wtsync.Undo`
(`internal/wtsync/undo.go`): every ref the newest run moved (all safety refs of
its epoch) is reset, after every checkout involved is locked and checked
clean, not mid-rebase and free of busy sessions. A branch that moved since the
run is refused; `--force` pins its current tip first, so the forced undo can
itself be undone. A handed-over rebase is aborted only after every branch
passed.

### `wt sync wait`

A verb that logs (`runLogged` in `cmd/wt/sync.go`: rebase, steps, resume)
starts `runlog.Begin` in `withVerbContext`; what it prints for a person (stderr,
and stdout unless `--json`) is copied into `.git/wt-sync-logs/`. Each lock the run takes calls `kit.Context.NoteLock`,
which writes `wt-sync-run.json` in that worktree's git dir, pointing at the log.
`Execute` writes the exit code with `runlog.EndActive` into the `.end` file
beside the log. `syncwait.Wait` reads the pointer, prints the log as it grows,
and exits with the code from the end file; a log with no end whose process is
gone (`wtsync.ProcessRunning`) is a run that died. It takes no lock and writes
nothing.

## The safety model

wt's job is to move branches without losing anything. Every change keeps these:

- **No lost commits or refs.** Before a run rewrites a branch it pins the old
  tip under `refs/wt-sync/<branch>/<epoch>` (`WriteSafety`, which refuses to
  overwrite). Where it ended is `refs/wt-sync-result/`, a fast-forward from the
  branch's own remote `refs/wt-sync-ff/`. `wt remove` and `wt sweep` pin a
  branch they delete under `refs/wt-swept/<run>/`; quarantine pins under
  `refs/wt-quarantine/`. A branch wt deletes or rewrites is pinned first. A run
  drops a safety ref by itself only for a move that did not stand: a rebase
  put back, or a layer move that was refused or failed and was rolled back to
  its old tip (`unpin`). That includes a branch someone else moved meanwhile:
  it then has no pin from this run and is held. Otherwise the pins go when
  you run `wt refs purge`, `wt purge` or `wt sync doctor --prune`, or
  `wt sweep` once a quarantine folder is gone.
- **No lost files.** A worktree with tracked changes, or a rebase in progress,
  is not rebased or moved, and `wt remove` refuses it unless `--force` names
  what may go; remove also counts untracked files and commits on no other ref.
  Quarantine moves by rename only.
- **No lost stash.** wt never runs `git stash`; the stash is shared by every
  worktree of a repository.
- **One run per worktree** ([sync-declaration.md](sync-declaration.md#locks--one-run-per-worktree)). `wtsync.Acquire` takes `wt-sync.lock` in the
  worktree's git dir atomically. A lock is live (`LockLive`) while it is
  younger than `LockExpiry` (30 minutes), or for as long as its process still
  runs (`ProcessRunning`, which also tells a reused pid by its start time;
  what cannot be told counts as running). A handover keeps its lock; resume and
  undo take it over. The lock covers the worktrees a run rebases and the
  layers it moves; local trunk's fast-forward runs before any lock, under its
  own checks (`syncrun` `trunk_sync.go`: trunk checked out nowhere, or in a
  clean checkout with nothing in progress and no busy session).
- **Held layers stay put.** A layer is held (`LayerHolder.Holds`) when its
  worktree is dirty, rebasing or bisecting, a session is busy or idle in it, a
  process works in it, another run's lock is live there, or where it stands
  cannot be read. A held layer is left and named; `--force` lifts only an idle
  session or a process (`Forcible`).
- **The tree guard.** A fold must not change what the line holds:
  `wtsync.TreeGuard`, called by `syncrun.FinishStack`, compares the new tip
  with the old one (a fold in place) or with git's merge of the old line onto the target (a fold that
  moves; for a line with `move!` commits, with the old line replayed onto the
  target without them, and nothing a conflict stop resolved), leaving out the declared per-layer step paths and, for a moving
  fold, the conflicting regions a stop resolved. A path a person resolved at
  a conflict stop wt handed over (`State.Resolutions`, read from the index by
  `wtsync.ResolutionsAtStop` when resume goes on and carried through every
  handover) is taken whole as they resolved it, with what the old line
  changed in it after that commit (`resolvedAtStop`). What a person amended
  into the commit at a check stop (`State.Fixes`, `wtsync.FixAtCheckStop`) is
  merged into the line the result is compared to, and its paths are named in
  `resolvedAtStop` too; every other path is held to the merge. A failed guard moves no layer
  and says so; `wt sync undo` puts the folded line back. The result is
  `fold.guard` in `--json`.
- **The command layer writes no ref or checkout.** Moves go through wtsync,
  behind its guards; `internal/commands/git_writes_test.go` fails on a git
  write in a non-test file there unless the site is on its allowlist, which
  names each one and its guard (trunk's fast-forward, an own remote's, a
  checkout in a worktree wt just made).
- **Undo restores refs and the files checked out on them.** It resets each
  branch to its safety ref (`git reset --hard` where the branch is checked
  out, `update-ref` where it is not), takes back a fast-forward, and aborts a
  handed-over rebase. Its checks come first and protect what a reset would
  throw away: every checkout involved must be clean, not mid-rebase (but for
  the run's own handover) and free of busy sessions, and a branch that moved
  since the run is refused.
- **The declaration comes from trunk.** `.wt-sync.yaml`, and the strategy
  scripts it names, are read from origin/<trunk>, so a feature branch cannot
  change which strategies resolve its conflicts or which steps are declared.
  The commands the steps run (`each_commit` checks, deferred steps) run in the
  worktree, so what they execute, a Makefile or a script there, is the
  branch's own.

## On-disk state

In the repository's common git dir (the main checkout's `.git`):

| File | Written by | Read by |
|---|---|---|
| `wt-sync-logs/` (a log and its `.end` per invocation) | `runlog` | `wt sync wait` |
| `wt-sync-stack/<epoch>.json`, `<epoch>.alone` | a stack run: the record of its layer moves, and the mark of an `--alone` run that moves no layer (`syncrun.MarkAlone`) | `FinishStack`, resume, undo |
| `wt-sync-push/<branch>.json` | a stack run left unpushed (`syncrun.KeepStackPush`): the line's recorded leases and tips for a later push, keyed by the line's branch (escaped) | `wt sync push --stack` only; removed by it (also `--forget`) and by undo, and added to by the next run of the line: one contribution per unpushed run |
| `wt-sync-owed/<branch>.json` | a run whose steps failed or were owed, with the run's `--each-commit` mode | the next run (an owed layer step still runs at an unchanged layer), `wt sync steps` (keeps the mode), resume |
| `wt-sync-keep.json`, `.log`, `.lock`, `.err` | `wt sync keep` (`keepstate`, launchd) | the overview, `wt sync keep status`, doctor |
| `wt-pr-cache.json` | `kit` | `wt list`, `wt sweep` |

In each worktree's own git dir (`.git/worktrees/<name>/`):

| File | Written by | Read by |
|---|---|---|
| `wt-sync.lock` | a run (`wtsync.Acquire`) | every other run, `wt sync wait`, doctor |
| `wt-sync-run.json` | `runlog`, on each lock taken | `wt sync wait` |
| `wt-sync-plan.md`, `wt-sync-state.json` | a handover | the person; resume and undo |
| `wt-sync-stack-todo`, `wt-sync-fold-index` | a stack run, while it runs | the run itself |

Inside the rebase's own directory, so git's abort removes them:
`wt-sync-layer-start`, `wt-sync-before`, `wt-sync-edit`, `wt-sync-amended`.

Refs: `refs/wt-sync/`, `refs/wt-sync-result/`, `refs/wt-sync-ff/`,
`refs/wt-sync-kept/`, `refs/wt-stack-map/`, `refs/wt-stack-fork/`,
`refs/wt-swept/`, `refs/wt-quarantine/`. `wt sync doctor --prune` and
`wt refs` clear the old ones.

In the repository's tree: `bin/worktree/worktree.conf` (or `worktree.toml`),
read from the checkout you stand in, else the main checkout's;
`bin/worktree/provision.sh`, run from the checkout you stand in
(`Repo.Root`); and `.wt-sync.yaml`, read from origin/<trunk> only. A quarantine folder holds `recovery.json`. Yours:
`~/.config/wt/config.toml`.

## The `--json` contract

`--json` output is read by programs, and one matters: gittree, which decodes
it and refuses to launch below the schema versions it pins. So:

- every shape has a JSON Schema in `schema/<name>.v1.json`, embedded by
  `schema.All` and printed by `wt schema <name>`;
- `x-version` is MAJOR.MINOR.PATCH; an added field or enum value is a minor;
  anything that breaks a reader (a rename, a removal, a changed type or
  meaning) is a new major in a new file, `*.v2.json`, never an edit to v1
  (`schema/schema_test.go` holds the major to the file name; the embed pattern
  in `schema/schema.go` names `*.v1.json`, so a v2 widens it);
- tests validate real output against the schemas (`testkit.ValidateJSON`)
  with every object closed, so a new field fails until the schema declares
  it; the shipped schemas stay open, so a reader tolerates an added field;
- [json.md](json.md) describes each field and keeps the changelog.

A change to the output updates the struct, the schema, its `x-version` and
json.md in one commit.

## Testing

Repository operations run real git on real repositories built per test. The
outside integrations are faked or kept away: gh, Superset, the Claude and
Codex sessions, tmutil (`testkit.FakeTmutil`) and devports
(`devportstest.Fake`).

- `internal/gittest`: `Isolate` keeps a package's tests off the machine
  (its own HOME, git config, PATH without claude or devports); `NewRepo`,
  `WithOrigin`, `Git` build and drive repositories; `NewStackLine`,
  `NewFoldLine`, `NewEditLine` build stacks for the sync tests.
- `internal/commands/testkit`: fixtures and fakes the command tests share (a
  fake gh, sessions, Superset), `Main` for `TestMain`, `ValidateJSON`.
- `<pkg>test` fixture packages (`synccmd/synccmdtest`,
  `provision/provisiontest`, `retire/retiretest`, `refs/refstest`,
  `quarantinecmd/quarantinecmdtest`): fixtures that run one command family,
  for its tests and the packages above it. Only tests import them.
- `cmd/wt` tests walk the command tree (help, examples parse, schema,
  completion); `test/*.bats` run the built binary.

`make test-unit`, `make test-fast`, `make test` and `make check`, and what to
run for which change: [testing.md](testing.md).

## Where to start

- **A flag.** Declare it in `cmd/wt/<command>.go`, carry it in the command
  package's options struct, say its default in the flag's usage where it is not
  obvious, and add an example if it changes how the command is used. A
  user-visible change adds a what's-new entry.
- **A sync strategy.** Implement `wtsync.Strategy` (`Name`, `Resolve`, refusing
  with a `*Refusal` what it does not own completely) in its own file, as
  `bullet_union.go` does, and add it to `kinds` in `internal/wtsync/strategy.go`,
  the one list Parse and the build both read. Document it in
  [sync-declaration.md](sync-declaration.md).
- **A step, or a step key.** `Deferred` and `EachCommit` in
  `internal/wtsync/config.go`; the runners are `defer.go` and
  `each_commit.go`; a `needs` value is `KnownNeeds` in `needs.go`, probed by
  `syncrun.Prober`.
- **A JSON field.** The struct in the command package (`syncrun/up_json.go`
  for runs, `synccmd/sync_json.go` for the overview, `jsonout` for shared
  parts), then the schema, its minor `x-version`, and json.md's section and
  changelog.
- **A new command.** A file in `cmd/wt`, added in `newRootCmd`'s group; the
  work in the family's package; `--json` only with a schema.
