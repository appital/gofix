package gofix

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/appital/gofix/config"
)

var registryTestSessionID = SessionID{BeginString: BeginStringFIX42, SenderCompID: "REGSENDER", TargetCompID: "REGTARGET"}

func TestRegistry_RegisterLookupUnregister(t *testing.T) {
	r := NewRegistry()
	s := &session{sessionID: registryTestSessionID}

	_, ok := r.lookup(registryTestSessionID)
	assert.False(t, ok)

	require.NoError(t, r.register(s))
	assert.Equal(t, errDuplicateSessionID, r.register(s))

	found, ok := r.lookup(registryTestSessionID)
	assert.True(t, ok)
	assert.Same(t, s, found)

	require.NoError(t, r.UnregisterSession(registryTestSessionID))
	assert.Equal(t, errUnknownSession, r.UnregisterSession(registryTestSessionID))
	assert.Equal(t, errUnknownSession, r.SendToTarget(NewMessage(), registryTestSessionID))
}

func TestRegistry_IsolatedFromOtherRegistries(t *testing.T) {
	r1, r2 := NewRegistry(), NewRegistry()

	require.NoError(t, r1.register(&session{sessionID: registryTestSessionID}))
	require.NoError(t, r2.register(&session{sessionID: registryTestSessionID}))

	_, ok := DefaultRegistry().lookup(registryTestSessionID)
	assert.False(t, ok, "custom registries must not leak into the default registry")
}

func TestRegistry_PackageFunctionsUseDefaultRegistry(t *testing.T) {
	s := &session{sessionID: registryTestSessionID}
	require.NoError(t, registerSession(s))
	t.Cleanup(func() { _ = UnregisterSession(registryTestSessionID) })

	found, ok := DefaultRegistry().lookup(registryTestSessionID)
	assert.True(t, ok)
	assert.Same(t, s, found)

	require.NoError(t, UnregisterSession(registryTestSessionID))
	_, ok = DefaultRegistry().lookup(registryTestSessionID)
	assert.False(t, ok)
}

// registryTestSettings returns a source of settings for one session, which
// does not connect: nothing listens on the initiator's port, and the
// acceptor listens on another.
func registryTestSettings(initiator bool) SettingsSource {
	settings := NewSettings()
	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, registryTestSessionID.BeginString)
	sessionSettings.Set(config.SenderCompID, registryTestSessionID.SenderCompID)
	sessionSettings.Set(config.TargetCompID, registryTestSessionID.TargetCompID)
	if initiator {
		sessionSettings.Set(config.HeartBtInt, "30")
		sessionSettings.Set(config.SocketConnectHost, "127.0.0.1")
		sessionSettings.Set(config.SocketConnectPort, "5001")
	} else {
		sessionSettings.Set(config.SocketAcceptPort, "5139")
	}
	if _, err := settings.AddSession(sessionSettings); err != nil {
		panic(err)
	}
	return StaticSettings(settings)
}

func TestRegistry_AcceptorAndInitiatorWithSameSessionID(t *testing.T) {
	acceptorRegistry, initiatorRegistry := NewRegistry(), NewRegistry()

	acceptor, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(false), NewNullLogFactory(),
		WithRegistry(acceptorRegistry))
	require.NoError(t, err)
	assert.Same(t, acceptorRegistry, acceptor.Registry())

	initiator, err := NewInitiator(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(true), NewNullLogFactory(),
		WithRegistry(initiatorRegistry))
	require.NoError(t, err)
	assert.Same(t, initiatorRegistry, initiator.Registry())

	// The sessions are created by Run, not by the constructors.
	_, ok := acceptorRegistry.lookup(registryTestSessionID)
	assert.False(t, ok)
	_, ok = initiatorRegistry.lookup(registryTestSessionID)
	assert.False(t, ok)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)
	_, err = startForTest(t, initiator)
	require.NoError(t, err)

	acceptorSession, ok := acceptorRegistry.lookup(registryTestSessionID)
	require.True(t, ok)
	assert.False(t, acceptorSession.InitiateLogon)

	initiatorSession, ok := initiatorRegistry.lookup(registryTestSessionID)
	require.True(t, ok)
	assert.True(t, initiatorSession.InitiateLogon)

	_, ok = DefaultRegistry().lookup(registryTestSessionID)
	assert.False(t, ok)
}

func TestRegistry_DefaultRegistryRejectsDuplicateSessionID(t *testing.T) {
	acceptor, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(false), NewNullLogFactory())
	require.NoError(t, err)
	assert.Same(t, DefaultRegistry(), acceptor.Registry())
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	initiator, err := NewInitiator(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(true), NewNullLogFactory())
	require.NoError(t, err)
	_, err = startForTest(t, initiator)
	assert.ErrorIs(t, err, errDuplicateSessionID)
}
