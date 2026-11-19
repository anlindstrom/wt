package synccmdtest

import (
	"path/filepath"
	"testing"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/gittest"
)

// InPlaceCheckedLine is CheckedLine for a fold in place, whose commits
// predate any script trunk added: the check is a command of the declaration,
// which appends the subject it ran at to the returned log and fails at every
// commit that has b1.txt until a commit has fix.txt. The file logPath+".pass",
// when it exists, makes it pass everywhere. paths, when not empty, is the
// step's paths list, as YAML.
func InPlaceCheckedLine(t *testing.T, paths string) (ctx *kit.Context, s *gittest.StackLine, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "checked.log")
	yaml := StackYAML + "each_commit:\n  - run: |\n" +
		"      git log -1 --format=%s >> '" + logPath + "'\n" +
		"      if [ -f b1.txt ] && [ ! -f fix.txt ] && [ ! -f '" + logPath + ".pass' ]; then echo 'b1.txt needs fix.txt'; exit 1; fi\n"
	if paths != "" {
		yaml += "    paths: " + paths + "\n"
	}
	ctx, s = StackLine(t, yaml)
	return ctx, s, logPath
}
