package wtsync

import (
	"fmt"
	"regexp"
	"strings"
)

// BulletUnion resolves a Markdown bullet list that both sides only add to:
// trunk's items as trunk has them, then the items the branch added, in the
// branch's order, each once. An item is its bullet line with the more
// indented lines under it, and it moves and is compared whole. A side that
// removes or changes an item is refused: a removal or a rewording is a
// decision, not a merge. It resolves nothing but bullet items and blank lines.
type BulletUnion struct{}

// Name identifies this strategy in errors and reports.
func (BulletUnion) Name() string { return StrategyBulletUnion }

// bulletRE is a bullet line: its indent, its marker and the text after it.
var bulletRE = regexp.MustCompile(`^([ \t]*)([-*+])[ \t]+\S`)

// bulletItem is one item: where it starts, how, and its lines exactly as the
// file has them, trailing white space included (two trailing spaces end a
// line of a paragraph), so what is compared and what is written is the same.
type bulletItem struct {
	indent     int
	marker     string
	lines      []string
	start, end int // the item's lines in the file it was read from: [start, end)
}

func (it bulletItem) key() string { return strings.Join(it.lines, "\n") }

// bulletIndent is the width of a line's leading white space, a tab counting four.
func bulletIndent(l string) int {
	n := 0
	for _, r := range l {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// bulletAt reports the indent and marker of a bullet line.
func bulletAt(l string) (indent int, marker string, ok bool) {
	m := bulletRE.FindStringSubmatch(l)
	if m == nil {
		return 0, "", false
	}
	return bulletIndent(m[1]), m[2], true
}

// itemAt reads the item that starts at lines[i]: the bullet line and every
// line after it up to the next non-blank line that is not indented deeper.
// Blank lines that end the item are not part of it.
func itemAt(lines []string, i int) bulletItem {
	indent, marker, _ := bulletAt(lines[i])
	it := bulletItem{indent: indent, marker: marker, start: i, end: i + 1}
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		if bulletIndent(lines[j]) <= indent {
			break
		}
		it.end = j + 1
	}
	it.lines = lines[i:it.end]
	return it
}

// bulletList is the run of sibling items under one anchor: the last line of
// prose before it, and which list of that anchor it is in the file.
type bulletList struct {
	key   string
	items []bulletItem
}

// bulletLists reads the lists of a file. Items are the top-level ones, with
// whatever is indented under them; blank lines do not end a list, any other
// line that is not a bullet does.
func bulletLists(lines []string) []bulletList {
	var out []bulletList
	seen := map[string]int{}
	anchor := ""
	cur := -1
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		indent, _, ok := bulletAt(lines[i])
		if !ok {
			cur, anchor = -1, strings.TrimSpace(lines[i])
			i++
			continue
		}
		if cur < 0 || out[cur].items[0].indent != indent {
			seen[anchor]++
			out = append(out, bulletList{key: fmt.Sprintf("%s#%d", anchor, seen[anchor])})
			cur = len(out) - 1
		}
		it := itemAt(lines, i)
		out[cur].items = append(out[cur].items, it)
		i = it.end
	}
	return out
}

// counts is how many times each item is in a list.
func counts(l bulletList) map[string]int {
	out := map[string]int{}
	for _, it := range l.items {
		out[it.key()]++
	}
	return out
}

// topItems reads one side of a block: blank lines and bullet items, each
// top-level item with whatever is indented under it. Anything else is refused.
func topItems(path string, lines []string) ([]bulletItem, error) {
	var out []bulletItem
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		if _, _, ok := bulletAt(lines[i]); !ok {
			return nil, Refuse(path, "%q is not a bullet item", strings.TrimSpace(lines[i]))
		}
		it := itemAt(lines, i)
		out = append(out, it)
		i = it.end
	}
	return out, nil
}

// keeps reports the first item of base that side does not keep, in the same
// list and as many times; "" when it keeps them all.
func keeps(base []bulletList, side []bulletList) string {
	byKey := map[string]map[string]int{}
	for _, l := range side {
		byKey[l.key] = counts(l)
	}
	for _, l := range base {
		have := byKey[l.key]
		for k, n := range counts(l) {
			if have[k] < n {
				return strings.TrimSpace(strings.SplitN(k, "\n", 2)[0])
			}
		}
	}
	return ""
}

// Resolve merges c and collapses each conflict block by way of collapse.
func (s BulletUnion) Resolve(c Conflict) ([]byte, error) {
	// An item of the base must stay as it is, in its own list, on both
	// sides, wherever in the file it sits, so a block that changes only the
	// nested lines of a parent outside it is caught here too.
	base := bulletLists(strings.Split(string(c.Base), "\n"))
	if it := keeps(base, bulletLists(strings.Split(string(c.Branch), "\n"))); it != "" {
		return nil, Refuse(c.Path, "the branch removes or changes %q; that is a decision, not a merge", it)
	}
	if it := keeps(base, bulletLists(strings.Split(string(c.Trunk), "\n"))); it != "" {
		return nil, Refuse(c.Path, "trunk removes or changes %q; that is a decision, not a merge", it)
	}
	segs, err := Merge3(c)
	if err != nil {
		return nil, err
	}
	// An item is written once in its list: the lists of trunk's side of the
	// file say what each block's list already holds.
	trunkSide, err := Render(segs, func(b Block) ([]string, error) { return b.Trunk, nil })
	if err != nil {
		return nil, err
	}
	lists := bulletLists(strings.Split(string(trunkSide), "\n"))
	// What each list holds, shared by every block of that list, so an item
	// the branch adds in two blocks of one list is written once.
	have := make([]map[string]bool, len(lists))
	for i, l := range lists {
		have[i] = map[string]bool{}
		for k := range counts(l) {
			have[i][k] = true
		}
	}
	var starts []int
	off := 0
	for _, seg := range segs {
		if seg.Block != nil {
			starts = append(starts, off)
			off += len(seg.Block.Trunk)
		} else {
			off += len(seg.Lines)
		}
	}
	n := 0
	return Render(segs, func(b Block) ([]string, error) {
		at := starts[n]
		n++
		return s.collapse(c.Path, b, lists, have, at)
	})
}

// collapse resolves one block, which starts at line at of trunk's side of the
// file: trunk's lines as they are, then each item the branch added that the
// block's list does not hold, after trunk's last item and before any blank
// lines that end the block.
func (s BulletUnion) collapse(path string, b Block, lists []bulletList, held []map[string]bool, at int) ([]string, error) {
	base, err := topItems(path, b.Base)
	if err != nil {
		return nil, err
	}
	trunk, err := topItems(path, b.Trunk)
	if err != nil {
		return nil, err
	}
	branch, err := topItems(path, b.Branch)
	if err != nil {
		return nil, err
	}
	if len(trunk) == 0 || len(branch) == 0 {
		return nil, Refuse(path, "the conflict is not on a bullet list")
	}
	for _, side := range [][]bulletItem{base, trunk, branch} {
		for _, it := range side {
			if it.marker != trunk[0].marker {
				return nil, Refuse(path, "the sides use different bullet markers (%s and %s) in one list", trunk[0].marker, it.marker)
			}
			if it.indent != trunk[0].indent {
				return nil, Refuse(path, "the sides use different indents in one list")
			}
		}
	}
	first := at + trunk[0].start
	have := map[string]bool{}
	for i, l := range lists {
		for _, it := range l.items {
			if it.start <= first && first < it.end {
				have = held[i]
			}
		}
	}
	var added []string
	for _, it := range branch {
		if have[it.key()] {
			continue
		}
		have[it.key()] = true
		added = append(added, it.lines...)
	}
	end := len(b.Trunk)
	for end > 0 && strings.TrimSpace(b.Trunk[end-1]) == "" {
		end--
	}
	out := append([]string{}, b.Trunk[:end]...)
	out = append(out, added...)
	return append(out, b.Trunk[end:]...), nil
}
