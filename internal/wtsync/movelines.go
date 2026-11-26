package wtsync

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// LineRange is a Move-Lines range: lines From to To, inclusive, of Path as
// the source commit leaves it.
type LineRange struct {
	Path string `json:"path"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

func (r LineRange) String() string { return fmt.Sprintf("%s:%d-%d", r.Path, r.From, r.To) }

// ParseLineRange reads "<path>:<from>-<to>", lines counted from 1.
func ParseLineRange(s string) (LineRange, error) {
	bad := fmt.Errorf("%q is not <path>:<from>-<to>, lines counted from 1", s)
	colon := strings.LastIndex(s, ":")
	if colon < 1 {
		return LineRange{}, bad
	}
	a, b, ok := strings.Cut(s[colon+1:], "-")
	from, errFrom := strconv.Atoi(a)
	to, errTo := strconv.Atoi(b)
	if !ok || errFrom != nil || errTo != nil || from < 1 || to < from {
		return LineRange{}, bad
	}
	return LineRange{Path: s[:colon], From: from, To: to}, nil
}

// diffHunk is one hunk of a file's change.
type diffHunk struct {
	oldStart, oldLen, newStart, newLen int
	// header is the hunk's "@@ -a,b +c,d @@", as git printed it.
	header string
	// body is the hunk's lines after its header, each with its newline.
	body []string
	// first and last are the lines of the new side its change takes up: the
	// added lines, and for lines removed with none added in their place the
	// line after them.
	first, last int
	// atEnd is a removal with no line after it to name it by.
	atEnd bool
}

// fileDiff is git's patch of one path.
type fileDiff struct {
	// head is the lines before the first hunk, each with its newline.
	head                    []string
	hunks                   []diffHunk
	binary, deleted, retype bool
	// newMode is the mode a change of mode gives the path, "" for none.
	newMode string
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// diffOfPath is the patch of path between the trees or commits from and to,
// with the context and hunk boundaries git's defaults give, whatever the
// user's settings say.
func diffOfPath(dir, from, to, path string) (fileDiff, error) {
	out, err := runGit(dir, nil, nil, "-c", "diff.suppressBlankEmpty=false", "--literal-pathspecs", "diff-tree", "-p", "--binary", "--full-index", "--no-renames", "--no-color",
		"--no-ext-diff", "--no-textconv", "-U3", "--inter-hunk-context=0", "--diff-algorithm=myers", "--indent-heuristic", "--src-prefix=a/", "--dst-prefix=b/",
		from, to, "--", path)
	if err != nil {
		return fileDiff{}, err
	}
	return parseFileDiff(string(out))
}

func parseFileDiff(out string) (fileDiff, error) {
	var d fileDiff
	lines := strings.SplitAfter(out, "\n")
	if last := len(lines) - 1; lines[last] == "" {
		lines = lines[:last]
	}
	diffs := 0
	for i := 0; i < len(lines); {
		l := lines[i]
		m := hunkHeader.FindStringSubmatch(l)
		if m == nil {
			switch {
			case strings.HasPrefix(l, "diff --git "):
				diffs++
			case strings.HasPrefix(l, "GIT binary patch"):
				d.binary = true
			case strings.HasPrefix(l, "deleted file mode "):
				d.deleted = true
			case strings.HasPrefix(l, "new mode "):
				d.newMode = strings.TrimSpace(strings.TrimPrefix(l, "new mode "))
			}
			if len(d.hunks) == 0 {
				d.head = append(d.head, l)
			}
			i++
			continue
		}
		num := func(s string) int {
			if s == "" {
				return 1
			}
			n, _ := strconv.Atoi(s)
			return n
		}
		h := diffHunk{oldStart: num(m[1]), oldLen: num(m[2]), newStart: num(m[3]), newLen: num(m[4]), header: m[0]}
		i++
		oldLeft, newLeft := h.oldLen, h.newLen
		for i < len(lines) && (oldLeft > 0 || newLeft > 0 || strings.HasPrefix(lines[i], `\`)) {
			switch lines[i][0] {
			case '-':
				oldLeft--
			case '+':
				newLeft--
			case ' ':
				oldLeft--
				newLeft--
			case '\\':
			default:
				return fileDiff{}, fmt.Errorf("a hunk git printed that does not read: %q", lines[i])
			}
			h.body = append(h.body, lines[i])
			i++
		}
		h.changedLines()
		d.hunks = append(d.hunks, h)
	}
	d.retype = diffs > 1
	return d, nil
}

func (h *diffHunk) changedLines() {
	at, removing := h.newStart, false
	take := func(n int) {
		if h.first == 0 {
			h.first = n
		}
		h.last = n
	}
	for _, l := range h.body {
		switch l[0] {
		case '-':
			removing = true
		case '+':
			take(at)
			at++
			removing = false
		case ' ':
			if removing {
				take(at)
				removing = false
			}
			at++
		}
	}
	if removing {
		take(at)
		h.atEnd = true
	}
}

// linesPick is the hunks of a source's change to one path that a move! commit
// takes by lines.
type linesPick struct {
	diff   fileDiff
	picked []bool
}

// and is p with the hunks other, a pick of the same change, takes too.
func (p linesPick) and(other linesPick) linesPick {
	picked := slices.Clone(p.picked)
	for i, taken := range other.picked {
		picked[i] = picked[i] || taken
	}
	return linesPick{diff: p.diff, picked: picked}
}

// pickHunks is the hunks of d that ranges, all of one path, take: every hunk a
// range overlaps, whose changed lines it must cover whole. why is set when a
// range cannot be taken.
func pickHunks(d fileDiff, ranges []LineRange) (linesPick, string) {
	path := ranges[0].Path
	whole := ": move the whole path instead (a path argument of wt sync move, or Move-Path)"
	switch {
	case d.retype:
		return linesPick{}, "the source changes the type of " + path + whole
	case d.binary:
		return linesPick{}, path + " is a binary file, which has no lines" + whole
	case d.deleted:
		return linesPick{}, "the source deletes " + path + ", which no line range can name" + whole
	case len(d.hunks) == 1 && d.hunks[0].newLen == 0:
		return linesPick{}, "the source empties " + path + ", which no line range can name" + whole
	}
	p := linesPick{diff: d, picked: make([]bool, len(d.hunks))}
	for _, r := range ranges {
		found := false
		for i, h := range d.hunks {
			if r.To < h.newStart || r.From > max(h.newStart+h.newLen-1, h.last) {
				continue
			}
			found = true
			if h.atEnd {
				return linesPick{}, fmt.Sprintf("%s takes the hunk %s:%s, which removes lines at the end of the file, and no line range can name those%s", r, path, h.header, whole)
			}
			if h.first < r.From || h.last > r.To {
				return linesPick{}, fmt.Sprintf("%s splits the hunk %s:%s: a range takes a hunk's changed lines whole, here lines %d-%d", r, path, h.header, h.first, h.last)
			}
			p.picked[i] = true
		}
		if !found {
			return linesPick{}, fmt.Sprintf("%s selects no hunk of the source's change to %s", r, path)
		}
	}
	return p, ""
}

// patch is the source's change to the path restricted to the picked hunks. A
// change of the path's mode stays with the source. git apply looks for a hunk
// from its new-side start, so each one's is where the hunk sits in the file the
// patch goes onto: ahead is how many more lines the source's parent has above
// a line of it than that file has.
func (p linesPick) patch(ahead func(line int) int) string {
	var b strings.Builder
	for _, l := range p.diff.head {
		if !strings.HasPrefix(l, "old mode ") && !strings.HasPrefix(l, "new mode ") {
			b.WriteString(l)
		}
	}
	left := 0
	for i, h := range p.diff.hunks {
		if !p.picked[i] {
			left += h.newLen - h.oldLen
			continue
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldLen, max(h.newStart-left-ahead(h.oldStart), 0), h.newLen)
		for _, l := range h.body {
			b.WriteString(l)
		}
	}
	return b.String()
}

// linesAhead is how many more lines d's new side has above its line than its
// old side has.
func (d fileDiff) linesAhead(line int) int {
	ahead := 0
	for _, h := range d.hunks {
		at := h.newStart
		if h.newLen == 0 {
			at++
		}
		for _, l := range h.body {
			switch l[0] {
			case '+':
				if at < line {
					ahead++
				}
				at++
			case '-':
				if at <= line {
					ahead--
				}
			case ' ':
				at++
			}
		}
	}
	return ahead
}

// withPicked is the tree of the source's parent with the picked hunks, and
// nothing else of the source's change.
func withPicked(dir, source string, picks map[string]linesPick) (string, error) {
	var patch strings.Builder
	for _, path := range slices.Sorted(maps.Keys(picks)) {
		patch.WriteString(picks[path].patch(func(int) int { return 0 }))
	}
	tree, ok, err := applyPart(dir, source+"^^{tree}", patch.String())
	if err == nil && !ok {
		err = fmt.Errorf("the hunks of %s do not apply to its own parent", source)
	}
	return tree, err
}

// carried is the file ours becomes when the change from base to theirs is
// merged onto it, as a blob id, "" for no file; ok is false when it does not
// merge clean. Modes are not merged.
func carried(dir string, ours, base, theirs treeEntry) (string, bool) {
	switch {
	case ours.oid == base.oid:
		return theirs.oid, true
	case base.oid == theirs.oid || ours.oid == theirs.oid:
		return ours.oid, true
	}
	const mode = "100644"
	merged, err := mergeEntries(dir, treeEntry{mode, ours.oid}, treeEntry{mode, base.oid}, treeEntry{mode, theirs.oid})
	return merged.oid, err == nil
}

// entriesAt is path's entry in each of the trees or commits revs.
func entriesAt(dir, path string, revs ...string) ([]treeEntry, error) {
	entries := make([]treeEntry, len(revs))
	for i, rev := range revs {
		var err error
		if entries[i], err = entryAt(dir, rev, path); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// landsAsSourced is whether the tree part, the tree onto with a move's part
// applied, has each path moved by lines as onto becomes when exactly the
// picked hunks of the source's change are merged onto it, wherever the lines
// around them have shifted to.
func landsAsSourced(dir, source string, picks map[string]linesPick, onto, part string) (bool, error) {
	if len(picks) == 0 {
		return true, nil
	}
	picked, err := withPicked(dir, source, picks)
	if err != nil {
		return false, err
	}
	for path := range picks {
		e, err := entriesAt(dir, path, onto, source+"^", picked, part)
		if err != nil {
			return false, err
		}
		if want, ok := carried(dir, e[0], e[1], e[2]); !ok || e[3].oid != want {
			return false, nil
		}
	}
	return true, nil
}

// keepsTheRest checks the source's replay, from the tree before to the tree
// after, of the paths picks moves hunks of: before has those hunks where the
// source has them, and the replay adds what lies between them and the source's
// own file, its change of mode with it, and nothing else. It returns the first
// path it does not hold for, "" when it holds for all.
func keepsTheRest(dir, source, before, after string, picks map[string]linesPick) (string, error) {
	picked, err := withPicked(dir, source, picks)
	if err != nil {
		return "", err
	}
	for _, path := range slices.Sorted(maps.Keys(picks)) {
		e, err := entriesAt(dir, path, before, source+"^", picked, source, after)
		if err != nil {
			return "", err
		}
		below, parent, taken, own, got := e[0], e[1], e[2], e[3], e[4]
		mode := below.mode
		if newMode := picks[path].diff.newMode; newMode != "" {
			mode = newMode
		}
		there, ok := carried(dir, below, parent, taken)
		want, clean := carried(dir, below, taken, own)
		if !ok || there != below.oid || !clean || got.oid != want || got.mode != mode {
			return path, nil
		}
	}
	return "", nil
}
