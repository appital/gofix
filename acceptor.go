// Copyright (c) quickfixengine.org  All rights reserved.
//
// This file may be distributed under the terms of the quickfixengine.org
// license as defined by quickfixengine.org and appearing in the file
// LICENSE included in the packaging of this file.
//
// This file is provided AS IS with NO WARRANTY OF ANY KIND, INCLUDING
// THE WARRANTY OF DESIGN, MERCHANTABILITY AND FITNESS FOR A
// PARTICULAR PURPOSE.
//
// See http://www.quickfixengine.org/LICENSE for licensing information.
//
// Contact ask@quickfixengine.org if any conditions of this licensing
// are not clear to you.

package gofix

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	proxyproto "github.com/pires/go-proxyproto"

	"github.com/appital/gofix/config"
)

// Acceptor accepts connections from FIX clients and manages the associated sessions.
type Acceptor struct {
	app                   Application
	settings              *Settings
	logFactory            LogFactory
	storeFactory          MessageStoreFactory
	globalLog             Log
	sessions              map[SessionID]*session
	sessionGroup          sync.WaitGroup
	listenerShutdown      sync.WaitGroup
	connectionGroup       sync.WaitGroup
	stopCtx               context.Context
	lifecycle             lifecycle
	readyOnce             sync.Once
	ready                 chan struct{}
	dynamicSessions       bool
	dynamicQualifier      bool
	dynamicQualifierCount atomic.Int64
	dynamicSessionChan    chan *session
	sessionAddr           sync.Map
	sessionHostPort       map[SessionID]int
	listeners             map[string]net.Listener
	connectionValidator   ConnectionValidator
	tlsConfig             *tls.Config
	newListenerCallback   NewListenerCallback
	maxMessageSize        int
	firstMessageTimeout   time.Duration
	socketAcceptHost      string
	useTCPProxy           bool
	// reloadMu serialises Reload, and keeps it from running while Run starts
	// or stops the acceptor. runState is guarded by it.
	reloadMu sync.Mutex
	runState runState
	// mu guards settings, sessions and sessionHostPort, which Reload changes
	// while connections are being handled.
	mu sync.RWMutex
	// sessionSettings holds the effective settings of the static sessions.
	// It is guarded by reloadMu.
	sessionSettings map[SessionID]*SessionSettings
	sessionFactory
}

// ConnectionValidator is an interface allowing to implement a custom authentication logic.
type ConnectionValidator interface {
	// Validate the connection for validity. This can be a part of authentication process.
	// For example, you may tie up a SenderCompID to an IP range, or to a specific TLS certificate as a part of mTLS.
	Validate(netConn net.Conn, session SessionID) error
}

// NewListenerCallback is a function that returns a net.Listener for the given address and tls.Config struct.
type NewListenerCallback func(address string, tlsConfig *tls.Config) (net.Listener, error)

// Run accepts connections until ctx is done, then stops accepting, logs out
// its sessions, closes their connections and returns. It returns nil once it
// has shut down because ctx is done, or the error that stopped it starting, in
// which case nothing is left running and Run may be called again.
//
// Run returns ctx's error, starting nothing, if ctx is already done, and an
// error if it is already running or has run. Ready is closed once the acceptor
// is listening.
func (a *Acceptor) Run(ctx context.Context) error {
	if err := a.lifecycle.begin(ctx); err != nil {
		return err
	}
	a.reloadMu.Lock()
	if err := a.start(ctx); err != nil {
		a.reloadMu.Unlock()
		a.lifecycle.abort()
		return err
	}
	a.runState = runStarted
	a.reloadMu.Unlock()
	close(a.readyChan())

	<-ctx.Done()
	a.reloadMu.Lock()
	a.runState = runFinished
	a.reloadMu.Unlock()
	return a.shutdown()
}

// Ready returns a channel that is closed once Run has started the acceptor
// and it is listening on every port. It is never closed if Run fails to
// start, so wait on it together with Run's result or its context:
//
//	g, ctx := errgroup.WithContext(ctx)
//	g.Go(func() error { return acceptor.Run(ctx) })
//	select {
//	case <-acceptor.Ready():
//	case <-ctx.Done():
//		return g.Wait()
//	}
func (a *Acceptor) Ready() <-chan struct{} {
	return a.readyChan()
}

func (a *Acceptor) readyChan() chan struct{} {
	a.readyOnce.Do(func() { a.ready = make(chan struct{}) })
	return a.ready
}

// start opens the listeners and starts the sessions. If it fails, it closes
// the listeners it opened and starts nothing.
func (a *Acceptor) start(ctx context.Context) (err error) {
	defer func() {
		if err == nil {
			return
		}
		for _, listener := range a.listeners {
			if listener != nil {
				listener.Close()
			}
		}
	}()

	a.socketAcceptHost = ""
	if a.settings.GlobalSettings().HasSetting(config.SocketAcceptHost) {
		if a.socketAcceptHost, err = a.settings.GlobalSettings().Setting(config.SocketAcceptHost); err != nil {
			return
		}
	}

	a.sessionHostPort = make(map[SessionID]int)
	a.listeners = make(map[string]net.Listener)
	for sessionID, sessionSettings := range a.sessionSettings {
		if sessionSettings.HasSetting(config.SocketAcceptPort) {
			if a.sessionHostPort[sessionID], err = sessionSettings.IntSetting(config.SocketAcceptPort); err != nil {
				return
			}
		} else if a.sessionHostPort[sessionID], err = a.settings.GlobalSettings().IntSetting(config.SocketAcceptPort); err != nil {
			return
		}
		a.listeners[a.listenAddress(a.sessionHostPort[sessionID])] = nil
	}

	if a.tlsConfig == nil {
		var tlsConfig *tls.Config
		if tlsConfig, err = loadTLSConfig(a.settings.GlobalSettings()); err != nil {
			return
		}
		a.tlsConfig = tlsConfig
	}

	if a.newListenerCallback == nil {
		a.newListenerCallback = func(address string, tlsConfig *tls.Config) (net.Listener, error) {
			if tlsConfig != nil {
				return tls.Listen("tcp", address, a.tlsConfig)
			}
			return net.Listen("tcp", address)
		}
	}

	a.useTCPProxy = false
	if a.settings.GlobalSettings().HasSetting(config.UseTCPProxy) {
		if a.useTCPProxy, err = a.settings.GlobalSettings().BoolSetting(config.UseTCPProxy); err != nil {
			return
		}
	}

	for address := range a.listeners {
		if a.listeners[address], err = a.openListener(address); err != nil {
			return
		}
	}

	a.stopCtx = ctx
	for _, s := range a.sessions {
		a.sessionGroup.Go(s.run)
	}
	if a.dynamicSessions {
		a.dynamicSessionChan = make(chan *session)
		a.sessionGroup.Go(a.dynamicSessionsLoop)
	}
	for _, listener := range a.listeners {
		a.listenerShutdown.Go(func() {
			a.listenForConnections(listener)
		})
	}
	return
}

// listenAddress is the address the acceptor listens on for sessions using
// port.
func (a *Acceptor) listenAddress(port int) string {
	return net.JoinHostPort(a.socketAcceptHost, strconv.Itoa(port))
}

// openListener opens a listener on address.
func (a *Acceptor) openListener(address string) (net.Listener, error) {
	listener, err := a.newListenerCallback(address, a.tlsConfig)
	if err != nil {
		return nil, err
	}
	if a.useTCPProxy {
		listener = &proxyproto.Listener{Listener: listener}
	}
	return listener, nil
}

// shutdown stops accepting connections, logs out existing sessions and closes
// their connections. It is called once the context passed to Run is done,
// which also closes connections still waiting for their first message and
// stops the dynamic sessions.
func (a *Acceptor) shutdown() error {
	for _, listener := range a.listeners {
		listener.Close()
	}
	// Once the listeners have returned no new connections can be handled.
	a.listenerShutdown.Wait()

	for _, session := range a.sessions {
		session.stop()
	}
	// Each connection returns once its session has disconnected.
	a.connectionGroup.Wait()
	a.sessionGroup.Wait()

	var errs []error
	for sessionID := range a.sessions {
		if err := a.reg().UnregisterSession(sessionID); err != nil {
			errs = append(errs, fmt.Errorf("unregistering session %v: %w", sessionID, err))
		}
	}
	return errors.Join(errs...)
}

// RemoteAddr gets remote IP address for a given session.
func (a *Acceptor) RemoteAddr(sessionID SessionID) (net.Addr, bool) {
	addr, ok := a.sessionAddr.Load(sessionID)
	if !ok || addr == nil {
		return nil, false
	}
	val, ok := addr.(net.Addr)
	return val, ok
}

// Registry returns the Registry the Acceptor's sessions are registered in.
func (a *Acceptor) Registry() *Registry {
	return a.reg()
}

// Reload brings the acceptor's sessions in line with settings. It compares
// each session's effective settings, its global settings with its own on top,
// with those of the session that is running:
//
//   - A session only in settings is created, registered and started. A
//     listener is opened for its port if no other session uses it.
//   - A session not in settings is logged out, disconnected and unregistered.
//     Its listener is closed if no session uses it any more, unless
//     DynamicSessions is set. Its store is closed but not reset, and its log
//     is closed.
//   - A session whose settings differ is stopped as if it had been removed,
//     then started with the new settings. It keeps its store, so its sequence
//     numbers carry over, and Reload returns an error and changes nothing if
//     settings changes its FileStore or SQLStore settings. To give a session
//     a new store, remove it with one Reload and add it with the next.
//   - A session whose settings are the same is left alone and stays
//     connected.
//
// Every new and changed session is built, every new listener opened and every
// new session registered before anything is stopped. If any of that fails,
// Reload returns the error and the acceptor is left as it was. If a restarted
// session cannot be registered again once the session it replaces has
// stopped, Reload leaves it stopped, returns the error, and starts it on the
// next Reload. Reload also returns an error and changes
// nothing if settings changes a global setting that the acceptor reads once
// rather than for each session, such as SocketAcceptHost, UseTCPProxy,
// MaxMessageSize, SocketReadTimeout, DynamicSessions or the TLS settings. To
// change those, stop the acceptor and run a new one.
//
// ctx limits how long Reload waits for sessions to log out. Once it is done,
// the sessions still logging out are disconnected without waiting further,
// and Reload returns ctx's error having still applied every change.
//
// Calls to Reload run one at a time. Called before Run, Reload replaces the
// settings Run will use. Called once Run has returned, it returns an error. A
// failed Reload does not stop Run.
//
// Sessions created by DynamicSessions are not in settings, and are left
// alone. Sessions created after Reload take its global settings. They can log
// on through any of the acceptor's ports, so with DynamicSessions Reload never
// closes a listener.
//
// The acceptor keeps settings, which must not be changed afterwards.
func (a *Acceptor) Reload(ctx context.Context, settings *Settings) error {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	if a.runState == runFinished {
		return errReloadAfterRun
	}
	if err := checkUnchanged(a.settings.GlobalSettings(), settings.GlobalSettings(), acceptorEngineSettings); err != nil {
		return err
	}

	nextSettings := settings.SessionSettings()
	changes := diffSessions(a.sessionSettings, nextSettings)
	if err := checkStoreSettings(a.sessionSettings, changes); err != nil {
		return err
	}

	nextKeys := make(map[SessionID]bool, len(nextSettings))
	for sessionID := range nextSettings {
		key := acceptorKey(sessionID)
		if nextKeys[key] {
			return fmt.Errorf("%v: %w", key, errDuplicateSessionID)
		}
		nextKeys[key] = true
	}

	hostPort := make(map[SessionID]int, len(nextSettings))
	for sessionID, sessionSettings := range nextSettings {
		port, err := sessionSettings.IntSetting(config.SocketAcceptPort)
		if err != nil {
			return fmt.Errorf("session %v: %w", sessionID, err)
		}
		hostPort[sessionID] = port
	}

	current := func(sessionID SessionID) *session { return a.sessions[acceptorKey(sessionID)] }
	built, err := a.buildSessions(changes, current, a.storeFactory, a.logFactory, a.app)
	if err != nil {
		return err
	}

	// Listeners are opened only once Run has started, which opens them
	// itself.
	opened := make(map[string]net.Listener)
	if a.runState == runStarted {
		for _, port := range hostPort {
			address := a.listenAddress(port)
			if _, ok := a.listeners[address]; ok {
				continue
			}
			if _, ok := opened[address]; ok {
				continue
			}
			listener, err := a.openListener(address)
			if err != nil {
				for _, l := range opened {
					l.Close()
				}
				discardSessions(changes, built)
				return err
			}
			opened[address] = listener
		}
	}

	if err := registerAdded(a.reg(), changes, built); err != nil {
		for _, l := range opened {
			l.Close()
		}
		discardSessions(changes, built)
		return err
	}

	// Nothing can fail from here on that leaves the acceptor as it was.
	removed := make([]*session, 0, len(changes.removed))
	for _, sessionID := range changes.removed {
		removed = append(removed, a.sessions[acceptorKey(sessionID)])
	}
	old := slices.Clone(removed)
	for sessionID := range changes.changed {
		old = append(old, a.sessions[acceptorKey(sessionID)])
	}

	var errs []error
	if a.runState == runStarted {
		stopping := make([]stoppingSession, 0, len(old))
		for _, s := range old {
			s.stop()
			stopping = append(stopping, stoppingSession{session: s, done: s.done})
		}
		errs = append(errs, waitStopped(ctx, stopping))
	}
	for _, s := range old {
		if err := a.reg().UnregisterSession(s.sessionID); err != nil {
			errs = append(errs, fmt.Errorf("unregistering session %v: %w", s.sessionID, err))
		}
	}

	failed, registerErrs := registerChanged(a.reg(), changes, built, nextSettings)
	errs = append(errs, registerErrs...)
	for _, sessionID := range failed {
		delete(hostPort, sessionID)
	}
	for _, s := range built {
		announceSession(s)
	}

	a.mu.Lock()
	for _, sessionID := range changes.removed {
		delete(a.sessions, acceptorKey(sessionID))
		a.sessionAddr.Delete(acceptorKey(sessionID))
	}
	for sessionID := range changes.changed {
		delete(a.sessions, acceptorKey(sessionID))
	}
	for sessionID, s := range built {
		a.sessions[acceptorKey(sessionID)] = s
	}
	a.sessionHostPort = hostPort
	a.settings = settings
	a.sessionSettings = nextSettings
	a.mu.Unlock()

	if a.runState == runStarted {
		for _, s := range built {
			a.sessionGroup.Go(s.run)
		}
		for address, listener := range opened {
			a.listeners[address] = listener
			a.listenerShutdown.Go(func() {
				a.listenForConnections(listener)
			})
		}
		// Dynamic sessions can log on through any of the acceptor's ports,
		// so none is closed while they are enabled.
		if !a.dynamicSessions {
			inUse := make(map[string]bool, len(hostPort))
			for _, port := range hostPort {
				inUse[a.listenAddress(port)] = true
			}
			for address, listener := range a.listeners {
				if !inUse[address] {
					listener.Close()
					delete(a.listeners, address)
				}
			}
		}
	}

	for _, s := range removed {
		if err := s.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing store of session %v: %w", s.sessionID, err))
		}
	}
	// A restarted session has a log of its own, but takes over the store of
	// the session it replaces.
	for _, s := range old {
		if err := closeLog(s.log); err != nil {
			errs = append(errs, fmt.Errorf("closing log of session %v: %w", s.sessionID, err))
		}
	}

	a.globalLog.OnEventf("Reloaded settings: %d sessions added, %d removed, %d restarted",
		len(changes.added), len(changes.removed), len(changes.changed))
	return errors.Join(errs...)
}

// acceptorKey is the key the acceptor keeps the session sessionID under.
func acceptorKey(sessionID SessionID) SessionID {
	sessionID.Qualifier = ""
	return sessionID
}

// NewAcceptor creates and initializes a new Acceptor. It returns an error if
// app has an optional callback method that would never be called; see
// Application.
func NewAcceptor(app Application, storeFactory MessageStoreFactory, settings *Settings, logFactory LogFactory, opts ...Option) (a *Acceptor, err error) {
	if err = checkApplication(app); err != nil {
		return nil, err
	}

	o := newOptions(opts)
	a = &Acceptor{
		app:             app,
		storeFactory:    storeFactory,
		settings:        settings,
		sessionSettings: settings.SessionSettings(),
		logFactory:      logFactory,
		sessions:        make(map[SessionID]*session),
		sessionHostPort: make(map[SessionID]int),
		listeners:       make(map[string]net.Listener),
		sessionFactory:  sessionFactory{registry: o.registry, metricsFactory: o.metricsFactory},

		maxMessageSize:      defaultMaxMessageSize,
		firstMessageTimeout: 30 * time.Second,
	}
	if settings.GlobalSettings().HasSetting(config.MaxMessageSize) {
		if a.maxMessageSize, err = settings.GlobalSettings().IntSetting(config.MaxMessageSize); err != nil {
			return
		}
		if a.maxMessageSize <= 0 {
			return nil, errors.New("MaxMessageSize must be greater than zero")
		}
	}
	if settings.GlobalSettings().HasSetting(config.SocketReadTimeout) {
		if a.firstMessageTimeout, err = settings.GlobalSettings().DurationSetting(config.SocketReadTimeout); err != nil {
			timeout, intErr := settings.GlobalSettings().IntSetting(config.SocketReadTimeout)
			if intErr != nil {
				return nil, intErr
			}
			a.firstMessageTimeout = time.Duration(timeout) * time.Second
		}
		if a.firstMessageTimeout <= 0 {
			return nil, errors.New("SocketReadTimeout must be greater than zero")
		}
	}
	if a.settings.GlobalSettings().HasSetting(config.DynamicSessions) {
		if a.dynamicSessions, err = settings.globalSettings.BoolSetting(config.DynamicSessions); err != nil {
			return
		}

		if a.settings.GlobalSettings().HasSetting(config.DynamicQualifier) {
			if a.dynamicQualifier, err = settings.globalSettings.BoolSetting(config.DynamicQualifier); err != nil {
				return
			}
		}
	}

	if a.globalLog, err = logFactory.Create(settings.GlobalSettings()); err != nil {
		return
	}
	globalRedactor, err := redactorFromSettings(settings.GlobalSettings())
	if err != nil {
		return nil, err
	}
	a.globalLog = newRedactingLog(a.globalLog, globalRedactor)

	for sessionID, sessionSettings := range settings.SessionSettings() {
		sessID := sessionID
		sessID.Qualifier = ""

		if _, dup := a.sessions[sessID]; dup {
			return a, errDuplicateSessionID
		}

		if a.sessions[sessID], err = a.createSession(sessionID, storeFactory, sessionSettings, logFactory, app); err != nil {
			return
		}
	}

	return
}

func (a *Acceptor) listenForConnections(listener net.Listener) {
	for {
		netConn, err := listener.Accept()
		if err != nil {
			return
		}

		a.connectionGroup.Go(func() {
			a.handleConnection(netConn)
		})
	}
}

func (a *Acceptor) invalidMessage(msg *bytes.Buffer, err error) {
	a.globalLog.OnEventf("Invalid Message: %s, %v", msg.Bytes(), err.Error())
}

func (a *Acceptor) handleConnection(netConn net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			a.globalLog.OnEventf("Connection Terminated with Panic: %s", debug.Stack())
		}

		// writeLoop closes the connection itself if a write fails.
		if err := netConn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			a.globalLog.OnEvent(err.Error())
		}
	}()

	// Until a session takes over the connection nothing else would unblock
	// reading the first message when the acceptor stops.
	unwatch := context.AfterFunc(a.stopCtx, func() {
		netConn.Close()
	})
	defer unwatch()

	reader := bufio.NewReader(netConn)
	parser := newParserWithLimit(reader, a.maxMessageSize)
	// A malformed first message (including a bad logon) closes the connection;
	// it must not leave the acceptor waiting for another one.
	parser.resync = false

	if a.firstMessageTimeout > 0 {
		if err := netConn.SetReadDeadline(time.Now().Add(a.firstMessageTimeout)); err != nil {
			a.globalLog.OnEvent(err.Error())
			return
		}
	}

	msgBytes, err := parser.ReadMessage()
	if err != nil {
		if err == io.EOF {
			a.globalLog.OnEvent("Connection Terminated")
		} else {
			a.globalLog.OnEvent(err.Error())
		}
		return
	}
	if err := netConn.SetReadDeadline(time.Time{}); err != nil {
		a.globalLog.OnEvent(err.Error())
		return
	}

	msg := NewMessage()
	err = ParseMessage(msg, msgBytes)
	if err != nil {
		a.invalidMessage(msgBytes, err)
		return
	}

	var beginString FIXString
	if err := msg.Header.GetField(tagBeginString, &beginString); err != nil {
		a.invalidMessage(msgBytes, err)
		return
	}

	var senderCompID FIXString
	if err := msg.Header.GetField(tagSenderCompID, &senderCompID); err != nil {
		a.invalidMessage(msgBytes, err)
		return
	}

	var senderSubID FIXString
	if msg.Header.Has(tagSenderSubID) {
		if err := msg.Header.GetField(tagSenderSubID, &senderSubID); err != nil {
			a.invalidMessage(msgBytes, err)
			return
		}
	}

	var senderLocationID FIXString
	if msg.Header.Has(tagSenderLocationID) {
		if err := msg.Header.GetField(tagSenderLocationID, &senderLocationID); err != nil {
			a.invalidMessage(msgBytes, err)
			return
		}
	}

	var targetCompID FIXString
	if err := msg.Header.GetField(tagTargetCompID, &targetCompID); err != nil {
		a.invalidMessage(msgBytes, err)
		return
	}

	var targetSubID FIXString
	if msg.Header.Has(tagTargetSubID) {
		if err := msg.Header.GetField(tagTargetSubID, &targetSubID); err != nil {
			a.invalidMessage(msgBytes, err)
			return
		}
	}

	var targetLocationID FIXString
	if msg.Header.Has(tagTargetLocationID) {
		if err := msg.Header.GetField(tagTargetLocationID, &targetLocationID); err != nil {
			a.invalidMessage(msgBytes, err)
			return
		}
	}

	sessID := SessionID{BeginString: string(beginString),
		SenderCompID: string(targetCompID), SenderSubID: string(targetSubID), SenderLocationID: string(targetLocationID),
		TargetCompID: string(senderCompID), TargetSubID: string(senderSubID), TargetLocationID: string(senderLocationID),
	}

	localConnectionPort := netConn.LocalAddr().(*net.TCPAddr).Port
	// The port, session and settings are read together so that they come
	// from the same Reload.
	a.mu.RLock()
	expectedPort, hasPort := a.sessionHostPort[sessID]
	session, ok := a.sessions[sessID]
	// Reload replaces settings rather than changing it.
	globalSettings := a.settings.globalSettings
	a.mu.RUnlock()
	if hasPort && expectedPort != localConnectionPort {
		a.globalLog.OnEventf("Session %v not found for incoming message: %s", sessID, msgBytes)
		return
	}

	// We have a session ID and a network connection. This seems to be a good place for any custom authentication logic.
	if a.connectionValidator != nil {
		if err := a.connectionValidator.Validate(netConn, sessID); err != nil {
			a.globalLog.OnEventf("Unable to validate a connection for session %v: %v", sessID, err.Error())
			return
		}
	}

	if a.dynamicQualifier {
		sessID.Qualifier = strconv.FormatInt(a.dynamicQualifierCount.Add(1), 10)
		// The acceptor keys its sessions without their qualifier, so a
		// qualified session is always a new dynamic one.
		session, ok = nil, false
	}
	if !ok {
		if !a.dynamicSessions {
			a.globalLog.OnEventf("Session %v not found for incoming message: %s", sessID, msgBytes)
			return
		}
		if err := checkDynamicSessionID(sessID); err != nil {
			a.globalLog.OnEventf("Dynamic session refused: %v", err)
			return
		}
		dynamicSession, err := a.sessionFactory.createSession(sessID, a.storeFactory, globalSettings.clone(), a.logFactory, a.app)
		if err != nil {
			a.globalLog.OnEventf("Dynamic session %v failed to create: %v", sessID, err)
			return
		}
		select {
		case a.dynamicSessionChan <- dynamicSession:
		case <-a.stopCtx.Done():
			if err := a.reg().UnregisterSession(sessID); err != nil {
				a.globalLog.OnEventf("Unregister dynamic session %v failed: %v", sessID, err)
			}
			return
		}
		session = dynamicSession
		defer session.stop()
	}

	if session.MaxMessageSize > 0 {
		if msgBytes.Len() > session.MaxMessageSize {
			a.globalLog.OnEventf("Session %v first message exceeds MaxMessageSize", sessID)
			return
		}
		parser.maxMessageSize = session.MaxMessageSize
	}
	parser.resync = true

	a.sessionAddr.Store(sessID, netConn.RemoteAddr())
	msgIn := make(chan fixIn, session.InChanCapacity)
	msgInDone := make(chan struct{})
	msgOut := make(chan []byte, messageOutCapacity)

	if err := session.connect(msgIn, msgInDone, msgOut); err != nil {
		a.globalLog.OnEventf("Unable to accept session %v connection: %v", sessID, err.Error())
		return
	}
	// The session now closes the connection by disconnecting, which lets it
	// log out cleanly when the acceptor stops.
	unwatch()

	go func() {
		if !sendMessageIn(msgIn, msgInDone, fixIn{bytes: msgBytes, receiveTime: parser.lastRead, pooled: true}) {
			close(msgIn)
			return
		}
		readLoop(parser, msgIn, msgInDone, a.globalLog, session.metrics)
	}()

	writeLoop(netConn, msgOut, a.globalLog, session.MaxMessagesPerSecond, session.WriteTimeout, session.metrics, session.writerReady)
}

func (a *Acceptor) dynamicSessionsLoop() {
	var id int
	var sessions = map[int]*session{}
	var complete = make(chan int)
	defer close(complete)
LOOP:
	for {
		select {
		case <-a.stopCtx.Done():
			for _, oldSession := range sessions {
				oldSession.stop()
			}
			break LOOP
		case session := <-a.dynamicSessionChan:
			id++
			sessionID := id
			sessions[sessionID] = session
			go func() {
				session.run()
				if err := a.reg().UnregisterSession(session.sessionID); err != nil {
					a.globalLog.OnEventf("Unregister dynamic session %v failed: %v", session.sessionID, err)
				}
				complete <- sessionID
			}()
		case id := <-complete:
			session, ok := sessions[id]
			if ok {
				a.sessionAddr.Delete(session.sessionID)
				delete(sessions, id)
			} else {
				a.globalLog.OnEventf("Missing dynamic session %v!", id)
			}
		}
	}

	if len(sessions) == 0 {
		return
	}

	for id := range complete {
		delete(sessions, id)
		if len(sessions) == 0 {
			return
		}
	}
}

// SetConnectionValidator sets an optional connection validator.
// Use it when you need a custom authentication logic that includes lower level interactions,
// like mTLS auth or IP whitelistening.
// To remove a previously set validator call it with a nil value:
//
//	a.SetConnectionValidator(nil)
func (a *Acceptor) SetConnectionValidator(validator ConnectionValidator) {
	a.connectionValidator = validator
}

// SetTLSConfig allows the creator of the Acceptor to specify a fully customizable tls.Config of their choice,
// which will be used by Run.
//
// Note: when the caller explicitly provides a tls.Config with this function,
// it takes precendent over TLS settings specified in the acceptor's settings.GlobalSettings(),
// meaning that the `settings.GlobalSettings()` object is not inspected or used for the creation of the tls.Config.
func (a *Acceptor) SetTLSConfig(tlsConfig *tls.Config) {
	a.tlsConfig = tlsConfig
}

// SetNewListenerCallback allows the creator of the Acceptor to specify the callback used to create each net.Listener
// which will be used by Run.
func (a *Acceptor) SetNewListenerCallback(cb NewListenerCallback) {
	a.newListenerCallback = cb
}

// maxDynamicSessionIDLength is the longest BeginString, CompID, SubID or
// LocationID with which a peer may create a dynamic session.
const maxDynamicSessionIDLength = 64

// checkDynamicSessionID returns an error unless each part of id, taken from a
// peer's Logon, is made up of letters, digits, '.', '_' and '-', and is no
// longer than maxDynamicSessionIDLength. The SessionID is passed to the store
// and log factories, which may use it to name files, so a peer must not be
// able to choose one that names a path.
func checkDynamicSessionID(id SessionID) error {
	for _, part := range []struct {
		name, value string
		required    bool
	}{
		{"BeginString", id.BeginString, true},
		{"SenderCompID", id.SenderCompID, true},
		{"SenderSubID", id.SenderSubID, false},
		{"SenderLocationID", id.SenderLocationID, false},
		{"TargetCompID", id.TargetCompID, true},
		{"TargetSubID", id.TargetSubID, false},
		{"TargetLocationID", id.TargetLocationID, false},
	} {
		switch {
		case part.value == "" && part.required:
			return fmt.Errorf("%s is empty", part.name)
		case len(part.value) > maxDynamicSessionIDLength:
			return fmt.Errorf("%s is longer than %d bytes", part.name, maxDynamicSessionIDLength)
		}
		for i := range len(part.value) {
			c := part.value[i]
			if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || c == '.' || c == '_' || c == '-' {
				continue
			}
			return fmt.Errorf("%s %q contains %q", part.name, part.value, c)
		}
	}
	return nil
}
