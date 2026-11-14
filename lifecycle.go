package gofix

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	// errAlreadyRun is returned by Run when it is already running or has run.
	errAlreadyRun = errors.New("already run")
	// errReloadBeforeRun is returned by Reload before Run has started the
	// engine, which loads the settings itself, including after a Run that
	// failed to start it.
	errReloadBeforeRun = errors.New("cannot reload until Run has started the engine")
	// errReloadAfterRun is returned by Reload once Run has begun to shut
	// down.
	errReloadAfterRun = errors.New("cannot reload after Run has stopped")
)

// runState is where an Acceptor or Initiator is in its life.
type runState int32

const (
	// runNotStarted is the state before Run is called, and after Run has
	// failed to start the engine, when Run may be called again.
	runNotStarted runState = iota
	// runStarting is the state while Run is starting the engine.
	runStarting
	// runStarted is the state once Run has started the engine, and Reload may
	// change it.
	runStarted
	// runFinished is the state once Run has begun to shut down. The engine
	// cannot be run again.
	runFinished
)

// lifecycle runs an Acceptor or an Initiator, and lets Reload change it only
// while it is running. Its zero value is ready to use.
//
// Run starts the engine without holding reloadMu, so that a Reload called
// meanwhile returns at once rather than waiting for Run's SettingsSource and
// Application callbacks. Reload therefore reads the state before it takes
// reloadMu, and again once it holds it.
type lifecycle struct {
	state atomic.Int32
	// reloadMu serialises Reload, and keeps it from running while Run stops
	// the engine. Once Run has started the engine, it guards what Reload
	// changes.
	reloadMu  sync.Mutex
	readyOnce sync.Once
	ready     chan struct{}
}

func (l *lifecycle) load() runState { return runState(l.state.Load()) }

func (l *lifecycle) store(state runState) { l.state.Store(int32(state)) }

// readyChan returns the channel Ready returns, which run closes once it has
// started the engine.
func (l *lifecycle) readyChan() chan struct{} {
	l.readyOnce.Do(func() { l.ready = make(chan struct{}) })
	return l.ready
}

// run is Run. It returns ctx's error if ctx is already done, and errAlreadyRun
// if Run is running or has run. Otherwise it calls start, which starts the
// engine and adds to created each session that calls OnCreate as it starts,
// and once those have, closes Ready. Once ctx is done it calls shutdown and
// returns its error.
//
// If start fails, nothing must be left running, so that Run may be called
// again. run then returns ctx's error if ctx is done, since Run was stopped
// rather than failing, whatever error stopping it caused, and otherwise
// start's error.
func (l *lifecycle) run(ctx context.Context, start func(context.Context, *sync.WaitGroup) error, shutdown func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.state.CompareAndSwap(int32(runNotStarted), int32(runStarting)) {
		return errAlreadyRun
	}

	var created sync.WaitGroup
	if err := start(ctx, &created); err != nil {
		l.store(runNotStarted)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	created.Wait()
	l.store(runStarted)
	close(l.readyChan())

	<-ctx.Done()
	l.reloadMu.Lock()
	l.store(runFinished)
	l.reloadMu.Unlock()
	return shutdown()
}

// reload is Reload. It returns an error unless Run has started the engine
// and not begun to shut it down. Otherwise it calls apply holding reloadMu.
// apply adds to created each session it starts that calls OnCreate as it
// starts, and reload returns once those have. They call OnCreate once reloadMu
// is released, so that OnCreate may call Reload.
func (l *lifecycle) reload(ctx context.Context, apply func(context.Context, *sync.WaitGroup) error) error {
	if err := l.reloadErr(); err != nil {
		return err
	}
	var created sync.WaitGroup
	err := l.reloadLocked(ctx, &created, apply)
	created.Wait()
	return err
}

func (l *lifecycle) reloadLocked(ctx context.Context, created *sync.WaitGroup, apply func(context.Context, *sync.WaitGroup) error) error {
	l.reloadMu.Lock()
	defer l.reloadMu.Unlock()
	// Run may have begun to shut down while Reload waited.
	if err := l.reloadErr(); err != nil {
		return err
	}
	return apply(ctx, created)
}

// reloadErr returns the error Reload returns, if any, in the engine's current
// state.
func (l *lifecycle) reloadErr() error {
	switch l.load() {
	case runNotStarted, runStarting:
		return errReloadBeforeRun
	case runFinished:
		return errReloadAfterRun
	}
	return nil
}
