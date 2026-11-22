package wtsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// PushRecord is what a --stack run leaves for the push that comes later: the
// leases it took and the tips it left, for a line whose moved layers it did
// not push (--no-push, a no to the question, nobody to ask). It is kept per
// line, apart from the run's own StackRecord, which goes when the run ends.
// A run never reads it; only wt sync push <work> --stack does.
type PushRecord struct {
	// Branch is the line's own branch, and Work its worktree's name.
	Branch string `json:"branch"`
	Work   string `json:"work"`
	// Path is where the line's branch is checked out.
	Path string `json:"path,omitempty"`
	// Epoch is the run's.
	Epoch   int64     `json:"epoch"`
	Written time.Time `json:"written"`
	// Head and Base are the line's tip before the run and where it leaves
	// trunk; Commits and Subject say what the line was. For the report only.
	Head    string `json:"head"`
	Base    string `json:"base"`
	Commits int    `json:"commits"`
	Subject string `json:"subject"`
	// Items are the branches to push, moved layers bottom first and the
	// line's own branch last.
	Items []PushRecordItem `json:"items"`
	// Left are the layers the run left out, named again by the push.
	Left []PushLeft `json:"left,omitempty"`
}

// PushRecordItem is one branch of a later push: where it pushes, the lease,
// and the tip the run left it at, which the push only goes on while it is at.
type PushRecordItem struct {
	BranchPush
	New string `json:"new"`
	Top bool   `json:"top,omitempty"`
}

// PushLeft is a layer the run left out of the push, and why.
type PushLeft struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip"`
	Why    string `json:"why"`
}

// pushRecordPath is the file of the line branch's record: in the common git
// dir, beside the stack records, named by the branch (escaped, as a branch
// name may hold slashes).
func pushRecordPath(mainRoot, branch string) (string, error) {
	common, err := gitEnv(mainRoot, nil, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "wt-sync-push", url.PathEscape(branch)+".json"), nil
}

// lockPushRecords waits for the flock every write of a push record holds, on
// the records' directory, and returns the func that lets it go. A settle's
// check and its change are then one step against a run saving its record.
func lockPushRecords(mainRoot string) (func(), error) {
	path, err := pushRecordPath(mainRoot, "x")
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // an fd fits an int
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // as above
		_ = f.Close()
	}, nil
}

// pushRecordBeforeChange runs between ReplacePushRecord's check and its
// change; tests use it to put another write there.
var pushRecordBeforeChange = func() {}

// WritePushRecord writes rec in full, atomically, replacing the line's.
func WritePushRecord(mainRoot string, rec *PushRecord) error {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return err
	}
	defer unlock()
	return writePushRecord(mainRoot, rec)
}

func writePushRecord(mainRoot string, rec *PushRecord) error {
	path, err := pushRecordPath(mainRoot, rec.Branch)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadPushRecord reads the record of the line whose own branch is branch; ok
// is false when it has none.
func ReadPushRecord(mainRoot, branch string) (*PushRecord, bool, error) {
	path, err := pushRecordPath(mainRoot, branch)
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var rec PushRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return &rec, true, nil
}

// ForgetPushRecord deletes the line's record, if it has one.
func ForgetPushRecord(mainRoot, branch string) error {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return err
	}
	defer unlock()
	return forgetPushRecord(mainRoot, branch)
}

func forgetPushRecord(mainRoot, branch string) error {
	path, err := pushRecordPath(mainRoot, branch)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ForgetPushRecordOfRun deletes the record the run of epoch left, whatever
// line it names: undo puts the line back, and the tips the record names are
// gone. A record of a later run is left.
func ForgetPushRecordOfRun(mainRoot string, epoch int64) error {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return err
	}
	defer unlock()
	path, err := pushRecordPath(mainRoot, "x")
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var rec struct {
			Epoch int64 `json:"epoch"`
		}
		if json.Unmarshal(b, &rec) != nil || rec.Epoch != epoch {
			continue
		}
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ErrPushRecordReplaced says a record is no longer the one a push was made
// from or a run wrote: a later run replaced it, or a push took it.
var ErrPushRecordReplaced = errors.New("the line's push record is no longer the one pushed from; it is left as it is")

// ReplacePushRecord replaces from, the record a push was made from, with to
// (nil deletes it), and only while the line's record is still from: the same
// run and the same time. Any other is left, and an error says so.
func ReplacePushRecord(mainRoot string, from, to *PushRecord) error {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return err
	}
	defer unlock()
	cur, ok, err := ReadPushRecord(mainRoot, from.Branch)
	if err != nil {
		return err
	}
	if !ok || cur.Epoch != from.Epoch || !cur.Written.Equal(from.Written) {
		return ErrPushRecordReplaced
	}
	pushRecordBeforeChange()
	if to == nil {
		return forgetPushRecord(mainRoot, from.Branch)
	}
	return writePushRecord(mainRoot, to)
}
