package gofix

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closeTrackingConn records whether it has been closed.
type closeTrackingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *closeTrackingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// pipeDialer dials one end of a net.Pipe, passing the other end to serve, and
// records the connections it dials.
type pipeDialer struct {
	serve func(server net.Conn)

	mu    sync.Mutex
	conns []*closeTrackingConn
}

func (d *pipeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	client, server := net.Pipe()
	go d.serve(server)
	conn := &closeTrackingConn{Conn: client}
	d.mu.Lock()
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return conn, nil
}

func (d *pipeDialer) lastConn() *closeTrackingConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[len(d.conns)-1]
}

// silentServer reads what the client sends and never replies, like a server
// that accepts TCP but never answers the TLS handshake.
func silentServer(server net.Conn) {
	_, _ = io.Copy(io.Discard, server)
}

func newConnectTestSession() *session {
	return &session{log: nullLog{}, admin: make(chan any), done: make(chan struct{})}
}

// connectSessionWithin runs connectSession and fails the test if it has not
// returned within a second.
func connectSessionWithin(ctx context.Context, t *testing.T, s *session, dialer *pipeDialer, address string, tlsConfig *tls.Config, handshakeTimeout time.Duration) *sessionConn {
	t.Helper()
	result := make(chan *sessionConn, 1)
	go func() {
		result <- connectSession(ctx, s, dialer, address, tlsConfig, handshakeTimeout)
	}()
	select {
	case conn := <-result:
		return conn
	case <-time.After(time.Second):
		t.Fatal("connectSession did not return")
		return nil
	}
}

func TestConnectSessionClosesConnOnFailedHandshake(t *testing.T) {
	dialer := &pipeDialer{serve: func(server net.Conn) {
		// net.Pipe is unbuffered, so keep reading the ClientHello while
		// replying.
		go silentServer(server)
		_, _ = server.Write([]byte("not a TLS server\r\n"))
	}}

	// The timeout is longer than connectSessionWithin waits, so the failure
	// must come from the reply, not the timeout.
	conn := connectSessionWithin(context.Background(), t, newConnectTestSession(), dialer, "localhost:5001", &tls.Config{}, time.Minute)

	assert.Nil(t, conn)
	assert.True(t, dialer.lastConn().closed.Load(), "connection should be closed")
}

func TestConnectSessionTimesOutHandshake(t *testing.T) {
	dialer := &pipeDialer{serve: silentServer}

	conn := connectSessionWithin(context.Background(), t, newConnectTestSession(), dialer, "localhost:5001", &tls.Config{}, 50*time.Millisecond)

	assert.Nil(t, conn)
	assert.True(t, dialer.lastConn().closed.Load(), "connection should be closed")
}

// Stopping the initiator cancels ctx, which must end a handshake even with no
// timeout, or Initiator.Stop waits for it for ever.
func TestConnectSessionCancelStopsHandshake(t *testing.T) {
	dialer := &pipeDialer{serve: silentServer}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	conn := connectSessionWithin(ctx, t, newConnectTestSession(), dialer, "localhost:5001", &tls.Config{}, 0)

	assert.Nil(t, conn)
	assert.True(t, dialer.lastConn().closed.Load(), "connection should be closed")
}

func TestConnectSessionClosesConnWhenSessionCannotConnect(t *testing.T) {
	dialer := &pipeDialer{serve: silentServer}
	s := newConnectTestSession()
	close(s.done)

	conn := connectSessionWithin(context.Background(), t, s, dialer, "localhost:5001", nil, time.Second)

	assert.Nil(t, conn)
	assert.True(t, dialer.lastConn().closed.Load(), "connection should be closed")
}

// Each attempt must verify the server against the host it dialed, not the
// first host the session tried.
func TestConnectSessionServerNamePerAddress(t *testing.T) {
	serverNames := make(chan string, 2)
	dialer := &pipeDialer{serve: func(server net.Conn) {
		tlsServer := tls.Server(server, &tls.Config{
			GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				serverNames <- hello.ServerName
				return nil, errors.New("refused")
			},
		})
		_ = tlsServer.Handshake()
		_ = tlsServer.Close()
	}}
	tlsConfig := &tls.Config{}
	s := newConnectTestSession()

	require.Nil(t, connectSessionWithin(context.Background(), t, s, dialer, "primary.example:5001", tlsConfig, time.Second))
	require.Nil(t, connectSessionWithin(context.Background(), t, s, dialer, "backup.example:5001", tlsConfig, time.Second))

	assert.Equal(t, "primary.example", <-serverNames)
	assert.Equal(t, "backup.example", <-serverNames)
	assert.Empty(t, tlsConfig.ServerName, "the session's tls.Config should not be changed")
}
