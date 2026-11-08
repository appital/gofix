package gofix

import (
	"context"
	"errors"
	"sync/atomic"
)

// errAlreadyRun is returned by Run when it is already running or has run.
var errAlreadyRun = errors.New("already run")

// lifecycle keeps an Acceptor or Initiator from being run more than once. Its
// zero value is ready to use.
type lifecycle struct {
	started atomic.Bool
}

// begin is called at the start of Run. It returns ctx's error if ctx is
// already done, and errAlreadyRun if Run is running or has run.
func (l *lifecycle) begin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.started.CompareAndSwap(false, true) {
		return errAlreadyRun
	}
	return nil
}

// abort is called when Run fails to start, once nothing is left running, so
// that Run may be called again.
func (l *lifecycle) abort() {
	l.started.Store(false)
}
