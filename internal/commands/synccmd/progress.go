package synccmd

import (
	"fmt"
	"io"
	"time"

	"github.com/anlindstrom/wt/internal/wtsync"
)

// A replay's count is written every replayLines-th of the way, at least
// replayStep commits apart, and whenever replayQuiet has passed without one.
const (
	replayLines = 10
	replayStep  = 10
	replayQuiet = 5 * time.Second
)

// lookProgress is the lines a text look writes on SyncOptions.Progress while
// it works; the zero value, with no writer, is silent.
type lookProgress struct {
	w io.Writer
	// now is what the replay counts are timed by; nil is time.Now.
	now func() time.Time
}

// phase says what the look does next.
func (p lookProgress) phase(format string, a ...any) { p.line("▸ "+format, a...) }

func (p lookProgress) line(format string, a ...any) {
	if p.w != nil {
		fmt.Fprintf(p.w, format+"\n", a...)
	}
}

// replay counts the commits of a simulated rebase onto the ref named.
func (p lookProgress) replay(onto string) wtsync.ReplayProgress {
	now := p.now
	if now == nil {
		now = time.Now
	}
	var last time.Time
	return func(done, total int) {
		at := now()
		if done%max(total/replayLines, replayStep) != 0 && at.Sub(last) < replayQuiet {
			return
		}
		last = at
		p.phase("replaying onto %s %d/%d", onto, done, total)
	}
}
