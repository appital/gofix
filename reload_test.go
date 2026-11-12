package gofix

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/appital/gofix/config"
)

// reloadApp counts logons and logouts for each session, by the CompID of the
// counterparty.
type reloadApp struct {
	mu              sync.Mutex
	logons, logouts map[string]int
}

func newReloadApp() *reloadApp {
	return &reloadApp{logons: make(map[string]int), logouts: make(map[string]int)}
}

func (a *reloadApp) FromApp(*Message, SessionID) MessageRejectError { return nil }
func (a *reloadApp) OnCreate(SessionID)                             {}

func (a *reloadApp) OnLogon(id SessionID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logons[id.TargetCompID]++
}

func (a *reloadApp) OnLogout(id SessionID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logouts[id.TargetCompID]++
}

func (a *reloadApp) counts(target string) (logons, logouts int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.logons[target], a.logouts[target]
}

// waitLogons waits until the session with target has logged on n times.
func (a *reloadApp) waitLogons(t *testing.T, target string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		logons, _ := a.counts(target)
		return logons >= n
	}, 10*time.Second, 10*time.Millisecond, "%s should have logged on %d times", target, n)
}

// waitLogouts waits until the session with target has logged out n times.
func (a *reloadApp) waitLogouts(t *testing.T, target string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, logouts := a.counts(target)
		return logouts >= n
	}, 10*time.Second, 10*time.Millisecond, "%s should have logged out %d times", target, n)
}

// reloadSettings builds Settings from global settings and the settings of
// each session.
func reloadSettings(t *testing.T, global map[string]string, sessions ...map[string]string) *Settings {
	t.Helper()
	settings := NewSettings()
	for k, v := range global {
		settings.GlobalSettings().Set(k, v)
	}
	for _, s := range sessions {
		sessionSettings := NewSessionSettings()
		sessionSettings.Set(config.BeginString, BeginStringFIX42)
		for k, v := range s {
			sessionSettings.Set(k, v)
		}
		_, err := settings.AddSession(sessionSettings)
		require.NoError(t, err)
	}
	return settings
}

var reloadAcceptorGlobal = map[string]string{config.HeartBtInt: "30"}

// acceptorSession is the settings of an acceptor session for client on port.
func acceptorSession(client string, port int, extra ...string) map[string]string {
	s := map[string]string{
		config.SenderCompID:     "SERVER",
		config.TargetCompID:     client,
		config.SocketAcceptPort: fmt.Sprint(port),
	}
	for i := 0; i < len(extra); i += 2 {
		s[extra[i]] = extra[i+1]
	}
	return s
}

var reloadInitiatorGlobal = map[string]string{
	config.SocketConnectHost: "127.0.0.1",
	config.HeartBtInt:        "30",
	config.ReconnectInterval: "1",
}

// initiatorSession is the settings of an initiator session for client that
// connects to port.
func initiatorSession(client string, port int, extra ...string) map[string]string {
	s := map[string]string{
		config.SenderCompID:      client,
		config.TargetCompID:      "SERVER",
		config.SocketConnectPort: fmt.Sprint(port),
	}
	for i := 0; i < len(extra); i += 2 {
		s[extra[i]] = extra[i+1]
	}
	return s
}

func serverSessionID(client string) SessionID {
	return SessionID{BeginString: BeginStringFIX42, SenderCompID: "SERVER", TargetCompID: client}
}

func clientSessionID(client string) SessionID {
	return SessionID{BeginString: BeginStringFIX42, SenderCompID: client, TargetCompID: "SERVER"}
}

func TestDiffSessions(t *testing.T) {
	settings := func(kv ...string) *SessionSettings {
		s := NewSessionSettings()
		for i := 0; i < len(kv); i += 2 {
			s.Set(kv[i], kv[i+1])
		}
		return s
	}
	same, changed, removed, added := clientSessionID("SAME"), clientSessionID("CHANGED"), clientSessionID("REMOVED"), clientSessionID("ADDED")

	changes := diffSessions(
		map[SessionID]*SessionSettings{
			same:    settings("HeartBtInt", "30"),
			changed: settings("HeartBtInt", "30"),
			removed: settings(),
		},
		map[SessionID]*SessionSettings{
			same:    settings("HeartBtInt", "30"),
			changed: settings("HeartBtInt", "60"),
			added:   settings(),
		},
	)

	assert.Equal(t, []SessionID{removed}, changes.removed)
	assert.Equal(t, []SessionID{changed}, keys(changes.changed))
	assert.Equal(t, []SessionID{added}, keys(changes.added))

	// A setting being added counts as a change, as does one being removed.
	changes = diffSessions(
		map[SessionID]*SessionSettings{same: settings("HeartBtInt", "30")},
		map[SessionID]*SessionSettings{same: settings("HeartBtInt", "30", "LogonTimeout", "10")},
	)
	assert.Equal(t, []SessionID{same}, keys(changes.changed))
	changes = diffSessions(
		map[SessionID]*SessionSettings{same: settings("HeartBtInt", "30", "LogonTimeout", "10")},
		map[SessionID]*SessionSettings{same: settings("HeartBtInt", "30")},
	)
	assert.Equal(t, []SessionID{same}, keys(changes.changed))
}

func keys(m map[SessionID]*SessionSettings) []SessionID {
	var ids []SessionID
	for id := range m {
		ids = append(ids, id)
	}
	return ids
}

// TestAcceptorReload adds, changes and removes acceptor sessions while an
// initiator is connected to them, and checks that only those sessions are
// affected.
func TestAcceptorReload(t *testing.T) {
	const portA, portB = 5120, 5121
	acceptorApp, initiatorApp := newReloadApp(), newReloadApp()
	registry := NewRegistry()

	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA)),
		NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	initiator, err := NewInitiator(initiatorApp, NewMemoryStoreFactory(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", portA), initiatorSession("B", portB)),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	runForTest(t, initiator)
	acceptorApp.waitLogons(t, "A", 1)

	// Adding B opens a listener on its port, and B connects.
	require.NoError(t, acceptor.Reload(context.Background(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA), acceptorSession("B", portB))))
	acceptorApp.waitLogons(t, "B", 1)
	_, err = registry.GetExpectedSenderNum(serverSessionID("B"))
	require.NoError(t, err, "B should be registered")

	// Changing A restarts it, keeping its sequence numbers.
	seqNum, err := registry.GetExpectedSenderNum(serverSessionID("A"))
	require.NoError(t, err)
	require.Greater(t, seqNum, 1)
	require.NoError(t, acceptor.Reload(context.Background(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA, config.LogonTimeout, "20"), acceptorSession("B", portB))))
	acceptorApp.waitLogouts(t, "A", 1)
	acceptorApp.waitLogons(t, "A", 2)
	restartedSeqNum, err := registry.GetExpectedSenderNum(serverSessionID("A"))
	require.NoError(t, err)
	assert.Greater(t, restartedSeqNum, seqNum, "A's sequence numbers should carry over")

	// Removing B logs it out, unregisters it and closes its listener.
	require.NoError(t, acceptor.Reload(context.Background(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA, config.LogonTimeout, "20"))))
	_, logouts := acceptorApp.counts("B")
	assert.Equal(t, 1, logouts)
	_, err = registry.GetExpectedSenderNum(serverSessionID("B"))
	require.ErrorIs(t, err, errUnknownSession)
	_, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", portB))
	require.Error(t, err, "B's listener should be closed")

	// A was restarted once, and B never.
	logons, logouts := acceptorApp.counts("A")
	assert.Equal(t, 2, logons)
	assert.Equal(t, 1, logouts)
	logons, _ = acceptorApp.counts("B")
	assert.Equal(t, 1, logons)
}

// TestAcceptorReloadFailureChangesNothing checks that a Reload that cannot be
// applied in full leaves the running sessions alone.
func TestAcceptorReloadFailureChangesNothing(t *testing.T) {
	const portA, portB, portBusy = 5122, 5123, 5124
	acceptorApp, initiatorApp := newReloadApp(), newReloadApp()
	registry := NewRegistry()

	current := reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA))
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(), current, NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	initiator, err := NewInitiator(initiatorApp, NewMemoryStoreFactory(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", portA)),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	runForTest(t, initiator)
	acceptorApp.waitLogons(t, "A", 1)

	busy, err := net.Listen("tcp", fmt.Sprintf(":%d", portBusy))
	require.NoError(t, err)
	t.Cleanup(func() { busy.Close() })

	for name, settings := range map[string]*Settings{
		"bad setting on a new session": reloadSettings(t, reloadAcceptorGlobal,
			acceptorSession("A", portA, config.LogonTimeout, "20"),
			acceptorSession("B", portB, config.ValidateFieldsOutOfOrder, "maybe")),
		"port in use": reloadSettings(t, reloadAcceptorGlobal,
			acceptorSession("A", portA, config.LogonTimeout, "20"),
			acceptorSession("B", portBusy)),
		"missing port": reloadSettings(t, reloadAcceptorGlobal,
			acceptorSession("A", portA, config.LogonTimeout, "20"),
			map[string]string{config.SenderCompID: "SERVER", config.TargetCompID: "B"}),
		"engine setting": reloadSettings(t, map[string]string{config.HeartBtInt: "30", config.UseTCPProxy: "Y"},
			acceptorSession("A", portA, config.LogonTimeout, "20")),
		"session registered elsewhere": reloadSettings(t, reloadAcceptorGlobal,
			acceptorSession("A", portA, config.LogonTimeout, "20"),
			map[string]string{config.SenderCompID: "OTHER", config.TargetCompID: "B", config.SocketAcceptPort: fmt.Sprint(portB)}),
	} {
		t.Run(name, func(t *testing.T) {
			other := &session{sessionID: SessionID{BeginString: BeginStringFIX42, SenderCompID: "OTHER", TargetCompID: "B"}}
			require.NoError(t, registry.register(other))
			defer func() { _ = registry.UnregisterSession(other.sessionID) }()

			require.Error(t, acceptor.Reload(context.Background(), settings))

			_, err := registry.GetExpectedSenderNum(serverSessionID("B"))
			assert.ErrorIs(t, err, errUnknownSession, "B should not be registered")
			_, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", portB))
			assert.Error(t, err, "no listener should be opened for B")
		})
	}

	// A was neither stopped nor changed.
	logons, logouts := acceptorApp.counts("A")
	assert.Equal(t, 1, logons)
	assert.Equal(t, 0, logouts)
	acceptor.mu.RLock()
	assert.Same(t, current, acceptor.settings)
	acceptor.mu.RUnlock()

	// The acceptor still reloads once the settings are good.
	require.NoError(t, acceptor.Reload(context.Background(), current))
}

// TestAcceptorReloadContextDisconnects checks that once Reload's context is
// done it disconnects a session whose counterparty does not answer its
// Logout, and returns the context's error.
func TestAcceptorReloadContextDisconnects(t *testing.T) {
	const port = 5125
	registry := NewRegistry()
	acceptorApp := newReloadApp()
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", port, config.LogoutTimeout, "30")),
		NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	sendingTime := "52=" + time.Now().UTC().Format("20060102-15:04:05.000")
	_, err = conn.Write([]byte(wireMessage("8=FIX.4.2", "35=A", "49=A", "56=SERVER", "34=1", sendingTime, "98=0", "108=30")))
	require.NoError(t, err)
	acceptorApp.waitLogons(t, "A", 1)

	// The peer reads, but never answers the Logout.
	peerClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(peerClosed)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = acceptor.Reload(ctx, reloadSettings(t, reloadAcceptorGlobal, acceptorSession("B", port)))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)

	select {
	case <-peerClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not closed")
	}
	_, logouts := acceptorApp.counts("A")
	assert.Equal(t, 1, logouts)
	_, err = registry.GetExpectedSenderNum(serverSessionID("A"))
	require.ErrorIs(t, err, errUnknownSession)
	_, err = registry.GetExpectedSenderNum(serverSessionID("B"))
	require.NoError(t, err, "the rest of the change should still be applied")
}

// TestAcceptorReloadBeforeRun checks that Reload before Run replaces the
// sessions Run starts.
func TestAcceptorReloadBeforeRun(t *testing.T) {
	const port = 5126
	registry := NewRegistry()
	acceptor, err := NewAcceptor(newReloadApp(), NewMemoryStoreFactory(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", port)),
		NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)

	require.NoError(t, acceptor.Reload(context.Background(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("B", port+1))))
	_, err = registry.GetExpectedSenderNum(serverSessionID("A"))
	require.ErrorIs(t, err, errUnknownSession)
	_, err = registry.GetExpectedSenderNum(serverSessionID("B"))
	require.NoError(t, err)

	_, err = startForTest(t, acceptor)
	require.NoError(t, err)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port+1))
	require.NoError(t, err, "Run should listen on B's port")
	conn.Close()
	_, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.Error(t, err, "Run should not listen on A's port")
}

func TestReloadAfterRunFails(t *testing.T) {
	acceptor := newLifecycleAcceptor(t, newReloadApp(), 5128)
	stop, err := startForTest(t, acceptor)
	require.NoError(t, err)
	stop()
	err = acceptor.Reload(context.Background(), acceptor.settings)
	require.ErrorIs(t, err, errReloadAfterRun)

	initiator := newLifecycleInitiator(t, newReloadApp(), 5128)
	run := runForTest(t, initiator)
	require.Eventually(t, func() bool {
		initiator.reloadMu.Lock()
		defer initiator.reloadMu.Unlock()
		return initiator.runState == runStarted
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, run.stop())
	err = initiator.Reload(context.Background(), initiator.settings)
	require.ErrorIs(t, err, errReloadAfterRun)
}

// TestInitiatorReload adds, changes and removes initiator sessions while they
// are connected to an acceptor, and checks that only those sessions are
// affected.
func TestInitiatorReload(t *testing.T) {
	const port = 5129
	acceptorApp, initiatorApp := newReloadApp(), newReloadApp()
	registry := NewRegistry()

	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", port), acceptorSession("B", port)),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	initiator, err := NewInitiator(initiatorApp, NewMemoryStoreFactory(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port)),
		NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	runForTest(t, initiator)
	initiatorApp.waitLogons(t, "SERVER", 1)
	acceptorApp.waitLogons(t, "A", 1)

	// Adding B connects it.
	require.NoError(t, initiator.Reload(context.Background(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port), initiatorSession("B", port))))
	acceptorApp.waitLogons(t, "B", 1)

	// Changing A restarts it, keeping its sequence numbers.
	seqNum, err := registry.GetExpectedSenderNum(clientSessionID("A"))
	require.NoError(t, err)
	require.Greater(t, seqNum, 1)
	require.NoError(t, initiator.Reload(context.Background(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port, config.LogonTimeout, "20"), initiatorSession("B", port))))
	acceptorApp.waitLogouts(t, "A", 1)
	acceptorApp.waitLogons(t, "A", 2)
	restartedSeqNum, err := registry.GetExpectedSenderNum(clientSessionID("A"))
	require.NoError(t, err)
	assert.Greater(t, restartedSeqNum, seqNum, "A's sequence numbers should carry over")

	// Removing B logs it out and unregisters it.
	require.NoError(t, initiator.Reload(context.Background(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port, config.LogonTimeout, "20"))))
	acceptorApp.waitLogouts(t, "B", 1)
	_, err = registry.GetExpectedSenderNum(clientSessionID("B"))
	require.ErrorIs(t, err, errUnknownSession)

	// A bad setting changes nothing.
	err = initiator.Reload(context.Background(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port, config.HeartBtInt, "often")))
	require.Error(t, err)
	err = initiator.Reload(context.Background(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port), initiatorSession("C", port, config.SocketUseSSL, "maybe")))
	require.Error(t, err)
	_, err = registry.GetExpectedSenderNum(clientSessionID("C"))
	require.ErrorIs(t, err, errUnknownSession)

	// A was restarted once, and B never.
	logons, logouts := acceptorApp.counts("A")
	assert.Equal(t, 2, logons)
	assert.Equal(t, 1, logouts)
	logons, logouts = acceptorApp.counts("B")
	assert.Equal(t, 1, logons)
	assert.Equal(t, 1, logouts)
}

// TestReloadIsSerialised runs Reloads concurrently with each other and with
// connections being accepted, for the race detector.
func TestReloadIsSerialised(t *testing.T) {
	const port = 5130
	acceptorApp := newReloadApp()
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(),
		reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", port)),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	initiator, err := NewInitiator(newReloadApp(), NewMemoryStoreFactory(),
		reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port), initiatorSession("B", port)),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	runForTest(t, initiator)

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for n := range 10 {
		wg.Go(func() {
			sessions := []map[string]string{acceptorSession("A", port)}
			if n%2 == 0 {
				sessions = append(sessions, acceptorSession("B", port))
			}
			errs <- acceptor.Reload(context.Background(), reloadSettings(t, reloadAcceptorGlobal, sessions...))
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}

	// A's settings never changed, so it was never restarted.
	_, logouts := acceptorApp.counts("A")
	assert.Equal(t, 0, logouts)
}
