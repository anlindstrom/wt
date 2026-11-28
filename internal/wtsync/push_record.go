package wtsync

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	// Layers is what the run did with every layer of the line, bottom first:
	// a push holds each layer no item sends to the tip the run left it at and
	// to its remote branch.
	Layers []PushLayer `json:"layers,omitempty"`
	// LayersRead says Layers is what the run read, which may be none: a
	// record from a build that did not keep it cannot show it is complete.
	LayersRead bool `json:"layersRead,omitempty"`
	// Rev names this version of the record: every write of it gives it a new
	// one, so a settle made from an older read, which still has the newest
	// run's epoch and time, is told from the record it was made from. Empty
	// for a record an older build wrote.
	Rev string `json:"rev,omitempty"`
	// Earlier are the runs before this one whose pushes are still owed,
	// oldest first: what the fields above say is the newest run's. A record
	// of one run, as a build before this one wrote it, has none.
	Earlier []PushRun `json:"earlier,omitempty"`
}

// PushRun is one run's contribution to a line's record: the fields of a
// PushRecord that belong to a run.
type PushRun struct {
	Path    string           `json:"path,omitempty"`
	Epoch   int64            `json:"epoch"`
	Written time.Time        `json:"written"`
	Head    string           `json:"head"`
	Base    string           `json:"base"`
	Commits int              `json:"commits"`
	Subject string           `json:"subject"`
	Items   []PushRecordItem `json:"items"`
	Left    []PushLeft       `json:"left,omitempty"`
	Layers  []PushLayer      `json:"layers,omitempty"`
	// LayersRead is as on PushRecord.
	LayersRead bool `json:"layersRead,omitempty"`
}

// Run is the record's own (newest) run as a PushRun.
func (r *PushRecord) Run() PushRun {
	return PushRun{Path: r.Path, Epoch: r.Epoch, Written: r.Written, Head: r.Head, Base: r.Base,
		Commits: r.Commits, Subject: r.Subject, Items: r.Items, Left: r.Left, Layers: r.Layers, LayersRead: r.LayersRead}
}

// Runs is every run the record holds, oldest first.
func (r *PushRecord) Runs() []PushRun {
	return append(slices.Clone(r.Earlier), r.Run())
}

// withRuns is a record for line branch and work holding runs, oldest first:
// the newest is the record's own fields. nil when runs is empty.
func withRuns(branch, work string, runs []PushRun) *PushRecord {
	if len(runs) == 0 {
		return nil
	}
	n := runs[len(runs)-1]
	return &PushRecord{Branch: branch, Work: work, Path: n.Path, Epoch: n.Epoch, Written: n.Written,
		Head: n.Head, Base: n.Base, Commits: n.Commits, Subject: n.Subject, Items: n.Items, Left: n.Left, Layers: n.Layers, LayersRead: n.LayersRead,
		Earlier: slices.Clone(runs[:len(runs)-1])}
}

// Without is the record with the entries for the branches in sent taken out
// of every run, a run with nothing left dropped; nil when no run is left. A
// push of a branch supersedes what every run recorded for it.
func (r *PushRecord) Without(sent map[string]bool) *PushRecord {
	var runs []PushRun
	for _, run := range r.Runs() {
		run.Items = slices.DeleteFunc(slices.Clone(run.Items), func(it PushRecordItem) bool { return sent[it.Branch] })
		if len(run.Items) > 0 {
			runs = append(runs, run)
		}
	}
	return withRuns(r.Branch, r.Work, runs)
}

// Branches are the branches the record's runs hold entries for, once each:
// layers in the order they were first moved, the line's own branch last, as
// Merged orders them.
func (r *PushRecord) Branches() []string {
	var out []string
	top := map[string]bool{}
	for _, run := range r.Runs() {
		for _, it := range run.Items {
			if !slices.Contains(out, it.Branch) {
				out = append(out, it.Branch)
			}
			top[it.Branch] = top[it.Branch] || it.Top
		}
	}
	slices.SortStableFunc(out, func(a, b string) int {
		switch {
		case top[a] == top[b]:
			return 0
		case top[a]:
			return 1
		}
		return -1
	})
	return out
}

// LeftOut is what the runs left out of the push, newest run first by
// branch: a branch a run moved is not left out, whatever another run said,
// and an older run's word is past once a newer run moved the branch or found
// it unchanged.
func (r *PushRecord) LeftOut() []PushLeft {
	moved := r.Branches()
	var out []PushLeft
	// decided is a branch a newer run moved or found unchanged: what an
	// older run left out of it is past.
	decided := map[string]bool{}
	runs := r.Runs()
	for i := len(runs) - 1; i >= 0; i-- {
		for _, l := range runs[i].Left {
			if slices.Contains(moved, l.Branch) || decided[l.Branch] || slices.ContainsFunc(out, func(o PushLeft) bool { return o.Branch == l.Branch }) {
				continue
			}
			out = append(out, l)
		}
		for _, l := range runs[i].Layers {
			if l.Result != MoveHeld {
				decided[l.Branch] = true
			}
		}
	}
	return out
}

// MergedItem is one branch of the push the record makes: its entry from the
// newest run that moved it, over the lease of the oldest where every run took
// the same one.
type MergedItem struct {
	PushRecordItem
	// LeaseFrom is the run the lease is from: the oldest that moved the
	// branch, or the newest when the leases differ. TipFrom is the run the tip
	// is from, the newest.
	LeaseFrom, TipFrom RunRef
	// LeasesDiffer says what each run's lease was, "" when they are one. The
	// item's lease is then the newest run's, which is good only while the
	// remote is at it: the older ones are stale after a push made since.
	LeasesDiffer string
}

// RunRef names a run of a record.
type RunRef struct {
	Epoch int64     `json:"epoch"`
	Time  time.Time `json:"time"`
}

// PushConflict is a branch the record cannot make one push of, and why.
type PushConflict struct {
	Branch string `json:"branch"`
	Why    string `json:"why"`
}

// Merged is the push the record's runs add up to, branches in the order they
// were first moved (the line's own last), and the branches that cannot be
// pushed. A branch moved by one run has that run's entry. One moved by
// several goes to the newest run's tip, and only while every run agrees on
// where it pushes: otherwise it is a PushConflict. Over the oldest run's lease
// when the runs took one lease; over the newest's when they did not (see
// MergedItem.LeasesDiffer).
func (r *PushRecord) Merged() ([]MergedItem, []PushConflict) {
	var order []string
	by := map[string][]MergedItem{}
	for _, run := range r.Runs() {
		ref := RunRef{Epoch: run.Epoch, Time: run.Written}
		for _, it := range run.Items {
			if _, ok := by[it.Branch]; !ok {
				order = append(order, it.Branch)
			}
			by[it.Branch] = append(by[it.Branch], MergedItem{PushRecordItem: it, LeaseFrom: ref, TipFrom: ref})
		}
	}
	var items []MergedItem
	var conflicts []PushConflict
	for _, b := range order {
		entries := by[b]
		if why := destinationsDiffer(entries); why != "" {
			conflicts = append(conflicts, PushConflict{Branch: b, Why: why})
			continue
		}
		oldest, merged := entries[0], entries[len(entries)-1]
		if slices.ContainsFunc(entries, func(e MergedItem) bool { return e.Lease != oldest.Lease }) {
			var leases []string
			for _, e := range entries {
				leases = append(leases, fmt.Sprintf("run %d over %s", e.TipFrom.Epoch, leaseText(e.Lease)))
			}
			merged.LeasesDiffer = strings.Join(leases, ", ")
		} else {
			merged.LeaseFrom = oldest.LeaseFrom
		}
		items = append(items, merged)
	}
	// The line's own branch goes last, as in each run.
	slices.SortStableFunc(items, func(a, b MergedItem) int {
		switch {
		case a.Top == b.Top:
			return 0
		case a.Top:
			return 1
		}
		return -1
	})
	return items, conflicts
}

// destinationsDiffer is why the runs' entries for one branch name more than
// one place to push it, or "".
func destinationsDiffer(entries []MergedItem) string {
	first := entries[0]
	for _, e := range entries[1:] {
		switch {
		case e.Remote != first.Remote || e.RemoteBranch != first.RemoteBranch:
			return fmt.Sprintf("run %d pushes it to %s/%s, run %d to %s/%s", first.TipFrom.Epoch, first.Remote, first.RemoteBranch, e.TipFrom.Epoch, e.Remote, e.RemoteBranch)
		case e.Endpoint != first.Endpoint:
			return fmt.Sprintf("%s pushed to %s when run %d took its lease and to %s for run %d", e.Remote, first.Endpoint, first.TipFrom.Epoch, e.Endpoint, e.TipFrom.Epoch)
		}
	}
	return ""
}

func leaseText(l string) string {
	if l == "" {
		return "none"
	}
	if len(l) > 7 {
		l = l[:7]
	}
	return l
}

// PushRecordItem is one branch of a later push: where it pushes, the lease,
// and the tip the run left it at, which the push only goes on while it is at.
type PushRecordItem struct {
	BranchPush
	New string `json:"new"`
	Top bool   `json:"top,omitempty"`
}

// PushLayer is what a run did with one layer: Result is the layer's move as
// the run ended it (MoveMoved, MoveUnchanged, MoveHeld), and Tip the commit
// it left the branch at: the new tip of a moved layer, the one it found for
// any other.
type PushLayer struct {
	Branch string `json:"branch"`
	Result string `json:"result"`
	Tip    string `json:"tip"`
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

// AddPushRun saves rec as the line's newest run, after the runs the line's
// record already holds, which it keeps, and returns the record as saved. A
// record of the same run (a resume) is replaced by it. The read and the write
// are one step under the records' flock.
func AddPushRun(mainRoot string, rec *PushRecord) (*PushRecord, error) {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cur, ok, err := ReadPushRecord(mainRoot, rec.Branch)
	if err != nil {
		return nil, err
	}
	runs := []PushRun{}
	if ok {
		runs = slices.DeleteFunc(cur.Runs(), func(r PushRun) bool { return r.Epoch == rec.Epoch })
	}
	merged := withRuns(rec.Branch, rec.Work, append(runs, rec.Run()))
	return merged, writePushRecord(mainRoot, merged)
}

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
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	rec.Rev = hex.EncodeToString(token[:])
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

// ForgetPushRecordLocked deletes the line's record under the flock, reading it
// first, and returns what it dropped; nil when the line had none. A record
// that cannot be read or decoded is deleted all the same (it is what blocks
// the line's next record), and unreadable says why it could not be read.
func ForgetPushRecordLocked(mainRoot, branch string) (rec *PushRecord, unreadable, err error) {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	rec, exists, unreadable, err := inspectPushRecord(mainRoot, branch)
	if err != nil || !exists {
		return nil, nil, err
	}
	return rec, unreadable, forgetPushRecord(mainRoot, branch)
}

// InspectPushRecord is what ForgetPushRecordLocked would drop, and changes
// nothing: the record, or why the line's record file cannot be read; both nil
// when the line has none.
func InspectPushRecord(mainRoot, branch string) (rec *PushRecord, unreadable, err error) {
	rec, _, unreadable, err = inspectPushRecord(mainRoot, branch)
	return rec, unreadable, err
}

func inspectPushRecord(mainRoot, branch string) (rec *PushRecord, exists bool, unreadable, err error) {
	path, err := pushRecordPath(mainRoot, branch)
	if err != nil {
		return nil, false, nil, err
	}
	b, rerr := os.ReadFile(path)
	if os.IsNotExist(rerr) {
		return nil, false, nil, nil
	}
	if rerr != nil {
		return nil, true, rerr, nil
	}
	var r PushRecord
	if derr := json.Unmarshal(b, &r); derr != nil {
		return nil, true, fmt.Errorf("%s: %w", path, derr), nil
	}
	return &r, true, nil, nil
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

// ForgetPushRecordOfRun takes the run of epoch out of the record it left,
// whatever line it names: undo puts the line back, and the tips that run's
// entries name are gone. The runs before it stay recorded;
// a record left with no run is deleted. A record of a later run is left.
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
		var rec PushRecord
		if json.Unmarshal(b, &rec) != nil {
			continue
		}
		runs := rec.Runs()
		rest := slices.DeleteFunc(slices.Clone(runs), func(r PushRun) bool { return r.Epoch == epoch })
		if len(rest) == len(runs) {
			continue
		}
		next := withRuns(rec.Branch, rec.Work, rest)
		if next == nil {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := writePushRecord(mainRoot, next); err != nil {
			return err
		}
	}
	return nil
}

// ErrPushRecordReplaced says a record is no longer the one a push was made
// from or a run wrote: a later run replaced it, or a push took it.
var ErrPushRecordReplaced = errors.New("the line's push record is no longer the one pushed from; it is left as it is")

// PushRecordStands is nil while the line's record is still rec, read under
// the flock, and ErrPushRecordReplaced once it is not.
func PushRecordStands(mainRoot string, rec *PushRecord) error {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return err
	}
	defer unlock()
	return stillRecorded(mainRoot, rec)
}

// stillRecorded is PushRecordStands for a caller that holds the flock: the
// same run, the same time and the same revision.
func stillRecorded(mainRoot string, rec *PushRecord) error {
	cur, ok, err := ReadPushRecord(mainRoot, rec.Branch)
	if err != nil {
		return err
	}
	if !ok || cur.Rev != rec.Rev || cur.Epoch != rec.Epoch || !cur.Written.Equal(rec.Written) {
		return ErrPushRecordReplaced
	}
	return nil
}

// ReplacePushRecord replaces from, the record a push was made from, with to
// (nil deletes it), and only while the line's record is still from: the same
// run, the same time and the same revision. Any other is left, and an error
// says so.
func ReplacePushRecord(mainRoot string, from, to *PushRecord) error {
	unlock, err := lockPushRecords(mainRoot)
	if err != nil {
		return err
	}
	defer unlock()
	if err := stillRecorded(mainRoot, from); err != nil {
		return err
	}
	pushRecordBeforeChange()
	if to == nil {
		return forgetPushRecord(mainRoot, from.Branch)
	}
	return writePushRecord(mainRoot, to)
}
