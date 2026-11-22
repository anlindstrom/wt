package syncfix

import (
	"errors"
	"io"

	"github.com/anlindstrom/wt/internal/commands/kit"
	"github.com/anlindstrom/wt/internal/commands/syncrun"
)

// PushLaterOptions tunes SyncPushLater.
type PushLaterOptions struct {
	syncrun.PushOptions
	// DryRun says what would be pushed and moves nothing.
	DryRun  bool
	Journal *syncrun.RunJournal
}

// SyncPushLater is wt sync push <work> --stack.
func SyncPushLater(ctx *kit.Context, work string, opts PushLaterOptions, w io.Writer) error {
	return errors.New("not implemented")
}
