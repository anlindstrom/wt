# Testing wt

Run the tests that cover what you changed while you work, and the whole
check once before you hand the work back. The full suite takes about 4
minutes on the shared Mac mini. The packages run side by side, most of their
tests run in parallel within them, and they build real git repositories;
the slowest are `internal/commands/synccmd` and `syncfix`. Run the suite
once, in the background, and keep working; never run it in a loop.

## Targets

| target | runs | wall time |
|---|---|---:|
| `make test-unit` | the packages whose tests build no git repository, with `-short` | about 8 s |
| `make test-fast` | `go test -short -timeout 20m ./...` | minutes |
| `make test` | `go test -timeout 30m ./...` | about 4 minutes |
| `make test-race` | `make test` under the race detector, as CI runs it | slower than `make test` |
| `make bats` | builds `bin/wt`, then every `test/*.bats` | about 140 s |
| `make test-mod` | the wt mod's suite, `claude plugin test mods/wt` | about 2 s |
| `make lint` | `golangci-lint run` | |
| `make check` | lint, test, bats and test-mod | about 7 minutes |

The `make test` time was measured on the Mac mini on 2026-11-21 with other
jobs sharing it; the suite itself drives the load past 50, so it is bound by
CPU, and a machine with fewer cores gains less from the parallel tests. The
other times are older. Several full runs at once can pass the 30 minute
timeout.

The tests are written against git 2.56 (Homebrew's; `gittest.MinGit`). Apple's
git 2.54 checks and replays some rebases differently, and two tests,
`TestEachCommitTipsDoesNotCheckALayerTheTargetAlreadyCarries` and
`TestFoldOfAStackEndToEnd`, call `gittest.Need(t, "2.56")` and fail with the
version they found: put `/opt/homebrew/bin` first on the PATH. A new test that
depends on a newer git's behaviour does the same.

`make test-unit` covers internal/config, find, naming, about, quarantine,
gittest, superset, textwrap, jsonout, refpins, schema, and
internal/commands/testkit. internal/git, internal/github and
internal/reposel are not in it: their tests build real git repositories.

## On the Mac mini

Anything that runs for minutes, the whole check above all, goes to the Mac
mini through offload, so its load and its temporary repositories stay off
the Mac being worked at. `.offload.toml` names the commands:

| command | runs |
|---|---|
| `offload run check` | `make lint test bats`: `make check` without test-mod, which needs claude |
| `offload run test -- <packages and flags>` | `go test`, `./...` with no words: `offload run test -- ./internal/wtsync -run 'Fold'` |
| `offload run test-fast` | `make test-fast` |
| `offload run lint`, `offload run bats` | the one target |

`offload run` sends the worktree as it is, uncommitted changes included, and
prints the job id first. Wait with `offload wait <job>` as a background
command; its exit code is the job's. The heavy commands take the host's one
heavy slot, so suites from several worktrees queue rather than run at once.
A package run with `-run` takes seconds here and needs no offload.

## What to run when

| you changed | run |
|---|---|
| a comment, docs, whats-new, wording only | nothing, or `go vet ./...` |
| one test or one function | `go test ./internal/<pkg> -run 'TestName$' -count=1` |
| internal/config, find, naming, about, quarantine, refpins, superset, timemachine, github, git, repo, reposel, jsonout or gittest | `go test ./internal/<pkg>/...`; each is under 50 s, most under 5 s |
| internal/wtsync | `go test ./internal/wtsync -run '<Area>'` first, then the whole package once (about 110 s) |
| internal/agents | `go test ./internal/agents` (a few seconds); some of its tests read the real process table |
| internal/commands/syncrun | `go test ./internal/commands/syncfix ./internal/commands/synccmd -run 'Push\|OwnRemote\|FastForward\|Finish'`: syncrun's behaviour is tested from those two packages, so `go test ./internal/commands/syncrun` alone covers almost nothing |
| goroutines, locks, signals or anything shared between them | `make test-race` once, in the background: CI runs the suite with `-race` and `make check` does not |
| internal/commands, one command family | `go test ./internal/commands/<pkg> -run '<Family>'`, see the examples below; the whole `./internal/commands/...` once before you finish, on the mini |
| a package-level hook variable or `Context` | `./internal/commands/...` once: the hooks live in kit (`FindGitHub`, `FindTmutil`, `MachineAgents`), supersetws (`Probe`, `HostCommand`) and session (`SessionsDeadline`); `testkit.Main` sets kit's, every TestMain the rest, and serial tests swap them, see "Parallel tests" |
| the `--json` shape or `schema/*.json` | the schema test and the JSON tests, see the examples below, and update docs/json.md in the same commit |
| cmd/wt flags, help text, completion | `go test ./cmd/wt -run` the family; the whole package takes about a minute |
| mods/wt | `make test-mod`; a change to its fixtures also `go test ./internal/commands/testkit -run Mod` |
| shell/, install.sh, stdout, stderr or exit codes | `make build && scripts/bats.sh test/<file>.bats`, the one file |
| before handing the work back | `offload run check` once, waited on in the background (see "On the Mac mini"); after a fix, run the touched family only |

The command packages, and what each holds:

| package | holds |
|---|---|
| `internal/commands` | wt list and wt status, and the tests that cross packages |
| `internal/commands/kit` | `Context`, Open/Locate, runs across repositories (`Bulk`, `AcrossRepos`) and the one question they ask, GitHub glue and its cache, the Time Machine exclusion, tables, the signal watch |
| `internal/commands/testkit` | fixtures and fakes the tests share; `testkit.Main` is the TestMain of every command package but kit |
| `internal/commands/session` | wt session, wt attach |
| `internal/commands/provision` | wt new, checkout, setup, init, config, port |
| `internal/commands/supersetws` | registering a worktree as a Superset workspace, and deleting that workspace once wt removes the worktree |
| `internal/commands/retire` | wt remove, wt sweep: they make quarantines and pin the branches they delete |
| `internal/commands/quarantinecmd` | wt restore, wt purge: they end a quarantine; the store is `internal/quarantine` |
| `internal/commands/refs` | wt refs sweep, swept, restore and purge; the pin store under `refs/wt-swept/` is `internal/refpins` |
| `internal/commands/syncrun` | what a sync run is made of: local trunk's fast-forward, own remotes, push, the idle-session check, finishing a rebase, the run's journal, what a signal says about a rebase in flight |
| `internal/commands/synccmd` | wt sync, wt up, wt sync rebase, push-to, doctor |
| `internal/commands/syncfix` | wt sync resume, wt sync undo |
| `internal/commands/synckeep` | wt sync keep; `keepstate` beneath it holds the keeper's state, lock and launchd job |
| `internal/commands/admin` | wt doctor, migrate, adopt, pr, hook, repos |

The `<pkg>test` packages (provisiontest, retiretest, refstest,
quarantinecmdtest, synccmdtest) hold the fixtures of one family for the
tests of other packages.

Examples of the -run patterns:

```
go test ./internal/commands/refs -run 'Refs|Pins|Token|Unique|NilJournals'
go test ./internal/commands/quarantinecmd -run 'Restore|Purge|Quarantine'
go test ./internal/commands/synccmd -run 'Sync|Up|Push|FastForward|Handover|Plan'
go test ./internal/commands/syncfix -run 'Resume|Undo'
go test ./internal/commands/synckeep -run 'Keep'
go test ./internal/commands/retire -run 'Remove|Sweep|Quarantine|Apply'
go test ./schema ./internal/commands/... -run 'JSON|Schema|Matches'
```

-run picks tests by name, not by file, so a pattern misses the few
tests of a family whose names say something else. Name the package: a
-run pattern against the wrong one selects no tests, and go test then
prints `ok` with `[no tests to run]`, which looks like a pass. The run of
`./internal/commands/...` before you finish covers the rest.

## JSON output

A test of a `--json` output passes it to `testkit.ValidateJSON`, usually
through the family's helper (`retiretest.RemoveJSON`,
`synccmdtest.DecodeSyncRun`, `provisiontest.NewPlanOf`). It validates the
output against the schema in `schema/` with every listed object closed:
a key the schema does not declare fails the test, at any depth. A field
added to the output therefore needs its schema property, the docs/json.md
row and an x-version minor in the same commit. The shipped schemas stay
open; only the test closes them.

The wt mod's tests (mods/wt/tests) answer it with wt output kept in
`mods/wt/tests/fixtures/<schema>.ts`, each `export default` and the JSON,
because a mod can import only code. `TestTheModsFixturesMatchTheirSchemas`
in internal/commands/testkit validates every one against its current
schema, so a JSON change that breaks the mod's input fails `go test`; bring
the fixture up to the new schema in the same commit.

## Bats

`test/helpers.bash` keeps the bats tests off the machine running them, as
`gittest.Isolate` does for the Go tests: HOME, XDG_CONFIG_HOME and git's
global config are the run's own (with an identity to commit as), git reads
no system config and no GIT_ variable from the environment, WT_ROOTS and
CODEX_HOME are unset, and no claude and no tmutil are on the PATH. Go's
caches stay where they were. Every `test/*.bats` loads it; a test that wants
a claude or a tmutil puts its own fake first on the PATH, and one that wants its own git config sets
GIT_CONFIG_GLOBAL in its setup. CI runs bats through `scripts/bats.sh`, under
the same deadline as `make bats`.

## Parallel tests

A test that calls `t.Parallel()` runs beside the other parallel tests of
its package, and Go starts them only once every serial test of that
package has finished. Most tests in the command packages, internal/wtsync,
internal/repo, internal/github and internal/superset are parallel: each
builds its own repositories under `t.TempDir()`. HOME and XDG_CONFIG_HOME
are set once per test binary and shared, so a parallel test does not write
under them; `testkit.UserConfigIn` makes a test serial.

A test stays serial when it touches what the whole test binary shares,
and its first line says which, as `// Not parallel: <why>`:

- `t.Setenv` or `t.Chdir`, which Go refuses in a parallel test: a fake
  `git`, `claude`, `superset` or `tmutil` on the PATH, HOME, XDG_CONFIG_HOME,
  CODEX_HOME, TMPDIR;
- a package-level variable that other tests read, for example
  `kit.MachineAgents`, `retire`'s `vanishWait`, `refpins.AfterBranchMoved`,
  `supersetws.Probe` and `HostCommand`, provision's
  `cloneDir` and `cloneOneFile`, `synccmd`'s `assessWorkers`,
  superset's `readDeadline`, agents' process listers and `Deadline`;
- signals: `git.Interrupt` and `git.KillRunning` signal the process groups
  `RunBounded` is waiting on, `syncrun.OnInterrupt` removes every temporary
  directory wtsync has open, and a few tests send a signal to the test
  process itself; a test that only re-runs itself through
  `testkit.InterruptedChild` shares nothing with that child and is parallel;
- the registries of the locks held and the interrupt journal, which belong
  to the process;
- a wall-clock bound that a loaded machine can break.

Every serial test of a package finishes before its first parallel test
starts, so a signal from a serial test never reaches a parallel test's git.
Keep it that way: a test that signals or swaps a hook does not call
`t.Parallel()`.

A test's own fakes go with the call, not into a package variable:

- `testkit.FakeGitHub(t, ctx, …)` and `testkit.StubGitHub(ctx, …)` set
  `ctx.GitHubFake`;
- `provisiontest.FakeSuperset`, `Stub` and `SupersetAt` set
  `ctx.SupersetFake`;
- `testkit.ListedSessions(ctx, …)`, `ClaudeLists(ctx, …)` and
  `retire.RealProcesses(ctx)` set `ctx.Agents`, the sessions and processes
  the context sees; `testkit.AgentsOf(ctx)` is that struct, for any other
  field, as a failing listing;
- `testkit.CrashAt(point)`, or any `quarantine.Hooks` (a failing config
  write of a restore among them), goes in the `QuarantineHooks` field of
  the remove, sweep, restore or purge options, and what a removal does
  after its last look at the checkout, or between a branch's ref and its
  config, in `RemoveOptions.AfterFinalCheck` and `AfterRefDeleted`.
- the platform, the home directory and the launchctl the keeper sees go
  in `ctx.Machine` (`kit.Machine`), and the `--all` commands of wt sync
  keep take the machine they open each repository on: a zero field is
  the machine's own, so no test sets HOME or PATH for them.
- the roots `kit.Find` scans go in `ctx.Roots`, so `wt attach` finds a
  test's own fixture without WT_ROOTS; a command across repositories
  (`--all`) still reads WT_ROOTS, and its test is serial;
- a docker that answers, for a step that declares `needs: [docker]`, goes in
  the run's options as `Probes` (`synccmdtest.DockerUp()`, already in
  `synccmdtest.NoAgents()` and `syncfix`'s `noResumeAgents`); `DockerDown()`
  is the one that fails. A wt run in a child process takes its docker from the
  child's own PATH: `synccmdtest.WriteFakeDocker(t, dir)` and `cmd.Env`, as
  cmd/wt's sync-wait tests do;
- the claude `wt session stop` runs goes in the session env's `claude`
  (`fakeClaudeStop`), not on the PATH;
- a keeper pass turns commit signing off with `git.NeverSign`, on the
  gits it starts rather than in the process environment; it is only ever
  turned on, and no test depends on a signed commit, so keeper passes run
  side by side.

The package variables behind them (`kit.FindGitHub`, `kit.FindTmutil`,
`kit.MachineAgents`, `supersetws.Probe`) are set once, in each
package's TestMain: `testkit.Main` sets kit's three, to no gh, no tmutil, no
session and no process, and kit's own TestMain repeats it. No test runs the
real tmutil. Only serial tests swap
them, as `provisiontest.SupersetSaying` and `StaleSuperset` swap
`supersetws.Probe`, and `testkit.FakeTmutil` puts a fake tmutil first on the
PATH, the real lookup back in `kit.FindTmutil` and `darwin` in the
context's `Machine`, and parallel tests only read them. A command that opens a
context of its own, as `--all` does for each repository, does not see the
fakes on the test's context; `testkit.MachineClaudeLists` swaps
`kit.MachineAgents`' claude for such a test, which is then serial.

Only the tests of process detection read the real process table:
`testkit.FakeClaude`, which leaves out the session running the test, and
`retire.RealProcesses`. `testkit.Main` gives ps a minute, `agents.Deadline`,
where wt gives it 10 s: a machine running several suites at once has taken
longer than that to start it.

A new test calls `t.Parallel()` first, unless one of the reasons above
applies.

## Time per package

Wall time of each package in one `go test -count=1 ./...` run, measured on
the Mac mini on 2026-11-21 while other sessions' jobs shared it (load 2 per
core and more), so a quiet machine is faster: the same synccmd run took 117 s
and syncfix 102 s when the mini was quiet. "serial" is the part spent before
the first parallel test started; the tests that stay serial run one after
another. Only the serial rows below were measured separately.

| package | s | note |
|---|---:|---|
| internal/commands/synccmd | 182 | the stack and fold tests build real repositories |
| internal/commands/syncfix | 172 | the same |
| internal/wtsync | 101 | |
| internal/commands/retire | 85 | the remove and sweep --reap tests: real processes, signals, a 5 s grace |
| cmd/wt | 62 | the sync-wait tests build a wt binary (`go build`) once and run it in child processes |
| internal/commands/quarantinecmd | 55 | wt restore and wt purge |
| internal/commands/provision | 32 | |
| internal/commands/admin | 25 | |
| internal/commands | 24 | wt list and status, and tests across packages |
| internal/commands/refs | 21 | |
| internal/commands/synckeep | 13 | |
| internal/repo | 10 | |
| internal/superset | 7 | about 4 s of it is real deadlines |
| internal/git | 5 | |
| everything else | under 5 each | most build no git repository |

The whole `go test ./...` takes about 4 minutes there (3 min 46 s for main
before the parallel fold tests); `offload run check`, with lint and bats, took
4 min 21 s.

Go runs the packages side by side, so the suite takes about as long as
the slowest package. Before the split internal/commands alone took 785 s
of a 786 s run; before the tests ran in parallel the suite took about
230 s, with internal/commands/synccmd at 223 s.

## What -short skips

A test that is too slow for the working loop starts with

```go
if testing.Short() {
	t.Skip("waits out a real deadline")
}
```

and `make test-fast` leaves it out. `make test` and `make check` still
run it. The guarded tests are:

- the loops that stop a restore, a removal, a purge or a sync at every
  step and run it again over real repositories;
- TestRefsPlanTime, which builds 300 commits and 150 refs to time the
  plan;
- the slowest of the tests that wait out a real deadline, kill a process
  that does not answer, or interrupt a run with a signal; not every one
  is guarded;
- the slow tests, a second or more, which build an origin, clones,
  worktrees and stacks, in the packages refs, purge, synccmd, syncfix,
  synckeep, admin and internal/commands (the own_remote, push_to,
  sync_push, sync_run, sync_resume and sync_undo files among them;
  `grep -rln testing.Short --include='*_test.go' internal/commands`
  lists the files). One test each of sync undo, sync resume, sync
  push-to and own-remote stays in, so -short still runs those commands:
  TestSyncUndoJSONReportsWhatItPutBack,
  TestSyncResumeJSONReportsTheFinishedRebase,
  TestSyncPushToRecordsSaysAndForgets and
  TestUpRefusesWhenTheFastForwardItselfFails.

internal/wtsync keeps its push and undo tests under -short: they guard
against pushing over someone's commit and against losing one, and the
package is not the slowest one anyway.

`grep -rn -B1 'testing.Short' --include='*_test.go' .` lists them. They
were about 440 s of the old single internal/commands package and 22 s of
internal/wtsync. Even without them the command packages have several
hundred tests left that each build a repository, so `make test-fast`
still takes minutes; `make test-unit` is the loop that finishes in
seconds.

Guard a new test the same way when it waits on a clock, or when it
builds an origin or a stack and you would not want it in a loop.
