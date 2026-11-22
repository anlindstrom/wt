package synccmd

import (
	"errors"
	"io"

	"github.com/anlindstrom/wt/internal/commands/kit"
)

// SyncPushToBranch is wt sync push-to --branch.
func SyncPushToBranch(ctx *kit.Context, branch, dest string, unset, dryRun bool, w io.Writer) error {
	return errors.New("not implemented")
}
