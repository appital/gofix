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

		var err error
		if initiator {
			_, err = NewInitiator(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(true), NewNullLogFactory(), opts...)
		} else {
			_, err = NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(false), NewNullLogFactory(), opts...)
		}
		require.NoError(t, err)

		session, ok := registry.lookup(registryTestSessionID)
		require.True(t, ok)
		require.Contains(t, factory.created, registryTestSessionID)
		assert.Same(t, factory.created[registryTestSessionID], session.metrics)
	}
}

func TestWithoutMetrics(t *testing.T) {
	registry := NewRegistry()
	_, err := NewAcceptor(&benchApp{}, NewMemoryStoreFactory(), registryTestSettings(false), NewNullLogFactory(),
		WithRegistry(registry))
	require.NoError(t, err)

	session, ok := registry.lookup(registryTestSessionID)
	require.True(t, ok)
	assert.Equal(t, nullMetrics{}, session.metrics)
}
