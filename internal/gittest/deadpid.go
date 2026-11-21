package gittest

import (
	"os/exec"
	"testing"
)

// DeadPID is the pid of a process that has ended, for a lock or a pointer
// whose writer is gone. A test that starts few processes meanwhile is the
// less likely to see the pid reused.
func DeadPID(t testing.TB) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running true: %v", err)
	}
	return cmd.Process.Pid
}
