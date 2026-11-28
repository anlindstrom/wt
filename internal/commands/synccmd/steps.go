package synccmd

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/jsonout"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// stepsDue is stepsDueIn for a plain run.
func stepsDue(t wtsync.Target, cfg *wtsync.Config, parents map[string]string, wt repo.Worktree, a wtsync.Assessment) wtsync.Due {
	return stepsDueIn(verdictMode{}, t, cfg, parents, wt, a)
}

// stepsDueIn is what of the declaration's steps a run of wt may run, known
// before it starts; nothing for a worktree a run would not rebase. A stack
// child's commits are counted from its parent's tip, as its rebase replays
// them. An error reading it counts as nothing due: the overview is advice,
// and a run reads it again and refuses on an error.
func stepsDueIn(mode verdictMode, t wtsync.Target, cfg *wtsync.Config, parents map[string]string, wt repo.Worktree, a wtsync.Assessment) wtsync.Due {
	if cfg == nil || wt.Branch == "" || a.OnlyFastForward() {
		return wtsync.Due{}
	}
	a, v, _ := mode.verdict(a)
	if v != wtsync.Proceed {
		return wtsync.Due{}
	}
	base := cmp.Or(t.Upstream, t.Onto)
	if parent, ok := parents[wt.Branch]; ok {
		base = "refs/heads/" + parent
	}
	due, err := wtsync.StepsDue(wt.Path, cfg, base, rebasedTip(wt.Branch, a), t.Onto, t.Upstream)
	if err != nil {
		return wtsync.Due{}
	}
	return due.WithOwed(wt.Path, cfg, wt.Branch)
}

// stackRefusal is why a run refuses the --stack fold of s, checked out as
// work's worktree assessed as a, before it plans anything; "" when it goes.
func stackRefusal(cfg *wtsync.Config, s *syncrun.Stack, work string, a wtsync.Assessment) string {
	if cfg != nil {
		if why := cfg.StackFoldRefusal(); why != "" {
			return why
		}
		if len(s.Layers) == 0 {
			return noLayerRefusal(work).Error()
		}
	}
	if _, v, why := (verdictMode{stack: true, rewrites: true}).verdict(a); v != wtsync.Proceed {
		return why
	}
	return ""
}

// ontoFoldRefusal is why a run onto target refuses a fixup of s's line, as it
// does with or without a move! commit; "" when it takes them.
func ontoFoldRefusal(main string, s *syncrun.Stack, target *OntoTarget) (string, error) {
	commits, err := lineCommits(main, s)
	if err != nil {
		return "", err
	}
	return wtsync.FixupOfCarried(lineDir(main, s), target.SHA, target.Upstream, s.Branch, commits)
}

// needRefusal is why a run refuses the worktree for a need of due that does not
// answer, "" when they all do.
func needRefusal(prober *syncrun.Prober, due wtsync.Due) string {
	_, why := syncrun.NeedsFor(prober, due, false)
	return why
}

// foldDue is what a run of the fold fl of s's line may run, and the commits it
// stops to check, as the run reads them; fl is nil for a fold onto a target
// with no move! commit.
func foldDue(main, path string, cfg *wtsync.Config, s *syncrun.Stack, target *OntoTarget, fl *FoldLook) (wtsync.Due, []wtsync.FoldStop, string, error) {
	var commits []string
	var err error
	if fl != nil {
		commits = fl.commits
	} else if commits, err = lineCommits(main, s); err != nil {
		return wtsync.Due{}, nil, "", err
	}
	req := lineRequest(path, s, target, commits)
	var known *wtsync.Folds
	if fl != nil {
		known = &fl.analysis
		req.EachCommit = fl.mode()
	}
	due, stops, err := wtsync.RewriteDue(main, cfg, req, known)
	return due, stops, req.EachCommit, err
}

// rebasedTip is what a run rebases of branch: its own remote's commit when
// it is behind that remote and the run fast-forwards it there first, so the
// commits only the remote has are counted too; the branch otherwise.
func rebasedTip(branch string, a wtsync.Assessment) string {
	if a.Own.FastForwards() && a.Own.Commit != "" {
		return a.Own.Commit
	}
	return branch
}

// parentsOf is the stack parents of every worktree, nil when they cannot be
// read: then every branch's commits count from trunk.
func parentsOf(ctx *kit.Context, onto string) map[string]string {
	all, err := ctx.Repo.Worktrees()
	if err != nil {
		return nil
	}
	parents, _, err := wtsync.Parents(ctx.Repo.MainRoot, onto, all)
	if err != nil {
		return nil
	}
	return parents
}

// checksLine is the overview's line for a worktree whose run checks commits.
func checksLine(n int) string {
	return "checks " + kit.Count(n, "commit") + " with the each_commit steps"
}

// checkView is the detail view's each_commit section: how many commits a run
// checks, what decides which, and the commits listed.
type checkView struct {
	head string
	rows []checkRow
}

// checkRow is a listed commit; note says why the run does not check it.
type checkRow struct {
	wtsync.Commit
	note string
}

// replayedChecks is the section for a run that checks each commit it replays.
func replayedChecks(due wtsync.Due) checkView {
	v := checkView{head: checksLine(len(due.CheckCommits)) + ", once each is replayed: those a step's paths match (a step with none: all)"}
	for _, c := range due.CheckCommits {
		v.rows = append(v.rows, checkRow{Commit: c})
	}
	return v
}

// foldedChecks is the section for a fold that checks at stops under mode:
// the commits it checks, then those in a layer it leaves unchanged.
func foldedChecks(stops []wtsync.FoldStop, mode string, atMost bool) checkView {
	var checked, unchanged []checkRow
	for _, s := range stops {
		if s.Untouched != "" {
			unchanged = append(unchanged, checkRow{Commit: s.Commit, note: "not checked: the fold leaves layer " + s.Untouched + " unchanged"})
		} else {
			checked = append(checked, checkRow{Commit: s.Commit})
		}
	}
	count := checksLine(len(checked))
	if atMost {
		count = strings.Replace(count, "checks ", "checks up to ", 1)
	}
	head := count + ", fixups folded in first: those a step's paths match (a step with none: all)"
	if mode == wtsync.EachTargets {
		head = count + " under --each-commit=targets: at fold targets, move sources and layer tips"
	}
	return checkView{head: head, rows: append(checked, unchanged...)}
}

// printChecks lists the commits a run would check, for the detail view.
func printChecks(w io.Writer, v checkView, due wtsync.Due) {
	if len(v.rows) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", v.head)
	for _, c := range v.rows {
		fmt.Fprintf(w, "  %s  %s", git.ShortID(c.SHA, 7), kit.OneLine(c.Subject))
		if c.note != "" {
			fmt.Fprintf(w, "  (%s)", c.note)
		}
		fmt.Fprintln(w)
	}
	for _, s := range due.Checks {
		fmt.Fprintf(w, "  ▸ %s\n", s.Run)
	}
}

// eachCommitSteps is cfg's each_commit steps for --json.
func eachCommitSteps(cfg *wtsync.Config) []DeclaredStep {
	steps := []DeclaredStep{}
	if cfg == nil {
		return steps
	}
	for _, s := range cfg.EachCommit {
		steps = append(steps, DeclaredStep{Step: s.Run, Paths: append([]string{}, s.Paths...), Needs: append([]string{}, s.Needs...)})
	}
	return steps
}

// checkCommits is due's commits for --json.
func checkCommits(due wtsync.Due) []CheckCommit {
	out := []CheckCommit{}
	for _, c := range due.CheckCommits {
		out = append(out, CheckCommit{Commit: c.SHA, Subject: c.Subject})
	}
	return out
}

// needProbes is every need cfg declares, probed when a step of some due
// would run with it.
func needProbes(cfg *wtsync.Config, dues []wtsync.Due, prober *syncrun.Prober) []NeedProbe {
	out := []NeedProbe{}
	names := cfg.DeclaredNeeds()
	for _, n := range names {
		np := NeedProbe{Need: n}
		for _, d := range dues {
			for _, dn := range d.Needs() {
				if dn == n {
					np.Probed = true
				}
			}
		}
		if np.Probed {
			if err := prober.Probe(n); err != nil {
				np.Error = jsonout.Strp(kit.OneLine(err.Error()))
			} else {
				np.OK = true
			}
		}
		out = append(out, np)
	}
	return out
}

// dueNeeds is needProbes for the needs of due alone.
func dueNeeds(cfg *wtsync.Config, due wtsync.Due, prober *syncrun.Prober) []NeedProbe {
	needed := due.Needs()
	return slices.DeleteFunc(needProbes(cfg, []wtsync.Due{due}, prober), func(p NeedProbe) bool { return !slices.Contains(needed, p.Need) })
}

// printNeeds says which probed needs did not answer, for the overview.
func printNeeds(w io.Writer, probes []NeedProbe) {
	for _, p := range probes {
		if p.Probed && !p.OK {
			fmt.Fprintf(w, "\n⚠ %s did not answer: %s; a run refuses the worktrees whose steps need it, or owes those steps with --allow-owed\n", p.Need, *p.Error)
		}
	}
}

// lineRequest is the rebase a fold of s's line, with the commits of it,
// checked out at path, makes: in place, or onto target.
func lineRequest(path string, s *syncrun.Stack, target *OntoTarget, commits []string) wtsync.Request {
	line := wtsync.Line{Branch: s.Branch, Head: s.Head, Base: s.Base, Commits: commits}
	for _, l := range s.Layers {
		line.Layers = append(line.Layers, wtsync.LineLayer{Branch: l.Branch, Tip: l.OldTip, Count: l.Commits})
	}
	req := wtsync.Request{Path: path, Branch: s.Branch, Onto: s.Base, EachCommit: wtsync.EachAll,
		Stack: &wtsync.StackRequest{Line: line, Autosquash: true}}
	if target != nil {
		req.Onto, req.Upstream = target.SHA, target.Upstream
	}
	return req
}

// foldRequest is lineRequest for the fold fl, under the mode it runs in.
func foldRequest(path string, s *syncrun.Stack, target *OntoTarget, fl *FoldLook) wtsync.Request {
	req := lineRequest(path, s, target, fl.commits)
	req.EachCommit = fl.mode()
	return req
}

// mode is where the checks of a run of the fold go: targets when the look
// asks for it or a move! commit needs it.
func (fl *FoldLook) mode() string {
	if len(fl.analysis.Moves) > 0 || fl.look.replaysChecks() {
		return wtsync.EachTargets
	}
	return wtsync.EachAll
}
