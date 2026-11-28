package schema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var semver = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// A schema's x-version is MAJOR.MINOR.PATCH, and its major is the schema
// number in its file name: a breaking change is a new file, never an edit.
// Every file is held to it, the ones a newer major shadows too, so none can
// pass itself off as another.
func TestEverySchemaVersionMatchesItsFileName(t *testing.T) {
	entries, err := files.ReadDir(".")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no schema files: %v", err)
	}
	for _, e := range entries {
		data, err := files.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		d := Doc{Name: strings.SplitN(e.Name(), ".", 2)[0], File: e.Name(), Bytes: data}
		m := semver.FindStringSubmatch(d.Version())
		if m == nil {
			t.Errorf("%s: x-version %q is not MAJOR.MINOR.PATCH", d.File, d.Version())
			continue
		}
		if want := fmt.Sprintf("%s.v%s.json", d.Name, m[1]); d.File != want {
			t.Errorf("%s: major version %s belongs in %s", d.File, m[1], want)
		}
	}
}

// sync-run stays open to a later minor's fields, and its title, which wt
// schema and docs/json.md list it by, names every verb that prints it.
func TestSyncRunIsOpenAndNamesItsVerbs(t *testing.T) {
	d, _ := Get("sync-run")
	var doc any
	if err := json.Unmarshal(d.Bytes, &doc); err != nil {
		t.Fatalf("%s: %v", d.File, err)
	}
	for _, at := range closed(doc, "#") {
		t.Errorf("%s closes the object at %s: a reader could not validate newer output", d.File, at)
	}
	if want := "wt sync rebase|resume|undo|steps|push --json"; d.Title() != want {
		t.Errorf("title %q, want %q", d.Title(), want)
	}
}

// A schema with more than one major is served at the newest; the older file
// stays, unedited, for whoever reads results saved under it.
func TestTheNewestMajorOfASchemaIsTheOneServed(t *testing.T) {
	d, ok := Get("sync-run")
	if !ok || d.File != "sync-run.v2.json" {
		t.Errorf("sync-run is %q, want sync-run.v2.json", d.File)
	}
	seen := map[string]bool{}
	for _, d := range All() {
		if seen[d.Name] {
			t.Errorf("%s is listed twice", d.Name)
		}
		seen[d.Name] = true
	}
}

// closed is where node, at path, holds additionalProperties false.
func closed(node any, path string) []string {
	var out []string
	switch n := node.(type) {
	case map[string]any:
		if n["additionalProperties"] == false {
			out = append(out, path)
		}
		for k, v := range n {
			out = append(out, closed(v, path+"/"+k)...)
		}
	case []any:
		for i, v := range n {
			out = append(out, closed(v, fmt.Sprintf("%s/%d", path, i))...)
		}
	}
	return out
}
