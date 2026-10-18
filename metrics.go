package gofix

// Metrics records connection activity for one session. Its methods are called
// from the read and write loops for every message, so they should be cheap and
// must be safe to call from different goroutines.
type Metrics interface {
	// MessageSent is called for each message the write loop writes, with its
	// length in bytes.
	MessageSent(bytes int)

	// MessageReceived is called for each message the read loop parses, with
	// its length in bytes.
	MessageReceived(bytes int)
}

// MetricsFactory creates the Metrics for each session. It is called once per
// session, including dynamic sessions, when the session is created.
type MetricsFactory interface {
	CreateSessionMetrics(sessionID SessionID) Metrics
}

type nullMetrics struct{}

func (nullMetrics) MessageSent(int)     {}
func (nullMetrics) MessageReceived(int) {}

type nullMetricsFactory struct{}

func (nullMetricsFactory) CreateSessionMetrics(SessionID) Metrics {
	return nullMetrics{}
}
