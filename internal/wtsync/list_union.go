package wtsync

import (
	"regexp"
	"slices"
	"strings"
)

// ListUnion resolves a file whose one contested line is a delimited list that
// both sides only ever add to: the union, trunk's order first, then the
// branch's additions. A removal is a decision, not a merge, and is refused.
type ListUnion struct {
	Line      *regexp.Regexp
	Delimiter string
}

// Name identifies this strategy in errors and reports.
func (ListUnion) Name() string { return "list-union" }

// itemRE is what one list item may look like: a quoted or bare token with no
// whitespace inside. Anything else means the line was not parsed the way it
// was written.
var itemRE = regexp.MustCompile(`^('[^'\s]+'|"[^"\s]+"|[^\s'",]+)$`)

// Resolve merges c and collapses each conflict block by way of collapse.
func (s ListUnion) Resolve(c Conflict) ([]byte, error) {
	// Removal is judged on whole files, so an item a side merely moved to
	// another list line still counts as present.
	baseLines := strings.Split(string(c.Base), "\n")
	if !s.hasLine(baseLines) {
		return nil, Refuse(c.Path, "no list line matching %s", s.Line)
	}
	baseItems, err := s.items(c.Path, baseLines)
	if err != nil {
		return nil, err
	}
	branchAll, err := s.items(c.Path, strings.Split(string(c.Branch), "\n"))
	if err != nil {
		return nil, err
	}
	trunkAll, err := s.items(c.Path, strings.Split(string(c.Trunk), "\n"))
	if err != nil {
		return nil, err
	}
	for _, it := range baseItems {
		if !slices.Contains(branchAll, it) {
			return nil, Refuse(c.Path, "the branch removes %s; that is a decision, not a merge", it)
		}
		if !slices.Contains(trunkAll, it) {
			return nil, Refuse(c.Path, "trunk removes %s; that is a decision, not a merge", it)
		}
	}
	segs, err := Merge3(c)
	if err != nil {
		return nil, err
	}
	// An item is written once in the file: start from every list item that
	// stays as it is, outside the blocks and on trunk's lines in all of them.
	var placed []string
	for _, seg := range segs {
		lines := seg.Lines
		if seg.Block != nil {
			lines = seg.Block.Trunk
		}
		its, err := s.items(c.Path, lines)
		if err != nil {
			return nil, err
		}
		placed = append(placed, its...)
	}
	return Render(segs, func(b Block) ([]string, error) {
		return s.collapseNew(c.Path, b, &placed)
	})
}

// collapse resolves one block of list lines and blank lines. See collapseNew.
func (s ListUnion) collapse(path string, b Block) ([]string, error) {
	return s.collapseNew(path, b, nil)
}

// collapseNew keeps trunk's lines as they are, blank lines included, and adds
// each item the branch added to the end of the trunk list line its own line
// shares the most items with (the first on a tie). A branch list line that
// shares no item with any trunk line stays a line of its own, after trunk's,
// unless it is the only list line on the branch's side and the block's base
// side holds a list line: then it is that line, edited, and joins trunk's
// first.
// An item is never added twice: placed holds the items already in the result
// from elsewhere, and grows by what this block adds. Any line in the block
// that is neither a list line nor blank is refused, as is a side with no list
// line.
func (s ListUnion) collapseNew(path string, b Block, placed *[]string) ([]string, error) {
	if len(b.Branch) == 0 || len(b.Trunk) == 0 {
		return nil, Refuse(path, "the conflict is not on the list line")
	}
	branchLines, err := s.listLines(path, b.Branch)
	if err != nil {
		return nil, err
	}
	trunkLines, err := s.listLines(path, b.Trunk)
	if err != nil {
		return nil, err
	}
	if len(branchLines) == 0 || len(trunkLines) == 0 {
		return nil, Refuse(path, "the conflict is not on the list line")
	}
	baseItems, err := s.items(path, b.Base)
	if err != nil {
		return nil, err
	}
	baseLines, err := s.listLines(path, b.Base)
	if err != nil {
		return nil, err
	}
	var have []string
	if placed != nil {
		have = *placed
	}
	for _, tl := range trunkLines {
		have = append(have, tl.items...)
	}
	out := append([]string{}, b.Trunk...)
	var own []string
	for _, bl := range branchLines {
		var added []string
		for _, it := range bl.items {
			if slices.Contains(baseItems, it) || slices.Contains(have, it) {
				continue
			}
			added = append(added, it)
			have = append(have, it)
		}
		if len(added) == 0 {
			continue
		}
		best, shared := -1, 0
		for _, tl := range trunkLines {
			n := 0
			for _, it := range bl.items {
				if slices.Contains(tl.items, it) {
					n++
				}
			}
			if n > shared {
				best, shared = tl.index, n
			}
		}
		if best < 0 && len(branchLines) == 1 && len(baseLines) > 0 {
			// The branch's first line is the base line, edited: it and
			// trunk's first line are the same line, changed on both sides.
			best = trunkLines[0].index
		}
		if best < 0 {
			own = append(own, bl.prefix+strings.Join(added, s.Delimiter+" "))
			continue
		}
		out[best] = s.appendItems(out[best], added)
	}
	if placed != nil {
		*placed = have
	}
	return append(out, own...), nil
}

// listLine is one list line of a block side: where it sits, how it starts and
// what it holds.
type listLine struct {
	index  int
	prefix string
	items  []string
}

// listLines parses the list lines of one side; blank lines are skipped and any
// other line is refused.
func (s ListUnion) listLines(path string, lines []string) ([]listLine, error) {
	var out []listLine
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		loc := s.Line.FindStringIndex(l)
		if loc == nil {
			return nil, Refuse(path, "the conflict is not on the list line")
		}
		if loc[0] != 0 {
			return nil, Refuse(path, "list line regex must match at the start")
		}
		items, err := s.items(path, []string{l})
		if err != nil {
			return nil, err
		}
		out = append(out, listLine{index: i, prefix: l[:loc[1]], items: items})
	}
	return out, nil
}

// hasLine reports whether any line matches s.Line, regardless of whether that
// line holds any items: an empty list is still a list, just not one with
// anything to remove.
func (s ListUnion) hasLine(lines []string) bool {
	for _, l := range lines {
		if s.Line.FindString(l) != "" {
			return true
		}
	}
	return false
}

// items collects the list items of every matching line, validated.
func (s ListUnion) items(path string, lines []string) ([]string, error) {
	var out []string
	for _, l := range lines {
		m := s.Line.FindString(l)
		if m == "" {
			continue
		}
		for _, raw := range strings.Split(strings.TrimPrefix(l, m), s.Delimiter) {
			it := strings.TrimSpace(raw)
			if it == "" {
				continue
			}
			if !itemRE.MatchString(it) {
				return nil, Refuse(path, "cannot parse %s as a list item", it)
			}
			out = append(out, it)
		}
	}
	return out, nil
}

// appendItems adds items to the end of a trunk list line without breaking it:
// no delimiter before the first item of a line with none yet, and none doubled
// after a line that already ends with one.
func (s ListUnion) appendItems(line string, added []string) string {
	joined := strings.Join(added, s.Delimiter+" ")
	m := s.Line.FindString(line)
	if strings.TrimSpace(strings.TrimPrefix(line, m)) == "" {
		if m != "" && !strings.HasSuffix(m, " ") && !strings.HasSuffix(m, "\t") {
			return line + " " + joined
		}
		return line + joined
	}
	line = strings.TrimRight(line, " \t")
	if strings.HasSuffix(line, s.Delimiter) {
		return line + " " + joined
	}
	return line + s.Delimiter + " " + joined
}
