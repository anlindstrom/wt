package wtsync

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// NeedDocker is the need of a step that cannot run without a Docker daemon.
const NeedDocker = "docker"

// KnownNeeds is every need a step may declare, each with the probe that
// tells whether it is there: a fixed set, so a declaration cannot name a
// command for wt to run as a probe.
var KnownNeeds = map[string]func() error{
	NeedDocker: probeDocker,
}

// probeDocker runs `docker info` under dockerDeadline.
func probeDocker() error {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	timedOut, _, err := git.RunBounded(dockerDeadline, cmd)
	if timedOut {
		return git.NoAnswer(fmt.Sprintf("docker info did not answer within %s", dockerDeadline))
	}
	if err != nil {
		return fmt.Errorf("docker info: %w", err)
	}
	return nil
}

// NeedEnvPrefix starts a need on an environment variable: env:<NAME>.
const NeedEnvPrefix = "env:"

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envNeedName is the variable of an env:<NAME> need; ok is false for any
// other need.
func envNeedName(need string) (name string, ok bool) {
	return strings.CutPrefix(need, NeedEnvPrefix)
}

// stepEnviron is the environment every step and check process starts with:
// the caller's, with NO_COLOR and the rebase editors set over it. A probe
// reads this same list, so it and the step cannot disagree.
func stepEnviron() []string { return stepEnvironIn("") }

// stepEnvironIn is stepEnviron for a command run in dir.
func stepEnvironIn(dir string) []string {
	return git.EnvironIn(dir, append([]string{"NO_COLOR=1"}, rebaseEnv...)...)
}

// probeEnv answers whether name is set and not empty in stepEnviron. The
// error names the variable, never its value.
func probeEnv(name string) error { return probeEnvIn(stepEnviron(), name) }

// probeEnvIn is probeEnv over env. The first entry for name decides, as it
// does for sh and for git.Environ.
func probeEnvIn(env []string, name string) error {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			if v == "" {
				break
			}
			return nil
		}
	}
	return fmt.Errorf("%s is not set", name)
}

// knownNeedNames is KnownNeeds' names, sorted, for a message.
func knownNeedNames() string {
	names := make([]string, 0, len(KnownNeeds))
	for n := range KnownNeeds {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func checkNeeds(needs []string) error {
	for _, n := range needs {
		if name, ok := envNeedName(n); ok {
			if !envNameRE.MatchString(name) {
				return fmt.Errorf("need %q: %q is not a valid environment variable name", n, name)
			}
			continue
		}
		if _, ok := KnownNeeds[n]; !ok {
			return fmt.Errorf("unknown need %q; the known needs are %s, and env:<NAME>", n, knownNeedNames())
		}
	}
	return nil
}

// ProbeNeedsWith probes each of needs once with probes, and returns each
// probe's answer: nil for one that answered.
func ProbeNeedsWith(probes map[string]func() error, needs []string) map[string]error {
	got := map[string]error{}
	for _, n := range needs {
		if _, done := got[n]; done {
			continue
		}
		probe, ok := probes[n]
		if name, isEnv := envNeedName(n); isEnv && !ok {
			got[n] = probeEnv(name)
			continue
		}
		if !ok {
			got[n] = fmt.Errorf("no probe for %q", n)
			continue
		}
		got[n] = probe()
	}
	return got
}

// ProbeNeeds is ProbeNeedsWith over KnownNeeds.
func ProbeNeeds(needs []string) map[string]error {
	return ProbeNeedsWith(KnownNeeds, needs)
}

// hasNeed reports whether any of needs is in want.
func hasNeed(needs []string, want string) bool {
	for _, n := range needs {
		if n == want {
			return true
		}
	}
	return false
}

// DeclaresNeed reports whether any step of the declaration needs n.
func (c *Config) DeclaresNeed(n string) bool {
	if c == nil {
		return false
	}
	for _, d := range c.Defer {
		if hasNeed(d.Needs, n) {
			return true
		}
	}
	for _, e := range c.EachCommit {
		if hasNeed(e.Needs, n) {
			return true
		}
	}
	return false
}

// Due is what of a declaration's steps may run for one branch's rebase,
// known before it starts: the each_commit steps with a commit to check, the
// commits they check, and the deferred steps whose paths match a path the
// rebase may change. The deferred gate is a superset: what trunk changed
// since the branch's merge base, and what the branch's own commits change.
// Which deferred steps really run is only known after the rebase.
type Due struct {
	Checks       []EachCommit
	CheckCommits []Commit
	Deferred     []Deferred
	// AtMost says the run may check fewer commits than CheckCommits.
	AtMost bool
}

// Needs is every need of a due step, each once, in the order declared.
func (d Due) Needs() []string {
	var needs []string
	add := func(ns []string) {
		for _, n := range ns {
			if !hasNeed(needs, n) {
				needs = append(needs, n)
			}
		}
	}
	for _, s := range d.Checks {
		add(s.Needs)
	}
	for _, s := range d.Deferred {
		add(s.Needs)
	}
	return needs
}

// Needing is the run line of every due step that needs n.
func (d Due) Needing(n string) []string {
	var steps []string
	for _, s := range d.Checks {
		if hasNeed(s.Needs, n) {
			steps = append(steps, s.Run)
		}
	}
	for _, s := range d.Deferred {
		if hasNeed(s.Needs, n) {
			steps = append(steps, s.Run)
		}
	}
	return steps
}

// StepsDue is what may run when branch is rebased: base is what its
// commits are counted from (trunk, or a stack parent's tip), trunk the
// commit the run rebases onto, and upstream, when it is not empty, where the
// replayed range starts (rebase --onto trunk upstream).
func StepsDue(wtPath string, cfg *Config, base, branch, trunk, upstream string) (Due, error) {
	var due Due
	if cfg == nil || (len(cfg.EachCommit) == 0 && len(cfg.Defer) == 0) {
		return due, nil
	}
	var err error
	if due.CheckCommits, due.Checks, err = checksDue(wtPath, cfg, Target{Onto: base}, branch); err != nil {
		return due, err
	}
	if len(cfg.Defer) == 0 {
		return due, nil
	}
	paths, err := mayChange(wtPath, branch, Target{Onto: trunk, Upstream: upstream})
	if err != nil {
		return due, err
	}
	for _, s := range cfg.Defer {
		if len(s.Paths) == 0 || anyMatches(s.Paths, paths) {
			due.Deferred = append(due.Deferred, s)
		}
	}
	return due, nil
}

// mayChange is every path a rebase of branch onto t may change: what t's
// target changed since the cutoff (the merge base, or the upstream), and
// what the commits replayed change.
func mayChange(wtPath, branch string, t Target) ([]string, error) {
	mb, err := t.base(wtPath, branch)
	if err != nil {
		return nil, err
	}
	paths, err := ChangedPaths(wtPath, mb, t.Onto)
	if err != nil {
		return nil, err
	}
	commits, err := logWithPaths(wtPath, "--right-only", "--no-merges", t.cutoff()+"..."+branch, "--")
	if err != nil {
		return nil, err
	}
	for _, c := range commits {
		paths = append(paths, c.Paths...)
	}
	return paths, nil
}

// EachCommitOrNeeds is what of the declaration the overview has to read
// the branches for: its each_commit steps, and its steps with needs.
func (c *Config) EachCommitOrNeeds() []string {
	if c == nil {
		return nil
	}
	var steps []string
	for _, e := range c.EachCommit {
		steps = append(steps, e.Run)
	}
	for _, d := range c.Defer {
		if len(d.Needs) > 0 {
			steps = append(steps, d.Run)
		}
	}
	return steps
}

// DeclaredNeeds is every need a step of the declaration names, each once, sorted.
func (c *Config) DeclaredNeeds() []string {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(ns []string) {
		for _, n := range ns {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	for _, d := range c.Defer {
		add(d.Needs)
	}
	for _, e := range c.EachCommit {
		add(e.Needs)
	}
	sort.Strings(out)
	return out
}
