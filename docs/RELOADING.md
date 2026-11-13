# Loading and reloading settings

An Acceptor or Initiator loads its settings from the `SettingsSource` passed
to `NewAcceptor` or `NewInitiator`:

```go
type SettingsSource func(ctx context.Context) (*Settings, error)
```

The engine calls it from `Run`, to load the settings it starts with, and from
each `Reload`, to load the settings to change to. It never calls it from the
constructor. A source that reads a database can therefore be a method or a
closure, and the first load happens inside `Run(ctx)` like the rest of the
engine's life:

```go
acceptor, err := gofix.NewAcceptor(app, storeFactory, loadSettingsFromDB, logFactory)
if err != nil {
	return err
}

g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return acceptor.Run(ctx) })

// From the admin API, after a session has been changed in the database:
reloadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()
return acceptor.Reload(reloadCtx)
```

For settings kept in a settings file, use `gofix.FileSettings(path)`. It reads
the file with `ParseSettings` on every load, so `Run` reports a file that is
missing or cannot be parsed, and `Reload`, called for example when the process
receives SIGHUP, applies the changes made to the file since.

For settings that are already loaded and do not change, such as those parsed
from an `io.Reader` with `ParseSettings`, use `gofix.StaticSettings(settings)`.

## What Run does

`Run` calls the source with its context, creates the global log, then creates,
registers and starts every session in the settings, as `Reload` does for a
session it adds. If the source returns an error, or any session cannot be
created, `Run` returns the error, leaving nothing registered or running, and
can be called again, which loads the settings again. `Run` does not retry by
itself.

No session exists, and none is in the `Registry`, until `Run` has created
them, so a bad setting is reported by `Run` and not by the constructor.
`Ready()`, on both `Acceptor` and `Initiator`, returns a channel that is
closed once the sessions are registered and started, and an acceptor is
listening.

## What Reload does

`Reload` calls the source with its context, then compares each session's
effective settings, meaning the global settings with the session's own on top,
with those of the session that is running:

| Session                          | What happens                                                                                                                                        |
| -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Only in the new settings         | It is created, registered and started. An initiator connects it. An acceptor opens a listener for its port if no other session uses it.             |
| Only in the old settings         | It is logged out, disconnected and unregistered. An acceptor closes its listener if no session uses it any more, unless `DynamicSessions` is set. Its store is closed but not reset, and its log is closed. |
| In both, with different settings | It is stopped as if it had been removed, then started with the new settings. It keeps its store, so its sequence numbers carry over. Its store settings cannot change; see below. |
| In both, with the same settings  | It is left alone and stays connected.                                                                                                               |

A change to a global setting changes the effective settings of every session that does not override it, so all of those sessions are restarted.

The engine passes each new session's effective settings to the `MessageStoreFactory`, and each new or restarted session's to the `LogFactory`, so the built-in file and SQL stores and logs, and custom factories that read the settings they are given, work for sessions that were not in the settings the engine started with.

The global log is created once, by `Run`, which passes `LogFactory.Create` the global settings it loaded. `Reload` does not recreate it, so a change to a global log setting such as `FileLogPath` applies to the sessions that inherit it, but the global log stays where it is.

Sessions an acceptor creates for `DynamicSessions` are not in the settings, so `Reload` leaves them alone. They can log on through any port the acceptor listens on, so with `DynamicSessions` set, `Reload` opens listeners for new ports but never closes one; the acceptor keeps listening on them until `Run` returns. Dynamic sessions created after a `Reload` take its global settings.

## All or nothing

The settings are loaded, every new and changed session is built, an acceptor opens every new listener, and every new session is registered, before anything is stopped. If any of that fails, for example because the source returns an error, or because of a bad setting, a port in use or a SessionID that is already registered, `Reload` returns the error and the running sessions are untouched.

A restarted session is registered again only once the session it replaces has stopped. If that fails, for example because another engine sharing the registry has registered its SessionID in the meantime, the session stays stopped, `Reload` returns the error, and the next `Reload` starts it as a new session.

## Store settings

A restarted session keeps the store of the session it replaces, so the settings the built-in stores read cannot change for it: `FileStorePath`, `FileStoreSync`, `SQLStoreDriver`, `SQLStoreDataSourceName`, `SQLStoreConnMaxLifetime`, `SQLStoreMessagesTableName` and `SQLStoreSessionsTableName`. If the new settings change any of them for a session that is in both, `Reload` returns an error and changes nothing.

To move a session to a new store, remove it with one `Reload` and add it back with the next. It then gets a store from the `MessageStoreFactory`, with whatever sequence numbers are at the new location, so copy the store there first if they must carry over.

A custom `MessageStoreFactory` may read other settings. `Reload` does not know about those, and a restarted session keeps its store even if they change.

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

The context passed to `Reload` is passed to the source, and limits how long `Reload` waits for sessions to log out. Once it is done, the sessions still waiting for their Logout to be acknowledged are disconnected without waiting further, and `Reload` returns the context's error, having still applied the whole change.

## Calling Reload

- Calls to `Reload` run one at a time.
- Called before `Run` has started the engine, `Reload` returns an error
  without calling the source, since `Run` loads the settings itself. Wait for
  `Ready()` first.
- Called once `Run` has begun to shut down, `Reload` returns an error.
- A failed `Reload` does not make `Run` return.
- The engine keeps the `*Settings` the source returns. Do not change them
  afterwards; return new `Settings` from each call. With `StaticSettings`,
  `Reload` finds nothing changed and does nothing.
- The engine does not watch the source. Call `Reload` when the settings have
  changed.
