package gofix

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
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

// engine is an Acceptor or an Initiator.
type engine interface {
	runner
	reloader
}

// startReloadable runs e, which loads its settings from source, and returns
// a function that reloads it with new settings.
func startReloadable(t *testing.T, e engine, source *testSource) func(context.Context, *Settings) error {
	t.Helper()
	_, err := startForTest(t, e)
	require.NoError(t, err)
	return func(ctx context.Context, settings *Settings) error {
		source.set(settings)
		return e.Reload(ctx)
	}
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

	source := newTestSource(reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA)))
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	initiator, err := NewInitiator(initiatorApp, NewMemoryStoreFactory(),
		StaticSettings(reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", portA), initiatorSession("B", portB))),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	runForTest(t, initiator)
	acceptorApp.waitLogons(t, "A", 1)

	// Adding B opens a listener on its port, and B connects.
	require.NoError(t, reloadWith(acceptor, source, reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA), acceptorSession("B", portB))))
	acceptorApp.waitLogons(t, "B", 1)
	_, err = registry.GetExpectedSenderNum(serverSessionID("B"))
	require.NoError(t, err, "B should be registered")

	// Changing A restarts it, keeping its sequence numbers.
	seqNum, err := registry.GetExpectedSenderNum(serverSessionID("A"))
	require.NoError(t, err)
	require.Greater(t, seqNum, 1)
	require.NoError(t, reloadWith(acceptor, source, reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA, config.LogonTimeout, "20"), acceptorSession("B", portB))))
	acceptorApp.waitLogouts(t, "A", 1)
	acceptorApp.waitLogons(t, "A", 2)
	restartedSeqNum, err := registry.GetExpectedSenderNum(serverSessionID("A"))
	require.NoError(t, err)
	assert.Greater(t, restartedSeqNum, seqNum, "A's sequence numbers should carry over")

	// Removing B logs it out, unregisters it and closes its listener.
	require.NoError(t, reloadWith(acceptor, source, reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", portA, config.LogonTimeout, "20"))))
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
	source := newTestSource(current)
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)
	acceptor.mu.RLock()
	kept := acceptor.settings
	acceptor.mu.RUnlock()

	initiator, err := NewInitiator(initiatorApp, NewMemoryStoreFactory(),
		StaticSettings(reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", portA))),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	runForTest(t, initiator)
	acceptorApp.waitLogons(t, "A", 1)

	busy, err := net.Listen("tcp", fmt.Sprintf(":%d", portBusy))
	require.NoError(t, err)
	t.Cleanup(func() { busy.Close() })

	for name, settings := range map[string]*Settings{
		"no settings": nil,
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

			loads := source.loaded()
			require.Error(t, reloadWith(acceptor, source, settings))
			assert.Equal(t, loads+1, source.loaded(), "Reload should load the settings once")

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
	assert.Same(t, kept, acceptor.settings)
	acceptor.mu.RUnlock()

	// The acceptor still reloads once the settings are good.
	require.NoError(t, reloadWith(acceptor, source, current))
}

// TestAcceptorReloadContextDisconnects checks that once Reload's context is
// done it disconnects a session whose counterparty does not answer its
// Logout, and returns the context's error.
func TestAcceptorReloadContextDisconnects(t *testing.T) {
	const port = 5125
	registry := NewRegistry()
	acceptorApp := newReloadApp()
	source := newTestSource(reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", port, config.LogoutTimeout, "30")))
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(registry))
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
	source.set(reloadSettings(t, reloadAcceptorGlobal, acceptorSession("B", port)))
	err = acceptor.Reload(ctx)
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

// TestReloadBeforeRunFails checks that Reload before Run returns an error
// without loading the settings, which Run loads itself.
func TestReloadBeforeRunFails(t *testing.T) {
	source := newTestSource(reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", 5126)))
	acceptor, err := NewAcceptor(newReloadApp(), NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	require.ErrorIs(t, acceptor.Reload(context.Background()), errReloadBeforeRun)

	initiator, err := NewInitiator(newReloadApp(), NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	require.ErrorIs(t, initiator.Reload(context.Background()), errReloadBeforeRun)

	assert.Zero(t, source.loaded())
}

// hookStoreFactory creates memory stores, calling onCreate first, and counts
// how many of each session's stores have been closed. It fails to create a
// store for failFor.
type hookStoreFactory struct {
	onCreate func(SessionID)
	failFor  SessionID
	mu       sync.Mutex
	closed   map[SessionID]int
}

func (f *hookStoreFactory) Create(sessionID SessionID, settings *SessionSettings) (MessageStore, error) {
	if f.onCreate != nil {
		f.onCreate(sessionID)
	}
	if sessionID == f.failFor {
		return nil, fmt.Errorf("cannot create store for %v", sessionID)
	}
	store, err := NewMemoryStoreFactory().Create(sessionID, settings)
	if err != nil {
		return nil, err
	}
	return hookStore{MessageStore: store, factory: f, sessionID: sessionID}, nil
}

func (f *hookStoreFactory) closedStores(sessionID SessionID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed[sessionID]
}

type hookStore struct {
	MessageStore
	factory   *hookStoreFactory
	sessionID SessionID
}

func (s hookStore) Close() error {
	s.factory.mu.Lock()
	if s.factory.closed == nil {
		s.factory.closed = make(map[SessionID]int)
	}
	s.factory.closed[s.sessionID]++
	s.factory.mu.Unlock()
	return s.MessageStore.Close()
}

// hookLogFactory creates logs that do nothing, and counts how many global logs
// and how many of each session's logs have been created and closed.
type hookLogFactory struct {
	mu                          sync.Mutex
	created, closed             map[SessionID]int
	globalCreated, globalClosed int
}

func (f *hookLogFactory) Create(*SessionSettings) (Log, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.globalCreated++
	return hookLog{factory: f, global: true}, nil
}

func (f *hookLogFactory) globalCounts() (created, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.globalCreated, f.globalClosed
}

func (f *hookLogFactory) CreateSessionLog(sessionID SessionID, _ *SessionSettings) (Log, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created == nil {
		f.created = make(map[SessionID]int)
	}
	f.created[sessionID]++
	return hookLog{factory: f, sessionID: sessionID}, nil
}

func (f *hookLogFactory) counts(sessionID SessionID) (created, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created[sessionID], f.closed[sessionID]
}

type hookLog struct {
	nullLog
	factory   *hookLogFactory
	sessionID SessionID
	global    bool
}

func (l hookLog) Close() error {
	l.factory.mu.Lock()
	defer l.factory.mu.Unlock()
	if l.global {
		l.factory.globalClosed++
		return nil
	}
	if l.factory.closed == nil {
		l.factory.closed = make(map[SessionID]int)
	}
	l.factory.closed[l.sessionID]++
	return nil
}

// TestReloadClosesSessionLogs checks that Reload closes the logs of the
// sessions it removes, restarts, or builds and then discards because the
// Reload fails.
func TestReloadClosesSessionLogs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		global  map[string]string
		session func(client string, extra ...string) map[string]string
		id      func(client string) SessionID
		reload  func(t *testing.T, stores MessageStoreFactory, settings *Settings, logs LogFactory, registry *Registry) func(context.Context, *Settings) error
	}{
		{
			name:    "acceptor",
			global:  reloadAcceptorGlobal,
			session: func(client string, extra ...string) map[string]string { return acceptorSession(client, 5135, extra...) },
			id:      serverSessionID,
			reload: func(t *testing.T, stores MessageStoreFactory, settings *Settings, logs LogFactory, registry *Registry) func(context.Context, *Settings) error {
				source := newTestSource(settings)
				acceptor, err := NewAcceptor(newReloadApp(), stores, source.load, logs, WithRegistry(registry))
				require.NoError(t, err)
				return startReloadable(t, acceptor, source)
			},
		},
		{
			name:   "initiator",
			global: reloadInitiatorGlobal,
			session: func(client string, extra ...string) map[string]string {
				return initiatorSession(client, 5135, extra...)
			},
			id: clientSessionID,
			reload: func(t *testing.T, stores MessageStoreFactory, settings *Settings, logs LogFactory, registry *Registry) func(context.Context, *Settings) error {
				source := newTestSource(settings)
				initiator, err := NewInitiator(newReloadApp(), stores, source.load, logs, WithRegistry(registry))
				require.NoError(t, err)
				return startReloadable(t, initiator, source)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stores := &hookStoreFactory{}
			logs := &hookLogFactory{}
			registry := NewRegistry()
			reload := tc.reload(t, stores,
				reloadSettings(t, tc.global, tc.session("A"), tc.session("B"), tc.session("C")), logs, registry)
			closed := func(client string) int {
				_, closed := logs.counts(tc.id(client))
				return closed
			}

			// B is removed, C restarted and D added.
			require.NoError(t, reload(context.Background(), reloadSettings(t, tc.global,
				tc.session("A"), tc.session("C", config.LogonTimeout, "20"), tc.session("D"))))
			assert.Equal(t, 0, closed("A"), "A's log should be open")
			assert.Equal(t, 1, closed("B"), "removed B's log should be closed")
			assert.Equal(t, 1, closed("C"), "the log of the C that was replaced should be closed")
			assert.Equal(t, 0, closed("D"), "D's log should be open")

			// F's store cannot be created, so the Reload fails having maybe
			// built E first.
			stores.failFor = tc.id("F")
			require.Error(t, reload(context.Background(), reloadSettings(t, tc.global,
				tc.session("A"), tc.session("C", config.LogonTimeout, "20"), tc.session("D"),
				tc.session("E"), tc.session("F"))))
			created, closedE := logs.counts(tc.id("E"))
			assert.Equal(t, created, closedE, "every log built for E should be closed")
			created, closedF := logs.counts(tc.id("F"))
			assert.Equal(t, 1, created)
			assert.Equal(t, 1, closedF, "F's log should be closed when its store cannot be created")
			assert.Equal(t, 0, closed("A"))
			assert.Equal(t, 1, closed("C"))
			assert.Equal(t, 0, closed("D"))

			// G is already registered, so the Reload fails once it has built
			// every session, including a new C.
			stores.failFor = SessionID{}
			require.NoError(t, registry.register(&session{sessionID: tc.id("G")}))
			require.Error(t, reload(context.Background(), reloadSettings(t, tc.global,
				tc.session("A"), tc.session("C", config.LogonTimeout, "30"), tc.session("D"),
				tc.session("G"))))
			created, closedC := logs.counts(tc.id("C"))
			assert.Equal(t, 1, created-closedC, "only the running C's log should be open")
			created, closedG := logs.counts(tc.id("G"))
			assert.Equal(t, created, closedG, "every log built for G should be closed")
		})
	}
}

// TestReloadRejectsStoreSettingsChange checks that Reload refuses to change
// the store settings of a session it would restart, and that the session can
// be moved to a new store by removing it and adding it again.
func TestReloadRejectsStoreSettingsChange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		global  map[string]string
		session func(extra ...string) map[string]string
		id      SessionID
		engine  func(t *testing.T, settings *Settings) (reload func(context.Context, *Settings) error, current func() *session)
	}{
		{
			name:    "acceptor",
			global:  reloadAcceptorGlobal,
			session: func(extra ...string) map[string]string { return acceptorSession("A", 5138, extra...) },
			id:      serverSessionID("A"),
			engine: func(t *testing.T, settings *Settings) (func(context.Context, *Settings) error, func() *session) {
				source := newTestSource(settings)
				acceptor, err := NewAcceptor(newReloadApp(), NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(NewRegistry()))
				require.NoError(t, err)
				return startReloadable(t, acceptor, source), func() *session { return acceptor.sessions[serverSessionID("A")] }
			},
		},
		{
			name:    "initiator",
			global:  reloadInitiatorGlobal,
			session: func(extra ...string) map[string]string { return initiatorSession("A", 5138, extra...) },
			id:      clientSessionID("A"),
			engine: func(t *testing.T, settings *Settings) (func(context.Context, *Settings) error, func() *session) {
				source := newTestSource(settings)
				initiator, err := NewInitiator(newReloadApp(), NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(NewRegistry()))
				require.NoError(t, err)
				return startReloadable(t, initiator, source), func() *session { return initiator.sessions[clientSessionID("A")] }
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reload, current := tc.engine(t, reloadSettings(t, tc.global, tc.session(config.FileStorePath, "old")))
			before := current()

			err := reload(context.Background(), reloadSettings(t, tc.global,
				tc.session(config.FileStorePath, "new", config.LogonTimeout, "20")))
			require.ErrorContains(t, err, config.FileStorePath)
			assert.True(t, before == current(), "A should not have been restarted")

			require.NoError(t, reload(context.Background(), reloadSettings(t, tc.global)))
			require.NoError(t, reload(context.Background(), reloadSettings(t, tc.global,
				tc.session(config.FileStorePath, "new"))))
			assert.NotNil(t, current(), "A should have been added again")
		})
	}
}

// TestInitiatorReloadFailsToRegister checks that a Reload that cannot
// register a session it adds changes nothing, closes the store it built for
// it, and can be retried.
func TestInitiatorReloadFailsToRegister(t *testing.T) {
	const port = 5133
	registry := NewRegistry()
	other := &session{sessionID: clientSessionID("B")}
	conflict := true
	// Another engine sharing the registry registers B while Reload is
	// building it, after Reload has checked that B is not registered.
	storeFactory := &hookStoreFactory{onCreate: func(sessionID SessionID) {
		if conflict && sessionID == clientSessionID("B") {
			require.NoError(t, registry.register(other))
		}
	}}
	source := newTestSource(reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port)))
	initiator, err := NewInitiator(newReloadApp(), storeFactory, source.load, NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, initiator)
	require.NoError(t, err)
	sessionA := func() *session {
		initiator.lifecycle.reloadMu.Lock()
		defer initiator.lifecycle.reloadMu.Unlock()
		return initiator.sessions[clientSessionID("A")]
	}
	before := sessionA()

	next := reloadSettings(t, reloadInitiatorGlobal,
		initiatorSession("A", port, config.LogonTimeout, "20"), initiatorSession("B", port))
	err = reloadWith(initiator, source, next)
	require.ErrorIs(t, err, errDuplicateSessionID)
	assert.True(t, before == sessionA(), "A should not have been restarted")
	registered, _ := registry.lookup(clientSessionID("A"))
	assert.True(t, before == registered, "the old A should still be registered")
	registered, _ = registry.lookup(clientSessionID("B"))
	assert.True(t, other == registered, "the other B should still be registered")
	assert.Equal(t, 1, storeFactory.closedStores(clientSessionID("B")), "B's store should have been closed")

	conflict = false
	require.NoError(t, registry.UnregisterSession(clientSessionID("B")))
	require.NoError(t, reloadWith(initiator, source, next))
	registered, _ = registry.lookup(clientSessionID("B"))
	require.NotNil(t, registered, "Reload should have retried B")
	assert.True(t, other != registered, "B should be registered to the initiator")
	assert.True(t, before != sessionA(), "A should have been restarted")
}

// TestAcceptorReloadRacesDynamicSession checks that a Reload that adds a
// session changes nothing if a dynamic session with its SessionID logs on
// while Reload is running, and can be retried once that session has gone.
func TestAcceptorReloadRacesDynamicSession(t *testing.T) {
	const port = 5134
	global := map[string]string{config.HeartBtInt: "30", config.DynamicSessions: "Y"}
	registry := NewRegistry()
	var loggedOn atomic.Bool
	var dynamicB *running
	storeFactory := &hookStoreFactory{}
	storeFactory.onCreate = func(sessionID SessionID) {
		if sessionID != serverSessionID("B") || !loggedOn.CompareAndSwap(false, true) {
			return
		}
		// B logs on as a dynamic session after Reload has checked that B is
		// not registered.
		initiator, err := NewInitiator(newReloadApp(), NewMemoryStoreFactory(),
			StaticSettings(reloadSettings(t, reloadInitiatorGlobal, initiatorSession("B", port))),
			NewNullLogFactory(), WithRegistry(NewRegistry()))
		require.NoError(t, err)
		dynamicB = runForTest(t, initiator)
		require.Eventually(t, func() bool {
			_, ok := registry.lookup(serverSessionID("B"))
			return ok
		}, 5*time.Second, 10*time.Millisecond)
	}
	source := newTestSource(reloadSettings(t, global, acceptorSession("A", port)))
	acceptor, err := NewAcceptor(newReloadApp(), storeFactory, source.load, NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)
	sessionA := func() *session {
		acceptor.mu.RLock()
		defer acceptor.mu.RUnlock()
		return acceptor.sessions[serverSessionID("A")]
	}
	before := sessionA()

	next := reloadSettings(t, global,
		acceptorSession("A", port, config.LogonTimeout, "20"), acceptorSession("B", port))
	err = reloadWith(acceptor, source, next)
	require.ErrorIs(t, err, errDuplicateSessionID)
	assert.True(t, before == sessionA(), "A should not have been restarted")
	registered, _ := registry.lookup(serverSessionID("A"))
	assert.True(t, before == registered, "the old A should still be registered")
	assert.Equal(t, 1, storeFactory.closedStores(serverSessionID("B")), "static B's store should have been closed")

	require.NoError(t, dynamicB.stop())
	require.Eventually(t, func() bool {
		_, ok := registry.lookup(serverSessionID("B"))
		return !ok
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, reloadWith(acceptor, source, next))
	acceptor.mu.RLock()
	staticB := acceptor.sessions[serverSessionID("B")]
	acceptor.mu.RUnlock()
	require.NotNil(t, staticB, "Reload should have retried B")
	registered, _ = registry.lookup(serverSessionID("B"))
	assert.True(t, staticB == registered, "B should be registered to the static session")
}

// TestAcceptorReloadKeepsDynamicSessionPorts checks that, with
// DynamicSessions, Reload keeps listening on a port no static session uses any
// more, since dynamic sessions log on through it.
func TestAcceptorReloadKeepsDynamicSessionPorts(t *testing.T) {
	const port = 5136
	global := map[string]string{config.HeartBtInt: "30", config.DynamicSessions: "Y"}
	acceptorApp := newReloadApp()
	source := newTestSource(reloadSettings(t, global, acceptorSession("A", port)))
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	// A moves to another port, so no static session uses port any more.
	require.NoError(t, reloadWith(acceptor, source, reloadSettings(t, global, acceptorSession("A", port+1))))

	initiator, err := NewInitiator(newReloadApp(), NewMemoryStoreFactory(),
		StaticSettings(reloadSettings(t, reloadInitiatorGlobal, initiatorSession("X", port))),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	runForTest(t, initiator)
	acceptorApp.waitLogons(t, "X", 1)
}

func TestReloadAfterRunFails(t *testing.T) {
	acceptor := newLifecycleAcceptor(t, newReloadApp(), 5128)
	stop, err := startForTest(t, acceptor)
	require.NoError(t, err)
	stop()
	require.ErrorIs(t, acceptor.Reload(context.Background()), errReloadAfterRun)

	initiator := newLifecycleInitiator(t, newReloadApp(), 5128)
	stop, err = startForTest(t, initiator)
	require.NoError(t, err)
	stop()
	require.ErrorIs(t, initiator.Reload(context.Background()), errReloadAfterRun)
}

// TestInitiatorReload adds, changes and removes initiator sessions while they
// are connected to an acceptor, and checks that only those sessions are
// affected.
func TestInitiatorReload(t *testing.T) {
	const port = 5129
	acceptorApp, initiatorApp := newReloadApp(), newReloadApp()
	registry := NewRegistry()

	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(),
		StaticSettings(reloadSettings(t, reloadAcceptorGlobal, acceptorSession("A", port), acceptorSession("B", port))),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	source := newTestSource(reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port)))
	initiator, err := NewInitiator(initiatorApp, NewMemoryStoreFactory(), source.load, NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err)
	runForTest(t, initiator)
	initiatorApp.waitLogons(t, "SERVER", 1)
	acceptorApp.waitLogons(t, "A", 1)

	// Adding B connects it.
	require.NoError(t, reloadWith(initiator, source, reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port), initiatorSession("B", port))))
	acceptorApp.waitLogons(t, "B", 1)

	// Changing A restarts it, keeping its sequence numbers.
	seqNum, err := registry.GetExpectedSenderNum(clientSessionID("A"))
	require.NoError(t, err)
	require.Greater(t, seqNum, 1)
	require.NoError(t, reloadWith(initiator, source, reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port, config.LogonTimeout, "20"), initiatorSession("B", port))))
	acceptorApp.waitLogouts(t, "A", 1)
	acceptorApp.waitLogons(t, "A", 2)
	restartedSeqNum, err := registry.GetExpectedSenderNum(clientSessionID("A"))
	require.NoError(t, err)
	assert.Greater(t, restartedSeqNum, seqNum, "A's sequence numbers should carry over")

	// Removing B logs it out and unregisters it.
	require.NoError(t, reloadWith(initiator, source, reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port, config.LogonTimeout, "20"))))
	acceptorApp.waitLogouts(t, "B", 1)
	_, err = registry.GetExpectedSenderNum(clientSessionID("B"))
	require.ErrorIs(t, err, errUnknownSession)

	// A bad setting changes nothing.
	err = reloadWith(initiator, source, reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port, config.HeartBtInt, "often")))
	require.Error(t, err)
	err = reloadWith(initiator, source, reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port), initiatorSession("C", port, config.SocketUseSSL, "maybe")))
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
	// Each load returns different settings, with B in every other one.
	var loads atomic.Int64
	source := func(context.Context) (*Settings, error) {
		sessions := []map[string]string{acceptorSession("A", port)}
		if loads.Add(1)%2 == 0 {
			sessions = append(sessions, acceptorSession("B", port))
		}
		return reloadSettings(t, reloadAcceptorGlobal, sessions...), nil
	}
	acceptor, err := NewAcceptor(acceptorApp, NewMemoryStoreFactory(), source, NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	initiator, err := NewInitiator(newReloadApp(), NewMemoryStoreFactory(),
		StaticSettings(reloadSettings(t, reloadInitiatorGlobal, initiatorSession("A", port), initiatorSession("B", port))),
		NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	runForTest(t, initiator)

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			errs <- acceptor.Reload(context.Background())
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
