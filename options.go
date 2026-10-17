package gofix

// Option configures an Acceptor, Initiator or MessageRouter.
type Option func(*options)

type options struct {
	registry *Registry
}

// WithRegistry sets the Registry that sessions are registered in and looked up from.
// Without it the shared DefaultRegistry is used.
func WithRegistry(r *Registry) Option {
	return func(o *options) {
		o.registry = r
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
	return o
}
