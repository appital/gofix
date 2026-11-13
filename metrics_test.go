package gofix

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingMetricsFactory struct {
	created map[SessionID]*recordingMetrics
}

func (f *recordingMetricsFactory) CreateSessionMetrics(sessionID SessionID) Metrics {
	m := &recordingMetrics{}
	f.created[sessionID] = m
	return m
}

func TestWithMetrics(t *testing.T) {
	for _, initiator := range []bool{false, true} {
		registry := NewRegistry()
		factory := &recordingMetricsFactory{created: map[SessionID]*recordingMetrics{}}
		opts := []Option{WithRegistry(registry), WithMetrics(factory)}

		var engine runner
		var err error
		if initiator {
			engine, err = NewInitiator(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(true), NewNullLogFactory(), opts...)
		} else {
			engine, err = NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(false), NewNullLogFactory(), opts...)
		}
		require.NoError(t, err)
		stop, err := startForTest(t, engine)
		require.NoError(t, err)

		session, ok := registry.lookup(registryTestSessionID)
		require.True(t, ok)
		require.Contains(t, factory.created, registryTestSessionID)
		assert.Same(t, factory.created[registryTestSessionID], session.metrics)
		stop()
	}
}

func TestWithoutMetrics(t *testing.T) {
	registry := NewRegistry()
	acceptor, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(false), NewNullLogFactory(),
		WithRegistry(registry))
	require.NoError(t, err)
	_, err = startForTest(t, acceptor)
	require.NoError(t, err)

	session, ok := registry.lookup(registryTestSessionID)
	require.True(t, ok)
	assert.Equal(t, nullMetrics{}, session.metrics)
}
