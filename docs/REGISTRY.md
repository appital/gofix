# Session registries

Every session an Acceptor or Initiator creates is registered in a `Registry`,
keyed by its `SessionID`. The registry is how you reach a session from outside
the engine, for example to send it a message.

By default all engines share one registry, `gofix.DefaultRegistry()`, and the
package level functions (`gofix.SendToTarget`, `gofix.ResetSession`, ...) use
it. Because SessionIDs must be unique within a registry, two engines that share
it cannot have a session with the same ID. For example, an Acceptor and an
Initiator that both use `FIX.4.4:ME->THEM` would clash.

To keep an engine's sessions separate, create a registry with
`gofix.NewRegistry()` and pass it with the `gofix.WithRegistry` option.

## Usage

```go
package main

import (
	"github.com/appital/gofix"
	"github.com/appital/gofix/log/screen"
)

type app struct {
	registry *gofix.Registry
}

func (a *app) OnCreate(gofix.SessionID)                    {}
func (a *app) OnLogon(gofix.SessionID)                     {}
func (a *app) OnLogout(gofix.SessionID)                    {}
func (a *app) ToAdmin(*gofix.Message, gofix.SessionID)     {}
func (a *app) ToApp(*gofix.Message, gofix.SessionID) error { return nil }
func (a *app) FromAdmin(*gofix.Message, gofix.SessionID) gofix.MessageRejectError {
	return nil
}

func (a *app) FromApp(msg *gofix.Message, sessionID gofix.SessionID) gofix.MessageRejectError {
	reply := gofix.NewMessage()
	// ... build the reply ...

	// Send through the registry the session belongs to. The package level
	// gofix.SendToTarget would only search the default registry.
	_ = a.registry.SendToTarget(reply, sessionID)
	return nil
}

func run(settings *gofix.Settings) error {
	registry := gofix.NewRegistry()

	acceptor, err := gofix.NewAcceptor(
		&app{registry: registry},
		gofix.NewMemoryStoreFactory(),
		settings,
		screen.NewLogFactory(),
		gofix.WithRegistry(registry),
	)
	if err != nil {
		return err
	}

	if err := acceptor.Start(); err != nil {
		return err
	}
	defer acceptor.Stop()

	// ...
	return nil
}
```

An Initiator works the same way: pass `gofix.WithRegistry(registry)` to
`gofix.NewInitiator`.

Things to note:

- Every package level function has a method of the same name on `Registry`:
  `Send`, `SendToTarget`, `ResetSession`, `UnregisterSession`,
  `SetNextSenderMsgSeqNum`, `SetNextTargetMsgSeqNum`, `GetExpectedSenderNum`,
  `GetExpectedTargetNum`, `GetMessageStore` and `GetLog`. With a custom
  registry, call the methods on it.
- If you route messages with a `MessageRouter`, give it the same registry:
  `gofix.NewMessageRouter(gofix.WithRegistry(registry))`. It uses the registry
  to find a session's default ApplVerID for FIXT.1.1 messages.
- `acceptor.Registry()` and `initiator.Registry()` return the registry an
  engine uses, which is `gofix.DefaultRegistry()` if none was given.
- Engines can share a custom registry. Give each engine its own when their
  SessionIDs may overlap.
