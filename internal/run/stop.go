package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
)

// The operator stops a run before its delivery with atm stop, or x twice on its screen: its working agent and every
// process it started are killed, as is a check command running then, and the step running fails, so the run ends
// failed with errStopped and asks nothing. Every other run goes on.

var errStopped = errors.New("stopped by operator")

// stops is each run's context while the background process runs it, by the run's directory: it ends, with
// errStopped, when the operator stops the run.
var stops sync.Map

// runCtx is the context of the run in directory dir; one the background process does not run, as a test's,
// never ends.
func runCtx(dir string) context.Context {
	v, _ := stops.Load(dir)
	ctx, _ := v.(context.Context)
	return cmp.Or(ctx, context.Background())
}

// stopRun stops run label, which must be going before its delivery, and waits until it ends.
func (s *server) stopRun(label string) error {
	r := s.find(label)
	if r == nil {
		return errors.New("no run " + label)
	}
	s.Lock()
	if r.Outcome.Outcome != "running" || r.Step == "delivery" {
		s.Unlock()
		return fmt.Errorf("run %s is %s at %s: only a run going before its delivery stops", r.Run, r.Outcome.Outcome,
			r.Step)
	}
	r.stop(errStopped)
	for r.Ended.IsZero() {
		more := r.more
		s.Unlock()
		<-more
		s.Lock()
	}
	s.Unlock()
	return nil
}

// Stop stops run label of the repository at root before its delivery and waits until it ends.
func Stop(root, label string) error {
	return call(root, request{Cmd: "stop", Run: label}, nil, func(reply) {})
}
