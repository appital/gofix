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
	stopFunc              context.CancelFunc
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

// Start accepting connections.
func (a *Acceptor) Start() (err error) {
	socketAcceptHost := ""
	if a.settings.GlobalSettings().HasSetting(config.SocketAcceptHost) {
		if socketAcceptHost, err = a.settings.GlobalSettings().Setting(config.SocketAcceptHost); err != nil {
			return
		}
	}

	a.sessionHostPort = make(map[SessionID]int)
	a.listeners = make(map[string]net.Listener)
	for sessionID, sessionSettings := range a.settings.SessionSettings() {
		if sessionSettings.HasSetting(config.SocketAcceptPort) {
			if a.sessionHostPort[sessionID], err = sessionSettings.IntSetting(config.SocketAcceptPort); err != nil {
				return
			}
		} else if a.sessionHostPort[sessionID], err = a.settings.GlobalSettings().IntSetting(config.SocketAcceptPort); err != nil {
			return
		}
		address := net.JoinHostPort(socketAcceptHost, strconv.Itoa(a.sessionHostPort[sessionID]))
		a.listeners[address] = nil
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

	var useTCPProxy bool
	if a.settings.GlobalSettings().HasSetting(config.UseTCPProxy) {
		if useTCPProxy, err = a.settings.GlobalSettings().BoolSetting(config.UseTCPProxy); err != nil {
			return
		}
	}

	for address := range a.listeners {
		if a.listeners[address], err = a.newListenerCallback(address, a.tlsConfig); err != nil {
			return
		} else if useTCPProxy {
			a.listeners[address] = &proxyproto.Listener{Listener: a.listeners[address]}
		}
	}

	a.stopCtx, a.stopFunc = context.WithCancel(context.Background())
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

// Stop logs out existing sessions, close their connections, and stop accepting new connections.
func (a *Acceptor) Stop() {
	for _, listener := range a.listeners {
		listener.Close()
	}
	// Once the listeners have returned no new connections can be handled.
	a.listenerShutdown.Wait()

	// Stopping closes connections still waiting for their first message and
	// stops the dynamic sessions.
	if a.stopFunc != nil {
		a.stopFunc()
	}
	for _, session := range a.sessions {
		session.stop()
	}
	// Each connection returns once its session has disconnected.
	a.connectionGroup.Wait()
	a.sessionGroup.Wait()

	for sessionID := range a.sessions {
		err := a.reg().UnregisterSession(sessionID)
		if err != nil {
			return
		}
	}
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

	if a.globalLog, err = logFactory.Create(); err != nil {
		return
	}

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

		if err := netConn.Close(); err != nil {
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
	if expectedPort, ok := a.sessionHostPort[sessID]; ok && expectedPort != localConnectionPort {
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
	}
	session, ok := a.sessions[sessID]
	if !ok {
		if !a.dynamicSessions {
			a.globalLog.OnEventf("Session %v not found for incoming message: %s", sessID, msgBytes)
			return
		}
		if err := checkDynamicSessionID(sessID); err != nil {
			a.globalLog.OnEventf("Dynamic session refused: %v", err)
			return
		}
		dynamicSession, err := a.sessionFactory.createSession(sessID, a.storeFactory, a.settings.globalSettings.clone(), a.logFactory, a.app)
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
	msgOut := make(chan []byte, messageOutCapacity)

	if err := session.connect(msgIn, msgOut); err != nil {
		a.globalLog.OnEventf("Unable to accept session %v connection: %v", sessID, err.Error())
		return
	}
	// The session now closes the connection by disconnecting, which lets it
	// log out cleanly when the acceptor stops.
	unwatch()

	go func() {
		msgIn <- fixIn{bytes: msgBytes, receiveTime: parser.lastRead, pooled: true}
		readLoop(parser, msgIn, a.globalLog, session.metrics)
	}()

	writeLoop(netConn, msgOut, a.globalLog, session.MaxMessagesPerSecond, session.metrics, session.writerReady)
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
// which will be used in the Start() method.
//
// Note: when the caller explicitly provides a tls.Config with this function,
// it takes precendent over TLS settings specified in the acceptor's settings.GlobalSettings(),
// meaning that the `settings.GlobalSettings()` object is not inspected or used for the creation of the tls.Config.
func (a *Acceptor) SetTLSConfig(tlsConfig *tls.Config) {
	a.tlsConfig = tlsConfig
}

// SetNewListenerCallback allows the creator of the Acceptor to specify the callback used to create each net.Listener
// which will be used in the Start() method.
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
