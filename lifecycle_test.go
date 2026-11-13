package gofix

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/appital/gofix/config"
)

// lifecycleApp records logons and logouts.
type lifecycleApp struct {
	logons, logouts atomic.Int64
}

func (a *lifecycleApp) FromApp(*Message, SessionID) MessageRejectError { return nil }
func (a *lifecycleApp) OnCreate(SessionID)                             {}
func (a *lifecycleApp) OnLogon(SessionID)                              { a.logons.Add(1) }
func (a *lifecycleApp) OnLogout(SessionID)                             { a.logouts.Add(1) }

func lifecycleSettings(sender, target string, global map[string]string) *Settings {
	settings := NewSettings()
	for k, v := range global {
		settings.GlobalSettings().Set(k, v)
	}
	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, BeginStringFIX42)
	sessionSettings.Set(config.SenderCompID, sender)
	sessionSettings.Set(config.TargetCompID, target)
	if _, err := settings.AddSession(sessionSettings); err != nil {
		panic(err)
	}
	return settings
}

func newLifecycleAcceptor(t *testing.T, app Application, port int) *Acceptor {
	t.Helper()
	settings := lifecycleSettings("SERVER", "CLIENT", map[string]string{
		config.SocketAcceptPort: fmt.Sprint(port),
		config.HeartBtInt:       "30",
	})
	acceptor, err := NewAcceptor(app, NewMemoryStoreFactory(), StaticSettings(settings), NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	return acceptor
}

func newLifecycleInitiator(t *testing.T, app Application, port int) *Initiator {
	t.Helper()
	settings := lifecycleSettings("CLIENT", "SERVER", map[string]string{
		config.SocketConnectHost: "127.0.0.1",
		config.SocketConnectPort: fmt.Sprint(port),
		config.HeartBtInt:        "30",
		config.ReconnectInterval: "1",
	})
	initiator, err := NewInitiator(app, NewMemoryStoreFactory(), StaticSettings(settings), NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)
	return initiator
}

// isClosed reports whether ch is closed.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestReadyIsOpenBeforeRun(t *testing.T) {
	acceptor := newLifecycleAcceptor(t, &lifecycleApp{}, 5006)
	assert.False(t, isClosed(acceptor.Ready()))
	assert.False(t, isClosed((&Acceptor{}).Ready()), "Ready should work on an Acceptor not made by NewAcceptor")

	initiator := newLifecycleInitiator(t, &lifecycleApp{}, 5006)
	assert.False(t, isClosed(initiator.Ready()))
	assert.False(t, isClosed((&Initiator{}).Ready()), "Ready should work on an Initiator not made by NewInitiator")
}

func TestRunWithCancelledContextStartsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	acceptor := newLifecycleAcceptor(t, &lifecycleApp{}, 5006)
	require.ErrorIs(t, acceptor.Run(ctx), context.Canceled)
	assert.False(t, isClosed(acceptor.Ready()))
	listener, err := net.Listen("tcp", "127.0.0.1:5006")
	require.NoError(t, err, "the acceptor should not be listening")
	require.NoError(t, listener.Close())

	initiator := newLifecycleInitiator(t, &lifecycleApp{}, 5006)
	require.ErrorIs(t, initiator.Run(ctx), context.Canceled)
}

// Of two concurrent calls to Run, one fails at once and the other runs until
// its context is cancelled.
func TestRunTwiceConcurrentlyFails(t *testing.T) {
	for name, r := range map[string]runner{
		"acceptor":  newLifecycleAcceptor(t, &lifecycleApp{}, 5006),
		"initiator": newLifecycleInitiator(t, &lifecycleApp{}, 5006),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			results := make(chan error, 2)
			for range 2 {
				go func() { results <- r.Run(ctx) }()
			}

			select {
			case err := <-results:
				assert.ErrorIs(t, err, errAlreadyRun)
			case <-time.After(5 * time.Second):
				t.Fatal("neither Run returned")
			}

			cancel()
			select {
			case err := <-results:
				assert.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return after its context was cancelled")
			}
		})
	}
}

func TestRunAfterShutdownFails(t *testing.T) {
	acceptor := newLifecycleAcceptor(t, &lifecycleApp{}, 5006)
	stop, err := startForTest(t, acceptor)
	require.NoError(t, err)
	stop()
	assert.ErrorIs(t, acceptor.Run(context.Background()), errAlreadyRun)
}

// A Run that fails to start must not leave listeners open, and may then be
// retried.
func TestAcceptorRunFailureClosesListeners(t *testing.T) {
	settings := NewSettings()
	settings.GlobalSettings().Set(config.HeartBtInt, "30")
	for i, port := range []string{"5007", "5008"} {
		sessionSettings := NewSessionSettings()
		sessionSettings.Set(config.BeginString, BeginStringFIX42)
		sessionSettings.Set(config.SenderCompID, fmt.Sprintf("SERVER%d", i))
		sessionSettings.Set(config.TargetCompID, "CLIENT")
		sessionSettings.Set(config.SocketAcceptPort, port)
		_, err := settings.AddSession(sessionSettings)
		require.NoError(t, err)
	}

	acceptor, err := NewAcceptor(&lifecycleApp{}, NewMemoryStoreFactory(), StaticSettings(settings), NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)

	var created []net.Listener
	acceptor.SetNewListenerCallback(func(address string, _ *tls.Config) (net.Listener, error) {
		if len(created) == 1 {
			return nil, errors.New("listen failed")
		}
		listener, err := net.Listen("tcp", address)
		if err == nil {
			created = append(created, listener)
		}
		return listener, err
	})

	_, err = startForTest(t, acceptor)
	require.Error(t, err)
	require.Len(t, created, 1)
	// The deadline only keeps Accept from blocking if the listener is still
	// open; on a closed listener it fails, which is fine.
	_ = created[0].(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond))
	_, err = created[0].Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "the listener created before the failure should be closed")
	assert.False(t, isClosed(acceptor.Ready()), "Ready should not be closed after a failed Run")

	acceptor.SetNewListenerCallback(func(address string, _ *tls.Config) (net.Listener, error) {
		return net.Listen("tcp", address)
	})
	_, err = startForTest(t, acceptor)
	assert.NoError(t, err, "Run should succeed when retried")
}

// A bad setting on one session must stop Run before any session starts
// connecting. Sessions are started in map order, so with several good sessions
// the bad one is rarely first.
func TestInitiatorRunFailureStartsNoSessions(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:5009")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	var accepted atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conn.Close()
		}
	}()

	settings := NewSettings()
	settings.GlobalSettings().Set(config.SocketConnectHost, "127.0.0.1")
	settings.GlobalSettings().Set(config.SocketConnectPort, "5009")
	settings.GlobalSettings().Set(config.HeartBtInt, "30")
	for i := range 6 {
		sessionSettings := NewSessionSettings()
		sessionSettings.Set(config.BeginString, BeginStringFIX42)
		sessionSettings.Set(config.SenderCompID, fmt.Sprintf("CLIENT%d", i))
		sessionSettings.Set(config.TargetCompID, "SERVER")
		if i == 0 {
			sessionSettings.Set(config.SocketTimeout, "soon")
		}
		_, err := settings.AddSession(sessionSettings)
		require.NoError(t, err)
	}
	initiator, err := NewInitiator(&lifecycleApp{}, NewMemoryStoreFactory(), StaticSettings(settings), NewNullLogFactory(), WithRegistry(NewRegistry()))
	require.NoError(t, err)

	// The context stays live after Run fails, as a caller's usually would, so
	// a session started anyway would keep connecting.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.Error(t, initiator.Run(ctx))

	// A session waits up to a second before it first connects.
	assert.Never(t, func() bool { return accepted.Load() > 0 }, 1500*time.Millisecond, 50*time.Millisecond,
		"no session should connect after Run failed")
}

// Cancelling the initiator's context logs its session out of the acceptor, and
// Run returns once that is done.
func TestCancelLogsOutSessions(t *testing.T) {
	acceptorApp, initiatorApp := &lifecycleApp{}, &lifecycleApp{}
	acceptor := newLifecycleAcceptor(t, acceptorApp, 5010)
	_, err := startForTest(t, acceptor)
	require.NoError(t, err)
	initiator := newLifecycleInitiator(t, initiatorApp, 5010)
	runningInitiator := runForTest(t, initiator)

	require.Eventually(t, func() bool {
		return acceptorApp.logons.Load() == 1 && initiatorApp.logons.Load() == 1
	}, 5*time.Second, 10*time.Millisecond, "the sessions should log on")

	stopped := make(chan struct{})
	go func() {
		assert.NoError(t, runningInitiator.stop())
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("initiator did not shut down after its context was cancelled")
	}

	assert.Equal(t, int64(1), initiatorApp.logouts.Load())
	assert.Eventually(t, func() bool { return acceptorApp.logouts.Load() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the acceptor's session should log out")
}
