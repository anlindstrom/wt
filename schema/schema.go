// Package schema holds the JSON Schemas of wt's --json output, embedded so
// `wt schema` prints the ones that match the binary running it.
package schema

import (
	"embed"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.v*.json
var files embed.FS

// Doc is one schema: the command whose --json output it describes, and its
// document.
type Doc struct {
	Name  string // "status", "up", "sweep-plan"
	File  string // "up.v1.json"
	Bytes []byte
}

// All is every schema, by name, each at its newest major. The files of
// earlier majors stay beside it, unedited, for results saved under them.
func All() []Doc {
	entries, _ := files.ReadDir(".")
	newest := map[string]Doc{}
	for _, e := range entries {
		data, err := files.ReadFile(e.Name())
		if err != nil {
			continue
		}
		d := Doc{Name: strings.SplitN(e.Name(), ".", 2)[0], File: e.Name(), Bytes: data}
		if have, ok := newest[d.Name]; !ok || Major(d.Version()) > Major(have.Version()) {
			newest[d.Name] = d
		}
	}
	out := make([]Doc, 0, len(newest))
	for _, d := range newest {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Major is the major number of a MAJOR.MINOR.PATCH version, 0 when it is
// not one.
func Major(version string) int {
	major, _, _ := strings.Cut(version, ".")
	n, _ := strconv.Atoi(major)
	return n
}

// ID is the schema's $id: where it is published.
func (d Doc) ID() string { return d.head().ID }

// Version is the schema's semantic version, its x-version: the major is the
// schema number in the file name and in every output's "schema"; a minor adds
// a field or an enumeration value; a patch changes wording only. Every output
// carries it as "schemaVersion".
func (d Doc) Version() string { return d.head().Version }

// Title is the schema's title: the command line whose output it describes,
// "wt sweep --dry-run --json".
func (d Doc) Title() string { return d.head().Title }

func (d Doc) head() (h struct {
	ID      string `json:"$id"`
	Version string `json:"x-version"`
	Title   string `json:"title"`
}) {
	_ = json.Unmarshal(d.Bytes, &h)
	return h
}

// VersionOf is the version of the schema of that name, "" when there is none.
func VersionOf(name string) string {
	d, _ := Get(name)
	return d.Version()
}

// Get is the schema of that name, and false when there is none.
func Get(name string) (Doc, bool) {
	for _, d := range All() {
		if d.Name == name {
			return d, true
		}
	}
	return Doc{}, false
}
