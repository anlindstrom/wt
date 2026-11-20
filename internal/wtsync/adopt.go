package wtsync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// AdoptKeptRef is where wt sync resume --adopt keeps the tip it took over,
// before it moves anything: refs/wt-sync-kept/<branch>/<epoch>-adopted, or
// the first of its -2, -3, ... that is free.
func AdoptKeptRef(branch string, epoch int64) string {
	return fmt.Sprintf("%s-adopted", EditKeptRef(branch, epoch))
}

// Adoption is a rebase a person finished by hand, with commits of their own
// made on top, that the branch's reflog proves.
type Adoption struct {
	// Finish is the tip the rebase finished at, and Head the branch's tip
	// now.
	Finish, Head string
	// Commits is Finish..Head, oldest first: made after the rebase.
	Commits []Commit
	// Kept is the ref that keeps Head, once it is kept.
	Kept string
}

// reflogEntry is one move of a branch, as its reflog records it.
type reflogEntry struct{ old, new, msg string }

// readReflog is the branch's reflog, oldest first, from the file git keeps
// it in. A reflog that is not there cannot prove anything, so it is an
// error.
func readReflog(wtPath, branch string) ([]reflogEntry, error) {
	path, err := gitEnv(wtPath, nil, nil, "rev-parse", "--git-path", "logs/refs/heads/"+branch)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(wtPath, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []reflogEntry
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		head, msg, _ := strings.Cut(line, "\t")
		f := strings.Fields(head)
		if len(f) < 2 {
			return nil, fmt.Errorf("a line of it cannot be read: %q", line)
		}
		out = append(out, reflogEntry{old: f[0], new: f[1], msg: msg})
	}
	return out, nil
}

// madeOnTop is whether a reflog entry is one that adds commits on top: a
// commit or a cherry-pick. An amend, a reset, a merge, a pull or a rebase is
// not.
func madeOnTop(msg string) bool {
	return strings.HasPrefix(msg, "commit: ") || strings.HasPrefix(msg, "cherry-pick: ")
}

// ProveAdoption proves the one shape resume --adopt takes over: HEAD on the
// branch; the branch's reflog has the rebase's own finish, moving it from
// the run's start tip old to some F on top of onto; every later move is a
// commit added on top of the one before, with no reset, amend, merge or
// rebase between; and no more than total commits from onto to F (zero total
// is not checked). Anything else is an error naming what does not fit.
func ProveAdoption(wtPath, branch, onto, old string, total int) (Adoption, error) {
	ref, err := gitEnv(wtPath, rebaseEnv, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || ref != "refs/heads/"+branch {
		return Adoption{}, fmt.Errorf("HEAD is %q, not %s", ref, branch)
	}
	head, err := gitEnv(wtPath, rebaseEnv, nil, "rev-parse", "HEAD")
	if err != nil {
		return Adoption{}, err
	}
	entries, err := readReflog(wtPath, branch)
	if err != nil {
		return Adoption{}, fmt.Errorf("%s's reflog cannot be read (%w), so it cannot show how the rebase finished", branch, err)
	}
	a := Adoption{Head: head}
	cur := head
	found := false
	for i := len(entries) - 1; i >= 0 && !found; i-- {
		e := entries[i]
		if e.new != cur {
			return Adoption{}, fmt.Errorf("%s's reflog does not show how it came to be at %s, so it shows no \"rebase (finish)\" that moved it from the tip the run started from (%s)", branch, git.ShortID(cur, 7), git.ShortID(old, 7))
		}
		if rest, ok := strings.CutPrefix(e.msg, "rebase (finish): "); ok {
			if e.old != old {
				return Adoption{}, fmt.Errorf("the last rebase that finished on %s started from %s, not from the tip the run started from (%s)", branch, git.ShortID(e.old, 7), git.ShortID(old, 7))
			}
			if _, to, ok := strings.Cut(rest, " onto "); ok {
				want, err1 := commitOf(wtPath, onto)
				got, err2 := commitOf(wtPath, strings.TrimSpace(to))
				if err1 != nil || err2 != nil || got != want {
					return Adoption{}, fmt.Errorf("the rebase that finished on %s went onto %s, not onto %s", branch, git.ShortID(strings.TrimSpace(to), 7), git.ShortID(onto, 7))
				}
			}
			a.Finish, found = e.new, true
			break
		}
		if !madeOnTop(e.msg) {
			return Adoption{}, fmt.Errorf("%s's reflog shows %q after the rebase finished: only commits made on top of it can be adopted", branch, e.msg)
		}
		if e.old == e.new {
			return Adoption{}, fmt.Errorf("%s's reflog shows %q, which did not move it", branch, e.msg)
		}
		if _, code, err := gitEnvAllow(wtPath, rebaseEnv, nil, 1, "merge-base", "--is-ancestor", e.old, e.new); err != nil {
			return Adoption{}, err
		} else if code == 1 {
			return Adoption{}, fmt.Errorf("%s's reflog shows %q, which moved it to %s, and that does not descend from %s", branch, e.msg, git.ShortID(e.new, 7), git.ShortID(e.old, 7))
		}
		if merges, err := gitEnv(wtPath, rebaseEnv, nil, "rev-list", "--merges", e.old+".."+e.new); err != nil {
			return Adoption{}, err
		} else if merges != "" {
			return Adoption{}, fmt.Errorf("%s's reflog shows %q, which brought in a merge", branch, e.msg)
		}
		cur = e.old
	}
	if !found {
		return Adoption{}, fmt.Errorf("%s's reflog has no \"rebase (finish)\" that moved it from the tip the run started from (%s)", branch, git.ShortID(old, 7))
	}
	if a.Finish == old {
		return Adoption{}, fmt.Errorf("the rebase finished %s back at the tip the run started from", branch)
	}
	if _, code, err := gitEnvAllow(wtPath, rebaseEnv, nil, 1, "merge-base", "--is-ancestor", onto, a.Finish); err != nil {
		return Adoption{}, err
	} else if code == 1 {
		return Adoption{}, fmt.Errorf("the rebase finished %s at %s, which is not on top of what the run was rebasing onto (%s)", branch, git.ShortID(a.Finish, 7), git.ShortID(onto, 7))
	}
	if total > 0 {
		out, err := gitEnv(wtPath, rebaseEnv, nil, "rev-list", "--count", onto+".."+a.Finish)
		if err != nil {
			return Adoption{}, err
		}
		n, err := strconv.Atoi(out)
		if err != nil {
			return Adoption{}, fmt.Errorf("counting %s..%s: %w", git.ShortID(onto, 7), git.ShortID(a.Finish, 7), err)
		}
		if n > total {
			return Adoption{}, fmt.Errorf("the rebase finished %s with %d commits on top of what the run rebased onto, and the run set out to replay %d: something was committed inside the rebase", branch, n, total)
		}
	}
	if a.Commits, err = commitsIn(wtPath, a.Finish+".."+head); err != nil {
		return Adoption{}, err
	}
	return a, nil
}

// KeepAdopted keeps sha, the tip resume --adopt takes over, under
// AdoptKeptRef or the first of its -2, -3, ... that is free, and returns
// the ref. A ref is only ever created, never moved.
func KeepAdopted(mainRoot, branch string, epoch int64, sha string) (string, error) {
	return keepSnapshot(mainRoot, AdoptKeptRef(branch, epoch), sha, "wt sync: the adopted tip")
}

// AdoptedKept is every kept ref of resume --adopt for branch in the run of
// epoch, with the tip it keeps.
func AdoptedKept(mainRoot, branch string, epoch int64) ([]KeptRef, error) {
	base := AdoptKeptRef(branch, epoch)
	out, err := gitEnv(mainRoot, nil, nil, "for-each-ref", "--format=%(refname) %(objectname)", EditKeptPrefix+branch+"/")
	if err != nil {
		return nil, err
	}
	var kept []KeptRef
	for _, line := range strings.Split(out, "\n") {
		ref, tip, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if n, ok := strings.CutPrefix(ref, base); ok && (n == "" || strings.HasPrefix(n, "-")) {
			kept = append(kept, KeptRef{Ref: ref, Tip: tip})
		}
	}
	return kept, nil
}

// KeptRef is a ref wt kept a tip under, and the tip.
type KeptRef struct{ Ref, Tip string }

// Adopt is Resume for a rebase finished by hand that ProveAdoption proved:
// it proves the shape again, keeps the tip under AdoptKeptRef before
// anything moves, and reads back what the rebase produced. NewTip is the
// branch's tip now, the commits made after the rebase included; Adopted
// says where the rebase itself finished, which the layers and the report of
// what it emptied are read against. The replay report is made as for a
// rebase wt did not drive to its end: what cannot be matched is said.
func Adopt(mainRoot string, cfg *Config, req Request, old string, safety Safety, proved Adoption, log io.Writer) (Result, error) {
	mode, err := EachCommitMode(req.EachCommit)
	if err != nil {
		return Result{Branch: req.Branch}, err
	}
	req.EachCommit = mode
	d := &driver{mainRoot: mainRoot, cfg: cfg, req: req, log: log, old: old, keep: true,
		res: Result{Branch: req.Branch, OldTip: old, Safety: safety, EachCommit: mode, Timings: req.timings(), Layered: req.Stack != nil || req.ReportLine != nil}, stepFailed: map[string]int{}, made: map[string]bool{}}
	d.restoreLayers(req.Resumed)
	d.res.Unchecked = slices.Clone(req.UncheckedBefore)
	if d.res.SignaturesDropped, err = signedCount(req.Path, req.base(), old); err != nil {
		return d.res, err
	}
	a, err := ProveAdoption(req.Path, req.Branch, req.Onto, old, req.Total)
	if err != nil {
		return d.res, err
	}
	if a.Finish != proved.Finish || a.Head != proved.Head {
		return d.res, fmt.Errorf("%s moved while resume was checking it (now %s, finished at %s)", req.Branch, git.ShortID(a.Head, 7), git.ShortID(a.Finish, 7))
	}
	if a.Kept, err = KeepAdopted(mainRoot, req.Branch, req.Epoch, a.Head); err != nil {
		return d.res, err
	}
	if log != nil {
		fmt.Fprintf(log, "  ⚑ %s kept under %s before anything moves\n", git.ShortID(a.Head, 7), a.Kept)
	}
	count, err := d.git("rev-list", "--count", req.Onto+".."+a.Finish)
	if err != nil {
		return d.res, err
	}
	if d.res.Replayed, err = strconv.Atoi(count); err != nil {
		return d.res, err
	}
	d.res.NewTip = a.Finish
	d.reportReplay()
	d.res.NewTip = a.Head
	d.res.Adopted = &a
	return d.res, nil
}

// Tip is one tip resume --adopt checks: a layer's, or the line's own, with
// the tip below it (onto for the bottom one).
type Tip struct {
	Branch, Commit, Base string
}

// rangePaths is every path the commits of base..tip change.
func rangePaths(dir, base, tip string) ([]string, error) {
	out, err := gitEnv(dir, nil, nil, "log", "--no-merges", "--format=", "--name-only", "-z", base+".."+tip)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// tipSteps is the each_commit steps due at tip: those some commit of
// base..tip calls for, a step with no paths always. Every declared entry is
// its own step, with its own paths and needs, even where two share a run.
func tipSteps(dir string, cfg *Config, tip Tip) ([]EachCommit, error) {
	paths, err := rangePaths(dir, tip.Base, tip.Commit)
	if err != nil {
		return nil, err
	}
	return cfg.checksFor(paths), nil
}

// TipsDue is every each_commit entry due at one of tips at least, in the
// order declared, each declaration once.
func TipsDue(dir string, cfg *Config, tips []Tip) ([]EachCommit, error) {
	if cfg == nil {
		return nil, nil
	}
	due := make([]bool, len(cfg.EachCommit))
	for _, tip := range tips {
		paths, err := rangePaths(dir, tip.Base, tip.Commit)
		if err != nil {
			return nil, err
		}
		for j, s := range cfg.EachCommit {
			if len(s.Paths) == 0 || touchesAny(s.Paths, paths) {
				due[j] = true
			}
		}
	}
	var out []EachCommit
	for j, s := range cfg.EachCommit {
		if due[j] {
			out = append(out, s)
		}
	}
	return out, nil
}

// AdoptDue is what an adopt may run, for its needs to be probed before
// anything moves: the each_commit steps CheckTips runs at tips (none under
// off, given no tips), and the deferred steps run once after the rebase
// whose paths the line changed from old, where the run started, to head,
// the adopted commits included. The per-layer steps are owed, not run.
func AdoptDue(dir string, cfg *Config, tips []Tip, old, head string) (Due, error) {
	var due Due
	if cfg == nil {
		return due, nil
	}
	var err error
	if due.Checks, err = TipsDue(dir, cfg, tips); err != nil {
		return due, err
	}
	after := cfg.AfterSteps()
	if len(after) == 0 {
		return due, nil
	}
	paths, err := ChangedPaths(dir, old, head)
	if err != nil {
		return due, err
	}
	for _, s := range after {
		if len(s.Paths) == 0 || anyMatches(s.Paths, paths) {
			due.Deferred = append(due.Deferred, s)
		}
	}
	return due, nil
}

// TipChecks is what CheckTips came to: the checks that failed and the ones
// that could not run, both owed, and how many tips a check ran at.
type TipChecks struct {
	Failed    []TipFailure
	Unchecked []Unchecked
	Checked   int
}

// TipFailure is a check that failed at a tip.
type TipFailure struct {
	Branch string
	CheckFailure
}

// Owed names the failure the way the closing error names what is owed.
func (f TipFailure) Owed() string {
	return fmt.Sprintf("%s at %s (failed at %s: %s)", f.Step, f.Branch, git.ShortID(f.Commit, 7), f.Err)
}

// CheckTips runs the each_commit steps at each tip, the way
// --each-commit=tips does at a layer's end: the steps some commit of
// base..tip calls for. A tip that is the worktree's HEAD is checked in the
// worktree; any other in a temporary detached worktree of its own, removed
// after, so the person's checkout never leaves the branch. A tip already
// checked (an emptied layer ends where the one below does) is not checked
// again. A step whose need did not answer, or whose script the tip lacks,
// is not run and is owed.
func CheckTips(wtPath string, cfg *Config, tips []Tip, unavailable map[string]string, rec *Timings, log io.Writer) TipChecks {
	var res TipChecks
	if cfg == nil || len(cfg.EachCommit) == 0 {
		return res
	}
	head, _ := gitEnv(wtPath, nil, nil, "rev-parse", "HEAD")
	seen := map[string]bool{}
	uncheck := func(step, why string) {
		if !slices.ContainsFunc(res.Unchecked, func(u Unchecked) bool { return u.Step == step }) {
			res.Unchecked = append(res.Unchecked, Unchecked{Step: step, Why: why})
		}
	}
	for _, tip := range tips {
		if seen[tip.Commit] {
			continue
		}
		seen[tip.Commit] = true
		steps, err := tipSteps(wtPath, cfg, tip)
		if err != nil {
			for _, s := range cfg.EachCommit {
				uncheck(s.Run, "the paths of "+tip.Branch+" could not be read: "+err.Error())
			}
			continue
		}
		if len(steps) == 0 {
			continue
		}
		subject, _ := gitEnv(wtPath, nil, nil, "log", "-1", "--format=%s", tip.Commit)
		dir, done, err := checkoutAt(wtPath, tip.Commit, head)
		if err != nil {
			for _, s := range steps {
				uncheck(s.Run, fmt.Sprintf("%s's tip %s could not be checked out: %v", tip.Branch, git.ShortID(tip.Commit, 7), err))
			}
			continue
		}
		started := false
		for _, s := range steps {
			if why := unavailableWhy(s.Needs, unavailable); why != "" {
				uncheck(s.Run, why)
				continue
			}
			if script := repoScript(s.Run); script != "" && !gitHas(wtPath, tip.Commit, script) {
				if _, err := os.Stat(filepath.Join(dir, script)); err != nil {
					uncheck(s.Run, fmt.Sprintf("%s is not in the tree at %s", script, git.ShortID(tip.Commit, 7)))
					continue
				}
			}
			if !started {
				started = true
				res.Checked++
				if log != nil {
					fmt.Fprintf(log, "  ▸ check %s at its tip %s %s\n", tip.Branch, git.ShortID(tip.Commit, 7), subject)
				}
			}
			why, output, retried, took := runTimed(rec, tip.Branch+" tip "+git.ShortID(tip.Commit, 7), dir, s, EachCommitTimeout)
			if why == "" {
				logCheck(log, cfg, s, why, retried, took)
				continue
			}
			f := TipFailure{Branch: tip.Branch, CheckFailure: CheckFailure{Step: s.Run, Commit: tip.Commit, Subject: subject, Err: why, Output: output, Retried: retried}}
			res.Failed = append(res.Failed, f)
			if log != nil {
				fmt.Fprintf(log, "  ✗ %s at %s: %s  %s\n", s.Run, tip.Branch, why, FormatDuration(took))
				for _, line := range lastOutputLines(output, 5) {
					fmt.Fprintf(log, "    %s\n", line)
				}
			}
		}
		done()
	}
	return res
}

// checkoutAt is where commit is checked out for a check: the worktree
// itself when commit is its HEAD, otherwise a temporary detached worktree
// that done removes. Only that worktree's own registration is ever removed:
// never a prune, which would also drop another worktree whose directory is
// missing, and with it a HEAD that may be all that keeps its commits alive.
// An interrupt removes the registration too (RemoveTempDirs).
func checkoutAt(wtPath, commit, head string) (dir string, done func(), err error) {
	if commit == head {
		return wtPath, func() {}, nil
	}
	common, err := gitEnv(wtPath, nil, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", nil, err
	}
	tmp, cleanup, err := tempDir("wt-sync-tip-*")
	if err != nil {
		return "", nil, err
	}
	// git records the checkout's real path; the registration is found by it.
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	dir = filepath.Join(tmp, "tip")
	registerTempWorktree(dir, common)
	remove := func() {
		if _, err := gitEnv(wtPath, nil, nil, "worktree", "remove", "--force", dir); err != nil {
			forgetTempWorktree(dir, common)
		}
		unregisterTempWorktree(dir)
		cleanup()
	}
	// No hooks: a post-checkout hook is the repository's, not a check's.
	if _, err := gitEnv(wtPath, nil, nil, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", "--quiet", dir, commit); err != nil {
		forgetTempWorktree(dir, common)
		unregisterTempWorktree(dir)
		cleanup()
		return "", nil, err
	}
	return dir, remove, nil
}

// AdoptedLayerSteps is the per-layer steps a rebase finished by hand never
// reached, owed: each step at each layer whose commits change its paths (or
// every layer, for a step without paths), unless done already records what
// it came to there. A per-layer step folds into the layer's own commits
// inside the rebase, so after it there is nothing to run it in.
func AdoptedLayerSteps(dir string, cfg *Config, done []LayerStep, tips []Tip) ([]LayerStep, error) {
	var owed []LayerStep
	steps := cfg.LayerSteps()
	for i, tip := range tips {
		var paths []string
		for _, s := range steps {
			if slices.ContainsFunc(done, func(d LayerStep) bool { return d.Branch == tip.Branch && d.Step.Run == s.Run }) {
				continue
			}
			if len(s.Paths) > 0 {
				if paths == nil {
					var err error
					if paths, err = rangePaths(dir, tip.Base, tip.Commit); err != nil {
						return nil, err
					}
				}
				if !anyMatches(s.Paths, paths) {
					continue
				}
			}
			owed = append(owed, LayerStep{Layer: i + 1, Layers: len(tips), Branch: tip.Branch, Step: s, Result: LayerOwed,
				Why: "not run: the rebase was finished by hand, past this layer's end"})
		}
	}
	return owed, nil
}
