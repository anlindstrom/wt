package synccmd

import (
	"errors"
	"io"

	"github.com/anlindstrom/wt/internal/commands/kit"
)

// MoveOptions is wt sync move: the source commit, the target and the paths.
type MoveOptions struct {
	From, Into string
	Paths      []string
}

// SyncMove writes the move! commit on work's line.
func SyncMove(ctx *kit.Context, work string, opts MoveOptions, w io.Writer) error {
	return errors.New("wt sync move is not built yet")
}
