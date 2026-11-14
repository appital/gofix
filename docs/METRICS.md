# Metrics

gofix does not record metrics itself. Instead, it tells a `gofix.Metrics` about
every message a session's connection sends and receives, with the message's
length in bytes. Your application implements it with whatever metrics system
it uses.

```go
type Metrics interface {
	MessageSent(bytes int)
	MessageReceived(bytes int)
}

type MetricsFactory interface {
	CreateSessionMetrics(sessionID SessionID) Metrics
}
```

The engine calls `CreateSessionMetrics` once for each session when it creates
the session, including dynamic sessions. The read and write loops then call
`MessageSent` and `MessageReceived` for every message, so keep them cheap. They
are called from different goroutines.

Pass the factory to `gofix.NewAcceptor` or `gofix.NewInitiator` with the
`gofix.WithMetrics` option. Without it, nothing is recorded.

If you just want message and byte counters in Prometheus, the
`github.com/appital/gofix/metrics/prometheus` module already provides them. The
example below shows how to write your own.

## Example: a Prometheus counter

This counts messages in a single Prometheus counter, labelled by session and
direction:

```go
package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	"github.com/appital/gofix"
	"github.com/appital/gofix/log/screen"
)

// messageMetrics is a gofix.MetricsFactory that counts the messages each
// session sends and receives.
type messageMetrics struct {
	messages *prometheus.CounterVec
}

func newMessageMetrics(reg prometheus.Registerer) (*messageMetrics, error) {
	messages := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fix_messages_total",
		Help: "FIX messages sent and received, by session and direction.",
	}, []string{"session", "direction"})

	if err := reg.Register(messages); err != nil {
		return nil, err
	}
	return &messageMetrics{messages: messages}, nil
}

// CreateSessionMetrics looks up the session's counters once, so recording a
// message does not have to look them up by label.
func (m *messageMetrics) CreateSessionMetrics(sessionID gofix.SessionID) gofix.Metrics {
	id := sessionID.String()
	return sessionMetrics{
		sent:     m.messages.WithLabelValues(id, "sent"),
		received: m.messages.WithLabelValues(id, "received"),
	}
}

// sessionMetrics is the gofix.Metrics for one session.
type sessionMetrics struct {
	sent, received prometheus.Counter
}

func (m sessionMetrics) MessageSent(int)     { m.sent.Inc() }
func (m sessionMetrics) MessageReceived(int) { m.received.Inc() }

func run(ctx context.Context, app gofix.Application, settings *gofix.Settings) error {
	reg := prometheus.NewRegistry()
	metrics, err := newMessageMetrics(reg)
	if err != nil {
		return err
	}

	acceptor, err := gofix.NewAcceptor(
		app,
		gofix.NewMemoryStoreFactory(),
		gofix.StaticSettings(settings),
		screen.NewLogFactory(),
		gofix.WithMetrics(metrics),
	)
	if err != nil {
		return err
	}

	// If either the acceptor or the metrics server fails, the other stops
	// too. Run returns once the sessions have logged out.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return acceptor.Run(ctx) })

	// Serve the metrics for Prometheus to scrape.
	server := &http.Server{
		Addr:    ":2112",
		Handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
	}
	context.AfterFunc(ctx, func() { server.Close() })
	g.Go(func() error {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	return g.Wait()
}
```

Scraping `/metrics` then shows a line per session and direction, for example:

```text
fix_messages_total{direction="received",session="FIX.4.4:ME->THEM"} 42
fix_messages_total{direction="sent",session="FIX.4.4:ME->THEM"} 40
```

Things to note:

- To count bytes as well, add a second counter and `Add(float64(bytes))` to it
  in `MessageSent` and `MessageReceived`.
- Messages are counted at the connection, so a message is counted when the
  write loop writes it, after any `MaxMessagesPerSecond` delay, and when the
  read loop parses it, before the session validates it.
- A session's ID becomes a label value, so each session adds its own time
  series. With many dynamic sessions, consider labelling by something coarser,
  such as the SenderCompID.
- The Prometheus client panics if a metric of the same name is registered
  twice with `MustRegister`. `Register`, as used above, returns an error
  instead, for example when two engines in one process share a registry. Create
  the factory once and pass it to both engines.
