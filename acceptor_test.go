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
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appital/gofix/config"

	proxyproto "github.com/pires/go-proxyproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptor_Start(t *testing.T) {
	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, BeginStringFIX42)
	sessionSettings.Set(config.SenderCompID, "sender")
	sessionSettings.Set(config.TargetCompID, "target")

	settingsWithTCPProxy := NewSettings()
	settingsWithTCPProxy.GlobalSettings().Set("UseTCPProxy", "Y")

	settingsWithNoTCPProxy := NewSettings()
	settingsWithNoTCPProxy.GlobalSettings().Set("UseTCPProxy", "N")

	genericSettings := NewSettings()

	const (
		GenericListener = iota
		ProxyListener
	)

	acceptorStartTests := []struct {
		name         string
		settings     *Settings
		listenerType int
	}{
		{"with TCP proxy set", settingsWithTCPProxy, ProxyListener},
		{"with no TCP proxy set", settingsWithNoTCPProxy, GenericListener},
		{"no TCP proxy configuration set", genericSettings, GenericListener},
	}

	for _, tt := range acceptorStartTests {
		t.Run(tt.name, func(t *testing.T) {
			tt.settings.GlobalSettings().Set("SocketAcceptPort", "5001")
			if _, err := tt.settings.AddSession(sessionSettings); err != nil {
				assert.Nil(t, err)
			}

			acceptor := &Acceptor{settings: tt.settings}
			if err := acceptor.Start(); err != nil {
				assert.NotNil(t, err)
			}
			assert.Len(t, acceptor.listeners, 1)

			for _, listener := range acceptor.listeners {
				if tt.listenerType == ProxyListener {
					_, ok := listener.(*proxyproto.Listener)
					assert.True(t, ok)
				}

				if tt.listenerType == GenericListener {
					_, ok := listener.(*net.TCPListener)
					assert.True(t, ok)
				}
			}

			acceptor.Stop()
		})
	}
}

func TestAcceptor_SetTLSConfig(t *testing.T) {
	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, BeginStringFIX42)
	sessionSettings.Set(config.SenderCompID, "sender")
	sessionSettings.Set(config.TargetCompID, "target")

	genericSettings := NewSettings()

	genericSettings.GlobalSettings().Set("SocketAcceptPort", "5001")
	_, err := genericSettings.AddSession(sessionSettings)
	require.NoError(t, err)

	logger, err := NewNullLogFactory().Create()
	require.NoError(t, err)
	acceptor := &Acceptor{settings: genericSettings, globalLog: logger}
	defer acceptor.Stop()
	// example of a customized tls.Config that loads the certificates dynamically by the `GetCertificate` function
	// as opposed to the Certificates slice, that is static in nature, and is only populated once and needs application restart to reload the certs.
	customizedTLSConfig := tls.Config{
		Certificates: []tls.Certificate{},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair("_test_data/localhost.crt", "_test_data/localhost.key")
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}

	acceptor.SetTLSConfig(&customizedTLSConfig)
	assert.NoError(t, acceptor.Start())
	assert.Len(t, acceptor.listeners, 1)

	conn, err := tls.Dial("tcp", "localhost:5001", &tls.Config{
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)
	assert.NotNil(t, conn)
	defer conn.Close()
}

func TestAcceptor_SetCallback(t *testing.T) {
	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, BeginStringFIX42)
	sessionSettings.Set(config.SenderCompID, "sender")
	sessionSettings.Set(config.TargetCompID, "target")

	genericSettings := NewSettings()

	genericSettings.GlobalSettings().Set("SocketAcceptPort", "5001")
	_, err := genericSettings.AddSession(sessionSettings)
	require.NoError(t, err)

	logger, err := NewNullLogFactory().Create()
	require.NoError(t, err)
	acceptor := &Acceptor{settings: genericSettings, globalLog: logger}
	defer acceptor.Stop()
	// example of a customized tls.Config that loads the certificates dynamically by the `GetCertificate` function
	// as opposed to the Certificates slice, that is static in nature, and is only populated once and needs application restart to reload the certs.
	customizedTLSConfig := tls.Config{
		Certificates: []tls.Certificate{},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair("_test_data/localhost.crt", "_test_data/localhost.key")
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}

	didUseCallback := false
	acceptor.SetTLSConfig(&customizedTLSConfig)
	acceptor.SetNewListenerCallback(func(address string, tlsConfig *tls.Config) (net.Listener, error) {
		didUseCallback = true
		assert.Equal(t, &customizedTLSConfig, tlsConfig)
		return tls.Listen("tcp", address, tlsConfig)
	})
	assert.NoError(t, acceptor.Start())
	assert.Len(t, acceptor.listeners, 1)

	conn, err := tls.Dial("tcp", "localhost:5001", &tls.Config{
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)
	assert.NotNil(t, conn)
	assert.True(t, didUseCallback)
	defer conn.Close()
}

func TestAcceptor_TCPProxy(t *testing.T) {
	const proxyHeader = "PROXY TCP4 192.0.2.10 127.0.0.1 40000 5001\r\n"

	logon := func() []byte {
		msg := NewMessage()
		msg.Header.
			SetField(tagBeginString, FIXString(BeginStringFIX42)).
			SetField(tagSenderCompID, FIXString("target")).
			SetField(tagTargetCompID, FIXString("sender")).
			SetField(tagSendingTime, FIXUTCTimestamp{Time: time.Now()}).
			SetField(tagMsgSeqNum, FIXInt(1)).
			SetField(tagMsgType, FIXString(msgTypeLogon))
		msg.Body.
			SetField(tagEncryptMethod, FIXString("0")).
			SetField(tagHeartBtInt, FIXInt(30))
		return msg.build()
	}

	startAcceptor := func(t *testing.T) (*Acceptor, SessionID) {
		sessionSettings := NewSessionSettings()
		sessionSettings.Set(config.BeginString, BeginStringFIX42)
		sessionSettings.Set(config.SenderCompID, "sender")
		sessionSettings.Set(config.TargetCompID, "target")
		sessionSettings.Set(config.HeartBtInt, "30")

		settings := NewSettings()
		settings.GlobalSettings().Set(config.SocketAcceptPort, "5001")
		settings.GlobalSettings().Set(config.UseTCPProxy, "Y")
		sessionID, err := settings.AddSession(sessionSettings)
		require.NoError(t, err)

		acceptor, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), settings, NewNullLogFactory())
		require.NoError(t, err)
		require.NoError(t, acceptor.Start())
		t.Cleanup(acceptor.Stop)
		return acceptor, sessionID
	}

	t.Run("connection with PROXY header reports the client address", func(t *testing.T) {
		acceptor, sessionID := startAcceptor(t)

		conn, err := net.Dial("tcp", "127.0.0.1:5001")
		require.NoError(t, err)
		defer conn.Close()
		_, err = conn.Write(append([]byte(proxyHeader), logon()...))
		require.NoError(t, err)

		assert.Eventually(t, func() bool {
			addr, ok := acceptor.RemoteAddr(sessionID)
			return ok && addr.String() == "192.0.2.10:40000"
		}, 2*time.Second, 10*time.Millisecond)
	})

	t.Run("connection without PROXY header is rejected", func(t *testing.T) {
		acceptor, sessionID := startAcceptor(t)

		conn, err := net.Dial("tcp", "127.0.0.1:5001")
		require.NoError(t, err)
		defer conn.Close()
		_, err = conn.Write(logon())
		require.NoError(t, err)

		// The acceptor closes the connection without answering the logon.
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		_, err = conn.Read(make([]byte, 1))
		assert.ErrorIs(t, err, io.EOF)

		_, ok := acceptor.RemoteAddr(sessionID)
		assert.False(t, ok)
	})
}

// startDynamicAcceptor starts an acceptor on port 5001 that accepts dynamic
// sessions, giving each one its own qualifier.
func startDynamicAcceptor(t *testing.T) *Acceptor {
	t.Helper()

	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, BeginStringFIX42)
	sessionSettings.Set(config.SenderCompID, "sender")
	sessionSettings.Set(config.TargetCompID, "target")

	settings := NewSettings()
	settings.GlobalSettings().Set(config.SocketAcceptPort, "5001")
	settings.GlobalSettings().Set(config.HeartBtInt, "30")
	settings.GlobalSettings().Set(config.LogoutTimeout, "1")
	settings.GlobalSettings().Set(config.DynamicSessions, "Y")
	settings.GlobalSettings().Set(config.DynamicQualifier, "Y")
	_, err := settings.AddSession(sessionSettings)
	require.NoError(t, err)

	acceptor, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), settings, NewNullLogFactory())
	require.NoError(t, err)
	require.NoError(t, acceptor.Start())
	return acceptor
}

// dynamicLogon builds a logon for a session the acceptor only knows dynamically.
func dynamicLogon() []byte {
	msg := NewMessage()
	msg.Header.
		SetField(tagBeginString, FIXString(BeginStringFIX42)).
		SetField(tagSenderCompID, FIXString("dynamic-client")).
		SetField(tagTargetCompID, FIXString("dynamic-server")).
		SetField(tagSendingTime, FIXUTCTimestamp{Time: time.Now()}).
		SetField(tagMsgSeqNum, FIXInt(1)).
		SetField(tagMsgType, FIXString(msgTypeLogon))
	msg.Body.
		SetField(tagEncryptMethod, FIXString("0")).
		SetField(tagHeartBtInt, FIXInt(30))
	return msg.build()
}

func TestAcceptor_DynamicQualifierConcurrentLogons(t *testing.T) {
	const connections = 20

	acceptor := startDynamicAcceptor(t)
	t.Cleanup(acceptor.Stop)

	// Every connection logs on with the same CompIDs at once, so each one relies
	// on the dynamic qualifier to get its own session.
	var wg sync.WaitGroup
	for range connections {
		wg.Go(func() {
			conn, err := net.Dial("tcp", "127.0.0.1:5001")
			if !assert.NoError(t, err) {
				return
			}
			t.Cleanup(func() { conn.Close() })
			_, err = conn.Write(dynamicLogon())
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	assert.Eventually(t, func() bool {
		for q := 1; q <= connections; q++ {
			sessionID := SessionID{
				BeginString:  BeginStringFIX42,
				SenderCompID: "dynamic-server",
				TargetCompID: "dynamic-client",
				Qualifier:    strconv.Itoa(q),
			}
			if _, ok := acceptor.RemoteAddr(sessionID); !ok {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond)
}

func TestAcceptor_StopClosesConnections(t *testing.T) {
	acceptor := startDynamicAcceptor(t)

	// One connection never sends a logon, so it is still waiting for its first
	// message when the acceptor stops.
	idle, err := net.Dial("tcp", "127.0.0.1:5001")
	require.NoError(t, err)
	defer idle.Close()

	loggedOn, err := net.Dial("tcp", "127.0.0.1:5001")
	require.NoError(t, err)
	defer loggedOn.Close()
	_, err = loggedOn.Write(dynamicLogon())
	require.NoError(t, err)

	sessionID := SessionID{
		BeginString:  BeginStringFIX42,
		SenderCompID: "dynamic-server",
		TargetCompID: "dynamic-client",
		Qualifier:    "1",
	}
	require.Eventually(t, func() bool {
		_, ok := acceptor.RemoteAddr(sessionID)
		return ok
	}, 2*time.Second, 10*time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		acceptor.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}

	// Stop only returns once every connection has been handled, so both have
	// been closed by now.
	for _, conn := range []net.Conn{idle, loggedOn} {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		_, err := io.Copy(io.Discard, conn)
		assert.NoError(t, err, "connection should have been closed by the acceptor")
	}
}

func TestCheckDynamicSessionID(t *testing.T) {
	valid := SessionID{BeginString: BeginStringFIX42, SenderCompID: "SERVER", TargetCompID: "client_1.a-b", TargetSubID: "DESK2"}
	assert.NoError(t, checkDynamicSessionID(valid))

	for _, mutate := range []func(*SessionID){
		func(id *SessionID) { id.TargetCompID = "/../../../tmp/x" },
		func(id *SessionID) { id.TargetCompID = `..\x` },
		func(id *SessionID) { id.TargetSubID = "a/b" },
		func(id *SessionID) { id.SenderLocationID = "a b" },
		func(id *SessionID) { id.BeginString = "FIX.4.2\n" },
		func(id *SessionID) { id.TargetCompID = "" },
		func(id *SessionID) { id.TargetCompID = strings.Repeat("A", maxDynamicSessionIDLength+1) },
	} {
		id := valid
		mutate(&id)
		assert.Error(t, checkDynamicSessionID(id), "%#v", id)
	}
}

// recordingStoreFactory is a memory store factory that records the SessionIDs
// it creates stores for.
type recordingStoreFactory struct {
	mu      sync.Mutex
	created []SessionID
}

func (f *recordingStoreFactory) Create(sessionID SessionID) (MessageStore, error) {
	f.mu.Lock()
	f.created = append(f.created, sessionID)
	f.mu.Unlock()
	return NewMemoryStoreFactory().Create(sessionID)
}

func TestAcceptor_DynamicSessionRefusesUnsafeID(t *testing.T) {
	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, BeginStringFIX42)
	sessionSettings.Set(config.SenderCompID, "SERVER")
	sessionSettings.Set(config.TargetCompID, "CLIENT")

	settings := NewSettings()
	settings.GlobalSettings().Set(config.SocketAcceptPort, "5002")
	settings.GlobalSettings().Set(config.HeartBtInt, "30")
	settings.GlobalSettings().Set(config.LogoutTimeout, "1")
	settings.GlobalSettings().Set(config.DynamicSessions, "Y")
	_, err := settings.AddSession(sessionSettings)
	require.NoError(t, err)

	stores := &recordingStoreFactory{}
	acceptor, err := NewAcceptor(&benchApp{}, stores, settings, NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	require.NoError(t, acceptor.Start())
	t.Cleanup(acceptor.Stop)

	conn, err := net.Dial("tcp", "127.0.0.1:5002")
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	logon := NewMessage()
	logon.Header.
		SetField(tagBeginString, FIXString(BeginStringFIX42)).
		SetField(tagSenderCompID, FIXString("/../../../tmp/x")).
		SetField(tagTargetCompID, FIXString("SERVER")).
		SetField(tagSendingTime, FIXUTCTimestamp{Time: time.Now()}).
		SetField(tagMsgSeqNum, FIXInt(1)).
		SetField(tagMsgType, FIXString(msgTypeLogon))
	logon.Body.
		SetField(tagEncryptMethod, FIXString("0")).
		SetField(tagHeartBtInt, FIXInt(30))
	_, err = conn.Write(logon.build())
	require.NoError(t, err)

	// The acceptor closes the connection without answering once it has
	// refused the session.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Read(make([]byte, 1024))
	require.ErrorIs(t, err, io.EOF)

	stores.mu.Lock()
	defer stores.mu.Unlock()
	for _, id := range stores.created {
		assert.NotEqual(t, "/../../../tmp/x", id.TargetCompID, "store created for %v", id)
	}
}

func TestNewAcceptor_MessageSizeAndReadTimeoutSettings(t *testing.T) {
	tests := []struct {
		setting string
		value   string
		valid   bool
	}{
		{config.MaxMessageSize, "2048", true},
		{config.MaxMessageSize, "0", false},
		{config.MaxMessageSize, "-1", false},
		{config.MaxMessageSize, "a", false},
		{config.SocketReadTimeout, "500ms", true},
		{config.SocketReadTimeout, "5", true},
		{config.SocketReadTimeout, "0", false},
		{config.SocketReadTimeout, "-1s", false},
		{config.SocketReadTimeout, "a", false},
	}

	for _, test := range tests {
		t.Run(test.setting+"="+test.value, func(t *testing.T) {
			settings := NewSettings()
			settings.GlobalSettings().Set(config.SocketAcceptPort, "5003")
			settings.GlobalSettings().Set(test.setting, test.value)

			_, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), settings, NewNullLogFactory())
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestAcceptor_FirstMessageLimits(t *testing.T) {
	logon := func() []byte {
		msg := NewMessage()
		msg.Header.
			SetField(tagBeginString, FIXString(BeginStringFIX42)).
			SetField(tagSenderCompID, FIXString("target")).
			SetField(tagTargetCompID, FIXString("sender")).
			SetField(tagSendingTime, FIXUTCTimestamp{Time: time.Now()}).
			SetField(tagMsgSeqNum, FIXInt(1)).
			SetField(tagMsgType, FIXString(msgTypeLogon))
		msg.Body.
			SetField(tagEncryptMethod, FIXString("0")).
			SetField(tagHeartBtInt, FIXInt(30))
		return msg.build()
	}

	// startAcceptor starts an acceptor on port 5003 with the given global and
	// session settings, and connects to it.
	startAcceptor := func(t *testing.T, global, session map[string]string) (*Acceptor, SessionID, net.Conn) {
		sessionSettings := NewSessionSettings()
		sessionSettings.Set(config.BeginString, BeginStringFIX42)
		sessionSettings.Set(config.SenderCompID, "sender")
		sessionSettings.Set(config.TargetCompID, "target")
		sessionSettings.Set(config.HeartBtInt, "30")
		for k, v := range session {
			sessionSettings.Set(k, v)
		}

		settings := NewSettings()
		settings.GlobalSettings().Set(config.SocketAcceptPort, "5003")
		for k, v := range global {
			settings.GlobalSettings().Set(k, v)
		}
		sessionID, err := settings.AddSession(sessionSettings)
		require.NoError(t, err)

		acceptor, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), settings, NewNullLogFactory(), WithRegistry(NewRegistry()))
		require.NoError(t, err)
		require.NoError(t, acceptor.Start())
		t.Cleanup(acceptor.Stop)

		conn, err := net.Dial("tcp", "127.0.0.1:5003")
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return acceptor, sessionID, conn
	}

	// requireClosed checks the acceptor closes conn without answering, well
	// before the test's own deadline.
	requireClosed := func(t *testing.T, conn net.Conn) {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, err := conn.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF)
	}

	t.Run("connection that sends nothing is closed", func(t *testing.T) {
		_, _, conn := startAcceptor(t, map[string]string{config.SocketReadTimeout: "200ms"}, nil)

		start := time.Now()
		requireClosed(t, conn)
		assert.Less(t, time.Since(start), 2*time.Second)
	})

	t.Run("BodyLength over the global limit closes the connection", func(t *testing.T) {
		_, _, conn := startAcceptor(t, map[string]string{config.MaxMessageSize: "1024"}, nil)

		_, err := conn.Write([]byte("8=FIX.4.2\x019=2000000000\x0135=A\x01"))
		require.NoError(t, err)
		requireClosed(t, conn)
	})

	t.Run("Logon over the session limit closes the connection", func(t *testing.T) {
		msg := logon()
		acceptor, sessionID, conn := startAcceptor(t, nil, map[string]string{config.MaxMessageSize: strconv.Itoa(len(msg) - 1)})

		_, err := conn.Write(msg)
		require.NoError(t, err)
		requireClosed(t, conn)

		_, ok := acceptor.RemoteAddr(sessionID)
		assert.False(t, ok)
	})

	t.Run("Logon within the limits is accepted", func(t *testing.T) {
		msg := logon()
		acceptor, sessionID, conn := startAcceptor(t,
			map[string]string{config.SocketReadTimeout: "200ms"},
			map[string]string{config.MaxMessageSize: strconv.Itoa(len(msg))})

		_, err := conn.Write(msg)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, ok := acceptor.RemoteAddr(sessionID)
			return ok
		}, 2*time.Second, 10*time.Millisecond)

		// The first-message deadline no longer applies once logged on.
		time.Sleep(400 * time.Millisecond)
		_, ok := acceptor.RemoteAddr(sessionID)
		assert.True(t, ok)
	})
}
