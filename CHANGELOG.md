## Unreleased

### BREAKING CHANGES
* An inbound message larger than `MaxMessageSize`, which defaults to 1 MiB, or whose BodyLength claims more, now closes the connection, and an acceptor closes a new connection that has not sent a complete first message within `SocketReadTimeout`, which defaults to 30 seconds. An acceptor applies the global `MaxMessageSize` to the first message on a connection, and the session's after that
* Inbound messages are now checked for wire integrity before they are parsed: BodyLength must end exactly at the CheckSum field, CheckSum must match the message bytes, and a message with a duplicate version, identity, sequence, timestamp or CheckSum tag is rejected. Messages that previously parsed because these were only checked on send, or not at all, are now disconnected instead of being passed to validation
* An inbound message with a repeated header or trailer tag, a header tag after the body, or a repeated tag the session acts on, such as HeartBtInt or NewSeqNo, now gets a session-level Reject ("Tag appears more than once" or "Tag specified out of required order") whatever `ValidateFieldsOutOfOrder` and `RejectInvalidMessage` say, and is not passed to `FromAdmin` or `FromApp`. With a data dictionary, so does a repeated body tag outside a repeating group. A repeated tag keeps its first value rather than its last, so the checks the session makes before rejecting see the same value as everything else. Without a data dictionary, other repeated body tags still keep their last value, because they cannot be told apart from repeating group members
* gofix no longer records Prometheus metrics itself, and no longer depends on the Prometheus client. Importing it used to register the `gofix_*` message and byte counters on the default Prometheus registry; to keep them, pass `gofix.WithMetrics` a factory from the new `github.com/appital/gofix/metrics/prometheus` module, which records the same counters to a registry you choose
* With `UseTCPProxy=Y`, every connection must now open with a PROXY protocol header; connections without one are closed instead of being accepted with the load balancer's address. A connection that sends nothing for 10 seconds is also closed. This follows from upgrading `github.com/pires/go-proxyproto` to v0.15.0
* `SocketMinimumTLSVersion` no longer accepts the deprecated `SSL30`, `TLS10` or `TLS11`; valid values are now `TLS12` (the default) and `TLS13`. Any other value is now an error rather than being silently ignored
* `Application` now has only `FromApp`. `OnCreate`, `OnLogon` and `OnLogout` move to a new optional `SessionHandler` interface, and `ToAdmin`, `FromAdmin` and `ToApp` to a new optional `MessageInterceptor` interface. The engine calls them when the application passed to `NewAcceptor` or `NewInitiator` implements them; when it does not, admin messages are accepted and sends go ahead, as with empty methods. Existing applications that implement all seven methods behave as before, but code calling the other methods on a value typed as `gofix.Application` no longer compiles. A method with the wrong signature now compiles but is never called, so add compile-time checks such as `var _ gofix.MessageInterceptor = (*myApp)(nil)`; see the `Application` documentation
* TLS connections no longer restrict key exchange to the P-256 curve; Go's defaults are used, which prefer X25519 and the post-quantum hybrid X25519MLKEM768. The deprecated `PreferServerCipherSuites` option, which Go ignores, is no longer set
* An acceptor now answers a Logon whose HeartBtInt is missing, negative, does not parse or is above the new `MaxHeartBtInt` setting, which defaults to 3600 seconds, with a Logout and closes the connection, without advancing sequence numbers. A HeartBtInt of 0 now turns heartbeats and the peer timeout off, as FIX defines, where it used to make the session send heartbeats continuously. `HeartBtIntOverride` still ignores the peer's value

### FEATURES
* Adds the `MaxResendQueueMessages` and `MaxResendQueueBytes` session settings, which bound the messages a session holds while it waits for a resend, default 100,000 messages and 64 MiB
* Adds the `SocketWriteTimeout` session setting, how long a write to the connection may take before the connection is closed, default 30 seconds
* Adds the `MaxHeartBtInt` acceptor setting, the largest HeartBtInt, in seconds, accepted in a peer's Logon, default 3600
* Adds the `MaxMessageSize` session setting, the largest inbound message in bytes, default 1 MiB, and the `SocketReadTimeout` setting, how long an acceptor waits for the first message on a new connection, default 30 seconds
* Adds the `RedactedTags` setting, the fields whose values are replaced with `***` in every message, and every event that includes a message, passed to the `Log`, including the acceptor's global log. It defaults to Password (554), NewPassword (925), EncryptedPassword (1402) and EncryptedNewPassword (1404), so these are no longer logged by default; set it empty to turn redaction off. Outbound admin messages, such as Logon, are also redacted before they are saved to the `MessageStore`. Application messages are saved as they are, because they may be resent
* Each optional `Application` callback now has its own interface: `OnCreateHandler`, `OnLogonHandler` and `OnLogoutHandler`, which make up `SessionHandler`, and `ToAdminInterceptor`, `FromAdminInterceptor` and `ToAppInterceptor`, which make up `MessageInterceptor`. The engine calls each callback the application implements, so an application can implement only `FromAdmin`, for example
* Adds `Metrics` and `MetricsFactory` interfaces and a `WithMetrics` option for `NewAcceptor` and `NewInitiator`. Each session's `Metrics` is told of every message its connection sends and receives, with its length, so applications can record connection metrics with whatever system they use. Without the option nothing is recorded
* `MessageRouter` implements `Application` with a `FromApp` method, so a router can be passed to `NewAcceptor` or `NewInitiator` directly
* Adds the `github.com/appital/gofix/metrics/prometheus` module, a `MetricsFactory` that records message and byte counters with Prometheus
* Adds `ReuseIncomingMessages` session setting: inbound messages are parsed into one reused `Message` and their read buffers are recycled, so receiving a message no longer allocates for either. Off by default; when on, a received `*Message` is valid only until the callback returns
* Adds a `Registry` type and a `WithRegistry` option for `NewAcceptor`, `NewInitiator` and `NewMessageRouter`. Engines built with their own registry no longer share the package global session map, so one process can run an Acceptor and an Initiator with the same SessionID. Without the option they use the shared `DefaultRegistry()`, as before. `Acceptor.Registry()` and `Initiator.Registry()` return the registry in use. The package level `Send`, `SendToTarget`, `ResetSession` and related functions act only on the default registry; with a custom registry, call the same methods on it instead

### ENHANCEMENTS
* Parsing with an application data dictionary no longer allocates per field when detecting repeating groups; adds `FieldDef.ChildField` for indexed group member lookup
* Looking up an absent field no longer formats a reject error up front, reverse-routing a message no longer round-trips its routing fields through strings, and setting a field takes one allocation instead of two
* `TagValue` values set through `FieldMap` setters are now copies; they no longer alias the slice passed in
* `NewMessage` is one allocation instead of seven, parsing presizes a fresh message's field storage from its field count, and setting fields carves their storage from chunks rather than allocating each one
* Validating a message no longer allocates: `RejectInvalidMessage` checks field formats in place instead of building a `FieldValue` per field, duplicate tags are tracked without a per-message map, and the message type is no longer copied to a string
* Building a message formats its checksum and body length without allocating, `SetInt` no longer allocates beyond the field itself, and a `RepeatingGroup` builds its template's tag ordering once rather than for every entry, allocates each entry and its lock together, and reads plain template fields without cloning them
* Building a message walks each section twice instead of three times, sorts a section only when it has changed since the last build, writes under a read lock, no longer copies each section to the heap, and writes into a buffer sized exactly up front
* The write loop writes every message already waiting before flushing, through a buffered writer, so a backlog goes out in far fewer syscalls; the outbound channel holds up to 64 messages.

### BUG FIXES
* While a session waited for a resend, it held every message received above the gap, with no limit, so a peer could make it use any amount of memory. Once `MaxResendQueueMessages` or `MaxResendQueueBytes` is reached, further messages above the gap are now dropped with a log event, and the session asks for them again once the resend catches up, so the application still gets every message in order
* A peer that stopped reading could leave its session waiting for ever to send once the connection's buffers filled, so the session stopped handling messages and timers, and `Acceptor.Stop` or `Initiator.Stop` never returned. A write that has waited `SocketWriteTimeout` now closes the connection and disconnects the session
* Inbound parsing no longer lets a peer choose a frame boundary independently of BodyLength, and no longer lets a whole message of SOH bytes make the parser allocate many times its size. Every DATA field that has a length field in the FIX specifications, such as XMLData, RawData, EncodedText and EncryptedPassword, may now contain SOH without splitting the frame, including inside repeating groups. A malformed message is skipped and logged as "Invalid Message Skipped" with the reason and the number of bytes dropped, and bytes between messages, such as a line break, are ignored
* A ResendRequest with a BeginSeqNo below 1 or a negative EndSeqNo is now rejected instead of being processed
* With `DynamicSessions`, an acceptor now creates a session only if the CompIDs, SubIDs, LocationIDs and BeginString in the peer's Logon are made up of letters, digits, `.`, `_` and `-`, and are no longer than 64 bytes. The file store also refuses a SessionID that would name a file outside `FileStorePath`
* An application that implemented only some of the `SessionHandler` or `MessageInterceptor` methods had none of them called; each implemented method is now called. `NewAcceptor` and `NewInitiator` now return an error when the application has one of these methods with a signature that does not match, or with a pointer receiver on an application passed by value, instead of never calling it
* With `ReuseIncomingMessages`, `Log.OnIncoming` was given the buffer the message was read into, which is then reused for later messages, possibly another session's. A log that kept the slice, for example to write it asynchronously, could record the wrong bytes. It is now given a copy
* `RejectInvalidMessage` no longer panics on a field whose data dictionary type the validator does not recognise; such fields are accepted like strings
* Removing a field and then setting it again no longer writes it twice with a BodyLength that counts it once
* A session whose write loop was busy no longer busy-waits on it, which saturated the CPU and collapsed throughput once there were more sessions than spare cores
* Integer fields, and a BodyLength, too large to fit in an `int` are now rejected as invalid instead of wrapping around
* Reading an empty integer field now returns an error
* A message whose `XMLDataLen` is longer than the rest of the message is now rejected as invalid
* An unexpected failure while reading or parsing an inbound message now drops that connection or rejects that message, rather than stopping the process. `ParseMessage` returns an error for messages too short to hold a header

## 0.9.12 (September 30, 2026)

### BUG FIXES
* Stop misattributing body fields that follow a nested repeating group [#756](https://github.com/quickfixgo/quickfix/pull/756)
* Forward OnEventf format arguments to the wrapped logs in the composite log [#779](https://github.com/quickfixgo/quickfix/pull/779)
* Correct the datadictionary parse error message in SessionFactory [#780](https://github.com/quickfixgo/quickfix/pull/780)

## 0.9.11 (September 14, 2026)

### FEATURES
* Adds `SetNewListenerCallback` to the Acceptor to allow custom `net.Listener` creation [#729](https://github.com/quickfixgo/quickfix/pull/729)
* Adds config option for ValidateFieldsHaveValues [#734](https://github.com/quickfixgo/quickfix/pull/734)
* Adds `Reset()` method to RepeatingGroup [#775](https://github.com/quickfixgo/quickfix/pull/775)

### ENHANCEMENTS
* Make both initiator and acceptor fixIn channels buffered [#736](https://github.com/quickfixgo/quickfix/pull/736)
* Update devcontainer Go image to 1.25 [#748](https://github.com/quickfixgo/quickfix/pull/748)
* Remove redundant buffer Reset when reading messages [#777](https://github.com/quickfixgo/quickfix/pull/777)

### BUG FIXES
* Resent messages with repeating groups on sessions without a data dictionary no longer produce an incorrect length and checksum [#743](https://github.com/quickfixgo/quickfix/pull/743)
* Validate each token of multi-value enum fields [#766](https://github.com/quickfixgo/quickfix/pull/766)
* Fix duplicate 10= checksum when resending a message ending in a repeating group [#767](https://github.com/quickfixgo/quickfix/pull/767)
* Resend Requests should not interleave with live traffic [#768](https://github.com/quickfixgo/quickfix/pull/768)
* Fix potential deadlock in FieldMap.GetTime [#776](https://github.com/quickfixgo/quickfix/pull/776)

## 0.9.10 (August 8, 2025)

### BUG FIXES
* Send Reset if the ResetTime elapsed in between checks [#725](https://github.com/quickfixgo/quickfix/pull/725)

## 0.9.9 (July 31, 2025)

### BUG FIXES
* Adds missing tz for resetseqtime without starttime endtime #723 [#723](https://github.com/quickfixgo/quickfix/pull/723)

## 0.9.8 (July 21, 2025)

### ENHANCEMENTS
* Add DB name configuration [#711](https://github.com/quickfixgo/quickfix/pull/711)
* Change ResetSeqTime to time.Time based on the config's timeZone [#712](https://github.com/quickfixgo/quickfix/pull/712)

### BUG FIXES
* Block Sends when Resend Request is active [#715](https://github.com/quickfixgo/quickfix/pull/715)
* Fix the issue of incorrect time range calculation across days [#718](https://github.com/quickfixgo/quickfix/pull/718)

## 0.9.7 (April 23, 2025)

### FEATURES
* Adds SQL, MongoDB and Composite FIX Log and LogFactory implementations, see `config/configuration.go` for details [#672](https://github.com/quickfixgo/quickfix/pull/672)
* Adds convenience getters for session log and store [#675](https://github.com/quickfixgo/quickfix/pull/675)
* Adds config option for ResetSeqTime [#705](https://github.com/quickfixgo/quickfix/pull/705)

### ENHANCEMENTS
* File store uses files exclusively [#680](https://github.com/quickfixgo/quickfix/pull/680)
* Protect concurrent usage of filestore [#688](https://github.com/quickfixgo/quickfix/pull/688)
* Support udecimal library in code generation [#700](https://github.com/quickfixgo/quickfix/pull/700)

### BUG FIXES
* Avoid unkeyed fields usage for exported struct in generated code [#683](https://github.com/quickfixgo/quickfix/pull/683)
* Iterate messages in filestore opens a separate file to avoid deadlock [#703](https://github.com/quickfixgo/quickfix/pull/703)
* Correct ordering in message trailer [#707](https://github.com/quickfixgo/quickfix/pull/707)

## 0.9.6 (September 20, 2024)

### ENHANCEMENTS
* Allow the clients of acceptor to specify their own tls.Config https://github.com/quickfixgo/quickfix/pull/667
* Adds NextExpectedSeqNum setting https://github.com/quickfixgo/quickfix/pull/668

### BUG FIXES
* Reinit stop sync to prevent deadlock on sequential start/stops https://github.com/quickfixgo/quickfix/pull/669
* Check logon auth before resetting store https://github.com/quickfixgo/quickfix/pull/670
* Reverts ToAdmin call sequencing https://github.com/quickfixgo/quickfix/pull/674

## 0.9.5 (August 14, 2024)

### ENHANCEMENTS
* Introduce message iterator to avoid loading all messages into memory at once upon resend request https://github.com/quickfixgo/quickfix/pull/659
* Only lock fieldmap once during message parsing https://github.com/quickfixgo/quickfix/pull/658
* Optimize tag value parsing https://github.com/quickfixgo/quickfix/pull/657
* Use bytes.Count to count the number of message fields https://github.com/quickfixgo/quickfix/pull/655
* Port config documentation into proper go doc format https://github.com/quickfixgo/quickfix/pull/649
* Support TLS configuration as raw bytes https://github.com/quickfixgo/quickfix/pull/647

### BUG FIXES
* Use the Go generated file convention https://github.com/quickfixgo/quickfix/pull/660
* Fix stuck call to Dial when calling Stop on the Initiator https://github.com/quickfixgo/quickfix/pull/654
* Do not increment NextTargetMsgSeqNum for out of sequence Logout and Test Requests https://github.com/quickfixgo/quickfix/pull/645

## 0.9.4 (May 29, 2024)

### ENHANCEMENTS
* Adds log to readLoop just like writeLoop https://github.com/quickfixgo/quickfix/pull/642

### BUG FIXES
* Maintain repeating group field order when parsing messages  https://github.com/quickfixgo/quickfix/pull/636

## 0.9.3 (May 9, 2024)

### BUG FIXES
* Change filestore.offsets from map[int]msgDef to sync.Map https://github.com/quickfixgo/quickfix/pull/639
* Unregister sessions on stop https://github.com/quickfixgo/quickfix/pull/637
* Corrects ResetOnLogon behavior for initiators https://github.com/quickfixgo/quickfix/pull/635

### FEATURES
* Add AllowUnknownMessageFields & CheckUserDefinedFields settings as included in QuickFIX/J https://github.com/quickfixgo/quickfix/pull/632

## 0.9.2 (April 23, 2024)

### BUG FIXES
* Prevent message queue blocking in the case of network connection trouble https://github.com/quickfixgo/quickfix/pull/615 https://github.com/quickfixgo/quickfix/pull/628
* Corrects validation of multiple repeating groups with different fields https://github.com/quickfixgo/quickfix/pull/623

## 0.9.1 (April 15, 2024)

### BUG FIXES
* Preserve original body when resending https://github.com/quickfixgo/quickfix/pull/624

## 0.9.0 (November 13, 2023)

### FEATURES
* Add Weekdays config setting as included in QuickFIX/J https://github.com/quickfixgo/quickfix/pull/590
* `MessageStore` Refactor

The message store types external to a quickfix-go application have been refactored into individual sub-packages within `quickfix`. The benefit of this is that the dependencies for these specific store types are no longer included in the quickfix package itself, so many projects depending on the quickfix package will no longer be bloated with large indirect dependencies if they are not specifically implemented in your application. This applies to the `mongo` (MongoDB), `file` (A file on-disk), and `sql` (Any db accessed with a go sql driver interface). The `memorystore` (in-memory message store) syntax remains unchanged. The minor drawback to this is that with some re-packaging came some minor syntax changes. See https://github.com/quickfixgo/quickfix/issues/547 and https://github.com/quickfixgo/quickfix/pull/592 for more information. The relevant examples are below.

MONGO
```go
import "github.com/quickfixgo/quickfix"

...
acceptor, err = quickfix.NewAcceptor(app, quickfix.NewMongoStoreFactory(appSettings), appSettings, fileLogFactory)
```
becomes 
```go
import (
  "github.com/quickfixgo/quickfix"
  "github.com/quickfixgo/quickfix/store/mongo"
)

...
acceptor, err = quickfix.NewAcceptor(app, mongo.NewStoreFactory(appSettings), appSettings, fileLogFactory)
```

FILE
```go
import "github.com/quickfixgo/quickfix"

...
acceptor, err = quickfix.NewAcceptor(app, quickfix.NewFileStoreFactory(appSettings), appSettings, fileLogFactory)
```
becomes 
```go
import (
  "github.com/quickfixgo/quickfix"
  "github.com/quickfixgo/quickfix/store/file"
)

...
acceptor, err = quickfix.NewAcceptor(app, file.NewStoreFactory(appSettings), appSettings, fileLogFactory)
```

SQL

```go
import "github.com/quickfixgo/quickfix"

...
acceptor, err = quickfix.NewAcceptor(app, quickfix.NewSQLStoreFactory(appSettings), appSettings, fileLogFactory)
```
becomes 
```go
import (
  "github.com/quickfixgo/quickfix"
  "github.com/quickfixgo/quickfix/store/sql"
)

...
acceptor, err = quickfix.NewAcceptor(app, sql.NewStoreFactory(appSettings), appSettings, fileLogFactory)
```


### ENHANCEMENTS
* Acceptance suite store type expansions https://github.com/quickfixgo/quickfix/pull/596 and https://github.com/quickfixgo/quickfix/pull/591
* Support Go v1.21 https://github.com/quickfixgo/quickfix/pull/589


### BUG FIXES
* Resolves outstanding issues with postgres db creation syntax and `pgx` driver https://github.com/quickfixgo/quickfix/pull/598
* Fix sequence number bug when storage fails https://github.com/quickfixgo/quickfix/pull/432


## 0.8.1 (October 27, 2023)

BUG FIXES

* Remove initiator wait [GH 587]
* for xml charset and bug of "Incorrect NumInGroup" [GH 368, 363, 365, 366]
* Allow time.Duration or int for timeouts [GH 477]
* Trim extra non-ascii characters that can arise from manually editing [GH 463, 464]

## 0.8.0 (October 25, 2023)

ENHANCEMENTS

* Remove tag from field map [GH 544]
* Add message.Bytes() to avoid string conversion [GH 546]
* Check RejectInvalidMessage on FIXT validation [GH 572]

BUG FIXES

* Fix repeating group read tags lost [GH 462]
* Acceptance test result must be predictable [GH 578]
* Makes event timer stop idempotent [GH 580, 581]
* Added WaitGroup Wait in Initiator [GH 584]

## 0.7.0 (January 2, 2023)

FEATURES

* PersistMessages Config [GH 297]
* MaxLatency [GH 242]
* ResetOnDisconnect Configuration [GH 68]
* Support for High Precision Timestamps [GH 288]
* LogonTimeout [GH 295]
* LogoutTimeout [GH 296]
* Socks Proxy [GH 375]

ENHANCEMENTS

* Add SocketUseSSL parameter to allow SSL/TLS without client certs [GH 311]
* Support for RejectInvalidMessage configuration [GH 336]
* Add deep copy for Messages [GH 338]
* Add Go Module support [GH 340]
* Support timeout on ssl connection [GH 347, 349]
* Dynamic Sessions [GH 521]
* Upgrade Mongo Driver to support transactions [GH 527]

BUG FIXES

* header and trailer templates use rootpath [GH 302]
* Initiator stop panic if stop chan's already closed [GH 359]
* Connection closed when inbound logon has a too-low sequence number [GH 369]
* TLS server name config [GH 384]
* Fix concurrent map write [GH 436]
* Race condition during bilateral initial resend request [GH 439]
* Deadlock when disconnecting dynamic session [GH 524]
* Align session's ticker with round second [GH 533]
* Seqnum persist and increment fix [GH 528]


## 0.6.0 (August 14, 2017)

FEATURES

* CheckLatency [GH 241, 266]
* ResendRequestChunkSize [GH 243, 245]
* EnableLastMsgSeqNumProcessed  [GH 253, 256]

ENHANCEMENTS

* config.SocketTLSForTesting(bool) [GH 235]
* API Interface Enhancements [GH 251, 252, 254, 255, 257, 258, 259]
* Misc. Performance Optimizations [GH 260, 261, 263, 264, 265, 268, 270, 271, 272, 273, 274, 275]
* TLS Configuration [GH 279, 280]
* Use data dictionary for parsing fix messages [GH 281]

BUG FIXES

* Resend logon fix [GH 244]
* PossDup messages with seqnum too low should not be sent to FromCallbacks [GH 246, 247]
* Router should not reject admin messages or business rejects [GH 249, 250]
* Fixes file log output for incoming, outgoing [GH 262]
* message.String() returns rawMessage if set, builds otherwise [GH 269]
* Use timestamp with time zone for postgres sql [GH 286]


## 0.5.0 (September 1, 2016)

FEATURES

* Session Scheduling [GH 31, 195, 196, 197, 198, 200, 201, 202, 203, 204, 205, 211, 212, 218, 220]
* TimeZone configuration [GH 206]
* StartDay, EndDay for week long sessions [GH 207, 239]
* Support for connection over SSL [GH 63, 193]
* SocketConnectHost/Port<n> [GH 65, 217]
* ResetOnLogout Configuration enhancement [GH 67, 192]
* SocketAcceptAddress, ipv6 support [GH 83, 215]
* RefreshOnLogon [GH 214]

ENHANCEMENTS

* Reject Logon Support [GH 57, 225]
* FIX Decimal [GH 114, 223, 224, 227, 228]
* test refactoring, leveraging testify mock assertions [GH 186]
* KISS on registry, session management [GH 208]
* moving and renaming test jigs [GH 210]
* Config Enhancements [GH 216]
* travis uses go 1.7 [GH 219]
* adds 'generated.go' suffix to generated source [GH 221]
* vendored deps GH [GH 222, 226]
* renames SQLStore config settings [GH 229]
* Add FieldMap.{SetInt, SetString} [GH 236]


BUG FIXES

* DefaultApplVerID Configuration not translating enum names [GH 89, 213]
* Dropped issues in logout state [GH 187, 188]
* correctly increments in target seq num on logout for in session state [GH 189]
* SendToTarget should return an error if toApp returns DoNotSend [GH 190, 191]
* Logon fix [GH 194]
* onlogout calls depend on session state [GH 199]
* fixes bug in resend state where resend response is processed incomplete [GH 230]
* fixes logic around logon message with sequence number too high [GH 231]
* SequenceReset, Resent Messages not hitting ToAdmin/ToApp [GH 232, 233]
* Next MsgSeqNo after received ResetSeqNumFlag=Y [GH 238, 240]


## 0.4.0 (August 1, 2016)

FEATURES

* ReconnectInterval Initiator Configuration [GH 66, 161]

ENHANCEMENTS

* Generate Getters for messages [GH 118]
* Drop generate-* apps into cmd/ [GH 125, 150]
* Misc field type refactoring  [GH 145]
* Cmd gen [GH 146, 147]
* refactoring enum generation [GH 148]
* pipelining generation [GH 149]
* Sending a message to an unlogged-in session, results an error [GH 173, 182]
* adds event logging related to session events [GH 175]
* Error handling around session code enhancement [GH 176]
* refactoring of session code [GH 183]
* Gen header beginstring [GH 184]

BUG FIXES

* Do not incr target seq num when seq num too high  [GH 151]
* can't parse securitylist message [GH 153, 155] 
* Concurrent map operations on Acceptor.Stop()  [GH 156]
* Return requiredConfigurationMissing when "FileStorePath" not found [GH 157]
* Checks around HeartBtInt configuration [GH 158]
* Removed complexity around closing Initiator sessions [GH 159]
* Proper FIX logout sequence [GH 160]
* Session logic doesn't account for a failure when calling messageStore methods (e.g. persisting a message) [GH 162]
* Session event loop [GH 164]
* Session event loop follow up [GH 165]
* Handle OnLogon/Logout callbacks in user space [GH 167]
* Session deadlocks if both initiator and acceptor enter the resend state [GH 169, 179]
* Initiator.Stop() does not wait for Acceptor's logout response, causing a resend on next logon [GH 170, 172]
* Reset peer timer after logon [GH 171]
* Ensure OnLogon is called even if seq num too high [GH 174]
* increment target seq num on logout [GH 177]
* fixes bogus resend logic [GH 178]
* Session forgets it is in resend state [GH 180, 181]
* fixes donotsend logic [GH 185]

## 0.3.0 (June 3, 2016)

FEATURES

* ValidateFieldsOutOfOrder Configuration Option [GH 107]

ENHANCEMENTS

* Datadictionary tests [GH 108]
* Proposal to add public method to convert raw fix message to quickfix.Message [GH 110]
* Make gen package public [GH 113]
* Make gen import path relative [GH 119]
* Validator interface [GH 120]
* Expose IncorrectDataFormat [GH 121]
* Spelling, fmtness [GH 123]
* More verbose error text on conditionally required field BMR [GH 131]
* better error handling in gen package [GH 134]
* Include timestamp in messages file log [GH 135]
* extracts repeating group interface [GH 137]
* Header, Body, Trailer FieldMap types [GH 138]
* Datadictionary/Gen refactor [GH 140]
* Gen value timestamp [GH 141]
* Slight gen revert [GH 142]
* replaced regex with faster impl for float parsing [GH 143]
* Expose sql.DB's SetConnMaxLifetime() in settings [GH 144, 139]

BUG FIXES

* Routing Incorrect for FIXT [GH 101]
* Validation Error for component tag [GH 102]
* Unmarshal error for repeating Group [GH 103]
* Marshaler/Reflector tries to convert time.Time struct into fix format [GH 109]
* nil pointer panic if no config.DataDictionary specified [GH 127]
* fixes required group validation error [GH 129]
* RefTagID is not known to BusinessMessageReject type [GH 130]

## 0.2.0 (April 19, 2016)

FEATURES

* Persisted Store [GH 32]
* Initializer Constructors for Generated Messages [GH 69]
* Field Setters for generated messages [GH 70]
* Support for optional components, component setters [GH 79]
* Setters for repeating groups in components [GH 87]
* DB Store [GH 88]

ENHANCEMENTS

* Gen refactor [GH 78]
* Refactoring data dictionary pkg [GH 93]

BUG FIXES

* Initiator panic if connection closed [GH 59]
* New logs overrides old ones [GH 72]
* Session sending message timeout [GH 80]
* Potential FIX50 Market Data marshaling bug [GH 91]
* Allow settings values to contain equals signs [GH 97]
* Error when trying to unmarshal FIX message (FIX 5.0) [GH 99]

## 0.1.0 (February 21, 2016)

* Initial release
