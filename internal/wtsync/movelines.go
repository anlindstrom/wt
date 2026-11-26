package wtsync

import (
	"fmt"
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
	out, err := runGit(dir, nil, nil, "--literal-pathspecs", "diff-tree", "-p", "--binary", "--full-index", "--no-renames", "--no-color",
		"--no-ext-diff", "--no-textconv", "-U3", "--inter-hunk-context=0", "--diff-algorithm=myers", "--src-prefix=a/", "--dst-prefix=b/",
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
// change of the path's mode stays with the source. Each hunk's new-side start
// is moved back by what the hunks left out before it add or remove, since git
// apply looks for a hunk from there.
func (p linesPick) patch() string {
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
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldLen, h.newStart-left, h.newLen)
		for _, l := range h.body {
			b.WriteString(l)
		}
	}
	return b.String()
}

// changesOf is the lines the hunks keep reports remove and add, in order.
func changesOf(hunks []diffHunk, keep func(int) bool) (removed, added []string) {
	for i, h := range hunks {
		if !keep(i) {
			continue
		}
		for _, l := range h.body {
			switch l[0] {
			case '-':
				removed = append(removed, strings.TrimSuffix(l, "\n"))
			case '+':
				added = append(added, strings.TrimSuffix(l, "\n"))
			}
		}
	}
	return removed, added
}

// keepsTheRest is whether the source's replay, which took path from the tree
// before to the tree after, changed exactly what the moves left of it: the
// hunks none of picks takes, and the source's own change of mode.
func keepsTheRest(dir, before, after, path string, picks []linesPick) (bool, error) {
	got, err := diffOfPath(dir, before, after, path)
	if err != nil {
		return false, err
	}
	d := picks[0].diff
	wantRemoved, wantAdded := changesOf(d.hunks, func(i int) bool {
		return !slices.ContainsFunc(picks, func(p linesPick) bool { return p.picked[i] })
	})
	removed, added := changesOf(got.hunks, func(int) bool { return true })
	return !got.binary && !got.deleted && !got.retype && got.newMode == d.newMode &&
		slices.Equal(removed, wantRemoved) && slices.Equal(added, wantAdded), nil
}
