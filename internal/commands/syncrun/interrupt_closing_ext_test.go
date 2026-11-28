package syncrun_test

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anlindstrom/wt/internal/commands/syncrun"
	"github.com/anlindstrom/wt/internal/commands/testkit"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// holdingWriter holds the signal handler in its first line, after it has
// removed the temporary directories, while the run asks for another one.
type holdingWriter struct{ swept, asked chan struct{} }

func (w holdingWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "interrupted") {
		w.swept <- struct{}{}
		<-w.asked
	}
	return len(p), nil
}

// Once a signal has taken the temporary directories, none is made: it would
// outlive the process.
func TestNoTemporaryDirectoryAfterAnInterruptRemovedThem(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("interrupts a process with a signal")
	}
	if os.Getenv(testkit.InterruptChild) == "" {
		out, status := testkit.InterruptedChild(t)
		if status != 128+int(syscall.SIGINT) || strings.Contains(out, "allocated") {
			t.Errorf("status %d:\n%s", status, out)
		}
		return
	}
	w := holdingWriter{swept: make(chan struct{}), asked: make(chan struct{})}
	defer syncrun.WatchSignals(w, nil)()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	<-w.swept
	made := make(chan struct{})
	go func() {
		// Merge3 works in a temporary directory of its own.
		_, _ = wtsync.Merge3(wtsync.Conflict{Path: "a", Branch: []byte("a\n"), Base: []byte("a\n"), Trunk: []byte("a\n")})
		close(made)
	}()
	select {
	case <-made:
		fmt.Println("allocated")
	case <-time.After(time.Second):
	}
	close(w.asked)
	select {}
}
