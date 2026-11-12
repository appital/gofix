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

// TestReloadAddsSessionWithFileStoreAndLog checks that a session added by
// Reload gets a file store and file log, although the factories were built
// before it existed.
func TestReloadAddsSessionWithFileStoreAndLog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session func(target string) map[string]string
		reload  func(t *testing.T, settings *gofix.Settings, registry *gofix.Registry) func(context.Context, *gofix.Settings) error
	}{
		{
			name: "acceptor",
			session: func(target string) map[string]string {
				return map[string]string{config.SenderCompID: "SERVER", config.TargetCompID: target, config.SocketAcceptPort: "5131"}
			},
			reload: func(t *testing.T, settings *gofix.Settings, registry *gofix.Registry) func(context.Context, *gofix.Settings) error {
				logFactory, err := filelog.NewLogFactory(settings)
				require.NoError(t, err)
				acceptor, err := gofix.NewAcceptor(nopApp{}, file.NewStoreFactory(), settings, logFactory, gofix.WithRegistry(registry))
				require.NoError(t, err)
				return acceptor.Reload
			},
		},
		{
			name: "initiator",
			session: func(target string) map[string]string {
				return map[string]string{config.SenderCompID: "CLIENT", config.TargetCompID: target,
					config.SocketConnectHost: "127.0.0.1", config.SocketConnectPort: "5131"}
			},
			reload: func(t *testing.T, settings *gofix.Settings, registry *gofix.Registry) func(context.Context, *gofix.Settings) error {
				logFactory, err := filelog.NewLogFactory(settings)
				require.NoError(t, err)
				initiator, err := gofix.NewInitiator(nopApp{}, file.NewStoreFactory(), settings, logFactory, gofix.WithRegistry(registry))
				require.NoError(t, err)
				return initiator.Reload
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			registry := gofix.NewRegistry()
			reload := tc.reload(t, fileSettings(t, dir, nil, tc.session("A")), registry)

			// Reload before Run builds and registers the new session without
			// connecting it.
			require.NoError(t, reload(context.Background(), fileSettings(t, dir, nil, tc.session("A"), tc.session("B"))))

			sender := tc.session("B")[config.SenderCompID]
			_, err := registry.GetMessageStore(gofix.SessionID{BeginString: gofix.BeginStringFIX42, SenderCompID: sender, TargetCompID: "B"})
			require.NoError(t, err)
			requireFiles(t, filepath.Join(dir, "B", "store"))
			requireFiles(t, filepath.Join(dir, "B", "log"))
		})
	}
}
