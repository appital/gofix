package gofix

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// SettingsSource loads the settings an Acceptor or Initiator runs with, for
// example from a file or a database. The engine calls it from Run, to load the
// settings it starts with, and from each Reload, passing the context it was
// given, so a SettingsSource may block and may fail. It is never called from
// NewAcceptor or NewInitiator.
//
// The engine copies the Settings a SettingsSource returns, so a SettingsSource
// may return the same Settings each time and change them between calls, but
// not while the engine is calling it.
type SettingsSource func(ctx context.Context) (*Settings, error)

// StaticSettings returns a SettingsSource that always returns settings, for
// settings that are already loaded. Reload applies the changes made to
// settings since the engine last loaded them, so if they never change it
// leaves the engine as it is.
func StaticSettings(settings *Settings) SettingsSource {
	return func(context.Context) (*Settings, error) {
		return settings, nil
	}
}

// FileSettings returns a SettingsSource that reads the settings file at path
// with ParseSettings each time it is called. Run therefore reports a file
// that is missing or cannot be parsed, and Reload applies the changes made to
// the file since it was last read.
func FileSettings(path string) SettingsSource {
	return func(context.Context) (*Settings, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		settings, err := ParseSettings(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return settings, nil
	}
}

var (
	errNilSettingsSource = errors.New("SettingsSource is nil")
	errNilSettings       = errors.New("SettingsSource returned nil Settings")
)

// load calls the source, and returns a copy of the Settings it returns, or an
// error if it fails or returns no Settings. The engine compares the settings
// each Reload loads with those it last loaded, so it must not share them with
// the source, which may change them.
func (s SettingsSource) load(ctx context.Context) (*Settings, error) {
	settings, err := s(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading settings: %w", err)
	}
	if settings == nil {
		return nil, fmt.Errorf("loading settings: %w", errNilSettings)
	}
	return settings.clone(), nil
}
