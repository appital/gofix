package gofix

import (
	"context"
	"errors"
	"sync/atomic"
)

// errAlreadyStarted is returned by Start when it has already been called.
var errAlreadyStarted = errors.New("already started")

// lifecycle tracks whether an Acceptor or Initiator has been started and
// whether it has finished shutting down. Its zero value is ready to use.
type lifecycle struct {
	started atomic.Bool
	// stopped is set once a Start succeeds, and its channel is closed once
	// shutdown has finished.
	stopped atomic.Pointer[chan struct{}]
}

// begin is called at the start of Start. It returns ctx's error if ctx is
// already done, and errAlreadyStarted if Start has been called before.
func (l *lifecycle) begin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.started.CompareAndSwap(false, true) {
		return errAlreadyStarted
	}
	return nil
}

// abort is called when Start fails after begin, once nothing is left
// running, so that Start may be called again.
func (l *lifecycle) abort() {
	l.started.Store(false)
}

// run is called when Start has succeeded. It calls shutdown once ctx is done,
// and then releases Wait.
func (l *lifecycle) run(ctx context.Context, shutdown func()) {
	stopped := make(chan struct{})
	l.stopped.Store(&stopped)
	go func() {
		<-ctx.Done()
		shutdown()
		close(stopped)
	}()
}

// wait blocks until shutdown has finished. It returns immediately if Start
// has not succeeded.
func (l *lifecycle) wait() {
	if stopped := l.stopped.Load(); stopped != nil {
		<-*stopped
	}
}
