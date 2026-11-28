package synccmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/config"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// CheckFailedStatus is the exit status of a look whose replayed checks
// failed.
const CheckFailedStatus = 4

// FoldLookCheck is one visit of the replay of a look with
// --each-commit=targets, as --json prints it.
type FoldLookCheck struct {
	// Kind is stop, recheck or layerStep.
	Kind string `json:"kind"`
	// Target is the commit of the line the visit is for, as it is now; null
	// for a per-layer step and for a commit such a step made.
	Target        *string `json:"target"`
	TargetSubject *string `json:"targetSubject"`
	Layer         string  `json:"layer"`
	// Fixups is the fixup! and move! commits folded into Target.
	Fixups []string `json:"fixups"`
	// AfterStep is the per-layer step a recheck follows.
	AfterStep *string        `json:"afterStep"`
	Steps     []FoldLookStep `json:"steps"`
}

// FoldLookStep is one step of a FoldLookCheck.
type FoldLookStep struct {
	Run    string  `json:"run"`
	Result string  `json:"result"`
	Why    *string `json:"why"`
	Log    *string `json:"log"`
}

// replaysChecks is whether the look runs the checks of the fold it shows.
func (l *StackLook) replaysChecks() bool {
	return l != nil && l.Autosquash && l.EachCommit == wtsync.EachTargets
}

// prober probes the needs of the look's run: for one that replays checks, as
// its scratch copy gives them.
func (o SyncOptions) prober(cfg *wtsync.Config) *syncrun.Prober {
	probes := o.Probes
	if o.Stack.replaysChecks() {
		probes = wtsync.LookProbes(cfg, probes)
	}
	return syncrun.StepOptions{Probes: probes}.Prober()
}

// tempOutside refuses a look that replays checks while the temporary
// directory, where its scratch copy, its helpers and the failed steps' output
// go, lies in a checkout of the repository or in its git dir.
func tempOutside(ctx *kit.Context) error {
	common, err := git.Run(ctx.Repo.MainRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	worktrees, err := ctx.Repo.Worktrees()
	if err != nil {
		return err
	}
	inside := []string{common, ctx.Repo.MainRoot}
	for _, wt := range worktrees {
		inside = append(inside, wt.Path)
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	temp := resolve(os.TempDir())
	for _, dir := range inside {
		if rel, err := filepath.Rel(resolve(dir), temp); err == nil && filepath.IsLocal(rel) {
			return fmt.Errorf("TMPDIR %s is inside %s: --each-commit=targets writes nothing in the repository; point TMPDIR outside it", temp, dir)
		}
	}
	return nil
}

// noMoveUsage is why a look with --each-commit=targets refuses --onto for a
// line with no move! commit, as the run does.
func noMoveUsage() error {
	return &config.InputError{Err: errors.New(wtsync.TargetsRefusal(false))}
}

// replayChecks runs the checks of the fold fl of s's line, checked out at
// path, on a scratch copy of the repository, and puts what they came to on
// fl. why is what the look could not finish, and outcome the error the
// command ends with; nil when every due step passed.
func replayChecks(w, progress io.Writer, path, trunkSHA string, cfg *wtsync.Config, s *syncrun.Stack, target *OntoTarget, fl *FoldLook) (res *wtsync.LineLook, why string, outcome error) {
	fl.Checks, fl.HooksNotRun = []FoldLookCheck{}, []string{}
	if fl.Refusal != nil {
		return nil, "", errors.New("the run refuses the fold, so no check ran")
	}
	if cfg == nil {
		return nil, "", fmt.Errorf("trunk declares no %s: the run refuses, so no check ran", wtsync.ConfigFile)
	}
	// Read, never taken: the look writes nothing here.
	if why, err := lockedOut(path); err != nil {
		return nil, err.Error(), err
	} else if why != "" {
		fl.refuse(why)
		return nil, "", errors.New("the run's checks were not replayed: " + why)
	}
	req := foldRequest(path, s, target, fl)
	req.Trunk = trunkSHA
	req.Stack.Forks = fl.forks
	// The failed steps' output outlives an interrupt, so it says where.
	var logs atomic.Pointer[string]
	defer kit.WatchSignals(w, func(w io.Writer) string {
		wtsync.CloseTempDirs()
		syncrun.OnInterrupt(w, wtsync.HeldLocks(), nil)
		if dir := logs.Load(); dir != nil {
			fmt.Fprintf(w, "the output of the checks that failed is in %s\n", *dir)
		}
		return ""
	})()
	res, refusal, err := wtsync.LookAtChecks(cfg, wtsync.LookRequest{Request: req, Folds: fl.analysis,
		Logs: func(dir string) { logs.Store(&dir) }}, progress)
	if refusal != "" {
		fl.refuse(refusal)
		return res, "", errors.New("the run's checks were not replayed: " + refusal)
	}
	fl.Checks = foldLookChecks(res)
	fl.HooksNotRun = append(fl.HooksNotRun, res.Hooks...)
	why, outcome = lookOutcome(res, err)
	return res, why, outcome
}

// lockedOut is why a run refuses the worktree at path for the lock another
// run holds on it, in the run's words; "" when none does.
func lockedOut(path string) (string, error) {
	gitDir, err := wtsync.GitDir(path)
	if err != nil {
		return "", err
	}
	if err := wtsync.Held(gitDir, time.Now()); err != nil {
		var held *wtsync.LockHeld
		if !errors.As(err, &held) {
			return "", err
		}
		return "locked: " + held.Error(), nil
	}
	return "", nil
}

// lookCounts is the results of every step of res, by result.
func lookCounts(res *wtsync.LineLook) map[string]int {
	n := map[string]int{}
	for _, v := range res.Visits {
		for _, s := range v.Steps {
			n[s.Result]++
		}
	}
	return n
}

// lookOutcome is what the replay could not finish, "" when it saw everything,
// and the error the command ends with. A failed check wins over everything
// else.
func lookOutcome(res *wtsync.LineLook, err error) (why string, outcome error) {
	n := lookCounts(res)
	switch {
	case err != nil:
		why = "the replay failed: " + err.Error()
	case res.Conflict != "":
		why = res.Conflict
	case len(res.Blocking) > 0:
		why = fmt.Sprintf("the look did not run the %s %s, which the run would", strings.Join(res.Blocking, ", "), plural(len(res.Blocking), "hook", "hooks"))
	}
	if len(res.Unlogged) > 0 {
		why = strings.TrimPrefix(why+"; "+strings.Join(res.Unlogged, "; "), "; ")
	}
	switch {
	case n[wtsync.LookFail] > 0:
		return why, kit.ExitError{Code: CheckFailedStatus}
	case why != "":
		return why, errors.New(why)
	case n[wtsync.LookOwed] > 0:
		return "", fmt.Errorf("%s owed: not every check ran", kit.Count(n[wtsync.LookOwed], "step"))
	case n[wtsync.LookIndeterminate] > 0:
		return "", fmt.Errorf("%s indeterminate: not every check is known", kit.Count(n[wtsync.LookIndeterminate], "step"))
	}
	return "", nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func foldLookChecks(res *wtsync.LineLook) []FoldLookCheck {
	out := []FoldLookCheck{}
	for _, v := range res.Visits {
		c := FoldLookCheck{Kind: v.Kind, Target: strpOrNil(v.Target), TargetSubject: strpOrNil(v.TargetSubject), Layer: v.Layer,
			Fixups: append([]string{}, v.Fixups...), AfterStep: strpOrNil(v.AfterStep), Steps: []FoldLookStep{}}
		for _, s := range v.Steps {
			c.Steps = append(c.Steps, FoldLookStep{Run: s.Run, Result: s.Result, Why: strpOrNil(s.Why), Log: strpOrNil(s.Log)})
		}
		out = append(out, c)
	}
	return out
}

func strpOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return jsonout.Strp(s)
}

// lookMarks is what the printed look puts before a step with each result.
var lookMarks = map[string]string{
	wtsync.LookPass: "✓", wtsync.LookFail: "✗", wtsync.LookOwed: "⏭", wtsync.LookNotRun: "·", wtsync.LookIndeterminate: "?",
}

// printReplayedChecks is the look's lines for the checks it ran: each visit
// with its steps, then each failure's log, and what was not seen.
func printReplayedChecks(w io.Writer, res *wtsync.LineLook, why string) {
	fmt.Fprintf(w, "\nchecks replayed on a scratch copy of the repository; nothing here moved\n")
	for _, v := range res.Visits {
		switch v.Kind {
		case wtsync.LookLayerStep:
			fmt.Fprintf(w, "  per-layer step of layer %s\n", v.Layer)
		default:
			at := "the commit of a per-layer step"
			if v.Target != "" {
				at = git.ShortID(v.Target, 7)
			}
			fmt.Fprintf(w, "  %s %s  (layer %s", at, kit.OneLine(v.TargetSubject), v.Layer)
			if n := len(v.Fixups); n > 0 {
				fmt.Fprintf(w, ", %s folded in", kit.Count(n, "commit"))
			}
			if v.AfterStep != "" {
				fmt.Fprintf(w, ", again after %s", kit.OneLine(v.AfterStep))
			}
			fmt.Fprintln(w, ")")
		}
		for _, s := range v.Steps {
			fmt.Fprintf(w, "    %s %s", lookMarks[s.Result], kit.OneLine(s.Run))
			switch s.Result {
			case wtsync.LookPass:
			case wtsync.LookNotRun:
				fmt.Fprintf(w, "  not run: an earlier step failed here")
			default:
				fmt.Fprintf(w, "  %s: %s", s.Result, kit.OneLine(s.Why))
			}
			fmt.Fprintln(w)
			if s.Log != "" {
				fmt.Fprintf(w, "      log %s\n", s.Log)
			}
		}
	}
	if why != "" {
		fmt.Fprintf(w, "  %s\n", kit.OneLine(why))
	}
	if len(res.Hooks) > 0 {
		fmt.Fprintf(w, "  not run here: %s, which the run runs as %s\n", strings.Join(res.Hooks, ", "), plural(len(res.Hooks), "a hook", "hooks"))
	}
	n := lookCounts(res)
	total := 0
	for _, c := range n {
		total += c
	}
	if total == 0 {
		if why == "" {
			fmt.Fprintln(w, "  no check is due")
		}
		return
	}
	var parts []string
	for _, r := range []struct{ result, words string }{{wtsync.LookPass, "passed"}, {wtsync.LookFail, "failed"},
		{wtsync.LookOwed, "owed"}, {wtsync.LookNotRun, "not run"}, {wtsync.LookIndeterminate, "indeterminate"}} {
		if c := n[r.result]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, r.words))
		}
	}
	fmt.Fprintf(w, "  %s\n", strings.Join(parts, ", "))
}
