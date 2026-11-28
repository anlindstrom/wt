package git

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var confined = struct {
	sync.Mutex
	roots map[string]int
}{roots: map[string]int{}}

// Confine keeps every git and every script run in a directory under root to
// the repository it finds there: the variables of the process environment
// that would point it at another one, or add configuration to it, are left
// out, and the global and system configuration are off. release ends it.
func Confine(root string) (release func()) {
	roots := []string{filepath.Clean(root)}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != roots[0] {
		roots = append(roots, resolved)
	}
	confined.Lock()
	defer confined.Unlock()
	for _, r := range roots {
		confined.roots[r]++
	}
	return func() {
		confined.Lock()
		defer confined.Unlock()
		for _, r := range roots {
			if confined.roots[r]--; confined.roots[r] <= 0 {
				delete(confined.roots, r)
			}
		}
	}
}

func isConfined(dir string) bool {
	confined.Lock()
	defer confined.Unlock()
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if confined.roots[d] > 0 {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

// redirects says the variable name points git at a repository, or at
// configuration, other than the one in the directory it runs in.
func redirects(name string) bool {
	switch name {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE",
		"GIT_QUARANTINE_PATH", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM":
		return true
	}
	return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// EnvironIn is Environ for a command run in dir: under a Confine root it is
// ConfinedEnviron.
func EnvironIn(dir string, extra ...string) []string {
	if !isConfined(dir) {
		return Environ(extra...)
	}
	return ConfinedEnviron(extra...)
}

// ConfinedEnviron is what a command run under a Confine root gets: the
// process environment without what would take it elsewhere, no global or
// system configuration, and extra set over it.
func ConfinedEnviron(extra ...string) []string {
	extra = append([]string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}, extra...)
	set := map[string]bool{}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		set[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !set[k] && !redirects(k) {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}
