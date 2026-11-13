package gofix_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/appital/gofix"
	"github.com/appital/gofix/config"
	filelog "github.com/appital/gofix/log/file"
	"github.com/appital/gofix/store/file"
)

type nopApp struct{}

func (nopApp) FromApp(*gofix.Message, gofix.SessionID) gofix.MessageRejectError { return nil }

// fileSettings returns settings for the given sessions, with each session's
// store and logs under its own directory in dir.
func fileSettings(t *testing.T, dir string, global map[string]string, sessions ...map[string]string) *gofix.Settings {
	t.Helper()
	settings := gofix.NewSettings()
	settings.GlobalSettings().Set(config.BeginString, gofix.BeginStringFIX42)
	settings.GlobalSettings().Set(config.HeartBtInt, "30")
	settings.GlobalSettings().Set(config.FileLogPath, filepath.Join(dir, "global"))
	for k, v := range global {
		settings.GlobalSettings().Set(k, v)
	}
	for _, s := range sessions {
		sessionSettings := gofix.NewSessionSettings()
		for k, v := range s {
			sessionSettings.Set(k, v)
		}
		target := s[config.TargetCompID]
		sessionSettings.Set(config.FileStorePath, filepath.Join(dir, target, "store"))
		sessionSettings.Set(config.FileLogPath, filepath.Join(dir, target, "log"))
		_, err := settings.AddSession(sessionSettings)
		require.NoError(t, err)
	}
	return settings
}

// requireFiles fails the test unless dir holds at least one file.
func requireFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "%s should not be empty", dir)
}

// engine is an Acceptor or an Initiator.
type engine interface {
	Run(ctx context.Context) error
	Ready() <-chan struct{}
	Reload(ctx context.Context) error
}

// start runs e until the test ends, and returns once it is ready.
func start(t *testing.T, e engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned %v", err)
		}
	})
	select {
	case <-e.Ready():
	case err := <-done:
		done <- err
		t.Fatalf("Run returned %v", err)
	}
}

// engines builds an acceptor and an initiator that use the file store and
// file log.
var engines = []struct {
	name    string
	session func(target string) map[string]string
	create  func(source gofix.SettingsSource, registry *gofix.Registry) (engine, error)
}{
	{
		name: "acceptor",
		session: func(target string) map[string]string {
			return map[string]string{config.SenderCompID: "SERVER", config.TargetCompID: target, config.SocketAcceptPort: "5131"}
		},
		create: func(source gofix.SettingsSource, registry *gofix.Registry) (engine, error) {
			return gofix.NewAcceptor(nopApp{}, file.NewStoreFactory(), source, filelog.NewLogFactory(), gofix.WithRegistry(registry))
		},
	},
	{
		name: "initiator",
		session: func(target string) map[string]string {
			return map[string]string{config.SenderCompID: "CLIENT", config.TargetCompID: target,
				config.SocketConnectHost: "127.0.0.1", config.SocketConnectPort: "5131"}
		},
		create: func(source gofix.SettingsSource, registry *gofix.Registry) (engine, error) {
			return gofix.NewInitiator(nopApp{}, file.NewStoreFactory(), source, filelog.NewLogFactory(), gofix.WithRegistry(registry))
		},
	},
}

// TestReloadAddsSessionWithFileStoreAndLog checks that a session added by
// Reload gets a file store and file log, although the factories were built
// before it existed.
func TestReloadAddsSessionWithFileStoreAndLog(t *testing.T) {
	for _, tc := range engines {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			registry := gofix.NewRegistry()
			settings := fileSettings(t, dir, nil, tc.session("A"))
			source := func(context.Context) (*gofix.Settings, error) { return settings, nil }
			e, err := tc.create(source, registry)
			require.NoError(t, err)
			start(t, e)

			settings = fileSettings(t, dir, nil, tc.session("A"), tc.session("B"))
			require.NoError(t, e.Reload(context.Background()))

			sender := tc.session("B")[config.SenderCompID]
			_, err = registry.GetMessageStore(gofix.SessionID{BeginString: gofix.BeginStringFIX42, SenderCompID: sender, TargetCompID: "B"})
			require.NoError(t, err)
			requireFiles(t, filepath.Join(dir, "B", "store"))
			requireFiles(t, filepath.Join(dir, "B", "log"))
		})
	}
}

// TestEngineCreatesGlobalLogFromGlobalSettings checks that Run passes the
// global settings it loaded to LogFactory.Create, so that the file log writes
// the global log to the global FileLogPath, and that the constructor creates
// no log.
func TestEngineCreatesGlobalLogFromGlobalSettings(t *testing.T) {
	for _, tc := range engines {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			e, err := tc.create(gofix.StaticSettings(fileSettings(t, dir, nil, tc.session("A"))), gofix.NewRegistry())
			require.NoError(t, err)
			assert.NoDirExists(t, filepath.Join(dir, "global"))

			start(t, e)
			assert.FileExists(t, filepath.Join(dir, "global", "GLOBAL.event.current.log"))
		})
	}
}
