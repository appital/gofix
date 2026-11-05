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
	stopChan        chan any
	wg              sync.WaitGroup
	sessions        map[SessionID]*session
	sessionFactory
}

// Start Initiator.
func (i *Initiator) Start() (err error) {
	i.stopChan = make(chan any)

	for sessionID, settings := range i.sessionSettings {
		// TODO: move into session factory.
		var tlsConfig *tls.Config
		if tlsConfig, err = loadTLSConfig(settings); err != nil {
			return
		}

		var dialer proxy.ContextDialer
		if dialer, err = loadDialerConfig(settings); err != nil {
			return
		}

		var handshakeTimeout time.Duration
		if handshakeTimeout, err = loadTLSHandshakeTimeout(settings); err != nil {
			return
		}

		i.wg.Go(func() {
			i.handleConnection(i.sessions[sessionID], tlsConfig, dialer, handshakeTimeout)
		})
	}
	return
}

// Stop Initiator.
func (i *Initiator) Stop() {
	select {
	case <-i.stopChan:
		// Closed already.
		return
	default:
	}
	close(i.stopChan)

	i.wg.Wait()

	for sessionID := range i.sessionSettings {
		err := i.reg().UnregisterSession(sessionID)
		if err != nil {
			return
		}
	}
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
func (i *Initiator) waitForInSessionTime(session *session) bool {
	inSessionTime := make(chan any)
	go func() {
		session.waitForInSessionTime()
		close(inSessionTime)
	}()

	select {
	case <-inSessionTime:
	case <-i.stopChan:
		return false
	}

	return true
}

// waitForReconnectInterval returns true if a reconnect should be re-attempted, false if handler should stop.
func (i *Initiator) waitForReconnectInterval(reconnectInterval time.Duration) bool {
	select {
	case <-time.After(reconnectInterval):
	case <-i.stopChan:
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

func (i *Initiator) handleConnection(session *session, tlsConfig *tls.Config, dialer proxy.ContextDialer, handshakeTimeout time.Duration) {
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
		if !i.waitForInSessionTime(session) {
			return
		}

		ctx, cancel := context.WithCancel(context.Background())

		// We start a goroutine in order to be able to cancel the dialer mid-connection
		// on receiving a stop signal to stop the initiator.
		go func() {
			select {
			case <-i.stopChan:
				cancel()
			case <-ctx.Done():
				return
			}
		}()

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

		// This ensures we properly cleanup the goroutine and context used for
		// dial cancelation after successful connection.
		cancel()

		select {
		case <-disconnected:
		case <-i.stopChan:
			return
		}

	reconnect:
		cancel()

		connectionAttempt++
		session.log.OnEventf("Reconnecting in %v", session.ReconnectInterval)
		if !i.waitForReconnectInterval(session.ReconnectInterval) {
			return
		}
	}
}
