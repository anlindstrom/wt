package wtsync

import (
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/gittest"
)

// A record a run saves while another run is settling the line's older record
// is kept: the settle's check and its delete are one step against every other
// write of the line's records.
func TestReplacePushRecordKeepsARecordSavedWhileItSettles(t *testing.T) {
	dir := gittest.NewRepo(t, t.TempDir(), "r")
	older := &PushRecord{Branch: "tmp/stack", Epoch: 1, Written: time.Unix(100, 0).UTC()}
	newer := &PushRecord{Branch: "tmp/stack", Epoch: 2, Written: time.Unix(200, 0).UTC()}
	if err := WritePushRecord(dir, older); err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	pushRecordBeforeChange = func() {
		go func() { saved <- WritePushRecord(dir, newer) }()
		time.Sleep(300 * time.Millisecond) // long enough for a write that does not wait
	}
	t.Cleanup(func() { pushRecordBeforeChange = func() {} })
	if err := ReplacePushRecord(dir, older, nil); err != nil {
		t.Fatalf("ReplacePushRecord: %v", err)
	}
	if err := <-saved; err != nil {
		t.Fatalf("the newer record's write: %v", err)
	}
	got, ok, err := ReadPushRecord(dir, "tmp/stack")
	if err != nil || !ok || got.Epoch != newer.Epoch {
		t.Fatalf("the newer run's record is lost: ok %v, record %+v, err %v", ok, got, err)
	}
}
