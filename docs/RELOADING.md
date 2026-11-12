# Reloading settings

An Acceptor or Initiator builds its sessions from the `*Settings` passed to
`NewAcceptor` or `NewInitiator`. To change them while the engine is running,
for example after an admin API has changed a session in a database, build new
`Settings` and pass them to `Reload`:

```go
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return acceptor.Run(ctx) })

// From the admin API, after a session has been changed:
settings, err := loadSettingsFromDB(ctx)
if err != nil {
	return err
}
reloadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()
return acceptor.Reload(reloadCtx, settings)
```

## What Reload does

`Reload` compares each session's effective settings, meaning the global
settings with the session's own on top, with those of the session that is
running:

| Session                          | What happens                                                                                                                                        |
| -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Only in the new settings         | It is created, registered and started. An initiator connects it. An acceptor opens a listener for its port if no other session uses it.             |
| Only in the old settings         | It is logged out, disconnected and unregistered. An acceptor closes its listener if no session uses it any more. Its store is closed but not reset. |
| In both, with different settings | It is stopped as if it had been removed, then started with the new settings. It keeps its store, so its sequence numbers carry over.                |
| In both, with the same settings  | It is left alone and stays connected.                                                                                                               |

A change to a global setting changes the effective settings of every session that does not override it, so all of those sessions are restarted.

The engine passes each new or restarted session's effective settings to the `MessageStoreFactory` and `LogFactory`, so the built-in file and SQL stores and logs, and custom factories that read the settings they are given, work for sessions that were not in the settings the engine was built with.

Sessions an acceptor creates for `DynamicSessions` are not in the settings, so `Reload` leaves them alone. Dynamic sessions created after a `Reload` take its global settings.

## All or nothing

Every new and changed session is built, and an acceptor opens every new listener, before anything is stopped. If any of that fails, for example because of a bad setting, a port in use or a SessionID that is already registered, `Reload` returns the error and the running sessions are untouched.

## Engine-level settings

Some of an acceptor's global settings configure the acceptor rather than its sessions, and are read only once:

- `SocketAcceptHost`
- `UseTCPProxy`
- `MaxMessageSize`
- `SocketReadTimeout`
- `DynamicSessions` and `DynamicQualifier`
- `RedactedTags`, which also redacts the acceptor's global log
- The TLS settings: `SocketUseSSL`, `SocketServerName`,
  `SocketInsecureSkipVerify`, `SocketPrivateKeyFile`, `SocketCertificateFile`,
  `SocketPrivateKeyBytes`, `SocketCertificateBytes`, `SocketCAFile`,
  `SocketCABytes` and `SocketMinimumTLSVersion`

If the new global settings change any of these, `Reload` returns an error and changes nothing. To change them, cancel the acceptor's `Run` and run a new acceptor. A session can still set `MaxMessageSize` or `RedactedTags` for itself, and changing it there restarts only that session.

An initiator has no engine-level settings.

## Timing out

The context passed to `Reload` limits how long it waits for sessions to log out. Once it is done, the sessions still waiting for their Logout to be acknowledged are disconnected without waiting further, and `Reload` returns the context's error, having still applied the whole change.

## Calling Reload

- Calls to `Reload` run one at a time.
- Called before `Run`, `Reload` replaces the settings `Run` will use.
- Called once `Run` has returned, `Reload` returns an error.
- A failed `Reload` does not make `Run` return.
- The engine keeps the `*Settings` passed to `Reload`. Do not change it
  afterwards; build new `Settings` for the next `Reload`.
