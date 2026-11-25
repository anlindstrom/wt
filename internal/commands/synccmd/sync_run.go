package synccmd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/agents"
	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// RunOptions tunes SyncRebase for callers and tests.
type RunOptions struct {
	NoFetch bool
	// NoFFTrunk leaves local trunk where it is after the fetch, as the user
	// setting ff_trunk false does.
	NoFFTrunk bool
	// Unattended is a run nobody is watching: wt sync keep once. With nothing
	// named it leaves a worktree with any session in it alone, idle or busy,
	// since there is nobody there to ask on its behalf.
	Unattended bool
	// IfReady rebases only what will go through without handing anything
	// to a person: clean, or every stop resolved by a verified strategy —
	// what the overview files under ready. A named worktree that is not is
	// refused, with its stack, and makes the run fail; with nothing named,
	// every worktree left behind trunk does.
	IfReady bool
	// AllowDiverged rebases a branch that has diverged from its own remote
	// as it stands: the remote has commits the branch never had, and the
	// push after it would replace them. It lifts nothing else.
	AllowDiverged bool
	// Force rebases a named worktree even with an agent session in it, busy
	// or idle: the sessions are named, not checked. Nothing else it refuses
	// is lifted — dirt, a handover, another run's lock. A run with nothing
	// named does not take it: that would roll over every session at once.
	Force bool
	// Alone rebases the one worktree named and none of the others stacked on
	// the same history (--alone): they are not refused on, moved or touched.
	Alone bool
	// EachCommit is where the each_commit checks run: "all" (also "") at
	// every replayed commit that touches their paths, "tips" at the end of
	// each layer only, "targets" there and at each commit a fixup folded
	// into (an in-place fold), "off" nowhere.
	EachCommit string
	// Journal records what the run does, participant by participant, for
	// --json; nil records nothing.
	Journal *syncrun.RunJournal
	// Expect is the token wt sync --json gave for the overview: the run
	// refuses, touching nothing, when what it would start on is no longer
	// what the overview showed. Empty checks nothing.
	Expect string
	// planExpect is wt up's: Expect is the token wt status --json gave for
	// the plan, and names trunk, the configuration and the stack.
	planExpect bool
	// label is the run's first word, "wt up" for the short form.
	label string
	// undeclaredOK lets a trunk with no .wt-sync.yaml be rebased onto, with
	// nothing declared: no strategies, no deferred steps. With IfReady that
	// takes a conflict-free rebase and nothing else, which is all wt up asks.
	undeclaredOK bool
	// Stack rebases the one worktree named as a stack held on its line,
	// and moves every layer branch on that line to its new tip.
	Stack *StackOptions
	// Steps runs the declared steps without rebasing onto trunk: wt sync
	// steps. Nil for a rebase.
	Steps *StepsOptions
	// Onto is the ref the named worktrees are rebased onto instead of
	// trunk (--onto); empty is trunk. Upstream, with it, replays only the
	// commits above that ref (--upstream).
	Onto, Upstream string
	// Own is the remotes as a caller that already fetched read them: the
	// keeper, and a run across repositories, which fetch before the run
	// they then start with NoFetch.
	Own *wtsync.Own
	syncrun.VerbOptions
	syncrun.PushOptions
	syncrun.StepOptions
}

// StepsOptions is wt sync steps: the line, or the branch, rebased in place
// onto its own base, with the steps run at every layer whatever changed.
type StepsOptions struct {
	// All runs every declared step; otherwise only what the last run left
	// owed.
	All bool
	// EachCommit is --each-commit: where the each_commit checks run. Empty
	// keeps the mode of the run that owed the steps.
	EachCommit string
}

// StackOptions is a run of a stack held on one line (--stack).
type StackOptions struct {
	// Layers names the layer branches; empty takes every local branch
	// whose tip is on the line, but backups.
	Layers []string
	// Autosquash folds the line's fixups: onto its own base, in place, or,
	// with Onto, as the line moves there.
	Autosquash bool
	// Edit is the commit of the line the run stops at for a person to
	// amend (--edit), as given; empty for none.
	Edit string
	// NoSideBranches leaves the branches that hang off the line alone, as
	// they were before wt carried them (--no-side-branches): none is read,
	// listed, marked or moved.
	NoSideBranches bool
	// AfterMove is a test's hook into the layer moves: an error stops them
	// there, as an interrupt would. Nil in a real run.
	AfterMove func(wtsync.LayerMove) error
	// BeforeCheck is a test's hook between the rebase and the check of the
	// new tips. Nil in a real run.
	BeforeCheck func(*wtsync.StackRecord)
	// BeforeRebase is a test's hook between the read of the line and its
	// rebase, with the worktree locked. Nil in a real run.
	BeforeRebase func()
	// BeforePush is a test's hook after every worktree is done and its lock
	// released, before the run pushes. Nil in a real run.
	BeforePush func()
	// Between is a test's hook into each stage of one layer move
	// (wtsync.MoveOptions.Between). Nil in a real run.
	Between func(stage string, m wtsync.LayerMove) error
}

// participant is one worktree of a run: the branch it is on, what triage
// made of it, the lock the run holds on it, and what its rebase came to.
type participant struct {
	wt      repo.Worktree
	work    string
	a       wtsync.Assessment
	verdict wtsync.Verdict
	reason  string
	// refused is why the run does not rebase this worktree, printed under
	// its row: its own refusal at triage or at the lock, a stack member
	// refused before anything moved, which poisons the whole stack (a stack
	// is rebased whole or not at all), or a parent that failed or was
	// restored, which takes what sits on top of it with it. Empty means it
	// goes ahead.
	refused string
	lock    *wtsync.Lock
	result  *wtsync.Result
	head    string // HEAD after the rebase and the deferred steps; what a child rebases onto
	// forced is the sessions Force took the run past: named before anything
	// moves, and told at the finish like an idle one.
	forced agents.Sessions
	// notRun is a refusal that came from another member of the stack, not
	// from this worktree: --json reports it as not run.
	notRun bool
	// found is the branch's tip as the run found it, when the run moved it
	// to its own remote's commit before rebasing: what the safety ref pins
	// and a restore goes back to.
	found string
	// unavailable is the needs of its steps that did not answer, in a run
	// told to go on with --allow-owed: those steps are owed.
	unavailable map[string]string
	// loose is the branches in no worktree on this worktree's line, read
	// before its rebase and moved after it; nil where the run moves none.
	loose *wtsync.LooseLine
	// looseOnto is what the line was rebased onto.
	looseOnto string
}

// RunPlan is one wt sync rebase: the trunk it rebases onto, the worktrees the
// arguments named and the stacks they belong to, and what each of them came
// to. SyncRebase drives it through its phases in order: selectBranches, triage,
// lockAll, then rebaseOne per worktree.
type RunPlan struct {
	ctx  *kit.Context
	opts RunOptions
	// eachCommit is the run's --each-commit mode, checked: all, tips, off.
	eachCommit string
	w          io.Writer
	tracker    *syncrun.RebaseTracker

	trunk    string // trunk as a person says it, "main"
	onto     string // what the run rebases onto, "origin/main"
	trunkSHA string // onto's tip, one SHA for the whole run
	cfg      *wtsync.Config
	// own is every branch against its own remote, read after the fetch.
	own *wtsync.Own
	// target is what --onto names, nil for a run onto trunk; tgt is what
	// every worktree of the run is assessed against and rebased onto:
	// trunk, or the target.
	target *OntoTarget
	tgt    wtsync.Target
	// stacked is the stack parents of the worktrees a run with --onto
	// leaves where they are, for the commands that follow them.
	stacked map[string]string
	// expectWork is the worktree a token read with --onto is of.
	expectWork string

	parents   map[string]string
	ambiguous map[string][]string // branches that merge two ancestors; not handled
	branches  []string            // every member of every named stack, parents first
	parts     map[string]*participant
	epoch     int64

	// Listed once for the run: selectReady and triage read the same list, and
	// the lock lists again with the lock in hand.
	agents       []agents.Agent
	agentsListed bool
	// survey is the overview --expect recomputed, whose assessments
	// selectReady and triage use rather than simulate the same rebases again.
	survey *Survey
	// all is a run with nothing named, which picked every ready worktree
	// itself: it asks before moving even one, since nobody named it. assessed
	// is what selectReady saw, by branch, so triage does not simulate the same
	// rebases again.
	all      bool
	assessed map[string]wtsync.Assessment

	// What each worktree came to: a count per outcome for the summary a run
	// of more than one ends with, the line under it for each that did not
	// finish, how the closing error names it, and what may be pushed.
	outcomes   map[string]int
	unfinished []string
	failures   []string
	Pushable   []syncrun.PushTarget
	// What the keeper records of a run with nothing named: the worktrees it
	// left alone with what holds them, the ones it rebased, the ones it
	// pushed, and the ones it only fast-forwarded to their own remote.
	Left, Rebased, Pushed []string
	Forwarded             []string
	// notReady is the work name of every worktree selectReady left behind
	// trunk, for a run that must say so: IfReady fails on them.
	notReady []string
	// line is the stack held on the named worktree's line, for --stack.
	line wtsync.Line
	// sides is the branches that hang off that line, for --stack; none with
	// --no-side-branches.
	sides []wtsync.SideBranch
	// edit is the commit of the line --edit names, resolved; nil without.
	edit *wtsync.EditStop
	// foldHeader is the header of a run that folds the line in place, held
	// until the line is read and its base is known; nil once printed.
	foldHeader *foldHeader
	// gaps is, for a fold, the hunks of its fixups that lack their context
	// behind their targets, named before the rebase.
	gaps          []wtsync.ContextGap
	gapsUnchecked []string
	// stackBroken is why the layers of a finished --stack rebase are not
	// moved: their plan could not be recorded.
	stackBroken string
	// stack is what the finished --stack rebase left of its layers, for the
	// push at the end of the run.
	stack *syncrun.Stack
	// stepSet is the steps wt sync steps runs, by their run line: what the
	// last run left owed, or nil for every one (--all).
	stepSet map[string]bool
}

// SyncRebase rebases the named worktrees (and the stacks they belong to) onto
// origin/<trunk>: safety ref, strategies at each stop, deferred steps once
// at the end. With nothing named it takes every worktree the overview calls
// ready, less recipe?, and asks first. Anything refused, restored, failed or
// owed is reported and makes the returned error non-nil, so a script sees it.
func SyncRebase(ctx *kit.Context, works []string, opts RunOptions, w io.Writer) error {
	return syncrun.Journaled(opts.Journal, func() error {
		_, err := RunSync(ctx, works, opts, w)
		return err
	})
}

// RunSync is SyncRebase with the plan handed back, for the keeper's record of
// what a pass did.
func RunSync(ctx *kit.Context, works []string, opts RunOptions, w io.Writer) (*RunPlan, error) {
	r := &RunPlan{ctx: ctx, opts: opts, w: w, tracker: &syncrun.RebaseTracker{}, trunk: ctx.Config.MainBranch, outcomes: map[string]int{}}
	err := r.run(works)
	return r, err
}

func (r *RunPlan) run(works []string) error {
	w, opts := r.w, r.opts
	defer syncrun.WatchSignals(w, r.tracker)()
	defer r.flushFoldHeader()

	given := opts.EachCommit
	if opts.Steps != nil {
		// wt sync steps settles its mode once it has read what was owed.
		given = opts.Steps.EachCommit
	}
	mode, err := wtsync.EachCommitMode(given)
	if err != nil {
		return err
	}
	r.eachCommit = mode
	if mode == wtsync.EachTargets && opts.Steps == nil && (opts.Stack == nil || !opts.Stack.Autosquash || opts.Stack.Edit != "" || opts.Onto != "") {
		return errors.New(wtsync.TargetsRefusal(opts.Stack != nil && opts.Stack.Edit != ""))
	}
	if mode == wtsync.EachTips && opts.Stack != nil && opts.Stack.Edit != "" {
		return errors.New("--each-commit=tips and --edit are two runs: an edit run's checks sit at the commits above the edit; use --each-commit=off, or edit first and check after")
	}
	opts.Journal.SetEachCommit(mode)
	if err := ontoUsage(works, opts.Onto, opts.Upstream); err != nil {
		return err
	}
	if opts.Onto != "" && opts.Steps != nil {
		return errors.New("wt sync steps runs the steps in place and takes no --onto")
	}
	if err := aloneUsage(opts, works); err != nil {
		return err
	}
	if err := r.declare(works); err != nil {
		return err
	}
	if opts.Expect != "" && !opts.planExpect {
		if r.target != nil {
			if len(works) != 1 {
				return errors.New("--expect with --onto takes the one worktree wt sync <work> --onto <ref> --json read the token for; name it alone")
			}
			r.expectWork = works[0]
		}
		if err := r.expectOverview(); err != nil {
			return err
		}
	}
	if len(works) == 0 && opts.Force {
		return errors.New("--force takes a run past the sessions in a worktree you name; name it")
	}
	if opts.Stack != nil && len(works) != 1 {
		return errors.New("--stack rebases the line of one worktree: name the one at its top")
	}
	if opts.Steps != nil && len(works) != 1 {
		return errors.New("wt sync steps runs the steps of one worktree: name it")
	}
	if len(works) == 0 && opts.AllowDiverged {
		return errors.New("--allow-diverged rebases a worktree you name over commits its remote has; name it")
	}
	if opts.Stack != nil {
		if err := wtsync.CheckLayerPatterns(r.ctx.Repo.MainRoot, opts.Stack.Layers); err != nil {
			return err
		}
	}
	// Every refusal --onto has comes before local trunk is touched.
	if r.target != nil {
		if err := r.ontoPrecheck(works); err != nil {
			return err
		}
	}
	// wt up's token names the stack, which is only known further down.
	if opts.Expect == "" || !opts.planExpect {
		r.syncTrunk()
	}
	if len(works) == 0 {
		ready, err := r.selectReady()
		if err != nil {
			return err
		}
		if opts.IfReady {
			for _, l := range r.notReady {
				r.failures = append(r.failures, l+" (not ready)")
			}
		}
		if len(ready) == 0 {
			if len(r.failures) > 0 {
				return fmt.Errorf("not completed: %s", strings.Join(r.failures, ", "))
			}
			return nil
		}
		works = ready
	}
	if err := r.selectBranches(works); err != nil {
		return err
	}
	r.fetchMissed()
	if opts.Expect != "" && opts.planExpect {
		if now := syncrun.PlanToken(r.ctx, r.trunk, r.branches, r.diverged(), r.layerNames()); now != opts.Expect {
			return fmt.Errorf("the plan changed since it was read (trunk %s, the configuration, the stack %s "+
				"or a remote the stack has diverged from is not what wt status --json saw); nothing is "+
				"rebased: read the plan again", r.trunk, strings.Join(r.branches, ", "))
		}
		r.syncTrunk()
	}
	for _, b := range r.branches {
		p := r.parts[b]
		// The journal's record: a tip git cannot read is recorded as none.
		before, _, _ := r.ctx.Repo.ResolveRef("refs/heads/" + b)
		opts.Journal.Join(p.work, b, p.wt.Path, before, syncrun.OwnRemoteSyncOf(syncrun.OwnStateOf(r.own, b)))
	}
	if err := r.readStack(); err != nil {
		return err
	}
	if err := r.readOwed(); err != nil {
		return err
	}
	if proceed, err := r.triage(); err != nil || !proceed {
		if err == nil {
			opts.Journal.FailWith(errors.New("not confirmed: " + syncrun.RebasedNothing.Line))
		}
		if err == nil && len(r.failures) > 0 {
			return fmt.Errorf("not completed: %s", strings.Join(r.failures, ", "))
		}
		return err
	}
	// The backstop for a run that returns early: whatever lockAll and
	// rebaseOne have not released by then. A handover drops its handle
	// before this runs, so the lock it leaves behind for resume and undo is
	// not among them.
	defer r.releaseAll()
	if err := r.lockAll(); err != nil {
		return err
	}
	for _, b := range r.branches {
		r.rebaseOne(b)
	}
	r.printFollowUps()
	if len(r.branches) > 1 {
		var counts []string
		for _, outcome := range []string{"rebased", "fast-forwarded", "skipped", "needs you", "refused", "restored", "failed"} {
			if n := r.outcomes[outcome]; n > 0 {
				counts = append(counts, fmt.Sprintf("%d %s", n, outcome))
			}
		}
		fmt.Fprintf(w, "\n%s\n", strings.Join(counts, " · "))
		for _, line := range r.unfinished {
			fmt.Fprintln(w, line)
		}
	}
	if st := opts.Stack; st != nil && st.BeforePush != nil {
		st.BeforePush()
	}
	// A --stack run pushes the line's moved layers and the line's own branch
	// in one atomic push per remote; OfferPush takes what is left.
	left, leftMode, pushed, pushFailed := syncrun.PushStack(r.ctx, w, opts.Push, opts.PushOptions, r.stack, r.Pushable)
	// What it left unpushed is kept, with its leases, for wt sync push --stack.
	syncrun.SettleStackPush(r.ctx, w, r.stack)
	more, moreFailed, err := syncrun.OfferPush(r.ctx, w, leftMode, opts.PushOptions, left)
	if err != nil {
		return err
	}
	pushed = append(pushed, more...)
	pushFailed = append(pushFailed, moreFailed...)
	r.Pushed = pushed
	for _, t := range r.Pushable {
		opts.Journal.Set(t.Branch, func(jp *syncrun.UpParticipant) {
			// An answer to the push question may have given it a destination.
			if jp.NoPushReason != nil {
				jp.PushCommand, jp.NoPushReason, jp.FixCommand = syncrun.PushJSON(t, syncrun.PushOf(r.ctx, t))
			}
			jp.Pushed = slices.Contains(pushed, t.Work)
		})
	}
	r.failures = append(r.failures, pushFailed...)
	if len(r.failures) > 0 {
		return fmt.Errorf("not completed: %s", strings.Join(r.failures, ", "))
	}
	return nil
}

// declare fetches trunk unless told not to, and with it the own remote of
// every branch works may bring into the run, pins the one SHA the whole run
// works against, prints the run's first line and reads the declaration
// there. Trunk that cannot be fetched refuses the run; a branch whose own
// remote cannot is refused by itself, at triage.
func (r *RunPlan) declare(works []string) error {
	ctx := r.ctx
	named := works
	var fetched string
	if r.opts.NoFetch {
		fetched = LastFetchedParen(ctx.Repo.MainRoot, r.opts.Clock())
		if r.own = r.opts.Own; r.own == nil {
			own, err := wtsync.ReadOwn(ctx.Repo.MainRoot, r.trunk)
			if err != nil {
				return err
			}
			r.own = own
		} else if err := r.own.Refresh(); err != nil {
			// Read again: a question may have waited since the caller's fetch.
			return err
		}
	} else {
		// An overview's token covers every worktree, so its run reads them
		// all, as a run with nothing named does.
		if r.opts.Expect != "" && !r.opts.planExpect {
			works = nil
		}
		own, err := syncrun.FetchRemotes(ctx, kit.NetworkTimeout, syncrun.OwnCandidates(ctx, works))
		if err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
		r.own = own
		fetched = "(fetched)"
	}
	// One SHA for the whole run: the declaration, the scripts and every
	// rebase target are the same trunk, whatever someone else fetches
	// underneath us mid-run.
	onto, trunkSHA, err := TrunkTip(ctx)
	if err != nil {
		return err
	}
	r.onto, r.trunkSHA = onto, trunkSHA
	r.tgt = wtsync.Target{Onto: trunkSHA}
	r.opts.Journal.Repo(ctx.Repo.MainRoot)
	// onto is the target's commit, recorded once the target resolves.
	recorded := trunkSHA
	if r.opts.Onto != "" {
		recorded = ""
	}
	r.opts.Journal.Trunk(r.trunk, ctx.TrunkSource(), onto, recorded, !r.opts.NoFetch)
	label := r.opts.label
	if label == "" && r.opts.Steps != nil {
		label = "wt sync steps"
	}
	if label == "" {
		label = "wt sync rebase"
	}
	if r.opts.Onto != "" {
		target, how, err := r.resolveTarget()
		if err != nil {
			return err
		}
		r.target, r.tgt = &target, target.Target(trunkSHA)
		fmt.Fprintf(r.w, "%s  onto %s %s\n", label, target.Short(), how)
		fmt.Fprintf(r.w, "the declaration and its scripts are read from trunk, %s %s %s\n", onto, git.ShortID(trunkSHA, 7), fetched)
	} else if r.foldsInPlace() {
		// Said once the line is read and its own base is known; as the plain
		// header if the run stops before that.
		r.foldHeader = &foldHeader{label: label, onto: onto, trunkSHA: git.ShortID(trunkSHA, 7), fetched: fetched}
	} else {
		fmt.Fprintf(r.w, "%s  onto %s %s %s\n", label, onto, git.ShortID(trunkSHA, 7), fetched)
	}
	r.opts.Journal.Target(r.targetJSON())
	cfg, err := wtsync.LoadFromRef(ctx.Repo.MainRoot, trunkSHA)
	if err != nil {
		r.flushFoldHeader()
	}
	if errors.Is(err, wtsync.ErrUndeclared) && r.opts.undeclaredOK {
		fmt.Fprintf(r.w, "no %s on %s: nothing is declared, so only a conflict-free rebase goes\n", wtsync.ConfigFile, onto)
		r.cfg = &wtsync.Config{}
		return nil
	}
	if errors.Is(err, wtsync.ErrUndeclared) {
		// Nothing to rebase, but trunk was fetched: local trunk follows,
		// unless an --expect is waiting to refuse the run anyway, or --onto
		// is: its refusals come before anything moves.
		if r.target != nil {
			if err := r.ontoPrecheck(named); err != nil {
				return err
			}
		}
		if r.opts.Expect == "" {
			r.syncTrunk()
		}
		return undeclaredError{fmt.Sprintf("%s declares no %s on %s: nothing is rebased", ctx.Repo.Name, wtsync.ConfigFile, onto)}
	}
	if err != nil {
		return err
	}
	r.cfg = cfg
	return nil
}

// syncTrunk brings local trunk to the trunk the run fetched, when it did,
// and says what came of it. It runs once an --expect has held: a refused
// run touches nothing, local trunk included.
func (r *RunPlan) syncTrunk() {
	if r.opts.NoFetch {
		return
	}
	ts := syncrun.SyncLocalTrunk(r.ctx, r.trunkSHA, syncrun.TrunkSyncOptions{Apply: true,
		OptedOut: r.opts.NoFFTrunk || !r.ctx.UserConfig().FFTrunk,
		Agents:   r.listAgents})
	r.opts.Journal.TrunkSync(ts)
	if line := ts.Line(); line != "" {
		fmt.Fprintln(r.w, line)
	}
}

// fetchMissed fetches the own remote of a branch the run chose and its first
// fetch did not ask for: the stack is read against the trunk that fetch
// brought, so it cannot be known before it. Not expected, and one more
// fetch when it happens rather than a branch compared as last fetched
// unsaid. It refuses nothing: a branch it cannot fetch is refused at triage,
// like one the first fetch could not.
func (r *RunPlan) fetchMissed() {
	if r.opts.NoFetch {
		return
	}
	var missed []string
	for _, b := range r.branches {
		if o := r.own.State(b); !o.Fetched && o.FetchFailed == "" && o.State != wtsync.OwnUnknown {
			missed = append(missed, b)
		}
	}
	if len(r.own.Asks(missed)) > 0 {
		_ = syncrun.FetchOwn(r.ctx, r.own, kit.NetworkTimeout, missed, false)
	}
}

// diverged is the remote commit of every branch of the run that has diverged
// from its own remote, by branch: what a plan's token covers of them.
func (r *RunPlan) diverged() map[string]string {
	out := map[string]string{}
	for _, b := range r.branches {
		if o := r.own.State(b); o.State == wtsync.OwnDiverged {
			out[b] = o.Commit
		}
	}
	return out
}

// undeclaredError is a trunk with no .wt-sync.yaml: nothing a run can do, and
// in a run across repositories not a failure — that repository has not opted
// in.
type undeclaredError struct{ msg string }

func (e undeclaredError) Error() string { return e.msg }

// expectOverview recomputes the overview wt sync --json printed, against the
// trunk just fetched, and refuses the run when its token is not the one
// expected: what the run would start on is not what the overview showed.
func (r *RunPlan) expectOverview() error {
	listed, err := r.listAgents()
	if err != nil {
		return err
	}
	var sv Survey
	if r.target != nil {
		sv, err = SurveyOnto(r.ctx, r.expectWork, *r.target, r.trunkSHA, r.cfg, listed, r.own)
	} else {
		sv, err = SurveyRepo(r.ctx, r.trunkSHA, r.cfg, listed, r.own)
	}
	if err != nil {
		return err
	}
	r.survey = &sv
	now, err := sv.Token(r.ctx, r.trunk, r.trunkSHA)
	if err != nil {
		return err
	}
	if now == nil || *now != r.opts.Expect {
		if r.target != nil {
			return fmt.Errorf("the look changed since it was read (%s, or what a run onto it would start on, is "+
				"not what wt sync %s --onto %s --json saw); nothing is rebased: look again", r.target.Short(), r.expectWork, r.target.Ref)
		}
		return fmt.Errorf("the overview changed since it was read (what a run on trunk %s would start on is "+
			"not what wt sync --json saw); nothing is rebased: read the overview again", r.trunk)
	}
	r.parents, r.ambiguous, r.assessed = sv.parents, sv.ambiguous, sv.assessed
	return nil
}

// listAgents lists the other agent sessions once for the run.
func (r *RunPlan) listAgents() ([]agents.Agent, error) {
	if !r.agentsListed {
		listed, err := r.opts.SessionsNow(r.ctx, syncrun.RebasedNothing)
		if err != nil {
			return nil, err
		}
		r.agents, r.agentsListed = listed, true
	}
	return r.agents, nil
}

// selectReady is what a run with nothing named rebases: every worktree the
// overview files under ready, less recipe?, whose run may stop and hand over
// a plan nobody asked for, and less a stack with a member held back, which
// would refuse every one of them at triage. The rest is named with what
// holds it, in the overview's words, and left as it is. The assessments are
// kept for triage, which would otherwise simulate the same rebases again.
func (r *RunPlan) selectReady() ([]string, error) {
	ctx := r.ctx
	all, err := ctx.Repo.Worktrees()
	if err != nil {
		return nil, err
	}
	worktrees := slices.DeleteFunc(slices.Clone(all), func(wt repo.Worktree) bool { return wt.IsMain })
	var assessments []wtsync.Assessment
	if sv := r.survey; sv != nil {
		worktrees, assessments = sv.worktrees, sv.assessments
	} else {
		listed, err := r.listAgents()
		if err != nil {
			return nil, err
		}
		assessments = assessAll(ctx.Repo.MainRoot, r.trunkSHA, r.cfg, worktrees, listed, r.own, assessWorkers)
		r.parents, r.ambiguous, err = wtsync.Parents(ctx.Repo.MainRoot, r.trunkSHA, all)
		if err != nil {
			return nil, err
		}
	}
	r.all = true
	r.assessed = map[string]wtsync.Assessment{}
	for i, wt := range worktrees {
		if wt.Branch != "" {
			r.assessed[wt.Branch] = assessments[i]
		}
	}
	var ready, left []string
	for i, wt := range worktrees {
		a := assessments[i]
		work := kit.WorktreeName(ctx, wt.Branch, wt.Path)
		switch {
		case syncrun.OnTrunk(a):
			// On trunk already; the overview leaves it out too.
		case !r.ready(a):
			left = append(left, leftLabel(work, a))
			// Detached and stale are lifecycle questions, not a rebase that
			// would not go through.
			if a.Class != wtsync.Detached && a.Class != wtsync.Stale {
				r.notReady = append(r.notReady, work)
			}
		default:
			if hold := r.stackHold(wt.Branch); hold != "" {
				left = append(left, work+" "+ClassLabel(a)+", "+hold)
				r.notReady = append(r.notReady, work)
				continue
			}
			ready = append(ready, work)
		}
	}
	if len(ready) == 0 {
		fmt.Fprintln(r.w, "nothing is ready to rebase")
	} else {
		fmt.Fprintf(r.w, "every ready worktree: %s\n", strings.Join(ready, ", "))
	}
	if len(left) > 0 {
		fmt.Fprintf(r.w, "left as they are: %s\n", strings.Join(left, " · "))
	}
	r.Left = left
	return ready, nil
}

// allow marks an assessment whose branch has diverged from its own remote as
// one this run rebases as it stands, when it was told to. Only a named
// worktree gets here with the flag: a run with nothing named refuses it.
func (r *RunPlan) allow(a wtsync.Assessment) wtsync.Assessment {
	if r.opts.AllowDiverged && a.Own.State == wtsync.OwnDiverged {
		a.Own.Allowed = true
	}
	return a
}

// ready is what a run with nothing named takes: readyToRun, and unattended
// nothing with a session in it, idle included. An idle session is a person
// away from their desk; a run they started may ask on its behalf, a keeper
// has nobody to ask.
func (r *RunPlan) ready(a wtsync.Assessment) bool {
	return readyToRun(a) && (!r.opts.Unattended || len(a.Sessions) == 0)
}

// stackHold is why a ready worktree is not taken on its own: a member of its
// stack that triage would refuse, or one that would be run and handed over
// or put back, so the whole stack would go with it. A member that is only
// skipped (on trunk, or nothing ahead of it) holds nothing. Empty means the
// stack goes as a whole.
func (r *RunPlan) stackHold(branch string) string {
	if anc, ok := r.ambiguous[branch]; ok {
		return "merges " + strings.Join(anc, " and ")
	}
	for _, m := range wtsync.Members(r.parents, branch) {
		if m == branch {
			continue
		}
		a, ok := r.assessed[m]
		if ok {
			if v, _ := wtsync.Preflight(a); v == wtsync.SkipRun || (v == wtsync.Proceed && r.ready(a)) {
				continue
			}
		}
		return "stacked with " + kit.BranchWorkName(r.ctx, m)
	}
	return ""
}

// selectBranches turns the arguments into the branches the run rebases: each
// named worktree's branch, every other member of its stack, parents first.
func (r *RunPlan) selectBranches(works []string) error {
	ctx := r.ctx
	worktrees, err := ctx.Repo.Worktrees()
	if err != nil {
		return err
	}
	byBranch := map[string]repo.Worktree{}
	for _, wt := range worktrees {
		if !wt.IsMain && wt.Branch != "" {
			byBranch[wt.Branch] = wt
		}
	}
	var named []string
	for _, arg := range works {
		wt, err := kit.LocateBranch(ctx, arg)
		if err != nil {
			return err
		}
		named = append(named, wt.Branch)
	}
	if r.parents == nil {
		r.parents, r.ambiguous, err = wtsync.Parents(ctx.Repo.MainRoot, r.trunkSHA, worktrees)
		if err != nil {
			return err
		}
	}
	if r.opts.Alone {
		// The named worktree goes without its stack: the others are not
		// triaged, locked or rebased, and say so in the run's output.
		left := leftAloneOf(ctx, worktrees, strandedBy(r.parents, r.ambiguous, named[0]))
		printLeftAlone(r.w, left)
		r.opts.Journal.SetLeftAlone(left)
		r.parents, r.ambiguous = map[string]string{}, map[string][]string{}
	}
	if r.target != nil {
		if err := r.ontoBranches(named, worktrees); err != nil {
			return err
		}
	}
	if r.opts.Stack != nil || r.opts.Steps != nil || r.target != nil {
		// The line is one worktree: its layers are moved by this run, not
		// rebased again as members of a stack of worktrees. wt sync steps
		// covers the one branch named.
		r.parents, r.ambiguous = map[string]string{}, map[string][]string{}
	}
	parents, ambiguous := r.parents, r.ambiguous
	for _, b := range named {
		if anc, ok := ambiguous[b]; ok {
			return fmt.Errorf("%s merges %s; that shape is not handled", kit.BranchWorkName(ctx, b), strings.Join(anc, " and "))
		}
	}
	seen := map[string]bool{}
	var branches []string
	for _, b := range named {
		var added []string
		for _, m := range wtsync.Members(parents, b) {
			if seen[m] {
				continue
			}
			seen[m] = true
			branches = append(branches, m)
			if m != b {
				added = append(added, kit.BranchWorkName(ctx, m))
			}
		}
		if len(added) > 0 {
			fmt.Fprintf(r.w, "%s is a stack with %s: rebasing all of them\n", kit.BranchWorkName(ctx, b), strings.Join(added, ", "))
		}
	}
	r.branches = wtsync.Order(parents, branches)
	r.parts = map[string]*participant{}
	for _, b := range r.branches {
		r.parts[b] = &participant{wt: byBranch[b], work: kit.BranchWorkName(ctx, b)}
	}
	return nil
}

// triage assesses every branch, refuses what preflight refuses (and the
// stack it belongs to), names the idle sessions the run is about to move
// files under, and asks the one question a run asks. proceed is false for
// the answer no, which is not an error: the line saying so has been printed.
func (r *RunPlan) triage() (proceed bool, err error) {
	listed, err := r.listAgents()
	if err != nil {
		return false, err
	}
	for _, b := range r.branches {
		p := r.parts[b]
		if a, ok := r.assessed[b]; ok {
			p.a = a
		} else {
			p.a = wtsync.AssessTo(r.ctx.Repo.MainRoot, r.tgt, r.cfg, p.wt, listed, syncrun.OwnStateOf(r.own, b))
		}
		if r.opts.Force && len(p.a.Sessions) > 0 {
			p.forced = p.a.Sessions
			if p.a.Own.Blocks == wtsync.OwnBlockSession {
				// The session was all that kept the branch from its remote's
				// commit, so that commit is what the run rebases: assessed
				// again there, with nobody in the way.
				p.a = wtsync.AssessTo(r.ctx.Repo.MainRoot, r.tgt, r.cfg, p.wt, nil, syncrun.OwnStateOf(r.own, b))
			}
			p.a.Sessions = nil
			fmt.Fprintf(r.w, "⚠ %s: --force takes the run past %s; the files it has read will change\n",
				p.work, kit.WhoLabel(p.forced))
		}
		p.a = r.allow(p.a)
		p.verdict, p.reason = wtsync.Preflight(p.a)
		if r.opts.Steps != nil && p.verdict == wtsync.SkipRun {
			// In place, onto its own base: trunk being there already is no
			// reason to skip it.
			p.verdict, p.reason = wtsync.Proceed, ""
		}
		if st := r.opts.Stack; st != nil {
			p.verdict, p.reason = stackVerdict(p.a, p.verdict, p.reason, st.Autosquash || st.Edit != "")
		}
		own, refused := p.a.Own, p.verdict == wtsync.RefuseRun
		r.opts.Journal.Set(b, func(jp *syncrun.UpParticipant) {
			jp.OwnRemoteSync = syncrun.OwnRemoteSyncOf(own)
			switch {
			case !refused || !own.Refuses():
			case own.FetchFailed != "":
				jp.OwnRemoteSync.SkippedReason = jsonout.Strp(syncrun.OwnSkipFetchFailed)
			default:
				jp.OwnRemoteSync.SkippedReason = jsonout.Strp(own.Blocks)
			}
		})
	}
	for _, b := range r.branches {
		if p := r.parts[b]; p.verdict == wtsync.RefuseRun {
			r.refuseStack(b, p.work+": "+p.reason)
		}
	}
	r.probeNeeds()
	if r.opts.IfReady {
		for _, b := range r.branches {
			p := r.parts[b]
			if p.verdict == wtsync.Proceed && p.refused == "" && !r.ready(p.a) {
				r.refuseStack(b, p.work+" would not sync cleanly: "+notReadyReason(p.a)+"; wt sync "+p.work+" for the detail")
			}
		}
	}
	var going []string
	underIdle := false
	for _, b := range r.branches {
		p := r.parts[b]
		if p.verdict != wtsync.Proceed || p.refused != "" {
			continue
		}
		going = append(going, p.work)
		// Preflight lets sessions through only when every one is idle.
		if len(p.a.Sessions) > 0 {
			fmt.Fprintln(r.w, syncrun.IdleNotice(p.work, p.a.Sessions))
			underIdle = true
		}
	}
	// A run that picked its worktrees itself asks even for one of them.
	if len(going) > 1 || underIdle || (r.all && len(going) > 0) {
		if r.opts.Confirm != nil {
			fmt.Fprintf(r.w, "about to rebase: %s\n", strings.Join(going, ", "))
		}
		// Nothing to re-check here: a run lists the sessions again at the
		// lock, whether or not anything was asked, and compares them there
		// with the lock in hand.
		ok, _, err := syncrun.AskIdle(r.ctx, r.w, r.opts.VerbOptions, going, nil, listed, syncrun.RebasedNothing)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// probeNeeds probes, once each, the needs of the steps that may run for a
// worktree the run goes ahead with, before anything moves. A worktree whose
// step needs something that did not answer is refused, and what sits on it
// with it; with --allow-owed it goes ahead, and those steps are owed.
func (r *RunPlan) probeNeeds() {
	prober := r.opts.Prober()
	for _, b := range r.branches {
		p := r.parts[b]
		if p.verdict != wtsync.Proceed || p.refused != "" || p.a.OnlyFastForward() {
			continue
		}
		base := r.trunkSHA
		if r.target != nil {
			base = r.target.cutoff()
		}
		if parent, ok := r.parents[b]; ok && r.parts[parent] != nil {
			base = "refs/heads/" + parent
		}
		due, err := wtsync.StepsDue(p.wt.Path, r.cfg, base, rebasedTip(b, p.a), r.tgt.Onto, r.tgt.Upstream)
		if err != nil {
			p.refused = p.work + ": could not tell which steps a rebase runs: " + err.Error()
			r.refuseAbove(b, p.work+" was refused")
			continue
		}
		if r.eachCommit == wtsync.EachOff {
			// A check that is off needs nothing; a deferred step still does.
			due.Checks, due.CheckCommits = nil, nil
		}
		unavailable, refusal := syncrun.NeedsFor(prober, due, r.opts.AllowOwed)
		if refusal != "" {
			p.refused = p.work + ": " + refusal
			r.refuseAbove(b, p.work+" was refused")
			continue
		}
		p.unavailable = unavailable
	}
}

// lockAll locks every member of every proceeding stack before touching any,
// and re-checks what triage saw: the lock is what makes the check hold. A
// worktree that fails here refuses its whole stack, as at triage.
func (r *RunPlan) lockAll() error {
	// Listed again now: a question can sit unanswered for as long as it
	// likes, and a triage over a large fleet takes a while.
	fresh, err := r.opts.SessionsAgain(r.ctx, syncrun.RebasedNothing)
	if err != nil {
		return err
	}
	r.epoch = r.opts.Clock().UnixNano()
	for _, b := range r.branches {
		p := r.parts[b]
		if p.refused != "" || p.verdict != wtsync.Proceed {
			continue
		}
		gitDir, err := wtsync.GitDir(p.wt.Path)
		if err != nil {
			return err
		}
		lock, err := wtsync.Acquire(gitDir, r.opts.Clock())
		if err != nil {
			r.refuseStack(b, p.work+": locked: "+err.Error())
			continue
		}
		p.lock = lock
		r.ctx.NoteLock(gitDir)
		// A check that cannot be run is not a passed check: this fails
		// closed, the way the restore check in wtsync does. It says so,
		// rather than reporting the state it never managed to read.
		stand := wtsync.Recheck(p.wt.Path, gitDir, fresh)
		if stand.Err != nil {
			// In git's own words, as this line has always read them: the
			// form wtsync gives its errors adds the exit status.
			msg := stand.Err.Error()
			var gerr *git.Error
			if errors.As(stand.Err, &gerr) {
				msg = gerr.Error()
			}
			r.refuseStack(b, p.work+": changed since triage: could not check: "+msg)
			continue
		}
		if stand.Rebasing {
			why := p.work + ": changed since triage: a rebase is in progress"
			if stand.Plan {
				why = p.work + ": left mid-rebase by an earlier run: " + wtsync.WayOut(wtsync.Way{Work: p.work, Plan: true, Rebasing: true})
			}
			r.refuseStack(b, why)
			continue
		}
		if stand.Dirty {
			r.refuseStack(b, p.work+": changed since triage: tracked changes")
			continue
		}
		if why := syncrun.SessionsChanged(p.a.Sessions, stand.Sessions); why != "" && !r.opts.Force {
			r.refuseStack(b, p.work+": changed since triage: "+why)
			continue
		}
	}
	// A worktree refused here, or with a stack member refused here, is not
	// touched by the run: its lock goes now, before anything is rebased,
	// so nobody waits on it for the length of the run.
	for _, b := range r.branches {
		if p := r.parts[b]; p.refused != "" {
			r.release(p)
		}
	}
	return nil
}

// release drops p's lock, if the run still holds one.
func (r *RunPlan) release(p *participant) {
	if p.lock != nil {
		_ = p.lock.Release()
		p.lock = nil
	}
}

// refuseStack refuses b and every other member of its stack that is not
// already refused: a member refused before anything has moved poisons the
// whole stack. A stack is rebased whole or not at all: rebasing part of it
// leaves the rest on commits the part it rebased has replaced.
func (r *RunPlan) refuseStack(b, why string) {
	for _, m := range wtsync.Members(r.parents, b) {
		if p := r.parts[m]; p != nil && p.refused == "" {
			p.refused, p.notRun = why, m != b
		}
	}
}

// refuseAbove refuses what sits on top of b and nothing else. A rebase that
// failed or was restored is different from a refusal: the branch is back
// where it was and its parent and siblings are untouched, so only its
// descendants lose their base.
func (r *RunPlan) refuseAbove(b, why string) {
	for _, m := range wtsync.Descendants(r.parents, b) {
		if p := r.parts[m]; p != nil && p.refused == "" {
			p.refused, p.notRun = why, true
		}
	}
}

// releaseAll drops every lock the run still holds. A handover's lock is
// left behind on purpose and its handle dropped before this runs, so it is
// not touched.
func (r *RunPlan) releaseAll() {
	for _, b := range r.branches {
		r.release(r.parts[b])
	}
}

// settle records what one worktree came to: the outcome for the count, the
// line under the summary for one that did not finish, and how the closing
// error names it. Either string may be empty.
func (r *RunPlan) settle(outcome, line, failure string) {
	r.outcomes[outcome]++
	if line != "" {
		r.unfinished = append(r.unfinished, "  "+line)
	}
	if failure != "" {
		r.failures = append(r.failures, failure)
	}
}

// rebaseOne prints b's row and, unless it is refused or skipped, rebases it:
// onto trunk, or onto the tip its stack parent ended at. A stop a person
// owns is handed over; a finished rebase runs the deferred steps and pins
// its result. Whatever it comes to is settled, and what sits on top of a
// branch that did not finish is refused.
func (r *RunPlan) rebaseOne(b string) {
	p := r.parts[b]
	// The lock goes as soon as this worktree is done, whichever way, so the
	// next worktree's rebase does not hold it. The one exception is the
	// handover, which drops the handle to leave its lock behind.
	defer r.release(p)
	fmt.Fprintf(r.w, "\n%s  %s  %d behind · %d ahead\n", p.work, b, p.a.Behind, p.a.Ahead)
	if r.passOver(b, p) {
		return
	}
	// wt sync steps works on the branch as it is here: it never takes in
	// its own remote's commits.
	if p.a.OnlyFastForward() && r.opts.Steps == nil {
		r.fastForwardOnly(b, p)
		return
	}
	req, ontoLabel, ok := r.request(b, p)
	if !ok {
		return
	}
	if err := r.readLoose(b, p, req); err != nil {
		p.refused = p.work + ": its layer branches could not be read: " + err.Error()
		r.passOver(b, p)
		return
	}
	rec, rerr := r.stackRecord(b, p, req)
	if r.opts.Stack != nil {
		if hook := r.opts.Stack.BeforeRebase; hook != nil {
			hook()
		}
		// Under the lock, just before the rebase: the line and its layers
		// must still be what was read, or every marker would be planned
		// against commits that are not there.
		why := r.lineMoved(b, p.wt.Path)
		if rerr != nil {
			why = "the stack's record could not be written: " + rerr.Error() + "; nothing is rebased"
		}
		if why != "" {
			if err := wtsync.ForgetStack(r.ctx.Repo.MainRoot, r.epoch); err != nil {
				// Nothing pins this run yet for undo to find it by: the
				// record is named so it can go by hand.
				why += fmt.Sprintf("; the stack's record could not be removed (%v): delete it by hand", err)
			}
			p.refused = p.work + ": " + why
			r.passOver(b, p)
			return
		}
	}
	timed := wtsync.NewTimings()
	req.Timings = timed
	r.opts.Journal.TrackTimings(b, timed)
	res, rerr := syncrun.TrackedRebase(r.tracker, &syncrun.RebaseInFlight{Work: p.work, Path: p.wt.Path, Safety: wtsync.SafetyRef(b, r.epoch)}, func() (wtsync.Result, error) {
		return wtsync.Rebase(r.ctx.Repo.MainRoot, r.cfg, req, r.w)
	})
	p.result = &res
	defer syncrun.ReportTimings(r.w, r.opts.Journal, b, timed, false)
	r.opts.Journal.SetSync(b, func(jp *syncrun.SyncParticipant) {
		jp.Checked, jp.CheckFailed, jp.Rechecked = res.Checked, syncrun.CheckFailedOf(res.CheckFailed), res.Rechecked
	})
	if rec != nil {
		if err := syncrun.StackAfterRebase(r.ctx, rec, res); err != nil {
			why := "the plan of its layers could not be recorded: " + err.Error()
			switch {
			case rerr != nil || res.Restored:
			case res.Left != nil:
				r.putBackStack(b, p, res, why)
				return
			default:
				r.stackBroken = why
			}
		}
		if rerr != nil || res.Restored {
			if err := wtsync.ForgetStack(r.ctx.Repo.MainRoot, r.epoch); err != nil {
				fmt.Fprintf(r.w, "  note: %v\n", err)
			}
		}
	}
	switch {
	case rerr != nil:
		r.leaveLoose(b, p, "the rebase did not finish")
		r.rebaseFailed(b, p, res, rerr)
	case res.Left != nil:
		r.leaveLoose(b, p, "the rebase stopped for you: wt sync resume finishes it, and does not move them")
		r.handOver(b, p, req, res)
		if rec != nil {
			fmt.Fprintf(r.w, "  the layers move once wt sync resume %s finishes the rebase\n", p.work)
		}
	case res.Restored:
		r.settleRestored(b, p, res)
	default:
		r.finishRebase(b, p, res, ontoLabel)
	}
}

// readOwed reads, for wt sync steps, what the last run of the one branch
// named left owed. Nothing owed is a refusal, unless --all asks for every
// step.
func (r *RunPlan) readOwed() error {
	if r.opts.Steps == nil {
		return nil
	}
	if len(r.branches) != 1 {
		r.settleStepsMode(nil)
		return nil
	}
	b := r.branches[0]
	if len(r.cfg.Defer) == 0 {
		return fmt.Errorf("%s declares no deferred step: nothing for wt sync steps to run", wtsync.ConfigFile)
	}
	owed, err := wtsync.ReadOwed(r.ctx.Repo.MainRoot, b)
	if err != nil {
		return fmt.Errorf("what the last run left owed could not be read: %w", err)
	}
	if r.opts.Steps.All {
		// Every step runs, but the mode is still the owing run's.
		r.settleStepsMode(owed)
		return nil
	}
	if owed == nil {
		return fmt.Errorf("nothing is owed for %s: the last run ran every step it had to; --all runs every step declared", r.parts[b].work)
	}
	r.stepSet = owed.StepSet()
	r.settleStepsMode(owed)
	return nil
}

// settleStepsMode is the --each-commit mode wt sync steps runs under, and
// says which and why in one line: the flag's, else the mode of the run that
// owed the steps, as the record kept it, else all. A run under targets folded
// fixups, which steps does not: its tips are what steps checks.
func (r *RunPlan) settleStepsMode(owed *wtsync.Owed) {
	mode, why := r.eachCommit, ""
	switch recorded := ""; {
	case r.opts.Steps.EachCommit != "":
		why = "as --each-commit says"
	case owed != nil && owed.EachCommit != "":
		recorded = owed.EachCommit
		if m, err := wtsync.EachCommitMode(recorded); err == nil {
			mode, why = m, "as the run that owed these steps"
			break
		}
		why = fmt.Sprintf("the default: the record of what was owed names %q, which is not a mode", recorded)
	case owed != nil:
		why = "the default: the record of what was owed names no mode (an older wt wrote it)"
	default:
		why = "the default"
	}
	if mode == wtsync.EachTargets {
		mode, why = wtsync.EachTips, why+"; it is targets, and wt sync steps folds no fixup, so each layer's tip is the target"
	}
	r.eachCommit = mode
	r.opts.Journal.SetEachCommit(mode)
	for _, b := range r.branches {
		r.opts.Journal.SetSync(b, func(p *syncrun.SyncParticipant) { p.EachCommit = mode })
	}
	fmt.Fprintf(r.w, "each_commit: %s, %s\n", mode, why)
}

// passOver settles a worktree the run does not rebase, refused or skipped,
// and reports whether it did.
func (r *RunPlan) passOver(b string, p *participant) bool {
	w, j := r.w, r.opts.Journal
	if p.refused != "" {
		fmt.Fprintf(w, "  ✗ refused: %s\n", p.refused)
		r.settle("refused", "✗ "+p.work+"  refused: "+p.refused, p.work)
		j.Set(b, func(jp *syncrun.UpParticipant) {
			jp.Result, jp.Reason = syncrun.ResultRefused, jsonout.Strp(p.refused)
			if p.notRun {
				jp.Result = syncrun.ResultNotRun
			}
		})
		return true
	}
	if p.verdict == wtsync.SkipRun {
		fmt.Fprintf(w, "  ⏭ skipped: %s\n", p.reason)
		r.settle("skipped", "", "")
		j.Set(b, func(jp *syncrun.UpParticipant) { jp.Result, jp.Reason = syncrun.ResultSkipped, jsonout.Strp(p.reason) })
		return true
	}
	return false
}

// stackVerdict is the verdict of a --stack run for a worktree whose own is v
// and why: a fold or an edit is onto the line's own base, so trunk being
// there already is no reason to skip it, and a line behind its own remote is
// refused, as the layers were read from it as it is here.
func stackVerdict(a wtsync.Assessment, v wtsync.Verdict, why string, onOwnBase bool) (wtsync.Verdict, string) {
	switch {
	case onOwnBase && v == wtsync.SkipRun:
		return wtsync.Proceed, ""
	case v == wtsync.Proceed && a.Own.FastForwards():
		return wtsync.RefuseRun, "behind its own remote (" + a.Own.Ref + "): the layers were read from the line as it is here; bring it up to date first"
	}
	return v, why
}

// request is b's rebase as the run asks for it, and what its line calls
// what it rebases onto: trunk, or the tip its stack parent ended at. It
// says what is known about the rebase before it starts, and fast-forwards
// a branch behind its own remote first; ok is false when that fast-forward
// did not go, which settled the worktree.
func (r *RunPlan) request(b string, p *participant) (req wtsync.Request, ontoLabel string, ok bool) {
	w, j := r.w, r.opts.Journal
	req = wtsync.Request{Path: p.wt.Path, Branch: b, Trunk: r.trunkSHA, Onto: r.tgt.Onto, Upstream: r.tgt.Upstream, Epoch: r.epoch, Work: p.work, Unavailable: p.unavailable,
		EachCommit: r.eachCommit}
	if line := syncrun.AllowedOwedLine(p.unavailable); line != "" {
		fmt.Fprintf(w, "  %s\n", line)
	}
	req.Stacked = len(wtsync.Descendants(r.parents, b)) > 0
	ontoLabel = r.onto
	if r.target != nil {
		ontoLabel = r.target.Ref
	}
	inPlace := false
	if st := r.opts.Stack; st != nil {
		req.Stack = &wtsync.StackRequest{Line: r.line, Autosquash: st.Autosquash, Forks: wtsync.ForkPoints(r.sides)}
		req.Gaps = r.gaps
		// A fold is in place unless --onto names where the line goes; an
		// edit always is.
		if (st.Autosquash && r.target == nil) || r.opts.Steps != nil || r.edit != nil {
			req.Onto, ontoLabel = r.line.Base, "its own base "+git.ShortID(r.line.Base, 7)
			inPlace = true
		}
		if r.edit != nil {
			req.Edit = r.edit
			req.Content = syncrun.EditContent(r.ctx.Repo.MainRoot, r.line.Head, syncrun.FoldPaths(r.cfg), nil)
		}
	}
	if r.opts.Steps != nil {
		req.ForceSteps, req.Steps = true, r.stepSet
		if r.opts.Stack == nil {
			base, err := git.Run(r.ctx.Repo.MainRoot, "merge-base", r.trunkSHA, "refs/heads/"+b)
			if err != nil {
				p.refused = p.work + ": its base on trunk could not be read: " + err.Error()
				r.passOver(b, p)
				return req, ontoLabel, false
			}
			req.Onto, ontoLabel = base, "its own base "+git.ShortID(base, 7)
		}
	}
	if parent, ok := r.parents[b]; ok {
		if pp := r.parts[parent]; pp != nil && pp.result != nil && !pp.result.Restored && pp.head != "" {
			req.Onto, req.Upstream, ontoLabel = pp.head, pp.result.OldTip, pp.work
		}
	}
	// The assessment replays onto trunk, which an in-place fold never meets.
	if !inPlace && p.a.Class == wtsync.Contested && p.a.Replay.Stop != nil {
		what := "the run stops there and writes a plan"
		if req.Stacked {
			what = "the run stops there and puts the branch back: a stack parent cannot be left waiting"
		}
		fmt.Fprintf(w, "  ⚠ contested at %d/%d: %s\n", p.a.Replay.Stop.Index, p.a.Replay.Stop.Total, what)
		printLikely(w, "  ", p.a.Replay.Likely)
	}
	if !inPlace {
		printReworked(w, "  ", p.a.Replay.Reworked)
	}
	if line := syncrun.OwnElsewhere(p.a.Own); line != "" {
		fmt.Fprintf(w, "  %s\n", line)
	}
	switch own := p.a.Own; {
	case own.State == wtsync.OwnUnknown:
		fmt.Fprintf(w, "  ⚠ %s\n", syncrun.OwnLine(own))
	case own.Allowed:
		fmt.Fprintf(w, "  ⚠ --allow-diverged: %s has %s this branch never had%s; rebasing it as it stands\n",
			own.Ref, kit.Count(own.Behind, "commit"), syncrun.AsLastFetched(own))
		// Only now: the flag given to a run that refused the worktree for
		// something else went over nothing.
		j.Set(b, func(jp *syncrun.UpParticipant) { jp.OwnRemoteSync.Allowed = true })
	case own.FastForwards() && r.opts.Steps != nil:
		fmt.Fprintf(w, "  ⚠ %s has %s this branch does not; wt sync steps leaves them there\n", own.Ref, kit.Count(own.Behind, "commit"))
	case own.FastForwards():
		safety, ok := r.fastForward(b, p, "")
		if !ok {
			return req, ontoLabel, false
		}
		req.Safety = safety
	}
	return req, ontoLabel, true
}

// foundTip is the tip the run found the branch at: the one the rebase
// starts from, unless the run fast-forwarded it first.
func (p *participant) foundTip(res wtsync.Result) string { return cmp.Or(p.found, res.OldTip) }

// rebaseFailed settles a rebase that failed outright.
func (r *RunPlan) rebaseFailed(b string, p *participant, res wtsync.Result, rerr error) {
	fmt.Fprintf(r.w, "  ✗ failed: %v\n", rerr)
	syncrun.ClearHandover(r.w, p.wt.Path)
	r.recordFailed(b, p, rerr.Error(), p.foundTip(res), res.Safety.Ref)
	r.settle("failed", fmt.Sprintf("✗ %s  failed: %v", p.work, rerr), p.work+" (failed)")
	r.refuseAbove(b, p.work+" failed")
}

// handOver hands a rebase that stopped at a stop a person owns over to
// them, leaving its lock behind, and settles it.
func (r *RunPlan) handOver(b string, p *participant, req wtsync.Request, res wtsync.Result) {
	w, j := r.w, r.opts.Journal
	herr := syncrun.HandOver(r.ctx, w, syncrun.HandoverInput{
		Work: p.work, Branch: b, Path: p.wt.Path, TrunkRef: r.onto, TrunkSHA: r.trunkSHA, OntoRef: r.ontoRef(),
		Onto: req.Onto, Upstream: req.Upstream, Epoch: r.epoch, Cfg: r.cfg, Res: res, Lock: p.lock,
		Found: p.found, Tracker: r.tracker, FollowUps: r.followUpLines(b, p, res),
	})
	if herr != nil {
		fmt.Fprintf(w, "  ✗ failed: %v\n", herr)
		// The rebase is still in the worktree and there is now no
		// plan file to explain it, so say the two things a person
		// cannot see for themselves. Any half-written brief goes:
		// the sidecar is the marker and it was never written, so
		// nothing acts on what is left, and a stale markdown would
		// describe a stop that is not this one.
		syncrun.ClearHandover(w, p.wt.Path)
		// Not wt sync undo: it refuses a mid-rebase worktree, and
		// there is no handover here for it to abort. The branch ref
		// never moved, so the abort is the whole of putting it back.
		way := wtsync.WayOut(wtsync.Way{Work: p.work, Path: p.wt.Path, Rebasing: true})
		fmt.Fprintf(w, "  ⚠ %s is left mid-rebase with no plan: %s\n", p.work, way)
		j.SetSync(b, func(jp *syncrun.SyncParticipant) {
			jp.Result, jp.Reason, jp.Recovery = syncrun.ResultNeedsRecovery, jsonout.Strp(herr.Error()), jsonout.Strp(way)
			jp.SafetyRef = jsonout.Strp(res.Safety.Ref)
		})
		r.settle("failed", "✗ "+p.work+"  left mid-rebase with no plan: "+way, p.work+" (failed)")
		r.refuseAbove(b, p.work+" failed")
		return
	}
	// The lock is left behind on purpose; dropping the handle here
	// keeps the deferred releases from removing the file.
	p.lock = nil
	j.SetSync(b, func(jp *syncrun.SyncParticipant) {
		jp.Result = syncrun.ResultHandedOver
		jp.Reason = jsonout.Strp("stopped at a conflict that is yours")
		if res.Left.Check != nil {
			jp.Reason = jsonout.Strp("stopped at a commit whose check failed: " + res.Left.Check.Line())
		}
		if res.Left.Edit {
			jp.Reason = jsonout.Strp(syncrun.EditReason(res.Left))
			if jp.Stack != nil && r.edit != nil {
				jp.Stack.Edit = syncrun.EditAtStop(r.edit)
			}
		}
		jp.Recovery = jsonout.Strp(wtsync.WayOut(wtsync.Way{Work: p.work, Plan: true, Rebasing: true, OwesAdd: true}))
		jp.SafetyRef, jp.UndoCommand, jp.PlanFile = jsonout.Strp(res.Safety.Ref), syncrun.UndoCommand(p.work), syncrun.PlanFileOf(p.wt.Path)
	})
	r.settle("needs you", "⚠ "+p.work+"  needs you: "+wtsync.WayOut(wtsync.Way{Work: p.work, Plan: true, Rebasing: true, OwesAdd: true}), p.work+" (needs you)")
	r.refuseAbove(b, p.work+" is waiting for you")
}

// settleRestored settles a rebase the loop put back at a stop nobody resolved.
func (r *RunPlan) settleRestored(b string, p *participant, res wtsync.Result) {
	var restored string
	if f := res.CheckFailed; f != nil {
		restored = fmt.Sprintf("restored: %s at %d/%d; a stack parent is not left waiting: fix the commit, then run again", f.Line(), f.Index, f.Total)
	} else {
		last := res.Stops[len(res.Stops)-1]
		var files []string
		for _, f := range last.Files {
			if !f.Resolved {
				files = append(files, f.Path)
			}
		}
		restored = fmt.Sprintf("restored: %s at %d/%d not resolved; rebase by hand", strings.Join(files, ", "), last.Index, last.Total)
	}
	fmt.Fprintf(r.w, "  ✗ %s\n", restored)
	syncrun.ClearHandover(r.w, p.wt.Path)
	r.dropFastForward(b, p)
	r.opts.Journal.SetSync(b, func(jp *syncrun.SyncParticipant) {
		jp.Result, jp.Reason, jp.SafetyRef = syncrun.ResultRestored, jsonout.Strp(restored), jsonout.Strp(res.Safety.Ref)
		jp.After = jsonout.Strp(p.foundTip(res))
	})
	r.settle("restored", "✗ "+p.work+"  "+restored, p.work+" (restored)")
	r.refuseAbove(b, p.work+" was restored")
}

// finishRebase runs what follows a finished rebase: the deferred steps and
// the result ref. It settles the worktree, as pushable when nothing is
// owed.
func (r *RunPlan) finishRebase(b string, p *participant, res wtsync.Result, ontoLabel string) {
	ctx, w, j := r.ctx, r.w, r.opts.Journal
	// What a --stack run may leave unpushed is kept before the lock goes
	// (the caller's deferred release), from the tip the run left, and only
	// for a line left pushable: a step failed or owed leaves none.
	var pushable []syncrun.PushTarget
	var tip string
	defer func() {
		if r.opts.Stack != nil && r.stack != nil {
			syncrun.KeepStackPush(ctx, w, r.stack, pushable, tip)
		}
	}()
	line := fmt.Sprintf("  ✓ rebased %s onto %s", kit.Count(res.Replayed, "commit"), ontoLabel)
	if res.SignaturesDropped > 0 {
		line += fmt.Sprintf(", %s dropped", kit.Count(res.SignaturesDropped, "signature"))
	}
	var after string
	out := syncrun.Finish(ctx, w, r.cfg, r.tracker, j, syncrun.FinishInput{
		Work: p.work, Path: p.wt.Path, Branch: b, Epoch: r.epoch, Res: res,
		Headline: line, Layered: r.opts.Stack != nil,
		Complete: func() syncrun.CompleteInput {
			// What the deferred steps compare and the undo line names is the tip
			// the run found, the commits a fast-forward brought included.
			done := res
			done.OldTip = p.foundTip(res)
			return syncrun.CompleteInput{
				Branch: b, Epoch: r.epoch, Res: done,
				Tell: append(slices.Clone(p.a.Sessions), p.forced...), TrunkName: cmp.Or(r.ontoRef(), r.trunk), Landed: p.a.Behind, Check: syncrun.PathsOnce(wtsync.StopPaths(res.Stops)),
				Unavailable: p.unavailable, Steps: r.stepSet, ForceSteps: r.opts.Steps != nil,
			}
		},
		Layers: func() (bool, string) { return r.finishStack(b, p, res), "" },
		Settle: func(head string, _ error, _ bool) []string {
			p.head = head
			r.moveLoose(b, p, res)
			// The journal's record: a tip git cannot read is recorded as none.
			after, _, _ = ctx.Repo.ResolveRef("refs/heads/" + b)
			return nil
		},
	})
	derr, owed := out.Err, out.Owed
	// The rebase itself stands; only this branch and what sits on it
	// lose their footing, so the rest of the run carries on.
	r.failures = append(r.failures, out.OwedFailures...)
	if derr != nil {
		fmt.Fprintf(w, "  ✗ failed: %v\n", derr)
		j.Set(b, func(jp *syncrun.UpParticipant) {
			jp.Result, jp.After = syncrun.ResultRebasedStepFailed, jsonout.Strp(after)
			jp.FailedSteps = append(jp.FailedSteps, "finish: "+derr.Error())
		})
		r.settle("failed", fmt.Sprintf("✗ %s  failed: %v", p.work, derr), p.work+" (failed)")
		r.refuseAbove(b, p.work+" failed")
		return
	}
	r.Rebased = append(r.Rebased, p.work)
	if len(owed) > 0 {
		r.settle("rebased", "✗ "+p.work+"  owed: "+strings.Join(owed, ", ")+"; run it by hand, then push", "")
		j.Set(b, func(jp *syncrun.UpParticipant) {
			jp.Result, jp.After = syncrun.ResultRebasedStepFailed, jsonout.Strp(after)
			jp.FailedSteps = append(jp.FailedSteps, owed...)
		})
		return
	}
	r.settle("rebased", "", "")
	t := syncrun.PushTarget{Work: p.work, Branch: b, Path: p.wt.Path}
	r.Pushable = append(r.Pushable, t)
	pushable, tip = []syncrun.PushTarget{t}, after
	j.Set(b, func(jp *syncrun.UpParticipant) {
		jp.Result, jp.After = syncrun.ResultRebased, jsonout.Strp(after)
		jp.PushCommand, jp.NoPushReason, jp.FixCommand = syncrun.PushJSON(t, syncrun.PushOf(ctx, t))
	})
}

// notReadyReason is why a worktree a run could start on is not ready, in the
// words a person decides by: what would stop it, or what holds it.
func notReadyReason(a wtsync.Assessment) string {
	switch {
	case a.Dirty:
		return "it has uncommitted changes"
	case a.Paused:
		return "an earlier run handed it over"
	case a.Own.Refuses():
		return a.Own.Refusal()
	case a.Unverified:
		return "a script strategy claims a file, and a script can only be checked by a real run"
	case a.Class == wtsync.Contested && a.Replay.Stop != nil:
		return fmt.Sprintf("it would stop at %d/%d with a conflict that is yours", a.Replay.Stop.Index, a.Replay.Stop.Total)
	}
	return "it is " + ClassLabel(a)
}

// recordFailed records a rebase that failed by what it left: back at the
// tip the run found, with no rebase in progress and nothing changed, is
// restored; anything else needs putting back by hand, and says how. safety
// is the ref the rebase pinned the old tip under, "" when it failed first.
func (r *RunPlan) recordFailed(b string, p *participant, why, oldTip, safety string) {
	now, _, nerr := r.ctx.Repo.ResolveRef("refs/heads/" + b)
	busy, berr := wtsync.RebaseInProgress(p.wt.Path)
	dirty, derr := repo.Dirty(p.wt.Path, true)
	head, herr := git.Run(p.wt.Path, "rev-parse", "--verify", "--quiet", "HEAD")
	restored := nerr == nil && berr == nil && !busy && derr == nil && !dirty && herr == nil &&
		oldTip != "" && now == oldTip && head == oldTip
	if restored {
		r.dropFastForward(b, p)
	}
	r.opts.Journal.SetSync(b, func(jp *syncrun.SyncParticipant) {
		jp.Reason, jp.After, jp.SafetyRef = jsonout.Strp(why), jsonout.Strp(now), jsonout.Strp(safety)
		if restored {
			jp.Result = syncrun.ResultRestored
			return
		}
		jp.Result, jp.UndoCommand = syncrun.ResultNeedsRecovery, nil
		jp.Recovery = jsonout.Strp(wtsync.WayOut(wtsync.Way{Work: p.work, Path: p.wt.Path, Rebasing: busy,
			Safety: wtsync.SafetyRef(b, r.epoch)}))
	})
}
