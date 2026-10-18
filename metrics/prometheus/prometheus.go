// Package prometheus records gofix connection metrics with Prometheus.
//
// It is a separate module, so that the core gofix module does not depend on
// the Prometheus client. Pass the factory to an Acceptor or Initiator with
// gofix.WithMetrics:
//
//	factory, err := prometheus.NewMetricsFactory(prom.DefaultRegisterer)
//	...
//	acceptor, err := gofix.NewAcceptor(app, storeFactory, settings, logFactory, gofix.WithMetrics(factory))
package prometheus

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/appital/gofix"
)

// MetricsFactory creates gofix.Metrics that count the messages and bytes each
// session sends and receives, labelled by session ID.
type MetricsFactory struct {
	outgoingMessageCount *prometheus.CounterVec
	outgoingMessageBytes *prometheus.CounterVec
	incomingMessageCount *prometheus.CounterVec
	incomingMessageBytes *prometheus.CounterVec
}

// NewMetricsFactory creates a MetricsFactory and registers its counters with
// reg, or with prometheus.DefaultRegisterer if reg is nil. It returns an
// error if a counter cannot be registered, such as when reg already has one
// of the same name.
func NewMetricsFactory(reg prometheus.Registerer) (*MetricsFactory, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	newCounter := func(name, help string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gofix",
			Name:      name,
			Help:      help,
		}, []string{"sessionID"})
	}

	f := &MetricsFactory{
		outgoingMessageCount: newCounter("outgoing_message_count", "Count of messages sent inside gofix writeLoop"),
		outgoingMessageBytes: newCounter("outgoing_message_bytes", "Count of bytes sent inside gofix writeLoop"),
		incomingMessageCount: newCounter("incoming_message_count", "Count of messages received inside gofix readLoop"),
		incomingMessageBytes: newCounter("incoming_message_bytes", "Count of bytes received inside gofix readLoop"),
	}

	for _, c := range []prometheus.Collector{
		f.outgoingMessageCount, f.outgoingMessageBytes, f.incomingMessageCount, f.incomingMessageBytes,
	} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}

	return f, nil
}

// CreateSessionMetrics returns the gofix.Metrics for sessionID.
func (f *MetricsFactory) CreateSessionMetrics(sessionID gofix.SessionID) gofix.Metrics {
	id := sessionID.String()
	return sessionMetrics{
		outgoingMessageCount: f.outgoingMessageCount.WithLabelValues(id),
		outgoingMessageBytes: f.outgoingMessageBytes.WithLabelValues(id),
		incomingMessageCount: f.incomingMessageCount.WithLabelValues(id),
		incomingMessageBytes: f.incomingMessageBytes.WithLabelValues(id),
	}
}

type sessionMetrics struct {
	outgoingMessageCount prometheus.Counter
	outgoingMessageBytes prometheus.Counter
	incomingMessageCount prometheus.Counter
	incomingMessageBytes prometheus.Counter
}

func (m sessionMetrics) MessageSent(bytes int) {
	m.outgoingMessageCount.Inc()
	m.outgoingMessageBytes.Add(float64(bytes))
}

func (m sessionMetrics) MessageReceived(bytes int) {
	m.incomingMessageCount.Inc()
	m.incomingMessageBytes.Add(float64(bytes))
}
