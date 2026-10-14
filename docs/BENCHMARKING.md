# Benchmarking and profiling gofix

This repository carries a load-test and profiling harness so that performance
changes can be argued from measurements rather than from reading the code.

Nothing here changes engine behaviour. It exists to make the engine measurable.

## The pieces

| What               | Where                                      | Measures                                                     |
| ------------------ | ------------------------------------------ | ------------------------------------------------------------ |
| Message corpus     | `internal/tape/`                           | — (shared fixture)                                           |
| Micro-benchmarks   | `bench_*_test.go` (root package)           | Individual layers: framing, parse, build, groups, validation |
| Session benchmarks | `bench_session_test.go`                    | The whole engine, socketless, plus an inbound IOI stream     |
| Load generator     | `_test/loadgen/`                           | The whole engine over real loopback TCP                      |
| Scripts            | `scripts/bench.sh`, `scripts/benchstat.sh` | Repeatable runs and comparison                               |

### The corpus

`internal/tape` generates a deterministic tape of FIX 4.4 messages. The same
`(seed, size, mix)` always produces byte-identical output, so two runs measure
the same work.

The default mix (`realistic`) models buy-side order flow: 70% ExecutionReport,
15% NewOrderSingle, 5% OrderCancelReplaceRequest, 3% OrderCancelRequest, 5%
MarketDataIncrementalRefresh (10–20 entry repeating group with nested
NoSecurityAltID), 2% Heartbeat. Other mixes: `orderflow`, `marketdata`, `flat`,
and `ioi`.

`ioi` models a counterparty streaming Indications of Interest at us: 98% IOI, 2%
Heartbeat. An IOI is mostly new interest, with some replaces and cancels that
refer back to an earlier IOIID. It usually carries a 1–3 entry NoIOIQualifiers
group and occasionally a NoRoutingIDs group naming target firms. The per-class
benchmarks (`msgtype=IOI`) draw from this mix, so the `realistic` tape and its
`tape` numbers are unaffected.

`internal/tape` deliberately imports nothing outside the standard library. It
cannot import `gofix` (the benchmarks are internal tests, so that would be an
import cycle) and it must not import `gen/…` (a gitignored build artifact). Its
FIX encoder is written independently of `Message.build()`, which makes it an
oracle rather than a mirror.

Four tests guard the `realistic` and `ioi` corpora, and they run as part of `make test`:

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

Key flags: `-scenario`, `-rate` (per session, `0` saturates), `-sessions`, `-duration`,
`-warmup` (excluded from all statistics), `-mix`, `-store=memory|file`,
`-persist`, `-validate`, `-reuse` (`ReuseIncomingMessages`), `-gomaxprocs`, and
the profile paths.

There are two scenarios:

- **`-scenario=orders`** (the default): the initiator sends NewOrderSingles and the
  acceptor answers each one with an ExecutionReport. Latency is the round trip.
- **`-scenario=ioi`**: the acceptor streams IOIs one way to the initiator,
  independently on each session, built with the generated `gen/fix44/ioi` types.
  The initiator reads them through the typed getters. Latency is one way, from
  just before `SendToTarget` to the receiving `FromApp`. `-mix` is ignored, and
  `allocs/msg` counts one build plus one parse per IOI. With
  `make loadtest`, use `LOAD_SCENARIO=ioi`.

It reports throughput, latency percentiles, and — the number this
whole exercise is about — **allocations per message**, taken from a
`runtime.MemStats` delta across the measurement window only.

Latency is measured without allocating: ClOrdIDs and IOIIDs are sequential
integers, so send times live in a preallocated ring indexed by sequence number
and matching a message is an array index.

### Reading the load generator's numbers

- **`-rate=0` saturates.** The sender outruns the engine, work queues up, and
  latency becomes a measure of queue depth rather than of the engine. Use it for
  maximum throughput and allocation rate; ignore its latency percentiles.
- **Low rates inflate latency.** At 1000/s the p50 is roughly 5× the p50 at
  5000/s, because every message pays a scheduler wakeup on an otherwise idle
  path. This is real, but it is not what you are optimising.
- **`unmatched` should be zero.** If it is not, replies arrived that could not be
  matched to a send, and the latency percentiles are incomplete.

## Before and after (#11 → #24)

The performance work in #12–#24, measured end to end. **Before** is `acde97c5`
(#11, the last commit before any of it) and **after** is `9b31b5f6` (#24).

Apple M2 (4 performance + 4 efficiency cores), macOS 27, Go 1.26.4,
`GOMAXPROCS=4`, `-count=10 -benchtime=1s`. Times are medians and `Δ time` is
benchstat's: `~` means no significant difference (p ≥ 0.05). Allocation counts
are exact.

Both commits ran the same benchmark code: this commit's `bench_*_test.go`,
`internal/tape` and `_test/loadgen`, copied onto a worktree of `acde97c5`. The
exceptions are things that did not exist before:

- Everything marked `reuse=on` or `recycled`. `ReuseIncomingMessages` arrived in
  #19, so these show `—` before.
- The `FieldMap` benchmarks other than `write`. #22 replaced
  `length`/`total`/`sortedTags` with `lengthAndTotal`/`sort`, so they are not
  compared.

To reproduce:

```sh
git worktree add ../gofix-baseline acde97c5
cp bench_*_test.go ../gofix-baseline/
cp -R internal/tape ../gofix-baseline/internal/
cp -R _test/loadgen ../gofix-baseline/_test/
# In the worktree, remove the reuse=on cases, the recycled parser variant and
# -reuse, which do not compile there. Then run the same commands on both sides:
GOMAXPROCS=4 go test -run '^$' -bench . -benchmem -count=10 -benchtime=1s . > before.txt
go run golang.org/x/perf/cmd/benchstat@latest before.txt after.txt
```

### In short

- **Receiving with a data dictionary is 7–18× faster.**
  `SessionIncoming/receive/dd=on` drops from 60.6 µs and 354 allocations to
  8.8 µs and 11. Without a dictionary it drops from 5.3 µs and 29 allocations to
  3.8 µs and 11.
- **With `ReuseIncomingMessages`, receiving allocates nothing**, with or without
  a dictionary.
- **Sending allocates less than half as much.** `SessionOutbound` drops from 288
  to 123 allocations and is 28% faster. `Build` alone drops from 12 to 3
  allocations.
- **Sessions scale.** At 2,000 orders/s each, the baseline collapses from 2
  sessions upwards (`GOMAXPROCS=4`), delivering under 250/s with round trips of
  seconds. Now 8 sessions deliver all 16,000/s at a 43 µs median.
- **Per message on the wire**, the loadgen falls from 217 to 110 allocations
  per order round trip, and from 199 to 112 per IOI.
- **A few things got slightly slower:**
  - `GetField` hits, by 6–8% (1–2 ns)
  - `ParserReadMessage` without buffer recycling, by 9% (14 ns)
  - `FIXInt` reads, by 9% (0.3 ns)

  These have not been investigated. They are small against the savings on the
  same paths.

#### End to end

`RoundTrip` is parse plus build with no session. `SessionIncoming` adds the state machine, sequence checking and the application callback, still without a socket.

| Benchmark                                          | ns/op before |  after |  Δ time | B/op before |   after | allocs before | after |
| -------------------------------------------------- | -----------: | -----: | ------: | ----------: | ------: | ------------: | ----: |
| `RoundTrip/dd=off`                                 |       5.9 µs | 4.3 µs | -27.08% |      9.5 KB |  7.7 KB |            45 |    16 |
| `RoundTrip/dd=on`                                  |      52.9 µs | 4.5 µs | -91.47% |     87.7 KB |  7.8 KB |           286 |    16 |
| `SessionIncoming/receive/dd=off`                   |       5.3 µs | 3.8 µs | -27.45% |      8.3 KB |  7.2 KB |            29 |    11 |
| `SessionIncoming/receive/dd=on`                    |      60.6 µs | 8.8 µs | -85.52% |     88.9 KB |  7.2 KB |           354 |    11 |
| `SessionIncoming/receive/dd=off/reuse=on`          |            — | 2.9 µs |       — |           — |     0 B |             — |     0 |
| `SessionIncoming/receive/dd=on/reuse=on`           |            — | 7.9 µs |       — |           — |     0 B |             — |     0 |
| `SessionIncoming/echo/dd=off/persist=off`          |      10.7 µs | 6.9 µs | -35.19% |     12.7 KB | 10.1 KB |           138 |    41 |
| `SessionIncoming/echo/dd=off/persist=on`           |      10.9 µs | 7.1 µs | -34.61% |     12.8 KB | 10.2 KB |           138 |    41 |
| `SessionIncoming/echo/dd=off/persist=off/reuse=on` |            — | 5.9 µs |       — |           — |  2.8 KB |             — |    29 |
| `SessionOutbound/persist=false`                    |      12.9 µs | 9.3 µs | -27.95% |     20.0 KB | 17.4 KB |           288 |   123 |
| `SessionOutbound/persist=true`                     |      12.9 µs | 9.1 µs | -29.35% |     20.1 KB | 17.6 KB |           288 |   123 |

#### Inbound

`fresh` allocates a `Message` per parse, as a session without `ReuseIncomingMessages` does. `reused` parses into one `Message`, as a session with it does.

| Benchmark                                   | ns/op before |  after |  Δ time | B/op before |  after | allocs before | after |
| ------------------------------------------- | -----------: | -----: | ------: | ----------: | -----: | ------------: | ----: |
| `ParserReadMessage/mix=realistic`           |       158 ns | 172 ns |  +9.02% |       557 B |  557 B |             2 |     2 |
| `ParserReadMessage/mix=realistic/recycled`  |            — |  78 ns |       — |           — |    0 B |             — |     0 |
| `ParserReadMessage/mix=marketdata`          |       402 ns | 414 ns |       ~ |      2.8 KB | 2.8 KB |             2 |     2 |
| `ParserReadMessage/mix=marketdata/recycled` |            — | 160 ns |       — |           — |    0 B |             — |     0 |
| `Parse/tape/fresh`                          |       3.6 µs | 2.6 µs | -27.87% |      8.3 KB | 7.2 KB |            28 |    11 |
| `Parse/tape/reused`                         |       1.7 µs | 1.7 µs |  -3.10% |         0 B |    0 B |             0 |     0 |
| `ParseWithDD/tape/fresh`                    |      51.2 µs | 2.8 µs | -94.47% |     86.2 KB | 7.2 KB |           270 |    11 |
| `ParseWithDD/tape/reused`                   |      49.5 µs | 1.9 µs | -96.24% |     78.0 KB |    0 B |           242 |     0 |
| `ReadAndParse/dd=off`                       |       3.7 µs | 2.7 µs | -26.08% |      8.9 KB | 7.8 KB |            30 |    13 |
| `ReadAndParse/dd=on`                        |      50.6 µs | 2.9 µs | -94.18% |     86.8 KB | 7.8 KB |           272 |    13 |
| `ExtractField`                              |       478 ns | 474 ns |       ~ |         0 B |    0 B |             0 |     0 |
| `GetField/hit/GetBytes`                     |        16 ns |  17 ns |  +7.42% |         0 B |    0 B |             0 |     0 |
| `GetField/hit/GetString`                    |        33 ns |  35 ns |  +6.37% |        24 B |   24 B |             2 |     2 |
| `GetField/hit/GetInt`                       |        18 ns |  19 ns |  +8.19% |         0 B |    0 B |             0 |     0 |
| `GetField/miss/GetBytes`                    |       128 ns |  21 ns | -83.26% |       128 B |    8 B |             4 |     1 |

#### Inbound, per message class

The last row is the worst case in the corpus: a ~3.3 KB market data message with a 19-entry repeating group, parsed against the FIX 4.4 dictionary.

| Benchmark                                         | ns/op before |   after |  Δ time | B/op before |   after | allocs before | after |
| ------------------------------------------------- | -----------: | ------: | ------: | ----------: | ------: | ------------: | ----: |
| `Parse/msgtype=Heartbeat/fresh`                   |       664 ns |  595 ns | -10.36% |      1.6 KB |  1.7 KB |            15 |     8 |
| `Parse/msgtype=OrderCancel/fresh`                 |       1.4 µs |  1.2 µs | -18.76% |      3.4 KB |  3.2 KB |            24 |    12 |
| `Parse/msgtype=IOI/fresh`                         |       2.0 µs |  1.5 µs | -23.11% |      4.9 KB |  4.2 KB |            26 |    12 |
| `Parse/msgtype=NewOrderSingle/fresh`              |       2.8 µs |  2.1 µs | -23.35% |      6.2 KB |  6.3 KB |            27 |    11 |
| `Parse/msgtype=ExecutionReport/fresh`             |       3.2 µs |  2.1 µs | -34.55% |      8.4 KB |  6.6 KB |            29 |    12 |
| `Parse/msgtype=MarketDataIncremental/fresh`       |      12.6 µs | 11.7 µs |  -7.19% |     21.7 KB | 24.6 KB |            27 |    11 |
| `ParseWithDD/msgtype=IOI/fresh`                   |       2.9 µs |  1.6 µs | -45.91% |      5.5 KB |  4.1 KB |            50 |    11 |
| `ParseWithDD/msgtype=ExecutionReport/fresh`       |       4.5 µs |  2.2 µs | -50.88% |      8.9 KB |  6.6 KB |            60 |    12 |
| `ParseWithDD/msgtype=NewOrderSingle/fresh`        |       6.6 µs |  2.3 µs | -64.61% |     11.2 KB |  6.3 KB |           120 |    11 |
| `ParseWithDD/msgtype=MarketDataIncremental/fresh` |     850.3 µs | 12.6 µs | -98.52% |     1.42 MB | 24.6 KB |          3695 |    11 |

#### Outbound

`Build` serialises an already-populated message; `NewAndBuild` also constructs and populates it.

| Benchmark                           | ns/op before |  after |  Δ time | B/op before |   after | allocs before | after |
| ----------------------------------- | -----------: | -----: | ------: | ----------: | ------: | ------------: | ----: |
| `NewMessage`                        |       147 ns |  57 ns | -61.02% |       440 B |   416 B |             7 |     1 |
| `TagValue/init`                     |        37 ns |  23 ns | -37.61% |        32 B |    24 B |             2 |     1 |
| `TagValue/parse`                    |       4.7 ns | 4.7 ns |       ~ |         0 B |     0 B |             0 |     0 |
| `SetField/SetBytes`                 |        46 ns |  34 ns | -26.90% |        32 B |    24 B |             2 |     1 |
| `SetField/SetString`                |        59 ns |  34 ns | -42.63% |        56 B |    24 B |             3 |     1 |
| `SetField/SetInt`                   |        64 ns |  37 ns | -42.24% |        32 B |    16 B |             3 |     1 |
| `SetField/SetField`                 |        60 ns |  45 ns | -24.70% |        56 B |    48 B |             3 |     2 |
| `SetField/populate/ExecutionReport` |       4.2 µs | 3.0 µs | -28.29% |      8.1 KB |  7.6 KB |           129 |    48 |
| `FormatCheckSum`                    |        50 ns | 0.8 ns | -98.33% |         3 B |     0 B |             1 |     0 |
| `FieldMap/write`                    |       456 ns | 362 ns | -20.42% |        48 B |     0 B |             1 |     0 |
| `Build/tape`                        |       2.4 µs | 1.6 µs | -33.59% |      1.5 KB |   525 B |            12 |     3 |
| `NewAndBuild/tape`                  |      12.3 µs | 8.9 µs | -27.99% |     19.7 KB | 17.2 KB |           269 |   113 |

#### Repeating groups

Measured on a 19-entry `NoMDEntries` group with nested `NoSecurityAltID` groups.

| Benchmark              | ns/op before |   after |  Δ time | B/op before |    after | allocs before | after |
| ---------------------- | -----------: | ------: | ------: | ----------: | -------: | ------------: | ----: |
| `RepeatingGroup/Add`   |       682 ns |  303 ns | -55.53% |      1.3 KB |    800 B |            12 |     7 |
| `RepeatingGroup/Build` |      90.5 µs | 62.1 µs | -31.37% |    188.7 KB | 161.5 KB |          2004 |   810 |
| `RepeatingGroup/Write` |      14.2 µs | 10.0 µs | -29.57% |     65.4 KB |  65.4 KB |            12 |    11 |
| `RepeatingGroup/Read`  |      37.9 µs | 33.4 µs | -11.85% |     64.9 KB |  62.0 KB |           746 |   340 |

#### Validation and copying

`Validate/RejectInvalidMessage=*` had wide run-to-run spread on the baseline (±64% and ±122%). The allocation counts are exact regardless.

| Benchmark                             | ns/op before |  after |  Δ time | B/op before |  after | allocs before | after |
| ------------------------------------- | -----------: | -----: | ------: | ----------: | -----: | ------------: | ----: |
| `Validate/RejectInvalidMessage=false` |       491 ns | 487 ns |       ~ |        16 B |    0 B |             1 |     0 |
| `Validate/RejectInvalidMessage=true`  |       5.3 µs | 3.2 µs | -39.47% |      2.7 KB |    0 B |            84 |     0 |
| `CopyInto/tape`                       |       2.7 µs | 1.4 µs | -46.45% |      6.6 KB | 4.8 KB |            48 |    13 |

#### Load generator

Loopback TCP, memory store, persistence on, no data dictionary, no
`ReuseIncomingMessages`, `-gomaxprocs=4`, 20 s measured after a 5 s warmup.
`allocs/msg` counts both sides of the process: for orders, one inbound parse
plus one outbound build in each direction per round trip.

Single session, orders, round-trip latency:

| Rate     | p50 before |  after | p99 before |  after | allocs/msg before | after | bytes/msg before |   after |
| -------- | ---------: | -----: | ---------: | -----: | ----------------: | ----: | ---------------: | ------: |
| 1,000/s  |     295 µs | 287 µs |     483 µs | 557 µs |               216 |   110 |          14.5 KB | 12.4 KB |
| 5,000/s  |      55 µs |  51 µs |      78 µs |  80 µs |               217 |   110 |          14.6 KB | 12.5 KB |
| 12,000/s |      54 µs |  49 µs |     2.6 ms |  84 µs |               217 |   110 |          14.5 KB | 12.5 KB |
| 20,000/s |      57 µs |  50 µs |    17.3 ms | 106 µs |               217 |   110 |          14.6 KB | 12.5 KB |

At 12,000/s and above the baseline's p99 grows to milliseconds. After, it stays
near 100 µs.

Saturated (`-rate=0`). Latency here measures queue depth, so only throughput is
shown. Delivered counts replies for orders and IOIs received for `ioi`:

| Scenario          | Sent before |     after | Delivered before |     after |
| ----------------- | ----------: | --------: | ---------------: | --------: |
| orders, 1 session |    91,050/s | 115,491/s |         27,835/s |  84,791/s |
| ioi, 1 session    |   114,264/s | 147,054/s |         39,409/s | 147,055/s |

Orders, 2,000/s per session:

| Sessions |  Offered | Delivered before |    after | p50 before |  after | p99 before |  after | allocs/msg before | after |
| -------: | -------: | ---------------: | -------: | ---------: | -----: | ---------: | -----: | ----------------: | ----: |
|        1 |  2,000/s |          2,000/s |  2,000/s |     193 µs | 188 µs |     442 µs | 319 µs |               217 |   110 |
|        2 |  4,000/s |            241/s |  4,000/s |      7.7 s | 152 µs |     22.5 s | 311 µs |                 — |   110 |
|        4 |  8,000/s |             88/s |  8,000/s |     14.0 s |  76 µs |     23.6 s | 109 µs |                 — |   110 |
|        8 | 16,000/s |             86/s | 16,000/s |     14.2 s |  43 µs |     24.7 s |  74 µs |                 — |   110 |

`—`: the baseline delivered so little that allocations per delivered message
mean nothing. At 8 sessions the baseline also failed to shut down and had to be
killed. Its results were already written.

### Inbound IOI stream

A counterparty streaming Indications of Interest at us is a use case we support
in production, so it has its own benchmarks. The IOI corpus is described under
[The corpus](#the-corpus).

Micro-benchmarks:

| Benchmark                                    | ns/op before |  after |  Δ time | B/op before |  after | allocs before | after |
| -------------------------------------------- | -----------: | -----: | ------: | ----------: | -----: | ------------: | ----: |
| `SessionIncomingIOI/receive/dd=off`          |       3.1 µs | 2.4 µs | -23.27% |      4.8 KB | 4.1 KB |            26 |    11 |
| `SessionIncomingIOI/receive/dd=on`           |       8.3 µs | 4.9 µs | -40.78% |      6.8 KB | 4.1 KB |            89 |    11 |
| `SessionIncomingIOI/receive/dd=off/reuse=on` |            — | 1.7 µs |       — |           — |    0 B |             — |     0 |
| `SessionIncomingIOI/receive/dd=on/reuse=on`  |            — | 4.3 µs |       — |           — |    0 B |             — |     0 |
| `Parse/msgtype=IOI/fresh`                    |       2.0 µs | 1.5 µs | -23.11% |      4.9 KB | 4.2 KB |            26 |    12 |
| `Parse/msgtype=IOI/reused`                   |       849 ns | 832 ns |  -1.97% |         0 B |    0 B |             0 |     0 |
| `ParseWithDD/msgtype=IOI/fresh`              |       2.9 µs | 1.6 µs | -45.91% |      5.5 KB | 4.1 KB |            50 |    11 |
| `ParseWithDD/msgtype=IOI/reused`             |       1.7 µs | 914 ns | -47.56% |       723 B |    0 B |            24 |     0 |
| `CopyInto/msgtype=IOI`                       |       1.8 µs | 1.1 µs | -38.14% |      4.3 KB | 3.4 KB |            36 |    14 |
| `Build/msgtype=IOI`                          |       1.6 µs | 1.0 µs | -37.45% |       721 B |  267 B |            11 |     3 |
| `NewAndBuild/msgtype=IOI`                    |       5.6 µs | 4.2 µs | -25.23% |      8.0 KB | 7.2 KB |           141 |    66 |

IOIs are lighter than the realistic tape: about two dozen fields and small
groups. Even so, receiving with the data dictionary used to cost 2.7× as much
as receiving without one, and now costs about 2×. With `ReuseIncomingMessages` on, receiving IOIs
allocates nothing.

The load generator with `-scenario=ioi`, single session, one-way latency:

| Rate     | p50 before | after | p99 before | after | allocs/msg before | after | bytes/msg before |   after |
| -------- | ---------: | ----: | ---------: | ----: | ----------------: | ----: | ---------------: | ------: |
| 5,000/s  |      29 µs | 27 µs |      62 µs | 58 µs |               199 |   112 |          14.0 KB | 12.4 KB |
| 20,000/s |      25 µs | 23 µs |     109 µs | 42 µs |               199 |   112 |          14.1 KB | 12.4 KB |

Saturated, the sender built 147,000 IOIs/s and the receiver kept pace. The
baseline received 39,000/s of the 114,000/s sent, and queued the rest (see the
table above).

Independent streams of 2,000 IOIs/s per session:

| Sessions |  Offered | Delivered before |    after | p50 before |  after | p99 before |  after | allocs/msg before | after |
| -------: | -------: | ---------------: | -------: | ---------: | -----: | ---------: | -----: | ----------------: | ----: |
|        1 |  2,000/s |          2,000/s |  2,000/s |     131 µs | 123 µs |     303 µs | 201 µs |               199 |   112 |
|        4 |  8,000/s |            196/s |  8,000/s |     13.2 s |  43 µs |     22.5 s |  66 µs |                 — |   112 |
|       16 | 32,000/s |            182/s | 32,000/s |     14.5 s |  18 µs |     24.2 s |  42 µs |                 — |   112 |

Sixteen concurrent IOI streams now deliver 32,000/s at an 18 µs median. Before,
four streams were enough to stall delivery.

## What the harness found

These are the observations that motivated building the harness, recorded as
they were made against the baseline. Each now ends with what became of it; see
[Before and after](#before-and-after-11--24) for the measurements.

1. **`FieldMap.add` is the single largest allocation site**, 42% of allocated
   objects in `BenchmarkRoundTrip/dd=off`. Every field costs an insert into
   `tagLookup map[Tag]field` plus an append to `tags []Tag`, and both grow by
   rehashing and reallocating as a message is populated. Neither is presized,
   even though `doParsing` counts the fields up front in order to size the
   `[]TagValue` arena. A message with 30 fields pays for several map growth
   steps that a single presize would avoid.

   **Now:** #18 presizes field storage from the parsed field count and carves
   fields from shared chunks. `NewMessage` is one allocation, and
   `Parse/tape/fresh` drops from 28 to 11.

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

   **Now:** #19 adds `ReuseIncomingMessages`, opt-in for the API reason above. With
   it on, `SessionIncoming/receive` allocates nothing.

3. **A data dictionary makes parsing ~12× more expensive.** `BenchmarkRoundTrip`
   goes from 44 allocations to 319, and `BenchmarkSessionIncoming/receive` from
   28 to 383. `isNumInGroupField` runs per body field and each call builds a
   `[]Tag{tag}`, converts the message type to a `string`, and constructs a
   `map[int]*FieldDef` for the group's fields. All three are derivable once per
   message type rather than once per field.

   **Now:** #12 resolves the message definition once per message and looks group
   members up through an index. `ParseWithDD/tape/fresh` drops from 270 allocations
   to 11, the same as without a dictionary.

4. **`TagValue.init` is the highest-frequency single-field allocation.** It
   grows `tv.bytes` from `nil` through four appends, costing 2 allocations and
   ~38 ns per field, even though the final length is known up front
   (`len(tagDigits) + 1 + len(value) + 1`). The inbound equivalent,
   `TagValue.parse`, sub-slices the raw buffer at ~8 ns and zero allocations.

   **Now:** #16 sizes the field exactly. `TagValue/init` drops to one allocation.

5. **`formatCheckSum` uses `fmt.Sprintf("%03d")`** for a value that is always
   0–255: ~85 ns and an allocation per message, for something a 256-entry table
   or three byte writes would do for free.

   **Now:** #21 computes the digits directly. `FormatCheckSum` takes under a
   nanosecond and allocates nothing.

6. **`build()` walks each section three times and re-sorts on every call.**
   `length()`, `total()` and `write()` each traverse the section — the first two
   by map iteration — and `write()` calls `sort.Sort` on `m.tags` unconditionally,
   even when nothing has changed since the last build. `write()` also takes the
   exclusive lock rather than a read lock, only because that sort mutates in
   place. The `bytes.Buffer` in `build()` is not pre-sized despite the body
   length already being known.

   **Now:** #22 makes one length-and-total walk, sorts only after a change, and
   presizes the buffer. `Build/tape` drops from 12 allocations to 3.

7. **`RepeatingGroup.Add()` rebuilds its tag ordering per element.** Each call
   constructs a `map[Tag]int` from the template and returns a closure over it,
   though the template is immutable for the group's lifetime.

   **Now:** #21 builds the ordering once per group. `RepeatingGroup/Build` drops
   from 2004 allocations to 810.

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

   **Now:** #23 has a session wait for its write loop instead of spinning, buffers
   `messageOut`, and batches writes through a `bufio.Writer`. At 2,000/s each, 8
   sessions deliver 16,000/s at a 43 µs median.

9. **Validation with `RejectInvalidMessage` costs 85 allocations per message**
   versus 1 without, because `validateField` constructs a `FIXString`/`FIXInt`/
   `FIXUTCTimestamp`/`FIXFloat` per field into a `FieldValue` interface.

   **Now:** #20 checks formats into concrete locals.
   `Validate/RejectInvalidMessage=true` allocates nothing.
