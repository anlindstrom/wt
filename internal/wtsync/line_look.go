package wtsync

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/anlindstrom/wt/internal/git"
)

// What a LineLook step came to.
const (
	LookPass   = "pass"
	LookFail   = "fail"
	LookOwed   = "owed"
	LookNotRun = "notRun"
	// LookIndeterminate is a step run on a checkout the look could not put
	// back after an earlier failure.
	LookIndeterminate = "indeterminate"
)

// The kinds of a LookVisit.
const (
	// LookStop is a stop the rebase made to check.
	LookStop = "stop"
	// LookRecheck is a commit checked again after a per-layer step folded.
	LookRecheck = "recheck"
	// LookLayerStep is a per-layer step that failed or is owed.
	LookLayerStep = "layerStep"
)

// LineLook is what the checks of a rebase came to, run on a scratch copy of
// the repository.
type LineLook struct {
	// Visits is every stop that ran or owed a step, in the order the rebase
	// made them.
	Visits []*LookVisit
	// Conflict says where the replay stopped for a person; "" when it
	// finished.
	Conflict string
	// Hooks is the active hooks the rebase of the source would run, which the
	// look did not; Blocking is those of them that could refuse the rebase or
	// change what it makes.
	Hooks, Blocking []string
	// Unlogged says which failed steps' output could not be kept, and why.
	Unlogged []string
}

// LookVisit is one visit of the rebase.
type LookVisit struct {
	Kind string
	// Target is the commit of the line, as it is now, the visit is for; ""
	// for a per-layer step and for a commit such a step made.
	Target, TargetSubject string
	Layer                 string
	// Fixups is the fixup! and move! commits folded into Target.
	Fixups []string
	// AfterStep is the per-layer step a recheck follows.
	AfterStep string
	Steps     []LookStep
}

// LookStep is one step of a visit. Why is set for a step that failed, is owed
// or is indeterminate; Log, the file with its output, for one that failed.
type LookStep struct {
	Run, Result, Why, Log string
}

// LookRequest is the rebase LookAtChecks replays: Request as the run would
// make it, in the worktree of the line, and the line's folds as read there.
type LookRequest struct {
	Request Request
	Folds   Folds
	// Logs is told the directory of the failed steps' output, once there is
	// one.
	Logs func(dir string)
}

// checkLook is what a driver records instead of stopping at a failed check.
type checkLook struct {
	look   *LineLook
	fixups map[string][]string
	// folded is the commits of the line a fold takes into another.
	folded map[string]bool
	// root holds the scratch repository and the snapshots; made is its name
	// as registered for removal.
	root, made string
	snapshots  int
	// origins names the commit of the line each recommitted one was.
	origins map[string]string
	// committed is whether a per-layer step came to commit, which would run
	// the hooks of a commit.
	committed bool
	// step is the per-layer step running.
	step   string
	logs   string
	logged int
	onLogs func(string)
	// dirty is why a later step runs on a checkout that was not put back.
	dirty string
}

// LookAtChecks replays req on a scratch repository that reads the source's
// objects, running the each_commit checks and per-layer steps where the run
// would, and goes on past a check that fails. Nothing is written in the
// source. refusal is why nothing ran. An error leaves what was seen before it
// in the LineLook.
func LookAtChecks(cfg *Config, req LookRequest, progress io.Writer) (res *LineLook, refusal string, err error) {
	res = &LineLook{}
	r := req.Request
	source := r.Path
	hooks, err := rebaseHooks(source, cfg)
	if err != nil {
		return res, "the hooks the run would run could not be listed: " + kitOneLine(err.Error()), nil
	}
	if base := os.Getenv("GIT_REPLACE_REF_BASE"); base != "" && strings.TrimSuffix(base, "/") != "refs/replace" {
		return res, fmt.Sprintf("GIT_REPLACE_REF_BASE is %s: the replay copies the replacements under refs/replace only; unset it", base), nil
	}
	for _, h := range hooks {
		res.Hooks = append(res.Hooks, h.name)
	}
	made, cleanup, err := tempDir("wt-sync-look-*")
	if err != nil {
		return res, "", err
	}
	defer cleanup()
	root, err := filepath.EvalSymlinks(made)
	if err != nil {
		return res, "", err
	}
	defer git.Confine(root)()
	repo := filepath.Join(root, "repo")
	if refusal, err = buildScratch(source, repo, r); err != nil || refusal != "" {
		if err != nil {
			err = fmt.Errorf("the scratch repository could not be made: %w", err)
		}
		return res, refusal, err
	}
	if refusal, err = sameFolds(cfg, repo, r, req.Folds); err != nil || refusal != "" {
		return res, refusal, err
	}
	l := &checkLook{look: res, fixups: map[string][]string{}, folded: map[string]bool{}, root: root, made: made, origins: map[string]string{}, onLogs: req.Logs}
	l.readFolds(req.Folds)
	r.Path, r.look = repo, l
	if r.Epoch == 0 {
		r.Epoch = time.Now().Unix()
	}
	done, err := Rebase(repo, cfg, r, progress)
	for _, h := range hooks {
		if h.refuses || h.commits && l.committed {
			res.Blocking = append(res.Blocking, h.name)
		}
	}
	if err != nil {
		return res, "", err
	}
	if h := done.Left; h != nil {
		res.Conflict = fmt.Sprintf("the replay stopped at %s %q on a conflict that is yours (%s): the stops after it were not reached",
			git.ShortID(h.Commit, 7), h.Subject, strings.Join(h.Left, ", "))
	}
	return res, "", nil
}

// rebaseHook is an active hook a rebase would run: one that refuses can stop
// it, and one of commits runs only where a per-layer step commits, and can
// refuse that commit or change it.
type rebaseHook struct {
	name             string
	refuses, commits bool
}

// rebaseHooks is the active hooks a rebase in the worktree at path would run.
func rebaseHooks(path string, cfg *Config) ([]rebaseHook, error) {
	declared := slices.ContainsFunc(cfg.LayerSteps(), func(d Deferred) bool { return d.Commit != "" })
	candidates := []rebaseHook{{name: "pre-rebase", refuses: true}, {name: "prepare-commit-msg", commits: true}}
	if declared {
		candidates = append(candidates, rebaseHook{name: "pre-commit", commits: true}, rebaseHook{name: "commit-msg", commits: true})
	}
	for _, name := range []string{"post-checkout", "post-commit", "post-rewrite", "reference-transaction", "post-index-change"} {
		candidates = append(candidates, rebaseHook{name: name})
	}
	var out []rebaseHook
	for _, h := range candidates {
		active, err := hookRuns(path, h.name)
		if err != nil {
			return nil, err
		}
		if active {
			out = append(out, h)
		}
	}
	return out, nil
}

// hookRuns is whether git run in the worktree at path has a hook name to run:
// an enabled entry of git hook list, a file or a configured one. A git without
// that listing has files only.
func hookRuns(path, name string) (bool, error) {
	out, err := gitEnv(path, nil, nil, "hook", "list", name)
	if err == nil {
		return slices.ContainsFunc(strings.Split(out, "\n"), func(l string) bool {
			return l != "" && !strings.HasPrefix(l, "disabled\t")
		}), nil
	}
	var gerr *git.Error
	if !errors.As(err, &gerr) || gerr.TimedOut {
		return false, err
	}
	said := strings.TrimSpace(gerr.Stderr)
	switch {
	// How git says it has nothing to list.
	case gerr.Code == 1 && (said == "" || strings.HasPrefix(said, "warning: no hooks found")):
		return false, nil
	case !strings.Contains(said, "unknown subcommand") && !strings.Contains(said, "is not a git command"):
		return false, err
	}
	dir, err := gitEnv(path, nil, nil, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return false, err
	}
	return activeHook(dir, name), nil
}

// scratchConfig says the setting key of the source decides what a rebase or a
// checkout of it produces, and names no other repository.
func scratchConfig(key string) bool {
	section, _, _ := strings.Cut(key, ".")
	switch section {
	case "merge", "diff", "filter", "i18n":
		return true
	}
	switch key {
	case "user.name", "user.email", "core.autocrlf", "core.eol", "core.safecrlf", "core.symlinks", "core.ignorecase",
		"core.precomposeunicode", "core.filemode", "core.attributesfile", "core.excludesfile", "core.quotepath",
		"core.abbrev", "core.usereplacerefs", "core.checkstat", "core.trustctime", "core.commentchar", "core.longpaths":
		return true
	}
	return false
}

// buildScratch makes the repository at repo: the source's objects borrowed,
// its branches, remote-tracking refs, tags and replacements as they are, the line's branch
// checked out, and no remote. refusal is that the line is no longer the one
// r was read from.
func buildScratch(source, repo string, r Request) (refusal string, err error) {
	ask := func(args ...string) (string, error) { return gitEnv(source, nil, nil, args...) }
	common, err := ask("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	objects, err := ask("rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", err
	}
	format, err := ask("rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	root := filepath.Dir(repo)
	if _, err := gitEnv(root, nil, nil, "init", "-q", "--template=", "--object-format="+format, repo); err != nil {
		return "", err
	}
	if err := writeIn(repo, ".git/objects/info/alternates", []byte(objects+"\n")); err != nil {
		return "", err
	}
	for _, name := range []string{"shallow", "info/attributes", "info/exclude"} {
		b, err := os.ReadFile(filepath.Join(common, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = writeIn(repo, ".git/"+name, b)
		}
		if err != nil {
			return "", err
		}
	}
	in := func(args ...string) error {
		_, err := gitEnv(repo, nil, nil, args...)
		return err
	}
	listed, err := ask("config", "--list", "--includes", "-z")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(listed, "\x00") {
		key, value, valued := strings.Cut(entry, "\n")
		if !scratchConfig(key) {
			continue
		}
		if !valued {
			value = "true"
		}
		if err := in("config", "--add", key, value); err != nil {
			return "", err
		}
	}
	for _, kv := range [][2]string{{"core.hooksPath", os.DevNull}, {"commit.gpgsign", "false"}, {"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		if err := in("config", kv[0], kv[1]); err != nil {
			return "", err
		}
	}
	refs, err := ask("for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/remotes", "refs/tags", "refs/replace")
	if err != nil {
		return "", err
	}
	at := map[string]string{}
	var create strings.Builder
	for _, line := range strings.Split(refs, "\n") {
		if sha, ref, ok := strings.Cut(line, " "); ok {
			at[ref] = sha
			fmt.Fprintf(&create, "create %s %s\n", ref, sha)
		}
	}
	if r.Stack != nil {
		line := r.Stack.Line
		read := append([]LineLayer{{Branch: line.Branch, Tip: line.Head}}, line.Layers...)
		for _, l := range read {
			if now := at["refs/heads/"+l.Branch]; now != l.Tip {
				return fmt.Sprintf("%s moved since the line was read (%s then, %s now): look again",
					l.Branch, git.ShortID(l.Tip, 7), git.ShortID(now, 7)), nil
			}
		}
	}
	if _, err := gitEnv(repo, nil, strings.NewReader(create.String()), "update-ref", "--stdin"); err != nil {
		return "", err
	}
	if err := copyOwed(source, repo, r.Branch); err != nil {
		return "", err
	}
	return "", in("checkout", "-q", "-f", r.Branch, "--")
}

// copyOwed gives the scratch the record of what the last run of branch left
// owed, which decides where the rebase stops. One that cannot be read there
// cannot be read here either, so every step runs, as in the run.
func copyOwed(source, repo, branch string) error {
	from, err := owedPath(source, branch)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		b = []byte("unreadable")
	}
	to, err := owedPath(repo, branch)
	if err != nil {
		return err
	}
	name, err := filepath.Rel(repo, to)
	if err != nil {
		return err
	}
	return writeIn(repo, name, b)
}

// writeIn writes b to the file name under dir, never outside it.
func writeIn(dir, name string, b []byte) (err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return root.WriteFile(name, b, 0o644)
}

// sameFolds refuses a line whose fixup! and move! commits name other commits
// in the scratch than in the source: a name may be a revision only the source
// resolves, a reflog entry or an upstream.
func sameFolds(cfg *Config, repo string, r Request, want Folds) (string, error) {
	if r.Stack == nil {
		return "", nil
	}
	commits := r.Stack.Line.Commits
	pairs, err := FoldPairs(repo, commits)
	if err != nil {
		return "", err
	}
	got := map[string]string{}
	for _, p := range pairs {
		got[p.Fixup] = p.Target
	}
	differs := func(sha, subject string) string {
		return fmt.Sprintf("%s %q names another commit in the look's scratch repository than here: name its target by its subject or full id",
			git.ShortID(sha, 7), subject)
	}
	for _, p := range want.Pairs {
		if got[p.Fixup] != p.Target {
			return differs(p.Fixup, p.FixupSubject), nil
		}
		delete(got, p.Fixup)
	}
	for _, p := range pairs {
		if _, extra := got[p.Fixup]; extra {
			return differs(p.Fixup, p.FixupSubject), nil
		}
	}
	if len(want.Moves) == 0 {
		return "", nil
	}
	moves, why, err := PlanMovesOnto(repo, cfg, commits, r.Onto, r.Upstream)
	if err != nil {
		return "", err
	}
	for _, m := range want.Moves {
		same := func(n PartMove) bool { return n.Commit == m.Commit && n.Target == m.Target && n.Source == m.Source }
		if why != "" || !slices.ContainsFunc(moves, same) {
			return differs(m.Commit, m.Subject), nil
		}
	}
	return "", nil
}

func (l *checkLook) readFolds(f Folds) {
	into := map[string]string{}
	for _, p := range f.Pairs {
		into[p.Fixup], l.folded[p.Fixup] = p.Target, true
	}
	for _, p := range f.Pairs {
		target := p.Target
		for into[target] != "" {
			target = into[target]
		}
		l.fixups[target] = append(l.fixups[target], p.Fixup)
	}
	for _, m := range f.Moves {
		l.folded[m.Commit] = true
		l.fixups[m.Target] = append(l.fixups[m.Target], m.Commit)
	}
}

// visit records a visit for the commit of the line target, every step of
// steps not run yet; nil with no step.
func (l *checkLook) visit(d *driver, kind, target, layer string, steps []EachCommit) (*LookVisit, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	v := &LookVisit{Kind: kind, Target: target, Layer: layer}
	if target != "" {
		v.Fixups = l.fixups[target]
		if n := d.layerOfCommit(target); n > 0 {
			v.Layer = d.layerName(n)
		}
		var err error
		if v.TargetSubject, err = d.git("log", "-1", "--format=%s", target); err != nil {
			return nil, err
		}
	}
	for _, s := range steps {
		v.Steps = append(v.Steps, LookStep{Run: s.Run, Result: LookNotRun})
	}
	l.look.Visits = append(l.look.Visits, v)
	return v, nil
}

// owed records that step i of v is owed for why.
func (l *checkLook) owed(v *LookVisit, i int, why string) {
	if v != nil {
		l.result(&v.Steps[i], LookOwed, why)
	}
}

// result sets what step s came to, unless the checkout it ran on says nothing.
func (l *checkLook) result(s *LookStep, result, why string) {
	if l.dirty != "" {
		result, why = LookIndeterminate, l.dirty
	}
	s.Result, s.Why = result, why
}

// ran records what step i of v came to, with output the whole of what it
// printed.
func (l *checkLook) ran(v *LookVisit, i int, why string, output []byte) {
	s := &v.Steps[i]
	result := LookPass
	if why != "" {
		result = LookFail
	}
	l.result(s, result, why)
	if s.Result == LookFail {
		s.Log = l.log(v.Target, s.Run, output)
	}
}

// log keeps output in a file of its own, outside the scratch, and returns
// its path; "" where it could not, which the LineLook says.
func (l *checkLook) log(target, run string, output []byte) string {
	path, err := l.writeLog(target, run, output)
	if err != nil {
		l.look.Unlogged = append(l.look.Unlogged, fmt.Sprintf("the output of %q could not be kept: %v", run, err))
		return ""
	}
	return path
}

func (l *checkLook) writeLog(target, run string, output []byte) (string, error) {
	if l.logs == "" {
		dir, err := os.MkdirTemp("", "wt-sync-checks-*")
		if err != nil {
			return "", err
		}
		l.logs = dir
		if l.onLogs != nil {
			l.onLogs(dir)
		}
	}
	l.logged++
	label := strings.Map(func(r rune) rune {
		if r == '-' || r == '.' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, checkLabel(nil, EachCommit{Run: run}))
	if len(label) > 40 {
		label = label[:40]
	}
	name := fmt.Sprintf("%02d-%s.log", l.logged, label)
	if target != "" {
		name = fmt.Sprintf("%02d-%s-%s.log", l.logged, git.ShortID(target, 7), label)
	}
	path := filepath.Join(l.logs, name)
	return path, os.WriteFile(path, output, 0o644)
}

// rechecked is visit for the replayed commit c, checked again after the
// running per-layer step folded.
func (l *checkLook) rechecked(d *driver, c string, steps []EachCommit) (*LookVisit, error) {
	target, err := l.original(d, c)
	if err != nil {
		return nil, err
	}
	branch := ""
	if n, _, err := d.layerAt(); err == nil && n > 0 {
		branch = d.layerName(n)
	}
	v, err := l.visit(d, LookRecheck, target, branch, steps)
	if err != nil || v == nil {
		return v, err
	}
	v.AfterStep = l.step
	if target == "" {
		v.TargetSubject, err = d.git("log", "-1", "--format=%s", c)
	}
	return v, err
}

// layerStep records a per-layer step that failed or is owed.
func (l *checkLook) layerStep(r LayerStep) {
	if !r.Owed() {
		return
	}
	result := LookOwed
	if r.Result == LayerFailed {
		result = LookFail
	}
	s := LookStep{Run: r.Step.Run}
	l.result(&s, result, r.Why)
	v := &LookVisit{Kind: LookLayerStep, Layer: r.Branch, Steps: []LookStep{s}}
	l.look.Visits = append(l.look.Visits, v)
	if s.Result == LookFail {
		v.Steps[0].Log = l.log("", r.Step.Run, []byte(strings.TrimSpace(r.Output+"\n"+r.Why)+"\n"))
	}
}

// recommitted notes that each of now is the commit of the same place in was.
func (l *checkLook) recommitted(d *driver, now, was []string) error {
	origins := make([]string, len(was))
	for i, c := range was {
		var err error
		if origins[i], err = l.original(d, c); err != nil {
			return err
		}
	}
	for i, c := range now {
		l.origins[c] = origins[i]
	}
	return nil
}

// original is the commit of the line the replayed commit c was; "" for one a
// per-layer step made.
func (l *checkLook) original(d *driver, c string) (string, error) {
	if o, ok := l.origins[c]; ok {
		return o, nil
	}
	dir, err := rebaseDir(d.req.Path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "rewritten-list"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	line := d.req.Stack.Line.Commits
	for _, entry := range strings.Split(string(b), "\n") {
		if f := strings.Fields(entry); len(f) >= 2 && f[1] == c && !l.folded[f[0]] && slices.Contains(line, f[0]) {
			return f[0], nil
		}
	}
	if slices.Contains(line, c) {
		return c, nil
	}
	return "", nil
}

// snapped is one entry of the checkout git does not track, as it was before a
// step.
type snapped struct {
	path string
	mode fs.FileMode
	link string
	mod  time.Time
	// sameAs is the path of an earlier entry this one is a hard link to.
	sameAs string
}

// snapshot is what git does not track in the checkout as it was before a
// step, kept under dir; err is why it could not be taken.
type snapshot struct {
	dir     string
	entries []snapped
	err     error
}

// before snapshots the checkout ahead of a step, so that it can be put back
// should the step fail; nil once the checkout says nothing anyway. A snapshot
// that cannot be taken matters only on a failure.
func (l *checkLook) before(d *driver) *snapshot {
	if l.dirty != "" {
		return nil
	}
	l.snapshots++
	s := &snapshot{dir: filepath.Join(l.root, fmt.Sprintf("snapshot-%d", l.snapshots))}
	s.err = l.take(s, d.req.Path)
	// An interrupt that removed the scratch under it stops here.
	_ = whileTempDir(l.made, func() error { return nil })
	return s
}

// drop forgets a snapshot nothing is put back from.
func (l *checkLook) drop(s *snapshot) {
	if s != nil {
		_ = removeTree(s.dir)
	}
}

func (l *checkLook) take(s *snapshot, checkout string) error {
	out, err := gitEnv(checkout, nil, nil, "ls-files", "-z")
	if err != nil {
		return err
	}
	tracked := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		tracked[filepath.FromSlash(p)] = true
	}
	// The files with more than one name, and how many of the names are
	// untracked ones of the checkout.
	type linked struct {
		first       string
		seen, names uint64
	}
	links := map[string]*linked{}
	if err := filepath.WalkDir(checkout, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(checkout, path)
		if err != nil || rel == "." {
			return err
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		if tracked[rel] {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		entry := snapped{path: rel, mode: info.Mode(), mod: info.ModTime()}
		switch {
		case entry.mode.IsDir():
		case entry.mode&fs.ModeSymlink != 0:
			if entry.link, err = os.Readlink(path); err != nil {
				return err
			}
		case entry.mode.IsRegular():
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
				id := fmt.Sprint(st.Dev, ":", st.Ino)
				if group := links[id]; group != nil {
					group.seen++
					entry.sameAs = group.first
					s.entries = append(s.entries, entry)
					return nil
				}
				links[id] = &linked{first: rel, seen: 1, names: uint64(st.Nlink)}
			}
			if err := whileTempDir(l.made, func() error { return copyFile(path, filepath.Join(s.dir, rel), 0o600) }); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s is neither a file, a directory nor a link", rel)
		}
		s.entries = append(s.entries, entry)
		return nil
	}); err != nil {
		return err
	}
	for _, group := range links {
		if group.seen != group.names {
			return fmt.Errorf("%s has a hard link the snapshot cannot keep, to a tracked file or one outside the checkout", group.first)
		}
	}
	return nil
}

func copyFile(from, to string, mode fs.FileMode) (err error) {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	_, err = io.Copy(out, in)
	return err
}

// restore puts the scratch checkout back as it was before the step run, which
// failed: HEAD at head and what git does not track as s has it. Where it
// cannot, every later result is indeterminate.
func (l *checkLook) restore(d *driver, s *snapshot, head, run string) {
	if s == nil {
		_, _ = d.git("reset", "-q", "--hard", head)
		return
	}
	defer l.drop(s)
	why := s.err
	if err := l.putBack(d, s, head); err != nil && why == nil {
		why = err
	}
	if why != nil {
		l.dirty = fmt.Sprintf("the checkout could not be put back after %q failed: %s", run, kitOneLine(why.Error()))
	}
}

func (l *checkLook) putBack(d *driver, s *snapshot, head string) error {
	if err := whileTempDir(l.made, func() error { makeWritable(d.req.Path); return nil }); err != nil {
		return err
	}
	for _, args := range [][]string{{"update-ref", "--no-deref", "HEAD", head}, {"reset", "-q", "--hard", head}} {
		if _, err := d.git(args...); err != nil {
			return err
		}
	}
	// What cannot be snapshotted is not removed either: it is still what a
	// later step would read.
	if s.err != nil {
		return nil
	}
	if _, err := d.git("clean", "-ffdxq"); err != nil {
		return err
	}
	for _, e := range s.entries {
		to := filepath.Join(d.req.Path, e.path)
		err := whileTempDir(l.made, func() error {
			switch {
			case e.mode.IsDir():
				return os.MkdirAll(to, 0o755)
			case e.mode&fs.ModeSymlink != 0:
				if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
					return err
				}
				return os.Symlink(e.link, to)
			}
			if e.sameAs != "" {
				return os.Link(filepath.Join(d.req.Path, e.sameAs), to)
			}
			if err := copyFile(filepath.Join(s.dir, e.path), to, 0o600); err != nil {
				return err
			}
			return errors.Join(os.Chmod(to, e.mode.Perm()), os.Chtimes(to, e.mod, e.mod))
		})
		if err != nil {
			return err
		}
	}
	// Directories last: a read-only one takes nothing more.
	for _, e := range slices.Backward(s.entries) {
		if e.mode.IsDir() {
			if err := os.Chmod(filepath.Join(d.req.Path, e.path), e.mode.Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}
