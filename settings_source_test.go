package gofix

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/appital/gofix/config"
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

// TestStaticSettingsReloadAppliesChanges checks that the engine copies the
// Settings a StaticSettings source returns, so that Reload compares the
// settings changed in place with those Run loaded: it restarts a session whose
// settings changed, and the acceptor refuses a change to a global setting it
// reads once.
func TestStaticSettingsReloadAppliesChanges(t *testing.T) {
	for _, tc := range sourceTestEngines(5146) {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			settings := reloadSettings(t, tc.global, tc.session("A"))
			e, err := tc.create(newReloadApp(), NewMemoryStoreFactory(), StaticSettings(settings), NewNullLogFactory(), registry)
			require.NoError(t, err)
			_, err = startForTest(t, e)
			require.NoError(t, err)
			before, _ := registry.lookup(tc.id("A"))
			require.NotNil(t, before)

			sessionSettings := settings.sessionSettings[tc.id("A")]
			require.NotNil(t, sessionSettings)
			sessionSettings.Set(config.LogonTimeout, "20")
			require.NoError(t, e.Reload(context.Background()))
			after, _ := registry.lookup(tc.id("A"))
			require.NotNil(t, after)
			assert.False(t, before == after, "A should have been restarted with its changed settings")

			if tc.name == "acceptor" {
				settings.GlobalSettings().Set(config.MaxMessageSize, "1024")
				require.ErrorContains(t, e.Reload(context.Background()), config.MaxMessageSize)
				again, _ := registry.lookup(tc.id("A"))
				assert.True(t, after == again, "a refused Reload should change nothing")
			}
		})
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

	// A done ctx stops it reading the file.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = source(ctx)
	require.ErrorIs(t, err, context.Canceled)
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

// TestRunCancelledWhileLoading checks that a Run whose context is done while
// the SettingsSource is loading returns the context's error, whatever error
// the SettingsSource returns, leaves nothing registered, and can be run again.
func TestRunCancelledWhileLoading(t *testing.T) {
	for _, tc := range sourceTestEngines(5149) {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			settings := reloadSettings(t, tc.global, tc.session("A"))
			loading := make(chan struct{}, 1)
			var loads int
			source := func(ctx context.Context) (*Settings, error) {
				loads++
				if loads > 1 {
					return settings, nil
				}
				loading <- struct{}{}
				<-ctx.Done()
				// A database driver need not return ctx's error.
				return nil, errors.New("connection closed")
			}
			e, err := tc.create(newReloadApp(), NewMemoryStoreFactory(), source, NewNullLogFactory(), registry)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- e.Run(ctx) }()
			<-loading
			cancel()
			assert.Equal(t, context.Canceled, <-done, "Run should return ctx's error, unwrapped")
			assert.False(t, isClosed(e.Ready()))
			_, ok := registry.lookup(tc.id("A"))
			assert.False(t, ok)

			_, err = startForTest(t, e)
			require.NoError(t, err, "Run should succeed when run again")
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

// TestRunClosesStoresAndLogs checks that Run, once it has shut down, has
// closed every store and log it created, including those of sessions Reload
// added or restarted, and each only once.
func TestRunClosesStoresAndLogs(t *testing.T) {
	for _, tc := range sourceTestEngines(5145) {
		t.Run(tc.name, func(t *testing.T) {
			stores, logs := &hookStoreFactory{}, &hookLogFactory{}
			source := newTestSource(reloadSettings(t, tc.global, tc.session("A"), tc.session("B")))
			e, err := tc.create(newReloadApp(), stores, source.load, logs, NewRegistry())
			require.NoError(t, err)
			run := runForTest(t, e)
			select {
			case <-e.Ready():
			case <-run.done:
				t.Fatalf("Run returned %v", run.err)
			}

			// B is restarted, keeping its store, and C added.
			require.NoError(t, reloadWith(e, source, reloadSettings(t, tc.global,
				tc.session("A"), tc.session("B", config.LogonTimeout, "20"), tc.session("C"))))
			_, closed := logs.globalCounts()
			assert.Zero(t, closed, "the global log should be open while Run is running")

			require.NoError(t, run.stop())
			for _, client := range []string{"A", "B", "C"} {
				assert.Equal(t, 1, stores.closedStores(tc.id(client)), "%s's store should be closed once", client)
				created, closed := logs.counts(tc.id(client))
				assert.Equal(t, created, closed, "every log created for %s should be closed", client)
			}
			created, closed := logs.globalCounts()
			assert.Equal(t, 1, created)
			assert.Equal(t, 1, closed, "the global log should be closed")
		})
	}
}

// onCreateApp is a reloadApp that calls onCreate from OnCreate.
type onCreateApp struct {
	*reloadApp
	onCreate func(SessionID)
}

func (a onCreateApp) OnCreate(sessionID SessionID) { a.onCreate(sessionID) }

// TestReloadWhileRunStarts checks that a Reload called while Run is starting
// the engine returns errReloadBeforeRun at once, rather than waiting for Run's
// SettingsSource, and that an Application may call Reload from OnCreate while
// Run starts the engine.
func TestReloadWhileRunStarts(t *testing.T) {
	for _, tc := range sourceTestEngines(5147) {
		t.Run(tc.name, func(t *testing.T) {
			settings := reloadSettings(t, tc.global, tc.session("A"))
			loading, release := make(chan struct{}), make(chan struct{})
			var loads int
			source := func(ctx context.Context) (*Settings, error) {
				loads++
				if loads == 1 {
					close(loading)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return settings, nil
			}

			var e engine
			onCreate := make(chan error, 1)
			app := onCreateApp{reloadApp: newReloadApp(), onCreate: func(SessionID) {
				onCreate <- e.Reload(context.Background())
			}}
			e, err := tc.create(app, NewMemoryStoreFactory(), source, NewNullLogFactory(), NewRegistry())
			require.NoError(t, err)
			run := runForTest(t, e)
			<-loading

			reloaded := make(chan error, 1)
			go func() { reloaded <- e.Reload(context.Background()) }()
			select {
			case err := <-reloaded:
				require.ErrorIs(t, err, errReloadBeforeRun)
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("Reload waited for Run's SettingsSource")
			}

			close(release)
			select {
			case <-e.Ready():
			case <-run.done:
				t.Fatalf("Run returned %v", run.err)
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not start")
			}
			require.ErrorIs(t, <-onCreate, errReloadBeforeRun, "Reload from OnCreate during Run")
			assert.Equal(t, 1, loads, "neither Reload should have loaded the settings")
		})
	}
}

// TestReloadFromOnCreate checks that OnCreate is called for each session
// before Ready is closed or the Reload that started the session returns, that
// OnCreate may call Reload while a Reload is calling it, and that a panic in
// OnCreate leaves the session running.
func TestReloadFromOnCreate(t *testing.T) {
	for _, tc := range sourceTestEngines(5148) {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			source := newTestSource(reloadSettings(t, tc.global, tc.session("A")))
			var e engine
			var mu sync.Mutex
			var createdIDs []SessionID
			nested := make(chan error, 1)
			app := onCreateApp{reloadApp: newReloadApp(), onCreate: func(sessionID SessionID) {
				mu.Lock()
				createdIDs = append(createdIDs, sessionID)
				mu.Unlock()
				switch sessionID {
				case tc.id("B"):
					source.set(reloadSettings(t, tc.global, tc.session("A"), tc.session("B"), tc.session("C")))
					nested <- e.Reload(context.Background())
				case tc.id("P"):
					panic("OnCreate failed")
				}
			}}
			created := func() []SessionID {
				mu.Lock()
				defer mu.Unlock()
				return slices.Clone(createdIDs)
			}
			e, err := tc.create(app, NewMemoryStoreFactory(), source.load, NewNullLogFactory(), registry)
			require.NoError(t, err)
			_, err = startForTest(t, e)
			require.NoError(t, err)
			assert.Equal(t, []SessionID{tc.id("A")}, created(), "OnCreate should be called before Ready")

			reloaded := make(chan error, 1)
			go func() {
				reloaded <- reloadWith(e, source, reloadSettings(t, tc.global, tc.session("A"), tc.session("B")))
			}()
			select {
			case err := <-reloaded:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("Reload deadlocked when OnCreate called Reload")
			}
			require.NoError(t, <-nested, "Reload from OnCreate")
			assert.Equal(t, []SessionID{tc.id("A"), tc.id("B"), tc.id("C")}, created(),
				"OnCreate should be called for B, and then for C by the Reload B's OnCreate made, before Reload returns")
			_, ok := registry.lookup(tc.id("C"))
			assert.True(t, ok, "C should have been added by the Reload from OnCreate")

			require.NoError(t, reloadWith(e, source, reloadSettings(t, tc.global,
				tc.session("A"), tc.session("B"), tc.session("C"), tc.session("P"))))
			assert.Contains(t, created(), tc.id("P"))
			_, ok = registry.lookup(tc.id("P"))
			assert.True(t, ok, "P should still be registered after its OnCreate panicked")
		})
	}
}

// TestReloadLogsOnlyChanges checks that the global log records the settings
// Run starts with and each Reload that changes something, but not a Reload
// that changes nothing.
func TestReloadLogsOnlyChanges(t *testing.T) {
	for _, tc := range sourceTestEngines(5150) {
		t.Run(tc.name, func(t *testing.T) {
			log := &textLog{}
			settings := reloadSettings(t, tc.global, tc.session("A"))
			e, err := tc.create(newReloadApp(), NewMemoryStoreFactory(), StaticSettings(settings), textLogFactory{log}, NewRegistry())
			require.NoError(t, err)
			_, err = startForTest(t, e)
			require.NoError(t, err)
			applied := func() int { return strings.Count(log.String(), "Applied settings") }
			assert.Equal(t, 1, applied(), "Run should log the settings it starts with")

			require.NoError(t, e.Reload(context.Background()))
			assert.Equal(t, 1, applied(), "a Reload that changes nothing should not be logged")

			settings.sessionSettings[tc.id("A")].Set(config.LogonTimeout, "20")
			require.NoError(t, e.Reload(context.Background()))
			assert.Equal(t, 2, applied(), "a Reload that restarts a session should be logged")
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
