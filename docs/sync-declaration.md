# .wt-sync.yaml — what wt sync is told about a repository

`wt sync` rebases worktrees onto trunk, or onto the ref `--onto` names.
`.wt-sync.yaml`, at the root of the
repository, tells it the shapes of the conflicts it may resolve and the work
that runs around a rebase. A repository without the file is shown by `wt sync`
and never rebased (`wt up` still takes a conflict-free rebase there).

The file is read from trunk, never from the branch being rebased: every entry
names something wt runs, and a branch must not be able to change that. A
branch that edits the file changes nothing until the edit is on trunk. A run
reads it from the one trunk commit it fetched. A resume reads it from the trunk
commit the run it continues read it from.

A rebase onto another ref (`wt sync rebase <work> --onto <ref>`, or `wt up
--onto <ref>`) reads the file from trunk too, never from the target: an
integration or release branch is rebased onto, its declaration is not taken.
The strategies resolve against the target's side of each conflict (an
`owned-line` version comes out above the target's), the scripts are trunk's, and
a repository whose trunk has no file refuses `wt sync rebase --onto` as it
refuses `wt sync rebase`, while `wt up --onto` still takes a conflict-free rebase.

A key wt does not know is an error, and so is a mistake in a key it does know:
the run refuses and says which entry. `wt sync doctor` checks the file before
the first run (`--json` gives its findings as records), and `wt doctor --all`
counts a file that does not parse as a problem.

```yaml
conflicts:
  - paths: [api/src/main/resources/application.yaml]
    strategy: owned-line
    line: '^    version:'
    rule: max-plus-patch
  - paths: [etc/openapi/*.json]
    strategy: openapi
  - paths: [settings.gradle]
    strategy: list-union
    line: '^include '

each_commit:
  - run: ./gradlew compileJava compileTestJava -q --offline
    paths: ["**/*.java", "**/*.gradle", gradle/libs.versions.toml]

defer:
  - run: ./gradlew compileJava compileTestJava
    paths: ["**/*.gradle", gradle/libs.versions.toml]
  - run: ./gradlew api:generateOpenApi
    paths: [etc/openapi/**]
    commit: "chore(api): regenerate the openapi specs after rebase"
    needs: [docker]

dependency_graph:
  - gradle/libs.versions.toml
  - "*/build.gradle"

test_paths:
  - "**/src/test/**"
```

## Paths

Every `paths` is a list of globs, matched against paths from the repository
root. `*` spans one path segment, `**` any number of them, none included.
Globs are matched, never expanded against the disk.

## `conflicts` — who resolves what

A list of rules. Each says that files matching `paths` have one conflict shape,
and names the strategy that resolves it. At a stop, each conflicted file goes
to the first rule whose `paths` match it; a file no rule claims is yours, and
the run hands the stop over with a plan file. A strategy that finds a conflict
not quite its shape refuses it, and the file is yours too: no strategy
guesses.

| key | meaning |
|---|---|
| `paths` | required: the files the rule claims |
| `strategy` | required: `owned-line`, `openapi`, `list-union`, `bullet-union`, `take-trunk` or `script` |
| `line` | a regular expression (Go's syntax), for `owned-line` and `list-union` |
| `rule` | how a version is decided, for `owned-line` and `openapi` (below) |
| `delimiter` | for `list-union`: what separates the items; `,` when left out |
| `run` | for `script`: the executable, relative to the root |

### `owned-line`

The file has one line, matched by `line`, whose value the branch owns: a
version both sides bump. Every other change must merge clean. Needs `line` and
`rule`.

```yaml
  - paths: [version.txt]
    strategy: owned-line
    line: '^\d'
    rule: max-plus-patch
```

Version lines pair across the sides by where they sit: the lines they nest
under by indentation, and the part of the line `line` matches. A version line
one side added is that side's and stays; one a side removed is refused. In a
file where several owned lines sit in the same place, such as a flat list of
`version:` lines, they pair by order, and a side with a different number of
them is refused.

### `openapi`

A generated OpenAPI document (JSON). Its paths, schemas and tags are merged key
by key; the same key changed on both sides is refused. `info.version` is
decided by `rule`, `max-plus-patch` when left out. It never regenerates: a
deferred step declared beside it does that once the rebase is done.

### `list-union`

The contested line, matched by `line`, is a list both sides only add to, such
as Gradle's `include`. Trunk's lines in a conflict stay as they are, in order,
blank lines included. Each item the branch added goes at the end of the trunk
line its own line shares the most items with (the first on a tie), so the
result is trunk's order first, then the branch's additions. A branch line that
shares no item with any trunk line, such as an `include` of its own, stays a
line of its own after trunk's lines. The one exception: when it is the only
list line on the branch's side of the conflict and the base side has a list
line, it is that line edited, and its additions join trunk's first line. An
item appears once in the file: one already on any list line that stays, or on
a trunk line anywhere in the file, is not added again.

Blank lines may sit in a conflict; the branch's own blank lines are dropped.
Any other line that is not a list line is refused, as is a conflict with
nothing on one side. An item one side removed is refused: a removal is a
decision, not a merge.

### `bullet-union`

A Markdown bullet list that both sides only add to, such as a README's
prerequisites or setup steps. It needs no other key. Trunk's items stay as
trunk has them, then the items the branch added follow in the branch's order.
An item is its bullet line (`- `, `* ` or `+ `) with the more indented lines
under it, and it moves and is compared as one unit; one both sides added is
written once in its list; another list may hold the same item.

```yaml
conflicts:
  - paths: [README.md]
    strategy: bullet-union
```

Trunk adds `- Node 22` and the branch adds `- Gradle 9` after `- Docker`:

```markdown
- Java 21
- Docker
- Node 22
- Gradle 9
```

It refuses, and the stop stays yours, when a side removes or changes an item
of the base, in the list it is in, wherever in the file (a nested line added
under an existing bullet changes that bullet, and so does a trailing space); when a conflict holds anything but bullet items
and blank lines, numbered lists included; and when the two sides' items use
different bullet markers or indents in one list. A removal or a rewording is a
decision, not a merge.

### `take-trunk`

Trunk's copy of the file, whole: a lockfile or a generated file nobody should
merge by hand. A deferred step declared beside it rebuilds it from the merged
sources.

### `script`

An executable in the repository, `run`, relative to the root and inside it. It
is read from trunk into a private directory and run from there, never from a
worktree. It is asked `--check <path>` against an index holding the conflict's
three versions, and `--resolve <path>` in the worktree during a run, where it
writes and stages the file. To `--check`, exit 0 claims the file, 1 does not
claim it, 2 refuses it with the reason on stderr; to `--resolve`, exit 0 says
the file is written and staged, which wt verifies. Anything else is an error. Each call has 60 seconds. A script can
only be checked by a real run, so the overview marks a worktree it claims a
file of `recipe?`.

### Rules for a version

| rule | the version the branch keeps |
|---|---|
| `max-plus-patch` | the higher of the two; when trunk's is higher, one patch above trunk's, so the branch keeps a version of its own |
| `shift-bump` | the pick's own bump made again on trunk's version, keeping its kind and size: a minor stays a minor, a patch a patch. The highest component the branch raised over the pick's parent is raised by the same amount on trunk's version; the components above it come from trunk, the ones below it from the branch. A pick that did not bump keeps trunk's version; one that lowered the version, or a version that is not X.Y.Z, is refused |
| `keep-branch` | the branch's |
| `keep-trunk` | trunk's |

A rule with `max-plus-patch` or `shift-bump` also lifts: a commit that changes
a claimed file which git merges without a conflict, while trunk bumped the same
version, has the version lifted too, at that commit, and the stop's note says
what it shifted.

## `each_commit` — a check after every replayed commit

Steps that run after each commit the rebase replays, at that commit: every
commit of a stack has to build on its own, because each is reviewed, bisected
and reverted on its own.

| key | meaning |
|---|---|
| `run` | required: a shell command, run with `sh -c` in the worktree |
| `paths` | the step runs at a commit that changes a path these match; empty or left out, at every commit |
| `needs` | what the step cannot run without (below) |
| `retry` | optional: a shell command, run with `sh -c` in the worktree, only when `run` fails (below) |

A check runs once the commit is final: after any stop at it is resolved and any
lift amended in. A commit git drops as empty is not checked. Steps run in the
order declared, each under its own 30 minutes; the first that fails stops the
rest. A check must not change tracked files: one that does has failed.

### `--each-commit` — where the checks run, for one run

`wt sync rebase --each-commit=all|tips|targets|off` says where this run's checks run.
`.wt-sync.yaml` has no key for it: the declaration stays what every run is
held to, and the flag is a person's choice for one.

| mode | the checks run |
|---|---|
| `all` | the default: at every replayed commit that touches a step's `paths` |
| `tips` | only at a layer's tip with `--stack` (each layer branch the line moves, and the line's own tip), and at the branch's tip without it |
| `targets` | at every layer's tip as `tips` does, and at each commit a `fixup!`, `squash!` or `amend!` folded into, once folded: for `--stack --autosquash` in place |
| `off` | nowhere |

Use `tips` for a line whose commits do not compile one by one: trunk removed
a method the line used, and the adaptation exists only on top of the rebased
line, so a check at the first commit that touches it can never pass. A check is
due at a tip when any commit replayed in that layer touched the step's `paths`
(a step with no `paths` is due at every tip), and its `needs` are judged as
now. A layer ends where the rebased line ends it, so a layer whose own tip trunk
carries, or that git drops as empty, is checked at the last commit of it that
stays, and a layer whose last commit is a fixup folded in by `--autosquash`
is checked at the commit it folded into. A layer with a commit that called for
a check is checked once, never zero times; a layer none of whose commits
stays has nothing to check. A failure stops at that tip like any other: fix it, amend the
fix in, `wt sync resume <work>`. `retry`, a script the commit predates and
`--allow-owed` work as in `all`, at fewer commits.

`tips` gives up bisectability for the run: the commits between the tips were
never checked, and a `git bisect` over them can land on one that does not
build. They are not owed, since leaving them out was the person's choice; the
plan file and the end of the run say which mode ran, and so does `--json`
(`eachCommit`). `off` runs no `each_commit` check at all and says so the same
way.

Use `targets` for a fold in place (`--stack --autosquash`, without `--onto`)
over a long line: there the commits that are not targets keep their own
changes, so the targets and the tips are the commits whose change differs, and
a fold that only touches string literals compiles about once per layer plus
once per target, not once per commit. At a target the check is due when the
folded commit (the target, with what folded into it) touches the step's
`paths`, as `all` judges each commit; at a tip it is due as under `tips`. A
commit a per-layer step folds generated output into is a target too, and the
rechecks after such a fold are at the targets and the tips only. The commits
between are not checked and not owed, as under `tips`, and `retry`, a script the
commit predates and `--allow-owed` work as there. `targets` is refused, before
anything moves, for a run that is not an in-place fold (no `--autosquash`,
`--onto`, or `--edit`): use `tips` there. A resume does not know the targets
of the run before it, so its rechecks after a per-layer fold cover every
commit the fold rewrote, and it says so.

### A layer the run leaves as it was

A `--stack` run skips the checks and the per-layer steps at a layer whose tip
the run leaves as the very commit it was, in every `--each-commit` mode, in
place or with `--onto`, and so does its resume. Nothing in such a layer can
differ: its tip, and everything below, is what it was. In practice these are
the layers wholly below the first commit the rebase rewrites, such as every
layer under the lowest fold target of a `--autosquash`. The run says so once,
at its end, for every layer it skipped (`layers 1-14 unchanged: their checks and
steps skipped`, or `layers 1-4, 6-9 unchanged: …` when they are not adjacent), and the commit is
judged when the sequencer reaches it: a layer with any commit, or a base under
it, that differs is checked as before, and when wt cannot tell it runs the
check. A step the last run left owed, or failed, at the layer still runs there,
so its record is settled; one that merely passed the layer does not. In
`--json` the skipped step's `layerSteps` row has `result: untouched` and
`ran: false`, and the checks that did not run are not counted in `checked`.
`wt sync steps` replays the line onto itself and runs at every layer.

A resume takes no `--each-commit`: it goes on with the mode its run was
given, which the handover records. `--each-commit=tips` with `--edit` is
refused, since an edit run's checks sit at the commits above the edit; with
`--autosquash`, `--onto` and `--if-ready` it means what it says (the
fixups are folded in, the target is where the line goes, and an `--if-ready`
run is still refused or taken as before).

### `--alone` — one worktree of a stack, as a throwaway copy

`wt sync rebase <work> --alone` rebases the one worktree named and nothing else
that is stacked on the same history. Without it, a run takes every worktree of
the stack and refuses all of them for one that is dirty. With it only the named
branch moves, onto trunk, replaying every commit it has above trunk: the
commits of a worktree below it are replayed too, as a copy of them. The other
worktrees of the stack are not triaged, locked, rebased, refused on or touched,
whatever state they are in; each is named, with the note that it sits on the old
commits. `wt sync <work> --alone` is the look: it names who a run would leave and
changes nothing.

| with | meaning |
|---|---|
| `wt sync undo <work>` | puts back what the run moved, the one branch and its worktree, and only that |
| `--if-ready` | the worktree named alone is judged: ready or refused; the rest of its stack is not asked about |
| `--json` | `leftAlone` lists the worktrees left, `work`, `branch` and `path`; `[]` for any other run |
| `--stack`, `--autosquash` | the line is rebased and folded exactly as a `--stack` run does (layer markers, the checks at layers, `--each-commit=tips`), and only the named branch moves: no layer branch is checked, moved, held, reset or locked, and no layer's worktree is touched. Each layer is named as left on the old commits, and is in `leftAlone`; each side branch is held, with the command that moves it. A reference run of a copy of a stacked line. `wt sync undo <work>` puts back the copy |
| `--edit` | refused: it moves the layers by the amend, and `--alone` moves none |
| `--onto` | refused: `--onto` already moves exactly the worktrees named |
| more than one worktree, none, `--all`, `--roots`, `--profile` | refused: it takes one worktree of one repository |
| `resume`, `undo`, `steps` | refused: they have no stack to leave out |

`wt up` takes no `--alone`: it moves the stack of the worktree it is in, and a
rebase of one worktree of a stack is a run you name.

### `retry` — a second go at the same commit

A build tool's output from the commit before can make `run` fail at a commit
that is fine: Gradle's incremental compile is the usual case. `retry` is the
command that runs at the same commit, in the same worktree, only when `run`
fails, for example the same build with `--rerun-tasks`.

- If `retry` passes, the check passes and the rebase goes on. The progress
  output says `passed on retry`.
- If `retry` fails too, the rebase stops as it does for a failing `run`, at the
  same commit. The stop names `run`'s failure and the retry's, and the plan file
  and the stop show the end of the **retry's** output.
- `retry` is run only for a commit the step runs at: `paths` and `needs` gate
  the step, and so its retry. It has its own 30 minutes, as `run` has.
- `retry` must not change tracked files, as `run` must not: one that does has
  failed. When `run` itself left tracked files changed, `retry` is not run on
  that tree and the stop is `run`'s. Untracked files either writes are left
  where they are, as for `run`.
- A step without `retry` behaves as before. `retry` is not run when `run`
  passes, and a failed check that you fix and resume runs `run` first again.

A check that fails stops the rebase at that commit and hands it over: HEAD is
the commit, nothing is unmerged, and the plan file names the step, its exit
and the end of its output. Fix it, amend the fix in with `git commit --amend`
(or add a commit), and `wt sync resume <work>` runs the check again at HEAD
and goes on only when it passes. A branch with a stack above it in the run is
put back instead, and the stack is not rebased.

In `wt sync rebase <work> --stack --autosquash`, which folds a line's
`fixup!` commits into their targets, a check runs at a target once its
fixups are folded in. `.wt-sync.yaml` has no key for `--stack`: a stack
held on one line is rebased with the same declaration as any branch.

The layers of a `--stack` line are the local branches, other than the line's
own, whose tips are on the line, bottom first. A branch with a backup name
(`wt refs sweep`'s backup patterns and `ref_keep_patterns`) is listed as not
taken and moves only when `--layer` names it. Without `--layer`, a branch with
no commits of its own (its tip is the line's tip or another layer's) that has
uncommitted changes to tracked files in a worktree other than the line's own
is listed as not taken, not held as a layer: it is probably a spike on the same
commit. A line this would leave with no layer keeps them all. `--layer` takes
it by name or by pattern, and its dirty worktree then holds it: it stays on
the old commits until those changes are committed or stashed.

`--layer` names the layers instead, and is repeated for several: a name that
is no local branch, or whose tip is not on the line, is refused. A value with
`*` or `?` is a pattern, matched against the whole branch name as the backup
patterns are (`*` crosses a slash, `?` is one character, no `[...]`); quote it
so the shell does not expand it. It takes every local branch it matches whose
tip is on the line; a match that is a backup name or off the line is listed as
not taken, and a pattern that takes no layer is refused before any branch of
the stack moves: one that matches no local branch at all after the fetch and
before local trunk is fast-forwarded, one whose matches are all backup names
or off the line once the line is read, after trunk is fetched and local trunk
perhaps fast-forwarded.
`wt sync rebase tmp/stack --stack --layer 'tmp/layer/*'` takes a stack of
many layers in one flag and leaves a stray branch on the line's tip out.

A `--stack` run also carries the line's side branches: the local branches cut
from a commit of the line that are not on it, those stacked on them, and those
above its top (`--no-side-branches` leaves them alone, unread). Their own
commits are replayed in the object store onto where the rebase put the commit
they fork from, and nothing of the declaration runs on them: no conflict rule,
no check, no deferred step. A side branch whose commits conflict there is held
with `wt sync rebase <branch> --onto <new> --upstream <old>`, a run of its own
that does run them, with its stops, `wt sync resume` and `wt sync undo`. A
branch with a backup name is listed and never moved. Branches at one commit
are siblings: carried together onto one new commit, or held together when
anything holds one of them, and what is stacked above them goes onto that
commit. The look names what holds a branch once, short (`a process in its
worktree: bash (pid 31227)`), and what frees it; only a run prints the
command that moves a held branch, since only a run knows the new commits.

The look lists the line's own branch as the last row of its layer table,
marked as the top, with its commits from the line's base.

`--autosquash` alone folds in place, onto the line's own base, and says so at
the top of its output (`folds <work> in place on its own base <sha>`, then the
trunk commit the declaration and its scripts are read from); a conflict with
trunk, which an in-place fold never meets, is not shown as contested. When the
content check at the end passes it prints `✓ tip tree equal to <old tip>`,
naming the step paths it left out. The replayed
trees can predate a script trunk declared since. A step whose `run` starts
with a repository path (`bin/check.sh`, `./gradlew`) that is not in the
replayed commit's tree is not run there and does not stop the rebase with
exit 127: it is reported as not checked at that commit, owed, and a run with
anything owed is not pushed or landed, as with an unmet `needs:`. A path that
trunk does not have either is a declaration error, said once, and owed the
same way. A step whose script is there and fails still stops. To check each
commit with trunk's scripts, fold and move in one run:
`wt sync rebase <work> --stack --autosquash --onto <trunk>`. The line's
content must then come out as git merges the old line onto trunk in every
file that merges cleanly, outside the paths the per-layer steps own; a file a
stop resolved, by a strategy or a person, is theirs in its conflicting region,
and the rest of the merge must still be in it. In place or onto a target, a
fixup is refused before anything moves unless it names exactly one commit that
will be replayed, found as git finds it (a commit id, tag or revision on the
line, else the subject, whitespace after `fixup!` ignored). One whose target
the base it is replayed onto already carries or reaches (trunk with `--onto
<trunk>`, the line's own base in place) is refused with both commits named;
one that is ambiguous or names nothing is refused with the fixup and the name
it gave. Every new layer tip must descend from the destination.

#### Moving part of a commit into an earlier one

A `move!` commit moves part of one commit of the line (the source) into an
earlier one (the target) in the same fold, where a `fixup!` can only move a
whole commit. The part is whole paths, hunks of a file chosen by line ranges, or
both. It is an empty commit at the top of the line, written by hand or by
`wt sync move <work> [<path>...] [--lines <path>:<from>-<to>] --from <source> --into <target>`:

```
move! feat: refusals

Move-From: feat: the door
Move-Path: door/refuse.go
Move-Lines: door/door.go:12-20
```

Each of target and source is a commit id or a whole subject that exactly one
commit of the line has; a shared subject is refused, naming the commits. A path
is named by `Move-Path` (the source's whole change to it) or by `Move-Lines`
(some of its hunks), never both; each may be repeated.

A `Move-Lines` range counts lines in the file as the source leaves it,
inclusive, and takes every hunk of the source's diff of that path it overlaps,
with git's default three lines of context (`git diff <source>^ <source> --
<path>`). It must cover each such hunk's changed lines whole: a range that stops
inside a hunk is refused, naming the hunk (`door/door.go:@@ -10,7 +10,9 @@`)
and the range. A hunk that only removes lines is named by the line after the
removal. A removal at the end of a file, a deleted or emptied file, a binary file and a
file whose type the source changes have no line to name: move the path whole. A
change of a file's mode stays with the source.

The fold, `wt sync rebase <work> --stack --autosquash --each-commit=targets`,
applies that part of the source's change at the target, after the target's own
fixups, as a fixup of a commit it builds on the target and keeps under
`refs/wt-sync-kept/<branch>/<epoch>-move-<n>`; the source's replay then no
longer makes that part of the change, keeps the rest of it, and the `move!`
commit is dropped. The tip's tree stays the line's, so the content check holds
as for any fold in place, and undo puts back every layer.

Before anything moves the run replays the whole fold in the object store and
refuses, naming the cause: a target or source that does not name one commit
below the `move!` commit, a target not below the source, a `move!` commit that
changes anything, a path the source does not change, a range that splits a hunk
or selects none, every hunk of every path the source changes (make it a
`fixup!` instead), a hunk two `move!` commits take, a part that does not apply
at the target (naming the first commit in between that carries its context),
any commit of the fold that does not merge clean with the part moved, a source
whose replay would change anything but what the moves leave it, and a fold that
would end in another tree. It needs `--each-commit=targets`, so a part that
builds only with code from a commit in between fails the check at the target's
layer; `--onto` and `--edit` are refused for now. `wt sync <work> --stack
--autosquash` lists each move, or the refusal, and moves nothing.

#### A fixup moved ahead of its context

A fixup written against the top of the line and folded into a commit low on
it can change lines that only a later commit adds: right behind its target,
its hunk has no context. Before anything replays, `wt sync rebase <work>
--stack --autosquash` (with or without `--onto`) names each such hunk, and
`wt sync <work> --stack --autosquash` says the same and moves nothing:

```
⚠ 1a2b3c4 "fixup! feat: page size": fixup moved ahead of its context: src/state.ts:13 needs 5d6e7f8 "feat: open flag", which comes later on the line
```

Each fixup is merged in the object store onto its target with the fixups of
the same target before it, from its own parent, as the rebase will merge it;
a file that does not merge clean is looked at hunk by hunk, and a hunk whose
lines before the change the target lacks names the first later commit whose
tree has them. A conflict no later commit explains is an ordinary one and is
not named. A fixup that conflicts leaves its clean part folded in for the
next fixup of the same target; when what that next one lacks may be the
earlier fixup's conflicting part, or another fixup's, it is named as not
analysed rather than given a commit it does not need. The trees are the
line's own, before any move onto `--onto`'s ref. No ref, index or worktree
is touched. It is a warning: the run goes ahead, and `--json` has the hunks
as `contextGaps` on the stack, and what could not be analysed, an analysis
that failed included, as `contextUnchecked`; the look and the run print
both.

When the run stops at such a fixup, the plan file and the stop say why with
that line instead of `same hunk both sides`, and point the way out: not a
resolution, but `wt sync undo <work>` and moving that hunk into a fixup of the
commit named (`git commit --fixup=<sha>`). If you resolve it anyway,
`wt sync resume <work>` checks the fixup line by line before anything
replays, counting the net effect: for each line the fixup adds, the copies of
it in the resolution, plus those the commits still to be replayed add, less
those they remove, must come to the copies the old tip (the run's safety ref)
has in that file. So a later commit that takes the line away again does not
count, and a copy the target already had does not stand in for the fixup's.
A file a later commit renames is followed to its new name, in the old tip and
in the commits to come, and one the rebase's target renamed (with `--onto`)
to the name the index holds. A binary file is compared by blob: the old tip
has the fixup's version, the resolution does not, and no commit to come
writes it in its own change; a deletion the fixup made and the resolution
kept is no drop. A line short is a loss, and resume refuses, changing
nothing:

```
the resolution drops 1 line of 1a2b3c4 "fixup! feat: page size" that the old tip has: src/state.ts:13
```

Fix the resolution, `git add` it and resume again. `--allow-dropped` goes on
when the drop is meant; the fold's content check at the end still holds the
layers to the old line. What the check cannot tell is printed as not
checked, never passed in silence: lines with no letter or digit (blank
lines, braces), too common to prove anything; a file the old tip no longer
has under a name git can follow; a binary file the line changes again after
the fixup; a binary file the fixup deletes and the resolution keeps; a file
still unmerged or not in the index under a name that can be followed; a file
that could not be read. A check that cannot run at all refuses with the
reason, and `--allow-dropped` goes on past that too. A fixup's removed lines
are left to the content check.

`wt sync` says how many commits a run would check, and `wt sync <work>` which,
without running anything. After the last commit, `defer` is still the place
for work that runs once.

```yaml
each_commit:
  - run: make build
    paths: ["src/**", Makefile]
  - run: ./gradlew compileJava -q
    retry: ./gradlew compileJava -q --rerun-tasks
    paths: ["**/*.java"]
```

### `wt sync resume --drop` — a commit trunk already carries, reworked

A stack commit that was lifted into a pull request and reworked a little before
it merged is on trunk as another patch: git does not drop the stack's copy, and
the replay can stop on it. The look names such a commit (same subject and author
as a commit trunk added since the range began, with that commit), and the stop
it causes says so too, with the files both commits change. wt keeps and replays
the commit unless you choose otherwise; subject and author alone never drop it.

At that stop, `wt sync resume <work> --drop <commit>`, naming the commit the
rebase stopped at, takes trunk's side of every file both commits change and
keeps the commit's changes to its other files; a commit left with nothing is
dropped. Before it replaces anything, every file it replaces that you or the
stop changed is kept in a commit under `refs/wt-sync-kept/<branch>/<epoch>-drop`
(a second drop of the same stop that has more to keep adds `-drop-2`), recorded
with the handover and named with the command that shows it; undo leaves that ref.
A file a strategy resolved stays as it resolved it. The drop refuses, changing
nothing, a symlink or a submodule among the files it would replace, a file
staged one way and changed another in the worktree, and a conflict in a file
trunk's commit does not touch: that one is still yours, so resolve it and
`git add` it first. A staged file counts as staged one way and changed another
when its content or its executable bit differs. What is kept is the bytes and
the mode in the worktree, with no clean filter applied. The handover records
which files the drop has still to replace: while any are left a plain
`wt sync resume` refuses and says to run `--drop` again, which finishes them. The taken files are the stop's resolution, as if you
had resolved them, so the layers count what is left and the fold's content check
holds the rest of the line; `wt sync undo` puts the commit back with the rest of
the run.

### `wt sync resume --adopt` — a rebase finished by hand

A rebase you finished yourself with `git rebase --continue` and then committed
on top of (an adaptation to a method trunk removed, say) is refused by a plain
`wt sync resume`: it cannot tell your commits from what a deferred step
committed on an earlier try. `wt sync resume <work> --adopt` takes it over when
the branch's reflog proves the one shape it accepts:

- HEAD is on the branch, and nothing tracked is uncommitted;
- the reflog has git's own `rebase (finish)` for the branch, from the tip the
  run started at, onto what the run rebased onto;
- every move after it is a commit (or a cherry-pick) on top of the one
  before: no reset, amend, merge or rebase;
- no more commits from onto to where the rebase finished than the run set out
  to replay.

Anything else is refused with the reason, as before, and a plain resume that
refuses this shape says `--adopt` is there. With `--adopt`, before anything
moves, the tip is kept under `refs/wt-sync-kept/<branch>/<epoch>-adopted`, a
ref that is only ever created. The layers of a `--stack` run move to the
markers its rebase wrote, checked the way a run checks them, never matched by
subject; if one does not check out, nothing moves. The commits made after the
rebase stay on the line's own branch and are named. Then the deferred steps
run, and the checks run at every layer's tip and at the line's tip, as under
`tips`. Under `all` that is said: the commits between were never checked.
Under `targets` it is said too: the commits a fixup folded into are not known,
and are not checked. Under `off` no check runs. A check that fails, and a per-layer step (`each: layer`)
the hand finish went past, is owed, and a worktree with anything owed is not
pushed. The checks of lower tips run in a temporary worktree of their own, and
only that worktree's registration is ever removed, an interrupt's included. An
interrupt during the checks leaves the line adopted and the checks owed, and
says where the tip is kept. `wt sync undo <work>` afterwards puts the line and its layers back as
they were before the run, and says that the kept ref still holds the adopted
tip. `--adopt` does not take over a `--stack --edit` run or a fold.

## `defer` — work after the rebase

Steps that run once, after the last commit is replayed, in the order declared;
or, with `each` or `fold`, at the end of every layer inside the rebase.

| key | meaning |
|---|---|
| `run` | required: a shell command, run with `sh -c` in the worktree |
| `paths` | the step runs when the rebase changed a path these match, comparing the branch before and after; empty or left out, after every rebase |
| `commit` | the message to commit the step's output with, when it changes tracked files |
| `needs` | what the step cannot run without (below) |
| `each` | `layer`: the step runs at the end of every layer, inside the rebase (below) |
| `fold` | `owner`: the step's output is folded into the commit that owns it (below); needs `paths` |

Each step has 30 minutes. A step that changes tracked files and declares no
`commit` has failed. A step that fails is owed: the rebase stands, the steps
after it do not run, and a worktree with anything owed is not pushed. Run the
step by hand, then push, or run `wt sync steps <work>`. A stack parent's owed
step does not stop the run from rebasing its children.

```yaml
defer:
  - run: npm install --package-lock-only
    paths: [package.json]
    commit: "chore: refresh the lockfile after rebase"
```

### `each: layer` and `fold: owner` — a step at every layer tip

A generated file that CI checks on every pull request (run the generator, then
`git diff --exit-code`) has to be right at every layer of a stack, and belongs
in the commit that changed it, not in a commit of its own on top.

```yaml
defer:
  - run: ./gradlew api:generateOpenApi
    paths: [etc/openapi/**]
    each: layer
    fold: owner
    commit: "chore(api): regenerate the openapi specs"
    needs: [docker]
```

With `each: layer` the step runs inside the rebase, at the end of every layer,
bottom first: in `wt sync rebase <work> --stack` each layer branch, and the
line's own commits above its top layer as one more; in any other run the
branch is the one layer, so a stack of worktrees gets the step at the end of
each branch's own rebase. At a layer's end HEAD is that layer's new tip, and
the step runs when its `paths` match a path that differs between the layer's
old and new tip, or that a commit of the layer changes. A line says which
layer it is at (`▸ step <run> layer 3/6 <branch>`).

What the step changes under its `paths` is its output. With `fold: owner` it
goes into the owner, the last commit of the layer that changed one of those
paths: that commit and every commit of the layer after it are rewritten with
those files taken from the step's output, and nothing else changes. Before the
rebase goes on, wt proves that the rewritten tip holds exactly the step's
output and that no rewritten commit differs from the one it replaces outside
the step's `paths`. The layers above are then replayed onto the folded tip,
in the same rebase. A layer where no commit changed those paths gets the step's
output as a commit of its own at its tip, with the `commit` message; with no
`commit` declared the step has failed there. Without `fold: owner`, an
`each: layer` step commits its output that way at every layer. `fold: owner`
without `each: layer` takes everything the rebase replayed as one layer; a
`--stack` run refuses it before anything moves, since it would rewrite
commits under the layers' markers.

A step that fails at a layer, changes a tracked file outside its `paths`, or
whose fold is not proved is owed at that layer and every layer above, where
it does not run again; the layers below stay folded and proved, the rebase goes
on to the end, and nothing owed is pushed. A rename or copy the step stages counts at both
ends: either one outside its `paths` is output outside them. A rebase handed
over to you keeps what its layer stops did, and `wt sync resume` goes on from
there: the same steps, the plan as they changed it, and what is owed.

Above a fold, a replayed commit that changes the same generated file can
conflict with the folded output. When the declared strategy refuses it, or
nothing claims the file, the replayed commit's side is taken and said
(`each: layer took the commit's side`): the stop at the end of that layer
writes the file again. Such a conflict is never handed to a person.

An `each_commit` check runs after each pick as before, so before the fold;
one whose `paths` take in a file the step wrote is run again at every commit
the fold rewrote, and at the step's own commit. `--autosquash` without `--onto` and
`wt sync steps --stack` move nothing onto trunk, and the line's content must
come out the same outside the per-layer steps' `paths`.

`wt sync steps <work>` runs what the last run left owed, without rebasing
onto trunk: each run records what it owes for the branch, and this clears
what it settles. `--all` runs every declared step, `--stack` at every layer
tip of the line. Its `each_commit` checks run under the mode of the run that
owed the steps, which the record keeps, and the first lines name it
(`each_commit: tips, as the run that owed these steps`); `--each-commit`
overrides it. `targets` reads as `tips` there, since steps folds no fixup and
says so. A record an older wt wrote names no mode, so it runs under `all`,
and the line says that. `--all` runs every step and keeps the recorded mode
too; only with no record to take one from does it run under `all`. The JSON says, per layer and step, whether it ran and why,
the commit it folded into, its commit, what is owed, and the proof
(`layerSteps`, docs/json.md).

## How long it took

Every `each_commit` check, rechecks included, ends its line with its time:
`✓ <name>  12s`, where the name is the base name of the command's first word
(numbered, `make (#2)`, when two declared checks share it); a pass on retry and
a failure name the `run` and carry the time too, a retry's counted with its
`run`. Every per-layer step's result line carries its time, and so does each
deferred step's.

A run and a resume end with a summary, when a check or a per-layer step ran:
the wall time (`⏱ the run took 14m3s`), each check and step with how many
times it ran and its total, and the slowest three with their commit or layer.
A resume says its numbers cover only what it ran. `--json` has the same as
`timings` on the participant and `elapsedMs` on each `layerSteps` entry
(docs/json.md).

## Locks — one run per worktree

A run takes a lock in each worktree it rebases or moves a layer in,
`wt-sync.lock` in the worktree's own git dir, naming its process, when it started and who ran it. A
second run is refused while the lock holds, with the pid and since when. A
lock holds while it is younger than 30 minutes, and for as long as its process
still runs, however old: a fold that runs for hours keeps its worktree. A
process with that pid that started after the lock was taken reused the pid and
does not hold it; one whose start cannot be read counts as running, so the cost
of doubt is a refused run, never two at once. A refusal of an old lock says
that the process is still running and that stopping it frees the worktree.

Once the process is gone, a lock older than 30 minutes is expired: the next run
breaks it, `wt sync doctor` names it and `--fix` removes it. A live one the
doctor only names, with no fix. Ctrl-C releases every lock the run holds; a run
that hands a stop over leaves its lock for `wt sync resume` to take over.

Local trunk's fast-forward after the fetch is not under this lock: it runs
before the run locks anything, and has its own checks instead (trunk checked
out nowhere, or in a clean checkout with nothing in progress and no session
busy in it; `wt up --help`).

## Waiting for a run in the background

A shell tool of an agent cuts a foreground command off after about ten
minutes, and a fold of a long line takes longer, so the run goes to the
background. `wt sync wait [<work>] [--timeout D]` waits for it: it prints what
the run prints (`▸ check n/N`, `▸ step … layer n/N`, the stops, the handover,
the timing summary), from the start of the run and then as it goes, and exits
with the run's own code.

```
wt sync rebase tmp/stack --stack --autosquash --yes > /dev/null 2>&1 &   # the run
wt sync wait tmp/stack --timeout 9m    # shows progress; exit 124: run it again
```

`wt sync rebase`, `wt sync steps` and `wt sync resume` copy
what they print into a log of their own, `wt-sync-logs/<start>-<pid>.log` under
the repository's git dir (the person's output is as it was; with `--json` the
copy holds what goes to stderr, never the result object). The log is made when
the run takes its first lock. A worktree whose lock the run takes gets a
`wt-sync-run.json` in its own git dir that names the log; a later run in the
worktree replaces it. The exit code goes in `<log>.end` when the command ends.
A look (`wt sync <work>`), a refusal before any lock, `wt sync undo`, `wt up`
and a rebase across repositories (`--all`, `--roots`, `--profile`) write none, and a
wait on their worktree finds no run. Logs older than 7 days whose process is
gone are removed when a new one is made. A log that cannot be written is one
warning and never changes the run.

A wait only reads. Its exit codes:

| code | meaning |
|---|---|
| the run's | 0 done; 1 a handover or a failure (with a line pointing at `wt sync resume <work>` when the run left a plan); 130 Ctrl-C, 143 SIGTERM |
| 124 | `--timeout` passed with the run still going; the run is untouched, wait again. A run that has ended exits with its own code, even when the timeout passes while the last of its log is still printing: the rest is left unprinted |
| 1 | no run was seen in the worktree; a newer run holds it that the wait cannot follow; the run ended without saying how (killed or crashed: `wt sync doctor`) |

## `needs` — what a step cannot run without

A step under `defer` or `each_commit` may name what it cannot run without. The
names are wt's own.

| need | probed by |
|---|---|
| `docker` | `docker info`, within 10 seconds |
| `env:<NAME>` | `<NAME>` is set and not empty in the environment the steps inherit |

`<NAME>` is a valid environment variable name (letters, digits and `_`, not
starting with a digit); anything else is a declaration error that `wt sync
doctor` and a run report. The probe looks only at whether the variable is set
and not empty: it never prints, logs, records or hashes the value, so the
value is in no output, plan, state file or `--json`. A variable set to the empty
string counts as missing.

Before a run moves any branch, it probes each need once, and only when a step
with it may run: an `each_commit` step with a commit to check, or a `defer`
step whose `paths` match a path the rebase may change (what trunk changed
since the branch's merge base, or what the branch's commits change). A need
that does not answer refuses the worktrees whose steps need it, and what sits
on them in a stack, before anything moves: the line names the step, the need
and what the probe said. `--allow-owed` rebases them anyway: the step is not
run, and is owed as a failed one is. An `each_commit` step is then owed once
for the branch, not checked at any commit.

`wt sync --json` reports each need and what its probe answered, so a tool can
decide before it starts, and `wt sync doctor` shows a `needs` row for Docker
when a step declares it, and for each `env:<NAME>` (`set` or `not set`).

```yaml
defer:
  - run: ./gradlew api:generateOpenApi
    paths: [etc/openapi/**]
    commit: "chore(api): regenerate the openapi specs"
    needs: [docker]
  - run: pnpm install
    needs: [env:GH_PACKAGES_TOKEN]
```

## `dependency_graph` — build files worth a note

Globs of files whose change on both sides is worth a line in the overview: a
build file or a version catalog trunk and the branch both changed. It is a
note, never a class: it changes nothing a run does.

```yaml
dependency_graph:
  - gradle/libs.versions.toml
  - build.gradle
```

## `test_paths` — the test files

Globs, as `paths` are, of the files that are tests. It is optional, and it
changes nothing a run does: it only lets the end of a rebase say more.

Every finished rebase (`wt sync rebase`, in place, with `--onto` or `--stack`,
and the `wt sync resume` that finishes one) names the commits it set out to
replay that left no commit in the result, as `dropped as empty` lines and in
`--json` as `emptied`: trunk already had the change, or a conflict was
resolved to trunk's side. Fixups and squashes that `--autosquash` folds, and
the commit an `--edit` amend empties on purpose, are not named. With
`test_paths` the end also names, as `kept only its tests` and `testsOnly`,
each commit whose replay changed only paths matching them while the original
changed other paths too: trunk made the fix, and what is left of the commit is
its tests.

```yaml
test_paths:
  - "**/src/test/**"
  - "**/*Test.java"
```

A commit is matched to its replay by author name, email and author date plus
subject, which a rebase keeps; where wt drove the rebase, its own plan says
which fixups it folded and which subject an `amend!` gave a commit. A rebase
finished by hand, or one wt has no plan of, has no such record: a commit whose
key is missing, but whose author and date or author and subject a commit of the
result has, or that a fixup or `amend!` in the range could have changed, is named
as `not found after the rebase`, `unmatched` in `--json`, not as dropped. So are
commits that share all of author, date and subject when fewer of them came out
than went in, any commit left without a partner while more commits came out
under some key than went in, and a fixup the plan lists as folded when wt did not
see the rebase run that fold into its target (a person went on by hand, or the
record is older than the watch): it may have been skipped, even where its change is
in the result. It is named `not confirmed folded` in the output. A commit that shares its key with another and comes out is never
named as `testsOnly`.
