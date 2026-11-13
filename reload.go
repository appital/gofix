package gofix

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"

	"github.com/appital/gofix/config"
)

var (
	// errReloadBeforeRun is returned by Reload before Run has started the
	// engine, which loads the settings itself.
	errReloadBeforeRun = errors.New("cannot reload before Run has started")
	// errReloadAfterRun is returned by Reload once Run has begun to shut
	// down.
	errReloadAfterRun = errors.New("cannot reload after Run has stopped")
)

// runState is where an Acceptor or Initiator is in its life, as far as
// Reload is concerned.
type runState int

const (
	// runNotStarted is the state before Run has started the engine, or
	// after it failed to.
	runNotStarted runState = iota
	runStarted
	// runFinished is the state once Run has begun to shut down.
	runFinished
)

// acceptorEngineSettings are the global settings an Acceptor reads once,
// rather than for each session. Reload refuses to change them. A setting that
// is read by the acceptor as a whole must be added here.
var acceptorEngineSettings = []string{
	config.SocketAcceptHost,
	config.UseTCPProxy,
	config.MaxMessageSize,
	config.SocketReadTimeout,
	config.DynamicSessions,
	config.DynamicQualifier,
	config.RedactedTags,
	config.SocketUseSSL,
	config.SocketServerName,
	config.SocketInsecureSkipVerify,
	config.SocketPrivateKeyFile,
	config.SocketCertificateFile,
	config.SocketPrivateKeyBytes,
	config.SocketCertificateBytes,
	config.SocketCAFile,
	config.SocketCABytes,
	config.SocketMinimumTLSVersion,
}

// checkUnchanged returns an error naming the first of names whose value
// in next differs from its value in current, or that is set in only one of
// them.
func checkUnchanged(current, next *SessionSettings, names []string) error {
	for _, name := range names {
		cur, inCur := current.settings[name]
		nxt, inNext := next.settings[name]
		if inCur != inNext || !bytes.Equal(cur, nxt) {
			return fmt.Errorf("%s cannot be changed by Reload", name)
		}
	}
	return nil
}

// storeSettings are the settings the built-in MessageStoreFactory
// implementations read. A restarted session keeps the store of the session it
// replaces, so Reload refuses to change them for it.
var storeSettings = []string{
	config.FileStorePath,
	config.FileStoreSync,
	config.SQLStoreDriver,
	config.SQLStoreDataSourceName,
	config.SQLStoreConnMaxLifetime,
	config.SQLStoreMessagesTableName,
	config.SQLStoreSessionsTableName,
}

// sessionChanges is what Run or Reload must do to bring an engine's sessions
// in line with new settings. Each map holds the effective settings of the sessions
// that need starting, keyed as Settings keys them.
type sessionChanges struct {
	added   map[SessionID]*SessionSettings
	changed map[SessionID]*SessionSettings
	removed []SessionID
}

// diffSessions compares the effective session settings an engine is running,
// current, with next.
func diffSessions(current, next map[SessionID]*SessionSettings) sessionChanges {
	changes := sessionChanges{
		added:   make(map[SessionID]*SessionSettings),
		changed: make(map[SessionID]*SessionSettings),
	}
	for id, settings := range next {
		cur, ok := current[id]
		switch {
		case !ok:
			changes.added[id] = settings
		case !maps.EqualFunc(cur.settings, settings.settings, bytes.Equal):
			changes.changed[id] = settings
		}
	}
	for id := range current {
		if _, ok := next[id]; !ok {
			changes.removed = append(changes.removed, id)
		}
	}
	slices.SortFunc(changes.removed, func(a, b SessionID) int {
		return cmp.Compare(a.String(), b.String())
	})
	return changes
}

// starting yields the sessions Reload builds and starts: those it adds, then
// those it restarts.
func (c sessionChanges) starting() iter.Seq2[SessionID, *SessionSettings] {
	return func(yield func(SessionID, *SessionSettings) bool) {
		for sessionID, settings := range c.added {
			if !yield(sessionID, settings) {
				return
			}
		}
		for sessionID, settings := range c.changed {
			if !yield(sessionID, settings) {
				return
			}
		}
	}
}

// checkStoreSettings returns an error if changes restarts a session with
// store settings that differ from those in current, the settings of the
// running sessions.
func checkStoreSettings(current map[SessionID]*SessionSettings, changes sessionChanges) error {
	for sessionID, settings := range changes.changed {
		if err := checkUnchanged(current[sessionID], settings, storeSettings); err != nil {
			return fmt.Errorf("session %v: %w; remove the session and add it again to give it a new store", sessionID, err)
		}
	}
	return nil
}

// existingStoreFactory hands a session that is being restarted the store of
// the session it replaces, so that its sequence numbers carry over.
type existingStoreFactory struct {
	store MessageStore
}

func (f existingStoreFactory) Create(SessionID, *SessionSettings) (MessageStore, error) {
	return f.store, nil
}

// stoppingSession is a session Reload is waiting for. done is closed once it
// has stopped.
type stoppingSession struct {
	session *session
	done    <-chan struct{}
}

// waitStopped waits for each session in stopping to stop. Once ctx is done it
// stops those still running without waiting for their Logout to be
// acknowledged, waits for them, and returns ctx's error.
func waitStopped(ctx context.Context, stopping []stoppingSession) error {
	var err error
	for _, s := range stopping {
		select {
		case <-s.done:
			continue
		case <-ctx.Done():
			err = ctx.Err()
		}
		s.session.forceStop()
		<-s.done
	}
	return err
}

// closeLog closes log if it implements io.Closer.
func closeLog(log Log) error {
	if closer, ok := log.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// buildSessions builds, without registering or starting them, the sessions
// Reload adds and restarts. A restarted session takes over the store of the
// running session it replaces, which current returns. If building any session
// fails, buildSessions discards those it built and returns the error.
func (f sessionFactory) buildSessions(
	changes sessionChanges, current func(SessionID) *session,
	storeFactory MessageStoreFactory, logFactory LogFactory, app Application,
) (map[SessionID]*session, error) {
	built := make(map[SessionID]*session, len(changes.added)+len(changes.changed))
	for sessionID, settings := range changes.starting() {
		sessionStoreFactory := storeFactory
		if _, restarted := changes.changed[sessionID]; restarted {
			sessionStoreFactory = existingStoreFactory{current(sessionID).store}
		}
		s, err := f.newSession(sessionID, sessionStoreFactory, settings, logFactory, app)
		if err != nil {
			discardSessions(changes, built)
			return nil, fmt.Errorf("session %v: %w", sessionID, err)
		}
		built[sessionID] = s
	}
	return built, nil
}

// discardSessions closes the logs of the sessions in built, which Reload
// built but will not start, and the stores of those it was adding. A
// restarted session's store still belongs to the session it would have
// replaced.
func discardSessions(changes sessionChanges, built map[SessionID]*session) {
	for sessionID, s := range built {
		if _, added := changes.added[sessionID]; added {
			_ = s.store.Close()
		}
		_ = closeLog(s.log)
	}
}

// registerAdded registers the sessions Reload adds, from built, before Reload
// stops anything. Once they are registered, neither a dynamic session nor
// another engine sharing the registry can take their SessionIDs and make
// Reload fail after it can no longer leave the engine as it was. Their
// applications are told they have been created once Reload has applied the
// change. If one cannot be registered, registerAdded unregisters the others
// and returns the error.
func registerAdded(reg *Registry, changes sessionChanges, built map[SessionID]*session) error {
	added := make([]*session, 0, len(changes.added))
	for sessionID := range changes.added {
		added = append(added, built[sessionID])
	}
	for n, s := range added {
		if err := reg.register(s); err != nil {
			for _, registered := range added[:n] {
				_ = reg.UnregisterSession(registered.sessionID)
			}
			return fmt.Errorf("session %v: %w", s.sessionID, err)
		}
	}
	return nil
}

// registerChanged registers the sessions Reload restarts, from built, once
// the sessions they replace have been unregistered. A session that cannot be
// registered has its store and log closed, since the session it replaces has
// stopped, and is dropped from built and from next, the settings the engine
// keeps, so that the next Reload adds it again. It returns the SessionIDs of
// those sessions and why each failed.
func registerChanged(reg *Registry, changes sessionChanges, built map[SessionID]*session, next map[SessionID]*SessionSettings) (failed []SessionID, errs []error) {
	for sessionID := range changes.changed {
		s := built[sessionID]
		if err := reg.register(s); err != nil {
			errs = append(errs, fmt.Errorf("registering session %v: %w", sessionID, err))
			if err := s.store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("closing store of session %v: %w", sessionID, err))
			}
			if err := closeLog(s.log); err != nil {
				errs = append(errs, fmt.Errorf("closing log of session %v: %w", sessionID, err))
			}
			delete(built, sessionID)
			delete(next, sessionID)
			failed = append(failed, sessionID)
		}
	}
	return failed, errs
}
