package run

import (
	"context"
	"fmt"
	"io"
)

// Follows says whether args, atm run's command line, ask for --follow.
func Follows(args []string) bool {
	_, _, _, a, err := parseArgs(args)
	return err == nil && a.follow
}

// StartFollowing starts a run and copies its events as JSON lines until it ends.
// Closing the stream leaves the run going. No terminal or external monitor is needed.
func StartFollowing(ctx context.Context, root string, args []string, w io.Writer) error {
	o, err := start(root, args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := make(chan Input)
	stop := context.AfterFunc(ctx, func() { close(in) })
	defer stop()
	var writeErr error
	end, err := Follow(root, o.Run, nil, in, func(f Frame) {
		if f.Event != nil && writeErr == nil {
			_, writeErr = fmt.Fprintln(w, string(f.Event))
			if writeErr != nil {
				cancel()
			}
		}
	})
	switch {
	case writeErr != nil:
		return writeErr
	case ctx.Err() != nil:
		return exitError{1, ctx.Err()}
	case err != nil:
		return err
	default:
		return end.Err()
	}
}
