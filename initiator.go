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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Initiator initiates connections and processes messages for all sessions.
type Initiator struct {
	app             Application
	settings        *Settings
	sessionSettings map[SessionID]*SessionSettings
	storeFactory    MessageStoreFactory
	logFactory      LogFactory
	globalLog       Log
	lifecycle       lifecycle
	wg              sync.WaitGroup
	sessions        map[SessionID]*session
	sessionFactory
}

// initiatorConfig is what an initiator needs to connect a session.
type initiatorConfig struct {
	tlsConfig        *tls.Config
	dialer           proxy.ContextDialer
	handshakeTimeout time.Duration
}

// Run connects the initiator's sessions, and reconnects them whenever they
// disconnect, until ctx is done. It then logs out the sessions, closes their
// connections and returns. It returns nil once it has shut down because ctx is
// done, or the error that stopped it starting, in which case no session was
// started and Run may be called again.
//
// Run returns ctx's error, starting nothing, if ctx is already done, and an
// error if it is already running or has run.
func (i *Initiator) Run(ctx context.Context) error {
	if err := i.lifecycle.begin(ctx); err != nil {
		return err
	}

	// Every session's settings are loaded before any session starts, so that
	// a bad setting leaves nothing running.
	configs := make(map[SessionID]initiatorConfig, len(i.sessionSettings))
	for sessionID, settings := range i.sessionSettings {
		// TODO: move into session factory.
		var cfg initiatorConfig
		var err error
		if cfg.tlsConfig, err = loadTLSConfig(settings); err != nil {
			i.lifecycle.abort()
			return err
		}
		if cfg.dialer, err = loadDialerConfig(settings); err != nil {
			i.lifecycle.abort()
			return err
		}
		if cfg.handshakeTimeout, err = loadTLSHandshakeTimeout(settings); err != nil {
			i.lifecycle.abort()
			return err
		}
		configs[sessionID] = cfg
	}

	for sessionID, cfg := range configs {
		i.wg.Go(func() {
			i.handleConnection(ctx, i.sessions[sessionID], cfg.tlsConfig, cfg.dialer, cfg.handshakeTimeout)
		})
	}

	// Each session logs out and disconnects once ctx is done.
	i.wg.Wait()

	var errs []error
	for sessionID := range i.sessionSettings {
		if err := i.reg().UnregisterSession(sessionID); err != nil {
			errs = append(errs, fmt.Errorf("unregistering session %v: %w", sessionID, err))
		}
	}
	return errors.Join(errs...)
}

// Registry returns the Registry the Initiator's sessions are registered in.
func (i *Initiator) Registry() *Registry {
	return i.reg()
}

// NewInitiator creates and initializes a new Initiator. It returns an error if
// app has an optional callback method that would never be called; see
// Application.
func NewInitiator(app Application, storeFactory MessageStoreFactory, appSettings *Settings, logFactory LogFactory, opts ...Option) (*Initiator, error) {
	if err := checkApplication(app); err != nil {
		return nil, err
	}

	o := newOptions(opts)
	i := &Initiator{
		app:             app,
		storeFactory:    storeFactory,
		settings:        appSettings,
		sessionSettings: appSettings.SessionSettings(),
		logFactory:      logFactory,
		sessions:        make(map[SessionID]*session),
		sessionFactory:  sessionFactory{BuildInitiators: true, registry: o.registry, metricsFactory: o.metricsFactory},
	}

	var err error
	i.globalLog, err = logFactory.Create()
	if err != nil {
		return i, err
	}

	for sessionID, s := range i.sessionSettings {
		session, err := i.createSession(sessionID, storeFactory, s, logFactory, app)
		if err != nil {
			return nil, err
		}

		i.sessions[sessionID] = session
	}

	return i, nil
}

// waitForInSessionTime returns true if the session is in session, false if the handler should stop.
func (i *Initiator) waitForInSessionTime(ctx context.Context, session *session) bool {
	inSessionTime := make(chan any)
	go func() {
		session.waitForInSessionTime()
		close(inSessionTime)
	}()

	select {
	case <-inSessionTime:
	case <-ctx.Done():
		return false
	}

	return true
}

// waitForReconnectInterval returns true if a reconnect should be re-attempted, false if handler should stop.
func (i *Initiator) waitForReconnectInterval(ctx context.Context, reconnectInterval time.Duration) bool {
	select {
	case <-time.After(reconnectInterval):
	case <-ctx.Done():
		return false
	}

	return true
}

// sessionConn is a connection an initiator has connected a session to, with
// the channels the session uses to send and receive on it.
type sessionConn struct {
	net.Conn
	msgIn     chan fixIn
	msgInDone chan struct{}
	msgOut    chan []byte
}

// connectSession dials address, completes a TLS handshake within
// handshakeTimeout if tlsConfig is set, and connects session to the
// connection. If any step fails it logs why to the session log, closes the
// connection if one was dialed, and returns nil. Cancelling ctx abandons a dial
// or handshake in progress.
func connectSession(ctx context.Context, session *session, dialer proxy.ContextDialer, address string, tlsConfig *tls.Config, handshakeTimeout time.Duration) *sessionConn {
	netConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		session.log.OnEventf("Failed to connect: %v", err)
		return nil
	}

	closeConn := func() {
		if err := netConn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			session.log.OnEvent(err.Error())
		}
	}

	if tlsConfig != nil {
		tlsConn, err := clientHandshake(ctx, netConn, address, tlsConfig, handshakeTimeout)
		if err != nil {
			session.log.OnEventf("Failed handshake: %v", err)
			closeConn()
			return nil
		}
		netConn = tlsConn
	}

	conn := &sessionConn{
		Conn:      netConn,
		msgIn:     make(chan fixIn, session.InChanCapacity),
		msgInDone: make(chan struct{}),
		msgOut:    make(chan []byte, messageOutCapacity),
	}
	if err := session.connect(conn.msgIn, conn.msgInDone, conn.msgOut); err != nil {
		session.log.OnEventf("Failed to initiate: %v", err)
		closeConn()
		return nil
	}

	return conn
}

// clientHandshake performs a TLS client handshake over netConn, which was
// dialed to address, giving up after timeout if it is positive. Unless
// tlsConfig skips verification or names a server, the server's certificate is
// verified against the host in address.
func clientHandshake(ctx context.Context, netConn net.Conn, address string, tlsConfig *tls.Config, timeout time.Duration) (*tls.Conn, error) {
	// tlsConfig is shared by every attempt, which may dial different
	// addresses, so the server name is set on a copy.
	if !tlsConfig.InsecureSkipVerify && tlsConfig.ServerName == "" {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}
		tlsConfig = tlsConfig.Clone()
		tlsConfig.ServerName = host
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	tlsConn := tls.Client(netConn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return tlsConn, nil
}

// handleConnection connects session, and reconnects it whenever it
// disconnects, until stopCtx is done.
func (i *Initiator) handleConnection(stopCtx context.Context, session *session, tlsConfig *tls.Config, dialer proxy.ContextDialer, handshakeTimeout time.Duration) {
	var wg sync.WaitGroup
	wg.Go(func() {
		session.run()
	})

	defer func() {
		session.stop()
		wg.Wait()
	}()

	connectionAttempt := 0

	for {
		if !i.waitForInSessionTime(stopCtx, session) {
			return
		}

		// Stopping cancels a dial or TLS handshake in progress.
		ctx, cancel := context.WithCancel(stopCtx)

		var disconnected chan any

		address := session.SocketConnectAddress[connectionAttempt%len(session.SocketConnectAddress)]
		session.log.OnEventf("Connecting to: %v", address)

		conn := connectSession(ctx, session, dialer, address, tlsConfig, handshakeTimeout)
		if conn == nil {
			goto reconnect
		}

		go readLoop(newParserWithLimit(bufio.NewReader(conn), session.MaxMessageSize), conn.msgIn, conn.msgInDone, session.log, session.metrics)
		disconnected = make(chan any)
		go func() {
			writeLoop(conn, conn.msgOut, session.log, session.MaxMessagesPerSecond, session.WriteTimeout, session.metrics, session.writerReady)
			// writeLoop closes the connection itself if a write fails.
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				session.log.OnEvent(err.Error())
			}
			close(disconnected)
		}()

		// The connection no longer needs ctx.
		cancel()

		select {
		case <-disconnected:
		case <-stopCtx.Done():
			return
		}

	reconnect:
		cancel()

		connectionAttempt++
		session.log.OnEventf("Reconnecting in %v", session.ReconnectInterval)
		if !i.waitForReconnectInterval(stopCtx, session.ReconnectInterval) {
			return
		}
	}
}
