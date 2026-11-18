package wtsync

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/anlindstrom/wt/internal/git"
)

// LineLayer is one layer of a stack held on one line: a branch whose tip is
// a commit of the line.
type LineLayer struct {
	Branch  string
	Tip     string
	Count   int // commits from the line's base to the tip
	Subject string
	Named   bool // named with --layer rather than found
}

// SkippedBranch is a branch on the line that is not taken as a layer.
type SkippedBranch struct{ Branch, Tip, Why string }

// Line is a stack held on one first-parent line: the commits from where
// branch leaves trunk (Base) to its tip (Head), oldest first, and the layer
// branches whose tips are among them, bottom first.
type Line struct {
	Branch, Head, Base string
	Commits            []string
	Layers             []LineLayer
	Skipped            []SkippedBranch
}

// fold subjects: a commit git's autosquash folds into another.
var foldPrefixes = []string{"fixup! ", "squash! ", "amend! "}

func isFold(subject string) bool {
	for _, p := range foldPrefixes {
		if strings.HasPrefix(subject, p) {
			return true
		}
	}
	return false
}

// ReadLine reads the line of branch against trunk. Without named, the layers
// are the local branches other than branch whose tips lie on the line; a
// branch matching one of backups (wt refs sweep's backup patterns) is listed
// under Skipped instead. With named, the layers are exactly those branches,
// and one whose tip is not on the line is refused. A line with a merge
// commit is refused: a rebase replays it without its merge. A layer whose
// tip is a fixup!, squash! or amend! commit is taken like any other: it ends
// at the rewrite of its last commit that stands on its own (planLayers).
func ReadLine(dir, branch, trunk string, named, backups []string) (Line, error) {
	line := Line{Branch: branch}
	var err error
	if line.Head, err = gitEnv(dir, nil, nil, "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		return line, err
	}
	if line.Base, err = gitEnv(dir, nil, nil, "merge-base", trunk, line.Head); err != nil {
		return line, fmt.Errorf("%s has no commit in common with %s: %w", branch, trunk, err)
	}
	rng := line.Base + ".." + line.Head
	if out, err := gitEnv(dir, nil, nil, "log", "--merges", "--format=%h %s", rng); err != nil {
		return line, err
	} else if out != "" {
		first, _, _ := strings.Cut(out, "\n")
		return line, fmt.Errorf("the line of %s has a merge commit (%s): a stack is rebased as one line only when it has none", branch, first)
	}
	out, err := gitEnv(dir, nil, nil, "rev-list", "--reverse", rng)
	if err != nil {
		return line, err
	}
	pos := map[string]int{}
	for _, sha := range strings.Fields(out) {
		line.Commits = append(line.Commits, sha)
		pos[sha] = len(line.Commits)
	}
	refs, err := gitEnv(dir, nil, nil, "for-each-ref", "--format=%(refname:short) %(objectname) %(contents:subject)", "refs/heads/")
	if err != nil {
		return line, err
	}
	type found struct{ tip, subject string }
	branches := map[string]found{}
	for _, r := range strings.Split(refs, "\n") {
		name, rest, _ := strings.Cut(r, " ")
		tip, subject, _ := strings.Cut(rest, " ")
		if name != "" && name != branch {
			branches[name] = found{tip, subject}
		}
	}
	add := func(name string, f found, isNamed bool) {
		line.Layers = append(line.Layers, LineLayer{Branch: name, Tip: f.tip, Count: pos[f.tip], Subject: f.subject, Named: isNamed})
	}
	if len(named) > 0 {
		for _, name := range named {
			f, ok := branches[name]
			if !ok {
				return line, fmt.Errorf("--layer %s: no such local branch", name)
			}
			if pos[f.tip] == 0 {
				return line, fmt.Errorf("--layer %s: its tip %s is not on the line of %s", name, git.ShortID(f.tip, 7), branch)
			}
			add(name, f, true)
		}
	} else {
		names := make([]string, 0, len(branches))
		for name := range branches {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			f := branches[name]
			if pos[f.tip] == 0 {
				continue
			}
			if backup(name, backups) {
				line.Skipped = append(line.Skipped, SkippedBranch{Branch: name, Tip: f.tip, Why: "a backup name; name it with --layer to move it"})
				continue
			}
			add(name, f, false)
		}
	}
	slices.SortStableFunc(line.Layers, func(a, b LineLayer) int {
		if a.Count != b.Count {
			return a.Count - b.Count
		}
		return strings.Compare(a.Branch, b.Branch)
	})
	return line, nil
}

// backup is whether name matches one of the patterns.
func backup(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// StackRequest is a rebase of a stack held on the line: every layer of Line
// gets a marker in the sequencer's list. Autosquash folds the line's
// fixup!, squash! and amend! commits onto its own base instead of rebasing
// it onto trunk.
type StackRequest struct {
	Line       Line
	Autosquash bool
}

// StackMapPrefix is where a rebase of a stack held on one line has git
// write each layer's new tip: refs/wt-stack-map/<epoch>/<n>, n from 1 at the
// bottom. Not under refs/wt-sync/, where any ref ending in a number is read
// as a safety record.
const StackMapPrefix = "refs/wt-stack-map/"

// StackMapRef names the marker of layer n (from 1) in the run of epoch.
func StackMapRef(epoch int64, n int) string {
	return StackMapPrefix + strconv.FormatInt(epoch, 10) + "/" + strconv.Itoa(n)
}

// The ways a layer's marker can land, as LayerPlan.Mapped says it.
const (
	MappedTip        = "tip"        // after the layer's own tip
	MappedTipDropped = "tipDropped" // its tip was left out; after its last commit left
	MappedEmptied    = "emptied"    // none of its commits is left; where the layer below ends
)

// LayerPlan is what a rebase of the line planned for one layer, read from
// the list git started it with: the marker, the commit the marker follows
// (empty when it is the rebase's base itself), the count of commits from
// the new base the layer must have, and the subject its new tip must carry.
type LayerPlan struct {
	Branch, Old, Ref string
	After            string
	Expect           int
	Subject          string
	Mapped           string
	// Folded is how many fixup!, squash! and amend! commits of the
	// layer's own prefix were folded into an earlier commit.
	Folded int
	// LeftOut is how many commits of the layer's own commits are not in the
	// list: trunk already carries them, patch for patch.
	LeftOut int
	// Picks is the layer's own picks in the list, in order: what
	// ReconcileDropped matches the rebased line against.
	Picks []string `json:",omitempty"`
	// Amended is, for each of Picks that an amend! commit is folded into,
	// the subject that pick has once folded.
	Amended map[string]string `json:",omitempty"`
	// Emptied is the layer's own picks the rebase dropped as empty (ApplyDrops).
	Emptied []string `json:",omitempty"`
	// Stepped is how many of the layer's picks a per-layer step's fold left
	// empty, which the plan's count already lost.
	Stepped int `json:",omitempty"`
	// StepEnd says a per-layer step set where the layer ends and the subject
	// of its tip (it committed there, or a fold below left the tip empty):
	// a drop of the layer's own picks then leaves both as they are.
	StepEnd bool `json:",omitempty"`
	// TipFolded says the layer's own tip is a fixup!, squash! or amend!
	// commit that autosquash folds into an earlier commit.
	TipFolded bool `json:",omitempty"`
}

// LayerTip is one layer's new tip, read back from its marker and checked.
type LayerTip struct {
	LayerPlan
	New string
}

// todoEntry is one command of a sequencer list that names a commit.
type todoEntry struct {
	start bool // pick or edit: starts a group
	amend bool // fixup -C or -c: the group takes this commit's message
	sha   string
}

// readStackTodo reads the commands of a list that name a commit of the line,
// resolving each abbreviated id among the line's commits.
func readStackTodo(todo string, commits []string) []todoEntry {
	var out []todoEntry
	for _, line := range strings.Split(todo, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		e := todoEntry{}
		id := f[1]
		switch f[0] {
		case "pick", "edit":
			e.start = true
		case "fixup", "squash":
			if f[1] == "-C" || f[1] == "-c" {
				if len(f) < 3 {
					continue
				}
				e.amend, id = true, f[2]
			}
		default:
			continue
		}
		for _, c := range commits {
			if strings.HasPrefix(c, id) {
				e.sha = c
				break
			}
		}
		if e.sha != "" {
			out = append(out, e)
		}
	}
	return out
}

// planLayers reads, from the list a rebase of the line started with, what
// each layer's marker will follow and what its new tip must be.
func planLayers(dir string, epoch int64, line Line, todo string) ([]LayerPlan, error) {
	pos := map[string]int{}
	for i, c := range line.Commits {
		pos[c] = i + 1
	}
	entries := readStackTodo(todo, line.Commits)
	inTodo := map[string]bool{}
	for _, e := range entries {
		inTodo[e.sha] = true
	}
	plans := make([]LayerPlan, len(line.Layers))
	prev := 0
	for i, l := range line.Layers {
		p := LayerPlan{Branch: l.Branch, Old: l.Tip, Ref: StackMapRef(epoch, i+1), Mapped: MappedTip}
		last, lastAmend, own := -1, "", 0
		for j, e := range entries {
			if !e.start {
				continue
			}
			if pos[e.sha] <= l.Count {
				p.Expect++
				last, lastAmend = j, ""
				if pos[e.sha] > prev {
					own++
					p.Picks = append(p.Picks, e.sha)
					if amend := amendIn(entries[j+1:]); amend != "" {
						subject, err := plannedSubject(dir, e.sha, amend)
						if err != nil {
							return nil, err
						}
						if p.Amended == nil {
							p.Amended = map[string]string{}
						}
						p.Amended[e.sha] = subject
					}
				}
			}
		}
		if last >= 0 {
			p.After = entries[last].sha
			lastAmend = amendIn(entries[last+1:])
		}
		for _, c := range line.Commits[prev:l.Count] {
			if !inTodo[c] {
				p.LeftOut++
			}
		}
		for _, e := range entries {
			if !e.start && pos[e.sha] <= l.Count {
				p.Folded++
			}
		}
		switch {
		case own == 0 && l.Count > prev:
			p.Mapped = MappedEmptied
		case own == 0:
			// At the same commit as the layer below: it ends where that one does.
		case !isStart(entries, l.Tip):
			p.Mapped = MappedTipDropped
			p.TipFolded = inTodo[l.Tip]
		}
		if p.After != "" {
			subject, err := plannedSubject(dir, p.After, lastAmend)
			if err != nil {
				return nil, err
			}
			p.Subject = subject
		}
		plans[i] = p
		prev = l.Count
	}
	return plans, nil
}

// amendIn is the last amend! commit folded into the group that starts the
// list rest follows, "" when none is.
func amendIn(rest []todoEntry) string {
	amend := ""
	for _, e := range rest {
		if e.start {
			break
		}
		if e.amend {
			amend = e.sha
		}
	}
	return amend
}

func isStart(entries []todoEntry, sha string) bool {
	for _, e := range entries {
		if e.start && e.sha == sha {
			return true
		}
	}
	return false
}

// plannedSubject is the subject the commit a marker follows will carry: its
// own, or, when an amend! commit is folded into it, the subject of the
// message the amend! carries.
func plannedSubject(dir, sha, amend string) (string, error) {
	if amend == "" {
		return gitEnv(dir, nil, nil, "log", "-1", "--format=%s", sha)
	}
	body, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%b", amend)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(body, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l, nil
		}
	}
	return "", nil
}

// CheckLayers reads each layer's marker once the rebase of the line has
// finished and checks it against the plan before any branch moves: the
// marker is there, it is on the rebased line (an ancestor of head), it has
// as many commits from onto as the plan expects, and its subject is the
// planned one. Any failure is an error naming the layer, and nothing is
// returned to move.
func CheckLayers(dir, head, onto string, plans []LayerPlan) ([]LayerTip, error) {
	tips := make([]LayerTip, 0, len(plans))
	for _, p := range plans {
		bad := func(why string) ([]LayerTip, error) {
			return nil, fmt.Errorf("layer %s: %s; no layer moved, the line itself is rebased", p.Branch, why)
		}
		tip, ok, err := refTip(dir, p.Ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			return bad("the rebase did not write its marker " + p.Ref)
		}
		if _, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", tip, head); err != nil {
			return nil, err
		} else if code == 1 {
			return bad(fmt.Sprintf("its marker %s is not on the rebased line", git.ShortID(tip, 7)))
		}
		if _, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", onto, tip); err != nil {
			return nil, err
		} else if code == 1 {
			return bad(fmt.Sprintf("its new tip %s does not descend from %s, where the line was put", git.ShortID(tip, 7), onto))
		}
		out, err := gitEnv(dir, nil, nil, "rev-list", "--count", onto+".."+tip)
		if err != nil {
			return nil, err
		}
		if n, _ := strconv.Atoi(out); n != p.Expect {
			return bad(fmt.Sprintf("its new tip %s has %d commits from the new base, and the plan expects %d", git.ShortID(tip, 7), n, p.Expect))
		}
		if p.After != "" {
			subject, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%s", tip)
			if err != nil {
				return nil, err
			}
			if subject != p.Subject {
				return bad(fmt.Sprintf("its new tip %s is %q, and the plan expects %q", git.ShortID(tip, 7), subject, p.Subject))
			}
		}
		tips = append(tips, LayerTip{LayerPlan: p, New: tip})
	}
	return tips, nil
}

// DropStackMap deletes the markers of plans that are there, each only while
// it is still at the commit it was read at, so a ref written by something
// else is left alone.
func DropStackMap(dir string, plans []LayerPlan) error {
	var errs []error
	for _, p := range plans {
		tip, ok, err := refTip(dir, p.Ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		if _, err := gitEnv(dir, nil, nil, "update-ref", "-d", p.Ref, tip); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// layerOf numbers every commit of the line by the layer it belongs to, 1
// the bottom; a commit above every layer gets len(Layers)+1.
func (l Line) layerOf() map[string]int {
	out := map[string]int{}
	for i, c := range l.Commits {
		n := len(l.Layers) + 1
		for j, layer := range l.Layers {
			if layer.Count >= i+1 {
				n = j + 1
				break
			}
		}
		out[c] = n
	}
	return out
}

// SamePatch is whether the change from oldBase to old is the same, by git's
// stable patch-id, as the change from newBase to tip: what a layer brings
// to the line, before and after the rebase.
func SamePatch(dir, oldBase, old, newBase, tip string) (bool, error) {
	id := func(a, b string) (string, error) {
		diff, err := gitEnv(dir, nil, nil, "diff", "--no-color", "--no-ext-diff", a, b)
		if err != nil || diff == "" {
			return "", err
		}
		out, err := gitEnv(dir, nil, strings.NewReader(diff+"\n"), "patch-id", "--stable")
		id, _, _ := strings.Cut(out, " ")
		return id, err
	}
	was, err := id(oldBase, old)
	if err != nil {
		return false, err
	}
	is, err := id(newBase, tip)
	return was == is, err
}

// ReconcileEdit brings plans in line with an in-place rebase with an edit:
// the commits the amend emptied are the ones dropped, read from the rebased
// line by author, author date and subject (only an edit run, whose
// commits wt itself changes, may be read so). The edited commit is
// known on the rebased line as what it was amended to, and a layer whose
// marker follows it takes that commit's subject.
func ReconcileEdit(dir, onto string, plans []LayerPlan, edit EditStop) ([]LayerPlan, error) {
	if edit.Amended == "" {
		return plans, nil
	}
	got, err := reconcileDropped(dir, onto, plans, &edit)
	if err != nil {
		return nil, err
	}
	subject, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%s", edit.Amended)
	if err != nil {
		return nil, err
	}
	for i := range got {
		if got[i].After == edit.Commit {
			got[i].Subject = subject
		}
	}
	return got, nil
}

// Emptied is the commits of the old line above the edited commit, up to
// oldTip, that the rebased line from base to newTip no longer has: the ones
// the amend emptied, which the rebase dropped. A commit is known on the
// rebased line by its author, author date and subject, as ReconcileDropped
// knows it.
func Emptied(dir, edited, oldTip, base, newTip string) ([]Commit, error) {
	keys := func(rng string) ([]string, []Commit, error) {
		out, err := gitEnv(dir, nil, nil, "log", "--reverse", "--format=%H %ae %at %s", rng, "--")
		if err != nil {
			return nil, nil, err
		}
		var ks []string
		var cs []Commit
		for _, l := range strings.Split(out, "\n") {
			f := strings.SplitN(l, " ", 4)
			if len(f) < 3 {
				continue
			}
			subject := ""
			if len(f) == 4 {
				subject = f[3]
			}
			ks = append(ks, strings.Join(f[1:], " "))
			cs = append(cs, Commit{SHA: f[0], Subject: subject})
		}
		return ks, cs, nil
	}
	oldKeys, old, err := keys(edited + ".." + oldTip)
	if err != nil {
		return nil, err
	}
	newKeys, _, err := keys(base + ".." + newTip)
	if err != nil {
		return nil, err
	}
	var out []Commit
	for i, k := range oldKeys {
		if !slices.Contains(newKeys, k) {
			out = append(out, old[i])
		}
	}
	return out, nil
}

// reconcileDropped is ReconcileEdit's reading of the rebased line: walking
// each layer's stretch, from the marker of the layer below (or onto) to its
// own, against the layer's planned picks in order. A pick with no commit was
// dropped; anything that will not line up leaves the plans as they were, for
// CheckLayers to refuse.
func reconcileDropped(dir, onto string, plans []LayerPlan, edit *EditStop) ([]LayerPlan, error) {
	var picks []string
	for _, p := range plans {
		picks = append(picks, p.Picks...)
	}
	if len(picks) == 0 {
		return plans, nil
	}
	keyOf := map[string]string{}
	// A pick is known on the rebased line by its author, author date and
	// subject, which a rebase keeps: author and second alone are shared by
	// commits made in quick succession.
	out, err := gitEnv(dir, nil, nil, append([]string{"log", "--no-walk=unsorted", "--format=%H %ae %at %s"}, picks...)...)
	if err != nil {
		return nil, err
	}
	owner := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		if sha, key, ok := strings.Cut(l, " "); ok {
			keyOf[sha] = key
		}
	}
	if edit != nil {
		// The edited pick is on the rebased line as what it was amended to.
		key, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%ae %at %s", edit.Amended)
		if err != nil {
			return nil, err
		}
		if _, ok := keyOf[edit.Commit]; ok {
			keyOf[edit.Commit] = key
		}
	}
	for i, p := range plans {
		for _, c := range p.Picks {
			owner[keyOf[c]] = i + 1
		}
	}
	got := slices.Clone(plans)
	from, droppedBelow, addedBelow := onto, 0, 0
	for i, p := range plans {
		tip, ok, err := refTip(dir, p.Ref)
		if err != nil || !ok {
			return plans, err
		}
		stretch, err := gitEnv(dir, nil, nil, "log", "--reverse", "--format=%ae %at %s", from+".."+tip, "--")
		if err != nil {
			return plans, nil //nolint:nilerr // a stretch git cannot read is CheckLayers' to refuse
		}
		j, kept, added, lastKept := 0, 0, 0, -1
		for _, key := range strings.Split(stretch, "\n") {
			if key == "" {
				continue
			}
			k := j
			for k < len(p.Picks) && keyOf[p.Picks[k]] != key {
				k++
			}
			switch {
			case k < len(p.Picks):
				kept, lastKept, j = kept+1, k, k+1
			case owner[key] != 0:
				return plans, nil
			default:
				added++
			}
		}
		dropped := len(p.Picks) - kept
		droppedBelow += dropped
		if edit != nil {
			// Commits added at the edit stop belong to the edited layer: it
			// and every layer above count them.
			addedBelow += added
		}
		if droppedBelow > 0 || addedBelow > 0 {
			got[i].Expect = p.Expect - droppedBelow
			if n, err := gitEnv(dir, nil, nil, "rev-list", "--count", onto+".."+tip); err == nil {
				got[i].Expect, _ = strconv.Atoi(n)
			}
		}
		if dropped > 0 {
			got[i].LeftOut += dropped
			tipDropped := len(p.Picks) > 0 && lastKept != len(p.Picks)-1 && p.After == p.Picks[len(p.Picks)-1]
			switch {
			case kept == 0 && added == 0:
				got[i].Mapped = MappedEmptied
			case tipDropped && added == 0 && p.Mapped == MappedTip:
				got[i].Mapped = MappedTipDropped
			}
			if tipDropped && added == 0 {
				subject, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%s", tip)
				if err != nil {
					return nil, err
				}
				got[i].Subject = subject
				if kept == 0 && i > 0 {
					got[i].After = got[i-1].After
				}
			}
		}
		from = tip
	}
	return got, nil
}

// FixupOfCarried is why a fold cannot take the line onto onto, "" when it
// can. Every fixup!, squash! or amend! commit that will be replayed must name
// exactly one commit that will be replayed too, found as git's autosquash
// finds it: after the prefix and its whitespace, a commit named by id, tag or
// any revision that is on the line, else an earlier commit of the line with
// that subject, else one whose subject begins with it. A fixup whose target
// onto already carries (patch for patch, or by reachability), that is outside
// the replayed range, that is ambiguous, or that cannot be found, is refused:
// git would leave it alone or fold it into another commit, which may be in
// another layer. upstream, when not empty, starts the replayed range there.
func FixupOfCarried(dir, onto, upstream, branch string, commits []string) (string, error) {
	if len(commits) == 0 {
		return "", nil
	}
	args := []string{"cherry", onto, branch}
	if upstream != "" {
		args = append(args, upstream)
	}
	out, err := gitEnv(dir, nil, nil, args...)
	if err != nil {
		return "", err
	}
	carried := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if sha, ok := strings.CutPrefix(l, "- "); ok {
			carried[strings.TrimSpace(sha)] = true
		}
	}
	subjects, err := gitEnv(dir, nil, nil, append([]string{"log", "--no-walk=unsorted", "--format=%H %s"}, commits...)...)
	if err != nil {
		return "", err
	}
	type entry struct{ sha, subject string }
	var all []entry
	for _, l := range strings.Split(subjects, "\n") {
		if sha, subject, ok := strings.Cut(l, " "); ok {
			all = append(all, entry{sha, subject})
		}
	}
	ancestor := func(a, of string) (bool, error) {
		_, code, err := gitEnvAllow(dir, nil, nil, 1, "merge-base", "--is-ancestor", a, of)
		return code == 0, err
	}
	// carriedOrOutside is why a commit of the line is not replayed, "" when it is.
	status := map[string]string{}
	notReplayed := func(sha string) (string, error) {
		if why, ok := status[sha]; ok {
			return why, nil
		}
		why := ""
		switch {
		case carried[sha]:
			why = onto + " already carries"
		default:
			if reach, err := ancestor(sha, onto); err != nil {
				return "", err
			} else if reach {
				why = onto + " already carries"
			} else if upstream != "" {
				if below, err := ancestor(sha, upstream); err != nil {
					return "", err
				} else if below {
					why = "lies below the replayed range"
				}
			}
		}
		status[sha] = why
		return why, nil
	}
	for i, e := range all {
		target, folding := e.subject, false
		for {
			rest, ok := cutFoldPrefix(target)
			if !ok {
				break
			}
			target, folding = strings.TrimSpace(rest), true
		}
		if !folding {
			continue
		}
		if why, err := notReplayed(e.sha); err != nil || why != "" {
			continue // the fixup itself is not replayed: nothing of it lands
		}
		refuse := func(format string, a ...any) (string, error) {
			return fmt.Sprintf("the fixup %q (%s) %s; drop it or fold it by hand, then run again; nothing is rebased",
				e.subject, git.ShortID(e.sha, 7), fmt.Sprintf(format, a...)), nil
		}
		var named *entry
		if sha, err := gitEnv(dir, nil, nil, "rev-parse", "--verify", "--quiet", target+"^{commit}"); err == nil {
			for j := range all[:i] {
				if all[j].sha == strings.TrimSpace(sha) {
					named = &all[j]
				}
			}
		}
		if named == nil {
			var exact, prefix []*entry
			for j := range all[:i] {
				switch c := &all[j]; {
				case c.subject == target:
					exact = append(exact, c)
				case strings.HasPrefix(c.subject, target):
					prefix = append(prefix, c)
				}
			}
			candidates := exact
			if len(candidates) == 0 {
				candidates = prefix
			}
			switch len(candidates) {
			case 0:
				return refuse("names no commit earlier on the line (%q)", target)
			case 1:
				named = candidates[0]
			default:
				return refuse("names more than one commit on the line (%q)", target)
			}
		}
		why, err := notReplayed(named.sha)
		if err != nil {
			return "", err
		}
		if why != "" {
			return refuse("is for %q (%s), which %s: there is nothing to fold it into",
				named.subject, git.ShortID(named.sha, 7), why)
		}
	}
	return "", nil
}

// cutFoldPrefix is s without a leading fixup!, squash! or amend!.
func cutFoldPrefix(s string) (string, bool) {
	for _, p := range []string{"fixup! ", "squash! ", "amend! "} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			return rest, true
		}
	}
	return s, false
}

// MergedTree is the tree git merges the old line onto onto with, from
// their common base: what a fold that moves the line onto onto must keep, in
// every file that merges cleanly. conflicted is the files that do not, whose
// tree entries carry conflict markers.
func MergedTree(mainRoot, base, onto, oldTip string) (tree string, conflicted []string, err error) {
	tree, clean, conflicts, _, err := mergeTree(mainRoot, base, onto, oldTip)
	if err != nil || clean {
		return tree, nil, err
	}
	for _, c := range conflicts {
		conflicted = append(conflicted, c.Path)
	}
	return tree, conflicted, nil
}

// ApplyDrops brings plans in line with the picks a finished rebase dropped as
// empty, as ReportReplay names them: only a rebase wt drove from its start
// to its end (confirmed) is read, and only the drops it is certain of. A
// reworded or rewritten commit is no drop, and a commit that shares its
// author, date and subject with another is told apart from it by nothing, so
// both stay as the plan had them and CheckLayers refuses a layer that
// counts differently. A drop a per-layer step already took out of the plan
// (LayerPlan.Stepped) is taken out once.
//
// A layer that lost picks expects fewer commits, and so does every layer
// above it. One whose planned tip was dropped ends at its last pick left
// (and its subject is that one's); one that lost every pick ends where the
// layer below ends. A layer whose end a step set (StepEnd) keeps it, and
// only its count follows.
func ApplyDrops(dir, onto string, plans []LayerPlan, dropped []Dropped, confirmed bool) ([]LayerPlan, error) {
	if !confirmed || len(dropped) == 0 {
		return plans, nil
	}
	certain := map[string]bool{}
	for _, d := range dropped {
		if !d.Unmatched {
			certain[d.Commit] = true
		}
	}
	got := slices.Clone(plans)
	cumDropped, cumStepped := 0, 0
	for i, p := range plans {
		var gone []string
		last := -1
		for k, pick := range p.Picks {
			if certain[pick] {
				gone = append(gone, pick)
			} else {
				last = k
			}
		}
		got[i].Emptied = gone
		cumDropped += max(len(gone), p.Stepped)
		cumStepped += p.Stepped
		if cumDropped > 0 {
			// Each drop once: the ones a step took out are in the layer's
			// planned count already.
			got[i].Expect = p.Expect + cumStepped - cumDropped
		}
		if len(gone) == 0 {
			continue
		}
		got[i].LeftOut += len(gone)
		if p.StepEnd {
			// A step set where the layer ends: it was read from the tip
			// after the drop.
			continue
		}
		tipDropped := p.After == p.Picks[len(p.Picks)-1] && certain[p.After]
		switch {
		case last < 0:
			got[i].Mapped = MappedEmptied
		case tipDropped && p.Mapped == MappedTip:
			got[i].Mapped = MappedTipDropped
		}
		if !tipDropped {
			continue
		}
		switch {
		case last >= 0:
			subject, err := plannedSubject(dir, p.Picks[last], "")
			if err != nil {
				return nil, err
			}
			if s, ok := p.Amended[p.Picks[last]]; ok {
				subject = s
			}
			got[i].Subject = subject
		case i > 0:
			got[i].After, got[i].Subject = got[i-1].After, got[i-1].Subject
		default:
			subject, err := gitEnv(dir, nil, nil, "log", "-1", "--format=%s", onto)
			if err != nil {
				return nil, err
			}
			got[i].Subject = subject
		}
	}
	// A layer with no pick of its own ends where the layer below ends, which
	// may have moved up to its last commit left.
	for i := 1; i < len(plans); i++ {
		if len(plans[i].Picks) == 0 && !plans[i].StepEnd && plans[i].After == plans[i-1].After {
			got[i].After, got[i].Subject = got[i-1].After, got[i-1].Subject
		}
	}
	return got, nil
}

// UnclearDrops is the commits of plans' picks that a rebase dropped but
// ReportReplay could not tell from another commit (Dropped.Unmatched): a layer
// that counts them as left is refused, and this says why.
func UnclearDrops(plans []LayerPlan, dropped []Dropped) []Dropped {
	in := map[string]bool{}
	for _, p := range plans {
		for _, pick := range p.Picks {
			in[pick] = true
		}
	}
	var out []Dropped
	for _, d := range dropped {
		if d.Unmatched && in[d.Commit] {
			out = append(out, d)
		}
	}
	return out
}
