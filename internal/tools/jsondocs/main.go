// Command jsondocs writes the generated regions of docs/json.md from the
// schemas in schema/.
//
// A region is a pair of comment lines in the document:
//
//	<!-- json-docs:begin up -->
//	<!-- json-docs:end -->
//
// naming one or more schemas (up, sync-run, ...), or "index" for the table
// that lists every schema. Everything between the two lines is replaced; the
// prose around a region is written by hand and never touched. The output is a
// pure function of the schemas: same input, same bytes.
//
//	go run ./internal/tools/jsondocs [-schema schema] [-doc docs/json.md] [-check]
//
// -check writes nothing and fails when the document would change.
//
// It lives under internal/ because it is a build tool of this repository, not
// something wt ships; `make json-docs` runs it.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/anlindstrom/wt/schema"
)

func main() {
	schemaDir := flag.String("schema", "schema", "directory holding the *.v*.json schemas")
	docPath := flag.String("doc", "docs/json.md", "the document whose regions are rewritten")
	check := flag.Bool("check", false, "write nothing; exit 1 when the document is out of date with the schemas")
	flag.Parse()
	if err := run(*schemaDir, *docPath, *check); err != nil {
		fmt.Fprintln(os.Stderr, "jsondocs:", err)
		os.Exit(1)
	}
}

func run(schemaDir, docPath string, check bool) error {
	schemas, err := loadSchemas(schemaDir)
	if err != nil {
		return err
	}
	doc, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	out, err := rewrite(string(doc), schemas)
	if err != nil {
		return fmt.Errorf("%s: %w", docPath, err)
	}
	if out == string(doc) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is out of date with %s: run make json-docs and commit it", docPath, schemaDir)
	}
	return os.WriteFile(docPath, []byte(out), 0o644) //nolint:gosec // G703: docPath is the -doc flag of a build tool a developer runs on their own checkout
}

// schemaDoc is one schema file: its name ("up"), file name and root object.
type schemaDoc struct {
	name, file string
	root       obj
	problems   []string // what the generator could not represent, reported by render
	at         string   // the field path being resolved, for problems
}

func loadSchemas(dir string) (map[string]*schemaDoc, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.v*.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.v*.json in %s", dir)
	}
	out := map[string]*schemaDoc{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("%s: not valid JSON", f)
		}
		root, err := parseObj(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		base := filepath.Base(f)
		name := strings.SplitN(base, ".", 2)[0]
		d := &schemaDoc{name: name, file: base, root: root}
		// The newest major is the one documented.
		if have, ok := out[name]; !ok || schema.Major(d.str("x-version")) > schema.Major(have.str("x-version")) {
			out[name] = d
		}
	}
	return out, nil
}

var (
	beginLine = regexp.MustCompile(`^<!-- json-docs:begin ([a-z0-9, -]+) -->$`)
	endLine   = "<!-- json-docs:end -->"
)

// rewrite replaces the body of every region in doc. Nothing is returned for a
// document whose markers are not well formed: a begin inside a region, an end
// with no begin, a begin with no end, an unreadable begin line, or a schema
// that does not exist, each named by its line.
func rewrite(doc string, schemas map[string]*schemaDoc) (string, error) {
	lines := strings.SplitAfter(doc, "\n")
	var b strings.Builder
	open := 0 // line number of the begin we are inside, 0 when outside
	for i := 0; i < len(lines); i++ {
		n := i + 1
		text := strings.TrimRight(lines[i], "\n")
		switch {
		case strings.HasPrefix(text, "<!-- json-docs:begin"):
			if open != 0 {
				return "", fmt.Errorf("line %d: json-docs:begin inside the region begun at line %d (no json-docs:end before it)", n, open)
			}
			m := beginLine.FindStringSubmatch(text)
			if m == nil {
				return "", fmt.Errorf("line %d: unreadable json-docs:begin marker %q", n, text)
			}
			names := strings.Split(m[1], ",")
			body, err := render(names, schemas)
			if err != nil {
				return "", fmt.Errorf("line %d: %w", n, err)
			}
			open = n
			b.WriteString(lines[i])
			b.WriteString(body)
		case text == endLine:
			if open == 0 {
				return "", fmt.Errorf("line %d: json-docs:end with no json-docs:begin before it", n)
			}
			open = 0
			b.WriteString(lines[i])
		case open != 0:
			// the old body of a region: replaced
		default:
			b.WriteString(lines[i])
		}
	}
	if open != 0 {
		return "", fmt.Errorf("line %d: json-docs:begin has no json-docs:end", open)
	}
	return b.String(), nil
}

func render(names []string, schemas map[string]*schemaDoc) (string, error) {
	if len(names) == 1 && strings.TrimSpace(names[0]) == "index" {
		return renderIndex(schemas), nil
	}
	var b strings.Builder
	for i, n := range names {
		n = strings.TrimSpace(n)
		s, ok := schemas[n]
		if !ok {
			return "", fmt.Errorf("json-docs region names %q: no schema/%s.v*.json", n, n)
		}
		if i > 0 {
			b.WriteString("\n")
		}
		if len(names) > 1 {
			fmt.Fprintf(&b, "`%s`, [`%s`](../schema/%s), schema %s:\n\n", s.str("title"), s.file, s.file, s.str("x-version"))
		}
		b.WriteString(renderTable(s))
		if len(s.problems) > 0 {
			return "", fmt.Errorf("schema %s %s", s.name, s.problems[0])
		}
	}
	return b.String(), nil
}

// renderIndex is the table of every schema, in the order of its command line.
func renderIndex(schemas map[string]*schemaDoc) string {
	list := make([]*schemaDoc, 0, len(schemas))
	for _, s := range schemas {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	var b strings.Builder
	b.WriteString("| Output | Schema | Version | `$id` |\n|---|---|---|---|\n")
	for _, s := range list {
		fmt.Fprintf(&b, "| `%s` | [`schema/%s`](../schema/%s) | %s | `%s` |\n",
			cell(s.str("title")), s.file, s.file, s.str("x-version"), s.str("$id"))
	}
	return b.String()
}

func (s *schemaDoc) str(key string) string {
	var v string
	if raw, ok := s.root.m[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

// obj is a JSON object that remembers its key order, which a schema's
// "properties" carry as the order fields are documented in.
type obj struct {
	keys []string
	m    map[string]json.RawMessage
}

func parseObj(data []byte) (obj, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return obj{}, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return obj{}, fmt.Errorf("not an object")
	}
	o := obj{m: map[string]json.RawMessage{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return obj{}, err
		}
		key := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return obj{}, err
		}
		o.keys = append(o.keys, key)
		o.m[key] = raw
	}
	return o, nil
}

// view is a schema node with $ref and oneOf folded away: what a reader of the
// document needs to know about one field.
type view struct {
	types    []string
	enum     []string // rendered values, null included
	format   string
	min      *float64
	minItems *float64
	pattern  string
	addl     string // the type of an object's additionalProperties schema
	desc     string
	props    obj
	req      map[string]bool
	items    *obj
	defName  string // the $defs entry it came through, when it did
}

func (s *schemaDoc) resolve(n obj, defs map[string]bool) view {
	var v view
	v.req = map[string]bool{}
	if raw, ok := n.m["$ref"]; ok {
		var ref string
		_ = json.Unmarshal(raw, &ref)
		name := strings.TrimPrefix(ref, "#/$defs/")
		defsObj, _ := parseObj(s.root.m["$defs"])
		if target, ok := defsObj.m[name]; ok {
			t, _ := parseObj(target)
			v = s.resolve(t, defs)
			v.defName = name
		}
	}
	s.merge(&v, n, defs)
	return v
}

// merge folds the keywords of n into v: the node's own, then each oneOf
// alternative's.
func (s *schemaDoc) merge(v *view, n obj, defs map[string]bool) {
	if raw, ok := n.m["type"]; ok {
		var one string
		var many []string
		if json.Unmarshal(raw, &one) == nil {
			v.types = addAll(v.types, one)
		} else if json.Unmarshal(raw, &many) == nil {
			v.types = addAll(v.types, many...)
		}
	}
	if raw, ok := n.m["const"]; ok {
		v.enum = addAll(v.enum, literal(raw))
	}
	if raw, ok := n.m["enum"]; ok {
		var vals []json.RawMessage
		_ = json.Unmarshal(raw, &vals)
		for _, x := range vals {
			v.enum = addAll(v.enum, literal(x))
		}
	}
	if raw, ok := n.m["format"]; ok {
		_ = json.Unmarshal(raw, &v.format)
	}
	if raw, ok := n.m["minimum"]; ok {
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			v.min = &f
		}
	}
	if raw, ok := n.m["minItems"]; ok {
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			v.minItems = &f
		}
	}
	if raw, ok := n.m["pattern"]; ok {
		_ = json.Unmarshal(raw, &v.pattern)
	}
	if raw, ok := n.m["additionalProperties"]; ok {
		if ao, err := parseObj(raw); err == nil {
			v.addl = s.resolve(ao, defs).typeCell("")
		}
	}
	if raw, ok := n.m["description"]; ok {
		var d string
		if json.Unmarshal(raw, &d) == nil && d != "" {
			v.desc = d
		}
	}
	if raw, ok := n.m["required"]; ok {
		var r []string
		_ = json.Unmarshal(raw, &r)
		for _, k := range r {
			v.req[k] = true
		}
	}
	if raw, ok := n.m["properties"]; ok {
		if p, err := parseObj(raw); err == nil {
			if len(v.props.keys) == 0 {
				v.props = p
			} else {
				for _, k := range p.keys {
					if _, dup := v.props.m[k]; !dup {
						v.props.keys = append(v.props.keys, k)
					}
					v.props.m[k] = p.m[k]
				}
			}
		}
	}
	if raw, ok := n.m["items"]; ok {
		if it, err := parseObj(raw); err == nil {
			v.items = &it
		}
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		raw, ok := n.m[kw]
		if !ok {
			continue
		}
		var alts []json.RawMessage
		_ = json.Unmarshal(raw, &alts)
		withProps, withItems := 0, 0
		ownProps, ownItems := len(v.props.keys) > 0, v.items != nil
		for _, a := range alts {
			if ao, err := parseObj(a); err == nil {
				sub := s.resolve(ao, defs)
				v.types = addAll(v.types, sub.types...)
				v.enum = addAll(v.enum, sub.enum...)
				if v.format == "" {
					v.format = sub.format
				}
				if v.min == nil {
					v.min = sub.min
				}
				if v.minItems == nil {
					v.minItems = sub.minItems
				}
				if v.pattern == "" {
					v.pattern = sub.pattern
				}
				if v.addl == "" {
					v.addl = sub.addl
				}
				if len(sub.props.keys) > 0 {
					withProps++
					if withProps > 1 || ownProps {
						s.problems = append(s.problems, fmt.Sprintf("at %q: %s adds properties beside another set (more than one alternative, or the node's own): the table would drop fields", s.at, kw))
					}
				}
				if sub.items != nil {
					withItems++
					if withItems > 1 || ownItems {
						s.problems = append(s.problems, fmt.Sprintf("at %q: %s has more than one array item schema (several alternatives, or the node's own): the table would keep one", s.at, kw))
					}
				}
				if v.desc == "" {
					v.desc = sub.desc
				}
				if len(sub.props.keys) > 0 && len(v.props.keys) == 0 {
					v.props = sub.props
					for k := range sub.req {
						v.req[k] = true
					}
				}
				if sub.items != nil && v.items == nil {
					v.items = sub.items
				}
				if v.defName == "" {
					v.defName = sub.defName
				}
			}
		}
	}
}

func addAll(dst []string, xs ...string) []string {
	for _, x := range xs {
		dup := false
		for _, d := range dst {
			if d == x {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, x)
		}
	}
	return dst
}

// literal renders a JSON scalar the way the document shows a value.
func literal(raw json.RawMessage) string {
	var s *string
	if json.Unmarshal(raw, &s) == nil && s != nil {
		return fmt.Sprintf("%q", *s)
	}
	return strings.TrimSpace(string(raw))
}

// typeCell: the type, then the values of an enumeration or a constant.
func (v view) typeCell(itemType string) string {
	var parts []string
	if len(v.enum) > 0 {
		vals := make([]string, 0, len(v.enum))
		for _, e := range v.enum {
			vals = append(vals, "`"+e+"`")
		}
		parts = append(parts, strings.Join(vals, " \\| "))
		if hasType(v.types, "null") && !contains(v.enum, "null") {
			parts = append(parts, "`null`")
		}
		return strings.Join(parts, " \\| ")
	}
	ts := make([]string, 0, len(v.types))
	for _, t := range v.types {
		if t == "array" && itemType != "" {
			t = "array of " + itemType
		}
		ts = append(ts, t)
	}
	// null last, as the document has always written "string | null".
	sort.SliceStable(ts, func(i, j int) bool { return ts[i] != "null" && ts[j] == "null" })
	out := strings.Join(ts, " \\| ")
	if v.addl != "" {
		out = strings.Replace(out, "object", "object of "+v.addl, 1)
	}
	if v.format != "" {
		out += " (" + v.format + ")"
	}
	if v.min != nil {
		out += fmt.Sprintf(", at least %g", *v.min)
	}
	if v.minItems != nil {
		out += fmt.Sprintf(", at least %g items", *v.minItems)
	}
	if v.pattern != "" {
		out += ", matching `" + strings.ReplaceAll(v.pattern, "|", "\\|") + "`"
	}
	return out
}

func hasType(ts []string, t string) bool { return contains(ts, t) }

func contains(xs []string, x string) bool {
	for _, e := range xs {
		if e == x {
			return true
		}
	}
	return false
}

// cell makes text safe inside a table cell without changing a word of it.
func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

// renderTable is the field table of one schema: every field path with its
// type, whether it is always present, and the schema's description verbatim.
// A definition used in several places is listed in full at the first and
// pointed to at the rest.
func renderTable(s *schemaDoc) string {
	var b strings.Builder
	b.WriteString("| Field | Type | Required | Description |\n|---|---|---|---|\n")
	expanded := map[string]string{}
	s.at = "(root)"
	root := s.resolve(s.root, nil)
	s.rows(&b, root, "", expanded)
	return b.String()
}

func (s *schemaDoc) rows(b *strings.Builder, parent view, prefix string, expanded map[string]string) {
	for _, key := range parent.props.keys {
		pn, err := parseObj(parent.props.m[key])
		if err != nil {
			continue
		}
		path := prefix + key
		s.at = path
		v := s.resolve(pn, nil)
		itemType := ""
		var itemView *view
		if hasType(v.types, "array") && v.items != nil {
			s.at = path + "[]"
			iv := s.resolve(*v.items, nil)
			itemView = &iv
			itemType = iv.typeCell("")
			if itemType == "" {
				itemType = "item"
			}
		}
		req := "no"
		if parent.req[key] {
			req = "yes"
		}
		desc := cell(v.desc)
		repeat := false
		if v.defName != "" && len(v.props.keys) > 0 {
			if first, seen := expanded[v.defName]; seen {
				repeat = true
				if desc != "" {
					desc += " "
				}
				desc += "Fields as at `" + first + "`."
			}
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", path, v.typeCell(itemType), req, desc)
		if repeat {
			continue
		}
		if len(v.props.keys) > 0 {
			if v.defName != "" {
				expanded[v.defName] = path
			}
			s.rows(b, v, path+".", expanded)
		}
		if itemView != nil && len(itemView.props.keys) > 0 {
			if itemView.defName != "" {
				if first, seen := expanded[itemView.defName]; seen {
					fmt.Fprintf(b, "| `%s[]` | object | yes | Fields as at `%s`. |\n", path, first)
					continue
				}
				expanded[itemView.defName] = path + "[]"
			}
			s.rows(b, *itemView, path+"[].", expanded)
		}
	}
}
