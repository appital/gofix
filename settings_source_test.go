package gofix

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourceTestEngines builds an acceptor and an initiator whose sessions use
// port, the acceptor to listen on and the initiator to connect to.
func sourceTestEngines(port int) []struct {
	name    string
	global  map[string]string
	session func(client string, extra ...string) map[string]string
	id      func(client string) SessionID
	create  func(app Application, stores MessageStoreFactory, source SettingsSource, logs LogFactory, registry *Registry) (engine, error)
} {
	return []struct {
		name    string
		global  map[string]string
		session func(client string, extra ...string) map[string]string
		id      func(client string) SessionID
		create  func(app Application, stores MessageStoreFactory, source SettingsSource, logs LogFactory, registry *Registry) (engine, error)
	}{
		{
			name:    "acceptor",
			global:  reloadAcceptorGlobal,
			session: func(client string, extra ...string) map[string]string { return acceptorSession(client, port, extra...) },
			id:      serverSessionID,
			create: func(app Application, stores MessageStoreFactory, source SettingsSource, logs LogFactory, registry *Registry) (engine, error) {
				return NewAcceptor(app, stores, source, logs, WithRegistry(registry))
			},
		},
		{
			name:   "initiator",
			global: reloadInitiatorGlobal,
			session: func(client string, extra ...string) map[string]string {
				return initiatorSession(client, port, extra...)
			},
			id: clientSessionID,
			create: func(app Application, stores MessageStoreFactory, source SettingsSource, logs LogFactory, registry *Registry) (engine, error) {
				return NewInitiator(app, stores, source, logs, WithRegistry(registry))
			},
		},
	}
}

func TestStaticSettings(t *testing.T) {
	settings := NewSettings()
	source := StaticSettings(settings)
	for range 2 {
		loaded, err := source(context.Background())
		require.NoError(t, err)
		assert.Same(t, settings, loaded)
	}
}

// fileSettingsConfig is a settings file for initiator sessions with the given
// SenderCompIDs. Nothing listens on the port they connect to.
func fileSettingsConfig(clients ...string) string {
	cfg := "[DEFAULT]\nBeginString=FIX.4.2\nTargetCompID=SERVER\nSocketConnectHost=127.0.0.1\nSocketConnectPort=5144\nHeartBtInt=30\n"
	for _, client := range clients {
		cfg += "\n[SESSION]\nSenderCompID=" + client + "\n"
	}
	return cfg
}

func TestFileSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fix.cfg")
	source := FileSettings(path)

	_, err := source(context.Background())
	require.ErrorIs(t, err, fs.ErrNotExist)

	require.NoError(t, os.WriteFile(path, []byte("[SESSION]\nnot a setting\n"), 0o600))
	_, err = source(context.Background())
	require.ErrorContains(t, err, path)
	require.ErrorContains(t, err, "line 2")

	require.NoError(t, os.WriteFile(path, []byte(fileSettingsConfig("A")), 0o600))
	first, err := source(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []SessionID{clientSessionID("A")}, keys(first.SessionSettings()))

	// Each call reads the file again and returns new Settings.
	require.NoError(t, os.WriteFile(path, []byte(fileSettingsConfig("B")), 0o600))
	second, err := source(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []SessionID{clientSessionID("B")}, keys(second.SessionSettings()))
	assert.Equal(t, []SessionID{clientSessionID("A")}, keys(first.SessionSettings()))
}

// TestFileSettingsReload checks that an engine reads its settings file in
// Run, and that Reload applies a change to it.
func TestFileSettingsReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fix.cfg")
	registry := NewRegistry()
	initiator, err := NewInitiator(newReloadApp(), NewMemoryStoreFactory(), FileSettings(path), NewNullLogFactory(), WithRegistry(registry))
	require.NoError(t, err, "the constructor should not read the file")

	_, err = startForTest(t, initiator)
	require.ErrorIs(t, err, fs.ErrNotExist)

	require.NoError(t, os.WriteFile(path, []byte(fileSettingsConfig("A")), 0o600))
	_, err = startForTest(t, initiator)
	require.NoError(t, err)
	_, ok := registry.lookup(clientSessionID("A"))
	assert.True(t, ok)

	require.NoError(t, os.WriteFile(path, []byte(fileSettingsConfig("A", "B")), 0o600))
	require.NoError(t, initiator.Reload(context.Background()))
	_, ok = registry.lookup(clientSessionID("B"))
	assert.True(t, ok, "Reload should add the session added to the file")
}

func TestNewEngineRequiresSettingsSource(t *testing.T) {
	_, err := NewAcceptor(newReloadApp(), NewMemoryStoreFactory(), nil, NewNullLogFactory())
	assert.ErrorIs(t, err, errNilSettingsSource)
	_, err = NewInitiator(newReloadApp(), NewMemoryStoreFactory(), nil, NewNullLogFactory())
	assert.ErrorIs(t, err, errNilSettingsSource)
}

// TestRunLoadsSettings checks that the constructor neither calls the
// SettingsSource nor creates sessions, and that Run calls it once, with its
// context, and creates the sessions in the settings it returns.
func TestRunLoadsSettings(t *testing.T) {
	type ctxKey struct{}
	for _, tc := range sourceTestEngines(5140) {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			settings := reloadSettings(t, tc.global, tc.session("A"))
			var loads []any
			source := func(ctx context.Context) (*Settings, error) {
				loads = append(loads, ctx.Value(ctxKey{}))
				return settings, nil
			}
			e, err := tc.create(newReloadApp(), NewMemoryStoreFactory(), source, NewNullLogFactory(), registry)
			require.NoError(t, err)
			assert.Empty(t, loads, "the constructor should not load the settings")
			_, ok := registry.lookup(tc.id("A"))
			assert.False(t, ok, "the constructor should not create sessions")

			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "run"))
			done := make(chan error, 1)
			go func() { done <- e.Run(ctx) }()
			select {
			case <-e.Ready():
			case err := <-done:
				t.Fatalf("Run returned %v", err)
			}
			_, ok = registry.lookup(tc.id("A"))
			assert.True(t, ok, "Run should create the sessions")

			cancel()
			require.NoError(t, <-done)
			assert.Equal(t, []any{"run"}, loads, "Run should load the settings once, with its context")
		})
	}
}

// TestRunFailsWhenSettingsSourceFails checks that Run returns the
// SettingsSource's error, or an error if it returns no Settings, and loads the
// settings again when it is retried.
func TestRunFailsWhenSettingsSourceFails(t *testing.T) {
	for _, tc := range sourceTestEngines(5141) {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			errDB := errors.New("database is down")
			source := newTestSource(nil)
			source.fail(errDB)
			e, err := tc.create(newReloadApp(), NewMemoryStoreFactory(), source.load, NewNullLogFactory(), registry)
			require.NoError(t, err)

			require.ErrorIs(t, e.Run(context.Background()), errDB)
			source.fail(nil)
			require.ErrorIs(t, e.Run(context.Background()), errNilSettings)
			assert.False(t, isClosed(e.Ready()), "Ready should not be closed after a failed Run")

			source.set(reloadSettings(t, tc.global, tc.session("A")))
			_, err = startForTest(t, e)
			require.NoError(t, err, "Run should succeed when retried")
			assert.Equal(t, 3, source.loaded())
			_, ok := registry.lookup(tc.id("A"))
			assert.True(t, ok)
		})
	}
}

// TestRunFailureRegistersNothing checks that a Run that cannot create every
// session leaves none registered, closes the stores and logs it created, and
// can be retried.
func TestRunFailureRegistersNothing(t *testing.T) {
	for _, tc := range sourceTestEngines(5142) {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			stores, logs := &hookStoreFactory{}, &hookLogFactory{}
			source := newTestSource(reloadSettings(t, tc.global, tc.session("A"), tc.session("B")))
			e, err := tc.create(newReloadApp(), stores, source.load, logs, registry)
			require.NoError(t, err)

			// Another engine sharing the registry has registered B.
			other := &session{sessionID: tc.id("B")}
			require.NoError(t, registry.register(other))

			_, err = startForTest(t, e)
			require.ErrorIs(t, err, errDuplicateSessionID)
			_, ok := registry.lookup(tc.id("A"))
			assert.False(t, ok, "A should not be left registered")
			registered, _ := registry.lookup(tc.id("B"))
			assert.True(t, other == registered, "the other B should still be registered")
			for _, client := range []string{"A", "B"} {
				assert.Equal(t, 1, stores.closedStores(tc.id(client)), "%s's store should be closed", client)
				created, closed := logs.counts(tc.id(client))
				assert.Equal(t, created, closed, "%s's log should be closed", client)
			}
			if tc.name == "acceptor" {
				listener, err := net.Listen("tcp", ":5142")
				require.NoError(t, err, "the acceptor should not be listening")
				require.NoError(t, listener.Close())
			}

			require.NoError(t, registry.UnregisterSession(tc.id("B")))
			_, err = startForTest(t, e)
			require.NoError(t, err, "Run should succeed when retried")
			for _, client := range []string{"A", "B"} {
				_, ok := registry.lookup(tc.id(client))
				assert.True(t, ok, "%s should be registered", client)
			}
		})
	}
}

// TestReloadLoadsSettings checks that Reload calls the SettingsSource with
// its context, and changes nothing if it fails.
func TestReloadLoadsSettings(t *testing.T) {
	type ctxKey struct{}
	for _, tc := range sourceTestEngines(5143) {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			settings := reloadSettings(t, tc.global, tc.session("A"))
			var sourceErr error
			var reloads []any
			source := func(ctx context.Context) (*Settings, error) {
				if v := ctx.Value(ctxKey{}); v != nil {
					reloads = append(reloads, v)
				}
				return settings, sourceErr
			}
			e, err := tc.create(newReloadApp(), NewMemoryStoreFactory(), source, NewNullLogFactory(), registry)
			require.NoError(t, err)
			_, err = startForTest(t, e)
			require.NoError(t, err)
			before, _ := registry.lookup(tc.id("A"))
			require.NotNil(t, before)

			// The settings have not changed, so neither does anything else.
			ctx := context.WithValue(context.Background(), ctxKey{}, "reload")
			require.NoError(t, e.Reload(ctx))
			after, _ := registry.lookup(tc.id("A"))
			assert.True(t, before == after, "A should not have been restarted")

			// A failed load changes nothing, whatever it returns.
			sourceErr = fmt.Errorf("database is down")
			settings = reloadSettings(t, tc.global, tc.session("B"))
			require.ErrorIs(t, e.Reload(ctx), sourceErr)
			after, _ = registry.lookup(tc.id("A"))
			assert.True(t, before == after, "A should not have been removed")
			_, ok := registry.lookup(tc.id("B"))
			assert.False(t, ok, "B should not have been added")

			sourceErr = nil
			require.NoError(t, e.Reload(ctx))
			_, ok = registry.lookup(tc.id("A"))
			assert.False(t, ok, "A should have been removed")
			_, ok = registry.lookup(tc.id("B"))
			assert.True(t, ok, "B should have been added")
			assert.Equal(t, []any{"reload", "reload", "reload"}, reloads)
		})
	}
}
