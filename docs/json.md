# wt's JSON output

`wt config`, `wt status`, `wt up`, `wt sweep`, `wt sync` and its verbs `rebase`,
`resume`, `undo`, `push` and `steps`, `wt new`,
`wt checkout`, `wt remove`, `wt restore`, `wt purge`, `wt refs` and
`wt session list`, `send` and `stop` print JSON on stdout when given `--json`,
for tools that drive wt (a git client, an editor, a script). Their human output is
unchanged without it. The `recovery.json` in the folder a removed worktree was
moved to is JSON with a schema too.

## Versions

Each output has a schema with a semantic version, and says which it follows:

- `schema` — the **major** version, an integer. A new major is a change that
  breaks a reader: a field renamed or removed, a type or a meaning changed. It
  comes as a new schema file (`up.v2.json`); v1 is never edited that way.
- `schemaVersion` — the full version, `MAJOR.MINOR.PATCH`. A **minor** adds a
  field or an enumeration value; a **patch** is wording only, and a wording
  change does not have to bump it.

So within one major, fields are only added and enumerations only grow: a reader
ignores fields it does not know and treats an unknown value as "not one I know",
never as an error. wt's tests fail when an output's `schemaVersion` is not its
schema's.

Each schema is versioned on its own. The [changelog](#changelog) at the end
lists every version of every schema.

A string field that has no value is `null`, not `""`. Paths are absolute.

A plan's `token` is opaque: compare it, never parse it. Its value can change
between wt versions, so `--expect` can refuse a token another version gave as a
changed plan; read the plan again.

`quarantine` and `quarantined`, in fields, values and two schema names, are a
worktree that `wt remove --move-to <dir>` or `wt sweep --move-to <dir>` moved
into a folder instead of deleting it; wt's help and messages call it a removed
worktree and its folder. The names are schema 1's and stay. A tool that has
to know which spelling its wt takes reads `wt schema remove`: an `x-version`
of 1.4.2 or later means `--move-to` and `wt purge` exist.

## Exit codes

Exit 0 means success, 1 a runtime failure, and 2 a usage mistake (arguments
or flags). Exit 3 means launchd is unavailable for `wt sync keep start` or
`stop`; use `wt sync keep once` from cron instead. `wt sync wait`, which has no
`--json`, exits with the code of the run it follows, and 124 when its `--timeout`
passed with that run still going.

## Signals and what wt starts

Every git, script and build wt runs gets a process group of its own. Its
deadline and a handled SIGINT or SIGTERM to wt stop that whole group, however
many processes it forked, before wt writes its object and exits 130 for SIGINT
or 143 for SIGTERM. Each verb's section below says what that object holds.

SIGKILL, or a crash, leaves wt no chance to do that. So a command that can write
or run long — a `provision.sh`, a build, a deferred step, a git that changes
something — is also listed with a small helper wt starts the first time it needs
one: wt's own binary as `wt reaper`, in a process group of its own, holding
none of wt's stdout, stderr or other descriptors. When wt is gone however it went, the reaper
sends each listed group SIGTERM, then SIGKILL after 200 ms, and exits. So a
caller only has to kill wt, or the process group it started wt in, and nothing
wt started keeps writing to a worktree. The groups go within about a quarter of
a second, not in the same instant. A group is taken off the list once its
command has exited, before wt reaps it, so the reaper never signals a group id
that has been reused since. The reaper reads everything wt wrote before it acts.
A process the command left running in its group after the command itself
exited is not stopped, just as a handled signal does not stop it. Reads — `wt
list`, `wt status` (its rebase simulation only adds objects), `wt schema`, the
queries every verb makes — never start a reaper.

Why not run everything in wt's own group, so that one signal from the caller
stops the lot? Then wt could stop its children only by signalling its own group.
That group also holds the other commands of a pipeline (`wt up --json | jq`), or
the script that called wt without job control. Escalating to SIGKILL would kill
wt before it wrote its object. And signalling only its direct children would let
their children run on.

## Sessions — Claude and Codex

A session is an agent working in a worktree. A busy one refuses what would
change files under it; an idle one is waiting for its person. The session wt
itself runs under is never listed. A background Claude session opened in a
terminal is one session, the terminal's, though claude lists it twice.
Wherever sessions are listed, `kind` says whose it is:

| `kind` | Found by | `name` | `state` |
|---|---|---|---|
| `claude` | `claude agents --json` | the session's name | `idle` for an interactive session with status idle, and for a parked background one: blocked, with neither a status nor a pid in the listing, which is one no process is running. Anything else is `busy` |
| `codex` | `ps`, each codex process's working directory, and the turn markers in `$CODEX_HOME/sessions` (default `~/.codex/sessions`) | `codex exec`, `codex`, or `codex (<originator>)` | see below |

A Codex session is one of two things:

- **A codex process working in the worktree**: `codex exec`, `codex review`
  or the interactive `codex`, with its working directory (or `--cd`) there.
  The launcher and the binary it starts are one session. `codex exec` and
  `codex review` are `busy` for as long as they live. An interactive `codex`
  is `idle` when it has a thread of its own logged in its directory since it
  started and no thread it could be running has a turn open; it is `busy`
  when one has — or when no log of its own can be found or read, which is
  not knowing, never idle. Where sessions carry them, `id` is `codex-<pid>`
  and `pid` the topmost process.
- **A thread an app-server runs there** (the Codex desktop app, its managed
  daemon, the Claude Code plugin): no process stands in the worktree, so it
  is a session only while its turn is open, it was written to in the last 24
  hours and an app-server that could be running it is alive, and it is
  always `busy`. `name` is `codex (<originator>)`, `id` the thread id, `pid`
  the app-server's, or null when more than one could be running it. An
  interactive `codex` whose thread an app-server runs is listed as both.

An open turn with nothing alive behind it is a crashed session and is not
listed. Only your own processes are looked at.

The listing fails, which is `sessionsError` or `sessionsUnknown` and never an
empty list, when `ps` fails, when a codex process will not say where it works
and neither its launcher, its `--cd` nor its threads do, when the logs cannot
be walked, and when a log that cannot be read may be a thread an app-server
is running.

Under an app-server nothing names the thread wt was started from, so the
session left out as wt's own is every such thread working in wt's own
working directory.

## JSON Schema

Every object has a JSON Schema (draft 2020-12), for validating what you read or
generating types from it:

<!-- json-docs:begin index -->
| Output | Schema | Version | `$id` |
|---|---|---|---|
| `wt checkout --json` | [`schema/checkout.v1.json`](../schema/checkout.v1.json) | 1.2.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/checkout.v1.json` |
| `wt checkout --dry-run --json` | [`schema/checkout-plan.v1.json`](../schema/checkout-plan.v1.json) | 1.3.1 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/checkout-plan.v1.json` |
| `wt config --json` | [`schema/config.v1.json`](../schema/config.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/config.v1.json` |
| `wt new --json` | [`schema/new.v1.json`](../schema/new.v1.json) | 1.3.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/new.v1.json` |
| `wt new --dry-run --json` | [`schema/new-plan.v1.json`](../schema/new-plan.v1.json) | 1.3.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/new-plan.v1.json` |
| `wt purge <dir> --yes --json` | [`schema/quarantine-purge.v1.json`](../schema/quarantine-purge.v1.json) | 1.0.1 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/quarantine-purge.v1.json` |
| `wt purge <dir> --dry-run --json` | [`schema/quarantine-purge-plan.v1.json`](../schema/quarantine-purge-plan.v1.json) | 1.0.1 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/quarantine-purge-plan.v1.json` |
| `<dir>/recovery.json` | [`schema/recovery.v1.json`](../schema/recovery.v1.json) | 1.2.1 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/recovery.v1.json` |
| `wt refs purge --yes --json` | [`schema/refs-purge.v1.json`](../schema/refs-purge.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/refs-purge.v1.json` |
| `wt refs purge --dry-run --json` | [`schema/refs-purge-plan.v1.json`](../schema/refs-purge-plan.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/refs-purge-plan.v1.json` |
| `wt refs restore <runId> --yes --json` | [`schema/refs-restore.v1.json`](../schema/refs-restore.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/refs-restore.v1.json` |
| `wt refs restore <runId> --dry-run --json` | [`schema/refs-restore-plan.v1.json`](../schema/refs-restore-plan.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/refs-restore-plan.v1.json` |
| `wt refs sweep --yes --json` | [`schema/refs-sweep.v1.json`](../schema/refs-sweep.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/refs-sweep.v1.json` |
| `wt refs sweep --dry-run --json` | [`schema/refs-sweep-plan.v1.json`](../schema/refs-sweep-plan.v1.json) | 1.1.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/refs-sweep-plan.v1.json` |
| `wt refs swept --json` | [`schema/refs-swept.v1.json`](../schema/refs-swept.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/refs-swept.v1.json` |
| `wt remove <work> --yes --json` | [`schema/remove.v1.json`](../schema/remove.v1.json) | 1.6.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/remove.v1.json` |
| `wt remove <work> --dry-run --json` | [`schema/remove-plan.v1.json`](../schema/remove-plan.v1.json) | 1.6.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/remove-plan.v1.json` |
| `wt restore <dir> --json` | [`schema/restore.v1.json`](../schema/restore.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/restore.v1.json` |
| `wt restore <dir> --dry-run --json` | [`schema/restore-plan.v1.json`](../schema/restore-plan.v1.json) | 1.1.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/restore-plan.v1.json` |
| `wt session list --json` | [`schema/session-list.v1.json`](../schema/session-list.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/session-list.v1.json` |
| `wt session send --json` | [`schema/session-send.v1.json`](../schema/session-send.v1.json) | 1.1.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/session-send.v1.json` |
| `wt session stop --json` | [`schema/session-stop.v1.json`](../schema/session-stop.v1.json) | 1.0.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/session-stop.v1.json` |
| `wt status --json` | [`schema/status.v1.json`](../schema/status.v1.json) | 1.12.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/status.v1.json` |
| `wt sweep --yes --json` | [`schema/sweep.v1.json`](../schema/sweep.v1.json) | 1.5.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/sweep.v1.json` |
| `wt sweep --dry-run --json` | [`schema/sweep-plan.v1.json`](../schema/sweep-plan.v1.json) | 1.7.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/sweep-plan.v1.json` |
| `wt sync --json` | [`schema/sync.v1.json`](../schema/sync.v1.json) | 1.16.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/sync.v1.json` |
| `wt sync rebase\|resume\|undo --json` | [`schema/sync-run.v1.json`](../schema/sync-run.v1.json) | 1.26.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/sync-run.v1.json` |
| `wt up --json` | [`schema/up.v1.json`](../schema/up.v1.json) | 1.7.0 | `https://raw.githubusercontent.com/anlindstrom/wt/main/schema/up.v1.json` |
<!-- json-docs:end -->

They are built into the binary: `wt schema` lists them and `wt schema up` prints
one, so the schema you read is the one for the wt you run. Validate against that
one. Its enumerations are exact for that wt, and a newer wt may add values and
fields within the same schema number — so an older copy of a schema can reject
newer output, and a reader that does not validate treats an unknown value as
unknown. wt's own tests validate every `--json` output against these schemas,
and more strictly than a reader should: an object whose schema lists its
properties may carry no key it does not list, so a field cannot reach the
output without reaching its schema. The schemas themselves stay open
(no `additionalProperties: false`), so a reader can validate older output.

## `wt config --json` — resolved repository configuration

Prints one object with the same resolved repository values as `wt config` and
`wt config --shell`. It writes nothing. `--json` with `--shell` is a usage error
(exit 2). The user's settings block is not included; branch suffix and type names
include user overrides exactly as the other outputs do.

<!-- json-docs:begin config -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"config"` | yes |  |
| `repo` | string | yes | The repository name. |
| `mainRoot` | string, matching `^/` | yes | The absolute path of the main checkout, including when called from a linked worktree. |
| `configured` | boolean | yes | A repository configuration file exists and parses; false for detected defaults. |
| `trunk` | string \| null | yes | The resolved trunk branch; null when absent. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | How trunk was found, with the same meanings as status.v1.json. |
| `branchPrefix` | string \| null | yes | The resolved naming prefix of branch names; null when none. |
| `typeSuffix` | string \| null | yes | The suffix used in worktree directory names. It names worktree folders. |
| `branchSuffix` | string \| null | yes | The resolved branch suffix, including user overrides; null when empty. |
| `defaultType` | string \| null | yes | The type a bare work name takes; null when there is none. |
| `types` | array of string | yes | The available worktree types, in configuration order. |
| `typeNames` | object of string | yes | Resolved type-to-name overrides; an empty object when none are set. |
| `developerConfigDirs` | array of string | yes | Repository-relative directory entries exactly as configured; empty is []. These entries stay relative. |
| `developerConfigFiles` | array of string | yes | Repository-relative file entries exactly as configured; empty is []. These entries stay relative. |
| `cacheDirs` | array of string | yes | Repository-relative cache entries exactly as configured; empty is []. These entries stay relative. |
| `requiredBins` | array of string | yes | The executables the repository requires. |
| `buildInitEnabled` | boolean | yes | Whether to run the build initialization. |
| `buildInitCommand` | string \| null | yes | The build initialization command; null when there is none. |
| `provisionScript` | boolean | yes | The current checkout has an executable bin/worktree/provision.sh file. |
<!-- json-docs:end -->

Empty lists are `[]`; an absent string is `null`. Within schema 1, the consumer
fields `schema`, `command`, `mainRoot`, `developerConfigFiles` and
`developerConfigDirs` keep their names and meanings.

## `wt status [<work>] --json` — the plan

What `wt up` would start on, from local state alone. It **writes nothing**: no
fetch, no rebase simulation, no pull-request cache; every git it runs reads, and
`git status` runs with `--no-optional-locks`. It lists
[agent sessions](#sessions--claude-and-codex) with `claude agents`, `ps` and
Codex's session logs. `<work>` defaults to `.`, the worktree you are in.

A worktree `wt up` would not start on is still a plan, with `upEligible: false`
and the reason — never an error exit. The command fails only outside a git
repository.

<!-- json-docs:begin status -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"status"` | yes |  |
| `token` | string \| null | yes | Names the inputs of this plan (trunk name, wt configuration, stack, and since 1.6.0 the remote commit of each stack member that has diverged from its own remote); pass it to `wt up --expect`. Null when not eligible, except that since 1.6.0 a plan held back by ownRemoteDiverged alone has one, for `wt up --allow-diverged --expect`. The one exception, since 1.6.0, is a plan whose upIneligibleCode is ownRemoteDiverged: it has a token, for wt up --allow-diverged --expect. |
| `trunk` | string \| null | yes | Trunk as wt up resolves it here. See trunkSource for how it was found. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | How trunk was determined, first match wins, from local refs alone (since 1.4.0): config — MAIN_BRANCH in the wt configuration file; originHead — refs/remotes/origin/HEAD; conventional — the first of development, main, master that exists locally or on origin (since 1.4.1; before, main, master, trunk, development, develop, ranked by where each exists); currentBranchGuess — no longer produced since 1.4.1: when nothing names trunk, detection fails and the repository reports noConfiguration. Null when trunk is. |
| `trunkRef` | string \| null | yes | origin/<trunk>, what wt up rebases onto after its fetch; null when trunk is null, that is when nothing names trunk. |
| `trunkRefExists` | boolean | yes | Whether that ref is here, as last fetched. |
| `trunkTip` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit of that ref; null when it is not here. |
| `trunkRefUpdatedAt` | string \| null (date-time) | yes | When the trunk ref last moved, from its own reflog (RFC 3339, UTC); null without one. It is not the time of FETCH_HEAD, which any fetch sets. |
| `trunkSync` | object | yes | What wt up would do to local trunk after its fetch, judged against trunkTip; fastForwarded is always false here. A skippedReason of null means a run would fast-forward local trunk, or that there is nothing to do. |
| `trunkSync.local` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | Local trunk after the run (as found unless fast-forwarded); null when there is none. |
| `trunkSync.remote` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The origin/<trunk> commit compared with; null when there is none. |
| `trunkSync.localAhead` | integer \| null, at least 0 | yes | Commits local trunk has that origin lacks, as found; null when either side is missing. |
| `trunkSync.remoteAhead` | integer \| null, at least 0 | yes | Commits origin has that local trunk lacks, as found; null when either side is missing. |
| `trunkSync.fastForwarded` | boolean | yes |  |
| `trunkSync.skippedReason` | `"optedOut"` \| `"ahead"` \| `"diverged"` \| `"dirty"` \| `"operation"` \| `"session"` \| `"notFastForward"` \| `"failed"` \| `null` | yes | Why local trunk was left; null when fast-forwarded or with nothing to do (the two are equal, or either side is missing). |
| `configured` | boolean | yes | A wt configuration file that parses. Since 1.3.0, false also for a repository with no file, which runs on detected defaults (what `wt init --yes` would write) with no problem named; before, that repository had no configuration at all. |
| `worktree` | object \| null | yes | The worktree named; null when it cannot be found. |
| `worktree.work` | string | yes | The work name, as wt list prints it. |
| `worktree.branch` | string | yes | Empty when detached. |
| `worktree.path` | string | yes | The worktree's absolute path. |
| `worktree.isMain` | boolean | yes | Whether it is the main checkout. |
| `worktree.state` | `"clean"` \| `"dirty"` \| `"unreadable"` | yes | The checkout: clean is nothing uncommitted. |
| `worktree.behind` | integer \| null, at least 0 | yes | Commits it lacks against trunkRef (the local trunk when that is not here); null when they cannot be counted. |
| `worktree.ahead` | integer \| null, at least 0 | yes | Commits it has that trunkRef lacks (against the local trunk when trunkRef is not here); null when they cannot be counted. |
| `worktree.ownRemote` | object | yes | Its state is none for the main checkout and for a detached HEAD. |
| `worktree.ownRemote.ref` | string \| null | yes | The remote-tracking ref, as remote/branch (origin/feat/x); null only when no ref could be named: for none, and for unknown unless it is a ref that could not be compared. |
| `worktree.ownRemote.state` | `"none"` \| `"gone"` \| `"unknown"` \| `"inSync"` \| `"ahead"` \| `"behind"` \| `"rebased"` \| `"diverged"` | yes | none: nothing to check: the ref is trunk's, or does not exist. gone: the branch names a ref the remote no longer has. unknown: where a push lands cannot be read from a tracking ref (a mirror remote, several push URLs, a push URL that is another repository than the fetch URL), or the branches or the remotes cannot be read at all, or the two tips cannot be compared; a run goes on unchecked. Since 1.8.0 also a branch a run would rebase and not push: noPushReason is then non-null. inSync: the same commit. ahead: only the branch has commits of its own. behind: only the remote has; a run fast-forwards the branch first. rebased: both have, and the remote's are old versions of the branch's (its tip is a tip the branch once had, or every commit only it has is matched patch for patch); a run goes on. diverged: both have, otherwise; a run refuses unless --allow-diverged. |
| `worktree.ownRemote.commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The remote commit compared with; null for none, gone and unknown. |
| `worktree.ownRemote.ahead` | integer \| null, at least 0 | yes | Commits only the branch has; null for none, gone and unknown. |
| `worktree.ownRemote.behind` | integer \| null, at least 0 | yes | Commits only the remote has; null for none, gone and unknown. |
| `worktree.ownRemote.fetched` | boolean | yes | Always false here: wt status does not fetch, so the remote side is as last fetched. |
| `worktree.ownRemote.blocks` | `"diverged"` \| `"operation"` \| `"dirty"` \| `"session"` \| `null` | yes | Why a run would refuse for it; non-null only when a run would. diverged with state diverged, for a branch a run would rebase: --allow-diverged goes on. operation, dirty, session with state behind: it cannot be fast-forwarded, because a git operation is in progress there, the checkout has tracked changes, or an agent session is busy in it. |
| `worktree.ownRemote.noPushReason` | string \| null | yes | Since 1.8.0: why a run would rebase the branch and not push it, in words; null when a run would push it, and for state none. It tracks a remote branch of another name and nothing is recorded for it (state unknown, fixCommand settles it); what is recorded for it cannot be used (state unknown); or it is recorded as pushing to a branch of another name that has commits it never had, is at a commit it was never at, or cannot be compared with it (state diverged, behind, rebased or unknown). See docs/json.md. |
| `worktree.ownRemote.fixCommand` | array of string \| null, at least 1 items | yes | Since 1.8.0: the command that settles noPushReason, as an argv, when one does: wt sync push-to <branch> <remote>/<branch>, which records the branch it tracks as where it pushes. The same command with the branch's own name on the remote records that instead. It names no directory: run it in the repository. Null otherwise. |
| `upEligible` | boolean | yes | wt up would start on it. Dirt, conflicts and busy sessions are not checked here, except where they stop a fast-forward (ownRemoteBehind). alreadyOnTrunk is the exception only the plan reports: wt up itself would fast-forward such a branch to trunk, which is why the plan holds it back. wt up checks dirt, conflicts and busy sessions, and its result says so. |
| `upIneligibleCode` | `"mainCheckout"` \| `"noConfiguration"` \| `"configurationInvalid"` \| `"detachedHead"` \| `"onTrunk"` \| `"handedOver"` \| `"notAWorktree"` \| `"ownRemoteDiverged"` \| `"ownRemoteBehind"` \| `"alreadyOnTrunk"` \| `null` | yes | Since 1.9.0 alreadyOnTrunk: the worktree's branch has no commit its trunk lacks (worktree.ahead is 0 and it is the only branch of its stack), so there is nothing to rebase; upIneligibleReason names the trunk ref. Since 1.6.0 ownRemoteBehind: a member of the stack is behind its own remote and cannot be fast-forwarded (its ownRemote.blocks says why). ownRemoteDiverged: no member is, and one has diverged from its own remote; wt up --allow-diverged goes on. handedOver: an earlier wt sync run waits on a person there. alreadyOnTrunk: a remote branch that is gone changes nothing. |
| `upIneligibleReason` | string \| null | yes | The code as a sentence. noConfiguration is, since 1.3.0, a repository with no file and no trunk to detect either; since 1.4.1 also a file with no MAIN_BRANCH when no trunk can be detected. For the two ownRemote codes it stands on its own: the branch, its remote ref, the counts, and the stack member when it is not the worktree asked about. |
| `stack` | array of object | yes | Every worktree wt up would move, parents first. From local refs as of now; just the one when the worktree has no stack. |
| `stack[].work` | string | yes | The work name of the stack member, as wt list prints it. |
| `stack[].branch` | string | yes | The worktree's branch. |
| `stack[].path` | string | yes | The worktree's absolute path. |
| `stack[].ownRemote` | object | yes | Since 1.6.0: a branch against its own remote, the remote-tracking ref a push of it would replace: what is recorded for the branch (branch.<name>.wtPushTo), else the branch's own name on the remote git pushes it to (refs/remotes/origin/<branch> unless <branch>@{push} has that name on another remote, or branch.<name>.pushRemote or remote.pushDefault names one). Since 1.8.0 never a branch of another name that is not recorded, whatever <branch>@{push} is. Never the upstream as such: a branch cut from trunk often tracks trunk and pushes to its own name. See docs/json.md. Fields as at `worktree.ownRemote`. |
| `stack[].layers` | array of object | yes | Since 1.12.0: the branches in no worktree whose tips are commits of this member's line, above its stack parent's tip or above trunkRef without one, bottom first: what wt up moves with the member's rebase, or leaves where they are (left says why). The token covers their names. Empty when there are none. |
| `stack[].layers[].branch` | string | yes |  |
| `stack[].layers[].tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `stack[].layers[].left` | string \| null | yes | Why wt up would leave it where it is: it is trunk, a backup name (also by a prefix at a slash), a tip that is a fixup!, squash! or amend! commit, or a merge commit on the line. Null when it would move it; a rebase that drops, folds or changes commits leaves it too, which only the run finds out. |
| `stack[].deferredDeclared` | array of object | yes | Since 1.7.0. The deferred steps .wt-sync.yaml declares on trunkTip, trunk as last fetched, in the declared order: what wt up goes through once it has rebased this member. Declared, not predicted: each may run. A step with no paths does, after every finished rebase; one with paths only when the rebase changed one of them; none after a step that failed. wt up fetches first and reads the declaration from the trunk it then rebases onto, which may be newer, and the token does not cover it. The same list on every member. Empty when trunk declares none or has no .wt-sync.yaml, when trunkRefExists is false, and when the declaration there cannot be read. |
| `stack[].deferredDeclared[].step` | string | yes | Its run line: the string deferred[].step carries for it in wt sync rebase --json. |
| `stack[].deferredDeclared[].paths` | array of string | yes | The globs that gate it, as declared. Empty: it runs after every finished rebase. Otherwise it runs only when the rebase changed a path one of them matches, which is not known before the rebase. |
| `stack[].deferredDeclared[].needs` | array of string, matching `^(docker\|env:[A-Za-z_][A-Za-z0-9_]*)$` | yes | Since 1.10.0 (env:<NAME> since 1.11.0). What the step cannot run without, as declared: wt up probes each before it moves anything. |
| `sessions` | array of object | yes | The agent sessions in the stack's worktrees. |
| `sessions[].work` | string | yes | The work name of the worktree it is in. |
| `sessions[].name` | string | yes | The session's name. |
| `sessions[].kind` | `"claude"` \| `"codex"` | yes | Since 1.5.0 also codex: a Codex session. name is then codex, codex exec, or codex (<originator>) for a thread an app-server runs. |
| `sessions[].state` | `"busy"` \| `"idle"` | yes | busy or idle; a busy session refuses wt up unless --force. |
| `sessionsError` | string \| null | yes | Listing the sessions failed; sessions is then empty, not known empty. |
<!-- json-docs:end -->

The **token** covers the trunk name, the bytes of the wt configuration file
`wt up` would read, the stack's branches in order, and, since 1.12.0, the names
of the `stack[].layers` of each member. A stack with none keeps the token it had. A newer trunk commit is
not in it. Nor is the commit of a branch's own remote, with one exception since
1.6.0: for each stack member whose `ownRemote.state` is `diverged` it covers
that remote commit. A plan with nothing diverged has the token an older wt
gives it.

One refusal refuses the whole stack, so the two `ownRemote` codes are about any
member of `stack`, the worktree asked about included; `stack[].ownRemote.blocks`
says which. `ownRemoteBehind`: a member is behind its own remote and cannot be
fast-forwarded (`blocks` is `operation`, `dirty` or `session`); `token` is null.
`ownRemoteDiverged`: no member is in that state and one has diverged (`blocks`
is `diverged`); `token` is set, and `wt up --allow-diverged --expect <token>`
goes ahead over exactly the divergence shown. A stack with both kinds is
`ownRemoteBehind`: the flag would not clear it.

## `wt up [<work>] --json` — the run

The same run as without `--json`. Progress, and any question, go to **stderr**;
stdout carries exactly one object. To run it unattended, pass `--yes` (yes to
every question, the push included) and `--no-push` to keep the push out.

With `--expect <token>`, `wt up` fetches, then recomputes the token and refuses,
touching no worktree, when it differs: the trunk's name, the configuration, the
stack, or the remote commit of a member that has diverged from its own remote is
not what the plan showed. It is then an `error` with no participants.

Before it rebases anything, the run checks each participant against
[its own remote](#ownremote--a-branch-against-its-own-remote): a branch behind
it is fast-forwarded first, one that has diverged from it refuses the run
unless `--allow-diverged` is given.

A handled SIGINT or SIGTERM writes the object too, through the same path — the
participants so far, the one in flight as `interrupted`, and the way back — then
exits 130 for SIGINT or 143 for SIGTERM. SIGKILL or a crash may write none;
treat a missing object as unknown.

<!-- json-docs:begin up -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"up"` | yes |  |
| `trunk` | string \| null | yes | Trunk as wt up resolved it; null when the run stopped before resolving it. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | How trunk was determined, first match wins, from local refs alone (since 1.3.0): config — MAIN_BRANCH in the wt configuration file; originHead — refs/remotes/origin/HEAD; conventional — the first of development, main, master that exists locally or on origin (since 1.3.1; before, main, master, trunk, development, develop, ranked by where each exists); currentBranchGuess — no longer produced since 1.3.1: when nothing names trunk, detection fails and the repository reports noConfiguration. Null when trunk is. Null with trunk. |
| `trunkRef` | string \| null | yes | origin/<trunk>, what the run rebases onto after its fetch; null when the run stopped before resolving it. |
| `onto` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit the run rebased onto: trunk's after its fetch, or since 1.6.0 the target --onto names. Null when it was never determined. |
| `target` | object \| null | yes | What onto is: trunk, or the ref --onto names (since 1.6.0). Null when it was never determined. |
| `target.ref` | string | yes | The ref as given to --onto, or origin/<trunk>. Examples: integ, origin/release. |
| `target.trunk` | boolean | yes | The target is trunk: no --onto was given. |
| `target.upstream` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit --upstream names: only the commits above it are replayed. Null without it. The run is then git rebase --onto <ref> <upstream>. |
| `fetched` | boolean | yes | Whether trunk was fetched: false with --no-fetch, or when the run stopped first. |
| `trunkSync` | object \| null | yes | What the run did to local trunk after its fetch: fast-forwarded to origin/<trunk> when safe, or why not. Null when it did not fetch. See trunkSync in wt status --json for the fields. |
| `trunkSync.local` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | Local trunk's commit after the run (as found, unless fast-forwarded); null when there is no local trunk. |
| `trunkSync.remote` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The origin/<trunk> commit compared with; null when there is none. |
| `trunkSync.localAhead` | integer \| null, at least 0 | yes | Commits local trunk has that origin lacks, as found; null when either side is missing. |
| `trunkSync.remoteAhead` | integer \| null, at least 0 | yes | Commits origin has that local trunk lacks, as found; null when either side is missing. |
| `trunkSync.fastForwarded` | boolean | yes |  |
| `trunkSync.skippedReason` | `"optedOut"` \| `"ahead"` \| `"diverged"` \| `"dirty"` \| `"operation"` \| `"session"` \| `"notFastForward"` \| `"failed"` \| `null` | yes | Why local trunk was left; null when fast-forwarded or with nothing to do (the two are equal, or either side is missing). |
| `outcome` | `"done"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | done: every participant rebased, fastForwarded or skipped. refused: nothing changed, or error stopped the run first. partial: something changed and not everything finished. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why the run stopped before touching any participant: configuration, fetch, --expect, the main checkout named, or a worktree not found. |
| `worktrees` | array of object | yes | The participants, parents first; empty when error stopped the run first. |
| `worktrees[].work` | string | yes | The work name, as wt list prints it. |
| `worktrees[].branch` | string | yes | The worktree's branch. |
| `worktrees[].path` | string | yes | The worktree's absolute path. |
| `worktrees[].result` | `"rebased"` \| `"fastForwarded"` \| `"rebasedStepFailed"` \| `"skipped"` \| `"refused"` \| `"notRun"` \| `"restored"` \| `"needsRecovery"` \| `"handedOver"` \| `"interrupted"` | yes | rebased, fastForwarded, rebasedStepFailed, needsRecovery, handedOver and interrupted changed the branch or worktree; skipped, refused, notRun and restored left them as they were. fastForwarded (since 1.4.0): the branch was behind its own remote and has nothing to rebase at the remote's commit, so the run moved it there and that is all; after is that commit, pushCommand is null. |
| `worktrees[].reason` | string \| null | yes | Why, for every result but rebased. |
| `worktrees[].before` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The branch's commit when the run began. |
| `worktrees[].after` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The branch's commit when the run ended. |
| `worktrees[].failedSteps` | array of string | yes | For rebasedStepFailed: each step that did not finish. |
| `worktrees[].recovery` | string \| null | yes | For needsRecovery, handedOver and interrupted: how to put it back or finish it. |
| `worktrees[].pushCommand` | array of string \| null, at least 1 items | yes | For rebased: the push, as an argv. Since 1.5.0 null also for rebased, when wt has no push to make or offer for the branch, which noPushReason explains. The argv has the form ["git", "-C", path, "push", …]. |
| `worktrees[].pushed` | boolean | yes | Whether the run pushed it: with --push, or with --yes without --no-push. |
| `worktrees[].noPushReason` | string \| null | yes | Since 1.5.0: for rebased with a null pushCommand, why wt made and offered no push for the branch, in words, whatever the reason: among them that it tracks a remote branch of another name with nothing recorded for it, and that the branch of another name it is recorded as pushing to holds commits that cannot be shown to be old versions of its own. Null otherwise. |
| `worktrees[].fixCommand` | array of string \| null, at least 1 items | yes | Since 1.5.0: the command that settles noPushReason, as an argv, when one does: wt sync push-to <branch> <remote>/<branch>, which records the branch it tracks as where it pushes. The same command with the branch's own name on the remote records that instead. It names no directory: run it in the repository. Null otherwise. |
| `worktrees[].layers` | array of object | yes | Since up 1.7.0: the branches in no worktree on this participant's line, bottom first, that the run moved with its rebase or left, and why. Empty when there are none, and for a participant the run did not rebase. |
| `worktrees[].layers[].branch` | string | yes |  |
| `worktrees[].layers[].before` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip when the run read the line. |
| `worktrees[].layers[].after` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | Where the run moved it: the replay of its commit. Null for a branch left. |
| `worktrees[].layers[].result` | `"moved"` \| `"left"` | yes | moved: the branch is at after, pinned with the run's safety and result refs, so wt sync undo puts it back with the worktrees. left: it stays at before; reason says why. |
| `worktrees[].layers[].reason` | string \| null | yes | Why it was left: it is trunk, a backup name (also by a prefix at a slash), a tip that is a fixup!, squash! or amend! commit, a merge commit on the line, a rebase that dropped, folded or changed commits, one that stopped or failed, a branch checked out meanwhile. Null for a branch moved. |
| `worktrees[].ownRemoteSync` | object | yes | Since 1.4.0: what the run found of a participant's own remote, the remote-tracking ref a push of its branch would replace, and what it did about it. See docs/json.md. |
| `worktrees[].ownRemoteSync.ref` | string \| null | yes | The remote-tracking ref, as remote/branch; null only when no ref could be named. |
| `worktrees[].ownRemoteSync.state` | `"none"` \| `"gone"` \| `"unknown"` \| `"inSync"` \| `"ahead"` \| `"behind"` \| `"rebased"` \| `"diverged"` | yes | As found, before anything moved; the states are those of ownRemote in wt status --json. |
| `worktrees[].ownRemoteSync.local` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The branch as found, before any fast-forward: where wt sync undo puts it back. |
| `worktrees[].ownRemoteSync.remote` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The remote commit compared with; null for none, gone and unknown. |
| `worktrees[].ownRemoteSync.localAhead` | integer \| null, at least 0 | yes | Commits only the branch had; null for none, gone and unknown. |
| `worktrees[].ownRemoteSync.remoteAhead` | integer \| null, at least 0 | yes | Commits only the remote had; null for none, gone and unknown. |
| `worktrees[].ownRemoteSync.fastForwarded` | boolean | yes | The run moved the branch from local to remote before rebasing it. It stays true as a record of what was done when the rebase after it was put back, which takes the branch back to local. |
| `worktrees[].ownRemoteSync.allowed` | boolean | yes | state is diverged and the run went on over it under --allow-diverged; false when the participant was refused for something else. |
| `worktrees[].ownRemoteSync.skippedReason` | `"diverged"` \| `"operation"` \| `"dirty"` \| `"session"` \| `"failed"` \| `"fetchFailed"` \| `null` | yes | Why the own remote refuses the participant; null when it does not. diverged: --allow-diverged goes on. operation, dirty, session: behind, and it cannot be fast-forwarded. failed: the fast-forward itself did not go through; nothing of this participant was touched. fetchFailed: the branch's own remote could not be fetched, so the run could not look; trunk's fetch failing is the run's error instead. |
| `recovery` | string \| null | yes | For interrupted: the way back wt printed. |
<!-- json-docs:end -->

`result`, exhaustively:

| Value | The branch | Meaning |
|---|---|---|
| `rebased` | moved | rebased onto trunk, every step after it done |
| `fastForwarded` | moved | since 1.4.0: behind [its own remote](#ownremote--a-branch-against-its-own-remote), with nothing to rebase at the remote's commit: moved there, and that is all. `after` is that commit, `pushCommand` is null and nothing is pushed, no deferred step runs |
| `rebasedStepFailed` | moved | rebased, but a later step did not finish: a deferred step, the result ref, the plan's cleanup (`failedSteps`) |
| `skipped` | unchanged | nothing to do: on trunk already, or nothing of its own |
| `refused` | unchanged | refused before anything was touched, for a reason of its own: uncommitted changes, a busy agent session, a conflict that would be yours |
| `notRun` | unchanged | not touched because of another member of its stack |
| `restored` | unchanged | the rebase failed or stopped and was put back: branch, index, HEAD and working tree as the run found them |
| `needsRecovery` | changed | the rebase failed and was not put back; `recovery` says how |
| `handedOver` | changed | stopped at a conflict that is a person's, with a plan file (`wt sync resume`, `wt sync undo`) |
| `interrupted` | changed | the one a signal caught; `recovery` says how |

`outcome`, from the participants:

| Value | When |
|---|---|
| `done` | every participant `rebased`, `fastForwarded` or `skipped` |
| `refused` | nothing changed: every participant `skipped`, `refused`, `notRun` or `restored`, or `error` stopped the run before any |
| `partial` | something changed and not everything finished |
| `interrupted` | a signal ended the run |

"Nothing changed" means each participant's branch, index, HEAD and working tree
are as they were; the fetched trunk ref and the objects a simulation writes are
expected writes.

The exit code keeps its meaning (0 when the run completed); read `outcome`.

### `layers` — branches in no worktree

A stack of worktrees can have branches between its members that no worktree
holds: `feat/a` and `feat/d` in worktrees, `feat/b` and `feat/c` not. `wt up`
and `wt sync rebase` rebase the worktrees, with `--no-update-refs`, so those
branches would stay on the old commits. They are moved with the rebase: a
participant's layers are the local branches in no worktree whose tips are
commits of the line above its stack parent's tip, or above trunk (the target)
when it has no parent. Once the participant's rebase has finished, each goes to
the replay of its commit, and is pinned like the worktrees, with a safety and a
result ref under the run's epoch, so `wt sync undo` puts it back with them.

A layer that cannot be moved is left where it is and reported, with `reason`:
the trunk branch itself, a name matching the backup patterns of `wt refs sweep` (or whose prefix at a slash does: `backup/*` covers `backup/2026-11/feat`), a tip that is a
`fixup!`, `squash!` or `amend!` commit, a merge commit on the line, a rebase
that dropped, folded or changed commits (the layers are matched to the replay
commit by commit; `wt sync rebase --stack` places them through that), a rebase
that stopped for a person or failed, and a branch that was checked out
meanwhile. A run with `--stack` or `wt sync steps` reads its own layers and
reports them under `stack`; `layers` is then empty. A branch whose commit the
rebase did not change is in neither list.

### `trunkSource` — how trunk was found

`wt status`, `wt up`, `wt sync`, `wt sweep --dry-run`, and the `wt new` and
`wt checkout` plans say how trunk was determined. The first that applies wins,
and only local refs are read — nothing asks the remote:

| Value | Trunk is |
|---|---|
| `config` | `MAIN_BRANCH` in the wt configuration file |
| `originHead` | the branch `refs/remotes/origin/HEAD` names |
| `conventional` | the first of `development`, `main`, `master` that exists locally or as `origin/<name>` — the order decides, not where it exists |
| `currentBranchGuess` | no longer produced since `status` 1.4.1, `up` 1.3.1, `sync` 1.1.1, `sweep-plan` 1.4.1, `new-plan` 1.2.1, `checkout-plan` 1.3.1, and never by `remove-plan` or `remove`; detection fails instead. Before, the main checkout's branch, which may be a feature branch |

When none of those names trunk, wt does not guess: commands refuse with
"cannot tell which branch is trunk", and the plans report `noConfiguration`
with `trunk` and `trunkSource` null. Null only with `trunk` null. A reader
treats `currentBranchGuess`, and a value it does not know, as no trunk to
rebase onto or judge branches merged against.

### `trunkSync` — local trunk

After fetching trunk, `wt up`, `wt sync rebase` bring local
`<trunk>` up to `origin/<trunk>` when that is a pure fast-forward and safe:
checked out nowhere, the ref moves by compare-and-swap; checked out in a
checkout that is clean (untracked files count), with no operation in progress
and no agent session busy in it (an idle one does not count, nor the session
running wt), `git merge --ff-only --no-overwrite-ignore` moves
it there. Anything else leaves it, with the reason. Local commits are never
thrown away, and a fast-forward that fails never fails the run.
`--no-ff-trunk`, or `wt config set ff_trunk false`, turns it off.

`skippedReason`, exhaustively. `ahead` and `diverged` are reported whenever they
hold, even with `--no-ff-trunk` or `ff_trunk` false: they are facts about the
repository a fast-forward could not get past anyway, and `ahead` is how a caller
learns local trunk has commits origin lacks. `optedOut` is reported only when a
fast-forward was otherwise possible.

| Value | Meaning |
|---|---|
| `optedOut` | `--no-ff-trunk`, or `ff_trunk` false in the user config, and trunk could otherwise have been fast-forwarded |
| `ahead` | local has commits origin lacks, and origin none local lacks |
| `diverged` | both have commits the other lacks |
| `dirty` | the checkout it is on has changes, untracked files included |
| `operation` | a rebase, merge, cherry-pick, revert or bisect is in progress on it |
| `session` | an agent session is busy in the checkout it is on (idle ones, and the session running wt, do not count), or the sessions could not be listed |
| `notFastForward` | it moved, or was checked out or switched away from, while wt was updating it |
| `failed` | git refused the update (an ignored file it would overwrite), or it is not safe to try: trunk checked out in more than one worktree |

### `target` — trunk, or `--onto`

What `onto` is, on `wt sync --json` (since `sync` 1.8.0), `wt sync rebase|resume
--json` (`sync-run` 1.8.0) and `wt up --json` (`up` 1.6.0). Without `--onto` it
is trunk, and `onto` is trunk's commit as it always was; the declaration and its
scripts are read from trunk either way.

The overview's `token` covers the target's ref and commit and the upstream,
only when `--onto` is given: without it the token is byte for byte what it was.
So `wt sync rebase <work> --onto <ref> --expect <token>` refuses a target that
moved since `wt sync <work> --onto <ref> --json` read it. `wt up --expect` takes
the token of `wt status --json`, which knows no target, so `wt up --onto
--expect` is refused.

### `ownRemote` — a branch against its own remote

`wt up` and `wt sync rebase` rewrite a branch, and what is pushed afterwards
replaces a ref on a remote. Before rebasing, a run looks at that ref for every
branch it would rebase, the named worktree's and each member of its stack, so
that a branch is never rebased short of commits its remote has, and never
pushed over commits it never had. `wt status --json` and `wt sync --json` report
the same reading as `ownRemote`; a run reports what it found and did as
`ownRemoteSync`.

**Which ref.** The remote-tracking ref wt's own push of the branch replaces.
The check and the push go by the same rule, in this order:

1. What is recorded for the branch: `branch.<name>.wtPushTo`, `<remote>
   <branch>`, written by `wt sync push-to` (`--branch` for a branch with no worktree), by the question a run asks, and by
   wt when it renames a branch that pushed to its own name.
2. Nothing, when the branch tracks a remote branch of another name that is
   not someone else's (trunk, or the remote branch of another local branch,
   by that branch's name or by what is recorded for it). That remote branch
   may be the branch's own, as after a rename, or the one it was cut from,
   and wt's push is forced, so wt does not guess:
   [nowhere to push](#where-a-branch-pushes).
3. Otherwise the branch's own name, on the remote git pushes it to: where
   `<branch>@{push}` is when that has the branch's name, else
   `branch.<name>.pushRemote` or `remote.pushDefault`, else origin.

Another program may read the record itself: `branch.<name>.wtPushTo` in the
repository's own git configuration, with the value `<remote> <branch>`, one
space between the two. The key and that format stay. wt uses a record only
when the remote exists, the branch is not trunk, and the name is one
`git check-ref-format --branch` gives back unchanged and is not `@`, does not
start with `+` or `refs/`; a reader refuses what wt refuses. No JSON field
carries it: `ownRemote.ref` is the ref the branch was compared with, and is
null for a recorded branch the remote does not have yet.

git's configuration chooses the remote and never another name. The upstream
under `push.default` `upstream`, and a `remote.<name>.push` refspec that
renames, are where git's own `git push` goes; wt's push does not go there
until it is recorded for the branch.

It is **not** `@{upstream}`. A branch cut from trunk often tracks
`origin/<trunk>` and is pushed under its own name: its upstream is trunk, its
own remote is `origin/<branch>`, and until that exists there is nothing to
check. Trunk is never the answer, on origin or under its name on another
remote: a branch that tracks it pushes under its own name, and the push names
both sides of its refspec, so git cannot send it to the upstream instead.

`state`, exhaustively, and what a run does:

| Value | Means | A run |
|---|---|---|
| `none` | nothing to check: the ref is trunk's, or does not exist | goes on |
| `gone` | the branch names the ref as its upstream and it is not there, or a fetch asked the remote for it and the remote no longer has it | goes on |
| `unknown` | where a push lands cannot be read from a tracking ref: a mirror remote, a remote with several push URLs, or a push URL that is another repository than the fetch URL (the same host and path over https and ssh is the same repository). Also when the branches or the remotes cannot be read at all, or the two tips cannot be compared. Since `status` 1.8.0, `sync` 1.5.0 also a branch with [nowhere to push](#where-a-branch-pushes): `noPushReason` is then non-null | goes on unchecked, and says so; with `noPushReason` non-null the branch is rebased and not pushed |
| `inSync` | the same commit | goes on |
| `ahead` | only the branch has commits of its own | goes on |
| `behind` | only the remote has | fast-forwards the branch to `commit`, then rebases. Where that is not safe it touches nothing and fails (`blocks`). When there is nothing to rebase at `commit`, which is on trunk already or has nothing of its own, the fast-forward is all the run does (`result` `fastForwarded`) |
| `rebased` | both have, and the remote's are old versions of the branch's | goes on |
| `diverged` | both have, otherwise | touches nothing and fails; `--allow-diverged` rebases the branch as it stands |

`rebased` is the state right after a rebase that was not pushed: the branch has
the new commits and the remote the old ones, so both counts are non-zero. It is
decided by either of two rules. The remote's tip is a **former tip** of the
branch: in the branch's reflog, under a safety ref a run or a forced undo
pinned for it (`refs/wt-sync/<branch>/…`), where a run left it
(`refs/wt-sync-result/<branch>/…`), or where a run fast-forwarded it to before
a rebase it then finished (`refs/wt-sync-ff/<branch>/…`). Or every commit only
the remote has is **matched patch for patch** by a commit only the branch has.
The first rule is what clears a rebase that changed a commit, by a strategy or
by hand; a merge on the remote side has no patch, and a commit that changes
nothing has the patch of every other such commit, so either is cleared by the
first rule or not at all. A remote that gained a commit the branch never had
fails both, also on a branch that was rebased: that is `diverged`.

What wt itself moved the branch to is a former tip only while that stands. A
run that fast-forwards a branch to its remote's commit and is then undone
(`wt sync undo`), or whose rebase is put back (`restored`), or that never got
as far as the fast-forward, leaves nothing saying the branch was ever there:
the `refs/wt-sync-ff/` ref goes, and so do the entries the fast-forward wrote
in the branch's reflog, the ones at that commit and no older than the run. A
commit made after that reads `diverged`, and git's own `--force-if-includes`
refuses to push over the remote's commit too. Nothing local is lost by it: the
commit is on the remote. A ref wt pinned for a branch that was since deleted
says nothing about a new branch of the same name, as long as the new one's
reflog still starts at its creation.

The former-tip rule is the one git's own `--force-if-includes` goes by, with
its limit: a remote tip the branch was once at and was then reset away from
*by hand* is a former tip too. And it lasts as long as its evidence. A rebase
whose patches differ rests on the reflog and on wt's refs; git expires reflog
entries no ref reaches after 30 days by default, and `wt sync doctor --prune`
removes the refs of runs that are neither the newest for their branch nor
recent. A branch rebased with `--no-push` by plain git, or by a run whose refs
were pruned, and left unpushed that long can turn `diverged` and then needs
`--allow-diverged`.

`blocks` is one code, by a fixed precedence:

| Value | With `state` | `group` in `wt sync --json` | Meaning |
|---|---|---|---|
| `diverged` | `diverged` | `needsYou`, or `skipped` with a busy session in the worktree | `--allow-diverged` goes on; under the flag nothing else about the own remote blocks. Only for a branch a run would rebase |
| `operation` | `behind` | `needsYou` | it cannot be fast-forwarded: a rebase, merge, cherry-pick, revert or bisect is in progress there |
| `dirty` | `behind` | `needsYou` | it cannot be fast-forwarded: the checkout has tracked changes, the dirt that refuses a rebase too. Untracked files do not block |
| `session` | `behind` | `skipped` | it cannot be fast-forwarded: an agent session is busy in the checkout. `--force` on a run takes it past |

A busy session beats every other reason for the group, as it always did: a
worktree one is in is `skipped` whatever its `blocks`, and `blocks` still says
what the own remote alone would refuse for.

A `behind` branch with `blocks` null will be fast-forwarded. In `wt sync
--json` its `class`, `behind`, `ahead` and `stops` are then those of the remote
commit, since that is what the run rebases: a conflict the remote's commits
bring is seen before anything moves.

**Fetching.** A command that fetches trunk fetches the own remotes with it.
Per remote, origin and each other remote a branch pushes to, that is one
`git ls-remote` for the branches to check and then one `git fetch` with exact
refspecs for the ones the remote has: two round trips. A fetch of trunk alone,
with no branch to check, is the single `git fetch origin <trunk>` it always
was, and nothing here needs a newer git than that does. A branch the remote
lacks is `gone` and is not fetched; no other branch is, whatever its name
starts with.

| Command | Fetches | `fetched` |
|---|---|---|
| `wt status --json`, `wt status`, `wt list` | nothing | always false |
| `wt sync`, `wt sync <work>`, `wt sync --json` | trunk and every worktree's own remote, each call bounded at 45 s; `--no-fetch` nothing | true unless `--no-fetch`, trunk's fetch failed (`fetchError`), that branch's own fetch failed, or the remote's fetch refspec is not the standard `refs/heads/*:refs/remotes/<remote>/*`, which wt does not write refs outside |
| `wt up`, `wt sync rebase` | trunk and the own remote of every branch the run may rebase; `--no-fetch` nothing | with `--no-fetch` the run compares with the last fetch, `fetched` is false, nothing is refused for it, and its lines say so |
| `wt sync keep once` | trunk and every worktree's own remote | as a run |
| `wt sync resume`, `wt sync undo` | nothing: the check ran when the run started | |

Whose fetch failed decides who is refused. Trunk's: the run refuses, `error`
says so, nothing is touched. A branch's own remote, because its fork cannot be
reached or its ref clashes with one here: that branch is refused by itself
(`skippedReason` `fetchFailed`, the reason naming git's own), with its stack as
any refusal, and the other participants go on; with `--if-ready` it is not
ready. In `wt sync --json` such a row is `verdict` `refuse` with the reason,
`fetched` false, `blocks` null, the rest as last fetched.

A fetch never removes a remote-tracking ref: one whose branch is gone from the
remote stays until `git fetch --prune`, and reads as `gone` from a command that
fetched and as whatever it last said from one that did not.

**`--allow-diverged`**, on `wt up` and `wt sync rebase`: rebase a `diverged`
branch as it stands. It does nothing for `behind`. Like `--force` it needs the
worktree named: `wt sync rebase` with nothing named, in one
repository or across many, refuse the flag. `wt up` always names one, the
worktree you are in when no `<work>` is given, so `wt up --allow-diverged` is
accepted. With `--expect <token>` it
goes ahead only over the divergence the token covers; a remote that moved again
since the look is refused as a changed plan. Without `--expect` it is consent to
whatever is found. The flag is not part of any token. With `--if-ready` a
`diverged` worktree is not ready, unless the flag is given; a `behind` one that
can be fast-forwarded is. The flag lets the rebase go; it does not push. wt's
own push ([`--force-with-lease --force-if-includes`, or its own check under
another name](#where-a-branch-pushes)) still refuses to replace
commits the branch never had, and the `pushCommand` a run reports is that push:
replacing them is the caller's own, deliberate force.

### Where a branch pushes

wt's own push, and the `pushCommand` a run reports, go to the ref `ownRemote`
names. The refspec always names both sides in full. git maps a refspec with no
colon to the branch's upstream under `push.default` `upstream`, which would
send a push meant for the branch's own name to whatever it tracks.

Under the branch's own name:

```
git -C <path> push --force-with-lease --force-if-includes [-u] <remote> refs/heads/<branch>:refs/heads/<branch>
```

Under another name, which only a record for the branch gives it:

```
git -C <path> push --force-with-lease=refs/heads/<theirs>:<commit> [-u] <remote> refs/heads/<branch>:refs/heads/<theirs>
```

git's `--force-if-includes` cannot guard the second: it reads the reflog of
the local branch that has the remote branch's name, and with none it refuses
every push. So wt makes that check itself, and pins what it checked.
`<commit>` is the remote commit the branch was compared with, empty for a
branch the remote must not have yet, and a remote that moved since is refused
by git on the pinned commit. The push is made, and the command exists, only
when one of these holds:

- the branch is `inSync` with that commit or `ahead` of it;
- the commit is a tip this branch once had, by the commit itself: in the
  branch's reflog, or under a ref a run pinned (the first rule of `rebased`
  in [`state`](#ownremote--a-branch-against-its-own-remote)).

That is stricter than `state`. A remote whose commits match the branch's only
patch for patch reads `rebased`, and is not pushed over under another name: a
patch is the same after somebody else rewords the commit or changes its
whitespace, and that commit is not this branch's. Under the branch's own name
git's `--force-if-includes` draws the same line. `noPushReason` says so when
it applies.

`-u` is there only for a branch with no upstream; a push never rewrites one.

A branch has **nowhere to push** when it tracks a remote branch of another
name and nothing is recorded for it. Then `ownRemote` is `state` `unknown`
with `ref` null, and after a run the participant is `rebased` with
`pushCommand` null and `pushed` false. Both carry the same two fields:

| Field | Meaning |
|---|---|
| `noPushReason` | the sentence the text output prints: `it tracks origin/own_apikey, a branch of another name, and nothing says whether it pushes there or to origin/feat_wt/own_apikey` |
| `fixCommand` | `["wt", "sync", "push-to", "<branch>", "<remote>/<upstream branch>"]`: records the branch it tracks as where it pushes. The other choice is the same command with the last argument replaced by the branch's own name on the remote, which the sentence ends with. Like wt's other argv hints it names no directory: run it in the repository, in any of its worktrees |

`noPushReason` is set with `fixCommand` null for every other reason wt makes
and offers no push: what is recorded for the branch cannot be used (no such
remote, trunk, not a branch name); and, under another name, the remote branch
has commits the branch never had, is at a commit the branch was never at, or
cannot be compared with the branch at all (a mirror remote, several push
URLs, a push URL that is another repository). On `ownRemote` it is null
exactly when a run would push the branch; a `diverged` branch under its own
name is refused before the rebase, which `blocks` says.

With a person at a terminal `wt up` and `wt sync rebase` ask the question
instead and record the answer. With `--push` or `--yes` a branch left
unpushed is named in the run's `not completed` error, like a push that
failed, and the command exits non-zero. Recording a destination afterwards
does not push the branch: it is rebased already, so the next `wt up` has
nothing to do with it. `wt sync push-to <work>` prints the push to run by
hand.

### `ownRemoteSync` — what a run did about it

On each participant of `wt up --json` and `wt sync rebase --json`. From
`wt sync resume` and `wt sync undo` the key is there and its value is null:
they do not check.

`skippedReason`, exhaustively; the participant is `refused`:

| Value | Meaning |
|---|---|
| `diverged` | as `blocks`: `--allow-diverged` goes on |
| `operation`, `dirty`, `session` | as `blocks`: behind, and it cannot be fast-forwarded. This refuses whatever the branch's class, since what a run would do with it is decided at the remote's commit |
| `failed` | the fast-forward itself did not go through: git would have overwritten an untracked or ignored file (`reason` names the first), or the branch moved since the check. Nothing of this participant was touched |
| `fetchFailed` | the branch's own remote could not be fetched, so the run could not look. The branch was not touched; local `<trunk>` may have been fast-forwarded before the refusal, as before any refusal, and `trunkSync` says so. Trunk's fetch failing is the run's `error` instead |

A participant the own remote refuses has `result: "refused"`; `reason` is the
first thing that refused it, in words, which for a dirty checkout is its dirt.
A `diverged` branch every commit of which the remote has in another form, on
commits the branch never had, was rebased on another machine and pushed while
this checkout kept the old versions: it is `diverged` like any other, and
`reason` (and `upIneligibleReason`) says what it looks like and that
`git reset --hard <ref>` takes the remote's.
Every refusal but one is decided before anything moves, and one refusal
refuses the whole stack: the other members are `notRun`. The exception is a
fast-forward that `failed`: it is found when the run gets to that branch. The
branch itself is untouched and `refused`, what sits on it is `notRun`, and a
parent the run had already rebased stays rebased, so the outcome is `partial`.

The safety ref is pinned at `local`, before the fast-forward, so
`wt sync undo` takes back both. After a fast-forward the participant ends as
one of:

| `result` | The fast-forward | `after` | `wt sync undo` |
|---|---|---|---|
| `fastForwarded` | kept: it is all the run did | `remote` | back to `local` |
| `rebased` | kept, in the rebased branch | the rebased tip | back to `local` |
| `rebasedStepFailed` | kept, in the rebased branch | the tip as the failed step left it | back to `local`, as for any `rebasedStepFailed` |
| `handedOver` | kept: the branch ref is at `remote`, under the rebase in progress | `remote` | aborts the rebase and goes back to `local`. `wt sync resume` finishes it instead, and an undo after that still goes back to `local` |
| `restored` | taken back with the rebase | `local`, equal to `before` | nothing to undo: "already at" |
| `needsRecovery` | as the failure left it; `recovery` says how to put it back, and names the safety ref | where the branch ref is, `remote` unless the failure moved it | once the worktree is out of the rebase, back to `local` |
| `interrupted` | kept if the signal came after it | `remote`, or `before` when the signal came first | once `recovery` is followed, back to `local` |

An undo that takes back a fast-forward also takes back the word that the
branch was ever at `remote` (above): the branch then reads `behind` again, and
with a commit of its own `diverged`.

### `deferredDeclared` — the deferred steps trunk declares

A repository's `.wt-sync.yaml` may declare steps under `defer:` that run once a
rebase has finished: regenerate a file, commit the result. A run reports what
each came to in [`deferred`](#wt-sync-rebaseresumeundo---json--the-result).
`deferredDeclared` is the same steps before the run, on each worktree of
`wt sync --json` (since `sync` 1.4.0) and each `stack` member of
`wt status --json` (since `status` 1.7.0). Its fields are in the field table above.

In the declared order, which is the order they run in.

**Declared, not predicted.** A step with empty `paths` runs after every
finished rebase. A step with `paths` runs only when the rebase changed a path
one of them matches, comparing the branch before and after, and that is not
known until the rebase is done: the result then says `skipped`, "no listed path
changed". No step runs after one that failed, and none runs for a worktree
the run did not rebase: one it skipped, refused, only fast-forwarded, or
handed over (`wt sync resume` runs them when it finishes that rebase). So the
list is what **may** run, never more than that.

The list is the repository's, so it is **the same on every worktree and every
stack member**, also on one a run would skip or refuse and on one whose
assessment failed (`class` `unknown`): `verdict`, `upEligible` and the rest say
whether a run rebases it. It is always an array: `[]` when trunk declares no
deferred step or has no `.wt-sync.yaml`. A plan with an empty `stack` has no
member to carry it.

**Where it is read from.** Always trunk's copy, never the worktree's own: a
branch that edits `.wt-sync.yaml` changes neither the plan nor what a run
executes, until that edit is on trunk.

| | reads `.wt-sync.yaml` from | against the run |
|---|---|---|
| `wt sync --json` | the trunk commit in `onto`, after its fetch | a run fetches again and reads the trunk commit it rebases onto. The overview's `token` covers the declaration, so a run with `--expect` refuses when it has changed |
| `wt status --json` | `trunkTip`, as last fetched; `[]` when `trunkRefExists` is false or the file there cannot be read | `wt up` fetches first, so it may read a newer declaration. The plan's `token` does **not** cover it |
| `wt sync resume` | the trunk commit the handed-over run rebased onto | |

## `wt sweep --dry-run --json` — the sweep's plan

The plan `wt sweep` prints, as one object: every row, why it counts as merged or
why it is kept, and a token. It deletes nothing. It fetches origin first, as a
sweep does, unless `--no-fetch`; it asks GitHub about the branches' pull requests
and lists the [agent sessions](#sessions--claude-and-codex), as a sweep does. `wt sweep --json` without
`--yes` prints the same plan. Run it from the repository's main checkout;
`--all`, `--roots` and `--profile` are refused with `--json` (run it in each
repository).

A plan that cannot be made — not the main checkout, no trunk, the fetch failed —
is still one object, with `error` set and no items, and a non-zero exit.

<!-- json-docs:begin sweep-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"sweep"` | yes |  |
| `repo` | string \| null | yes | The repository's main checkout; null outside one. |
| `trunk` | string \| null | yes | Trunk's branch name; null outside a repository. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | How trunk was determined, first match wins, from local refs alone (since 1.3.0): config — MAIN_BRANCH in the wt configuration file; originHead — refs/remotes/origin/HEAD; conventional — the first of development, main, master that exists locally or on origin (since 1.4.1; before, main, master, trunk, development, develop, ranked by where each exists); currentBranchGuess — no longer produced since 1.4.1: when nothing names trunk, detection fails and the repository reports noConfiguration. Null when trunk is. |
| `bases` | array of object | yes | What merged is measured against, first one first: origin/<trunk> as fetched, then <trunk>. |
| `bases[].name` | string | yes |  |
| `bases[].tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `fetched` | boolean | yes | origin was fetched first (false with --no-fetch, without an origin, or when the plan stopped before). |
| `token` | string \| null | yes | Names this plan; pass it to `wt sweep --expect <token> --yes --json`. Null when there is nothing to remove or delete, or on error. |
| `error` | string \| null | yes | Why no plan could be made: not the main checkout, no trunk, the fetch failed — items is then empty — or a --move-to folder that cannot be used. |
| `items` | array of object | yes | The rows, in the order wt sweep prints them. |
| `items[].category` | `"remove"` \| `"delete"` \| `"inUse"` \| `"upstreamGone"` | yes | remove: the worktree goes, and its branch with it. delete: a branch in no worktree goes. inUse: merged, but kept (kept says why): its worktree is kept, or (deliberateBackup, since 1.7.0) its name is a deliberate backup. upstreamGone: its upstream is gone but trunk lacks its commits, kept. |
| `items[].branch` | string \| null | yes | Null for a detached worktree. |
| `items[].tip` | string, matching `^[0-9a-f]{40,64}$` | yes | The branch's commit, or a detached worktree's HEAD. |
| `items[].work` | string \| null | yes | The worktree's work name; null for a branch in no worktree, or a detached one. |
| `items[].path` | string \| null | yes | The worktree; null for a branch in no worktree. |
| `items[].merged` | object \| null | yes | Why it counts as merged; null for upstreamGone. |
| `items[].merged.how` | `"ancestor"` \| `"patch"` \| `"pullRequest"` | yes | ancestor: into contains the tip. patch: every commit is on into under another id, byte for byte. pullRequest: GitHub merged its pull request into trunk at exactly this tip. The first is a merge or fast-forward; the second a rebase merge or a cherry-pick; the third a squash merge. |
| `items[].merged.into` | string | yes | The base it is on (origin/main, main); trunk's name for pullRequest. |
| `items[].merged.pullRequest` | integer \| null | yes | For pullRequest: its number; null otherwise. |
| `items[].pullRequest` | object \| null | yes | What GitHub says about the branch's pull request, merged or not: its number, state, base and url. Null when there is none, or GitHub was not asked. |
| `items[].pullRequest.number` | integer | yes | The pull request's number. |
| `items[].pullRequest.state` | `"OPEN"` \| `"MERGED"` \| `"CLOSED"` | yes | OPEN, MERGED or CLOSED, as GitHub says. |
| `items[].pullRequest.base` | string \| null | yes | The branch the pull request targets. |
| `items[].pullRequest.url` | string \| null | yes | The pull request's URL. |
| `items[].kept` | array of `"dirty"` \| `"statusUnknown"` \| `"detached"` \| `"session"` \| `"sessionsUnknown"` \| `"lockHeld"` \| `"directoryMissing"` \| `"heldByRebase"` \| `"heldByBisect"` \| `"mainCheckout"` \| `"branchNotDeletable"` \| `"notMerged"` \| `"operation"` \| `"hiddenChanges"` \| `"headUnreachable"` \| `"reachUnknown"` \| `"submoduleUnreachable"` \| `"nestedWorktree"` \| `"processes"` \| `"processesUnknown"` \| `"deliberateBackup"` | yes | Why it is kept, every reason that applies; empty for remove and delete. processes (since 1.6.0): without --reap, processes still run in its worktree (listed in processes). processesUnknown (since 1.6.0): the processes in it could not be found, or told apart from agent sessions. deliberateBackup (since 1.7.0): the branch name matches ref_keep_patterns, so the branch is not deleted and its worktree, if any, is not removed. |
| `items[].reason` | string | yes | The row as wt sweep prints it: why it goes, or why it stays and what to do. |
| `items[].ahead` | integer \| null, at least 0 | yes | For upstreamGone: commits the first base lacks. |
| `items[].subject` | string \| null | yes | The tip's commit subject. |
| `items[].processes` | array of object | yes | Since 1.6.0. The processes working in its worktree, as remove-plan lists them: another user's, any with a controlling terminal, wt's own and agent processes are never listed. reap is true on a remove item under --reap, which stops them before the worktree goes; they keep an inUse item only without --reap, when kept has processes; under --reap, or on a row kept for another reason, they are listed and do not hold it. Empty for a branch in no worktree, and when none was found. |
| `items[].processes[].pid` | integer, at least 2 | yes |  |
| `items[].processes[].command` | string | yes | Its command line as ps shows it, arguments joined with spaces. |
| `items[].processes[].reap` | boolean | yes |  |
| `quarantine` | string \| null | yes | Since 1.2.0. With --move-to <dir>: the folder the worktrees go to, absolute, which the token covers; a folder that exists or is on another volume refuses the plan (error set, token null, items kept). null without it. |
| `keepSuperset` | boolean | yes | Since 1.4.0. --keep-superset was given: the removed worktrees' Superset workspaces are left, and Superset is not asked anything. The token covers it. |
| `reap` | boolean | yes | Since 1.6.0. --reap was given: the processes still working in each worktree that goes are stopped first, and they no longer keep it. The token covers it, and the processes of each remove item. |
| `configFingerprint` | string \| null, matching `^[0-9a-f]{64}$` | yes | Since 1.5.0. The wt configuration the token covers: the lowercase hex SHA-256 of the configuration file wt read (bin/worktree/worktree.toml or worktree.conf). Two plans with the same value read the same file. null when there is none. It is the worktree's own file before the main checkout's. |
<!-- json-docs:end -->

`category`, exhaustively:

| Value | `wt sweep` prints it under | Meaning |
|---|---|---|
| `remove` | Will be removed with its branch | merged, and its worktree is safe to remove: the worktree goes the way `wt remove` takes it, then the branch |
| `delete` | Will be deleted | merged, and in no worktree: the branch goes |
| `inUse` | Merged, but kept | merged, but kept: its worktree is kept, or its name is a deliberate backup; `kept` says why |
| `upstreamGone` | Upstream gone, but not merged, so kept | its upstream is gone, but trunk lacks its commits |

`kept`, exhaustively. A worktree is never removed with any of these:

| Value | Meaning |
|---|---|
| `dirty` | uncommitted changes, untracked files, edits to files status is told to skip, and initialised submodules (their content, or a checked-out commit other than the recorded one) included, whatever git's config says |
| `statusUnknown` | `git status` failed there, so its changes could not be seen |
| `detached` | a worktree with no branch whose HEAD trunk contains; sweep never removes one (`wt remove <path>` does) |
| `session` | an agent session is in it, idle or busy |
| `sessionsUnknown` | the sessions could not be listed, so no worktree is known to be free of one |
| `lockHeld` | git's worktree lock, and its holder is still running |
| `directoryMissing` | git lists it, but its directory is gone (`git worktree prune`) |
| `heldByRebase`, `heldByBisect` | a rebase or bisect in that worktree holds the branch |
| `mainCheckout` | the main checkout is on the branch |
| `branchNotDeletable` | `wt remove` would not delete the branch with the worktree |
| `notMerged` | for `upstreamGone`: trunk lacks its commits |
| `deliberateBackup` | since 1.7.0: the branch name matches `ref_keep_patterns` (default `backup/*` `backup-*` `*-backup`), so the branch is not deleted and its worktree, if any, is not removed |
| `operation` | a rebase, merge, cherry-pick, revert or bisect is in progress there |
| `hiddenChanges` | files `git status` is told not to look at (assume-unchanged, skip-worktree) |
| `headUnreachable` | its HEAD holds commits that no ref or other worktree would |
| `reachUnknown` | what the removal would leave unreachable could not be worked out |
| `submoduleUnreachable` | a submodule has commits that only this worktree's copy of it holds |
| `nestedWorktree` | another worktree's checkout is inside this one |
| `processes` | since 1.6.0, without `--reap`: processes still run in it, listed in `processes`; something is using it |
| `processesUnknown` | since 1.6.0: the processes in it could not be found, or told apart from agent sessions |

The **token** covers the repository, trunk's name, the wt configuration file
(`configFingerprint` shows which), and every row: its category, branch, commit, worktree, evidence, pull request
(number, state, base) and reasons, codes and words. So a branch that moved, a
new merged branch, a worktree that turned dirty, a session that arrived or
changed, a pull request GitHub no longer answers for — any of them is a
different token. So is the `--move-to` folder, or its absence: a token read
for a move does not let a sweep delete instead. So is `--reap`, with the
processes each `remove` item would stop, by pid and command. A newer trunk commit that changes no row is not.

## `wt sweep --yes --json` — the sweep

The same sweep as `wt sweep --yes`. The plan as wt prints it, and the progress,
go to **stderr**; stdout carries exactly one object.

With `--expect <token>` (it needs `--yes --json`), the sweep fetches (unless
`--no-fetch`), makes the plan again and refuses, touching nothing, unless it has
that token: `outcome` `refused`, `error` set, no items, a non-zero exit. Then,
as every sweep does, it reads everything once more immediately before it acts,
and a row that changed in between is kept with the reason.

A handled SIGINT or SIGTERM writes the object too — the rows so far, the one in
flight as `interrupted` with what is true of it now — then exits 130 for SIGINT
or 143 for SIGTERM. SIGKILL or a crash may write none; treat a missing object as
unknown.

<!-- json-docs:begin sweep -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"sweep"` | yes |  |
| `repo` | string \| null | yes |  |
| `trunk` | string \| null | yes |  |
| `fetched` | boolean | yes | Whether origin was fetched first. |
| `token` | string \| null | yes | The token of the plan this run carried out; null when it stopped before one was made, or there was nothing to do. |
| `outcome` | `"done"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | done: every item to remove or delete went. refused: nothing was removed or deleted. partial: some went, some did not. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why the run stopped before touching anything: an --expect that no longer holds, the fetch, not the main checkout. |
| `items` | array of object | yes | Every row of the plan, in its order; empty when error stopped the run first. |
| `items[].category` | `"remove"` \| `"delete"` \| `"inUse"` \| `"upstreamGone"` | yes | Its part of the plan, as in the sweep-plan schema. |
| `items[].branch` | string \| null | yes |  |
| `items[].tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `items[].work` | string \| null | yes |  |
| `items[].path` | string \| null | yes |  |
| `items[].result` | `"removed"` \| `"deleted"` \| `"kept"` \| `"failed"` \| `"notRun"` \| `"interrupted"` | yes | removed: the worktree and its branch are gone. deleted: the branch is gone. kept: left as it was, on purpose. failed: tried and not finished; worktreeRemoved and branchDeleted say what did happen. notRun: the run ended before it. interrupted: the one a signal caught. |
| `items[].reason` | string \| null | yes | Why, for everything but removed and deleted. |
| `items[].worktreeRemoved` | boolean | yes | Whether the worktree is gone now. |
| `items[].branchDeleted` | boolean | yes | Whether the branch is gone now. |
| `items[].pin` | string \| null, matching `^refs/wt-swept/` | yes | Since 1.4.0. The ref the deleted branch was moved into, refs/wt-swept/<runId>/heads/<branch>, read back holding tip; null when the branch was not moved there. |
| `items[].restoreCommand` | array of string \| null, at least 1 items | yes | When the branch was deleted: the argv that puts it back, not its upstream setting: ["wt", "refs", "restore", runId, "--only", "refs/heads/<branch>", "--yes"] for a pinned branch, ["wt", "restore", dir] for the branch of a worktree moved to a folder. Run it in repo. Null when the branch is there, or gone with no pin. Before 1.4.0 it was ["git", "-C", repo, "branch", branch, tip]; that form is gone. |
| `items[].quarantine` | object \| null | yes | Since 1.1.0. For a worktree the run moved, or tried to move, into the quarantine; null otherwise. Read from the folder after the row. dir is its own folder (wt restore <dir> puts it back); checkoutMoved and adminMoved say which of the two moves are done. |
| `items[].quarantine.dir` | string | yes | This worktree's folder in the quarantine; wt restore <dir> puts it back. |
| `items[].quarantine.checkoutMoved` | boolean | yes | Its checkout is in <dir>/checkout now. |
| `items[].quarantine.adminMoved` | boolean | yes | Its admin dir is in <dir>/admin now: git no longer lists the worktree. |
| `items[].superset` | object \| null | yes | Since 1.2.0. For a worktree the run removed, what came of its Superset workspace: result, and reason, which is why for skipped, failed and optedOut and null otherwise. Null when no worktree was removed. It changes neither result nor outcome. |
| `items[].superset.result` | `"deregistered"` \| `"notRegistered"` \| `"skipped"` \| `"failed"` \| `"optedOut"` | yes | deregistered: the workspace was deleted, after the directory left its path and git stopped listing it. notRegistered: Superset is running and has no workspace there. skipped: the integration is off, Superset is not installed or not running, or the worktree was still there. failed: Superset erred. optedOut (since 1.3.0): --keep-superset; Superset was not asked. |
| `items[].superset.reason` | string \| null | yes | Why, for skipped, failed and optedOut (--keep-superset). |
| `items[].reaped` | array of object | yes | Since 1.5.0. With --reap: each process stopped, or tried, right before the worktree went, as in the remove schema; one that survived leaves the worktree in place, result failed. Empty otherwise. |
| `items[].reaped[].pid` | integer, at least 2 | yes |  |
| `items[].reaped[].command` | string | yes |  |
| `items[].reaped[].result` | `"terminated"` \| `"killed"` \| `"gone"` \| `"survived"` | yes |  |
| `recovery` | string \| null | yes | For interrupted: what wt printed. |
| `quarantine` | string \| null | yes | Since 1.1.0. With --move-to <dir>: the folder, absolute. Each removed worktree went into a folder of its own in it, with its own recovery.json. null without it. |
| `runId` | string \| null, matching `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4,32}$` | yes | Since 1.4.0. The run under refs/wt-swept/ the deleted branches were moved into, as wt refs sweep moves backups: wt refs swept lists it, wt refs restore puts them back, wt refs purge deletes them for good. null when no branch was moved: nothing was deleted, or only quarantined worktrees' branches, which the quarantine pins. A run a signal ended may name a run that holds only its meta. |
<!-- json-docs:end -->

`superset.result`, exhaustively: `deregistered` — the workspace was deleted,
after the directory had left its path and git no longer listed it;
`notRegistered` — Superset is running and has no workspace there; `skipped` —
the integration is off (`wt config set superset true` turns it on,
`SUPERSET_REGISTER=off` keeps a repository out), Superset is not installed or
not running, or the worktree was still there; `failed` — Superset erred;
`optedOut` (since 1.3.0) — `--keep-superset` was given and Superset was not
asked anything. None of them changes `result`, `outcome` or the exit code.

Superset has no delete that leaves the checkout alone: its `ws delete`
force-removes the checkout it recorded. wt asks for it only once the directory
has left its path and git no longer lists it, read again right before the call,
but the check and the delete are not one step: a worktree made at the same path
in the instant between them would be removed by Superset. A tool that creates
worktrees while it removes others passes `--keep-superset`, which closes that
window by not asking Superset at all.

`worktreeRemoved`, `branchDeleted` and `pin` are read from the repository after
the row, not from what was attempted.

### Deleted branches are pinned

Since 1.4.0 a sweep deletes no branch outright. Each one it deletes — a
`delete` row's, and a `remove` row's once its worktree is gone — is moved into
`refs/wt-swept/<runId>/heads/<branch>` the way `wt refs sweep` moves a backup:
the run's meta blob is written first, then each branch goes in one atomic
`update-ref` transaction that creates the pin at the planned tip and deletes the
branch at that tip, then its `branch.<name>.*` config. One sweep is one run,
begun at the first branch it moves: a sweep that moves none has `runId` null and
leaves nothing under `refs/wt-swept/`. `wt refs swept` lists the run,
`wt refs restore <runId>` puts the branches back, and `wt refs purge <runId>`
deletes them for good. With `--move-to`, a removed worktree's branch is
pinned with it under `refs/wt-quarantine/` and not a second time; a
`delete` row's branch is still pinned in the run. A signal after the meta is
written and before any branch moved can leave a run that holds only its meta:
`runId` then names it, and `wt refs purge` clears it.

`result`, exhaustively:

| Value | Meaning |
|---|---|
| `removed` | the worktree and its branch are gone |
| `deleted` | the branch is gone |
| `kept` | left as it was on purpose: the plan kept it (`inUse`, `upstreamGone`), or it changed or became unsafe after the plan was made |
| `failed` | tried and not finished; `worktreeRemoved` and `branchDeleted` say what did happen |
| `notRun` | not reached: the sweep ended before it |
| `interrupted` | the one a signal caught |

`outcome`, from the items to remove or delete:

| Value | When |
|---|---|
| `done` | every one `removed` or `deleted` — or there were none |
| `refused` | nothing was removed or deleted, or `error` stopped the sweep first |
| `partial` | some went, some did not |
| `interrupted` | a signal ended the sweep |

The exit code is non-zero when anything to remove or delete was kept or failed;
read `outcome`.

With `--move-to <dir>`, `<dir>` must be a new folder whose parent exists, on
the volume of every worktree the plan removes and of their admin dirs; otherwise
the sweep refuses before touching anything (`error` set). Each worktree goes into
`<dir>/<its directory's name>` (numbered `-2`, `-3` on a clash), with its own
`recovery.json`, exactly as `wt remove --move-to` would move it;
`worktreeRemoved` is then true once its checkout has left its path.

## `<dir>/recovery.json` — a removed worktree's journal

`wt remove <work> --move-to <dir>` (and `wt sweep --move-to`, per
worktree) moves the worktree aside instead of deleting it. `<dir>` is a new
folder the caller names, on the worktree's volume and outside every checkout of
the repository, its git dir and every other such folder (a purge of that one would
take it along); it is made with `mkdir`, not `mkdir -p`, and a
folder that exists refuses the removal, as does one inside the repository or a
checkout or admin dir on another volume: every move is a rename. After its last
check the
removal runs these steps, writing `recovery.json` durably (a temp file, fsync,
rename, folder fsync) before anything changes and before and after each step:

1. `lock` — `git worktree lock --reason "wt quarantine <dir>"`, fsynced, so
   nothing prunes the registration while the checkout is away (a stale lock, or
   one `--force` breaks, is released first);
2. `pin` — refs under `refs/wt-quarantine/<id>-<hash>/` hold HEAD and the
   branch's tip: once the branch is deleted and the admin dir is out of the
   repository, nothing else would, and `git gc` would take a squash-merged
   branch's commits;
3. `moveCheckout` — the checkout to `<dir>/checkout`;
4. `moveAdmin` — `.git/worktrees/<id>`, with the worktree's initialised
   submodule repositories under `modules/`, to `<dir>/admin`: git no longer lists
   the worktree. wt never runs `git worktree prune`;
5. `relink` — `<dir>/checkout/.git` names `<dir>/admin`, so a new worktree that
   takes the id is never the one git in the folder works on;
6. `branch` — the branch's local config section is recorded, then the branch is
   deleted or renamed as the plan says.

A file written after the last check moves with the checkout. Nothing is deleted:
emptying the folder is the user's own act, or `wt purge <dir>`'s. The
pins go when the restore is done, with the purge, or at the next `wt sweep` once
the folder has been deleted (its parent still
there: a folder on a volume that is not mounted keeps them).

<!-- json-docs:begin recovery -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"remove"` \| `"sweep"` | yes | The wt command that made the quarantine. |
| `createdAt` | string | yes | When, RFC 3339, UTC. |
| `repo` | string | yes | The repository's main checkout. |
| `commonDir` | string | yes | The repository's common git dir. |
| `worktreeId` | string | yes | The worktree's id: its admin dir is <commonDir>/worktrees/<worktreeId>. |
| `dir` | string | yes | The quarantine folder, as the removal named it. Its git lock reason while quarantined is "wt quarantine <dir>". |
| `checkout` | object | yes | The worktree's checkout; quarantined at <dir>/checkout. |
| `checkout.path` | string | yes | Where it was. |
| `checkout.quarantined` | string | yes | Where it goes in the quarantine. |
| `checkout.device` | integer, at least 0 | yes |  |
| `checkout.inode` | integer, at least 0 | yes | A rename keeps device and inode: a directory at either place, path or quarantined, with these is this one, and nothing else is. |
| `admin` | object | yes | The worktree's admin dir, .git/worktrees/<id>, with its initialised submodules' repositories under modules/; quarantined at <dir>/admin. Moving it is what unregisters the worktree. Fields as at `checkout`. |
| `gitFile` | string | yes | The checkout's .git file as it was. While quarantined, <dir>/checkout/.git names <dir>/admin instead, so a new worktree that takes the id is never the one git in the quarantine works on; restore writes this back. |
| `pins` | object | yes | Refs that hold the worktree's HEAD and its branch's tip while it is quarantined — once the branch is deleted and the admin dir is out of the repository nothing else may, and gc would take them. Created before anything moves, deleted when the restore is done, or by wt sweep once the quarantine folder has been deleted. |
| `pins.head` | string \| null | yes | refs/wt-quarantine/<id>-<hash>/head, at head; null when there is none. |
| `pins.tip` | string \| null | yes | refs/wt-quarantine/<id>-<hash>/tip, at branch.tip; null without a branch. |
| `pins.dir` | string | yes | refs/wt-quarantine/<id>-<hash>/dir, a blob holding the quarantine folder's path, by which wt sweep tells a deleted quarantine's pins. |
| `head` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit checked out; null before the first commit. |
| `branch` | object \| null | yes | The branch checked out; null for a detached HEAD or a branch with no commit. |
| `branch.name` | string | yes |  |
| `branch.tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `branch.plan` | `"delete"` \| `"keep"` \| `"none"` | yes | delete: merged, deleted. keep: renamed to keepAs. none: left alone. |
| `branch.keepAs` | string \| null | yes |  |
| `branch.config` | array of object \| null | yes | The branch's section of the local config, every value in order, captured right before the branch step; null until then. |
| `branch.config[].key` | string | yes |  |
| `branch.config[].value` | string | yes |  |
| `branch.result` | `"deleted"` \| `"renamed"` \| `"untouched"` \| `"kept"` \| `"failed"` \| `null` | yes | What the branch step came to. kept: turned down on purpose (moved, no longer merged, taken by a worktree). failed: git failed; the removal is partial. null before the step. |
| `steps` | array of object | yes | The removal's steps, exactly these in this order: lock (git's worktree lock with the quarantine's reason, fsynced), pin (the refs in pins), moveCheckout, moveAdmin, relink (the quarantined checkout's .git names <dir>/admin), branch. Closed: another step is a new major. |
| `steps[].name` | `"lock"` \| `"pin"` \| `"moveCheckout"` \| `"moveAdmin"` \| `"relink"` \| `"branch"` \| `"unlock"` \| `"unpin"` | yes |  |
| `steps[].state` | `"pending"` \| `"running"` \| `"done"` \| `"failed"` | yes | Closed. running is written before the step changes anything: left so, the step may or may not have happened, and the places say which. |
| `steps[].error` | string \| null | yes |  |
| `restore` | object \| null | yes | The restore's own journal; null until wt restore first changes something. |
| `restore.action` | `"none"` \| `"renameBack"` \| `"recreate"` \| `"attach"` \| `"detach"` \| `null` | yes | What the restore does to the branch; see the restore schema. |
| `restore.occupiedBy` | string \| null | yes | The worktree using the branch, when that made the action detach. |
| `restore.createdBranch` | boolean | no | Set once this restore has made the branch again (recreate): it owns that branch and its config until the restore is done, also after a failed step, so running it again puts back the recorded config still missing. Absent in records written before 1.1.0: read as false. |
| `restore.steps` | array of object | yes | branch, moveAdmin, relink, moveCheckout, unlock, unpin, exactly, in order. Closed. |
| `restore.steps[]` | object | yes | Fields as at `steps[]`. |
| `purge` | object \| null | no | The purge's journal: set only in <dir>/recovery.purging.json, the copy of this record `wt purge <dir>` writes, and which replaces recovery.json, before it deletes anything. With no recovery.json no wt restore, of any version, restores it: the pins may be gone. Running the purge again finishes it. Absent in records written before 1.2.0: read as null. |
| `purge.startedAt` | string | yes | When the purge began, RFC 3339, UTC. |
<!-- json-docs:end -->

The step lists and the states are **closed**, unlike the enumerations elsewhere:
wt refuses a record whose steps are not exactly these, in this order, so another
step or state comes as a new major (`recovery.v2.json`), never a minor. Fields
may still be added.

A step's `state` is `pending`, `running`, `done` or `failed`. `running` is
written before the step changes anything, so a step left `running` may or may
not have happened: the two places each directory can be (and its inode) say
which. A removal that left no result — killed, or crashed — is fully described by
the file. A lock `"wt quarantine <dir>"` on a worktree with no `recovery.json`
was stopped before anything moved.

A branch step that fails once the checkout has gone is **partial**, and wt says
"partly done", not "removed": the file's `branch.result` is `failed`.

## `wt restore <dir> --dry-run --json` — the plan

What `wt restore <dir>` would do, changing nothing: where each directory is,
what happens to the branch, and every reason it would refuse.

<!-- json-docs:begin restore-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"restore"` | yes |  |
| `dir` | string | yes | The quarantine folder, absolute. |
| `repo` | string \| null | yes | The repository, from recovery.json; null without one. |
| `checkout` | object \| null | yes |  |
| `checkout.path` | string | yes |  |
| `checkout.quarantined` | string | yes |  |
| `checkout.location` | `"original"` \| `"quarantined"` \| `"taken"` \| `"missing"` | yes | original: at path, the same directory (by inode) — moved back, or never moved. quarantined: in the quarantine. taken: something else is at path. missing: in neither place. |
| `admin` | object \| null | yes | Fields as at `checkout`. |
| `head` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit to detach at, if it comes to that. |
| `branch` | object \| null | yes |  |
| `branch.name` | string | yes |  |
| `branch.tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `branch.keepAs` | string \| null | yes |  |
| `branch.removal` | `"deleted"` \| `"renamed"` \| `"untouched"` \| `"kept"` \| `"failed"` \| `null` | yes | What the removal's branch step came to, as recovery.json records it. |
| `branch.action` | `"none"` \| `"renameBack"` \| `"recreate"` \| `"attach"` \| `"detach"` \| `null` | yes | none: the branch is there at its tip. renameBack: kept under keepAs; gets its name back, config included. recreate: deleted and the name free; made again at tip with its recorded config. attach: its name holds another commit and keepAs still has tip; HEAD names keepAs. detach: another worktree uses it, or it moved on; no branch change, HEAD detached at head. null when a problem stops the decision. |
| `branch.occupiedBy` | string \| null | yes | The worktree that has the branch — as HEAD, rebasing it, or bisecting from it — when that made the action detach. |
| `locked` | boolean | yes | The worktree carries this quarantine's git lock, which the restore takes off last. |
| `orphan` | string \| null | yes | A worktree locked for this quarantine with no recovery.json: stopped before anything moved; restoring it is unlocking it. |
| `problems` | array of object | yes | Every reason the restore refuses; empty when it would go ahead. Each has a code and its text. |
| `problems[].code` | `"noRecord"` \| `"repository"` \| `"checkoutTaken"` \| `"checkoutMissing"` \| `"adminTaken"` \| `"adminMissing"` \| `"volume"` \| `"commitMissing"` \| `"worktreesUnknown"` \| `"purging"` | yes | Why the restore refuses: noRecord, repository, checkoutTaken, checkoutMissing, adminTaken, adminMissing, volume, commitMissing, worktreesUnknown, or purging (a purge began: its pins may be gone). |
| `problems[].text` | string | yes | The problem in words. |
| `error` | string \| null | yes | The problems in words, or why the plan could not be made. |
<!-- json-docs:end -->

`branch.action`, exhaustively, decided in this order:

| Value | When | Then |
|---|---|---|
| `detach` | another worktree has the branch — under its name or the kept name — as HEAD, rebasing it (`head-name`), or bisecting from it (`BISECT_START`) | no branch change; HEAD detached at `head`; `occupiedBy` names that worktree |
| `none` | the branch is at `tip` | nothing |
| `renameBack` | its name is free and the kept name is at `tip` | renamed back, config section with it |
| `recreate` | its name is free and there is no kept branch | made again at `tip` (refused, naming the commit, when it is not in the repository), and its recorded config added to the empty section |
| `attach` | its name holds another commit, the kept name is at `tip` | HEAD names the kept branch |
| `detach` | anything else (the kept branch moved on) | as above, `occupiedBy` null |

Before the admin dir moves back — also when a restore that stopped resumes — the
branch HEAD will name is read again: gone, moved off `tip`, or taken by another
worktree in between, and HEAD is detached instead.

A removal that stopped before its admin dir moved never unregistered the
worktree, which may have been worked in since: `action` is `none` whatever its
branch did, and the restore only unlocks it and deletes the pins.

The recorded config goes back only when this restore made the branch again, or
the branch's section is empty: a branch somebody else made keeps its own config.
It goes back entry by entry, and one already there counts once, so a restore
stopped halfway through it adds the rest. Once it has made the branch
(`createdBranch`) the branch stays its own: a restore run again after a failed
config write adds the entries still missing, leaves a key somebody set to a
value of their own since, and fails unless every other recorded entry is back.

## `wt restore <dir> --json` — the result

The restore: the branch action, then the admin dir back — its parent folders made
again if `git worktree prune` or a tidy took them, never the directory itself —
then the checkout's `.git` as it was, then the checkout, then the removal's
lock off and its pins deleted, each journalled in `recovery.json` and idempotent, so
running `wt restore <dir>` again finishes a restore — or a removal — stopped
anywhere. The human output goes to stderr. A handled SIGINT or SIGTERM writes the
object too.

<!-- json-docs:begin restore -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"restore"` | yes |  |
| `dir` | string | yes |  |
| `repo` | string \| null | yes | As in the plan: the repository's main checkout, from recovery.json; when the record cannot be loaded, the current repository's. |
| `outcome` | `"restored"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | restored: the worktree is registered at its path again, with the branch action done, the quarantine's lock is off and its pins are deleted. refused: nothing changed. partial: a step failed after others were done; recovery.json says which, and wt restore <dir> again finishes it. interrupted: a signal ended the run. |
| `error` | string \| null | yes | The problems in words; null when there were none. |
| `checkout` | object \| null | yes | As in the plan, read when the object is written. |
| `checkout.path` | string | yes |  |
| `checkout.quarantined` | string | yes |  |
| `checkout.location` | `"original"` \| `"quarantined"` \| `"taken"` \| `"missing"` | yes | original: at path, the same directory (by inode) — moved back, or never moved. quarantined: in the quarantine. taken: something else is at path. missing: in neither place. |
| `admin` | object \| null | yes | As in the plan, read when the object is written. Fields as at `checkout`. |
| `branch` | object \| null | yes | As in the plan, read when the object is written. |
| `branch.name` | string | yes |  |
| `branch.tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `branch.keepAs` | string \| null | yes |  |
| `branch.removal` | `"deleted"` \| `"renamed"` \| `"untouched"` \| `"kept"` \| `"failed"` \| `null` | yes | What the removal's branch step came to, as recovery.json records it. |
| `branch.action` | `"none"` \| `"renameBack"` \| `"recreate"` \| `"attach"` \| `"detach"` \| `null` | yes | none: the branch is there at its tip. renameBack: kept under keepAs; gets its name back, config included. recreate: deleted and the name free; made again at tip with its recorded config. attach: its name holds another commit and keepAs still has tip; HEAD names keepAs. detach: another worktree uses it, or it moved on; no branch change, HEAD detached at head. null when a problem stops the decision. |
| `branch.occupiedBy` | string \| null | yes | The worktree that has the branch — as HEAD, rebasing it, or bisecting from it — when that made the action detach. |
| `unlocked` | boolean | yes | This run took the quarantine's lock off. |
| `steps` | array of object | yes | The restore's steps as recovery.json records them — branch, moveAdmin, relink, moveCheckout, unlock, unpin — or empty when it has none. The list is closed. |
| `steps[].name` | `"branch"` \| `"moveAdmin"` \| `"relink"` \| `"moveCheckout"` \| `"unlock"` \| `"unpin"` | yes |  |
| `steps[].state` | `"pending"` \| `"running"` \| `"done"` \| `"failed"` | yes |  |
| `steps[].error` | string \| null | yes |  |
| `recovery` | string \| null | yes | For interrupted: what wt printed. |
<!-- json-docs:end -->

## `wt purge <dir> --dry-run --json` — the plan

What `wt purge <dir>` would delete, for good, and every reason it
would refuse, deleting nothing. `--json` without `--yes` prints it too.
`command` is `"quarantine purge"` and the schemas are `quarantine-purge-plan`
and `quarantine-purge`, as they were when the command was `wt quarantine purge`.

<!-- json-docs:begin quarantine-purge-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"quarantine purge"` | yes |  |
| `dir` | string | yes | The folder, absolute. |
| `repo` | string \| null | yes | The repository's main checkout, from recovery.json; null without one. |
| `state` | `"quarantined"` \| `"restored"` \| `"purging"` \| `"empty"` \| `"unsettled"` \| `null` | yes | quarantined: a removal moved a worktree here and finished; the purge deletes it for good. restored: wt restore put it back; only what it left goes. purging: a purge began and stopped; this one finishes it. empty: an empty folder; nothing names what it was, so it is refused (problem empty). A purge stopped between its last two steps leaves one. unsettled: a removal or a restore stopped mid-way; refused (problem unsettled). null: the folder is not a quarantine purge can read. |
| `token` | string \| null | yes | Names this plan: the folder by path and identity, its state, its record byte for byte, its entries, every path in it with its type, size and time, each pin and where it is, and what would be lost. null when the purge would refuse. The purge reads the plan again, holding the folder's lock, and goes ahead only while it is the same. |
| `worktree` | string \| null | yes | Where the quarantined worktree's checkout was, from recovery.json. |
| `branch` | string \| null | yes | The branch it had checked out, from recovery.json. |
| `createdAt` | string \| null | yes | When the quarantine was made, RFC 3339, UTC. |
| `entries` | array of string | yes | Every name in the folder, sorted: all of it is deleted. A symlink is deleted, never what it names. |
| `bytes` | integer \| null, at least 0 | yes | The size of every file in the folder, symlinks not followed; null when something in it cannot be read. |
| `pins` | array of object | yes | The refs recovery.json names, in the order head, tip, dir. Each is deleted only while it is at oid. |
| `pins[].name` | `"head"` \| `"tip"` \| `"dir"` | yes |  |
| `pins[].ref` | string | yes | refs/wt-quarantine/<id>-<hash>/<name>. |
| `pins[].oid` | string \| null | yes | Where it is now; null when it is gone already. |
| `unreachable` | array of object | yes | Each pinned commit whose history nothing else holds: once the pins go, gc may take those commits. |
| `unreachable[].kind` | `"head"` \| `"tip"` | yes |  |
| `unreachable[].oid` | string | yes |  |
| `unreachable[].count` | integer, at least 1 | yes | Commits reachable from oid and from no other ref or worktree HEAD. |
| `reachError` | string \| null | yes | Why what becomes unreachable could not be counted. |
| `repositoryGone` | boolean | yes | The repository's git dir, as recorded, is gone. Deleted, its pins went with it; moved, they stay there until wt sweep in it drops them. The purge goes ahead on the record's own checks, and every pin is listed with oid null. |
| `problems` | array of object | yes |  |
| `problems[].code` | `"missing"` \| `"notAFolder"` \| `"noRecord"` \| `"wrongFolder"` \| `"repository"` \| `"pinsForeign"` \| `"pinsUnreadable"` \| `"unsettled"` \| `"foreignEntry"` \| `"nestedQuarantine"` \| `"empty"` \| `"worktreesUnknown"` \| `"registered"` \| `"locked"` \| `"location"` | yes | missing: no such folder. notAFolder: a symlink or a file. noRecord: no recovery.json (nor recovery.purging.json), or one wt cannot trust. wrongFolder: the record was written for another folder, or what is at its checkout or admin is not the directory the removal moved there. repository: its repository cannot be opened, or is another one now. pinsForeign: a pin it names is not this quarantine's, or holds something the removal did not pin. pinsUnreadable: git cannot read a pin (a broken ref), which is not taken for one that is gone. unsettled: a removal or a restore stopped mid-way; wt restore <dir> settles it. worktreesUnknown: the worktrees cannot be listed. registered: git has a worktree registered inside the folder. locked: a worktree is locked for this quarantine. location: the folder is inside a checkout or the git dir, or holds the git dir. foreignEntry: something at its top level no quarantine in its state leaves there (quarantined: checkout, admin; always: the journal, its temp file, .DS_Store). nestedQuarantine: another quarantine's record below its top level. empty: an empty folder. |
| `problems[].text` | string | yes | The problem in words. |
| `error` | string \| null | yes | The problems in words. |
<!-- json-docs:end -->

`state`:

| Value | Meaning | The purge |
|---|---|---|
| `quarantined` | a removal moved a worktree here and finished (a `failed` branch step counts: the branch stayed) | deletes the pins, then the folder: the worktree is gone for good |
| `restored` | `wt restore` put it back | deletes what is left: the folder (`recovery.json`) and any pin |
| `purging` | a purge began and stopped: `recovery.purging.json` is there | finishes it |
| `empty` | an empty folder: nothing names what it was (a purge stopped between deleting its journal and the folder leaves one) | refuses (`empty`); `rmdir` takes it |
| `unsettled` | a removal or a restore stopped mid-way | refuses: `wt restore <dir>` settles it |

It refuses, deleting nothing, on any problem:

| Code | When |
|---|---|
| `missing` | no such folder (a purge that finished leaves none) |
| `notAFolder` | `<dir>` is a symlink or a file |
| `noRecord` | no `recovery.json` (nor `recovery.purging.json`) in a folder that is not empty, or one wt cannot trust |
| `wrongFolder` | the record was written for another folder, names places that are not this folder's `checkout` and `admin`, or what is at `checkout` or `admin` is not the directory the removal moved there (by device and inode) |
| `repository` | its repository cannot be opened, or has another git dir now |
| `pinsForeign` | a pin it names is not under this folder's `refs/wt-quarantine/<id>-<hash>/`, or holds something the removal did not pin |
| `pinsUnreadable` | git cannot read a pin (a broken ref): not taken for one that is gone |
| `unsettled` | a removal or a restore stopped mid-way |
| `worktreesUnknown` | the worktrees cannot be listed |
| `registered` | git has a worktree registered inside the folder — compared by the directories paths lead to, not by spelling |
| `locked` | a worktree is locked for a removal into this folder (`wt quarantine <dir>`) |
| `location` | the folder is inside a checkout or the git dir, or holds the git dir |
| `foreignEntry` | something at its top level a removal never leaves there in that state: only `checkout` and `admin` (while `quarantined` or `purging`), the journal and its temp file, and `.DS_Store` are |
| `nestedQuarantine` | the record of another folder a worktree was moved to (one that validates, not a project file of that name) anywhere below its top level |
| `empty` | an empty folder |

## `wt purge <dir> --yes --json` — the result

The purge holds a lock on the folder that `wt restore` takes too, reads the
plan again under it and goes ahead only while it is the same (else `planChanged`,
nothing deleted). Then, in this order: `recovery.purging.json` — the record with
`purge.startedAt` — replaces `recovery.json`, so no `wt restore` takes a worktree
whose pins are going; each pin is deleted only while it is at the `oid` read;
git's worktrees are read once more; every entry the plan listed is deleted —
`checkout`, then `admin`, then the rest by name — and one that appeared since
stops it; then `recovery.purging.json`, then the folder.
Every deletion is made inside the folder as opened and checked, never through its
path again, never following a symlink, making a read-only folder writable first.
A purge stopped anywhere but between its last two steps leaves its journal, and
running it again finishes it; stopped there, it leaves an empty folder, its pins
already gone, which it then refuses (`empty`).

Inside the checkout it deletes as `rm -rf` would: into a volume mounted there,
and not past a file flagged immutable (`partial`). The record is trusted as wt
wrote it: a forged one naming this folder, its inodes and its pins passes, and is
not a boundary — whoever can write the folder can delete it.
The human output goes to stderr; a handled SIGINT or SIGTERM writes the object
too.

<!-- json-docs:begin quarantine-purge -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"quarantine purge"` | yes |  |
| `dir` | string | yes |  |
| `repo` | string \| null | yes | As in the plan: the repository's main checkout, from recovery.json; null without one. |
| `state` | `"quarantined"` \| `"restored"` \| `"purging"` \| `"empty"` \| `"unsettled"` \| `null` | yes | What the folder was when the purge planned, as in the plan. |
| `token` | string \| null | yes | The token of the plan the purge went by. |
| `outcome` | `"purged"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | purged: every pin and the folder are gone. refused: nothing was deleted (problems says why, planChanged for --expect). partial: the purge began and stopped; recovery.purging.json, or an empty folder, says so, and running it again finishes it. interrupted: a signal ended the run. |
| `error` | string \| null | yes |  |
| `problems` | array of object | yes |  |
| `problems[].code` | `"missing"` \| `"notAFolder"` \| `"noRecord"` \| `"wrongFolder"` \| `"repository"` \| `"pinsForeign"` \| `"pinsUnreadable"` \| `"unsettled"` \| `"foreignEntry"` \| `"nestedQuarantine"` \| `"empty"` \| `"worktreesUnknown"` \| `"registered"` \| `"locked"` \| `"location"` \| `"planChanged"` | yes | As in the plan, and planChanged: the plan is not the one --expect named, not the one the question was asked about, or not the one read again under the folder's lock. |
| `problems[].text` | string | yes | The problem in words. |
| `pins` | array of object | yes | oid is as planned; result is read when the object is written. |
| `pins[].name` | `"head"` \| `"tip"` \| `"dir"` | yes |  |
| `pins[].ref` | string | yes |  |
| `pins[].oid` | string \| null | yes | Where it was when the purge planned; null when it was gone already. |
| `pins[].result` | `"deleted"` \| `"absent"` \| `"kept"` | yes | deleted: it was there and is gone. absent: it was gone already. kept: it is still there, or cannot be read. It is read when the object is written. |
| `folderDeleted` | boolean | yes | The folder is gone. |
| `recovery` | string \| null | yes | For interrupted: what wt printed. |
<!-- json-docs:end -->

## `wt refs sweep --dry-run --json` — the ref sweep's plan

What `wt refs sweep` would move, as one object: every branch and tag whose name
matches a backup pattern — here, and with `--remote` on origin — what it is, and
a token. It moves nothing. It fetches origin first unless `--no-fetch`; with
`--remote` it also runs `git ls-remote origin` and asks GitHub, once, which of
origin's backup branches have an open pull request. `wt refs sweep --json`
without `--yes` prints the same plan. It runs from any worktree of the
repository.

A **backup** is a ref whose short name matches one of `ref_sweep_patterns`
(user config; the default is the seven transient `safe/*` `safety/*` `safe-*`
`safety-*` `*-safe` `*-safety` `prefold/*`) or one of `ref_keep_patterns` (the
deliberate backups: `backup/*` `backup-*` `*-backup`), where `*` matches any run of
characters, slashes included. Every other ref under `refs/heads/`,
`refs/tags/` and `refs/remotes/origin/` is a **container** — trunk, protected
and checked-out branches included. For a remote backup only origin's own
branches and tags count. Nothing under `refs/wt-swept/`, `refs/wt-quarantine/`,
`refs/wt-sync/` or `refs/stash` is either.

**All contact with origin goes through one URL.** `--remote` resolves origin's
effective fetch and push URLs (`git remote get-url --all origin` and `--push
--all`, `insteadOf` and `pushInsteadOf` applied) and requires them to be one and
the same URL, else `remoteAmbiguous`. The listing, the fetch into each pin, the
delete push, and later the restore's reads, push and read-back all use it. A URL
that carries a password in any form (`user:secret@host:path` too), or any
userinfo on http, https, ftp or ftps — a token in the user name cannot be told
from a name — is refused with `remoteCredentialsInUrl`, and a
`<transport>::<address>` URL is judged on its address too; a user name on ssh
(`git@host:…`) is fine. The userinfo of a `scheme://` URL runs to the last `@`
before the first `/`, `?` or `#`; in other forms it is what precedes the first
`@` with no `/` before it. These rules are gittree's `RemoteURL`, rule for rule.
Only the URL with all its userinfo removed (`git@host:o/r` → `host:o/r`) is ever
printed or recorded.

A plan that cannot be made — no trunk, the fetch failed, `--remote` without an
origin or with one of those problems — is still one object, with `error` set,
and a non-zero exit. So is an
`--only` that names an id the plan does not have or a row it keeps: the items
are there, the token is null.

<!-- json-docs:begin refs-sweep-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"refs sweep"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `trunk` | string \| null | yes | Trunk's branch name. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `null` | yes | How trunk was found: see trunkSource in wt status --json. |
| `fetched` | boolean | yes | Whether origin was fetched first. |
| `remote` | boolean | yes | Whether --remote was given. |
| `patterns` | array of string | yes | ref_sweep_patterns, in the order they are tried. |
| `keepPatterns` | array of string | yes | ref_keep_patterns: a ref matching one is listed as kept (deliberateBackup) and never selected, even when patterns matches it too. |
| `age` | string \| null | yes | ref_sweep_age, e.g. 14d. |
| `now` | integer | yes | Unix seconds the plan was made at; age is measured from it. |
| `endpoint` | object \| null | yes | With --remote: the one URL all contact with origin goes through, and its digest, which the token covers. Null without --remote. It is {url, digest}: the one URL with its userinfo removed, and sha256: followed by the lowercase hex SHA-256 of the full effective URL. |
| `endpoint.url` | string | yes | origin's one effective URL, fetch and push alike, insteadOf and pushInsteadOf applied, with all userinfo removed (git@host:o/r is host:o/r). |
| `endpoint.digest` | string, matching `^sha256:[0-9a-f]{64}$` | yes | sha256: and the lowercase hex SHA-256 of the effective URL's UTF-8 bytes. |
| `runId` | null | yes | Always null: the run that applies the plan mints it. |
| `token` | string \| null, matching `^rs1-[0-9a-f]{32}$` | yes | rs1-…: names the plan; pass it to --yes --expect. It covers the rows, --remote, the patterns, the age, the repository and trunk, not the selection, so --only picks from the plan a token names. Null when no row could be swept, or on error. |
| `error` | string \| null | yes | Why no plan could be made. |
| `problems` | array of object | yes | {code, text} for the refusals that have a code: remoteAmbiguous, remoteCredentialsInUrl; empty otherwise. |
| `problems[].code` | `"remoteAmbiguous"` \| `"remoteCredentialsInUrl"` \| `"planChanged"` \| `"runIdTaken"` | yes | remoteAmbiguous: --remote, and origin's effective fetch and push URLs are not one and the same URL. remoteCredentialsInUrl: --remote, and that URL carries a password (in any form, scp-like too), or any userinfo on http, https, ftp or ftps; a <transport>::<address> URL is judged on its address too. planChanged: the plan is not the one --expect named, or not the one the question was asked about. runIdTaken: --run-id names a run that has refs under refs/wt-swept/ already. |
| `problems[].text` | string | yes | The refusal in words. |
| `items` | array of object | yes | Every backup, here first, then origin's. |
| `items[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `items[].kind` | `"branch"` \| `"tag"` \| `"remoteBranch"` \| `"remoteTag"` | yes | branch, tag, remoteBranch or remoteTag: a branch or tag here, or on origin. |
| `items[].name` | string | yes | The short name the pattern matched. |
| `items[].pattern` | string | yes | The first pattern it matched. |
| `items[].category` | `"backupContained"` \| `"backupOld"` \| `"backupYoung"` \| `"kept"` | yes | backupContained: another ref contains it (selected). backupOld: it holds commits no other ref has and is older than age (selected). backupYoung: the same, younger (not selected unless --only names it). kept: something holds it, or it is a deliberate backup (ref_keep_patterns); kept says what. Never selected. |
| `items[].selected` | boolean | yes | A sweep with this plan moves it: backupContained and backupOld by default, or what --only names. |
| `items[].tip` | string \| null, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The commit; null when it is not here (objectMissing). |
| `items[].object` | string, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The ref's own value: the tag object of an annotated tag, else the tip. |
| `items[].annotated` | boolean \| null | yes | Tags: an annotated tag. Null for branches. |
| `items[].containedIn` | string \| null | yes | The first ref that contains the tip: trunk (refs/heads/<trunk>, then refs/remotes/origin/<trunk>), then origin's branches, then the rest, each group by name. A remote row names origin's ref as origin:<ref>. |
| `items[].uniqueCommits` | integer \| null, at least 0 | yes | Commits on no container; null when contained or not known. |
| `items[].date` | integer \| null | yes | Unix seconds: when the branch was created, the tag was made, or the tip was committed; see dateSource. See dateSource. |
| `items[].dateSource` | `"reflog"` \| `"tagger"` \| `"commit"` \| `null` | yes | Which point in time date is: reflog, a branch here, the first entry of its reflog, which is when it was made; tagger, an annotated tag, its tagger date; commit, anything else, its tip's committer date. |
| `items[].kept` | array of `"worktree"` \| `"heldByRebase"` \| `"heldByBisect"` \| `"quarantine"` \| `"protected"` \| `"objectMissing"` \| `"pullRequestOpen"` \| `"pullRequestUnknown"` \| `"notContained"` \| `"deliberateBackup"` | yes | Every reason that holds it; empty unless category is kept. |
| `items[].subject` | string \| null | yes | The tip's commit subject. |
<!-- json-docs:end -->

The categories:

- `backupContained` — a container has its tip: nothing is only on it. Selected.
- `backupOld` — it holds commits no container has, and `date` is older than
  `age`. Selected. Here only: a remote backup is never swept unless contained.
- `backupYoung` — the same, younger. Not selected unless `--only` names it.
  A `git branch safe/x` taken this morning of a three-week-old branch is
  young: its date is the branch's reflog, not its commits.
- `kept` — something holds it; `kept` says what:

| `kept` code | Meaning |
|---|---|
| `worktree` | a worktree has the branch checked out (the main checkout included) |
| `heldByRebase`, `heldByBisect` | a rebase or bisect in a worktree holds it |
| `quarantine` | a worktree moved to a folder that is still there names it: the branch a `--move-to` removal moved aside, or the name it was kept under, read from the `recovery.json` its pins under `refs/wt-quarantine/` name; or, when that folder is there and its record cannot be read, the commit its tip pin holds. One whose pins are gone cannot be seen here. The plan wt prints says `a worktree moved aside` for it |
| `protected` | trunk, the branch origin's HEAD names, or a long-lived branch (`main`, `master`, `develop`, `development`, `staging`, `production`, `release*`) |
| `objectMissing` | the object is not here, so containment cannot be read (a remote ref not fetched) |
| `pullRequestOpen` | a remote branch with an open pull request: deleting it would close the pull request |
| `pullRequestUnknown` | a remote branch, and GitHub is off, unreachable or not logged in |
| `notContained` | a remote ref no ref on origin contains |
| `deliberateBackup` | its name matches `ref_keep_patterns`: a backup someone made on purpose. Listed, never selected, and `--only` refuses it; the plan wt prints says `a deliberate backup` |

## `wt refs sweep --yes --json` — the ref sweep

The same run as `wt refs sweep --yes`. The plan as wt prints it and the progress
go to **stderr**; stdout carries exactly one object.

The run makes the plan (fetching unless `--no-fetch`), and with `--expect
<token>` refuses, moving nothing, unless the plan made now has that token. With
`--only` it moves exactly the rows named (an unknown or kept id refuses the
lot). Then it takes the run id — `--run-id <id>`, or one it mints: the UTC
second it began and four hex digits, `20260930T091500Z-3f2a`. A `--run-id`
must match `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4,32}$` and have nothing under
`refs/wt-swept/<id>/` yet (else `runIdTaken`, nothing moved).

Before any ref moves it writes the run's **meta**: a blob — not a commit, so no
history graph shows it — at `refs/wt-swept/<runId>/meta`, holding JSON `{runId,
repo (the git dir, absolute), sweptAt (unix seconds), endpoint ({url, digest} or
null), wtVersion}`. A run stopped anywhere after that is found by its id, and a
remote ref is only restored to the origin the meta names. Then it moves each
selected row, reading it once more right before:

- **here**, one `git update-ref --stdin` transaction per ref: create the pin at
  `object`, delete the ref at `object`. Either both happen or neither; a ref
  that moved since the plan is `kept`. A branch checked out since the plan is
  `kept`. A branch's `branch.<name>.*` config goes with it, as with `git branch
  -D`; a restore does not bring the upstream back.
- **on origin**, `git fetch origin +<ref>:<pin>`, then, only if the pin is at
  `object`, `git push --force-with-lease=<ref>:<object> origin :<ref>`, then
  `git ls-remote` reads it back. A pin at anything else is dropped and the row
  is `kept`.

Each pin is `refs/wt-swept/<runId>/<space>/<name>`, `<space>` being `heads`,
`tags`, `remote-heads` or `remote-tags`. The pins and the meta are the record:
there is no journal file.

A handled SIGINT or SIGTERM writes the object too, with `outcome` `interrupted`,
then exits 130 for SIGINT or 143 for SIGTERM: the rows done keep their result,
the row in flight is `interrupted` with `pinned` and `deleted` read back, the
rest are `notRun`. The same holds for `wt refs restore` and `wt refs purge`.

<!-- json-docs:begin refs-sweep -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"refs sweep"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `trunk` | string \| null | yes | Trunk's branch name. |
| `fetched` | boolean | yes | Whether origin was fetched first. |
| `remote` | boolean | yes | Whether --remote was given. |
| `token` | string \| null, matching `^rs1-[0-9a-f]{32}$` | yes | The token of the plan the run went by. |
| `runId` | string \| null, matching `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4,32}$` | yes | The run's id, which names its pins; null when the run never began: it was refused, or nothing was selected. A run that ends having moved nothing drops its meta, so nothing of it is left under refs/wt-swept/. |
| `outcome` | `"done"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | done: every selected row was swept. refused: nothing was pinned or deleted (error says why, e.g. --expect). partial: some were, some not. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why the run stopped before moving anything. |
| `problems` | array of object | yes | {code, text}: the plan's, and planChanged (--expect named another plan, or the plan changed while the question was open) or runIdTaken. |
| `problems[].code` | `"remoteAmbiguous"` \| `"remoteCredentialsInUrl"` \| `"planChanged"` \| `"runIdTaken"` | yes | remoteAmbiguous: --remote, and origin's effective fetch and push URLs are not one and the same URL. remoteCredentialsInUrl: --remote, and that URL carries a password (in any form, scp-like too), or any userinfo on http, https, ftp or ftps; a <transport>::<address> URL is judged on its address too. planChanged: the plan is not the one --expect named, or not the one the question was asked about. runIdTaken: --run-id names a run that has refs under refs/wt-swept/ already. |
| `problems[].text` | string | yes | The problem in words. |
| `recovery` | string \| null | yes | For interrupted: what wt printed. |
| `items` | array of object | yes | Every row of the plan, in its order. |
| `items[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `items[].kind` | `"branch"` \| `"tag"` \| `"remoteBranch"` \| `"remoteTag"` | yes | branch, tag, remoteBranch or remoteTag: a branch or tag here, or on origin. |
| `items[].name` | string | yes | The short name the pattern matched. |
| `items[].category` | `"backupContained"` \| `"backupOld"` \| `"backupYoung"` \| `"kept"` | yes | backupContained: another ref contains it (selected). backupOld: it holds commits no other ref has and is older than age (selected). backupYoung: the same, younger (not selected unless --only names it). kept: something holds it, or it is a deliberate backup (ref_keep_patterns); a kept row is never selected. |
| `items[].selected` | boolean | yes | Whether a sweep with this plan moves it: backupContained and backupOld by default, or exactly what --only names. |
| `items[].tip` | string \| null, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The commit; null when it is not here (objectMissing). |
| `items[].object` | string, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The ref's own value: the tag object of an annotated tag, else the tip. |
| `items[].result` | `"swept"` \| `"kept"` \| `"failed"` \| `"notRun"` \| `"interrupted"` \| `"notSelected"` | yes | swept: pinned and deleted. kept: the plan kept it, or it moved or was taken after the plan (reason says). failed: tried and not finished (pinned and deleted say what did happen). notRun: the run ended before it. interrupted: a signal caught it. notSelected: the plan did not select it. |
| `items[].reason` | string \| null | yes | Why, for kept and failed. For a row the plan kept, the codes that hold it; for a row kept when the run reached it, what changed after the plan was made (it moved, it was deleted, it was checked out). |
| `items[].pin` | string \| null | yes | refs/wt-swept/<runId>/{heads,tags,remote-heads,remote-tags}/<name>, for a selected row once the run began. |
| `items[].pinned` | boolean | yes | The pin holds object, read back after the row. |
| `items[].deleted` | boolean | yes | The ref is gone, here or on origin, read back after the row. |
| `items[].restoreCommand` | array of string \| null | yes | For a row pinned and deleted: the argv that puts it back, ["wt", "refs", "restore", <runId>, "--only", <id>, "--yes"], whatever the row's kind (a branch, a tag, or a remote ref). Run it in repo. |
<!-- json-docs:end -->

Each item: `id`, `kind`, `name`, `category`, `selected`, `tip`, `object` as in
the plan, and. Its fields are in the field table above.

## `wt refs restore <runId> --dry-run --json` — the plan

What `wt refs restore <runId>` would put back, as one object: each pin of the
run, and what would be done with it. It changes nothing; for a remote pin it asks
origin (`ls-remote`) what is at the name. `wt refs restore <runId> --json`
without `--yes` prints the same plan. `--only <id>`, one id per flag, restores
just those; an id the run does not have, or one whose name is taken, refuses.

<!-- json-docs:begin refs-restore-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"refs restore"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `runId` | string | yes | The run id as given, also when it is not a run id (then error says so). |
| `token` | string \| null, matching `^rr1-[0-9a-f]{32}$` | yes | rr1-…: names the plan; pass it to --yes --expect. It covers the repository, the run, each pin with its object and action, and, when a remote ref can go back and origin resolves to one URL now, that URL's digest. Null when nothing can go back, or on error. |
| `error` | string \| null | yes | Why no plan could be made: a run id that is not one, a run with nothing under refs/wt-swept/<runId>/, or --only refused. |
| `items` | array of object | yes | The refs the run pinned, each with its id, kind, name, pin, object, action and whether it is selected. |
| `items[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `items[].kind` | `"branch"` \| `"tag"` \| `"remoteBranch"` \| `"remoteTag"` | yes | branch, tag, remoteBranch or remoteTag: a branch or tag here, or on origin. |
| `items[].name` | string | yes | The short name of the ref. |
| `items[].pin` | string | yes | The pin under refs/wt-swept/ that holds the object. |
| `items[].object` | string, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The ref's own value: the tag object of an annotated tag, else the tip. |
| `items[].action` | `"create"` \| `"none"` \| `"occupied"` \| `"remoteOccupied"` \| `"pinGone"` \| `"endpointChanged"` \| `"endpointUnknown"` | yes | create: the name is free; it is made at object. none: it is already there at object; only the pin goes. occupied: the name holds another object here. remoteOccupied: origin has something there. endpointChanged: a remote ref, and origin no longer resolves to the URLs the run recorded in its meta. endpointUnknown: a remote ref, and the run has no meta. pinGone: the pin is not there any more; a pin that goes between the plan and the run is reported as kept, its reason starting pinGone. |
| `items[].selected` | boolean | yes | A restore with this plan acts on it: every row whose action is create or none, or what --only names. |
<!-- json-docs:end -->

`action` is `create` (the name is free: the ref is made at `object`), `none` (it
is there at `object` already: only the pin goes), `occupied` (the name holds
something else here), `remoteOccupied` (origin has something there) or `pinGone`
(the pin is not there), and for a remote ref `endpointChanged` (origin's one
URL, resolved as the sweep does, no longer has the digest the run's meta
recorded, or no longer resolves to one URL) or `endpointUnknown` (the run has no
meta). A pin that moves or goes between the plan and the run is `kept`, its
`reason` starting `pinGone`. Local refs are not affected by the endpoint. `selected` is every row whose action
is `create` or `none`, or what `--only` names.

## `wt refs restore <runId> --yes --json` — the result

Here each ref is one `update-ref --stdin` transaction: create the ref at
`object` (only where it is not), delete the pin at `object`. On origin it is
`git push --force-with-lease=<ref>: origin <pin>:<ref>` (only where origin has
nothing), then `git ls-remote` must show the ref at `object`, and only then is
the pin deleted, at its own value. A push that was rejected, timed out or was
interrupted, or a read-back that does not show it, keeps the pin (`failed`); a
later restore that finds origin holding the ref at `object` plans `none` and
only drops the pin. A branch comes back without its upstream setting. A run
whose pins are all restored leaves nothing under `refs/wt-swept/<runId>/`: its
meta goes once its last pin does.
`--expect <token>` refuses unless the plan made now has that token. The human
output goes to stderr; a handled SIGINT or SIGTERM writes the object too.

<!-- json-docs:begin refs-restore -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"refs restore"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `runId` | string | yes | The run id as given; one not in wt's format is an error. |
| `token` | string \| null, matching `^rr1-[0-9a-f]{32}$` | yes | The token of the plan the run went by. |
| `outcome` | `"done"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | done: every selected row was restored. refused: nothing changed. partial: some were, some not. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why the run stopped before moving anything; as in the sweep. |
| `recovery` | string \| null | yes | For interrupted: what wt printed. |
| `items` | array of object | yes | The plan's items, each with its result (restored, kept, failed, notRun, interrupted, notSelected), reason, restored (the ref is at object, read back) and pinDeleted (the pin is gone, read back). |
| `items[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `items[].kind` | `"branch"` \| `"tag"` \| `"remoteBranch"` \| `"remoteTag"` | yes | branch, tag, remoteBranch or remoteTag: a branch or tag here, or on origin. |
| `items[].name` | string | yes | The short name of the ref. |
| `items[].pin` | string | yes | The pin under refs/wt-swept/ that holds the object. |
| `items[].object` | string, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The ref's own value: the tag object of an annotated tag, else the tip. |
| `items[].action` | `"create"` \| `"none"` \| `"occupied"` \| `"remoteOccupied"` \| `"pinGone"` \| `"endpointChanged"` \| `"endpointUnknown"` | yes | create: the name is free; it is made at object. none: it is already there at object; only the pin goes. occupied: the name holds another object here. remoteOccupied: origin has something there. endpointChanged: a remote ref, and origin no longer resolves to the URLs the run recorded in its meta. endpointUnknown: a remote ref, and the run has no meta. pinGone: the pin is not there any more; a pin that goes between the plan and the run is reported as kept, its reason starting pinGone. |
| `items[].selected` | boolean | yes | Whether the restore takes it: every row whose action is create or none, or exactly what --only names. |
| `items[].result` | `"restored"` \| `"kept"` \| `"failed"` \| `"notRun"` \| `"interrupted"` \| `"notSelected"` | yes | restored: the ref is at object and the pin is gone. kept: left alone on purpose (reason says). failed: tried and not finished. notRun: the run ended before it. interrupted: a signal caught it. notSelected: not selected. |
| `items[].reason` | string \| null | yes | Why, for kept and failed. |
| `items[].restored` | boolean | yes | The ref holds object, read back after the row. |
| `items[].pinDeleted` | boolean | yes | The pin is gone, read back after the row. |
<!-- json-docs:end -->

## `wt refs purge (<runId>… | --older-than <age>) --dry-run --json` — the plan

What `wt refs purge` would delete for good: the pins of the runs named, or of
every run older than `--older-than` (`14d`, `2w`, `36h`) by its `sweptAt`: the
meta's, else the time in its id. It deletes nothing. A run named that has
nothing under `refs/wt-swept/<runId>/` is an error; one with only its meta (a
run stopped before it moved anything) is purged like any other.

<!-- json-docs:begin refs-purge-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"refs purge"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `olderThan` | string \| null | yes | --older-than as given; null when runs were named. |
| `token` | string \| null, matching `^rp1-[0-9a-f]{32}$` | yes | rp1-…: names the plan; pass it to --yes --expect. It covers the runs, their pins, each pin's object and what deleting it would lose (unreachable). Null when there is nothing to purge, or on error. |
| `error` | string \| null | yes | Why no plan could be made. |
| `runs` | array of object | yes | The runs to purge, each with its runId, sweptAt, meta, endpoint, refs and unreachable. |
| `runs[].runId` | string, matching `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4,32}$` | yes | The run's id. |
| `runs[].sweptAt` | integer | yes | Unix seconds the run began: its meta's sweptAt, else the time in its id. Unix seconds: the meta's, else from the id. |
| `runs[].meta` | string \| null | yes | refs/wt-swept/<runId>/meta, deleted last; null when the run has none. The run's meta ref, or null. |
| `runs[].endpoint` | object \| null | yes | The origin the run's meta records; null for a local run or a run with no meta. As the meta records it, or null. |
| `runs[].endpoint.url` | string | yes | origin's one effective URL, fetch and push alike, insteadOf and pushInsteadOf applied, with all userinfo removed (git@host:o/r is host:o/r). |
| `runs[].endpoint.digest` | string, matching `^sha256:[0-9a-f]{64}$` | yes | sha256: and the lowercase hex SHA-256 of the effective URL's UTF-8 bytes. |
| `runs[].refs` | array of object | yes | The run's pins, each with its pin, id and object. |
| `runs[].refs[].pin` | string | yes | The pin ref under refs/wt-swept/. |
| `runs[].refs[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `runs[].refs[].object` | string, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The pin's object. |
| `runs[].unreachable` | array of object | yes | The pins whose commits no ref outside this purge and no worktree's HEAD reaches: what git gc may take once the pin goes. A commit two purged pins share counts for both; reflogs are not counted. |
| `runs[].unreachable[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `runs[].unreachable[].count` | integer, at least 1 | yes | How many commits nothing else holds. |
<!-- json-docs:end -->

## `wt refs purge … --yes --json` — the result

Each pin is deleted only while it holds `object`; once every pin of a run is
gone, the run's pins are read again and, with none left, its meta goes last, at
the value read. A pin that appeared meanwhile (a sweep still running under that
id) keeps the meta. The human output goes to
stderr; a handled SIGINT or SIGTERM writes the object too.

<!-- json-docs:begin refs-purge -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"refs purge"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `token` | string \| null, matching `^rp1-[0-9a-f]{32}$` | yes | The token of the plan the purge went by. |
| `outcome` | `"purged"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | purged: every pin planned is gone. refused: nothing was deleted (error says why). partial: some pins are gone, some not; running it again finishes it. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why the run stopped before deleting anything. |
| `recovery` | string \| null | yes | For interrupted: what wt printed. |
| `runs` | array of object | yes | Each run, with its runId, sweptAt, refs, meta and metaDeleted (read back); each ref has pin, id, object, result and reason. |
| `runs[].runId` | string, matching `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4,32}$` | yes | The run's id. |
| `runs[].sweptAt` | integer | yes | Unix seconds: the meta's, else from the id. |
| `runs[].refs` | array of object | yes | The run's pins, each with its pin, id, object, result and reason. |
| `runs[].refs[].pin` | string | yes | The pin ref under refs/wt-swept/. |
| `runs[].refs[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `runs[].refs[].object` | string, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The pin's object. |
| `runs[].refs[].result` | `"deleted"` \| `"absent"` \| `"kept"` \| `"notRun"` \| `"interrupted"` | yes | deleted: it was at object and is gone. absent: it was gone already. kept: it is still there (it moved, or git refused; reason says). notRun: the run ended before it. interrupted: a signal caught it and it is still there. |
| `runs[].refs[].reason` | string \| null | yes | Why, for kept: it moved, or git refused. |
| `runs[].meta` | string \| null | yes | The run's meta ref; null when it had none. |
| `runs[].metaDeleted` | boolean | yes | The meta is gone, read back. It is deleted only once every pin of the run is. |
<!-- json-docs:end -->

## `wt refs swept --json` — the runs

Every run with pins or a meta still under `refs/wt-swept/`, oldest first. A run
that wrote its meta and was stopped before it moved anything is listed with no
refs. It changes nothing.

<!-- json-docs:begin refs-swept -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"refs swept"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `error` | string \| null | yes | Why the runs could not be listed. |
| `runs` | array of object | yes | The runs under refs/wt-swept/, each with its runId, sweptAt, endpoint and refs. |
| `runs[].runId` | string, matching `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4,32}$` | yes | The run's id. |
| `runs[].sweptAt` | integer | yes | Unix seconds the run began: its meta's sweptAt, else the time in its id. Unix seconds: the meta's, else from the id. |
| `runs[].endpoint` | object \| null | yes | The origin the run's meta records; null for a local run or a run with no meta. As the meta records it, or null. |
| `runs[].endpoint.url` | string | yes | origin's one effective URL, fetch and push alike, insteadOf and pushInsteadOf applied, with all userinfo removed (git@host:o/r is host:o/r). |
| `runs[].endpoint.digest` | string, matching `^sha256:[0-9a-f]{64}$` | yes | sha256: and the lowercase hex SHA-256 of the effective URL's UTF-8 bytes. |
| `runs[].refs` | array of object | yes | Empty for a run that wrote its meta and moved nothing. |
| `runs[].refs[].id` | string | yes | refs/heads/<name> or refs/tags/<name> here; origin:refs/heads/<name> or origin:refs/tags/<name> on origin. --only takes it, one id per flag and never split: a ref name may contain a comma. |
| `runs[].refs[].kind` | `"branch"` \| `"tag"` \| `"remoteBranch"` \| `"remoteTag"` | yes | branch, tag, remoteBranch or remoteTag: a branch or tag here, or on origin. |
| `runs[].refs[].name` | string | yes | The short name of the ref. |
| `runs[].refs[].pin` | string | yes | The pin under refs/wt-swept/ that holds the object. |
| `runs[].refs[].object` | string, matching `^[0-9a-f]{40}([0-9a-f]{24})?$` | yes | The ref's own value: the tag object of an annotated tag, else the tip. |
<!-- json-docs:end -->

### `needs` — what a step cannot run without

A step under `defer:` or `each_commit:` may declare `needs: [docker]` or `needs: [env:<NAME>]`. Before
a run moves any branch or safety ref, it probes each need once (`docker`: `docker
info`, under a 10-second deadline), and only when a step with it may run for a
worktree the run goes ahead with: an `each_commit` step with a commit to check,
or a deferred step whose `paths` match a path the rebase may change (what trunk
changed since the branch's merge base, and what the branch's own commits
change; which deferred steps really run is only known after). A need that does
not answer refuses that worktree, and what sits on it in a stack, before
anything moves: `refused`, with the step, the need and the probe's answer in
`reason`; the members above it are `notRun`. With `--allow-owed` the run goes on:
a deferred step that needs it is `owed` in `deferred`, an `each_commit` step is
not run at any commit, and either is named in `failedSteps`, so the worktree is
`rebasedStepFailed` and not pushed. An `each_commit` step whose script the
replayed commit's tree predates (an in-place `--autosquash`, where the line
sits on an older trunk) is owed the same way at those commits, without
`--allow-owed`: named in `failedSteps`, `rebasedStepFailed`, not pushed.

`wt sync --json` reports the same probe, in its top-level `needs`, before
anyone starts.

## `wt sync --json` — the overview

What a rebase onto trunk would do to every worktree of the repository, as
`wt sync` prints it grouped. It fetches trunk first (`--no-fetch` compares with
trunk as last fetched; a fetch that fails is `fetchError`, and the overview goes
on), then simulates each rebase in the object store: it writes loose objects and
moves no ref. `wt sync <work> --json` is refused; the overview covers them all,
except with `--stack`: `wt sync <work> --stack --json` is the overview with
`stack` filled for that worktree's line, and with `--onto` (since 1.8.0):
`wt sync <work> --onto <ref> --json` is the overview of that one worktree
against the ref, which `onto`, `target` and `token` name.

`wt sync <work> --stack --autosquash --json` with no `--onto` (since 1.15.0)
also fills `fold`: the fold `wt sync rebase` makes in place on the line's own
base, which fixup folds into which commit and layer and which layers change.
That run never rebases onto trunk, so the worktrees' `class`, `behind`, `stops`
and `verdict` there still describe a rebase onto trunk it does not make; `fold`
is what it does. With `--onto`, and without `--autosquash`, `fold` is null.

Since 1.16.0 each worktree also carries `likelyStops` and `reworkedOnTrunk`, both
advice that changes no class and no verdict. `likelyStops` lists the commits after
the first stop that is yours that change a file trunk changed since the replayed
range began, each with the files it shares: a guess from the overlap alone, where
the simulation stopped looking. Git may merge any of them cleanly, and a file a
declared rule claims is listed too, since the rule may refuse the conflict there;
the run stops at the first stop that is yours whatever the list says.
`reworkedOnTrunk` names each replayed commit that trunk carries under the same
subject and author, with the trunk commit: a name match, not proof that trunk holds
the changes, so wt keeps and replays the commit. Both are empty when there is
nothing to say.

With `--all`, `--roots` or `--profile` stdout is an **array** of these objects,
one per repository, in the selection's order; `wt status --json` takes none of
those, so there is nothing to mirror. A repository that could not be read is an
object with `error` set and no worktrees, and the exit code is non-zero.

<!-- json-docs:begin sync -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows. |
| `command` | `"sync"` | yes |  |
| `repo` | string | yes | The main checkout, as wt sweep --json names the repository. |
| `name` | string | yes | The repository's name: its main checkout's directory name. |
| `trunk` | string \| null | yes | Trunk's name, such as main; null when nothing names trunk, and error says so. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | How trunk was determined, first match wins, from local refs alone (since 1.1.0): config — MAIN_BRANCH in the wt configuration file; originHead — refs/remotes/origin/HEAD; conventional — the first of development, main, master that exists locally or on origin (since 1.1.1; before, main, master, trunk, development, develop, ranked by where each exists); currentBranchGuess — no longer produced since 1.1.1: when nothing names trunk, detection fails and the repository reports noConfiguration. Null when trunk is. |
| `trunkRef` | string \| null | yes | origin/<trunk>, what every worktree is assessed against. Such as origin/main; null when nothing names trunk, and error says so. |
| `onto` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit assessed against, or rebased onto: trunk, or --onto's target (since 1.8.0). Null when it was never determined. Null when trunk is not known. |
| `target` | object \| null | yes | What onto is: trunk, or the ref --onto names (since 1.8.0). Null when it was never determined. Null when trunk is not known. |
| `target.ref` | string | yes | The ref as given to --onto, or origin/<trunk>. |
| `target.trunk` | boolean | yes | The target is trunk: no --onto was given. |
| `target.upstream` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit --upstream names: only the commits above it are replayed. Null without it. |
| `fetched` | boolean | yes | Trunk was fetched first. |
| `fetchError` | string \| null | yes | The fetch failed: the overview is against trunk as last fetched. |
| `declared` | boolean | yes | Trunk declares .wt-sync.yaml. Without it the worktrees are assessed but a run rebases none. |
| `token` | string \| null | yes | Names what a run would start on; pass it to wt sync rebase --expect. Since 1.3.0 a worktree refused only for having diverged from its own remote is in it too, with the remote commit, for a run under --allow-diverged. Null when a run would start on nothing. |
| `error` | string \| null | yes | Why there is no overview: trunk not known, the repository not readable. The exit code is then non-zero. |
| `sessionsError` | string \| null | yes | Agent sessions could not be listed; sessions are then empty, not known empty. |
| `eachCommitDeclared` | array of object | yes | Since 1.6.0. The each_commit checks .wt-sync.yaml declares on the trunk commit in onto, in the declared order: each runs after every replayed commit that changes a path its paths match, every commit when it lists none. Empty when trunk declares none. |
| `eachCommitDeclared[].step` | string | yes | Its run line: the string deferred[].step carries for it in wt sync rebase --json. |
| `eachCommitDeclared[].paths` | array of string | yes | The globs that gate it, as declared. For a deferred step: empty, it runs after every finished rebase; otherwise only when the rebase changed a path one of them matches, which is not known before the rebase. For an each_commit step: empty, it runs at every replayed commit; otherwise at each one changing a path one of them matches. |
| `eachCommitDeclared[].needs` | array of string, matching `^(docker\|env:[A-Za-z_][A-Za-z0-9_]*)$` | yes | Since 1.6.0 (env:<NAME> since 1.10.0). What the step cannot run without, as declared; the overview's needs says what each probe answered. |
| `needs` | array of object | yes | Since 1.6.0. Every need a declared step has, once each, with what its probe said. A need is probed only when a step with it would run for some worktree of the overview: an each_commit step with a commit to check, or a deferred step whose paths match a path the rebase may change. A run probes the same way before it moves anything, and refuses a worktree whose step needs one that did not answer, unless started with --allow-owed. |
| `needs[].need` | string, matching `^(docker\|env:[A-Za-z_][A-Za-z0-9_]*)$` | yes | A declared need: docker, or env:<NAME> for an environment variable that must be set and not empty (since 1.10.0). |
| `needs[].probed` | boolean | yes | The probe ran: some worktree has a step with this need that would run. |
| `needs[].ok` | boolean | yes | The probe answered. False when it did not, or was not run. |
| `needs[].error` | string \| null | yes | Why the probe failed; null when it answered or was not run. |
| `worktrees` | array of object | yes | Every worktree but the main checkout, in git's order. |
| `worktrees[].work` | string | yes | The work name, as wt list prints it. |
| `worktrees[].branch` | string \| null | yes | Null when detached. |
| `worktrees[].path` | string | yes | The worktree's absolute path. |
| `worktrees[].group` | `"ready"` \| `"needsYou"` \| `"skipped"` \| `"current"` | yes | The overview's group. current is on trunk already, which the human overview leaves out. Since 1.3.0 a worktree whose class is current or stale only at its remote's commit, which a run fast-forwards it to, is ready. |
| `worktrees[].class` | `"conflict-free"` \| `"recipe"` \| `"contested"` \| `"divergent"` \| `"stale"` \| `"current"` \| `"detached"` \| `"unknown"` | yes | What the simulated rebase found; unknown when the assessment failed (error). |
| `worktrees[].verified` | boolean | yes | false is the overview's question mark (recipe?): a script owns a path and the replay could not be carried past it. |
| `worktrees[].verdict` | `"proceed"` \| `"skip"` \| `"refuse"` | yes | What wt sync rebase <work> would do: start on it, skip it, or refuse it untouched. A contested one is rebased up to its stop and handed over. |
| `worktrees[].runnable` | boolean | yes | wt sync rebase, with nothing named, takes it. |
| `worktrees[].reason` | string \| null | yes | Why it is not runnable; null when it is. |
| `worktrees[].behind` | integer | yes | Commits it lacks against trunk. |
| `worktrees[].ahead` | integer | yes | Commits it has that trunk lacks. |
| `worktrees[].dirty` | boolean | yes | Tracked changes. |
| `worktrees[].handedOver` | boolean | yes | An earlier run left a conflict here for a person: wt sync resume or wt sync undo. |
| `worktrees[].planFile` | string \| null | yes | The plan file of a handover. |
| `worktrees[].sessions` | array of object | yes | The agent sessions in it, each with work, name, kind and state. |
| `worktrees[].sessions[].work` | string | yes | The work name of the worktree it is in. |
| `worktrees[].sessions[].name` | string | yes | The session's name. |
| `worktrees[].sessions[].kind` | `"claude"` \| `"codex"` | yes | Since 1.2.0 also codex: a Codex session. |
| `worktrees[].sessions[].state` | `"busy"` \| `"idle"` | yes | busy or idle; a busy session keeps a run off the worktree. |
| `worktrees[].stack` | array of object | yes | Every worktree a run on this one would move, parents first; just this one without a stack; empty when detached. |
| `worktrees[].stack[].work` | string | yes |  |
| `worktrees[].stack[].branch` | string | yes |  |
| `worktrees[].stack[].path` | string | yes |  |
| `worktrees[].stops` | array of object | yes | Every stop the simulated rebase reached, in order. |
| `worktrees[].stops[].index` | integer, at least 1 | yes | The commit's place in the replay: with total, 1/3. |
| `worktrees[].stops[].total` | integer, at least 1 | yes | How many commits the replay has; with index, 1/3. |
| `worktrees[].stops[].commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit replayed there. |
| `worktrees[].stops[].subject` | string | yes |  |
| `worktrees[].stops[].resolved` | boolean | yes | Every conflicted file here was resolved by a strategy. |
| `worktrees[].stops[].yours` | boolean | yes | The first stop no strategy resolves: a run hands it to a person. |
| `worktrees[].stops[].files` | array of object | yes | The conflicted files at the stop, each with path, resolved, strategy and note. |
| `worktrees[].stops[].files[].path` | string | yes |  |
| `worktrees[].stops[].files[].resolved` | boolean | yes |  |
| `worktrees[].stops[].files[].strategy` | string \| null | yes | The strategy that claims it; null when none does. |
| `worktrees[].stops[].files[].note` | string \| null | yes | Why a strategy refused it, or what a lift did. |
| `worktrees[].likelyStops` | array of object | yes | Since 1.16.0. The commits after the first stop that is yours that change a file trunk changed since the replayed range began, in replay order; a file a declared rule claims is listed too. Guessed from the overlap alone, not simulated: git may merge any of them cleanly, and the run stops at the first stop that is yours whatever this says. Empty when the replay has no stop that is yours. |
| `worktrees[].likelyStops[].index` | integer, at least 1 | yes | The commit's place in the replay: with total, 5/9. |
| `worktrees[].likelyStops[].total` | integer, at least 1 | yes |  |
| `worktrees[].likelyStops[].commit` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].likelyStops[].subject` | string | yes |  |
| `worktrees[].likelyStops[].paths` | array of string | yes | The files it changes that trunk changed too. |
| `worktrees[].reworkedOnTrunk` | array of object | yes | Since 1.16.0. The replayed commits that trunk carries under the same subject and author since the range began, a patch of its own that git does not drop. A name match does not prove trunk holds the changes: wt keeps and replays each of these, and drops one only as git does, when its changes are already there. |
| `worktrees[].reworkedOnTrunk[].index` | integer, at least 1 | yes |  |
| `worktrees[].reworkedOnTrunk[].total` | integer, at least 1 | yes |  |
| `worktrees[].reworkedOnTrunk[].commit` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit on the branch. |
| `worktrees[].reworkedOnTrunk[].subject` | string | yes |  |
| `worktrees[].reworkedOnTrunk[].trunkCommit` | string, matching `^[0-9a-f]{40,64}$` | yes | The trunk commit it matches. |
| `worktrees[].reworkedOnTrunk[].trunkSubject` | string | yes |  |
| `worktrees[].strategies` | array of string | yes | The declared strategies that resolved a file at some stop, each once. |
| `worktrees[].deferredDeclared` | array of object | yes | Since 1.4.0. The deferred steps .wt-sync.yaml declares on the trunk commit in onto, in the declared order: what a run goes through once it has rebased this worktree, itself or through wt sync resume after a handover. Declared, not predicted: each may run. A step with no paths does, after every finished rebase; one with paths only when the rebase changed one of them; none after a step that failed. The same list on every worktree, also one a run would skip or refuse, where nothing is rebased and so none runs: verdict says which. Empty when trunk declares none or has no .wt-sync.yaml. |
| `worktrees[].deferredDeclared[]` | object | yes | Fields as at `eachCommitDeclared[]`. |
| `worktrees[].checks` | integer, at least 0 | yes | Since 1.6.0. How many of the branch's commits a run would check with the each_commit steps: those changing a path a step's paths match. Nothing is run to say so. A commit a resolution leaves with no matching change is not checked by the run, so this is an upper bound. It is 0 for a worktree a run would not rebase. |
| `worktrees[].checkCommits` | array of object | yes | Since 1.6.0. The commits checks counts, oldest first, as they are on the branch now. |
| `worktrees[].checkCommits[].commit` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].checkCommits[].subject` | string | yes |  |
| `worktrees[].notes` | array of string | yes | Advisory; never change the class. |
| `worktrees[].error` | string \| null | yes | The assessment failed. |
| `worktrees[].ownRemote` | object | yes | Since 1.3.0. A worktree a run would refuse for it has verdict refuse, runnable false and the reason in words; its group is needsYou, or skipped when a busy session is in it, whatever blocks says; class stays about trunk. For a branch that is behind and will be fast-forwarded, class, behind, ahead and stops are those of the remote commit: what the run rebases. When that class is current or stale there is nothing to rebase: the row is verdict proceed, runnable true, group ready, and a run fast-forwards the branch and is done. |
| `worktrees[].ownRemote.ref` | string \| null | yes | The remote-tracking ref, as remote/branch (origin/feat/x); null only when no ref could be named: for none, and for unknown unless it is a ref that could not be compared. |
| `worktrees[].ownRemote.state` | `"none"` \| `"gone"` \| `"unknown"` \| `"inSync"` \| `"ahead"` \| `"behind"` \| `"rebased"` \| `"diverged"` | yes | none: nothing to check: the ref is trunk's, or does not exist. gone: the branch names a ref the remote no longer has. unknown: where a push lands cannot be read from a tracking ref (a mirror remote, several push URLs, a push URL that is another repository than the fetch URL), or the branches or the remotes cannot be read at all, or the two tips cannot be compared; a run goes on unchecked. Since 1.5.0 also a branch a run would rebase and not push: noPushReason is then non-null. inSync: the same commit. ahead: only the branch has commits of its own. behind: only the remote has; a run fast-forwards the branch first. rebased: both have, and the remote's are old versions of the branch's (its tip is a tip the branch once had, or every commit only it has is matched patch for patch); a run goes on. diverged: both have, otherwise; a run refuses unless --allow-diverged. |
| `worktrees[].ownRemote.commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The remote commit compared with; null for none, gone and unknown. |
| `worktrees[].ownRemote.ahead` | integer \| null, at least 0 | yes | Commits only the branch has; null for none, gone and unknown. |
| `worktrees[].ownRemote.behind` | integer \| null, at least 0 | yes | Commits only the remote has; null for none, gone and unknown. |
| `worktrees[].ownRemote.fetched` | boolean | yes | true: the remote side is from this command's fetch. false: as last fetched (--no-fetch, trunk's fetch or this branch's own failed, or a remote whose fetch refspec is not the standard one). |
| `worktrees[].ownRemote.blocks` | `"diverged"` \| `"operation"` \| `"dirty"` \| `"session"` \| `null` | yes | Why a run would refuse for it; non-null only when a run would. diverged with state diverged, for a branch a run would rebase: --allow-diverged goes on. operation, dirty, session with state behind: it cannot be fast-forwarded, because a git operation is in progress there, the checkout has tracked changes, or an agent session is busy in it. |
| `worktrees[].ownRemote.noPushReason` | string \| null | yes | Since 1.5.0: why a run would rebase the branch and not push it, in words; null when a run would push it, and for state none. It tracks a remote branch of another name and nothing is recorded for it (state unknown, fixCommand settles it); what is recorded for it cannot be used (state unknown); or it is recorded as pushing to a branch of another name that has commits it never had, is at a commit it was never at, or cannot be compared with it (state diverged, behind, rebased or unknown). See docs/json.md. |
| `worktrees[].ownRemote.fixCommand` | array of string \| null, at least 1 items | yes | Since 1.5.0: the command that settles noPushReason, as an argv, when one does: wt sync push-to <branch> <remote>/<branch>, which records the branch it tracks as where it pushes. The same command with the branch's own name on the remote records that instead. It names no directory: run it in the repository. Null otherwise. |
| `stack` | object \| null | yes | Since 1.7.0: the stack held on the line of the worktree wt sync <work> --stack names: its layers and what holds each. Null without --stack. |
| `stack.branch` | string | yes | The line's own branch: the worktree named. |
| `stack.base` | string, matching `^[0-9a-f]{40,64}$` | yes | Where the line leaves trunk: the merge base. |
| `stack.head` | string, matching `^[0-9a-f]{40,64}$` | yes | The line's tip before the run. |
| `stack.autosquash` | boolean | yes | A fold of the line's fixups (--autosquash): onto its own base, or, with --onto, as the line moves onto the target. |
| `stack.layers` | array of object | yes |  |
| `stack.layers[].branch` | string | yes | The layer branch. |
| `stack.layers[].oldTip` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip before the run. |
| `stack.layers[].commits` | integer, at least 1 | yes | Its commits from the line's base, its tip included. |
| `stack.layers[].subject` | string | yes | Its tip's subject. |
| `stack.layers[].foundBy` | `"tipOnLine"` \| `"named"` | yes | tipOnLine: a local branch whose tip is a commit of the line. named: named with --layer, by name or by a pattern. |
| `stack.layers[].worktree` | string \| null | yes | The worktree the branch is checked out in; null when it is in none. |
| `stack.layers[].state` | `"none"` \| `"clean"` \| `"dirty"` \| `"rebasing"` \| `"bisecting"` \| `"busy"` \| `"idle"` \| `"process"` \| `"unknown"` | yes | What holds the layer's worktree: none (in no worktree), clean (nothing), dirty (tracked changes), rebasing (a rebase in progress, there or in another worktree that will move it), bisecting (a bisect in another worktree started from it), busy or idle (an agent session), process (a process whose working directory is inside the worktree), unknown (it could not be read, which holds the layer). In a run, as read when the layer was moved. |
| `stack.layers[].stateDetail` | string \| null | yes | Who or what holds it: the session, the process, or why it could not be read. |
| `stack.layers[].newTip` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The run's: the layer's new tip, read from the rebase and checked. Null in the look, and when the run moved no layer. |
| `stack.layers[].commitsAfter` | integer \| null, at least 0 | yes | The run's: commits of the new tip from the new base, as the check compared them with the plan. |
| `stack.layers[].mapped` | `"tip"` \| `"tipDropped"` \| `"emptied"` \| `null` | yes | The run's: tip (the new tip is its own tip rebased), tipDropped (its tip was left out because trunk already carries it, dropped as empty, or folded into an earlier commit by --autosquash; the new tip is its last commit left), emptied (every commit of it was left out because trunk carries it, dropped as empty, or folded into an earlier commit by --autosquash; it ends where the layer below does). |
| `stack.layers[].patch` | `"same"` \| `"changed"` \| `"unknown"` \| `null` | yes | The run's: whether what the layer brings to the line is the same patch as before, by git's stable patch-id. Never a refusal. |
| `stack.layers[].patchWhy` | string \| null | yes | The run's: why the patch changed. |
| `stack.layers[].result` | `"moved"` \| `"held"` \| `"unchanged"` \| `"planned"` \| `"moving"` \| `"needsRecovery"` \| `null` | yes | The run's: moved (the branch is at newTip, its worktree reset with it), held (left where it was; held says why and moveCommand moves it by hand), unchanged (its new tip is its old one), planned (recorded and not moved: the moves stopped before it), moving (its move was under way when the moves stopped: undo puts its ref back), needsRecovery (a move that went part way and could not be put back: held says what to look at, the layer keeps its pins, and wt sync undo puts it back). |
| `stack.layers[].how` | `"ref"` \| `"worktree"` \| `null` | yes | The run's: ref (a branch in no worktree, moved with update-ref guarded by its old tip), worktree (in its worktree, under that worktree's lock: the same guarded update-ref, then its files follow with read-tree -m -u). |
| `stack.layers[].held` | string \| null | yes | The run's: why the layer was held, or for needsRecovery what to look at. |
| `stack.layers[].moveCommand` | string \| null | yes | The run's: what moves a held layer by hand. |
| `stack.layers[].pushCommand` | array of string \| null, at least 1 items | yes | The run's: for a moved layer, its push to its own remote as an argv, with a lease on the commit wt last fetched there. A run does not push layers. Null otherwise. |
| `stack.layers[].noPushReason` | string \| null | yes | The run's: for a moved layer with no safe push command, why: no remote branch of its own yet, or the remote has commits the branch never had. |
| `stack.layers[].note` | string \| null | yes | The run's: what else happened to a moved layer, such as a checkout of it that landed during its move, at its new tip. |
| `stack.top` | object | yes | Since 1.13.0: the line's own branch, the top of the stack: the last row of the look's layer table. Not one of layers, which are the branches a run moves after its rebase; the rebase itself moves the top. |
| `stack.top.branch` | string | yes | The line's own branch. |
| `stack.top.oldTip` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip before the run: head. |
| `stack.top.commits` | integer, at least 0 | yes | Its commits from the line's base, its tip included; 0 for a branch with nothing ahead of trunk. |
| `stack.top.subject` | string | yes | Its tip's subject. |
| `stack.top.worktree` | string \| null | yes | The worktree it is checked out in. |
| `stack.skipped` | array of object | yes |  |
| `stack.skipped[].branch` | string | yes |  |
| `stack.skipped[].tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `stack.skipped[].reason` | string | yes |  |
| `stack.sideBranches` | array of object | yes | Since 1.12.0: the local branches that hang off the line, parents first: forked from a commit of it above its base, or above its top. Empty with --no-side-branches. |
| `stack.sideBranches[].branch` | string | yes |  |
| `stack.sideBranches[].kind` | `"forked"` \| `"above"` | yes | forked: it leaves the line at a commit below its top. above: it carries the line's top. |
| `stack.sideBranches[].oldTip` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip before the run. |
| `stack.sideBranches[].fork` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit of the line it leaves the line at: its merge base with the line's tip. |
| `stack.sideBranches[].forkSubject` | string | yes |  |
| `stack.sideBranches[].on` | string \| null | yes | The side branch it is stacked on, which moves first; null when it sits on the line itself. A backup is never what a branch is on, and neither is a branch at its own tip (see sameTip). On branches at one tip, it names the one that stands for them: one with an upstream, then the oldest, then the first by name. |
| `stack.sideBranches[].base` | string, matching `^[0-9a-f]{40,64}$` | yes | Where its own commits start: the tip of the branch it is on, or fork. |
| `stack.sideBranches[].commits` | integer, at least 0 | yes | Its own commits, from base to its tip. A branch at the same tip as another is never on it: both count the same commits. |
| `stack.sideBranches[].backup` | boolean | yes | Its name is a backup (wt refs sweep's patterns): listed, never moved. |
| `stack.sideBranches[].worktree` | string \| null | yes | The worktree it is checked out in; null when it is in none. |
| `stack.sideBranches[].state` | `"none"` \| `"clean"` \| `"dirty"` \| `"rebasing"` \| `"bisecting"` \| `"busy"` \| `"idle"` \| `"process"` \| `"unknown"` | yes | What holds its worktree, as for a layer. |
| `stack.sideBranches[].stateDetail` | string \| null | yes |  |
| `stack.sideBranches[].carry` | boolean | yes | The look's: the run would carry it. False for a backup, a branch with a merge commit or on more than one branch, one whose worktree something holds, one stacked on a branch that is not carried, and every one under --alone. |
| `stack.sideBranches[].left` | string \| null | yes | Why it would not be carried; null when it would. |
| `stack.sideBranches[].newBase` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The run's: where it goes: for a branch forked mid-line, where the rebase put the commit it forks from, read from the run's own marker and checked as a layer's is; above the top, the line's new tip; on another side branch, that branch's new tip. Null when it is not known. |
| `stack.sideBranches[].newTip` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The run's: its own commits replayed onto newBase. Null when it was not replayed. |
| `stack.sideBranches[].result` | `"moved"` \| `"held"` \| `"unchanged"` \| `"planned"` \| `"moving"` \| `"needsRecovery"` \| `null` | yes | The run's, as for a layer. A backup is always held, with no moveCommand. |
| `stack.sideBranches[].how` | `"ref"` \| `"worktree"` \| `null` | yes |  |
| `stack.sideBranches[].held` | string \| null | yes | The run's: why it was held. |
| `stack.sideBranches[].moveCommand` | string \| null | yes | The run's: for a held branch, the command that moves it later, with real commits and branch names shell-quoted: wt sync rebase '<branch>' --onto <newBase> --upstream <base> for a branch in a worktree, git -C '<main checkout>' rebase --no-update-refs --onto <newBase> <base> '<branch>' for one in the main checkout, git rebase --no-update-refs --onto <newBase> <base> '<branch>' for one in none; for a branch on a held side branch, --onto names that branch, to run once it has moved. Null for a backup, for a branch with a merge commit of its own (move it by hand), and when where it goes is not known. |
| `stack.sideBranches[].sameTip` | array of string | yes | Since 1.13.0: the other side branches at its very tip, by name: siblings, carried or held together onto one new commit. Empty for most. |
| `stack.refusal` | string \| null | yes | The run's: why no layer moved: a new tip failed its check, or the fold changed the line's content. Null in the look, and when each layer was moved or held. |
| `stack.edit` | object \| null | yes | Since 1.9.0: what a run with --edit did to the commit it stopped at. Null in the look and for any other run. |
| `stack.edit.commit` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit as the line had it. |
| `stack.edit.subject` | string | yes | Its subject. |
| `stack.edit.amended` | string, matching `^([0-9a-f]{40,64})?$` | yes | What the run went on from at the edit stop: the amended commit, or the last commit added there; empty if the run never went on from it. |
| `stack.edit.paths` | array of string | yes | The paths the amend changed, sorted; empty when it changed nothing. |
| `stack.edit.emptied` | array of object | yes | The commits above it that the amend emptied, which the rebase dropped, oldest first. |
| `stack.edit.emptied[].sha` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit as the old line had it. |
| `stack.edit.emptied[].subject` | string | yes |  |
| `stack.contextGaps` | array of object | yes | Since 1.11.0: for a fold (--autosquash), in the look and in the run, every hunk of a fixup that does not apply behind its target, where autosquash puts it, because only a later commit of the line carries its context; line order. Empty for any other look or run. A warning: the run goes ahead. |
| `stack.contextGaps[].fixup` | string, matching `^[0-9a-f]{40,64}$` | yes | The fixup commit, as the line has it. |
| `stack.contextGaps[].fixupSubject` | string | yes |  |
| `stack.contextGaps[].target` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit autosquash folds it into. |
| `stack.contextGaps[].targetSubject` | string | yes |  |
| `stack.contextGaps[].path` | string | yes | The hunk's file. |
| `stack.contextGaps[].lines` | string, matching `^[0-9]+(-[0-9]+)?$` | yes | The lines the hunk changes in the fixup's own file: 13, or 13-15. |
| `stack.contextGaps[].needs` | string, matching `^[0-9a-f]{40,64}$` | yes | The first commit after the target, in line order, that carries the hunk's context. |
| `stack.contextGaps[].needsSubject` | string | yes |  |
| `stack.contextGaps[].message` | string | yes | The line wt prints for it: fixup moved ahead of its context: <path>:<lines> needs <sha> "<subject>", which comes later on the line. |
| `stack.contextUnchecked` | array of string | yes | Since 1.11.0: for a fold, what of its fixups could not be analysed, one line each: a fixup whose missing context cannot be told apart from another fixup's, because it is in an earlier fixup of the same target that conflicts or in a fixup of another commit that does not fold clean, or the analysis failing as a whole; each such line counts the files and hunks at risk and the later commits that add their context. Empty when every fixup was analysed and for any other look or run. |
| `stack.push` | object \| null | yes | Since 1.14.0: for a --stack run that pushed (--push, or yes to the question), what came of the push: every moved layer and the line's own branch that had somewhere safe to push, in one atomic push per remote, each over a lease on the commit the run recorded for its remote branch before it moved anything. Null in the look, and for a run that pushed nothing: with --no-push, a no to the question, or nobody to ask. |
| `stack.push.branches` | array of object | yes | Each branch that was in a push, in this order whatever the remotes: moved layers bottom first, then the line's own branch. A held layer, and a branch with nowhere safe to push, are not in it: layers[] names them (held, noPushReason). |
| `stack.push.branches[].branch` | string | yes | The local branch. |
| `stack.push.branches[].remote` | string | yes | The remote it was pushed to. |
| `stack.push.branches[].remoteBranch` | string | yes | Its name on the remote. |
| `stack.push.branches[].old` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The lease: the commit the remote branch had to be at, as last fetched before the run moved anything. Null for a branch that must not exist on the remote yet. |
| `stack.push.branches[].new` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit pushed: the branch's new tip. |
| `stack.push.branches[].outcome` | `"pushed"` \| `"leaseFailed"` \| `"notSent"` \| `"unknown"` | yes | pushed: the remote branch is at new. leaseFailed: git rejected it as stale: the remote branch was not at old any more, somebody pushed to it since; this is the branch that moved under the push. Any other refusal (a hook, a policy) is notSent, with git's own reason in why. notSent: nothing on its remote moved: another branch of the same push failed its lease, or the remote refused the push as a whole (a remote that does not accept --atomic is refused, never pushed branch by branch). unknown: the push ended in a transport failure (a timeout, a dropped connection) and the remote's own refs, read back, show the branch at neither old nor new, or could not be read: whether it moved is not known. A transport failure whose read-back shows the branch at new is pushed, and at old notSent. |
| `stack.push.branches[].why` | string \| null | yes | For an outcome other than pushed, what git said. Null for pushed. |
| `fold` | object \| null | yes | Since 1.15.0: for --stack --autosquash with no --onto, the fold a run makes in place on the line's own base: which fixup folds into which commit and layer, and which layers change. The worktrees' class, behind, stops and verdict still describe a rebase onto trunk, which that run does not make. Null for any other look, and with --onto. |
| `fold.base` | string, matching `^[0-9a-f]{40,64}$` | yes | The line's own base, which the fold stays on: stack.base. |
| `fold.refusal` | string \| null | yes | Why the run refuses the fold before it moves anything, in the run's own words: a fixup whose name finds no commit or more than one, one for a commit trunk or the line's base already carries, or a declaration the line cannot take. Null when it does not. When it is set, folds, unchanged, repointed and held are all empty. |
| `fold.folds` | array of object | yes | Every fixup!, squash! or amend! commit of the line with the commit it folds into, oldest fixup first. A fixup whose name finds no commit, or more than one, is left out: the run refuses it. |
| `fold.folds[].fixup` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `fold.folds[].fixupSubject` | string | yes |  |
| `fold.folds[].fixupLayer` | string | yes | The layer branch the fixup is a commit of, or the line's own branch above the top layer. |
| `fold.folds[].target` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `fold.folds[].targetSubject` | string | yes |  |
| `fold.folds[].targetLayer` | string | yes | The layer branch the target is a commit of, or the line's own branch above the top layer. |
| `fold.unchanged` | array of string | yes | The layers whose commits all lie below every fold target: the fold leaves them as they are. All layers when there is no fold, none with --alone. |
| `fold.repointed` | array of string | yes | The layers that get new commits, bottom first: not those in held. |
| `fold.held` | array of object | yes | The layers the fold would change that the run leaves on their old commits: one a worktree holds (dirty, a process, a rebase), or every layer with --alone. |
| `fold.held[].branch` | string | yes |  |
| `fold.held[].why` | string | yes |  |
<!-- json-docs:end -->

A worktree its own remote refuses (`ownRemote.blocks` set, or its fetch failed)
has `verdict` `refuse`, `runnable` false and `reason` in words. Its `group` is
`needsYou`, except with a busy session in it, whatever its `blocks`, which is
`skipped` like any worktree a busy session is in. `class` is unchanged: it
stays about trunk.

That is the worktree's own verdict. Another member of its `stack` is not
refused with it in the overview: it keeps its own `verdict`, and has `runnable`
false with the `reason` `stacked with <work>`, as for any member a run would
not start on. A run refuses the whole stack.

A worktree that is behind its own remote and has nothing to rebase at the
remote's commit is one a run acts on: `class` `current` or `stale` (the remote
commit's), `verdict` `proceed`, `runnable` true, `group` `ready`, `reason` null,
`ownRemote.state` `behind` with `blocks` null. `wt sync rebase <work>`, a run with
nothing named and `--if-ready` all take it, fast-forward it and report
`fastForwarded`. Without that `ownRemote` a `current` row is `group` `current`
and a `stale` one `skipped`, as before.

The **token** covers the trunk's name, the wt configuration, the `.wt-sync.yaml`
on trunk, and every worktree whose `verdict` is `proceed`: its branch, class,
`verified`, `runnable` and stack. A newer trunk commit is not in it, but what it
changes about those is: a worktree that stops being ready after a fetch changes
the token. Nor is the commit of a branch's own remote, so a remote that moved
from `inSync` to `behind` changes nothing: the run fast-forwards. The one
exception, since 1.3.0: the remote commit of every worktree whose
`ownRemote.blocks` is `diverged` is in the token, whatever else keeps a run off
it (a busy session `--force` would lift, say), and one refused for nothing but
that is in it as a worktree too, since `--allow-diverged` starts on it. So
`wt sync rebase <work>… --if-ready --allow-diverged --expect <token>` goes ahead
over exactly the diverged rows shown, and an overview with nothing diverged
has the token an older wt gives it.

## `wt sync rebase|resume|undo --json` — the result

`wt sync rebase [<work>...] --json`, `wt sync resume <work> --json` and `wt sync undo <work> --json` do
what they do without it, and print one object in the shape of `wt up --json`'s,
with more said about each worktree. Progress, and any question, go to
**stderr**; stdout carries exactly that object, also when a handled SIGINT or
SIGTERM ends the verb (exit 130 for SIGINT, 143 for SIGTERM). `--all`, `--roots`
and `--profile` are refused with `--json`: a run across repositories has no
single result.

To run unattended: a run with nothing named, and no terminal, rebases nothing
without `--yes` (it is then `refused` with `error` saying so), as without
`--json`. `--yes` says yes to every question, the push included; `--no-push`
keeps the push out, on `rebase` and `resume` (undo never pushes). A named worktree
goes ahead without `--yes`, and with no terminal nothing is asked.

With `--expect <token>`, `wt sync rebase` fetch, recompute
the overview's token and refuse, touching no worktree, when it differs: what a run
would start on is not what `wt sync --json` showed. It is then an `error` with
no participants and a non-zero exit. The fetch then covers every worktree's own
remote, as the overview's did.

The fields are `wt up --json`'s (above), with `command` one of `"sync rebase"`,
`"sync resume"`, `"sync undo"`, `"sync steps"` (since 1.7.0: `wt sync steps
<work> --json`, a run in place onto the worktree's own base that runs the
deferred steps and rebases nothing onto trunk) (`"sync run"` is what the verb called itself
before `sync-run` 1.3.0; no wt prints it since 1.4.1, and the schema keeps the
value for results saved before that), and:

<!-- json-docs:begin sync-run -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows. |
| `command` | `"sync rebase"` \| `"sync run"` \| `"sync resume"` \| `"sync undo"` \| `"sync steps"` \| `"sync push"` | yes | The verb. sync push since 1.22.0: the push of a --stack line rebased earlier with --no-push; its one worktree row carries stack.push, and result pushed when anything went. sync steps since 1.7.0. sync rebase since 1.3.0: before it the verb was spelled wt sync run and named itself sync run. No wt prints sync run since 1.4.1; the value stays for results saved before it. |
| `repo` | string \| null | yes | The main checkout, as wt sweep --json names the repository; null when the verb stopped before reading it. |
| `trunk` | string \| null | yes |  |
| `trunkRef` | string \| null | yes |  |
| `onto` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit assessed against, or rebased onto: trunk, or --onto's target (since 1.8.0). Null when it was never determined. For resume, the one the handed-over run recorded; null for undo. |
| `target` | object \| null | yes | What onto is: trunk, or the ref --onto names (since 1.8.0). Null when it was never determined, and for undo. For resume, what the handed-over run rebased onto. |
| `target.ref` | string | yes | The ref as given to --onto, or origin/<trunk>. |
| `target.trunk` | boolean | yes | The target is trunk: no --onto was given. |
| `target.upstream` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit --upstream names: only the commits above it are replayed. Null without it. |
| `fetched` | boolean | yes |  |
| `trunkSync` | object \| null | yes | What the run did to local trunk after its fetch, as for wt up. Null when it did not fetch, and always for resume and undo. |
| `trunkSync.local` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | Local trunk after the run (as found unless fast-forwarded); null when there is none. |
| `trunkSync.remote` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The origin/<trunk> commit compared with; null when there is none. |
| `trunkSync.localAhead` | integer \| null, at least 0 | yes | Commits local trunk has that origin lacks, as found. |
| `trunkSync.remoteAhead` | integer \| null, at least 0 | yes | Commits origin has that local trunk lacks, as found. |
| `trunkSync.fastForwarded` | boolean | yes |  |
| `trunkSync.skippedReason` | `"optedOut"` \| `"ahead"` \| `"diverged"` \| `"dirty"` \| `"operation"` \| `"session"` \| `"notFastForward"` \| `"failed"` \| `null` | yes | Why local trunk was left; null when fast-forwarded or with nothing to do. |
| `outcome` | `"done"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | done: every participant rebased, fastForwarded, undone or skipped. refused: nothing changed, or error stopped the verb first. partial: something changed and not everything finished. interrupted: a signal ended it. |
| `error` | string \| null | yes | Why the verb stopped before touching any participant, as for wt up; also a question answered no (or nobody there to answer it), and for undo, why it stopped after putting some back. |
| `worktrees` | array of object | yes | The participants, parents first. |
| `worktrees[].work` | string | yes | The work name, as wt list prints it. |
| `worktrees[].branch` | string | yes | The worktree's branch. |
| `worktrees[].path` | string | yes | The worktree's absolute path. |
| `worktrees[].result` | `"rebased"` \| `"fastForwarded"` \| `"rebasedStepFailed"` \| `"skipped"` \| `"refused"` \| `"notRun"` \| `"restored"` \| `"needsRecovery"` \| `"handedOver"` \| `"interrupted"` \| `"undone"` \| `"pushed"` | yes | pushed (since 1.22.0, wt sync push only): the recorded branches were sent, at least one, and nothing was rebased; refused is a push that moved nothing. rebased, fastForwarded, rebasedStepFailed, needsRecovery, handedOver, interrupted and undone changed the branch or worktree; skipped, refused, notRun and restored left them as they were. fastForwarded (since 1.2.0): the branch was behind its own remote and has nothing to rebase at the remote's commit, so the run moved it there and that is all; after is that commit, pushCommand is null, undoCommand puts it back. |
| `worktrees[].reason` | string \| null | yes |  |
| `worktrees[].before` | string \| null, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].after` | string \| null, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].failedSteps` | array of string | yes |  |
| `worktrees[].recovery` | string \| null | yes |  |
| `worktrees[].pushCommand` | array of string \| null, at least 1 items | yes | For rebased: the push, as an argv. Since 1.4.0 null also for rebased, when wt has no push to make or offer for the branch, which noPushReason explains. |
| `worktrees[].pushed` | boolean | yes |  |
| `worktrees[].noPushReason` | string \| null | yes | Since 1.4.0: for rebased with a null pushCommand, why wt made and offered no push for the branch, in words, whatever the reason: among them that it tracks a remote branch of another name with nothing recorded for it, and that the branch of another name it is recorded as pushing to holds commits that cannot be shown to be old versions of its own. Null otherwise. |
| `worktrees[].fixCommand` | array of string \| null, at least 1 items | yes | Since 1.4.0: the command that settles noPushReason, as an argv, when one does: wt sync push-to <branch> <remote>/<branch>, which records the branch it tracks as where it pushes. The same command with the branch's own name on the remote records that instead. It names no directory: run it in the repository. Null otherwise. |
| `worktrees[].safetyRef` | string \| null | yes | The ref pinning the branch's tip from before the run: refs/wt-sync/<branch>/<epoch>. |
| `worktrees[].deferred` | array of object | yes | What each deferred step of a finished rebase came to: step (its run line), result, reason (why failed, skipped or owed) and commit (for committed). |
| `worktrees[].deferred[].step` | string | yes | The step's run line from .wt-sync.yaml. |
| `worktrees[].deferred[].result` | `"done"` \| `"committed"` \| `"failed"` \| `"skipped"` \| `"owed"` | yes | owed since 1.5.0: not run, because a need of it did not answer its probe in a run started with --allow-owed; owed like a failed step. |
| `worktrees[].deferred[].reason` | string \| null | yes | Why the step failed, was skipped or is owed. |
| `worktrees[].deferred[].commit` | string \| null | yes | For committed: the commit. |
| `worktrees[].undoCommand` | array of string \| null, at least 1 items | yes | What puts it back where the run found it, as an argv; null when there is nothing to undo. For example ["wt", "sync", "undo", work]. |
| `worktrees[].planFile` | string \| null | yes | For handedOver: the plan file saying what is yours. |
| `worktrees[].layers` | array of object | yes | Since sync-run 1.15.0: the branches in no worktree on this participant's line, bottom first, that the run moved with its rebase or left, and why. Empty when there are none, and for a participant the run did not rebase. |
| `worktrees[].layers[].branch` | string | yes |  |
| `worktrees[].layers[].before` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip when the run read the line. |
| `worktrees[].layers[].after` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | Where the run moved it: the replay of its commit. Null for a branch left. |
| `worktrees[].layers[].result` | `"moved"` \| `"left"` | yes | moved: the branch is at after, pinned with the run's safety and result refs, so wt sync undo puts it back with the worktrees. left: it stays at before; reason says why. |
| `worktrees[].layers[].reason` | string \| null | yes | Why it was left: it is trunk, a backup name (also by a prefix at a slash), a tip that is a fixup!, squash! or amend! commit, a merge commit on the line, a rebase that dropped, folded or changed commits, one that stopped or failed, a branch checked out meanwhile. Null for a branch moved. |
| `worktrees[].ownRemoteSync` | object \| null | yes | Since 1.2.0. Always an object from wt sync rebase; always null from resume and undo, which do not check: the check ran when the run started. |
| `worktrees[].ownRemoteSync.ref` | string \| null | yes | The remote-tracking ref, as remote/branch; null only when no ref could be named. |
| `worktrees[].ownRemoteSync.state` | `"none"` \| `"gone"` \| `"unknown"` \| `"inSync"` \| `"ahead"` \| `"behind"` \| `"rebased"` \| `"diverged"` | yes | As found, before anything moved; the states are those of ownRemote in wt status --json. |
| `worktrees[].ownRemoteSync.local` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The branch as found, before any fast-forward: where wt sync undo puts it back. |
| `worktrees[].ownRemoteSync.remote` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The remote commit compared with; null for none, gone and unknown. |
| `worktrees[].ownRemoteSync.localAhead` | integer \| null, at least 0 | yes | Commits only the branch had; null for none, gone and unknown. |
| `worktrees[].ownRemoteSync.remoteAhead` | integer \| null, at least 0 | yes | Commits only the remote had; null for none, gone and unknown. |
| `worktrees[].ownRemoteSync.fastForwarded` | boolean | yes | The run moved the branch from local to remote before rebasing it. It stays true as a record when the rebase after it was put back, which takes the branch back to local. |
| `worktrees[].ownRemoteSync.allowed` | boolean | yes | state is diverged and the run went on over it under --allow-diverged; false when the participant was refused for something else. |
| `worktrees[].ownRemoteSync.skippedReason` | `"diverged"` \| `"operation"` \| `"dirty"` \| `"session"` \| `"failed"` \| `"fetchFailed"` \| `null` | yes | Why the own remote refuses the participant; null when it does not. diverged: --allow-diverged goes on. operation, dirty, session: behind, and it cannot be fast-forwarded. failed: the fast-forward itself did not go through; nothing of this participant was touched. fetchFailed: the branch's own remote could not be fetched, so the run could not look; trunk's fetch failing is the run's error instead. |
| `worktrees[].checked` | integer, at least 0 | yes | Since 1.5.0: how many replayed commits the each_commit checks ran at, in this run or this resume. 0 when nothing was checked. |
| `worktrees[].eachCommit` | `"all"` \| `"tips"` \| `"targets"` \| `"off"` | yes | Since 1.11.0 (targets since 1.19.0): where the run's each_commit checks ran, as wt sync rebase --each-commit named it. all: at every replayed commit that touches a step's paths, the default. tips: only at the tip of each layer of a --stack line, or of the branch; the commits between are not checked and not owed. targets: at each layer's tip and at each commit a fixup, squash or amend! folded into, in an in-place --stack --autosquash fold; the commits between are not checked and not owed. off: no each_commit check ran. A resume says its run's; wt sync undo, which checks nothing, says all. wt sync steps says the mode it ran under: --each-commit's, else the mode of the run that owed the steps (targets reads as tips), else all. |
| `worktrees[].rechecked` | integer, at least 0 | yes | Since 1.7.0: how many commits the each_commit checks that read a per-layer step's files ran at again, once the step had folded its output into them or committed it. 0 when none. |
| `worktrees[].checkFailed` | object \| null | yes | Since 1.5.0: the each_commit check that stopped the rebase: handedOver at that commit, or restored for a stack parent. Null when none failed. The step, the commit (the replayed commit it failed at), its subject and its error. The participant is handedOver at that commit, HEAD the commit, nothing unmerged, and wt sync resume runs the check again there once the fix is amended in or committed; a stack parent is restored instead, and its children notRun. |
| `worktrees[].checkFailed.step` | string | yes | The check's run line from .wt-sync.yaml. |
| `worktrees[].checkFailed.commit` | string, matching `^[0-9a-f]{40,64}$` | yes | The replayed commit it failed at. |
| `worktrees[].checkFailed.subject` | string | yes |  |
| `worktrees[].checkFailed.error` | string | yes | What failed: an exit status, a timeout, the tracked files it changed, or, for a step with a retry, the exit of the run and of the retry that failed too. |
| `worktrees[].stack` | object \| null | yes | Since 1.6.0: for a --stack run, the stack held on this worktree's line: its layers and what came of each. Null for any other run. Also for the resume of a --stack run it handed over; null from undo. |
| `worktrees[].stack.branch` | string | yes | The line's own branch: the worktree named. |
| `worktrees[].stack.base` | string, matching `^[0-9a-f]{40,64}$` | yes | Where the line leaves trunk: the merge base. |
| `worktrees[].stack.head` | string, matching `^[0-9a-f]{40,64}$` | yes | The line's tip before the run. |
| `worktrees[].stack.autosquash` | boolean | yes | A fold of the line's fixups (--autosquash): onto its own base, or, with --onto, as the line moves onto the target. |
| `worktrees[].stack.layers` | array of object | yes | The layer branches, bottom first: what a run moves once its rebase has finished. The line's own branch is never one of them. |
| `worktrees[].stack.layers[].branch` | string | yes | The layer branch. |
| `worktrees[].stack.layers[].oldTip` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip before the run. |
| `worktrees[].stack.layers[].commits` | integer, at least 1 | yes | Its commits from the line's base, its tip included. |
| `worktrees[].stack.layers[].subject` | string | yes | Its tip's subject. |
| `worktrees[].stack.layers[].foundBy` | `"tipOnLine"` \| `"named"` | yes | tipOnLine: a local branch whose tip is a commit of the line. named: named with --layer, by name or by a pattern. |
| `worktrees[].stack.layers[].worktree` | string \| null | yes | The worktree the branch is checked out in; null when it is in none. |
| `worktrees[].stack.layers[].state` | `"none"` \| `"clean"` \| `"dirty"` \| `"rebasing"` \| `"bisecting"` \| `"busy"` \| `"idle"` \| `"process"` \| `"unknown"` | yes | What holds the layer's worktree: none (in no worktree), clean (nothing), dirty (tracked changes), rebasing (a rebase in progress, there or in another worktree that will move it), bisecting (a bisect in another worktree started from it), busy or idle (an agent session), process (a process whose working directory is inside the worktree), unknown (it could not be read, which holds the layer). In a run, as read when the layer was moved. A process is any process, a dev server in a terminal tab included. |
| `worktrees[].stack.layers[].stateDetail` | string \| null | yes | Who or what holds it: the session, the process, or why it could not be read. The process is named as pid 123 npm run dev. |
| `worktrees[].stack.layers[].newTip` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The run's: the layer's new tip, read from the rebase and checked. Null in the look, and when the run moved no layer. Git wrote it as the rebase passed it, and it is checked before any layer moved. |
| `worktrees[].stack.layers[].commitsAfter` | integer \| null, at least 0 | yes | The run's: the new tip's commits from the new base: the layer's commits up to it, less those trunk already carries and the fixups folded into an earlier commit. |
| `worktrees[].stack.layers[].mapped` | `"tip"` \| `"tipDropped"` \| `"emptied"` \| `null` | yes | The run's: tip (the new tip is its own tip rebased), tipDropped (its tip was left out because trunk already carries it, dropped as empty, or folded into an earlier commit by --autosquash; the new tip is its last commit left), emptied (every commit of it was left out because trunk carries it, dropped as empty, or folded into an earlier commit by --autosquash; it ends where the layer below does). |
| `worktrees[].stack.layers[].patch` | `"same"` \| `"changed"` \| `"unknown"` \| `null` | yes | The run's: whether what the layer brings to the line is the same patch as before, by git's stable patch-id. Never a refusal. |
| `worktrees[].stack.layers[].patchWhy` | string \| null | yes | The run's: why the patch changed. |
| `worktrees[].stack.layers[].result` | `"moved"` \| `"held"` \| `"unchanged"` \| `"planned"` \| `"moving"` \| `"needsRecovery"` \| `null` | yes | The run's: moved (the branch is at newTip, its worktree reset with it), held (left where it was; held says why and moveCommand moves it by hand), unchanged (its new tip is its old one), planned (recorded and not moved: the moves stopped before it), moving (its move was under way when the moves stopped: undo puts its ref back), needsRecovery (a move that went part way and could not be put back: held says what to look at, the layer keeps its pins, the run fails, and wt sync undo <line> puts it back). |
| `worktrees[].stack.layers[].how` | `"ref"` \| `"worktree"` \| `null` | yes | The run's: ref (a branch in no worktree, moved with update-ref guarded by its old tip), worktree (in its worktree, under that worktree's lock: the same guarded update-ref, then its files follow with read-tree -m -u). For a layer in a worktree: update-ref, then read-tree -m -u <old> <new>. |
| `worktrees[].stack.layers[].held` | string \| null | yes | The run's: why the layer was held, or for needsRecovery what to look at. It is also held for a tip that moved during the run, or another run's lock on its worktree, which holds while that run's process runs, however long, and otherwise until the lock is 30 minutes old. |
| `worktrees[].stack.layers[].moveCommand` | string \| null | yes | The run's: what moves a held layer by hand. |
| `worktrees[].stack.layers[].pushCommand` | array of string \| null, at least 1 items | yes | The run's: for a moved layer, its push to its own remote as an argv, with a lease on the commit wt last fetched there. A run pushes the layers it moved only with --push or a yes to the question, in one atomic push (push, since 1.21.0); without, this is the push to run by hand. Null otherwise. |
| `worktrees[].stack.layers[].noPushReason` | string \| null | yes | The run's: for a moved layer with no safe push command, why: no remote branch of its own yet, or the remote has commits the branch never had. |
| `worktrees[].stack.layers[].note` | string \| null | yes | The run's: what else happened to a moved layer, such as a checkout of it that landed during its move, at its new tip. |
| `worktrees[].stack.top` | object | yes | Since 1.17.0: the line's own branch, the top of the stack: the last row of the look's layer table. Not one of layers, which are the branches a run moves after its rebase; the rebase itself moves the top. Its fields: branch, oldTip, commits (from the line's base, its tip included; 0 for a branch with nothing ahead of trunk), subject (its tip's), worktree (where it is checked out). |
| `worktrees[].stack.top.branch` | string | yes | The line's own branch. |
| `worktrees[].stack.top.oldTip` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip before the run: head. |
| `worktrees[].stack.top.commits` | integer, at least 0 | yes | Its commits from the line's base, its tip included; 0 for a branch with nothing ahead of trunk. |
| `worktrees[].stack.top.subject` | string | yes | Its tip's subject. |
| `worktrees[].stack.top.worktree` | string \| null | yes | The worktree it is checked out in. |
| `worktrees[].stack.skipped` | array of object | yes | Branches on the line not taken, each with branch, tip and reason. |
| `worktrees[].stack.skipped[].branch` | string | yes |  |
| `worktrees[].stack.skipped[].tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].stack.skipped[].reason` | string | yes | A backup name, unless --layer names it (a pattern never does); without --layer, a branch with no commits of its own that has uncommitted tracked changes in another worktree, which --layer by name or by pattern takes; or a branch a --layer pattern matched whose tip is not on the line. |
| `worktrees[].stack.sideBranches` | array of object | yes | Since 1.16.0: the local branches that hang off the line, parents first: forked from a commit of it above its base, or above its top. Empty with --no-side-branches. |
| `worktrees[].stack.sideBranches[].branch` | string | yes |  |
| `worktrees[].stack.sideBranches[].kind` | `"forked"` \| `"above"` | yes | forked: it leaves the line at a commit below its top. above: it carries the line's top. |
| `worktrees[].stack.sideBranches[].oldTip` | string, matching `^[0-9a-f]{40,64}$` | yes | Its tip before the run. |
| `worktrees[].stack.sideBranches[].fork` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit of the line it leaves the line at: its merge base with the line's tip. |
| `worktrees[].stack.sideBranches[].forkSubject` | string | yes |  |
| `worktrees[].stack.sideBranches[].on` | string \| null | yes | The side branch it is stacked on, which moves first; null when it sits on the line itself. A backup is never what a branch is on, and neither is a branch at its own tip (see sameTip). On branches at one tip, it names the one that stands for them: one with an upstream, then the oldest, then the first by name. |
| `worktrees[].stack.sideBranches[].base` | string, matching `^[0-9a-f]{40,64}$` | yes | Where its own commits start: the tip of the branch it is on, or fork. |
| `worktrees[].stack.sideBranches[].commits` | integer, at least 0 | yes | Its own commits, from base to its tip. A branch at the same tip as another is never on it: both count the same commits. |
| `worktrees[].stack.sideBranches[].backup` | boolean | yes | Its name is a backup (wt refs sweep's patterns): listed, never moved. |
| `worktrees[].stack.sideBranches[].worktree` | string \| null | yes | The worktree it is checked out in; null when it is in none. |
| `worktrees[].stack.sideBranches[].state` | `"none"` \| `"clean"` \| `"dirty"` \| `"rebasing"` \| `"bisecting"` \| `"busy"` \| `"idle"` \| `"process"` \| `"unknown"` | yes | What holds its worktree, as for a layer. |
| `worktrees[].stack.sideBranches[].stateDetail` | string \| null | yes |  |
| `worktrees[].stack.sideBranches[].carry` | boolean | yes | The look's: the run would carry it. In a run, it was moved or is unchanged. False for a backup, a branch with a merge commit or on more than one branch, one whose worktree something holds, one stacked on a branch that is not carried, and every branch with --alone. |
| `worktrees[].stack.sideBranches[].left` | string \| null | yes | Why it is not carried: a backup, a merge commit of its own (a replay drops it), on more than one branch, what holds its worktree (said short: a process in its worktree: bash (pid 31227), +10 more), a branch at its tip that is held, stacked on a branch that is not carried, --alone; null when it is carried. |
| `worktrees[].stack.sideBranches[].newBase` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The run's: where it goes: for a branch forked mid-line, where the rebase put the commit it forks from, read from the run's own marker and checked as a layer's is; above the top, the line's new tip; on another side branch, that branch's new tip. Null when it is not known. |
| `worktrees[].stack.sideBranches[].newTip` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The run's: its own commits replayed onto newBase. Null when it was not replayed. |
| `worktrees[].stack.sideBranches[].result` | `"moved"` \| `"held"` \| `"unchanged"` \| `"planned"` \| `"moving"` \| `"needsRecovery"` \| `null` | yes | The run's, as for a layer. A backup is always held, with no moveCommand. |
| `worktrees[].stack.sideBranches[].how` | `"ref"` \| `"worktree"` \| `null` | yes | Same as for a layer. |
| `worktrees[].stack.sideBranches[].held` | string \| null | yes | The run's: why it was held. It is held for what holds its worktree, a conflict of its own commits where it goes (the commit and paths), the branch it is on being held, the fork's marker failing its check, or --alone. |
| `worktrees[].stack.sideBranches[].moveCommand` | string \| null | yes | The run's: what moves a held branch later, with real commits and every branch name shell-quoted: wt sync rebase '<branch>' --onto <newBase> --upstream <base> for a branch in a worktree, which is a run of its own with stops, resume and undo; git -C '<main checkout>' rebase --no-update-refs --onto <newBase> <base> '<branch>' for one checked out in the main checkout, which wt sync rebase does not take; git rebase --no-update-refs --onto <newBase> <base> '<branch>' for one in no worktree; --onto '<branch>' for one on a held side branch, to run once that has moved. For a branch that moves with the sibling standing for its tip: one shell line whose guards refuse, changing nothing, unless the branch is still at the tip the run recorded and the sibling is at the commit it lands on: test "$(git [-C '<worktree>'] rev-parse --verify --quiet 'refs/heads/<branch>')" = <old tip> && test "$(… 'refs/heads/<sibling>')" = <its new tip> && git [-C '<worktree>'] rebase --no-update-refs --no-autostash --onto <its new tip> <old tip> '<branch>'. With the sibling held too, no commit exists to check it against: it gets no command, and held says to move the sibling first, then this branch by hand onto the sibling's new tip, once that is checked to carry all of its commits. Nothing is replayed, so the branch lands on that very commit; the refs are refs/heads/, so no tag of the same name is taken; --no-autostash keeps the refusal over tracked changes with rebase.autoStash set. Null for a backup, for a branch with a merge commit of its own (a rebase would drop the merge: move it by hand), and when newBase is not known. |
| `worktrees[].stack.sideBranches[].sameTip` | array of string | yes | Since 1.17.0: the other side branches at its very tip, by name: siblings, carried or held together onto one new commit. Empty for most. |
| `worktrees[].stack.refusal` | string \| null | yes | The run's: why no layer moved: a new tip failed its check, or the fold changed the line's content. Null in the look, and when each layer was moved or held. The line itself is left rebased, and wt sync undo puts it back. |
| `worktrees[].stack.edit` | object \| null | yes | Since 1.9.0: what a run with --edit did to the commit it stopped at. Null in the look and for any other run. |
| `worktrees[].stack.edit.commit` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit as the line had it. |
| `worktrees[].stack.edit.subject` | string | yes | Its subject. |
| `worktrees[].stack.edit.amended` | string, matching `^([0-9a-f]{40,64})?$` | yes | What the run went on from at the edit stop: the amended commit, or the last commit added there; empty if the run never went on from it. |
| `worktrees[].stack.edit.paths` | array of string | yes | The paths the amend changed, sorted; empty when it changed nothing. |
| `worktrees[].stack.edit.emptied` | array of object | yes | The commits above it that the amend emptied, which the rebase dropped, oldest first. Each is sha (as the old line had it) and subject. A layer all of whose commits emptied is mapped emptied and ends where the layer below does. |
| `worktrees[].stack.edit.emptied[].sha` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit as the old line had it. |
| `worktrees[].stack.edit.emptied[].subject` | string | yes |  |
| `worktrees[].stack.contextGaps` | array of object | yes | Since 1.14.0: for a fold (--autosquash), in the look and in the run, every hunk of a fixup that does not apply behind its target, where autosquash puts it, because only a later commit of the line carries its context; line order. Empty for any other look or run. A warning: the run goes ahead. |
| `worktrees[].stack.contextGaps[].fixup` | string, matching `^[0-9a-f]{40,64}$` | yes | The fixup commit, as the line has it. It is the fixup!, squash! or amend! commit. |
| `worktrees[].stack.contextGaps[].fixupSubject` | string | yes |  |
| `worktrees[].stack.contextGaps[].target` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit autosquash folds it into. |
| `worktrees[].stack.contextGaps[].targetSubject` | string | yes |  |
| `worktrees[].stack.contextGaps[].path` | string | yes | The hunk's file. |
| `worktrees[].stack.contextGaps[].lines` | string, matching `^[0-9]+(-[0-9]+)?$` | yes | The lines the hunk changes in the fixup's own file: 13, or 13-15. |
| `worktrees[].stack.contextGaps[].needs` | string, matching `^[0-9a-f]{40,64}$` | yes | The first commit after the target, in line order, that carries the hunk's context. |
| `worktrees[].stack.contextGaps[].needsSubject` | string | yes |  |
| `worktrees[].stack.contextGaps[].message` | string | yes | The line wt prints for it: fixup moved ahead of its context: <path>:<lines> needs <sha> "<subject>", which comes later on the line. |
| `worktrees[].stack.contextUnchecked` | array of string | yes | Since 1.14.0: for a fold, what of its fixups could not be analysed, one line each: a fixup whose missing context cannot be told apart from another fixup's, because it is in an earlier fixup of the same target that conflicts or in a fixup of another commit that does not fold clean, or the analysis failing as a whole; each such line counts the files and hunks at risk and the later commits that add their context. Empty when every fixup was analysed and for any other look or run. |
| `worktrees[].stack.push` | object \| null | yes | Since 1.21.0: for a --stack run that pushed (--push, or yes to the question), what came of the push: every moved layer and the line's own branch that had somewhere safe to push, in one atomic push per remote, each over a lease on the commit the run recorded for its remote branch before it moved anything. Null in the look, and for a run that pushed nothing: with --no-push, a no to the question, or nobody to ask. |
| `worktrees[].stack.push.lineLeftOut` | string \| null | yes | Since 1.23.0: why the line's own branch is not in branches, when a stack push left it out: pushing it would create its remote branch, and nothing recorded says it should, so it goes only with --push-line (wt sync rebase --stack --push, wt sync push --stack). Null when it was in the push, or has nowhere to push. |
| `worktrees[].stack.push.branches` | array of object | yes | Each branch that was in a push, in this order whatever the remotes: moved layers bottom first (for a push of several recorded runs, in the order the runs first recorded them), then the line's own branch. A held layer, and a branch with nowhere safe to push, are not in it: layers[] names them (held, noPushReason). |
| `worktrees[].stack.push.branches[].branch` | string | yes | The local branch. |
| `worktrees[].stack.push.branches[].rule` | string | yes | Since 1.23.0: what decided the remote branch: recorded (wt sync push-to), or its own name. Empty for a push recorded before 1.23.0. |
| `worktrees[].stack.push.branches[].remote` | string | yes | The remote it was pushed to. |
| `worktrees[].stack.push.branches[].remoteBranch` | string | yes | Its name on the remote. |
| `worktrees[].stack.push.branches[].old` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The lease: the commit the remote branch had to be at, as last fetched before the run moved anything. Null for a branch that must not exist on the remote yet. |
| `worktrees[].stack.push.branches[].new` | string, matching `^[0-9a-f]{40,64}$` | yes | The commit pushed: the branch's new tip. |
| `worktrees[].stack.push.branches[].outcome` | `"pushed"` \| `"leaseFailed"` \| `"notSent"` \| `"unknown"` | yes | pushed: the remote branch is at new. leaseFailed: git rejected it as stale: the remote branch was not at old any more, somebody pushed to it since; this is the branch that moved under the push. Any other refusal (a hook, a policy) is notSent, with git's own reason in why. notSent: nothing on its remote moved: another branch of the same push failed its lease, or the remote refused the push as a whole (a remote that does not accept --atomic is refused, never pushed branch by branch). unknown: the push ended in a transport failure (a timeout, a dropped connection) and the remote's own refs, read back, show the branch at neither old nor new, or could not be read: whether it moved is not known. A transport failure whose read-back shows the branch at new is pushed, and at old notSent. |
| `worktrees[].stack.push.branches[].why` | string \| null | yes | For an outcome other than pushed, what git said. Null for pushed. |
| `worktrees[].stack.push.branches[].from` | object \| null | yes | Since 1.25.0: for wt sync push --stack, the run the pushed tip (new) is from: the newest unpushed run that moved the branch; old is the lease of the oldest. Null for a run's own push. |
| `worktrees[].stack.push.branches[].from.epoch` | integer | yes | The run's epoch. |
| `worktrees[].stack.push.branches[].from.time` | string (date-time) | yes | When the run wrote its record. |
| `worktrees[].stack.push.conflicted` | array of object | yes | Since 1.25.0: for wt sync push --stack refused because the runs the line's record holds disagree about a branch (see why), the branches that disagree, and branches is empty: nothing was pushed, and the record is kept. Empty for a run's own push and when no branch disagrees. |
| `worktrees[].stack.push.conflicted[].branch` | string | yes | The local branch. |
| `worktrees[].stack.push.conflicted[].why` | string | yes | Why: the runs push it to different remote branches or endpoints, or took different leases (the remote moved between the runs). |
| `worktrees[].fold` | object \| null | yes | Since 1.18.0: for a --stack run that folds the line (--autosquash, --edit, or wt sync steps), what it folded on and what its tree guard compared. Null for any other run. |
| `worktrees[].fold.inPlace` | boolean | yes | The line is folded on its own base, not moved onto a target. |
| `worktrees[].fold.base` | string, matching `^[0-9a-f]{40,64}$` | yes | The line's own base: what an in-place fold rebases onto. |
| `worktrees[].fold.guard` | object \| null | yes | The tree guard once the rebase finished: oldTip, newTip, onto (the commit a moving fold's old line was merged onto to compare, null for a fold in place), excludedGlobs (the declared per-layer step paths a changed file matched, left out), conflictAllowed (a moving fold only: files that conflicted in that merge and were resolved at a stop, in which every line of the merge outside the conflicting regions must still be, in order; not step paths), resolvedAtStop (since 1.24.0, only when not empty: paths a person resolved at a stop, taken as they resolved them), outcome (passed or failed) and reason (why it failed, else null). Null while the run has not finished, and for an edit. |
| `worktrees[].fold.guard.oldTip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].fold.guard.newTip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].fold.guard.onto` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | For a fold that moves the line: the commit the old line was merged onto to compare. Null for a fold in place, which compares with the old tip itself. |
| `worktrees[].fold.guard.excludedGlobs` | array of string | yes | The declared per-layer step paths a changed file matched, left out of the comparison. |
| `worktrees[].fold.guard.conflictAllowed` | array of string | yes | For a fold that moves the line: the files that conflicted when merged and were resolved at a stop; every line of the merge outside the conflicting regions must still be in them, in order. Not step paths. |
| `worktrees[].fold.guard.resolvedAtStop` | array of string | no | Since 1.24.0, present only when not empty: for a fold that moves the line, the paths a person resolved at a conflict stop wt handed over, taken as intended where the merge would have given other content; the check holds each to what the stop resolved it to, with what the old line changed in it after that commit. |
| `worktrees[].fold.guard.outcome` | `"passed"` \| `"failed"` | yes |  |
| `worktrees[].fold.guard.reason` | string \| null | yes | Why it failed; null when it passed. |
| `worktrees[].layerSteps` | array of object | yes | Since 1.7.0: what each per-layer step (each: layer or fold: owner) came to at the end of each layer, bottom first, steps in the order declared. Empty when the declaration has none or the rebase did not finish. |
| `worktrees[].layerSteps[].layer` | integer, at least 1 | yes | The layer, from 1 at the bottom. In a --stack run the line's own commits above its top layer are one more layer; a rebase of one branch is one layer. |
| `worktrees[].layerSteps[].layers` | integer, at least 1 | yes | How many layers the run stopped at. |
| `worktrees[].layerSteps[].branch` | string | yes | The layer's branch: a layer branch, or the line's own for the commits above every layer. |
| `worktrees[].layerSteps[].step` | string | yes | The step's run line from .wt-sync.yaml. |
| `worktrees[].layerSteps[].ran` | boolean | yes | Whether the step ran at this layer. |
| `worktrees[].layerSteps[].result` | `"unchanged"` \| `"folded"` \| `"committed"` \| `"skipped"` \| `"untouched"` \| `"owed"` \| `"failed"` | yes | unchanged: ran and changed nothing. folded: its output was folded into owner. committed: no commit of the layer changed its paths, and its output is the step's own commit at the layer's tip. skipped: no listed path changed in the layer. untouched (since 1.26.0): the run left the layer's tip as it was, so the step did not run; ran is false. owed: not run, because a need did not answer or it failed at a layer below. failed: ran and failed, changed files outside its paths, or its fold was not proved; owed, and owed at every layer above. |
| `worktrees[].layerSteps[].reason` | string \| null | yes | Why it ran, or why not, or why it failed. |
| `worktrees[].layerSteps[].owner` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | For folded, the commit its output was folded into, as rewritten: the last commit of the layer that changed one of its paths. |
| `worktrees[].layerSteps[].ownerSubject` | string \| null | yes | The owner's subject. |
| `worktrees[].layerSteps[].commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | For committed, the step's commit. |
| `worktrees[].layerSteps[].proof` | string \| null | yes | For a fold: held when the rewritten tip's tree is the step's output and every rewritten commit is unchanged outside the step's paths; otherwise what did not hold. Null when nothing was folded. If it did not hold, nothing was folded. |
| `worktrees[].layerSteps[].elapsedMs` | integer, at least 0 | yes | Since 1.20.0: how long the step took at this layer, in milliseconds. 0 when it did not run. |
| `worktrees[].emptied` | array of object | yes | Since 1.10.0: each commit a finished rebase set out to replay that left no commit in the result: trunk already had its change, or a conflict was resolved to trunk's side. Oldest first. Fixups and squashes folded by --autosquash and the commit --edit amends are not listed. Empty when none, and when the rebase did not finish. |
| `worktrees[].emptied[].commit` | string, matching `^[0-9a-f]{40,64}$` | yes | The original commit's full id. |
| `worktrees[].emptied[].subject` | string | yes | Its subject. |
| `worktrees[].emptied[].layer` | string \| null | yes | The layer branch it was in for a --stack run; for a commit above every layer, the line's own branch. Null for any other run. |
| `worktrees[].emptied[].unmatched` | boolean | yes | True when wt cannot tell whether the commit left no commit: its key (author, author date, subject) is missing from the result but a commit of the result has its author and date or its author and subject, or, with no record of the rebase's plan, a fixup or amend could have changed it, or more commits came out under one key than went in (one of them may be it, changed), or it is a fixup the plan lists as folded, in a rebase wt did not carry out start to finish, whose change is not in the result (it may have been skipped). Check the result for it. Only in emptied. |
| `worktrees[].testsOnly` | array of object | yes | Since 1.10.0: each commit whose replay changed only paths matching the declaration's test_paths while the original changed others too. Empty when none, when no test_paths is declared, and when the rebase did not finish. |
| `worktrees[].testsOnly[]` | object | yes | Fields as at `worktrees[].emptied[]`. |
| `worktrees[].adopted` | object \| null | yes | Since 1.12.0: for wt sync resume --adopt, the rebase finished by hand that it took over. Null for every other run, and for a resume --adopt that refused. keptRef is the ref that keeps the tip it found, made before anything moved: refs/wt-sync-kept/<branch>/<epoch>-adopted, or -adopted-2 and on when that is taken; undo leaves it. rebaseTip is where the rebase itself finished, which the layers are moved against. commits are the commits made after the rebase, oldest first, each with commit and subject; they stay on the branch. A check that failed at a tip is in failedSteps. |
| `worktrees[].adopted.keptRef` | string | yes | The ref that keeps the tip resume found, made before anything moved and never moved after: refs/wt-sync-kept/<branch>/<epoch>-adopted, or -adopted-2 and so on when that is taken. wt sync undo leaves it. |
| `worktrees[].adopted.rebaseTip` | string, matching `^[0-9a-f]{40,64}$` | yes | Where the rebase itself finished: the layers are read and moved against it. |
| `worktrees[].adopted.commits` | array of object | yes | The commits made after the rebase, oldest first: rebaseTip..the tip resume found. They stay on the line's own branch. |
| `worktrees[].adopted.commits[].commit` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `worktrees[].adopted.commits[].subject` | string | yes |  |
| `worktrees[].timings` | object | yes | Since 1.20.0: how long the run's each_commit checks and per-layer steps took. A resume's covers what that resume ran. Empty lists when none ran; wt sync undo, which runs none, says 0 and empty. |
| `worktrees[].timings.wallMs` | integer, at least 0 | yes | The wall time of the run, or of the resume, in milliseconds. |
| `worktrees[].timings.runs` | array of object | yes | One row per each_commit step and per per-layer step that ran, in the order each first ran. |
| `worktrees[].timings.runs[].kind` | `"check"` \| `"step"` | yes | check: an each_commit step, at every commit, tip or recheck it ran at. step: a per-layer step. |
| `worktrees[].timings.runs[].run` | string | yes | The step's run line from .wt-sync.yaml. |
| `worktrees[].timings.runs[].count` | integer, at least 1 | yes | How many times it ran. A check that failed and was retried is one run, its retry's time in it. |
| `worktrees[].timings.runs[].totalMs` | integer, at least 0 | yes | The time of all of them, in milliseconds. |
| `worktrees[].timings.slowest` | array of object | yes | The slowest runs of any check or step, slowest first; at most three. |
| `worktrees[].timings.slowest[].kind` | `"check"` \| `"step"` | yes |  |
| `worktrees[].timings.slowest[].run` | string | yes |  |
| `worktrees[].timings.slowest[].at` | string | yes | Where it ran: a commit's short id and subject for a check, a layer and its branch for a step. |
| `worktrees[].timings.slowest[].ms` | integer, at least 0 | yes |  |
| `worktrees[].droppedRecord` | object \| null | yes | Since 1.25.0: for wt sync push <work> --stack --forget, the push record it dropped, or, with --dry-run, would drop. Null for every other run, and for a --forget of a line with no record. The result is skipped: no ref moved and nothing was pushed. |
| `worktrees[].droppedRecord.runs` | array of object | yes | The runs the record held, oldest first. Empty when the record could not be read (unreadable). |
| `worktrees[].droppedRecord.runs[]` | object | yes | Fields as at `worktrees[].stack.push.branches[].from`. |
| `worktrees[].droppedRecord.branches` | array of string | yes | The branches the runs held entries for, each once: layers in the order the runs first recorded them (bottom first within one run), then the line's own branch. Empty when the record could not be read. |
| `worktrees[].droppedRecord.dropped` | boolean | yes | True when the record was dropped; false for --dry-run, which drops nothing. |
| `worktrees[].droppedRecord.unreadable` | string \| null | yes | Why the record's contents could not be read, when they could not: the record file is removed all the same, as it blocks the line's next record. Null when it was read. |
| `leftAlone` | array of object | yes | Since 1.13.0: what a `--alone` run did not take along and left on the commits it was on, each named by `work`, `branch` and `path`: the worktrees stacked on the same history as the one it rebased, the branches in no worktree on the commits it replayed, or, with --stack, the layer branches of the line. [] for every other run. |
| `leftAlone[].work` | string \| null | yes | The worktree; null for a branch that is in no worktree. Null for a branch in no worktree. |
| `leftAlone[].branch` | string | yes |  |
| `leftAlone[].path` | string \| null | yes | Null for a branch in no worktree. |
| `recovery` | string \| null | yes | For interrupted: the way back wt printed. |
<!-- json-docs:end -->

### `dropped` — a commit a rebase emptied

An element of `emptied` or `testsOnly`, for `wt sync rebase` (in place, `--onto`
or `--stack`) and the `wt sync resume` that finishes one. Fixups and squashes
that `--autosquash` folds, and the commit an `--edit` amend empties on purpose,
are in neither list. A commit is matched to its replay by author name, email
and author date plus subject, which a rebase keeps, never by subject alone; where
wt drove the rebase, the subject is the one the run's own plan says an `amend!` gave
the commit, and the folds are the ones the plan lists, so a fixup git left out
because trunk carries its patch is named. The commits an `--edit` amend empties
on purpose are in neither list, and a run whose line was put back reports neither.

`result` has `wt up`'s values and two more. `pushed` (`wt sync push <work> --stack`,
`sync-run` 1.22.0): the line's recorded branches were sent, with `stack.push` as for
a run that pushed; a dry run is `skipped`, and a push that sent nothing `refused`
with its `reason`. The other is `undone`: `wt sync undo` put the
branch back at its safety ref, or aborted its handed-over rebase. A branch undo
finds at its old tip already is `skipped`; one it aborted but could not rewind
is `needsRecovery`. `outcome` follows the same rules, with `undone` counting as
`rebased` does. Undo lists every branch of the run it undoes before putting any
back, each `notRun` until it is reported; a signal partway marks `interrupted`
each of those whose tip, or the rebase it was waiting in, changed. The exit code
keeps its meaning; read `outcome`.

### `stack` — a stack held on one line

A stack of layer branches whose commits all sit on one first-parent line:
`branch` is the worktree's branch at the top of it (a tmp branch at the top,
or the top layer itself). The look (`wt sync <work> --stack --json`) fills
what it sees; a run (`wt sync rebase <work> --stack --json`) fills the rest.

A moved layer has a safety ref and a result ref under the run's epoch, so
`wt sync undo <line>` puts it back with the line; a held one has none and is no
part of that undo.

#### `push` — a stack's push

Since `sync` 1.14.0 and `sync-run` 1.21.0. A `--stack` run that pushes (`--push`,
a yes to the question, or the push of a `wt sync resume`) pushes the layers it
moved and the line's own branch in one `git push --atomic` per remote. Each ref
carries `--force-with-lease=refs/heads/<remoteBranch>:<old>`, `old` being the
commit the remote branch held when the run began (the remote-tracking value as
last fetched, kept in the run's record, so a resume leases on it too, not on a
later fetch), and pushes `<new>:refs/heads/<remoteBranch>`. A line's own branch
with no remote branch yet has a null `old`: its lease says the branch must not
exist.

`branches` lists what was in a push: the moved layers bottom first, then the
line. A held layer and a layer with nowhere safe to push are not in it: they are
named in `layers[]` (`held`, `noPushReason`), as for a run that pushes nothing.
Layers that push to different remotes get one push each, and atomicity holds
within a remote only.

If a lease fails, nothing on that remote moves: the branch that moved under its
lease is `leaseFailed`, the others of that push `notSent`, every local branch
stays where the rebase put it and `wt sync undo <line>` puts the line back, as
for any push that fails. A remote that does not accept `--atomic` is refused the
same way, all `notSent`; wt never falls back to one push per branch. Any other refusal (a hook, a policy) is `notSent` with git's reason. After a transport failure no outcome is assumed: wt reads the pushed refs back from the remote, and reports each branch `pushed`, `notSent`, or `unknown` when the refs say neither or cannot be read.

The push goes to the push URL the run recorded with the leases, not to the remote's name, so a remote reconfigured while the question waits redirects nothing; a push to a URL moves no remote-tracking ref, so wt moves each pushed branch's, and sets the line's upstream when it had none. The read-back after a transport failure asks that same URL.

A stack is pushed as a whole or not at all: if the line was not left pushable (a step or check failed or is owed), the record has no leases (an older wt wrote it), the line's own branch has nowhere safe to push, or a remote no longer pushes to the one URL the run recorded, nothing is pushed, `push` is null, and the per-branch commands print as they do without `--push`. A layer whose remote branch is at a commit it never held, even a patch-equivalent one such as a teammate's reworded commit, is left out like one with nowhere to push. `push` is
null in the look, and for a run that pushed nothing.

`wt sync push <work> --stack --json` (`sync-run` 1.22.0, `command` `sync push`)
reports the push a `--stack` run left for later (`--no-push`, a no to the
question, nobody to ask) in this same shape: one worktree row, the line's, whose
`stack.push` is the push over the leases the run recorded, with `branches`,
`old` and `new` as above. `stack.skipped` names the layers the run left out
(held, or nowhere safe to push), `reason` saying why; the rest of `stack` is what
the record kept of the line and `layers` is empty, since the push moves none.
Nothing is fetched. Before anything is pushed it is refused (`refusal`, result
`refused`) when the line has no record, a branch of it is no longer at the tip the
run left, a remote no longer pushes to the recorded URL, or the runs the record
holds disagree about a branch (`stack.push.conflicted`, then with an empty
`branches`); a failed lease moves nothing on that remote and the record is kept
for what was not sent.

Runs that were not pushed add up (`sync-run` 1.25.0): the record holds a
contribution per run, and the push sends each branch to the newest run's tip over
the oldest run's lease. `stack.push.branches[].from` names the run the pushed tip
is from (epoch and time); it is null for a run's own push. A branch whose runs
push it to different remote branches or took different leases (the remote moved
between the runs) is `conflicted`: the whole push is refused and nothing moves.

`wt sync push <work> --stack --forget --json` drops the line's record and nothing
else. Its row is `skipped`, and `droppedRecord` gives the `runs` (epoch and time,
oldest first) and `branches` it held and whether it was `dropped` (false with
`--dry-run`); `unreadable` says why when the record's contents could not be read (it is removed all the same, with `runs` and `branches` empty). `droppedRecord` is null in every other result, and when the line had no record.

#### Side branches — what hangs off the line

Since `sync` 1.12.0 and `sync-run` 1.16.0. A side branch is a local branch
whose tip is no commit of the line but which contains its first commit and
leaves it (its merge base with the line's tip) at a commit of the line that
trunk (local and remote-tracking) and the `--onto` target do not have; trunk
itself, the long-lived branches wt refs sweep protects and the branch `--onto`
names never are:
`forked` from a commit below the top, or `above` the top itself. A branch
whose tip contains another side branch's is `on` that one, the nearest. A
backup (wt refs sweep's names) is never what a branch is on. Branches at the
very same tip are siblings, never on each other (since `sync` 1.13.0 and
`sync-run` 1.17.0): each names the others in `sameTip`, they count the same
commits, and they are carried together onto one new commit or held together.
One of them stands for them, and what is stacked above them is `on` it: one
with an upstream, then the oldest by its reflog, then the first by name, never
a backup. They come parents first: the roots by where they fork, then by name,
each followed by the branches at its tip and then what is on it.

The rebase marks where git's list puts each fork commit, after the last
commit at or below it that is left, and checks that marker as it checks a
layer's. Once the layers have moved, a side branch's own commits (`base` to
its tip) are replayed in the object store onto `newBase`, and it moves as a
layer does: `update-ref` guarded by its old tip, in its worktree only while
that is clean and idle, with a safety and a result ref under the run's epoch,
so `wt sync undo <line>` puts it back with the line. A backup is listed and
never moved. A branch that is not moved is `held`, with `moveCommand` when
where it goes is known.

A side branch's commits are replayed by merging each onto the new parent
(`git merge-tree`), as a rebase picks them; the declaration's conflict rules
and checks do not run on them, so a conflict holds the branch with its
command, which runs them.

`contextGaps`, since `sync` 1.11.0 and `sync-run` 1.14.0, for
`wt sync <work> --stack --autosquash` and `wt sync rebase <work> --stack
--autosquash`, with or without `--onto`: a fixup written at the top of the line
and folded into a commit low on it can carry hunks whose context only a later
commit adds. Each fixup is merged, in the object store, onto its target with the
fixups of the same target before it, from its own parent; a file that does not
merge clean is looked at hunk by hunk. The trees are the line's own, before any
move onto `--onto`'s ref.

A stop at such a fixup says the same in its plan and output, and suggests
`wt sync undo <work>` and moving that hunk into a fixup of `needs`. A
`wt sync resume` there refuses a resolution that drops lines of the fixup the
old tip has and no later commit brings (`--allow-dropped` goes on past it): the
participant is `refused`, with the lines in `reason`.

`edit`, since 1.9.0, for `wt sync rebase <work> --stack --edit <rev>`: the line
replayed in place with a stop at `<rev>` for a person to amend.

The line's content was held, before its branch or any layer moved, to the old
line with the amend merged in. A run whose content was refused is not here: it
is `restored`, with nothing moved and what it came to kept under
`refs/wt-sync-kept/<branch>/<epoch>`, which its `reason` names.

### `fold` — what a fold compared

Since `sync-run` 1.18.0, on a participant of a `--stack` run that folds the
line. For a fold in place (`--autosquash` without `--onto`, `--edit`,
`wt sync steps --stack`) the run's `target` and `onto` say only what the
line was assessed against and where the declaration is read from, trunk;
the rebase goes onto `base`.

### `layerSteps` — a step at every layer tip

A deferred step with `each: layer` or `fold: owner` runs inside the rebase, at
the end of each layer: in a `--stack` run every layer branch, bottom first, and
the line's own commits above the top layer as one more; in any other run the
branch is the one layer. One entry per layer and step. Its fields are in the field table above.

### `timings` — where the time went

On each participant of `wt sync rebase`, `wt sync steps`, `wt sync resume` and
`wt sync undo`. The text output ends with the same numbers. A resume's covers
only what that resume ran, not the run it continues. Every duration is an
integer number of milliseconds.

A `runs` row: `kind` (`check` for an `each_commit` step, at every commit, tip
or recheck it ran at; `step` for a per-layer step), `run` (its `run` line),
`count` (how many times it ran; a check that failed and was retried is one
run, with its retry's time in it) and `totalMs`. A `slowest` entry: `kind`,
`run`, `at` (a commit's short id and subject, `<branch> tip <id>`, `<id>
(recheck)`, or `layer n/N <branch>`) and `ms`.

## `wt new <type>/<work> [--base <ref>] --dry-run --json` — the plan

What `wt new` would create, from local state alone. It **writes nothing**: no
fetch, no branch, no directory. `wt checkout <branch> [<work>] --dry-run --json`
is the same for a branch that exists, locally or on a remote (below). A name that cannot be created is
still a plan, with its `problems` and no token — never an error exit. The
command fails only outside a git repository.

The provisioning flags (`--no-setup`, `--no-build`, `--no-superset`) change
`provision`, `buildCommand` and `superset` as they would change the run.

<!-- json-docs:begin new-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"new"` | yes |  |
| `token` | string \| null | yes | Names the inputs of this plan; pass it to `wt new --json --expect`. Null when the plan has a problem. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | How trunk was determined, first match wins, from local refs alone (since 1.2.0): config — MAIN_BRANCH in the wt configuration file; originHead — refs/remotes/origin/HEAD; conventional — the first of development, main, master that exists locally or on origin (since 1.2.1; before, main, master, trunk, development, develop, ranked by where each exists); currentBranchGuess — no longer produced since 1.2.1: when nothing names trunk, detection fails and the repository reports noConfiguration. Null when trunk is. |
| `configured` | boolean | yes | A wt configuration file that parses. Since 1.1.0, false also for a repository with no file, which runs on detected defaults (what `wt init --yes` would write) with no problem named; before, that repository had no configuration at all. |
| `types` | array of string | yes | The worktree types this repository declares. |
| `defaultType` | string \| null | yes | The type a bare work name takes. |
| `type` | string \| null | yes | As parsed from the argument; null when it does not parse, or when the name or type is not valid. |
| `work` | string \| null | yes | As parsed from the argument; null when it does not parse, or when the name or type is not valid. |
| `branch` | string \| null | yes | The branch the worktree would be on; null when the name or type is not valid. |
| `path` | string \| null | yes | The absolute path the worktree would be created at; null when the name or type is not valid. |
| `base` | string \| null | yes | What the branch would be cut from: as given with --base, else trunk, or origin/<trunk> when local trunk is behind it (since 1.3.0). |
| `baseCommit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit base resolves to now; null when it does not resolve. |
| `baseSource` | `"local"` \| `"remote"` \| `"base"` | yes | Where base comes from (since 1.3.0): local — the local trunk branch, which is equal to origin/<trunk>, ahead of it, diverged from it, or has no origin/<trunk> to compare with; remote — origin/<trunk>, because local trunk is behind it; base — what --base named. wt new fetches trunk first unless --no-fetch or --base is given, and --dry-run does too, so this is as of that fetch (a failed fetch leaves origin/<trunk> as last fetched). |
| `provision` | boolean | yes | The repository's bin/worktree/provision.sh would run. |
| `buildCommand` | string \| null | yes | The build command that would run; null when build initialisation is off, --no-build or --no-setup. |
| `superset` | boolean | yes | wt would try to register the worktree as a Superset workspace: you opted in and SUPERSET_REGISTER is auto or on. Whether Superset is running and knows this repository is not checked. |
| `problems` | array of object | yes | Why the worktree would not be created; empty when it would. |
| `problems[].code` | `"branchExists"` \| `"pathExists"` \| `"unknownType"` \| `"invalidName"` \| `"noConfiguration"` \| `"configurationInvalid"` \| `"branchMissing"` \| `"branchCheckedOut"` \| `"baseMissing"` | yes | branchExists, pathExists, unknownType, invalidName, noConfiguration (since 1.1.0 only when there is no file and no trunk to detect either; since 1.2.1 also a file with no MAIN_BRANCH when no trunk can be detected), configurationInvalid, branchMissing, branchCheckedOut (git gives a branch one worktree), baseMissing. |
| `problems[].message` | string | yes | The same problem as a sentence, as wt would print it. |
<!-- json-docs:end -->

`problems[].code`: `branchExists`, `pathExists`, `unknownType`, `invalidName`
(`.`, `/`, too many slashes, a branch name git refuses, nothing derivable), `noConfiguration`
(no `MAIN_BRANCH` in a configuration file, and no trunk wt can detect: `wt init` names one),
`configurationInvalid`, `branchMissing` and `branchCheckedOut` (checkout only:
git gives a branch one worktree), `baseMissing`.

Without `--base`, `wt new` fetches trunk from `origin` first (`--no-fetch` does not),
and `--dry-run` fetches too, so the plan is of what the run will see. It cuts from
local trunk, or from `origin/<trunk>` when local trunk is behind it: `base` then
reads `origin/<trunk>` and `baseSource` is `remote`. A fetch that fails leaves
`origin/<trunk>` as last fetched, and `wt new` goes on.

The **token** covers the command, the branch and path, `baseCommit`, and the bytes
of the wt configuration file, so a fetch that moves trunk between the plan and the
run makes `--expect` refuse. `--base <oid>` with a full commit id, as a git
client names a commit, cuts the branch from exactly that commit and gives it no
upstream; `--base origin/main` would track `origin/main` under git's
`branch.autoSetupMerge`.

## `wt checkout <branch> [<work>] --dry-run --json` — the plan

As `wt new`'s, with `command` `"checkout"`, `type` the default type, and in place
of `base` and `baseCommit`:

<!-- json-docs:begin checkout-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"checkout"` | yes |  |
| `token` | string \| null | yes | Names the inputs of this plan; pass it to `wt checkout --json --expect`. Null when the plan has a problem. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | How trunk was determined, first match wins, from local refs alone (since 1.3.0): config — MAIN_BRANCH in the wt configuration file; originHead — refs/remotes/origin/HEAD; conventional — the first of development, main, master that exists locally or on origin (since 1.3.1; before, main, master, trunk, development, develop, ranked by where each exists); currentBranchGuess — no longer produced since 1.3.1: when nothing names trunk, detection fails and the repository reports noConfiguration. Null when trunk is. |
| `configured` | boolean | yes | A wt configuration file that parses. Since 1.2.0, false also for a repository with no file, which runs on detected defaults (what `wt init --yes` would write) with no problem named; before, that repository had no configuration at all. |
| `types` | array of string | yes | The worktree types this repository declares. |
| `defaultType` | string \| null | yes | The type a bare work name takes. |
| `type` | string \| null | yes | As parsed from the argument; null when it does not parse, or when the name or type is not valid. |
| `work` | string \| null | yes | As parsed from the argument; null when it does not parse, or when the name or type is not valid. |
| `branch` | string \| null | yes | The branch the worktree would be on; null when the name or type is not valid. |
| `path` | string \| null | yes | The absolute path the worktree would be created at; null when the name or type is not valid. |
| `branchCommit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The local branch's commit now; null when there is no such local branch, as for source remote. |
| `source` | `"local"` \| `"remote"` \| `null` | yes | Since 1.1.0. Where the branch comes from: local, a local branch of that exact name, checked out as it is; remote, a remote-tracking ref as last fetched (no fetch is made), from which the local branch would be created, tracking it. Null when neither has it. |
| `remote` | string \| null | yes | Since 1.1.0. For source remote: the remote. Null otherwise. |
| `remoteRef` | string \| null | yes | Since 1.1.0. For source remote: its remote-tracking ref, refs/remotes/<remote>/<branch>, which becomes the branch's upstream. Null otherwise. |
| `remoteCommit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | Since 1.1.0. For source remote: the commit remoteRef is at now, which the branch would be created at and the token pins. Null otherwise. |
| `provision` | boolean | yes | The repository's bin/worktree/provision.sh would run. |
| `buildCommand` | string \| null | yes | The build command that would run; null when build initialisation is off, --no-build or --no-setup. |
| `superset` | boolean | yes | wt would try to register the worktree as a Superset workspace: you opted in and SUPERSET_REGISTER is auto or on. Whether Superset is running and knows this repository is not checked. |
| `problems` | array of object | yes | Why the worktree would not be created; empty when it would. |
| `problems[].code` | `"branchExists"` \| `"pathExists"` \| `"unknownType"` \| `"invalidName"` \| `"noConfiguration"` \| `"configurationInvalid"` \| `"branchMissing"` \| `"branchCheckedOut"` \| `"baseMissing"` \| `"branchAmbiguous"` \| `"remoteBranchMissing"` | yes | branchExists (also: a branch to create from a remote exists locally), pathExists, unknownType, invalidName, noConfiguration (since 1.2.0 only when there is no file and no trunk to detect either; since 1.3.1 also a file with no MAIN_BRANCH when no trunk can be detected), configurationInvalid, branchMissing (not local, and no remote has it), branchCheckedOut (git gives a branch one worktree), baseMissing, branchAmbiguous (since 1.1.0: a bare name several remotes have, none of them checkout.defaultRemote), remoteBranchMissing (since 1.1.0: <remote>/<branch> that remote does not have as last fetched). |
| `problems[].message` | string | yes | The same problem as a sentence, as wt would print it. |
<!-- json-docs:end -->

The argument is resolved from local state alone, in this order:

1. a local branch of exactly that name — `source` `local`, used as it is. A
   local branch literally called `origin/topic` wins over the remote;
2. `<remote>/<branch>` for a configured remote whose remote-tracking ref
   `refs/remotes/<remote>/<branch>` exists — `source` `remote`;
3. a bare `<branch>` that exactly one remote has — `source` `remote`. When
   several do, git's `checkout.defaultRemote` picks one if it names one of
   them; otherwise it is `branchAmbiguous`, the message naming each.

For `remote`, `branch` is the local branch the run would create at
`remoteCommit`, with `remoteRef` as its upstream (`branch.<b>.remote` and
`branch.<b>.merge`). That branch existing already is `branchExists`: check it out
by its own name. Nothing is fetched — the remote-tracking refs are as the last
`git fetch` left them, so a branch pushed since is `remoteBranchMissing`
(`<remote>/<branch>` named) or `branchMissing` (a bare name) until you fetch.

`<branch>` is a branch name; a revision expression such as `main~1` is
`branchMissing`. The token pins the commit the worktree would land on —
`branchCommit`, or `remoteCommit` together with `remoteRef` — so
`wt checkout --expect` refuses a branch or remote-tracking ref that has moved
since the plan, and a plan made for a remote branch is refused once a local
branch of that name appears.

## `wt new … --json` and `wt checkout … --json` — the run

The same run as without `--json`: the plan is recomputed, then the worktree is
created and provisioned as it always is. Progress goes to **stderr**; stdout
carries exactly one object. Neither asks anything.

A plan with a problem is refused, creating nothing. With `--expect <token>` the
recomputed plan's token must equal the one given (an empty one is refused), or
the run is refused with `planChanged`, creating nothing: the base or branch moved, the name or the
configuration changed. `planChanged` comes first, followed by any problem the
recomputed plan has — a plan made for `origin/topic` gives `planChanged` and
`branchExists` once a local `topic` appears. That check is the last thing before `git worktree add`;
afterwards wt reads the new worktree's HEAD back, and a HEAD that is not the
pinned commit (the branch moved in between, or a hook committed) is
`headMoved` — created, never a quiet success. `wt checkout` from a remote creates
the branch only if it does not exist: one that appeared after that last check is
left as it is and the run refused as `planChanged`, with nothing created.

A handled SIGINT or SIGTERM, from the moment the command starts, writes the
object too — the step in flight `interrupted`, the rest as they stood — then
stops a `provision.sh`, build command or git still running, each with its whole
process group, and exits 130 for SIGINT or 143 for SIGTERM. SIGKILL or a crash
may write none; treat a missing object as unknown. Its reaper then stops what
was running (see [Signals](#signals-and-what-wt-starts)). A usage error (a
missing argument, an unknown flag, `--expect` without `--json`) is reported as
without `--json`, with no object.

<!-- json-docs:begin new,checkout -->
`wt new --json`, [`new.v1.json`](../schema/new.v1.json), schema 1.3.0:

| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"new"` | yes |  |
| `outcome` | `"created"` \| `"createdWithProblems"` \| `"refused"` \| `"failed"` \| `"interrupted"` | yes | created: the worktree made, every step done or skipped. createdWithProblems: the worktree made, and a step failed or was not run, or it is not on expectedCommit. refused: nothing made, see problems. failed: git worktree add failed and left no worktree. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why it was refused, or stopped before planning (not in a git repository). |
| `problems` | array of object | yes | Why it was refused, or what is wrong with the worktree made that no step names. As in the plan, plus planChanged and headMoved. |
| `problems[].code` | `"branchExists"` \| `"pathExists"` \| `"unknownType"` \| `"invalidName"` \| `"noConfiguration"` \| `"configurationInvalid"` \| `"branchMissing"` \| `"branchCheckedOut"` \| `"baseMissing"` \| `"planChanged"` \| `"headMoved"` | yes | The plan's problems, and planChanged (--expect no longer holds), headMoved (the worktree is not on expectedCommit). |
| `problems[].message` | string | yes | The problem as a sentence. |
| `type` | string \| null | yes | From the plan. |
| `work` | string \| null | yes | From the plan. |
| `branch` | string \| null | yes | From the plan. |
| `path` | string \| null | yes | From the plan. |
| `base` | string \| null | yes | For wt new: what the branch was cut from: as given with --base, else trunk, or origin/<trunk> when local trunk was behind it (since 1.3.0). Null for wt checkout. |
| `expectedCommit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit the plan pinned: the base's for wt new, the branch's for wt checkout. |
| `commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The new worktree's HEAD, read after it was created; null when none was. |
| `steps` | array of object | yes | Every side effect, always all eight, in this order: worktree, branch, config, cache (since 1.1.0 for wt new, 1.2.0 for wt checkout; before, seven steps), provision, submodules, build, superset. A step that was skipped or not reached still has its row. |
| `steps[].step` | `"worktree"` \| `"branch"` \| `"config"` \| `"cache"` \| `"provision"` \| `"submodules"` \| `"build"` \| `"superset"` | yes |  |
| `steps[].result` | `"created"` \| `"untouched"` \| `"done"` \| `"registered"` \| `"skipped"` \| `"failed"` \| `"notRun"` \| `"interrupted"` | yes | worktree: created (with a reason when git failed after making it), failed. branch: created (wt new), untouched (wt checkout). config, cache, provision, submodules, build: done, skipped, failed; cache is done with a reason naming any cache the filesystem could not clone. superset: registered, skipped, failed. Any step: notRun, interrupted. |
| `steps[].commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | worktree: its HEAD. branch: its tip. |
| `steps[].reason` | string \| null | yes | Why, for skipped, failed and interrupted; a note otherwise. |
| `baseSource` | `"local"` \| `"remote"` \| `"base"` \| `null` | yes | Null when the run stopped before it planned. Where base comes from (since 1.3.0): local — the local trunk branch, which is equal to origin/<trunk>, ahead of it, diverged from it, or has no origin/<trunk> to compare with; remote — origin/<trunk>, because local trunk is behind it; base — what --base named. wt new fetches trunk first unless --no-fetch or --base is given, and --dry-run does too, so this is as of that fetch (a failed fetch leaves origin/<trunk> as last fetched). |
| `addresses` | array of object | yes | Since 1.2.0: where the new worktree's dev servers are reached, one {app, url} per app that devports config --json gives a domain, in the order it lists them, the url being https://<name>.<domain> with the name devports gives the worktree. Always present: empty when no app has a domain, or devports is not installed, fails, does not answer within two seconds, or is too old to report domains (wt does not read .dev-ports.yaml itself). Nothing is routed by wt: the address works once the dev server runs. |
| `addresses[].app` | string | yes | The app's name, as `devports config --json` reports it. |
| `addresses[].url` | string | yes | `https://<name>.<domain>`: where the app is reached when its dev server runs, with the name devports gives the worktree and the app's `domain:`. |

`wt checkout --json`, [`checkout.v1.json`](../schema/checkout.v1.json), schema 1.2.0:

| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"checkout"` | yes |  |
| `outcome` | `"created"` \| `"createdWithProblems"` \| `"refused"` \| `"failed"` \| `"interrupted"` | yes | created: the worktree made, every step done or skipped. createdWithProblems: the worktree made, and a step failed or was not run, or it is not on expectedCommit. refused: nothing made, see problems. failed: git worktree add failed and left no worktree. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why it was refused, or stopped before planning (not in a git repository). |
| `problems` | array of object | yes | Why it was refused, or what is wrong with the worktree made that no step names. As in the plan, plus planChanged and headMoved. |
| `problems[].code` | `"branchExists"` \| `"pathExists"` \| `"unknownType"` \| `"invalidName"` \| `"noConfiguration"` \| `"configurationInvalid"` \| `"branchMissing"` \| `"branchCheckedOut"` \| `"baseMissing"` \| `"branchAmbiguous"` \| `"remoteBranchMissing"` \| `"planChanged"` \| `"headMoved"` | yes | The plan's problems, and planChanged (--expect no longer holds, or a local branch of that name appeared before wt could create it from the remote; it is never overwritten), headMoved (the worktree is not on expectedCommit). |
| `problems[].message` | string | yes | The problem as a sentence. |
| `type` | string \| null | yes | From the plan. |
| `work` | string \| null | yes | From the plan. |
| `branch` | string \| null | yes | From the plan. |
| `path` | string \| null | yes | From the plan. |
| `base` | string \| null | yes | For wt new: what the branch was cut from. Null for wt checkout. |
| `expectedCommit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit the plan pinned: the base's for wt new, the branch's for wt checkout — for source remote, the remote-tracking ref's. |
| `commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The new worktree's HEAD, read after it was created; null when none was. |
| `source` | `"local"` \| `"remote"` \| `null` | yes | Since 1.1.0. Where the branch comes from: local, a local branch of that exact name, checked out as it is; remote, a remote-tracking ref as last fetched (no fetch is made), from which the local branch was created, tracking it. Null when neither has it. |
| `remote` | string \| null | yes | Since 1.1.0. For source remote: the remote. Null otherwise. |
| `remoteRef` | string \| null | yes | Since 1.1.0. For source remote: its remote-tracking ref, refs/remotes/<remote>/<branch>, which becomes the branch's upstream. Null otherwise. |
| `upstream` | string \| null | yes | The branch's upstream ref, read back after the run; null when it has none or there is no branch. |
| `steps` | array of object | yes | Every side effect, always all eight, in this order: worktree, branch, config, cache (since 1.1.0 for wt new, 1.2.0 for wt checkout; before, seven steps), provision, submodules, build, superset. A step that was skipped or not reached still has its row. |
| `steps[].step` | `"worktree"` \| `"branch"` \| `"config"` \| `"cache"` \| `"provision"` \| `"submodules"` \| `"build"` \| `"superset"` | yes |  |
| `steps[].result` | `"created"` \| `"untouched"` \| `"done"` \| `"registered"` \| `"skipped"` \| `"failed"` \| `"notRun"` \| `"interrupted"` | yes | worktree: created (with a reason when git failed after making it), failed. branch: created (wt new, and wt checkout from a remote, its reason tracking <remote>/<branch>), untouched (wt checkout of a local branch). config, cache, provision, submodules, build: done, skipped, failed; cache is done with a reason naming any cache the filesystem could not clone. superset: registered, skipped, failed. Any step: notRun, interrupted. |
| `steps[].commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | worktree: its HEAD. branch: its tip. |
| `steps[].reason` | string \| null | yes | Why, for skipped, failed and interrupted; a note otherwise. |
<!-- json-docs:end -->

`steps[].result`, by step:

| Step | Results |
|---|---|
| `worktree` | `created`, `failed` (`git worktree add` failed and left no worktree, or the path was taken). `created` with a `reason` when git failed after making it, as when a `post-checkout` hook fails; the steps after it are then `notRun` |
| `branch` | `created` (`wt new`; `wt checkout` from a remote, with the reason `tracking <remote>/<branch>`, or that the upstream could not be set), `untouched` (`wt checkout` of a local branch) |
| `config` | `done`, `skipped` (no source checkout), `failed` (an entry could not be copied, or escapes the worktree) |
| `cache` | `done` (each `WORKTREE_CACHE_DIRS` entry cloned copy-on-write, already there or not in the source; a `reason` names any the filesystem could not clone), `skipped` (no `WORKTREE_CACHE_DIRS`, `--no-cache`, no source checkout, or not one could be cloned), `failed` (an entry escapes the worktree, by its path or through a symlinked parent, or a clone or a lookup in the source failed for another reason) |
| `provision` | `done`, `skipped` (no `provision.sh`), `failed` |
| `submodules` | `done`, `skipped` (no `.gitmodules`), `failed` |
| `build` | `done`, `skipped` (`--no-build`, or off in the configuration), `failed` |
| `superset` | `registered` (also when it already had the workspace), `skipped` (not opted in, `SUPERSET_REGISTER=off`, `--no-superset`, or under `auto` not installed, not running, no project for the repository), `failed` (Superset erred, or under `on` did not take it) |
| any | `notRun` (the run stopped before it), `interrupted` |

`--no-setup` skips every step after `branch`.

`outcome`, from the steps:

| Value | When |
|---|---|
| `created` | `worktree` created and every other step done, skipped, `untouched` or `registered`; no problem |
| `createdWithProblems` | `worktree` created, and a step `failed` or `notRun`, or `headMoved` |
| `refused` | nothing created: a problem in the plan, or `planChanged` |
| `failed` | `worktree` failed and there is no worktree; `branch` says whether `wt new`'s branch was made anyway |
| `interrupted` | a signal ended the run |

The exit code keeps its meaning: non-zero for a refusal, a failed
`git worktree add` and a failed `provision.sh`; zero for a failed build,
submodule initialisation or Superset registration. Read `outcome`.

## `wt remove <work> --dry-run --json` — the plan

The plan `wt remove` prints, as one object: the checkout, its HEAD, where the
branch stands and what becomes of it, every reason the removal would refuse,
what it would leave unreachable, and a token. It changes nothing. `wt remove
<work> --json` without `--yes` prints the same plan. Like the removal it lists the
[agent sessions](#sessions--claude-and-codex) and never fetches: merged is measured against
`origin/<trunk>` as last fetched and `<trunk>`.

An unmerged branch wt made is kept under another name. It carries over where
it pushed, unless a remote branch it could push to holds commits it lacks, as
last fetched: a stale copy must not push over what moved on. `branch.staleRoutes`
lists each such remote branch and the way a push would get there (`via`):

| `via` | The route | The removal |
|---|---|---|
| `recorded` | where wt records or infers the branch pushes, which the rename would carry along | removes it (`dropped`) |
| `upstream` | the upstream, where `push.default` sends a plain push to it: `upstream`, or `simple` with the kept name the upstream's | removes it (`dropped`). An upstream nothing pushes to stays |
| `sameName` | the kept name on the remote a plain push goes to | leaves it, and says that git refuses the push unless forced |
| `refspec` | a `remote.<name>.push` refspec whose source is the kept branch or `HEAD`, or a pattern that matches it, mapped to its destination | leaves it, and says so; with a `+` it refuses, below |

Configuration is read as git reads it, from every scope; the writes stay in the
repository's own. A `remote.<name>.push` refspec is shared by every checkout and
wt does not change it. One with a `+` (`forces`) makes a plain `git push` from the
kept copy overwrite the branch that moved on, so the removal refuses before it
changes anything: the problem `staleForcedPush`, which no `--force` goes past,
and a null token. Delete the branch by hand with `git branch -D` once you have
checked it is an old copy, or remove the refspec. The token covers every route and
the commit compared, so a plan read before the remote moved, or moved further, no
longer matches. Nothing is fetched, and a destination with no remote-tracking ref
is not compared.

A plan the removal would refuse is still the whole object, with `error` set, a
null token and a non-zero exit; so is one that cannot be made (no such
worktree, the main checkout), with the worktree fields null.

<!-- json-docs:begin remove-plan -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"remove"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `trunk` | string \| null | yes | Trunk's branch name. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | Since 1.3.0. How trunk was found, as in the other plans (see docs/json.md, trunkSource); null with trunk. |
| `token` | string \| null | yes | Names this plan; pass it to wt remove --yes --json --expect. Null when the removal would refuse, or on error. |
| `error` | string \| null | yes | Why the removal would refuse, or why no plan could be made. |
| `path` | string \| null | yes | The checkout. |
| `adminDir` | string \| null | yes | The worktree's own git dir, .git/worktrees/<id>; null when it cannot be read. |
| `head` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | The commit checked out; null before the first commit. |
| `detached` | boolean | yes | No branch is checked out: no branch is touched. |
| `branch` | object \| null | yes | The branch checked out; null when detached. |
| `branch.name` | string | yes |  |
| `branch.tip` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | Null when the branch is already gone. |
| `branch.merge` | `"merged"` \| `"applied"` \| `"unmerged"` \| `"unknown"` | yes | merged: base contains the tip. applied: every commit is on base under another id, byte for byte. unmerged: base lacks commits of it. unknown: no trunk to compare with, or the branch is gone. |
| `branch.base` | string \| null | yes | The base the standing is about. |
| `branch.ahead` | integer, at least 0 | yes | Commits base lacks; meaningful for unmerged and applied. |
| `branch.pullRequest` | integer \| null | yes | A pull request GitHub merged into trunk at exactly tip, as wt list or wt sweep recorded it; it makes an unmerged branch deletable. |
| `branch.outcome` | `"delete"` \| `"keep"` \| `"none"` | yes | delete: merged, applied or pullRequest; deleted after the checkout, only at tip. keep: an unmerged branch wt made; renamed to keepAs. none: left as it is; reason says why. The same names recovery.json's branch.plan uses. |
| `branch.keepAs` | string \| null | yes |  |
| `branch.staleRoutes` | array of object | yes | Since 1.6.0. For outcome keep: every remote branch the kept copy could push to that holds commits the branch lacks, as last fetched, each with how it would get there and what the removal does about it. via: recorded (where wt records or infers the branch pushes; it goes with the rename), upstream (the upstream, where push.default sends a plain push to it), sameName (the kept name on the remote a plain push goes to), refspec (a remote.<name>.push refspec, as remote.<name>.push=<refspec>, which wt does not change: it is the remote's, shared by every checkout). dropped: the removal takes that route away by removing the branch's own setting; false: it stays, and git refuses a plain push unless forced. forces: a refspec with +, which a plain push follows over a branch that moved on; that refuses the removal (problem staleForcedPush) and nothing is changed. The token covers every route and the commit compared. Empty when none, or the branch is not kept. |
| `branch.staleRoutes[].ref` | string | yes | The remote branch, as origin/feat. |
| `branch.staleRoutes[].via` | `"recorded"` \| `"upstream"` \| `"sameName"` \| `"refspec"` | yes |  |
| `branch.staleRoutes[].refspec` | string \| null | yes | For via refspec: the setting, remote.<name>.push=<refspec>. Null otherwise. |
| `branch.staleRoutes[].forces` | boolean | yes |  |
| `branch.staleRoutes[].dropped` | boolean | yes |  |
| `branch.reason` | string \| null | yes | For none: why. |
| `bases` | array of object | yes | What merged is measured against, first first: origin/<trunk> as last fetched, then <trunk>. Remove never fetches. |
| `bases[].name` | string | yes |  |
| `bases[].tip` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `dirty` | boolean | yes | Anything uncommitted, submodules included, whatever git's config says. |
| `statusError` | string \| null | yes | Why the status could not be read; the removal refuses. |
| `hiddenFiles` | array of string | yes | Unedited files git status is told not to look at (assume-unchanged, skip-worktree). |
| `movedSubmodules` | array of string | yes | Submodules checked out at another commit than the recorded one. |
| `hasSubmodules` | boolean | yes | The checkout has a .gitmodules. |
| `nestedWorktrees` | array of string | yes | Other worktrees whose checkouts are inside this one. |
| `operation` | `"rebase"` \| `"merge"` \| `"cherry-pick"` \| `"revert"` \| `"sequencer"` \| `"bisect"` \| `null` | yes | A git operation stopped halfway there; sequencer is a cherry-pick or revert sequence whose HEAD marker is gone. |
| `sessions` | array of object | yes | {id, name, kind, pid, state} for each agent session in the checkout, other than the one running wt: kind claude or codex (since 1.4.0), state idle or busy. |
| `sessions[].id` | string | yes | Claude's session id. For a Codex session, codex-<pid> for a codex process working in the checkout, the thread id for a thread an app-server runs. |
| `sessions[].kind` | `"claude"` \| `"codex"` | yes | Since 1.4.0. Whose session it is. |
| `sessions[].name` | string \| null | yes |  |
| `sessions[].pid` | integer \| null | yes |  |
| `sessions[].state` | `"idle"` \| `"busy"` | yes |  |
| `sessionsError` | string \| null | yes | Why the sessions could not be listed; the removal refuses without --force. |
| `lock` | object \| null | yes | Git's lock on the worktree: reason, holder (who wt worked out is behind it), pid, and held (still running; a stale one is released). |
| `lock.reason` | string \| null | yes | git's reason for the lock. |
| `lock.holder` | string | yes | Whoever wt worked out is behind it. |
| `lock.pid` | integer \| null | yes | The process the reason names. |
| `lock.held` | boolean | yes | Its holder is still there. A held lock refuses without --force; a stale one is released. |
| `unreachable` | array of object | yes | Each tip nothing surviving the removal holds, with kind, oid, count, path and restoreCommand. A branch is the branch the removal deletes: listed, not refused, with the git branch argv that brings its commits back. head and submodule (with path) refuse. |
| `unreachable[].kind` | `"branch"` \| `"head"` \| `"submodule"` | yes | branch: the branch the removal deletes; listed, not refused. head: the checkout's HEAD; refuses. submodule: a submodule's commits; refuses. |
| `unreachable[].oid` | string, matching `^[0-9a-f]{40,64}$` | yes |  |
| `unreachable[].count` | integer, at least 1 | yes |  |
| `unreachable[].path` | string \| null | yes | For submodule: the submodule, relative to the checkout. |
| `unreachable[].restoreCommand` | array of string \| null | yes | For branch: the git branch argv that brings the commits back once the branch is gone, to run in the repository (git -C <repo> branch <name> <oid>). |
| `reachError` | string \| null | yes | Why that could not be worked out; the removal refuses. |
| `quarantine` | string \| null | yes | With --move-to <dir>: the folder, absolute. |
| `keepSuperset` | boolean | yes | Since 1.1.0. --keep-superset was given: the worktree's Superset workspace is left, and Superset is not asked anything. The token covers it. |
| `processes` | array of object | yes | Since 1.5.0. The processes working in the checkout, or in a directory inside it, that wt does not spare: a dev server, a watcher, a language server. Spared and never listed: another user's processes, any with a controlling terminal, wt itself with its children and ancestors, and agent processes, Claude's and Codex's, the caller's own included. Empty when there are none, or when they could not be found (processesError says why). Removal ends them only with --reap; the token covers them then. reap is true with --reap, which ends it before the checkout goes, and false without, when it is left running. |
| `processes[].pid` | integer, at least 2 | yes |  |
| `processes[].command` | string | yes | Its command line as ps shows it, arguments joined with spaces. |
| `processes[].reap` | boolean | yes | --reap was given: the removal ends it, SIGTERM and then SIGKILL after 5 seconds, before the checkout goes. False: it is left running. |
| `processesError` | string \| null | yes | Since 1.5.0. Why the processes could not be found: the process table could not be read, or the agent sessions could not be listed, so none is known to be safe to end. With --reap this refuses the removal (problem processesUnknown), whatever --force says; without it the removal goes ahead and leaves them. With --reap that refuses (processesUnknown). |
| `force` | boolean | yes | Bare --force was given: the problems with force true do not refuse. False for --force=<list>, whose categories forceWith names. |
| `forceWith` | array of `"idle-sessions"` \| `"busy-sessions"` \| `"sessions-unknown"` \| `"hidden-files"` \| `"lock"` | yes | Since 1.2.0. The --force categories given, all five for bare --force; empty without it. The token covers them. |
| `problems` | array of object | yes | Every problem with the removal; those that refuse it are all of them, less the ones whose forceWith is in the top-level forceWith. |
| `problems[].code` | `"dirty"` \| `"statusUnknown"` \| `"session"` \| `"sessionsUnknown"` \| `"lockHeld"` \| `"operation"` \| `"hiddenChanges"` \| `"headUnreachable"` \| `"reachUnknown"` \| `"submoduleUnreachable"` \| `"nestedWorktree"` \| `"quarantineUnusable"` \| `"staleForcedPush"` \| `"processesUnknown"` \| `"planChanged"` | yes | dirty: anything uncommitted, submodules included, or a submodule not at its recorded commit. statusUnknown: git status, the git dir or HEAD could not be read. session: an agent session is in it, idle or busy. sessionsUnknown: the sessions could not be listed. lockHeld: git's worktree lock, and its holder is still running. operation: a rebase, merge, cherry-pick, revert or bisect in progress. hiddenChanges: files git status is told not to look at. headUnreachable: HEAD holds commits no surviving ref or worktree would. reachUnknown: what would be lost could not be worked out. submoduleUnreachable: a submodule commit only this worktree's copy holds. nestedWorktree: another worktree is inside it. staleForcedPush: a remote.<name>.push refspec with + would force a plain git push from the kept copy over a remote branch that moved on; nothing is changed (since 1.6.0). quarantineUnusable: the --move-to folder exists, is on another volume, or is inside the repository. processesUnknown: with --reap, the processes in it could not be found or told apart from agent sessions; no --force goes past it. planChanged (result only): --expect named another plan. The codes shared with sweep-plan's kept mean the same there. |
| `problems[].message` | string | yes | The problem as a sentence. |
| `problems[].force` | boolean | yes | --force goes past it: somebody's claim on the checkout, not work that would be lost. |
| `problems[].forceWith` | `"idle-sessions"` \| `"busy-sessions"` \| `"sessions-unknown"` \| `"hidden-files"` \| `"lock"` \| `null` | yes | Since 1.2.0. The --force category that goes past it; null when force is false. A session problem is either idle-sessions or busy-sessions: idle and busy sessions are two problems. Null when force is false (since 1.2.0). |
<!-- json-docs:end -->

`problems[].code`, exhaustively; the codes it shares with `sweep-plan`'s `kept`
mean the same there:

| Value | Meaning |
|---|---|
| `dirty` | uncommitted changes, submodules included, or a submodule at another commit |
| `statusUnknown` | the status, the git dir or HEAD could not be read |
| `session` | an agent session is in it: one problem for the idle ones (`idle-sessions`), one for the busy ones (`busy-sessions`) |
| `sessionsUnknown` | the sessions could not be listed (`sessions-unknown`) |
| `lockHeld` | git's lock, and its holder is still running (`lock`) |
| `hiddenChanges` | files `git status` is told not to look at (`hidden-files`) |
| `operation` | a rebase, merge, cherry-pick, revert or bisect in progress |
| `headUnreachable` | its HEAD holds commits nothing surviving would |
| `submoduleUnreachable` | a submodule commit only this worktree's copy holds |
| `reachUnknown` | what would be lost could not be worked out |
| `nestedWorktree` | another worktree is inside it |
| `quarantineUnusable` | the `--move-to` folder exists, is on another volume, or is inside the repository |
| `staleForcedPush` | since 1.6.0: a `remote.<name>.push` refspec with `+` would force a plain `git push` from the kept copy over a remote branch that moved on; no `--force` goes past it |
| `processesUnknown` | since 1.5.0, with `--reap`: the processes in it could not be found, or told apart from agent sessions; no `--force` goes past it |
| `planChanged` | result only: `--expect` named another plan |

`--force=<list>` names what the removal may go past, comma-separated or
repeated: `idle-sessions`, `busy-sessions`, `sessions-unknown`, `hidden-files`,
`lock`; `busy-sessions` covers idle sessions too, so a session that stops
working does not turn a forced removal into a refusal. Bare `--force` is all
five; `--force=` is none; an unknown name, `true` or `false` included, is a
usage error. Nothing goes past the others. Right before the checkout is deleted or moved,
its status and the sessions are read again: a session that turned busy or
arrived since the plan refuses the removal unless `--force` names its state
now, and a listing that fails refuses unless it names `sessions-unknown`.

The **token** covers the repository, trunk's name, how it was found and both
bases' commits, the wt configuration file, and everything the removal turns on: the checkout, its
admin dir, HEAD, the branch, its tip, standing, pull request and outcome, the
commit its kept name holds, anything uncommitted or hidden, moved submodules, nested worktrees, the
operation, the sessions by id and state (idle or busy), the lock (reason,
pid, held), what would be lost, the `--move-to` folder or its absence, the
`--force` categories, whether the
Superset integration is on, `--keep-superset`, and `--reap` with the processes
it would end, by pid and command. Any difference is
a different token — a new trunk commit too, since the base commit is part of
what the removal was judged against.

## `wt remove <work> --yes --json` — the removal

The same removal as `wt remove <work> --yes`. The plan as wt prints it, and the
progress, go to **stderr**; stdout carries exactly one object. It never asks.

With `--expect <token>` (it needs `--yes --json`), the plan is made again and
the removal refuses, touching nothing, unless it has that token: `outcome`
`refused`, `planChanged` in `problems` with the plan's own problems, a non-zero
exit. Then, as every removal does, the checkout is read once more right before
it goes, and the branch's merge is asked again right before it is deleted.

A handled SIGINT or SIGTERM, from the moment the repository is open, writes the
object too, read from the disk as it is then — between the two moves of a
`--move-to` that is `partlyMoved`, `branch` `notRun` — and exits 130 for SIGINT
or 143 for SIGTERM. SIGKILL or a crash may write none: `<dir>/recovery.json`
still says how far the move got. A usage error is reported as without `--json`,
with no object.

<!-- json-docs:begin remove -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes | The major version: a new one only for a change that breaks readers. |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes | The full semantic version of this schema that the output follows: minor for an added field or enumeration value, patch for wording. |
| `command` | `"remove"` | yes |  |
| `repo` | string \| null | yes | The main checkout; null outside a repository. |
| `trunk` | string \| null | yes | Trunk's branch name. |
| `trunkSource` | `"config"` \| `"originHead"` \| `"conventional"` \| `"currentBranchGuess"` \| `null` | yes | Since 1.3.0. How trunk was found, as in the other plans (see docs/json.md, trunkSource); null with trunk. |
| `token` | string \| null | yes | The token of the plan this run made; with --expect, the one it was held to. |
| `outcome` | `"removed"` \| `"removedWithBranchProblem"` \| `"refused"` \| `"partial"` \| `"interrupted"` | yes | removed: the worktree is gone, or quarantined, and the branch step did what the plan said. removedWithBranchProblem: the worktree is gone and the branch was left as it was on purpose (it moved, is no longer merged, or a worktree took it); error says which. refused: nothing in the repository changed; problems says why, and reaped lists any process stopped before the refusal. partial: something changed and a later step failed — a quarantine that stopped after writing recovery.json, the second move, or the branch step; the steps and quarantine say which, and restoreCommand undoes a quarantine. interrupted: a signal ended the run. |
| `error` | string \| null | yes | Why it refused or stopped. |
| `problems` | array of object | yes | For refused: why, as the plan names it, and planChanged for --expect. |
| `problems[].code` | `"dirty"` \| `"statusUnknown"` \| `"session"` \| `"sessionsUnknown"` \| `"lockHeld"` \| `"operation"` \| `"hiddenChanges"` \| `"headUnreachable"` \| `"reachUnknown"` \| `"submoduleUnreachable"` \| `"nestedWorktree"` \| `"quarantineUnusable"` \| `"staleForcedPush"` \| `"processesUnknown"` \| `"planChanged"` | yes | dirty: anything uncommitted, submodules included, or a submodule not at its recorded commit. statusUnknown: git status, the git dir or HEAD could not be read. session: an agent session is in it, idle or busy. sessionsUnknown: the sessions could not be listed. lockHeld: git's worktree lock, and its holder is still running. operation: a rebase, merge, cherry-pick, revert or bisect in progress. hiddenChanges: files git status is told not to look at. headUnreachable: HEAD holds commits no surviving ref or worktree would. reachUnknown: what would be lost could not be worked out. submoduleUnreachable: a submodule commit only this worktree's copy holds. nestedWorktree: another worktree is inside it. staleForcedPush: a remote.<name>.push refspec with + would force a plain git push from the kept copy over a remote branch that moved on; nothing is changed (since 1.6.0). quarantineUnusable: the --move-to folder exists, is on another volume, or is inside the repository. processesUnknown: with --reap, the processes in it could not be found or told apart from agent sessions; no --force goes past it. planChanged (result only): --expect named another plan. The codes shared with sweep-plan's kept mean the same there. |
| `problems[].message` | string | yes | The problem as a sentence. |
| `problems[].force` | boolean | yes | --force goes past it: somebody's claim on the checkout, not work that would be lost. |
| `problems[].forceWith` | `"idle-sessions"` \| `"busy-sessions"` \| `"sessions-unknown"` \| `"hidden-files"` \| `"lock"` \| `null` | yes | Since 1.2.0. The --force category that goes past it; null when force is false. A session problem is either idle-sessions or busy-sessions: idle and busy sessions are two problems. |
| `path` | string \| null | yes | From the plan: the checkout. |
| `branch` | string \| null | yes | From the plan. |
| `tip` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | From the plan. |
| `keepAs` | string \| null | yes | From the plan. |
| `staleRoutes` | array of object | yes | Since 1.6.0. From the plan: the stale routes of the kept copy, as branch.staleRoutes lists them in the plan. |
| `staleRoutes[].ref` | string | yes | The remote branch, as origin/feat. |
| `staleRoutes[].via` | `"recorded"` \| `"upstream"` \| `"sameName"` \| `"refspec"` | yes |  |
| `staleRoutes[].refspec` | string \| null | yes | For via refspec: the setting, remote.<name>.push=<refspec>. Null otherwise. |
| `staleRoutes[].forces` | boolean | yes |  |
| `staleRoutes[].dropped` | boolean | yes |  |
| `steps` | array of object | yes | worktree, branch and superset, always all three, in that order: step, result, commit, reason and pin. |
| `steps[].step` | `"worktree"` \| `"branch"` \| `"superset"` | yes |  |
| `steps[].result` | `"removed"` \| `"quarantined"` \| `"partlyMoved"` \| `"kept"` \| `"notRun"` \| `"interrupted"` \| `"deleted"` \| `"renamed"` \| `"untouched"` \| `"failed"` \| `"deregistered"` \| `"notRegistered"` \| `"skipped"` \| `"optedOut"` | yes | worktree: removed, quarantined (both moves done), partlyMoved (the checkout moved, its admin dir not), kept (still there). branch: deleted, renamed (to keepAs), untouched, kept (turned down on purpose), failed (git failed). superset: deregistered, notRegistered, skipped, failed, optedOut (since 1.1.0: --keep-superset; Superset was not asked). Any: notRun (not reached), interrupted (a signal caught it and its state does not say it finished). |
| `steps[].commit` | string \| null, matching `^[0-9a-f]{40,64}$` | yes | branch's tip. |
| `steps[].reason` | string \| null | yes |  |
| `steps[].pin` | string \| null, matching `^refs/wt-swept/` | yes | Since 1.4.0: for branch, the ref the deleted branch was moved into, read back; null otherwise. |
| `forced` | array of `"idle-sessions"` \| `"busy-sessions"` \| `"sessions-unknown"` \| `"hidden-files"` \| `"lock"` | yes | Since 1.2.0. The --force categories the removal went past, in the plan or in the sessions and checkout read again right before the checkout went; empty when it went past none or did not get that far. |
| `quarantine` | object \| null | yes | With --move-to, once its recovery.json is written: dir, checkoutMoved, adminMoved (each directory, by identity, in the folder), recoveryFile, and steps as recovery.json records them. |
| `quarantine.dir` | string | yes | The worktree's own folder in the --move-to folder, the destination of the moves. It is there once recovery.json is written, whether or not either directory has moved: checkoutMoved and adminMoved say which have. |
| `quarantine.checkoutMoved` | boolean | yes | Whether the checkout is in the folder, by identity. |
| `quarantine.adminMoved` | boolean | yes | Whether the admin directory is in the folder, by identity. |
| `quarantine.recoveryFile` | string | yes | <dir>/recovery.json: see wt schema recovery. |
| `quarantine.steps` | array of object | yes | The removal's steps as recovery.json records them; empty when it has none. |
| `quarantine.steps[].name` | `"lock"` \| `"pin"` \| `"moveCheckout"` \| `"moveAdmin"` \| `"relink"` \| `"branch"` | yes | The step, as recovery.json records it. |
| `quarantine.steps[].state` | `"pending"` \| `"running"` \| `"done"` \| `"failed"` | yes |  |
| `quarantine.steps[].error` | string \| null | yes |  |
| `restoreCommand` | array of string \| null | yes | The argv that puts back what went, to run in repo: wt restore <dir> once a quarantine wrote its recovery.json; since 1.4.0 wt refs restore <runId> --only refs/heads/<name> --yes for a branch moved into a pin; else git -C <repo> branch <name> <tip> for a branch gone from its name (renamed to keepAs); null when nothing needs it. |
| `recovery` | string \| null | yes | For interrupted: what wt printed. |
| `runId` | string \| null, matching `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4,32}$` | yes | Since 1.4.0. The run under refs/wt-swept/ the deleted branch was moved into, as wt refs sweep moves backups: wt refs swept lists it, wt refs restore puts it back, wt refs purge deletes it for good. null when the branch was not moved: renamed, untouched, kept, or deleted by a quarantine, which pins it itself. A run a signal ended may name a run that holds only its meta. |
| `reaped` | array of object | yes | Since 1.5.0. With --reap: each process the removal ended, or tried to, right before the checkout went, found afresh then. Empty without --reap, when there were none, and for a run a signal ended. |
| `reaped[].pid` | integer, at least 2 | yes |  |
| `reaped[].command` | string | yes |  |
| `reaped[].result` | `"terminated"` \| `"killed"` \| `"gone"` \| `"survived"` | yes | terminated: it exited after SIGTERM. killed: it outlived SIGTERM's 5 seconds and SIGKILL ended it. gone: it had exited before it was signalled. survived: it is still running, or wt may not signal it; the removal then stopped before anything was removed. |
<!-- json-docs:end -->

Since 1.4.0 a removal without `--move-to` deletes a merged branch the way a
sweep does (see [Deleted branches are pinned](#deleted-branches-are-pinned)):
into `refs/wt-swept/<runId>/heads/<name>`, in a run of its own, begun only when
the branch step deletes. An unmerged branch wt made is renamed to `keepAs`,
which keeps its commits under a name, so nothing is pinned and `runId` is null;
so it is for a branch left `untouched` or `kept`. With `--move-to`, the
removal pins the branch under `refs/wt-quarantine/`, and `wt restore <dir>`
puts it back with the worktree.

`steps[].result`, by step:

| Step | Results |
|---|---|
| `worktree` | `removed`, `quarantined` (both moves), `partlyMoved` (the checkout moved, its admin dir not), `kept` (still there; `reason` says why) |
| `branch` | `deleted`, `renamed` (to `keepAs`), `untouched`, `kept` (turned down on purpose: it moved, is no longer merged, or a worktree took it), `failed` (git failed) |
| `superset` | `deregistered`, `notRegistered`, `skipped`, `failed`, and since 1.1.0 `optedOut` (`--keep-superset`), as in `sweep`, where the risk `--keep-superset` closes is explained |
| any | `notRun` (not reached), `interrupted` (a signal caught it, and its state does not say it finished) |

`outcome`:

| Value | When |
|---|---|
| `removed` | the worktree is gone, or moved to its folder, and the branch step did what the plan said |
| `removedWithBranchProblem` | the worktree is gone, and the branch `kept` |
| `refused` | nothing in the repository changed; processes `reaped` lists may have been stopped |
| `partial` | something changed and a later step failed: a `--move-to` that stopped after writing `recovery.json` (its lock and pins are taken), the second move, or the branch step (`failed`); `wt restore <dir>` puts the worktree back |
| `interrupted` | a signal ended the run |

The exit code is zero only for `removed`.

### Processes — what still runs in a worktree

A removal looks for the processes whose working directory is the checkout or a
directory inside it: a dev server, a watcher, a language server. It never
counts another user's processes, any with a controlling terminal (a shell, an
editor in a terminal, a dev server started in a terminal tab), wt itself with its children and ancestors, or an agent
process, Claude's or Codex's, the session running wt included. The process
table is read with `ps`, the directories with `lsof` (or `/proc`); a process
whose directory may not be read is not counted.

Without `--reap` they are listed and left running: the removal goes ahead.
With it they are found again right before the checkout goes and ended:
SIGTERM, up to 5 seconds, then SIGKILL. `reaped[].result`:

| Value | Meaning |
|---|---|
| `terminated` | it exited after SIGTERM |
| `killed` | it outlived SIGTERM's 5 seconds, and SIGKILL ended it |
| `gone` | it had exited before it was signalled |
| `survived` | still running, or wt may not signal it: the removal stops, exit 1, nothing removed |

Processes that cannot be found, or an agent listing that fails, refuse a
`--reap` removal, whatever `--force` says: nothing is ended unless it is known
not to be an agent's.

## `wt session list [<work>] --json` — a worktree's sessions

The Claude sessions in a worktree, found as `wt attach` finds them: `<work>`
as `wt find` matches, none meaning the worktree you are in, and from the main
checkout every worktree of the repository. It connects to each inbox, to see
that it takes a connection, and writes nothing; `--no-inbox-check` leaves the
connection out. `--all`, `--roots` and `--profile` list every selected
repository's sessions from one read of claude's, opening only the
repositories that hold one. Codex sessions are listed too,
and take no messages.

<!-- json-docs:begin session-list -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"session list"` | yes |  |
| `sessions` | array of object | yes | The worktree's sessions, the one that needs its person first and then the newest, the session wt runs under last. |
| `sessions[].work` | string | yes | The worktree's name. |
| `sessions[].path` | string | yes | The worktree's path. |
| `sessions[].kind` | `"claude"` \| `"codex"` | yes | Whose session. A Codex session has no sessionId, no inbox and no transcript; its state is busy or idle. |
| `sessions[].id` | string \| null | yes | The short id claude attach takes; null for a session open only in a terminal. |
| `sessions[].sessionId` | string \| null | yes | The conversation's full id: what --expect-session takes. |
| `sessions[].name` | string \| null | yes | The name the session goes by, cleaned of control characters; null when it has none. |
| `sessions[].pid` | integer \| null, at least 2 | yes | Claude's process; null for a session with none, a parked background one. |
| `sessions[].terminal` | boolean | yes | An interactive session, open in a terminal; false for a background one, even while attached. |
| `sessions[].state` | `"busy"` \| `"idle"` | yes | As everywhere in wt: idle is a session waiting for its person; anything else, and anything not known to be idle, is busy. |
| `sessions[].claudeState` | string \| null | yes | claude agents --json's state (working, blocked, done, failed, stopped), raw; null for a session with none. |
| `sessions[].claudeStatus` | string \| null | yes | claude agents --json's status (busy, waiting, idle), raw; null while it has no process. |
| `sessions[].waitingFor` | string \| null | yes | What a waiting session waits on (permission prompt, input needed), raw; null unless it is. |
| `sessions[].inbox` | string \| null | yes | The inbox's socket, as uds: without the prefix: an absolute path. Null when inboxUnavailable says why. The address to name in --reply-to has the same directory. |
| `sessions[].inboxUnavailable` | `"notRunning"` \| `"own"` \| `"codex"` \| `"noRegistry"` \| `"notASocket"` \| `"refused"` \| `null` | yes | Why there is no inbox. notRunning: no process. own: the session wt runs under; wt does not send to it. codex: a Codex session takes no messages. noRegistry: claude's registry file for the pid is missing, unreadable, or names another session. notASocket: the path is not a socket of yours in a directory closed to others. refused: it did not accept a connection. noRegistry covers a reused pid: the registry file names another session. |
| `sessions[].transcript` | string \| null | yes | The session's transcript file, when one is found. It is the session's .jsonl under ~/.claude/projects. |
| `sessions[].startedAt` | string \| null | yes | RFC 3339, UTC, from claude agents --json's startedAt; null when it has none. |
| `sessions[].lastActivityAt` | string \| null | yes | RFC 3339, UTC: the modification time of the transcript file, one stat and no read, so it is there for a parked session too. Claude writes the transcript at every turn. Null when no transcript was found; never guessed. |
| `sessions[].activity` | `"needsInput"` \| `"working"` \| `"idle"` \| `"done"` \| `"parked"` | yes | The one state word wt list and gittree agree on. parked: a background session with no process, whatever it was waiting for. needsInput: it has a process and is blocked or waiting on a prompt (waitingFor says which). done: claude's done, failed and stopped (claudeState has the raw word). working and idle as state busy and idle. A Codex session is working or idle. |
| `sessions[].background` | boolean | yes | A claude --bg session, which has an attach id, whether or not it is open in a terminal now; false for one started in a terminal. |
| `sessions[].shown` | boolean | yes | The session wt list shows for its worktree. |
| `sessions[].repo` | string | yes | The repository's name; empty when it is not known. |
| `sessions[].stateSince` | string \| null | yes | RFC 3339, UTC: when claude last changed the session's status, from statusUpdatedAt in its registry file ~/.claude/sessions/<pid>.json. Null for a session with no process, or no registry file. |
| `sessionsError` | string \| null | yes | Why claude's sessions could not be read (claude is not on the PATH, it did not answer). The sessions are then empty and the exit status is 1. |
| `error` | string \| null | yes | Why the worktree could not be found, or is ambiguous. |
| `codexError` | string \| null | yes | Why Codex's sessions could not be looked for (the process table, its logs). The Claude sessions are listed regardless. |
<!-- json-docs:end -->

The registry file and the socket are Claude Code's, not a documented
interface; `inboxUnavailable` is what a change there looks like.

## `wt session send <work> [<session>] --json` — the outcome

One message to one running session's inbox. The text comes from stdin, or
`--message-file`. The line written is
`{"msgV":1,"msg_id":…,"type":"user","message":{"role":"user","content":…},"priority":"next","session_id":…[,"from":…]}`,
with the content in a `<cross-session-message>` header that names the sender.
Nothing is retried. `--dry-run` goes as far as the frame and sends nothing.

<!-- json-docs:begin session-send -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"session send"` | yes |  |
| `target` | object \| null | yes | The session chosen; null when none was. |
| `target.work` | string | yes | The worktree's name. |
| `target.path` | string | yes | The worktree's path. |
| `target.kind` | `"claude"` \| `"codex"` | yes | Whose session. A Codex session has no sessionId, no inbox and no transcript; its state is busy or idle. |
| `target.id` | string \| null | yes | The short id claude attach takes; null for a session open only in a terminal. |
| `target.sessionId` | string \| null | yes | The conversation's full id: what --expect-session takes. |
| `target.name` | string \| null | yes | The name the session goes by, cleaned of control characters; null when it has none. |
| `target.pid` | integer \| null, at least 2 | yes | Claude's process; null for a session with none, a parked background one. |
| `target.terminal` | boolean | yes | An interactive session, open in a terminal; false for a background one, even while attached. |
| `target.state` | `"busy"` \| `"idle"` | yes | As everywhere in wt: idle is a session waiting for its person; anything else, and anything not known to be idle, is busy. |
| `target.claudeState` | string \| null | yes | claude agents --json's state (working, blocked, done, failed, stopped), raw; null for a session with none. |
| `target.claudeStatus` | string \| null | yes | claude agents --json's status (busy, waiting, idle), raw; null while it has no process. |
| `target.waitingFor` | string \| null | yes | What a waiting session waits on (permission prompt, input needed), raw; null unless it is. |
| `target.inbox` | string \| null | yes | The inbox's socket, as uds: without the prefix: an absolute path. Null when inboxUnavailable says why. The address to name in --reply-to has the same directory. |
| `target.inboxUnavailable` | `"notRunning"` \| `"own"` \| `"codex"` \| `"noRegistry"` \| `"notASocket"` \| `"refused"` \| `null` | yes | Why there is no inbox. notRunning: no process. own: the session wt runs under; wt does not send to it. codex: a Codex session takes no messages. noRegistry: claude's registry file for the pid is missing, unreadable, or names another session. notASocket: the path is not a socket of yours in a directory closed to others. refused: it did not accept a connection. noRegistry covers a reused pid: the registry file names another session. |
| `target.transcript` | string \| null | yes | The session's transcript file, when one is found. It is the session's .jsonl under ~/.claude/projects. |
| `target.startedAt` | string \| null | yes | RFC 3339, UTC, from claude agents --json's startedAt; null when it has none. |
| `target.lastActivityAt` | string \| null | yes | RFC 3339, UTC: the modification time of the transcript file, one stat and no read, so it is there for a parked session too. Claude writes the transcript at every turn. Null when no transcript was found; never guessed. |
| `target.activity` | `"needsInput"` \| `"working"` \| `"idle"` \| `"done"` \| `"parked"` | yes | The one state word wt list and gittree agree on. parked: a background session with no process, whatever it was waiting for. needsInput: it has a process and is blocked or waiting on a prompt (waitingFor says which). done: claude's done, failed and stopped (claudeState has the raw word). working and idle as state busy and idle. A Codex session is working or idle. |
| `target.background` | boolean | yes | A claude --bg session, which has an attach id, whether or not it is open in a terminal now; false for one started in a terminal. |
| `target.shown` | boolean | yes | The session wt list shows for its worktree. |
| `target.repo` | string | yes | The repository's name; empty when it is not known. |
| `target.stateSince` | string \| null | yes | RFC 3339, UTC: when claude last changed the session's status, from statusUpdatedAt in its registry file ~/.claude/sessions/<pid>.json. Null for a session with no process, or no registry file. |
| `dryRun` | boolean | yes | Whether this was a dry run: nothing was written. |
| `sent` | boolean | yes | True: the line was written to the inbox and the connection closed cleanly. Not that the session's Claude has read it. |
| `msgId` | string \| null | yes | The message's id, a UUID, in the frame; null unless sent. |
| `bytes` | integer, at least 0 | yes | The size of the message text read, in bytes, the whole of it even when over the limit; 0 when it was not read. |
| `limit` | `262144` | yes | The most message text wt sends, in bytes. |
| `error` | string \| null | yes | Set on failure, with a non-zero exit. |
| `errorCode` | `"noSession"` \| `"ambiguous"` \| `"notRunning"` \| `"noInbox"` \| `"changed"` \| `"tooLarge"` \| `"empty"` \| `"writeFailed"` \| `"badArgument"` \| `"notExact"` \| `null` | yes | noSession: no worktree or no session matches. ambiguous: several match, nothing chosen (exit 2). notRunning: the session has no process. noInbox: it has no inbox wt may write to (target.inboxUnavailable says why). changed: the session is not the one --expect-session named. tooLarge: the message, or its frame, is over the limit. empty: no text. writeFailed: the connection or the write failed. badArgument: --from-name or --reply-to is not acceptable (exit 2). notExact: <work> only matched a worktree and is not its name, and no --expect-session was given. |
<!-- json-docs:end -->

`errorCode`:

| Value | Exit | When |
|---|---|---|
| `noSession` | 1 | no worktree matches, or no session in it, or none by that name |
| `ambiguous` | 2 | several match, or several are running and none was named; nothing is chosen. `error` lists them |
| `notRunning` | 1 | the session has no process. wt does not send to a parked session or resume it |
| `noInbox` | 1 | no inbox wt may write to; `target.inboxUnavailable` says why |
| `changed` | 1 | `--expect-session` named a session that is not the one there |
| `notExact` | 1 | `<work>` only matched a worktree and is not its name, and no `--expect-session` was given |
| `tooLarge` | 1 | the message is over `limit`, or its frame is over the inbox's 1 MiB line |
| `empty` | 1 | nothing but whitespace, or unreadable |
| `writeFailed` | 1 | the connection or the write failed, or the inbox reset the connection during it |
| `badArgument` | 2 | `--from-name` has a quote, bracket, ampersand or control character, or `--reply-to` is not `uds:<a socket of yours in the inbox's directory>` |

A message that contains the header's own tags has their opening brackets
written as `&lt;`, so it cannot close the header and write outside it.

## `wt session stop <work> [<session>] --json` — the outcome

One background Claude session stopped with `claude stop <id>`, the attach id
`wt attach` uses. Its conversation is kept; `claude attach <id>` resumes it.
Only a background Claude session is stopped: one open in a terminal, a Codex
session and the session wt runs under are refused, and nothing is stopped.
`--dry-run` resolves and checks, and runs nothing.

<!-- json-docs:begin session-stop -->
| Field | Type | Required | Description |
|---|---|---|---|
| `schema` | `1` | yes |  |
| `schemaVersion` | string, matching `^1\.[0-9]+\.[0-9]+$` | yes |  |
| `command` | `"session stop"` | yes |  |
| `session` | object \| null | yes | The session chosen; null when none was. |
| `session.work` | string | yes | The worktree's name. |
| `session.path` | string | yes | The worktree's path. |
| `session.kind` | `"claude"` \| `"codex"` | yes | Whose session. A Codex session has no sessionId, no inbox and no transcript; its state is busy or idle. |
| `session.id` | string \| null | yes | The short id claude attach takes; null for a session open only in a terminal. |
| `session.sessionId` | string \| null | yes | The conversation's full id: what --expect-session takes. |
| `session.name` | string \| null | yes | The name the session goes by, cleaned of control characters; null when it has none. |
| `session.pid` | integer \| null, at least 2 | yes | Claude's process; null for a session with none, a parked background one. |
| `session.terminal` | boolean | yes | An interactive session, open in a terminal; false for a background one, even while attached. |
| `session.state` | `"busy"` \| `"idle"` | yes | As everywhere in wt: idle is a session waiting for its person; anything else, and anything not known to be idle, is busy. |
| `session.claudeState` | string \| null | yes | claude agents --json's state (working, blocked, done, failed, stopped), raw; null for a session with none. |
| `session.claudeStatus` | string \| null | yes | claude agents --json's status (busy, waiting, idle), raw; null while it has no process. |
| `session.waitingFor` | string \| null | yes | What a waiting session waits on (permission prompt, input needed), raw; null unless it is. |
| `session.inbox` | string \| null | yes | The inbox's socket, as uds: without the prefix: an absolute path. Null when inboxUnavailable says why. |
| `session.inboxUnavailable` | `"notRunning"` \| `"own"` \| `"codex"` \| `"noRegistry"` \| `"notASocket"` \| `"refused"` \| `null` | yes | Why there is no inbox. notRunning: no process. own: the session wt runs under; wt does not send to it. codex: a Codex session takes no messages. noRegistry: claude's registry file for the pid is missing, unreadable, or names another session. notASocket: the path is not a socket of yours in a directory closed to others. refused: it did not accept a connection. |
| `session.transcript` | string \| null | yes | The session's transcript file, when one is found. |
| `session.startedAt` | string \| null | yes | RFC 3339, UTC, from claude agents --json's startedAt; null when it has none. |
| `session.lastActivityAt` | string \| null | yes | RFC 3339, UTC: the modification time of the transcript file, one stat and no read, so it is there for a parked session too. Claude writes the transcript at every turn. Null when no transcript was found; never guessed. |
| `session.activity` | `"needsInput"` \| `"working"` \| `"idle"` \| `"done"` \| `"parked"` | yes | The one state word wt list and gittree agree on. parked: a background session with no process, whatever it was waiting for. needsInput: it has a process and is blocked or waiting on a prompt (waitingFor says which). done: claude's done, failed and stopped (claudeState has the raw word). working and idle as state busy and idle. A Codex session is working or idle. |
| `session.background` | boolean | yes | A claude --bg session, which has an attach id, whether or not it is open in a terminal now; false for one started in a terminal. |
| `session.shown` | boolean | yes | The session wt list shows for its worktree. |
| `session.repo` | string | yes | The repository's name; empty when it is not known. |
| `session.stateSince` | string \| null | yes | RFC 3339, UTC: when claude last changed the session's status, from statusUpdatedAt in its registry file ~/.claude/sessions/<pid>.json. Null for a session with no process, or no registry file. |
| `dryRun` | boolean | yes | Whether this was a dry run: nothing was stopped. |
| `outcome` | `"stopped"` \| `"already-stopped"` \| `"would-stop"` \| `"refused"` \| `"failed"` | yes | stopped: claude stop ran and succeeded. already-stopped: the session has no process, so there was nothing to stop (exit 0). would-stop: --dry-run, the session would be stopped and nothing was. refused: the session is not one wt stops, or could not be told from another (exit 1; ambiguous is 2). failed: no worktree or session was found, or claude stop failed (exit 1). |
| `reason` | string \| null | yes | Why for refused and failed, and a note for already-stopped; null otherwise. |
| `reasonCode` | `"noSession"` \| `"ambiguous"` \| `"changed"` \| `"interactive"` \| `"codex"` \| `"own"` \| `"noId"` \| `"notBackground"` \| `"noProcess"` \| `"stopFailed"` \| `"ownUnknown"` \| `"notExact"` \| `null` | yes | noSession: no worktree or no session matches (failed). ambiguous: several match, nothing chosen (refused, exit 2). changed: the session is not the one --expect-session named (refused). notExact: <work> is not exactly a worktree's name and no --expect-session was given (refused). ownUnknown: the process table could not be read, so wt cannot tell whether the session is its own (refused). interactive: the session is open in a terminal (refused). codex: a Codex session (refused). own: the session wt runs under (refused). noId: the session has no attach id claude stop could take (refused). notBackground: the worktree has no background Claude session (refused). noProcess: with already-stopped, a parked session. stopFailed: claude stop failed or timed out (failed). |
| `message` | string \| null | yes | What claude stop printed, stdout then stderr, trimmed; null when it printed nothing or did not run. |
<!-- json-docs:end -->

`reasonCode`:

| Value | Outcome | Exit | When |
|---|---|---|---|
| `noSession` | `failed` | 1 | no worktree matches, or no session in it, or none by that name |
| `ambiguous` | `refused` | 2 | several match, or several background sessions are running and none was named; nothing is chosen. `reason` lists them |
| `changed` | `refused` | 1 | `--expect-session` named a session that is not the one there |
| `interactive` | `refused` | 1 | the session is open in a terminal: a background conversation resumed there is listed as the terminal's. A background session someone has opened with `claude attach` is still a background session, and is stopped |
| `notExact` | `refused` | 1 | `<work>` only matched a worktree and is not its name, and no `--expect-session` was given |
| `ownUnknown` | `refused` | 1 | the process table could not be read, so wt cannot tell whether the session is the one it runs under |
| `codex` | `refused` | 1 | a Codex session |
| `own` | `refused` | 1 | the session wt runs under |
| `noId` | `refused` | 1 | the session has no attach id `claude stop` could take |
| `notBackground` | `refused` | 1 | none of the worktree's sessions is a background Claude session; `reason` lists them |
| `noProcess` | `already-stopped` | 0 | a parked session |
| `stopFailed` | `failed` | 1 | `claude stop` exited non-zero, was not found, or did not answer in 30 s; `message` has what it said |

## Changelog

Oldest first. Each row names every schema it changed, at the version that
change made it.

| Schema and version | Change |
|---|---|
| `status` 1.0.0, `up` 1.0.0 | `wt status --json` and `wt up --json`, as wt 53bc0d8 printed them |
| `status` 1.1.0, `up` 1.1.0 | `schemaVersion` in both outputs; the schemas published under `schema/` and by `wt schema` |
| `status` 1.2.0, `up` 1.2.0 | `trunkSync` in both: what a run did, or would do, to local trunk after its fetch |
| `sweep-plan` 1.0.0, `sweep` 1.0.0 | `wt sweep --dry-run --json`, the plan, and `wt sweep --yes --json`, the result |
| `sweep-plan` 1.1.0 | the `kept` values `operation`, `hiddenChanges`, `headUnreachable`, `reachUnknown`, `submoduleUnreachable` and `nestedWorktree` |
| `sync` 1.0.0 | `wt sync --json`, the overview |
| `sync-run` 1.0.0 | `wt sync run`, `resume` and `undo --json`, the result |
| `new-plan` 1.0.0, `checkout-plan` 1.0.0 | `wt new` and `wt checkout --dry-run --json`, the plan |
| `new` 1.0.0, `checkout` 1.0.0 | `wt new` and `wt checkout --json`, the result |
| `sync-run` 1.1.0 | `trunkSync`: what the run did to local trunk after its fetch |
| `sweep` 1.1.0 | `quarantine`, top-level and on each item: the folders `--quarantine` moved worktrees into |
| `sweep-plan` 1.2.0 | `quarantine`: the folder `--quarantine` names, which the token covers |
| `recovery` 1.0.0 | `<dir>/recovery.json`, the journal of `--quarantine` and `wt restore` |
| `restore-plan` 1.0.0, `restore` 1.0.0 | `wt restore <dir> --dry-run --json`, the plan, and `--json`, the result |
| `sweep` 1.2.0 | `superset` on each item: what came of a removed worktree's Superset workspace |
| `remove-plan` 1.0.0, `remove` 1.0.0 | `wt remove <work> --dry-run --json`, the plan, and `--yes --json`, the result |
| `checkout-plan` 1.1.0, `checkout` 1.1.0 | `source`, `remote`, `remoteRef` (and `remoteCommit` in the plan, `upstream` in the result): a branch created from a remote-tracking ref; problems `branchAmbiguous`, `remoteBranchMissing` |
| `new-plan` 1.1.0, `checkout-plan` 1.2.0, `status` 1.3.0 | a repository with no configuration file runs on detected defaults: `configured` false with no problem; `noConfiguration` only when no trunk can be detected either |
| `recovery` 1.1.0 | `restore.createdBranch`: this restore made the branch again, so a run after a failed one finishes its config |
| `quarantine-purge-plan` 1.0.0, `quarantine-purge` 1.0.0 | `wt quarantine purge <dir> --json`, the plan, and `--yes --json`, the result |
| `recovery` 1.2.0 | `purge`: the journal of `wt quarantine purge`, in `recovery.purging.json`, which replaces `recovery.json` before anything is deleted |
| `restore-plan` 1.1.0 | the problem `purging`: a purge began, so the restore refuses |
| `status` 1.4.0, `up` 1.3.0, `sync` 1.1.0, `sweep-plan` 1.3.0, `new-plan` 1.2.0, `checkout-plan` 1.3.0 | [`trunkSource`](#trunksource--how-trunk-was-found): how trunk was found. Also marks the fixed detection order: a conventional name before the checked-out branch |
| `remove-plan` 1.1.0, `sweep-plan` 1.4.0 | `keepSuperset`: `--keep-superset` was given, which the token covers |
| `remove` 1.1.0, `sweep` 1.3.0 | the `superset` result `optedOut`: `--keep-superset`, Superset was not asked |
| `remove-plan` 1.2.0 | `forceWith`, top-level and on each problem: the `--force` categories given, and the one that goes past each problem |
| `remove` 1.2.0 | `forced`: the `--force` categories the removal went past; `forceWith` on each problem |
| `remove-plan` 1.3.0, `remove` 1.3.0 | [`trunkSource`](#trunksource--how-trunk-was-found): how trunk was found |
| `status` 1.4.1, `up` 1.3.1, `sync` 1.1.1, `sweep-plan` 1.4.1, `new-plan` 1.2.1, `checkout-plan` 1.3.1 | detection is origin/HEAD, then the first of `development`, `main`, `master`, else it fails: `currentBranchGuess` is no longer produced, and a repository nothing names trunk for is `noConfiguration` — also one whose file has no `MAIN_BRANCH` |
| `refs-sweep-plan` 1.0.0, `refs-sweep` 1.0.0, `refs-restore-plan` 1.0.0, `refs-restore` 1.0.0, `refs-purge-plan` 1.0.0, `refs-purge` 1.0.0, `refs-swept` 1.0.0 | `wt refs sweep`, `restore` and `purge --json`, each plan and result, and `wt refs swept --json` |
| `sweep-plan` 1.5.0 | `configFingerprint`: the configuration file the token covers, so two plans can be compared |
| `sweep` 1.4.0 | `runId`, and `pin` on each item: the branches a sweep deletes are moved into a run under `refs/wt-swept/`. `restoreCommand` is now `wt refs restore <runId> --only refs/heads/<branch> --yes`, or `wt restore <dir>` for a quarantined worktree's branch; the `git branch` form is no longer produced |
| `remove` 1.4.0 | `runId`, and `pin` on each step: a deleted branch is moved into a run of its own under `refs/wt-swept/`; `restoreCommand` for it is `wt refs restore` |
| `status` 1.5.0, `sync` 1.2.0 | [Codex sessions](#sessions--claude-and-codex): `sessions[].kind` is `claude` or `codex` |
| `remove-plan` 1.4.0 | [Codex sessions](#sessions--claude-and-codex): `kind` on each of `sessions` |
| `remove` 1.4.1 | wording: a session problem is any agent session, and `sessionsUnknown` any listing that failed |
| `status` 1.6.0, `sync` 1.3.0, `up` 1.4.0, `sync-run` 1.2.0 | [a branch against its own remote](#ownremote--a-branch-against-its-own-remote): `ownRemote` on the worktree and each stack member of `status` and on each worktree of `sync`; `ownRemoteSync` on each participant of `up` and `sync-run`; `upIneligibleCode` `ownRemoteDiverged` and `ownRemoteBehind`; a token for a plan held back by divergence alone |
| `sync-run` 1.3.0, `sync` 1.3.1 | `wt sync run` is spelled `wt sync rebase`, and `--run` on `wt sync` is `--rebase`: `command` gains `sync rebase`, which the new spelling prints. From `sync-run` 1.3.0 on, `wt sync rebase <work>...` and `wt sync --rebase` exist. `sync` changes in wording only |
| `sync` 1.4.0, `status` 1.7.0 | [`deferredDeclared`](#deferreddeclared--the-deferred-steps-trunk-declares): the deferred steps a run may perform, on each worktree of `sync` and each stack member of `status` |
| `status` 1.8.0, `sync` 1.5.0, `up` 1.5.0, `sync-run` 1.4.0 | [where a branch pushes](#where-a-branch-pushes). New: `noPushReason` and `fixCommand` on `ownRemote` and on each participant of a run. Three things existing fields now mean, none of which a reader of the earlier minors could see: (1) a `rebased` participant can have `pushCommand` null, when wt has no push to make or offer for it, and `noPushReason` then says why; (2) `ownRemote.state` `unknown` also covers a branch a run would rebase and not push, which `noPushReason` tells apart from the unknowns a run pushes over unchecked; (3) `ownRemote.ref` is where wt's own push goes by the rule in [`ownRemote`](#ownremote--a-branch-against-its-own-remote): what is recorded for the branch first, never `<branch>@{push}` when that has another name than the branch. Every `pushCommand` now spells its refspec in full, `refs/heads/<branch>:refs/heads/<remote branch>` |
| `remove` 1.4.2, `remove-plan` 1.4.1, `sweep` 1.4.1, `sweep-plan` 1.5.1, `recovery` 1.2.1, `quarantine-purge-plan` 1.0.1, `quarantine-purge` 1.0.1 | `--quarantine <dir>` on `wt remove` and `wt sweep` is spelled `--move-to <dir>`, and `wt quarantine purge <dir>` is `wt purge <dir>`. From `remove` 1.4.2 on, `--move-to` and `wt purge` exist. Wording only: every field, value and schema name is as it was |
| `sync-run` 1.4.1 | wording: `wt sync run` and `wt sync --run` are no longer taken, so no wt prints `command` `sync run` any more; the value stays in the schema for results saved before |
| `new` 1.1.0, `checkout` 1.2.0 | the `cache` step, after `config`: cloning `WORKTREE_CACHE_DIRS` copy-on-write. `steps` holds eight steps where it held seven |
| `status` 1.9.0 | `upIneligibleCode` `alreadyOnTrunk`: the branch has no commit its trunk lacks, so there is nothing to rebase |
| `session-list` 1.0.0, `session-send` 1.0.0 | `wt session list --json`, a worktree's sessions, and `wt session send --json`, the outcome of a message |
| `session-stop` 1.0.0 | `wt session stop --json`, the outcome of a stop |
| `remove-plan` 1.5.0, `remove` 1.5.0 | [`--reap`](#processes--what-still-runs-in-a-worktree): `processes` and `processesError` in the plan, `reaped` in the result, the problem `processesUnknown` |
| `sweep-plan` 1.6.0, `sweep` 1.5.0 | [`--reap`](#processes--what-still-runs-in-a-worktree): `reap` on the plan and `processes` on each item, the `kept` codes `processes` and `processesUnknown`, `reaped` on each item of the result |
| `refs-sweep-plan` 1.1.0 | `keepPatterns`, and the `kept` code `deliberateBackup`: a ref matching `ref_keep_patterns` (default `backup/*` `backup-*` `*-backup`) is listed and never selected. `ref_sweep_patterns` now defaults to the transient names (`safe/*` `safety/*` `safe-*` `safety-*` `*-safe` `*-safety` `prefold/*`). `refs-sweep` changes in wording only: its `reason` for such an item is `deliberateBackup` |
| `sweep-plan` 1.7.0 | the `kept` code `deliberateBackup`: a merged branch whose name matches `ref_keep_patterns` is no longer deleted by `wt sweep`; it is an `inUse` item, and its worktree is not removed. `sweep` changes in wording only |
| `session-send` 1.1.0 | `errorCode` `notExact`: `<work>` only matched a worktree and is not its name, and no `--expect-session` was given; 1.0.0 said `noSession` |
| `config` 1.0.0 | `wt config --json`: resolved repository configuration, including developer config entries, main checkout and user naming overrides |
| `sync` 1.6.0, `sync-run` 1.5.0, `status` 1.10.0 | [`each_commit` checks and `needs`](#needs--what-a-step-cannot-run-without): `eachCommitDeclared`, `needs`, and `checks` and `checkCommits` on each worktree of `sync`; `needs` on every declared step of `sync` and `status`; `checked` and `checkFailed` on each participant of `sync-run`, and the `deferred` result `owed` |
| `sync` 1.7.0, `sync-run` 1.6.0 | `stack`: the stack held on one line, on the overview (`--stack`) and on a `--stack` run's participant, with each layer and what came of it |
| `sync-run` 1.7.0 | [`layerSteps`](#layersteps--a-step-at-every-layer-tip) and `rechecked` on each participant: what a step with `each: layer` or `fold: owner` came to at each layer; `command` `sync steps` for `wt sync steps` |
| `sync` 1.8.0, `sync-run` 1.8.0, `up` 1.6.0 | [`target`](#target--trunk-or---onto): what `onto` is, trunk or the ref `--onto` names, with `--upstream`'s commit; `onto` is that target's commit. `wt sync <work> --onto <ref> --json` is the overview of that one worktree, and its `token` covers the target |
| `sync` 1.9.0, `sync-run` 1.9.0 | `edit` on [`stack`](#stack--a-stack-held-on-one-line): what `wt sync rebase <work> --stack --edit <rev>` did to that commit, the paths its amend changed and the commits it emptied; null in the look and for any other run |
| `sync-run` 1.10.0 | [`emptied` and `testsOnly`](#dropped--a-commit-a-rebase-emptied) on each participant: the commits a finished rebase left nothing of, and, with `test_paths` declared, those it left only the tests of |
| `sync-run` 1.11.0 | `eachCommit` on each participant: `all`, `tips` or `off`, the run's `--each-commit` mode; the commits skipped under `tips` and `off` are not owed |
| `sync-run` 1.12.0 | `adopted` on each participant: what `wt sync resume --adopt` took over, the ref keeping the tip it found, where the rebase finished, and the commits made after it |
| `sync-run` 1.13.0 | `leftAlone`: what a `wt sync rebase <work> --alone` run left as it was: the worktrees stacked on the same history as the one it rebased, or with `--stack` the layer branches of its line |
| `sync` 1.10.0, `status` 1.11.0 | `needs` takes `env:<NAME>` as well as `docker`: a step that needs an environment variable that must be set and not empty; the probe never carries the value |
| `sync` 1.11.0, `sync-run` 1.14.0 | `contextGaps` and `contextUnchecked` on [`stack`](#stack--a-stack-held-on-one-line): for a fold, the hunks of fixups that lack their context behind their target, each with the later commit that carries it; a `wt sync resume` refused for a resolution that drops such a hunk is `refused` with the lines in `reason` |
| `up` 1.7.0, `sync-run` 1.15.0, `status` 1.12.0 | [`layers`](#layers--branches-in-no-worktree) on each participant of `up` and `sync-run`: the branches in no worktree on its line that the run moved with its rebase or left, and why; `layers` on each `stack` member of `status`, which its token covers |
| `sync` 1.12.0, `sync-run` 1.16.0 | [`sideBranches`](#side-branches--what-hangs-off-the-line) on `stack`: the branches forked from the line or above its top, which a `--stack` run carries with it, and what came of each |
| `new` 1.2.0 | `addresses` on `wt new --json`: where the new worktree's dev servers are reached, one `{app, url}` per app devports gives a domain; `[]` without devports |
| `sync` 1.13.0, `sync-run` 1.17.0 | `top` on [`stack`](#stack--a-stack-held-on-one-line): the line's own branch, the last row of the look's layer table; `sameTip` on each side branch: branches at one tip are siblings, carried or held together, and none is `on` another |
| `sync-run` 1.18.0 | [`fold`](#fold--what-a-fold-compared) on each participant: for a fold of the line, whether it is in place and on which base, and what its tree guard compared and found |
| `sync-run` 1.19.0 | `eachCommit` gains `targets`: the run's `--each-commit=targets` mode, checks at each layer's tip and at each commit a fixup folded into |
| `sync-run` 1.20.0 | [`timings`](#timings--where-the-time-went) on each participant: the run's wall time, how many times each check and step ran and its total, and the slowest runs; `elapsedMs` on each [`layerSteps`](#layersteps--a-step-at-every-layer-tip) entry. All in milliseconds |
| `remove-plan` 1.6.0, `remove` 1.6.0 | `staleRoutes` (on `branch` in the plan, top level in the result): for a kept branch, each remote branch it could push to that holds commits it lacks, as last fetched, with how (`via`), whether the removal takes the route away (`dropped`) and whether it forces (`forces`). The problem code `staleForcedPush` |
| `sync` 1.14.0, `sync-run` 1.21.0 | [`push`](#push--a-stacks-push) on `stack`: for a `--stack` run that pushed, each moved layer and the line's own branch with the lease, the commit pushed and what came of it, in one atomic push per remote; null in the look and when nothing was pushed |
| `sync-run` 1.22.0 | `wt sync push <work> --stack --json`: `command` gains `sync push` and `result` gains `pushed`; the row's `stack.push` is the push a `--no-push` run left for later |
| `sync-run` 1.23.0 | `rule` on each branch of [`push`](#push--a-stacks-push) (recorded by `wt sync push-to`, or its own name) and `lineLeftOut`: why a stack push left the line's own branch out (pushing it would create its remote branch; `--push-line` includes it) |
| `sync-run` 1.24.0 | `worktrees[].fold.guard.resolvedAtStop`, present only when a fold took a person's resolution at a stop as intended |
| `sync-run` 1.25.0 | `from` on each branch of [`push`](#push--a-stacks-push) (the run its tip is from), `push.conflicted` (branches the unpushed runs disagree on) and `worktrees[].droppedRecord` (what `wt sync push --stack --forget` dropped) |
| `sync` 1.16.0 | `likelyStops` and `reworkedOnTrunk` on each worktree: later commits that share a file with what trunk changed, and replayed commits trunk carries under the same subject and author; advice, never a verdict |
| `sync-run` 1.26.0 | `untouched` among the `result` values of `worktrees[].layerSteps[]`: the run left the layer's tip as it was, so the step did not run (`ran` is false) |
