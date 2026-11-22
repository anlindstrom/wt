package retire

import (
	"fmt"
	"slices"
	"strings"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/git"
	"github.com/anlindstrom/wt/internal/repo"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// How a kept copy would push to a remote branch, as --json names it.
const (
	// ViaRecorded is where wt records or infers the branch pushes.
	ViaRecorded = "recorded"
	// ViaUpstream is the branch's upstream, which push.default sends it to.
	ViaUpstream = "upstream"
	// ViaSameName is the kept name on the remote a plain push goes to.
	ViaSameName = "sameName"
	// ViaRefspec is a remote.<name>.push refspec.
	ViaRefspec = "refspec"
)

// ProblemStaleForcedPush is a plain git push from the kept copy that would
// force its way over a remote branch that has moved on. The settings are the
// remote's, shared by every checkout, so wt does not change them: nothing is
// renamed or removed, and no --force goes past it.
const ProblemStaleForcedPush = "staleForcedPush"

// StaleRoute is one way the kept copy of a branch could push to a remote
// branch that holds commits it lacks, as last fetched.
type StaleRoute struct {
	// Ref is the remote branch as a person says it, "origin/feat".
	Ref string
	// Tip is the commit the comparison read there.
	Tip string
	// Via is one of the Via* values; Refspec the remote.<name>.push setting
	// for ViaRefspec, as "remote.origin.push=+HEAD:refs/heads/feat".
	Via, Refspec string
	// Forces is a refspec with +: a plain push goes over a remote that has
	// moved on without anyone forcing it.
	Forces bool
	// Dropped is a route the removal takes away, by removing the branch's
	// own setting. A route that is not dropped is left, and said.
	Dropped bool
}

// staleRoutes reads where the kept copy of branch, named keep, would push, as
// git applies its configuration, and which of those remote branches hold
// commits the branch lacks. dropKeys is the branch's own settings the removal
// then takes from the copy. Nothing is fetched or written; a destination with
// no remote-tracking ref is not compared.
func staleRoutes(ctx *kit.Context, branch, keep string) (routes []StaleRoute, dropKeys []string) {
	main := ctx.Repo.MainRoot
	remotes, _ := ctx.Repo.Remotes()
	own := wtsync.ReadOwnOrUnknown(main, ctx.Config.MainBranch)
	pushDefault := cmp(configValue(main, "push.default"), "simple")
	pushRemote := configValue(main, "branch."+branch+".pushRemote")
	upRemote := configValue(main, "branch."+branch+".remote")
	upBranch := strings.TrimPrefix(configValue(main, "branch."+branch+".merge"), "refs/heads/")
	defaultRemote := configValue(main, "remote.pushDefault")

	// stale compares a remote branch with the local one, as last fetched.
	stale := func(remote, name string) (ref, tip string, ok bool) {
		full := "refs/remotes/" + remote + "/" + name
		tip, there, err := ctx.Repo.ResolveRef(full)
		if err != nil || !there {
			return "", "", false
		}
		n, ok := ctx.Repo.CommitsAhead(full, "refs/heads/"+branch)
		return remote + "/" + name, tip, ok && n > 0
	}
	add := func(r StaleRoute) {
		if !slices.ContainsFunc(routes, func(o StaleRoute) bool { return o.Ref == r.Ref && o.Via == r.Via && o.Refspec == r.Refspec }) {
			routes = append(routes, r)
		}
	}

	// What goes with the copy: where wt records or infers the branch
	// pushes, which the rename carries along.
	if st := own.State(branch); st.Compared() && st.Behind > 0 {
		if remote, name := splitKnown(remotes, st.Ref, pushRemote, upRemote, defaultRemote, "origin"); remote != "" {
			if ref, tip, ok := stale(remote, name); ok {
				add(StaleRoute{Ref: ref, Tip: tip, Via: ViaRecorded, Dropped: true})
				// An upstream that is the very branch the copy was pushing to goes
				// with it; one that only names another branch to pull from stays.
				if ctx.Repo.Upstream(branch) == "refs/remotes/"+ref {
					dropKeys = append(dropKeys, "remote", "merge")
				}
			}
		}
	}
	// The upstream, only where a plain push is sent to it: push.default
	// upstream, or simple with the kept name the upstream's. A push refspec on
	// the remote a plain push goes to overrides push.default, so then the
	// upstream is no route of its own.
	plainRemote := cmp(pushRemote, defaultRemote, ifThen(upRemote != ".", upRemote), "origin")
	upstreamPushes := upRemote != "" && upRemote != "." && upBranch != "" &&
		len(configValues(main, "remote."+plainRemote+".push")) == 0 &&
		(pushDefault == "upstream" || pushDefault == "tracking" || (pushDefault == "simple" && upBranch == keep))
	if upstreamPushes {
		if ref, tip, ok := stale(upRemote, upBranch); ok {
			add(StaleRoute{Ref: ref, Tip: tip, Via: ViaUpstream, Dropped: true})
			if !slices.Contains(dropKeys, "remote") {
				dropKeys = append(dropKeys, "remote", "merge")
			}
		}
	}
	if len(routes) > 0 {
		dropKeys = append(dropKeys, repo.PushToVar, "pushRemote")
	}

	// What stays: the remote a plain push goes to, once those are gone.
	upstreamLeft := upRemote != "" && upBranch != "" && !slices.Contains(dropKeys, "remote")
	push := pushRemote
	if len(routes) > 0 {
		push = ""
	}
	push = cmp(push, defaultRemote, ifThen(upstreamLeft, upRemote), "origin")
	specs := configValues(main, "remote."+push+".push")
	for _, spec := range specs {
		if dest, forces, ok := refspecDest(spec, keep); ok {
			if ref, tip, stl := stale(push, dest); stl {
				add(StaleRoute{Ref: ref, Tip: tip, Via: ViaRefspec, Forces: forces,
					Refspec: "remote." + push + ".push=" + spec})
			}
		}
	}
	// With a refspec configured, push.default is not read at all.
	sameName := len(specs) == 0 && (pushDefault == "current" || pushDefault == "matching" || (pushDefault == "simple" && !upstreamLeft))
	if sameName {
		if ref, tip, ok := stale(push, keep); ok {
			add(StaleRoute{Ref: ref, Tip: tip, Via: ViaSameName})
		}
	}
	return routes, dropKeys
}

func cmp(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func ifThen(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}

// splitKnown reads "origin/feat/x" as a remote and the branch on it: one of
// prefer, in order, when the ref sits under it, so a remote named origin/team
// does not claim origin's team/x, and otherwise the longest remote that fits.
func splitKnown(remotes []string, ref string, prefer ...string) (remote, branch string) {
	for _, r := range prefer {
		if b, ok := strings.CutPrefix(ref, r+"/"); ok && b != "" && slices.Contains(remotes, r) {
			return r, b
		}
	}
	for _, r := range remotes {
		if b, ok := strings.CutPrefix(ref, r+"/"); ok && b != "" && len(r) > len(remote) {
			remote, branch = r, b
		}
	}
	return remote, branch
}

// configValues is every value git reads for a key, from every scope it reads;
// none when it has none or cannot be read.
func configValues(dir, key string) []string {
	out, err := git.Exec(git.Opts{Dir: dir}, "config", "--get-all", key)
	if _, err := git.Answer(err, 1); err != nil {
		return nil
	}
	var vals []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			vals = append(vals, l)
		}
	}
	return vals
}

func configValue(dir, key string) string {
	if vals := configValues(dir, key); len(vals) > 0 {
		return vals[len(vals)-1]
	}
	return ""
}

// refspecDest is the remote branch a push refspec sends the branch keep,
// checked out, to: the source may be HEAD, the branch or a pattern that
// matches it. forces is a leading +.
func refspecDest(spec, keep string) (dest string, forces, ok bool) {
	forces = strings.HasPrefix(spec, "+")
	spec = strings.TrimPrefix(spec, "+")
	src, dst, hasDst := strings.Cut(spec, ":")
	if src == "" && hasDst && dst == "" {
		return keep, forces, true // ":", matching: every branch to the same name there
	}
	if src == "" || (hasDst && dst == "") {
		return "", false, false // a delete, or nothing
	}
	full := func(name string) string {
		if strings.HasPrefix(name, "refs/") {
			return name
		}
		return "refs/heads/" + name
	}
	head := src == "HEAD" || src == "@"
	// A source is any revision; one read from HEAD, such as HEAD~0 or @^{},
	// pushes from the kept copy once it is checked out. git wants a
	// destination for it.
	if !head && headRevision(src) {
		if !hasDst {
			return "", false, false
		}
		head = true
	}
	if !hasDst {
		dst = src
		if head {
			dst = keep
		}
	}
	var to string
	switch {
	case head:
		to = full(dst)
	case strings.Contains(src, "*"):
		pre, post, _ := strings.Cut(full(src), "*")
		b := "refs/heads/" + keep
		if !strings.HasPrefix(b, pre) || !strings.HasSuffix(b, post) || len(b) < len(pre)+len(post) {
			return "", false, false
		}
		to = strings.Replace(full(dst), "*", b[len(pre):len(b)-len(post)], 1)
	case full(src) == "refs/heads/"+keep:
		to = full(dst)
	default:
		return "", false, false
	}
	dest, ok = strings.CutPrefix(to, "refs/heads/")
	return dest, forces, ok && dest != ""
}

// headRevision is a revision read from HEAD: HEAD or @ followed by ~, ^ or @.
func headRevision(src string) bool {
	for _, h := range []string{"HEAD", "@"} {
		if rest, ok := strings.CutPrefix(src, h); ok && rest != "" && strings.ContainsRune("~^@", rune(rest[0])) {
			return true
		}
	}
	return false
}

// refused is the routes that would force a push over a remote branch that has
// moved on, in words: nothing is changed, and the way out is named.
func staleRefusal(routes []StaleRoute, keep string) string {
	var out []string
	for _, r := range routes {
		if r.Forces && !r.Dropped {
			out = append(out, fmt.Sprintf("%s would force a plain git push from the kept copy %s over %s, which has moved on; "+
				"nothing was changed: delete the branch with git branch -D after checking it is an old copy, or remove that refspec",
				r.Refspec, keep, r.Ref))
		}
	}
	return strings.Join(out, "; ")
}

// staleLines say what the removal does about each route, to be printed one
// to a line: those it takes away, and those it leaves, which git refuses
// unless forced.
func staleLines(routes []StaleRoute, keep string) []string {
	var out []string
	left := map[string]bool{}
	for _, r := range routes {
		if !r.Dropped {
			left[r.Ref] = true
		}
	}
	for _, r := range routes {
		switch {
		case r.Dropped && !left[r.Ref]:
			out = append(out, staleRemoteLine(r.Ref, keep))
		case r.Via == ViaSameName:
			out = append(out, fmt.Sprintf("a plain git push from %s still goes to %s, which has moved on; git refuses it unless forced",
				keep, r.Ref))
		case r.Via == ViaRefspec && !r.Forces:
			key, _, _ := strings.Cut(r.Refspec, "=")
			out = append(out, fmt.Sprintf("%s still sends %s to %s, which has moved on; git refuses it unless forced",
				key, keep, r.Ref))
		}
	}
	return out
}

// staleRemoteLine says a kept copy will not push where its branch did, and
// the command that sets it again.
func staleRemoteLine(remote, kept string) string {
	return fmt.Sprintf("%s has moved on; the kept copy, %s, won't push there (wt sync push-to %s %s sets it again)",
		remote, kept, kept, remote)
}

// dropPush takes from a branch the settings that send a push to a remote
// branch that moved on. Settings it has not got are not an error.
func dropPush(ctx *kit.Context, branch string, keys []string) error {
	for _, key := range keys {
		_, err := git.Exec(git.Opts{Dir: ctx.Repo.MainRoot}, "config", "--local", "--unset-all", "branch."+branch+"."+key)
		if _, err := git.Answer(err, 5); err != nil {
			return err
		}
	}
	return nil
}
