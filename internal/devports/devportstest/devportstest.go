// Package devportstest puts a stand-in devports first on the PATH of one
// serial test.
package devportstest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anlindstrom/wt/internal/gittest"
)

// Fake is what the stand-in answers.
type Fake struct {
	// Name is what `route name` prints; empty makes it fail.
	Name string
	// Config is the whole `config --json` document; empty makes it fail.
	Config string
	// Routes is the whole `route list --json` document; empty prints none.
	Routes string
	// Exit, when not 0, makes every call fail with it.
	Exit int
	// Sleep, in seconds, is how long every call takes before it answers.
	Sleep float64

	log string
}

// Install puts the stand-in first on the PATH. Never use it in a parallel test.
func (f *Fake) Install(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	f.log = filepath.Join(dir, "calls.log")
	routes := filepath.Join(dir, "routes.json")
	if err := os.WriteFile(routes, []byte(f.Routes), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(f.Config), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + f.log + "'\n"
	if f.Sleep > 0 {
		script += "sleep " + strconv.FormatFloat(f.Sleep, 'f', -1, 64) + "\n"
	}
	script += "if [ " + strconv.Itoa(f.Exit) + " != 0 ]; then echo 'devports: broken' >&2; exit " + strconv.Itoa(f.Exit) + "; fi\n" +
		"case \"$1 $2\" in\n" +
		"  'route name') [ -n '" + f.Name + "' ] && echo '" + f.Name + "' || exit 1;;\n" +
		"  'config --json') [ -s '" + config + "' ] && cat '" + config + "' || exit 1;;\n" +
		"  'route list') cat '" + routes + "';;\n" +
		"  *) echo \"devports: unexpected $*\" >&2; exit 64;;\n" +
		"esac\n"
	gittest.WriteExecutable(t, filepath.Join(dir, "devports"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Calls is each call made to the stand-in, its arguments one line each.
func (f *Fake) Calls() []string {
	b, _ := os.ReadFile(f.log)
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// RouteDoc is a `route list --json` document of routes, each given as
// url, name, checkout, pid.
func RouteDoc(routes ...[4]string) string {
	var items []string
	for _, r := range routes {
		items = append(items, `{"host":"`+strings.TrimPrefix(r[0], "https://")+`","url":"`+r[0]+`","name":"`+r[1]+
			`","label":"am","port":3543,"file":"/p/f.yaml","checkout":"`+r[2]+`","pid":`+r[3]+
			`,"command":"node","owner":null,"since":"2026-11-19T10:00:00Z","tlsOption":null,"middlewares":[]}`)
	}
	return `{"schema":1,"schemaVersion":"1.1.0","command":"route","routes":[` + strings.Join(items, ",") + `]}`
}

// ConfigDoc is a `config --json` document of apps, each given as name and
// domain; an empty domain is a null one. It is what a devports that reports
// each app's domain prints.
func ConfigDoc(apps ...[2]string) string {
	var items []string
	for _, a := range apps {
		domain := "null"
		if a[1] != "" {
			domain = `"` + a[1] + `"`
		}
		items = append(items, `{"name":"`+a[0]+`","group":"web","domain":`+domain+`}`)
	}
	return `{"schema":1,"schemaVersion":"1.0.0","command":"config","apps":[` + strings.Join(items, ",") + `]}`
}

// OldConfigDoc is the document of a devports that does not report domains:
// apps with names and nothing of a domain.
func OldConfigDoc(names ...string) string {
	var items []string
	for _, n := range names {
		items = append(items, `{"name":"`+n+`","group":"web"}`)
	}
	return `{"schema":1,"schemaVersion":"1.0.0","command":"config","apps":[` + strings.Join(items, ",") + `]}`
}
