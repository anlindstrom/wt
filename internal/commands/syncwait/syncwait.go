// Package syncwait is wt sync wait: it follows the log of the run in a
// worktree until the run ends, and exits with the run's own code.
//
// It only reads. It takes, breaks and renews no lock and writes and removes
// no file, so a wait can never change what a run does.
package syncwait

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/runlog"
	"github.com/anlindstrom/wt/internal/wtsync"
)

// TimeoutStatus is the exit code of a wait whose --timeout passed with the
// run still going, as timeout(1) exits.
const TimeoutStatus = 124

// Options are what a wait is given.
type Options struct {
	// Timeout bounds the whole wait; 0 waits for as long as it takes.
	// TimeoutLabel is how it was spelled, for the line that says it passed.
	Timeout      time.Duration
	TimeoutLabel string
	// Poll is how often the log is read; zero is half a second.
	Poll time.Duration
	// Quiet is how long the log may stay still before a line says the run
	// is still going; zero is a minute.
	Quiet time.Duration
	// Now is the clock; nil is the real one.
	Now func() time.Time
}

// Wait follows the log of the latest run to take the lock of the worktree
// work names ("." for the one the caller is in), printing it to w. It returns
// nil when the run ended 0, kit.ExitError with the run's code when it did not
// and TimeoutStatus when the timeout came first, and an error for the cases
// where there is no run it can follow.
func Wait(ctx *kit.Context, work string, opts Options, w io.Writer) error {
	if opts.Poll <= 0 {
		opts.Poll = 500 * time.Millisecond
	}
	if opts.Quiet <= 0 {
		opts.Quiet = time.Minute
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	target := work
	if target == "" {
		target = "."
	}
	wt, err := kit.Locate(ctx, target)
	if err != nil {
		return err
	}
	gitDir, err := wtsync.GitDir(wt.Path)
	if err != nil {
		return err
	}
	name := kit.WorktreeName(ctx, wt.Branch, wt.Path)
	ptr, ok, err := runlog.ReadPointer(gitDir)
	if err != nil {
		return fmt.Errorf("cannot read the run pointer of %s: %w", name, err)
	}
	if !ok {
		return fmt.Errorf("no run has been seen in %s", name)
	}
	f, err := os.Open(ptr.Log) //nolint:gosec // G304: the log a run of wt wrote, named by its pointer
	if err != nil {
		return fmt.Errorf("cannot read the run log %s: %w", ptr.Log, err)
	}
	defer f.Close() //nolint:errcheck // read only

	started := now()
	lastGrew := started
	buf := make([]byte, 32*1024)
	// drain prints what the log has gained since the last call.
	drain := func() error {
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return werr
				}
				lastGrew = now()
			}
			if rerr == io.EOF || n == 0 && rerr == nil {
				return nil
			}
			if rerr != nil {
				return fmt.Errorf("cannot read the run log %s: %w", ptr.Log, rerr)
			}
		}
	}
	again := "wt sync wait"
	if work != "" {
		again += " " + work
	}

	for {
		if err := drain(); err != nil {
			return err
		}
		if err := newerRun(gitDir, ptr, name); err != nil {
			return err
		}
		code, ended, err := runlog.ReadEnd(ptr.Log)
		if err != nil {
			return err
		}
		if ended {
			// Everything the run printed was written before its end file.
			if err := drain(); err != nil {
				return err
			}
			return finish(w, gitDir, work, code)
		}
		if !wtsync.ProcessRunning(ptr.PID, ptr.Started()) {
			// It may have ended between the two looks above.
			if err := drain(); err != nil {
				return err
			}
			if code, ended, err := runlog.ReadEnd(ptr.Log); err != nil {
				return err
			} else if ended {
				return finish(w, gitDir, work, code)
			}
			return fmt.Errorf("the run in %s ended without saying how (killed, a crash or a racing signal); wt sync doctor shows what it left", name)
		}
		if opts.Timeout > 0 && now().Sub(started) >= opts.Timeout {
			fmt.Fprintf(w, "… still running after %s; %s again to keep waiting\n", opts.TimeoutLabel, again)
			return kit.ExitError{Code: TimeoutStatus}
		}
		if now().Sub(lastGrew) >= opts.Quiet {
			fmt.Fprintf(w, "… still running (%dm)\n", int(now().Sub(ptr.Started())/time.Minute))
			lastGrew = now()
		}
		time.Sleep(opts.Poll)
	}
}

// newerRun is the error for a worktree that a run this wait cannot follow
// holds now: a process other than the log's writer that took the lock after
// the writer started and still runs.
func newerRun(gitDir string, ptr runlog.Pointer, name string) error {
	lock, ok, err := wtsync.ReadLock(gitDir)
	if err != nil || !ok {
		return nil //nolint:nilerr // a lock that cannot be read is not a reason to stop following the log
	}
	if lock.PID == ptr.PID || lock.Started.Before(ptr.Started()) || !wtsync.ProcessRunning(lock.PID, lock.Started) {
		return nil
	}
	return fmt.Errorf("a newer run (pid %d) holds %s that wt sync wait cannot follow: its log could not be written, or it rebases across repositories", lock.PID, name)
}

// finish ends the wait with the run's code, pointing a failed run's reader at
// resume when the worktree has a plan.
func finish(w io.Writer, gitDir, work string, code int) error {
	if code == 0 {
		return nil
	}
	if _, err := os.Stat(wtsync.PlanPath(gitDir)); err == nil {
		target := work
		if target == "" {
			target = "."
		}
		fmt.Fprintf(w, "wt sync resume %s continues what the run handed over\n", target)
	}
	return kit.ExitError{Code: code}
}
