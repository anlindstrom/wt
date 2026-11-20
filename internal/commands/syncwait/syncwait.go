// Package syncwait is wt sync wait: it follows the log of the run in a
// worktree until the run ends, and exits with the run's own code.
//
// It only reads. It takes, breaks and renews no lock and writes and removes
// no file, so a wait can never change what a run does.
package syncwait

import (
	"errors"
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
	// Running says whether the process pid, started no later than notAfter,
	// still runs; nil is wtsync.ProcessRunning. Tests inject a slow or a
	// scripted one.
	Running func(pid int, notAfter time.Time) bool
	// Resolve finds the worktree's own git dir and the name to call it by;
	// nil is the kit's lookup of <work> in the repository. Tests inject a
	// slow one.
	Resolve func(target string) (gitDir, name string, err error)
}

// Wait follows the log of the latest run to take the lock of the worktree
// work names ("." for the one the caller is in), printing it to w. It returns
// nil when the run ended 0, kit.ExitError with the run's code when it did not
// and TimeoutStatus when the timeout came first, and an error for the cases
// where there is no run it can follow.
func Wait(ctx *kit.Context, work string, opts Options, w io.Writer) error {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	// The timeout bounds the whole wait, resolving the worktree included.
	started := opts.Now()
	target := work
	if target == "" {
		target = "."
	}
	resolve := opts.Resolve
	if resolve == nil {
		resolve = func(target string) (string, string, error) {
			wt, err := kit.Locate(ctx, target)
			if err != nil {
				return "", "", err
			}
			gitDir, err := wtsync.GitDir(wt.Path)
			if err != nil {
				return "", "", err
			}
			return gitDir, kit.WorktreeName(ctx, wt.Branch, wt.Path), nil
		}
	}
	// Resolving runs git, so it is inside the timeout too: a lookup still
	// going when the deadline comes is given up on.
	type resolved struct {
		gitDir, name string
		err          error
	}
	res := make(chan resolved, 1)
	go func() {
		g, n, err := resolve(target)
		res <- resolved{g, n, err}
	}()
	var r resolved
	if opts.Timeout > 0 {
		select {
		case r = <-res:
		case <-time.After(max(opts.Timeout-opts.Now().Sub(started), 0)):
			fmt.Fprintf(w, "… still running after %s; %s again to keep waiting\n", opts.TimeoutLabel, againLine(work))
			return kit.ExitError{Code: TimeoutStatus}
		}
	} else {
		r = <-res
	}
	if r.err != nil {
		return r.err
	}
	gitDir, name := r.gitDir, r.name
	return follow(gitDir, name, work, opts, started, w)
}

// follow is Wait once the worktree is resolved: gitDir is its own git dir,
// name what the person called it and started when the wait began. The timeout
// bounds all of it: reading and printing the log, every probe and every sleep.
func follow(gitDir, name, work string, opts Options, started time.Time, w io.Writer) error {
	if opts.Poll <= 0 {
		opts.Poll = 500 * time.Millisecond
	}
	if opts.Quiet <= 0 {
		opts.Quiet = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Running == nil {
		opts.Running = wtsync.ProcessRunning
	}
	now := opts.Now
	var deadline time.Time
	if opts.Timeout > 0 {
		deadline = started.Add(opts.Timeout)
	}
	// left is the time until the deadline; ok is false once it has passed.
	left := func() (time.Duration, bool) {
		if deadline.IsZero() {
			return time.Hour, true
		}
		d := deadline.Sub(now())
		return d, d > 0
	}
	// probe is opts.Running within the time left: a probe still going when
	// the deadline comes is given up on, and the wait ends at 124.
	var probeTimedOut bool
	probe := func(pid int, notAfter time.Time) bool {
		d, ok := left()
		if !ok {
			probeTimedOut = true
			return true
		}
		res := make(chan bool, 1)
		go func() { res <- opts.Running(pid, notAfter) }()
		if deadline.IsZero() {
			return <-res
		}
		select {
		case r := <-res:
			return r
		case <-time.After(d):
			probeTimedOut = true
			return true
		}
	}

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

	// timedOut ends a wait whose deadline passed: with 124, unless the run
	// has ended, whose code wins however late the replay of its log was. An
	// end file that cannot be read is said, as a poll says it, and a newer
	// run holding the worktree is said before any end of this one is taken.
	timedOut := func() error {
		code, ended, err := runlog.ReadEnd(ptr.Log)
		if err != nil {
			return err
		}
		if ended {
			if err := newerRun(gitDir, ptr, name, func() bool { return probe(ptr.PID, ptr.Started()) }, probe); err != nil {
				return err
			}
			return finish(w, gitDir, work, code)
		}
		fmt.Fprintf(w, "… still running after %s; %s again to keep waiting\n", opts.TimeoutLabel, againLine(work))
		return kit.ExitError{Code: TimeoutStatus}
	}

	lastGrew := started
	buf := make([]byte, 32*1024)
	errTimeout := errors.New("timeout")
	// drain prints what the log has gained since the last call, until the
	// deadline.
	drain := func() error {
		for {
			if _, ok := left(); !ok {
				return errTimeout
			}
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
	// stop turns what ended a step into the wait's result.
	stop := func(err error) error {
		if errors.Is(err, errTimeout) || probeTimedOut {
			return timedOut()
		}
		return err
	}

	for {
		// The writer's liveness is asked once per poll.
		var alive, asked bool
		writerRunning := func() bool {
			if !asked {
				alive, asked = probe(ptr.PID, ptr.Started()), true
			}
			return alive
		}
		if err := drain(); err != nil {
			return stop(err)
		}
		err := newerRun(gitDir, ptr, name, writerRunning, probe)
		if probeTimedOut {
			return timedOut()
		}
		if err != nil {
			return err
		}
		code, ended, err := runlog.ReadEnd(ptr.Log)
		if err != nil {
			return err
		}
		if ended {
			// Everything the run printed was written before its end file.
			if err := drain(); err != nil {
				return stop(err)
			}
			return finish(w, gitDir, work, code)
		}
		if !writerRunning() {
			if probeTimedOut {
				return timedOut()
			}
			// It may have ended between the two looks above.
			if err := drain(); err != nil {
				return stop(err)
			}
			if code, ended, err := runlog.ReadEnd(ptr.Log); err != nil {
				return err
			} else if ended {
				return finish(w, gitDir, work, code)
			}
			return fmt.Errorf("the run in %s ended without saying how (killed, a crash or a racing signal); wt sync doctor shows what it left", name)
		}
		if probeTimedOut {
			return timedOut()
		}
		d, ok := left()
		if !ok {
			return timedOut()
		}
		if now().Sub(lastGrew) >= opts.Quiet {
			fmt.Fprintf(w, "… still running (%dm)\n", int(now().Sub(ptr.Started())/time.Minute))
			lastGrew = now()
		}
		time.Sleep(min(opts.Poll, d))
	}
}

// againLine is the command that waits again.
func againLine(work string) string {
	if work == "" {
		return "wt sync wait"
	}
	return "wt sync wait " + work
}

// newerRun is the error for a worktree that a run this wait cannot follow
// holds now: a lock taken after the log's run began by a process other than
// the log's writer, that still runs. A lock with the writer's pid is the
// writer's own only while that pid still names the writer's process (started
// when the pointer says); otherwise the pid was reused.
func newerRun(gitDir string, ptr runlog.Pointer, name string, writerRunning func() bool, probe func(int, time.Time) bool) error {
	lock, ok, err := wtsync.ReadLock(gitDir)
	if err != nil || !ok {
		return nil //nolint:nilerr // a lock that cannot be read is not a reason to stop following the log
	}
	if lock.Started.Before(ptr.Started()) {
		return nil
	}
	if lock.PID == ptr.PID && writerRunning() {
		return nil
	}
	if !probe(lock.PID, lock.Started) {
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
