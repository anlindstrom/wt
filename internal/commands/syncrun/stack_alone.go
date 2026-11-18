package syncrun

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/anlindstrom/wt/internal/git"
)

// A --stack run given --alone rebases and folds the line, and moves none of
// its layers. The stack's record is wtsync's and has no field for that, and a
// resume of a handed-over run must still skip the layers, so the run marks its
// epoch next to the record: a file named <epoch>.alone in wt-sync-stack.

func aloneMarker(mainRoot string, epoch int64) (string, error) {
	common, err := git.Run(mainRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "wt-sync-stack", strconv.FormatInt(epoch, 10)+".alone"), nil
}

// MarkAlone records that the stack run of epoch moves no layer.
func MarkAlone(mainRoot string, epoch int64) error {
	path, err := aloneMarker(mainRoot, epoch)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o644)
}

// IsAlone is whether the stack run of epoch was marked by MarkAlone.
func IsAlone(mainRoot string, epoch int64) bool {
	path, err := aloneMarker(mainRoot, epoch)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// ClearAlone removes the mark once the run's layers are settled.
func ClearAlone(mainRoot string, epoch int64) {
	if path, err := aloneMarker(mainRoot, epoch); err == nil {
		_ = os.Remove(path)
	}
}
