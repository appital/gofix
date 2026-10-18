package gofix

// Option configures an Acceptor, Initiator or MessageRouter.
type Option func(*options)

type options struct {
	registry       *Registry
	metricsFactory MetricsFactory
}

// WithRegistry sets the Registry that sessions are registered in and looked up from.
// Without it the shared DefaultRegistry is used.
func WithRegistry(r *Registry) Option {
	return func(o *options) {
		o.registry = r
	}
}

// WithMetrics sets the MetricsFactory that creates the Metrics each session
// records connection activity to. Without it nothing is recorded. It has no
// effect on a MessageRouter.
func WithMetrics(f MetricsFactory) Option {
	return func(o *options) {
		o.metricsFactory = f
	}
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.registry == nil {
		o.registry = defaultRegistry
	}
	if o.metricsFactory == nil {
		o.metricsFactory = nullMetricsFactory{}
	}
	return o
}
