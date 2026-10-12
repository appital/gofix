# Benchmarking and profiling gofix

This repository carries a load-test and profiling harness so that performance
changes can be argued from measurements rather than from reading the code.

Nothing here changes engine behaviour. It exists to make the engine measurable.

## The pieces

| What               | Where                                      | Measures                                                     |
| ------------------ | ------------------------------------------ | ------------------------------------------------------------ |
| Message corpus     | `internal/tape/`                           | — (shared fixture)                                           |
| Micro-benchmarks   | `bench_*_test.go` (root package)           | Individual layers: framing, parse, build, groups, validation |
| Session benchmarks | `bench_session_test.go`                    | The whole engine, socketless                                 |
| Load generator     | `_test/loadgen/`                           | The whole engine over real loopback TCP                      |
| Scripts            | `scripts/bench.sh`, `scripts/benchstat.sh` | Repeatable runs and comparison                               |

### The corpus

`internal/tape` generates a deterministic tape of FIX 4.4 messages. The same
`(seed, size, mix)` always produces byte-identical output, so two runs measure
the same work.

The default mix (`realistic`) models buy-side order flow: 70% ExecutionReport,
15% NewOrderSingle, 5% OrderCancelReplaceRequest, 3% OrderCancelRequest, 5%
MarketDataIncrementalRefresh (10–20 entry repeating group with nested
NoSecurityAltID), 2% Heartbeat. Other mixes: `orderflow`, `marketdata`, `flat`.

`internal/tape` deliberately imports nothing outside the standard library. It
cannot import `gofix` (the benchmarks are internal tests, so that would be an
import cycle) and it must not import `gen/…` (a gitignored build artifact). Its
FIX encoder is written independently of `Message.build()`, which makes it an
oracle rather than a mirror.

Four tests guard the corpus, and they run as part of `make test`:

- `TestCorpusBuildMatchesWire` — the tape's encoder and `Message.build()` agree byte for byte.
- `TestCorpusRoundTrip` — parse then rebuild reproduces the original bytes.
- `TestCorpusValidates` — every message passes FIX 4.4 dictionary validation, so the
  validation benchmarks measure the happy path and not the reject path.
- `TestCorpusShape` — every modelled message class is present and the group-heavy
  ones really do carry groups.

If you change the corpus, every previously recorded baseline becomes
meaningless. `corpusSeed` and `corpusSize` in `bench_corpus_test.go` are fixed
for that reason.

## Running the micro-benchmarks

```sh
make bench-baseline          # record where we are today  -> .bench/baseline.txt
# ...make a change...
make bench                   # measure again              -> .bench/current.txt
make bench-compare           # benchstat baseline vs current
```

Narrow it down while iterating:

```sh
make bench BENCH=BenchmarkRoundTrip
make bench BENCH='BenchmarkParse$$/tape'
```

Knobs: `BENCH`, `BENCH_COUNT` (default 10), `BENCH_TIME` (default 1s),
`BENCH_GOMAXPROCS` (default 4).

`benchstat` is fetched with `go run golang.org/x/perf/cmd/benchstat@latest`, so
it never enters `go.mod`.

### Is the harness quiet enough to trust?

```sh
make bench-selftest
```

This runs the same code twice and compares the two. Everything should come back
`~` (no significant difference). If it does not, the machine is too noisy to
draw conclusions from — close other applications and try again before believing
any result.

On Apple silicon and recent Intel parts the cores are asymmetric, so a single
run can land on a performance core or an efficiency core and differ by 2× for no
reason at all. That is why the defaults are `-count=10` plus benchstat rather
than one long run: allocation counts are exact, but timings need repetitions.

## Profiling

```sh
make bench-profile BENCH=BenchmarkRoundTrip
```

Writes `.bench/cpu.prof` and `.bench/mem.prof` and prints the top allocation
sites. `-memprofilerate=1` is set, so every allocation is sampled — accurate,
but the timings from a profiling run are not comparable to a normal one.

```sh
go tool pprof -http=: .bench/cpu.prof
go tool pprof -top -sample_index=alloc_objects .bench/mem.prof   # count of allocations
go tool pprof -top -sample_index=alloc_space   .bench/mem.prof   # bytes allocated
```

`alloc_objects` is usually the one you want: this engine's problem is the number
of allocations, not their size.

## The load generator

The micro-benchmarks isolate layers. The loadgen runs the whole thing: an
acceptor and an initiator in one process, talking FIX 4.4 over loopback TCP,
with the generated typed field setters an application would actually use.

```sh
make generate        # loadgen imports gen/..., which is not committed
make loadtest        # 30s at 5000/s, all profiles into .bench/
```

Or drive it directly:

```sh
cd _test && go build -o loadgen_bin ./loadgen/
./loadgen_bin -duration=30s -rate=10000 -sessions=1 -mix=orderflow \
  -cpuprofile=cpu.prof -memprofile=mem.prof
./loadgen_bin -rate=5000 -pprof=:6060        # live pprof while it runs
./loadgen_bin -rate=5000 -json=- -quiet      # machine-readable results
```

Key flags: `-rate` (per session, `0` saturates), `-sessions`, `-duration`,
`-warmup` (excluded from all statistics), `-mix`, `-store=memory|file`,
`-persist`, `-validate`, `-gomaxprocs`, and the profile paths.

It reports throughput, round-trip latency percentiles, and — the number this
whole exercise is about — **allocations per message**, taken from a
`runtime.MemStats` delta across the measurement window only.

Round-trip latency is measured without allocating: ClOrdIDs are sequential
integers, so send times live in a preallocated ring indexed by sequence number
and matching a reply is an array index.

### Reading the load generator's numbers

- **`-rate=0` saturates.** The sender outruns the engine, work queues up, and
  latency becomes a measure of queue depth rather than of the engine. Use it for
  maximum throughput and allocation rate; ignore its latency percentiles.
- **Low rates inflate latency.** At 1000/s the p50 is roughly 5× the p50 at
  5000/s, because every message pays a scheduler wakeup on an otherwise idle
  path. This is real, but it is not what you are optimising.
- **`unmatched` should be zero.** If it is not, replies arrived that could not be
  matched to a send, and the latency percentiles are incomplete.

## Baseline

Apple M2 (4 performance + 4 efficiency cores), macOS, Go 1.26, `GOMAXPROCS=4`,
`-count=6 -benchtime=300ms`, median of six runs. Regenerate with
`make bench-baseline`.

#### End to end

`RoundTrip` is parse plus build with no session. `SessionIncoming` adds the state machine, sequence checking and the application callback, still without a socket.

| Benchmark                                 |   ns/op |    B/op | allocs/op |
| ----------------------------------------- | ------: | ------: | --------: |
| `RoundTrip/dd=off`                        |  5.9 µs |  9.3 KB |        44 |
| `RoundTrip/dd=on`                         | 51.7 µs | 84.1 KB |       318 |
| `SessionIncoming/receive/dd=off`          |  5.4 µs |  8.1 KB |        29 |
| `SessionIncoming/receive/dd=on`           | 59.9 µs | 85.3 KB |       387 |
| `SessionIncoming/echo/dd=off/persist=off` | 10.9 µs | 12.4 KB |       140 |
| `SessionIncoming/echo/dd=off/persist=on`  | 11.1 µs | 12.6 KB |       140 |
| `SessionOutbound/persist=false`           | 12.9 µs | 19.5 KB |       288 |
| `SessionOutbound/persist=true`            | 12.8 µs | 19.6 KB |       288 |

#### Inbound

`fresh` allocates a `Message` per parse, as a live session does. `reused` parses into one `Message`, which engages the field-arena reuse already present in `doParsing`.

| Benchmark                          |   ns/op |    B/op | allocs/op |
| ---------------------------------- | ------: | ------: | --------: |
| `ParserReadMessage/mix=realistic`  |  161 ns |   557 B |         2 |
| `ParserReadMessage/mix=marketdata` |  413 ns |  2.8 KB |         2 |
| `Parse/tape/fresh`                 |  3.6 µs |  8.1 KB |        28 |
| `Parse/tape/reused`                |  1.7 µs |     0 B |         0 |
| `ParseWithDD/tape/fresh`           | 50.4 µs | 82.6 KB |       302 |
| `ParseWithDD/tape/reused`          | 48.2 µs | 74.8 KB |       275 |
| `ReadAndParse/dd=off`              |  3.6 µs |  8.7 KB |        30 |
| `ReadAndParse/dd=on`               | 49.5 µs | 83.2 KB |       304 |
| `ExtractField`                     |  480 ns |     0 B |         0 |
| `GetField/hit/GetBytes`            |   15 ns |     0 B |         0 |
| `GetField/hit/GetString`           |   33 ns |    24 B |         2 |
| `GetField/hit/GetInt`              |   18 ns |     0 B |         0 |
| `GetField/miss/GetBytes`           |  127 ns |   128 B |         4 |

#### Inbound, per message class

The last row is the worst case in the corpus: a ~3.3 KB market data message with a 19-entry repeating group, parsed against the FIX 4.4 dictionary.

| Benchmark                                         |    ns/op |    B/op | allocs/op |
| ------------------------------------------------- | -------: | ------: | --------: |
| `Parse/msgtype=Heartbeat/fresh`                   |   650 ns |  1.6 KB |        15 |
| `Parse/msgtype=OrderCancel/fresh`                 |   1.4 µs |  3.4 KB |        24 |
| `Parse/msgtype=NewOrderSingle/fresh`              |   2.7 µs |  6.1 KB |        27 |
| `Parse/msgtype=ExecutionReport/fresh`             |   3.2 µs |  8.2 KB |        29 |
| `Parse/msgtype=MarketDataIncremental/fresh`       |  12.8 µs | 21.2 KB |        27 |
| `ParseWithDD/msgtype=ExecutionReport/fresh`       |   4.4 µs |  8.7 KB |        60 |
| `ParseWithDD/msgtype=NewOrderSingle/fresh`        |   6.5 µs | 10.8 KB |       124 |
| `ParseWithDD/msgtype=MarketDataIncremental/fresh` | 827.4 µs | 1.33 MB |      4272 |

#### Outbound

`Build` serialises an already-populated message; `NewAndBuild` also constructs and populates it.

| Benchmark                           |   ns/op |    B/op | allocs/op |
| ----------------------------------- | ------: | ------: | --------: |
| `NewMessage`                        |  143 ns |   440 B |         7 |
| `TagValue/init`                     |   38 ns |    32 B |         2 |
| `TagValue/parse`                    |    5 ns |     0 B |         0 |
| `SetField/SetBytes`                 |   48 ns |    32 B |         2 |
| `SetField/SetString`                |   60 ns |    56 B |         3 |
| `SetField/SetInt`                   |   65 ns |    32 B |         3 |
| `SetField/SetField`                 |   62 ns |    56 B |         3 |
| `SetField/populate/ExecutionReport` |  4.1 µs |  8.0 KB |       129 |
| `FormatCheckSum`                    |   48 ns |     3 B |         1 |
| `FieldMap/length`                   |  302 ns |     0 B |         0 |
| `FieldMap/total`                    |  357 ns |     0 B |         0 |
| `FieldMap/sortedTags`               |   86 ns |     0 B |         0 |
| `FieldMap/write`                    |  463 ns |    48 B |         1 |
| `Build/tape`                        |  2.4 µs |  1.5 KB |        12 |
| `NewAndBuild/tape`                  | 12.2 µs | 19.2 KB |       269 |

#### Repeating groups

Measured on a 19-entry `NoMDEntries` group with nested `NoSecurityAltID` groups.

| Benchmark              |   ns/op |     B/op | allocs/op |
| ---------------------- | ------: | -------: | --------: |
| `RepeatingGroup/Add`   |  675 ns |   1.2 KB |        12 |
| `RepeatingGroup/Build` | 91.1 µs | 184.3 KB |      2004 |
| `RepeatingGroup/Write` | 14.4 µs |  63.8 KB |        12 |
| `RepeatingGroup/Read`  | 37.5 µs |  63.3 KB |       746 |

#### Validation and copying

| Benchmark                             |  ns/op |   B/op | allocs/op |
| ------------------------------------- | -----: | -----: | --------: |
| `Validate/RejectInvalidMessage=false` | 526 ns |   16 B |         1 |
| `Validate/RejectInvalidMessage=true`  | 5.4 µs | 2.6 KB |        84 |
| `CopyInto/tape`                       | 2.7 µs | 6.4 KB |        48 |

#### Load generator

Single session, loopback TCP, memory store, persistence on, no data dictionary:

| Rate     | Achieved |    p50 |     p99 | allocs/msg | alloc bytes/msg |
| -------- | -------: | -----: | ------: | ---------: | --------------: |
| 1,000/s  |  1,000/s | 287 µs | 4.98 ms |          — |               — |
| 5,000/s  |  5,000/s |  55 µs |  113 µs |       ~211 |          ~14 KB |
| 12,000/s | 12,000/s |  53 µs |  369 µs |          — |               — |
| 20,000/s | 20,000/s |  55 µs | 1.41 ms |          — |               — |

`allocs/msg` counts both directions, so it covers one inbound parse plus one
outbound build per order.

Scaling across sessions at 2,000/s each:

| Sessions | Delivered |    p50 | User CPU per wall second |
| -------- | --------: | -----: | -----------------------: |
| 1        |   2,000/s | 180 µs |                     ~0.4 |
| 2        |   4,000/s | 152 µs |                    ~0.45 |
| 4        |     531/s |  3.9 s |                     ~5.0 |
| 8        |     112/s |  4.0 s |                        — |

The collapse at four sessions is finding 8 below.

## What the harness found

These are the observations that motivated building it. They are targets for
optimisation work, not defects fixed here.

1. **`FieldMap.add` is the single largest allocation site**, 42% of allocated
   objects in `BenchmarkRoundTrip/dd=off`. Every field costs an insert into
   `tagLookup map[Tag]field` plus an append to `tags []Tag`, and both grow by
   rehashing and reallocating as a message is populated. Neither is presized,
   even though `doParsing` counts the fields up front in order to size the
   `[]TagValue` arena. A message with 30 fields pays for several map growth
   steps that a single presize would avoid.

2. **Inbound `Message`s are allocated per message when they need not be.**
   `BenchmarkParse/tape/fresh` costs ~27 allocations and ~8 KB; `reused` costs
   **zero**. `doParsing` already reuses the `[]TagValue` arena and clears the
   field maps in place rather than reallocating them — but `stateMachine.Incoming`
   calls `NewMessage()` for every inbound message, so that machinery never
   engages on a live session. `NewMessage()` alone is 7 allocations (the struct,
   three `sync.RWMutex`, three `map[Tag]field`) before a single field exists.

   Pooling here would be an API change: applications may legally retain a
   `*Message` past the callback today, and every `TagValue.value` aliases the
   message's raw buffer.

3. **A data dictionary makes parsing ~12× more expensive.** `BenchmarkRoundTrip`
   goes from 44 allocations to 319, and `BenchmarkSessionIncoming/receive` from
   28 to 383. `isNumInGroupField` runs per body field and each call builds a
   `[]Tag{tag}`, converts the message type to a `string`, and constructs a
   `map[int]*FieldDef` for the group's fields. All three are derivable once per
   message type rather than once per field.

4. **`TagValue.init` is the highest-frequency single-field allocation.** It
   grows `tv.bytes` from `nil` through four appends, costing 2 allocations and
   ~38 ns per field, even though the final length is known up front
   (`len(tagDigits) + 1 + len(value) + 1`). The inbound equivalent,
   `TagValue.parse`, sub-slices the raw buffer at ~8 ns and zero allocations.

5. **`formatCheckSum` uses `fmt.Sprintf("%03d")`** for a value that is always
   0–255: ~85 ns and an allocation per message, for something a 256-entry table
   or three byte writes would do for free.

6. **`build()` walks each section three times and re-sorts on every call.**
   `length()`, `total()` and `write()` each traverse the section — the first two
   by map iteration — and `write()` calls `sort.Sort` on `m.tags` unconditionally,
   even when nothing has changed since the last build. `write()` also takes the
   exclusive lock rather than a read lock, only because that sort mutates in
   place. The `bytes.Buffer` in `build()` is not pre-sized despite the body
   length already being known.

7. **`RepeatingGroup.Add()` rebuilds its tag ordering per element.** Each call
   constructs a `map[Tag]int` from the template and returns a closure over it,
   though the template is immutable for the group's lifetime.

8. **Session throughput falls off a cliff between 2 and 4 sessions.** At 2000
   msg/s per session the loadgen uses ~45% of one machine's CPU with 2 sessions
   and saturates it at 4, where round trips collapse from 150 µs to seconds. The
   CPU profile is 56% `runtime.selectgo` and 20% `runtime.nanotime1`. Each
   session runs two `internal.EventTimer`s, each with its own goroutine parked in
   a select, and both are `Reset` on every message — `peerTimer` per inbound,
   `stateTimer` per outbound. Add the unbuffered `messageOut` channel (there is
   no `OutChanCapacity` to match `InChanCapacity`) and the per-message
   `net.Conn.Write` with no `bufio.Writer`, and the scheduler does more work than
   the FIX engine does.

9. **Validation with `RejectInvalidMessage` costs 85 allocations per message**
   versus 1 without, because `validateField` constructs a `FIXString`/`FIXInt`/
   `FIXUTCTimestamp`/`FIXFloat` per field into a `FieldValue` interface.
