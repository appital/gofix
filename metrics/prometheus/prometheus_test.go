package prometheus

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/appital/gofix"
)

var _ gofix.MetricsFactory = (*MetricsFactory)(nil)

func TestSessionMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	f, err := NewMetricsFactory(reg)
	require.NoError(t, err)

	a := gofix.SessionID{BeginString: gofix.BeginStringFIX42, SenderCompID: "A", TargetCompID: "B"}
	b := gofix.SessionID{BeginString: gofix.BeginStringFIX42, SenderCompID: "C", TargetCompID: "D"}

	ma := f.CreateSessionMetrics(a)
	ma.MessageSent(10)
	ma.MessageSent(5)
	ma.MessageReceived(7)
	f.CreateSessionMetrics(b).MessageReceived(3)

	assert.Equal(t, 2.0, testutil.ToFloat64(f.outgoingMessageCount.WithLabelValues(a.String())))
	assert.Equal(t, 15.0, testutil.ToFloat64(f.outgoingMessageBytes.WithLabelValues(a.String())))
	assert.Equal(t, 1.0, testutil.ToFloat64(f.incomingMessageCount.WithLabelValues(a.String())))
	assert.Equal(t, 7.0, testutil.ToFloat64(f.incomingMessageBytes.WithLabelValues(a.String())))
	assert.Equal(t, 0.0, testutil.ToFloat64(f.outgoingMessageCount.WithLabelValues(b.String())))
	assert.Equal(t, 3.0, testutil.ToFloat64(f.incomingMessageBytes.WithLabelValues(b.String())))
}

func TestNewMetricsFactoryRegistersOnce(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, err := NewMetricsFactory(reg)
	require.NoError(t, err)

	_, err = NewMetricsFactory(reg)
	assert.Error(t, err, "registering the same counters twice should fail")
}
