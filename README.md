# gofix

[![Build Status](https://github.com/appital/gofix/workflows/CI/badge.svg)](https://github.com/appital/gofix/actions) [![GoDoc](https://godoc.org/github.com/appital/gofix?status.png)](https://godoc.org/github.com/appital/gofix) [![Go Report Card](https://goreportcard.com/badge/github.com/appital/gofix)](https://goreportcard.com/report/github.com/appital/gofix)

A [FIX Protocol](https://www.fixtrading.org/) engine for Go, maintained by Appital.

| ⚠️ Warning                                                                                                                                                                                                                                                                                                                                                                  |
| :-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| gofix is an experimental fork built for Appital's own use. We do not support it for any other use. Its API and behaviour may change without notice, and we do not plan to accept outside feature requests or provide help with integration. **For most projects we strongly recommend the upstream [QuickFIX/Go](https://github.com/quickfixgo/quickfix) library instead.** |

## Why a fork?

gofix is a direct fork of [QuickFIX/Go](https://github.com/quickfixgo/quickfix),
taken at v0.9.12. QuickFIX/Go is a mature, well-tested engine and remains the
base of this one; nearly all of the session, store, log and data-dictionary code
here is theirs.

We forked to drop QuickFIX/Go's compatibility constraints. As a widely used
library, upstream has to keep its public API, its behaviour and its
generated-code layout stable. That limits which changes it can accept,
especially changes for performance and for running many sessions in one process.
gofix does not make those guarantees, so we can:

- **Change the API and behaviour when it helps.** For example,
  `ReuseIncomingMessages` shortens how long a received `*Message` is valid, and
  `UseTCPProxy=Y` now requires a PROXY protocol header on every connection.
- **Cut allocations and syscalls on the hot path.** Parsing, validating and
  building messages, field lookup and repeating groups have been reworked to
  allocate little or nothing per message. The write loop now batches outbound
  messages.
- **Scale to many sessions.** We fixed write-loop busy-waiting that saturated
  the CPU once there were more sessions than spare cores.
- **Add settings we need in production.** These include `MaxMessagesPerSecond`
  rate limiting, `LogoutTimeout` on acceptor sessions and a `Metrics` interface
  for connection metrics, with an optional Prometheus implementation.
- **Keep generated code alongside the engine.** The FIX 4.0 to 5.0SP2 packages
  are generated into `gen/` from `spec/` under the
  `github.com/appital/gofix/gen` module path. They are not published as separate
  repositories.

gofix is **not** a drop-in replacement for QuickFIX/Go. The package is named
`gofix`, the import paths are different, and breaking changes are listed under
`BREAKING CHANGES` in [CHANGELOG.md](CHANGELOG.md). When upstream fixes are
relevant, we port them by hand.

## Features

Inherited from QuickFIX/Go:

- FIX versions 4.0 to 5.0SP2
- Spec-driven run-time message validation
- Spec-driven code generation of type-safe FIX messages, fields and repeating groups
- Session state storage: SQL, on-disk or in-memory
- Logging: file, screen, SQL or composite
- Daily and weekly session scheduling
- TLS support
- The QuickFIX acceptance test suite

## Installation

```sh
go get github.com/appital/gofix
```

```go
import "github.com/appital/gofix"
```

## Generated Code

The generated message, field, tag and enum packages are not checked in. To
generate them into `gen/`, run:

```sh
make generate             # or: make generate-udecimal
```

Then import them from `github.com/appital/gofix/gen/...`, for example
`github.com/appital/gofix/gen/fix44/newordersingle`.

Generated code has the `.generated.go` suffix. To change it, edit the generator
in `cmd/generate-fix`. For applications with a custom FIX spec, run `go run
./cmd/generate-fix --help` to see how to generate code from your own XML.

## Applications

Unlike QuickFIX/Go, `gofix.Application` has one method, `FromApp`. The other
callbacks are in two optional interfaces, which the engine uses when the
application passed to `NewAcceptor` or `NewInitiator` implements them:

- `gofix.SessionHandler`: `OnCreate`, `OnLogon` and `OnLogout`.
- `gofix.MessageInterceptor`: `ToAdmin`, `FromAdmin` and `ToApp`.

A method with the wrong signature still compiles but is never called, so an
acceptor that checks Logons in `FromAdmin` could accept them unchecked. Assert
each interface your application is meant to implement at compile time:

```go
var (
	_ gofix.Application        = (*myApp)(nil)
	_ gofix.SessionHandler     = (*myApp)(nil)
	_ gofix.MessageInterceptor = (*myApp)(nil)
)
```

A `*gofix.MessageRouter` is itself an `Application`, so it can be passed to
`NewAcceptor` or `NewInitiator` directly.

## Metrics

gofix does not record metrics itself. To count the messages and bytes each
session sends and receives, implement `gofix.MetricsFactory` and pass it to
`NewAcceptor` or `NewInitiator` with `gofix.WithMetrics`. See
[docs/METRICS.md](docs/METRICS.md) for an example.

For Prometheus, use the separate `github.com/appital/gofix/metrics/prometheus`
module, which keeps the Prometheus client out of the core module:

```go
import (
	gofixprom "github.com/appital/gofix/metrics/prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

metrics, err := gofixprom.NewMetricsFactory(prometheus.DefaultRegisterer)
...
acceptor, err := gofix.NewAcceptor(app, storeFactory, settings, logFactory, gofix.WithMetrics(metrics))
```

It records `gofix_outgoing_message_count`, `gofix_outgoing_message_bytes`,
`gofix_incoming_message_count` and `gofix_incoming_message_bytes`, labelled by
`sessionID`.

## Documentation

- [Go API documentation](https://godoc.org/github.com/appital/gofix)
- The [QuickFIX/Go user manual](https://quickfixengine.org/go/documentation/)
  still covers most configuration and concepts. Where gofix differs, see
  [CHANGELOG.md](CHANGELOG.md) and `config/configuration.go`.
- [docs/REGISTRY.md](docs/REGISTRY.md) explains session registries and how to
  run engines whose SessionIDs overlap.
- [docs/METRICS.md](docs/METRICS.md) explains the `Metrics` interface, with an
  example that implements a Prometheus counter.

## Development

### Build and Test

The default make target runs `go vet` and the unit tests:

```sh
make
```

A VS Code dev container config is in `.devcontainer/` if you want a ready-made environment.

### Acceptance Tests

The acceptance suite is the same one used across all QuickFIX implementations,
and it needs Ruby on your `PATH`:

```sh
make generate
make build-test-srv
make accept
```

### Benchmarking

Performance changes should come with measurements. The repository includes a
benchmark corpus, per-layer micro-benchmarks and a full-stack load generator:

```sh
# record a baseline, make a change, then compare
make bench-baseline
make bench
make bench-compare

# profile one benchmark
make bench-profile BENCH=BenchmarkRoundTrip

# full-stack load test over loopback TCP, with profiles
make generate
make loadtest
```

For details, see [docs/BENCHMARKING.md](docs/BENCHMARKING.md).

## Licensing

gofix is derived from QuickFIX/Go and is distributed under the QuickFIX Software
License. For its terms, see [LICENSE](LICENSE).

This product includes software developed by quickfixengine.org
(https://www.quickfixengine.org/).
