package wtsync

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/gittest"
)

func item(branch, lease, tip string) PushRecordItem {
	return PushRecordItem{BranchPush: BranchPush{Branch: branch, Remote: "origin", RemoteBranch: branch, Lease: lease, Endpoint: "u", Rule: "its own name"}, New: tip}
}

func run(epoch int64, items ...PushRecordItem) *PushRecord {
	return &PushRecord{Branch: "line", Work: "line", Epoch: epoch, Written: time.Unix(epoch, 0).UTC(), Items: items}
}

func TestMergedTakesTheOldestLeaseAndTheNewestTip(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	a := run(1, item("s1", "l1", "a1"), item("s2", "l2", "a2"), item("line", "l3", "a3"))
	b := run(2, item("s2", "l2", "b2"), item("line", "l3", "b3"))
	if err := WritePushRecord(dir, a); err != nil {
		t.Fatal(err)
	}
	rec, err := AddPushRun(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	items, conflicts := rec.Merged()
	if len(conflicts) != 0 {
		t.Fatalf("conflicts %+v", conflicts)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Branch+" "+it.Lease+"→"+it.New)
	}
	if want := "s1 l1→a1, s2 l2→b2, line l3→b3"; strings.Join(got, ", ") != want {
		t.Errorf("merged %q, want %q", strings.Join(got, ", "), want)
	}
	if items[0].TipFrom.Epoch != 1 || items[1].TipFrom.Epoch != 2 || items[1].LeaseFrom.Epoch != 1 {
		t.Errorf("runs named wrongly: %+v", items)
	}
	// What is read back is the same.
	back, ok, err := ReadPushRecord(dir, "line")
	if err != nil || !ok || len(back.Runs()) != 2 || back.Epoch != 2 {
		t.Fatalf("read back %+v ok %v err %v", back, ok, err)
	}
}

func TestMergedRefusesWhatTheRunsDisagreeOn(t *testing.T) {
	for name, mutate := range map[string]func(*PushRecordItem){
		"remote":   func(i *PushRecordItem) { i.RemoteBranch = "elsewhere" },
		"endpoint": func(i *PushRecordItem) { i.Endpoint = "v" },
	} {
		t.Run(name, func(t *testing.T) {
			second := item("s2", "l2", "b2")
			mutate(&second)
			rec := withRuns("line", "line", []PushRun{run(1, item("s1", "l1", "a1"), item("s2", "l2", "a2")).Run(), run(2, second).Run()})
			items, conflicts := rec.Merged()
			if len(conflicts) != 1 || conflicts[0].Branch != "s2" || conflicts[0].Why == "" {
				t.Fatalf("conflicts %+v, want s2 alone", conflicts)
			}
			if len(items) != 1 || items[0].Branch != "s1" {
				t.Errorf("items %+v, want s1 alone", items)
			}
		})
	}
}

// Leases that differ are not a conflict: the branch goes over the newest
// run's lease, which the push has to find the remote at.
func TestMergedTakesTheNewestLeaseWhenTheLeasesDiffer(t *testing.T) {
	rec := withRuns("line", "line", []PushRun{run(1, item("s1", "l0", "a1")).Run(), run(2, item("s1", "l1", "b1")).Run()})
	items, conflicts := rec.Merged()
	if len(conflicts) != 0 || len(items) != 1 || items[0].Lease != "l1" || items[0].LeaseFrom.Epoch != 2 || items[0].New != "b1" {
		t.Errorf("items %+v conflicts %+v, want s1 over run 2's lease l1 to b1", items, conflicts)
	}
}

// A lease that differs in one run does not hide a destination that differs in
// a later one.
func TestMergedRefusesAChangedDestinationBehindAChangedLease(t *testing.T) {
	third := item("s1", "l1", "c1")
	third.RemoteBranch = "elsewhere"
	rec := withRuns("line", "line", []PushRun{run(1, item("s1", "l0", "a1")).Run(), run(2, item("s1", "l1", "b1")).Run(), run(3, third).Run()})
	items, conflicts := rec.Merged()
	if len(items) != 0 || len(conflicts) != 1 || !strings.Contains(conflicts[0].Why, "origin/elsewhere") {
		t.Errorf("items %+v conflicts %+v, want s1 refused for its destination", items, conflicts)
	}
}

// A newer run that left a layer out does not drop the older run's entry.
func TestLeftOutOfANewerRunKeepsTheOlderEntry(t *testing.T) {
	older := run(1, item("s1", "l1", "a1"), item("line", "l3", "a3"))
	newer := run(2, item("line", "l3", "b3"))
	newer.Left = []PushLeft{{Branch: "s1", Tip: "x", Why: "held: step"}}
	rec := withRuns("line", "line", []PushRun{older.Run(), newer.Run()})
	items, _ := rec.Merged()
	if len(items) != 2 || items[0].Branch != "s1" || items[0].New != "a1" {
		t.Fatalf("items %+v, want s1's entry kept", items)
	}
	if left := rec.LeftOut(); len(left) != 0 {
		t.Errorf("s1 is moved by a run, so not left out: %+v", left)
	}
}

func TestAddPushRunReplacesTheSameRun(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	if _, err := AddPushRun(dir, run(1, item("s1", "l1", "a1"))); err != nil {
		t.Fatal(err)
	}
	rec, err := AddPushRun(dir, run(1, item("s1", "l1", "a1b")))
	if err != nil || len(rec.Runs()) != 1 || rec.Items[0].New != "a1b" {
		t.Fatalf("record %+v err %v, want one run, the resumed one", rec, err)
	}
}

func TestForgetPushRecordOfRunTakesOnlyThatRun(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	for _, r := range []*PushRecord{run(1, item("s1", "l1", "a1")), run(2, item("s1", "l1", "b1"))} {
		if _, err := AddPushRun(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := ForgetPushRecordOfRun(dir, 2); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := ReadPushRecord(dir, "line")
	if err != nil || !ok || len(rec.Runs()) != 1 || rec.Epoch != 1 || rec.Items[0].New != "a1" || len(rec.Earlier) != 0 {
		t.Fatalf("record %+v ok %v err %v, want run 1 alone, as its own fields", rec, ok, err)
	}
	if err := ForgetPushRecordOfRun(dir, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadPushRecord(dir, "line"); ok {
		t.Error("the record of no run is kept")
	}
}

func TestWithoutTakesASentBranchOutOfEveryRun(t *testing.T) {
	rec := withRuns("line", "line", []PushRun{run(1, item("s1", "l", "a"), item("s2", "l", "a")).Run(), run(2, item("s2", "l", "b")).Run()})
	left := rec.Without(map[string]bool{"s2": true})
	if left == nil || len(left.Runs()) != 1 || left.Epoch != 1 || len(left.Items) != 1 || left.Items[0].Branch != "s1" {
		t.Fatalf("left %+v, want run 1 with s1", left)
	}
	if rec.Without(map[string]bool{"s1": true, "s2": true}) != nil {
		t.Error("everything sent leaves a record")
	}
}

// A record as ce3bfb4 wrote it (one run, its fields at the top, no earlier
// runs) is one contribution.
func TestReadsTheOldSingleRunShape(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	old := `{"branch":"line","work":"line","epoch":7,"written":"2026-11-20T10:00:00Z","head":"h","base":"b","commits":2,"subject":"s",
	  "items":[{"branch":"s1","remote":"origin","remoteBranch":"s1","lease":"l1","endpoint":"u","new":"n1"},{"branch":"line","remote":"origin","remoteBranch":"line","lease":"l2","endpoint":"u","new":"n2","top":true}],
	  "left":[{"branch":"s3","tip":"t","why":"held: x"}]}`
	path, err := pushRecordPath(dir, "line")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(strings.TrimSuffix(path, "/line.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := ReadPushRecord(dir, "line")
	if err != nil || !ok {
		t.Fatalf("read: ok %v err %v", ok, err)
	}
	items, conflicts := rec.Merged()
	if len(rec.Runs()) != 1 || rec.Runs()[0].Epoch != 7 || len(items) != 2 || len(conflicts) != 0 || len(rec.LeftOut()) != 1 {
		t.Errorf("runs %d items %+v conflicts %+v left %+v", len(rec.Runs()), items, conflicts, rec.LeftOut())
	}
	// A single run is written back in that shape too.
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "earlier") {
		t.Errorf("a one-run record is not in the old shape: %s", b)
	}
	// And a later run adds to it.
	got, err := AddPushRun(dir, run(8, item("line", "l2", "m2")))
	if err != nil || len(got.Runs()) != 2 {
		t.Fatalf("add: %+v %v", got, err)
	}
}

// A push that sent an earlier-only layer rewrites the record while a newer
// run still waits to settle from its snapshot: the snapshot must not pass for
// the record, though the newest run's epoch and time are unchanged.
func TestReplacePushRecordRefusesAStaleSettleAfterAPartialUpdate(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	if _, err := AddPushRun(dir, run(1, item("s1", "l1", "a1"), item("line", "l3", "a3"))); err != nil {
		t.Fatal(err)
	}
	waiting, err := AddPushRun(dir, run(2, item("line", "l3", "b3")))
	if err != nil {
		t.Fatal(err)
	}
	// Another push, from its own read, sends s1 and leaves run 2's line.
	snap, _, err := ReadPushRecord(dir, "line")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplacePushRecord(dir, snap, snap.Without(map[string]bool{"s1": true})); err != nil {
		t.Fatal(err)
	}
	// The waiting run settles from its stale snapshot, which still names s1.
	if err := ReplacePushRecord(dir, waiting, waiting.Without(map[string]bool{"line": true})); !errors.Is(err, ErrPushRecordReplaced) {
		t.Fatalf("the stale settle: %v, want ErrPushRecordReplaced", err)
	}
	got, _, _ := ReadPushRecord(dir, "line")
	for _, r := range got.Runs() {
		for _, it := range r.Items {
			if it.Branch == "s1" {
				t.Errorf("s1, already sent, is back in the record: %+v", got)
			}
		}
	}
}

// The branches a record lists are layers first and the line last, whichever
// run named which first.
func TestBranchesPutTheLineLast(t *testing.T) {
	line := item("line", "l", "a")
	line.Top = true
	top := item("line", "l", "b")
	top.Top = true
	rec := withRuns("line", "line", []PushRun{run(1, line).Run(), run(2, item("s1", "l", "b"), top).Run()})
	if got := strings.Join(rec.Branches(), ","); got != "s1,line" {
		t.Errorf("branches %q, want s1,line", got)
	}
}
