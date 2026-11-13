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
// The engine keeps the Settings a SettingsSource returns, so they must not be
// changed afterwards. Return new Settings from each call instead.
type SettingsSource func(ctx context.Context) (*Settings, error)

// StaticSettings returns a SettingsSource that always returns settings, for
// an engine whose settings do not change. Reload does nothing with it.
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

// load calls the source, and returns an error if it fails or returns no
// Settings.
func (s SettingsSource) load(ctx context.Context) (*Settings, error) {
	settings, err := s(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading settings: %w", err)
	}
	if settings == nil {
		return nil, fmt.Errorf("loading settings: %w", errNilSettings)
	}
	return settings, nil
}
