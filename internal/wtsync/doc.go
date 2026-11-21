// Package wtsync keeps worktrees rebased on trunk: it reads a repository's
// declaration of its conflict shapes, resolves those shapes deterministically,
// and simulates a rebase before anything is touched (an autosquash fold's
// stops are the exception: a run says it does not simulate them).
//
// # Module map
//
// The package is flat, but its files fall into six parts. Each part lists
// its files and what they own; the command layer (internal/commands/synccmd,
// syncrun, syncfix, synckeep, syncwait) enters each part through the exports
// named under "Used from outside": the main entry points, not every export
// the command layer calls.
//
// # 1. Declaration and strategies
//
// What .wt-sync.yaml says and how each named strategy resolves a conflict.
//
//   - config.go: Config, Rule, Deferred, Parse, LoadFromTrunk, LoadFromRef,
//     MatchGlob; the declaration read from trunk and its steps. (--layer
//     patterns are not declared: ReadLine and CheckLayerPatterns in
//     stack_line.go take them.)
//   - strategy.go: the Strategy interface and kinds, the one table of the
//     strategies a rule may name (what it must say, how to build it).
//   - bullet_union.go, list_union.go, owned_line.go, openapi.go, script.go,
//     take_trunk.go: one strategy each.
//   - lift.go, rules.go: the version rules: a version bump carried over a
//     conflict, and lifted after a clean merge where no strategy ran
//     (replay.go calls it), and the value rules they use.
//   - merge3.go, conflict.go, resolve.go: the three-way merge the strategies
//     share, Conflict (the three blobs), Refusal and Resolution (the answer).
//
// Used from outside: Config, ConfigFile, ErrUndeclared, LoadFromTrunk,
// LoadFromRef, Deferred, MatchGlob.
//
// # 2. Simulation and assessment
//
// What a rebase would do, worked out in the object store with nothing of the
// user's touched, and the class a worktree falls in.
//
//   - replay.go, simtree.go, stages.go: SimulateRebase and its plumbing: each
//     commit merged with merge-tree, resolved paths written into a tree.
//   - triage.go: Assess, AssessOwn, AssessTo, Assessment and Class.
//   - target.go: Target, what a rebase is assessed against and replayed onto.
//   - landing.go, emptied.go: what landed on trunk, and the commits a rebase
//     drops (Dropped, ReplayPlan, ReportReplay).
//   - context.go: ContextGap, a fixup hunk whose context a later commit adds.
//
// Used from outside: AssessOwn, AssessTo, Assessment, Target,
// FileOutcome, KeyGroup, KeyCounts, LandingList, Dropped, ReplayPlan,
// ContextGaps, GapsAt, BehindAhead.
//
// # 3. Rebase driver
//
// The run itself. One unexported driver value carries a run; its methods sit
// in several files, one phase each.
//
//   - rebase.go: Preflight, Rebase and Resume; the driver's loop (drive), a
//     stop's lift (liftStop), finish, handover and restore, and
//     VerifyFinished.
//   - todo.go: todoPlan, what the run does to the sequencer's list.
//   - each_commit.go: the each_commit checks, planned onto commits, run at
//     them, and what a failed or missing one leaves unchecked.
//   - fold.go: the per-layer steps and the fold in place of a stack held on
//     one line (layerSteps, foldInto, restoreLayers).
//   - edit.go, edit_conflicts.go, edit_replay.go: the stop of a --stack
//     --edit run and the plumbing that reads and replays across it.
//   - stage.go: StagedConflicts and RebaseProgress, read from the index and
//     the rebase's own state.
//   - defer.go, needs.go: the deferred steps (RunDeferred, RunAfterSteps) and
//     the needs that can make one unavailable.
//   - adopt.go: wt sync resume --adopt, taking over a tip.
//   - recheck.go: Standing, what a verb re-reads once it holds the lock.
//   - timings.go, tempdir.go: timing rows of checks and steps, and the
//     temporary directories a simulation or run opens.
//
// Used from outside: Preflight, Verdict, Request, Handover, Result, Rebase,
// Resume, StripMarks, MarksLeft, VerifyFinished, VerifyLeft, StopPaths,
// AtHandoverStop, TodoHash, Recheck, RunAfterSteps, ChangedPaths,
// DeferredResult, StepsDue, Due, ProbeNeedsWith, Adopt, ProveAdoption,
// EachCommitMode, CheckStopOf, Unchecked, CheckFailure, LayerStep,
// StagedConflicts, RebaseInProgress, Timings, TimingSummary,
// RemoveTempDirs.
//
// # 4. Stack model
//
// Which branches are a stack, and how its layers move.
//
//   - stack.go: Parents, Members, Order, Descendants: the stack relation
//     across worktrees.
//   - stack_line.go: a stack held on one line: ReadLine, LineLayer, the layer
//     patterns, StackRequest and the drops and reconciliation it needs.
//   - stack_move.go: LayerHolder (what holds a layer), MoveLayers, and the
//     stack record a run keeps (StackRecord).
//   - descendants.go: side branches off the line, ReadSides and CarrySides.
//   - loose_layers.go: branches in no worktree that a run moves with the
//     line (ReadLoose, LooseLine).
//
// Used from outside: Parents, Members, Order, Descendants, ReadLine,
// LineLayer, CheckLayerPatterns, CheckLayers, StackRequest, LayerPlan,
// ReadHolder, LayerHolder, MoveLayers, LayerMove, StackRecord,
// WriteStackRecord, ReadStackRecord, ForgetStack, ReadSides, CarrySides,
// ReadLoose, LooseLine.
//
// # 5. Durable state and safety
//
// What a run leaves on disk so that it can be resumed, undone or refused.
//
//   - safety.go: the safety refs, result refs and pins (Safety, SafetyRef,
//     WriteResult, Pin, Disown).
//   - lock.go: the per-worktree lock (Acquire, TakeOver, LockLive,
//     ProcessRunning, HeldLocks).
//   - plan.go: the plan file and the State handed over at a stop
//     (WritePlanFile, WriteState, ReadState) and the one-line wordings of the
//     verbs' outcomes.
//   - owed.go: the deferred steps a run left owed.
//   - undo.go: Undo and UndoBranches, putting a line back.
//   - doctor.go: Doctor, the check of all of the above.
//   - way.go: Way and WayOut, the one sentence for where a verb left a
//     worktree.
//
// Used from outside: Safety, SafetyRef, WriteResult, Pin, Disown, Lock,
// Acquire, TakeOver, ReadLock, HeldLocks, ProcessRunning, GitDir, State,
// PlanPath, PlanHolders, RemovePlan, RenderPlan, WritePlanFile, WriteState,
// ReadState, Owed, OwedStep, ReadOwed, WriteOwed, Undo, UndoBranches,
// Restored, Doctor, DoctorOptions, Check, Way, HandoverWay, WayOut.
//
// # 6. Own remote
//
// A branch against its own remote.
//
//   - ownremote.go: Own, ReadOwn, OwnState; where a push goes (Push), the
//     fetch a run asks for (OwnAsk, with its refspec), and what blocks a
//     fast-forward or a push (OwnBlocks). The fast-forward itself is
//     syncrun's (own_remote.go).
//   - rename.go: RenameKeepingPush, a branch's rename that keeps its pushes
//     where they went.
//
// Used from outside: RenameKeepingPush, Own, ReadOwn, ReadOwnOrUnknown, OwnState, OwnRemote,
// OwnBlocks, Push, OwnAsk, Remotes.
package wtsync
