package gofix

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/appital/gofix/config"
)

// errReloadAfterRun is returned by Reload once Run has returned.
var errReloadAfterRun = errors.New("cannot reload after Run has returned")

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

// checkEngineSettings returns an error naming the first of names whose value
// in next differs from its value in current, or that is set in only one of
// them.
func checkEngineSettings(current, next *SessionSettings, names []string) error {
	for _, name := range names {
		cur, inCur := current.settings[name]
		nxt, inNext := next.settings[name]
		if inCur != inNext || !bytes.Equal(cur, nxt) {
			return fmt.Errorf("%s cannot be changed by Reload", name)
		}
	}
	return nil
}

// sessionChanges is what Reload must do to bring an engine's sessions in line
// with new settings. Each map holds the effective settings of the sessions
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

// closeStores closes the stores of sessions that were built but never used.
func closeStores(sessions []*session) {
	for _, s := range sessions {
		_ = s.store.Close()
	}
}
