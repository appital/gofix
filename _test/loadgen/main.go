// Command loadgen drives a gofix acceptor and initiator against each other
// over loopback TCP at a controlled rate, and reports throughput, latency and
// allocation per message.
//
// Two scenarios are modelled. "orders" has the initiator send orders and time
// the round trip to each ExecutionReport. "ioi" has the acceptor stream
// Indications of Interest one way to the initiator, and times their delivery.
//
// It lives under _test/ because it imports the generated gen/... packages,
// which are build artifacts excluded from the module's normal package set.
// Build it with `make build-loadgen`.
//
// The socketless equivalent - the same engine work with no kernel in the way -
// is BenchmarkSessionIncoming in the root package.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	// Registers the pprof handlers on http.DefaultServeMux.
	_ "net/http/pprof"

	"github.com/appital/gofix"
	"github.com/appital/gofix/internal/tape"
)

type options struct {
	scenario string
	duration time.Duration
	warmup   time.Duration
	rate     int
	sessions int
	mix      string
	seed     int64
	inflight int

	store    string
	persist  bool
	validate bool
	reuse    bool

	cpuProfile   string
	memProfile   string
	blockProfile string
	mutexProfile string
	tracePath    string
	pprofAddr    string

	gomaxprocs int
	jsonOut    string
	quiet      bool
}

func main() {
	opts := parseFlags()

	if opts.gomaxprocs > 0 {
		runtime.GOMAXPROCS(opts.gomaxprocs)
	}
	if opts.pprofAddr != "" {
		go func() {
			if err := http.ListenAndServe(opts.pprofAddr, nil); err != nil {
				fmt.Fprintf(os.Stderr, "pprof server: %v\n", err)
			}
		}()
		fmt.Fprintf(os.Stderr, "pprof listening on http://%s/debug/pprof/\n", opts.pprofAddr)
	}

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	var o options

	flag.StringVar(&o.scenario, "scenario", scenarioOrders,
		"orders: round-tripped orders and fills; ioi: a one-way IOI stream from acceptor to initiator")
	flag.DurationVar(&o.duration, "duration", 30*time.Second, "measurement window")
	flag.DurationVar(&o.warmup, "warmup", 5*time.Second, "warmup before measuring; excluded from all stats")
	flag.IntVar(&o.rate, "rate", 5000, "messages per second per session; 0 saturates")
	flag.IntVar(&o.sessions, "sessions", 1, "concurrent FIX sessions")
	flag.StringVar(&o.mix, "mix", "orderflow", "corpus mix: realistic, orderflow, marketdata, flat; ignored by -scenario=ioi")
	flag.Int64Var(&o.seed, "seed", 20240603, "corpus seed; keep fixed to compare runs")
	flag.IntVar(&o.inflight, "inflight", 1<<20, "latency ring size; must exceed messages in flight")

	flag.StringVar(&o.store, "store", "memory", "message store: memory or file")
	flag.BoolVar(&o.persist, "persist", true, "persist outbound messages to the store")
	flag.BoolVar(&o.validate, "validate", false, "validate inbound messages against the FIX 4.4 data dictionary")
	flag.BoolVar(&o.reuse, "reuse", false, "enable ReuseIncomingMessages on every session")

	flag.StringVar(&o.cpuProfile, "cpuprofile", "", "write a CPU profile here")
	flag.StringVar(&o.memProfile, "memprofile", "", "write a heap profile here")
	flag.StringVar(&o.blockProfile, "blockprofile", "", "write a block profile here")
	flag.StringVar(&o.mutexProfile, "mutexprofile", "", "write a mutex profile here")
	flag.StringVar(&o.tracePath, "trace", "", "write an execution trace here")
	flag.StringVar(&o.pprofAddr, "pprof", "", "serve live pprof on this address, e.g. :6060")

	flag.IntVar(&o.gomaxprocs, "gomaxprocs", 0, "override GOMAXPROCS; pin it to reduce run-to-run variance")
	flag.StringVar(&o.jsonOut, "json", "", `write results as JSON to this path, or "-" for stdout`)
	flag.BoolVar(&o.quiet, "quiet", false, "suppress the human-readable summary")

	flag.Parse()

	if o.scenario == scenarioIOI {
		o.mix = tape.MixIOI.Name
	}

	return o
}

const (
	scenarioOrders = "orders"
	scenarioIOI    = "ioi"
)

func run(opts options) error {
	build := orderFromEntry
	switch opts.scenario {
	case scenarioOrders:
	case scenarioIOI:
		build = ioiFromEntry
	default:
		return fmt.Errorf("unknown scenario %q", opts.scenario)
	}

	mix, ok := tape.Mixes[opts.mix]
	if !ok {
		return fmt.Errorf("unknown mix %q", opts.mix)
	}

	port, err := freePort()
	if err != nil {
		return fmt.Errorf("finding a free port: %w", err)
	}

	entries := tape.Gen(opts.seed, 512, mix)
	if opts.scenario == scenarioIOI {
		// The mix's heartbeats model a quiet counterparty for the parser
		// benchmarks; here the engine sends its own, so stream only the IOIs.
		entries = slices.DeleteFunc(entries, func(e tape.Entry) bool { return e.MsgType != tape.MsgTypeIOI })
	}

	hist := &histogram{}
	server := newServerApp()
	client := newClientApp(hist, opts.inflight)

	// Separate counters per side, so "sent" really means client -> server.
	clientBytes := &byteCounter{}
	serverBytes := &byteCounter{}

	acceptor, err := startAcceptor(opts, port, server, serverBytes)
	if err != nil {
		return err
	}
	defer acceptor.Stop()

	initiator, err := startInitiator(opts, port, client, clientBytes)
	if err != nil {
		return err
	}
	defer initiator.Stop()

	sessionIDs, err := awaitLogon(client, opts.sessions, 30*time.Second)
	if err != nil {
		return err
	}

	stop := make(chan struct{})
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	var wg sync.WaitGroup
	for _, sessionID := range sessionIDs {
		wg.Add(1)
		go func(sessionID gofix.SessionID) {
			defer wg.Done()
			// IOIs flow the other way: the acceptor's end of the session sends.
			if opts.scenario == scenarioIOI {
				sessionID = counterparty(sessionID)
			}
			pump(client, sessionID, entries, opts.rate, build, stop)
		}(sessionID)
	}

	if opts.warmup > 0 {
		fmt.Fprintf(os.Stderr, "warming up for %v...\n", opts.warmup)
		select {
		case <-time.After(opts.warmup):
		case <-interrupt:
			close(stop)
			wg.Wait()
			return nil
		}
	}

	profiles, err := startProfiling(opts)
	if err != nil {
		close(stop)
		wg.Wait()
		return err
	}

	// Everything before this point is excluded from the numbers.
	runtime.GC()
	client.resetCounters()
	clientBytes.reset()
	serverBytes.reset()
	hist.reset()
	memBefore := snapshotMem()
	clientBytes.enabled.Store(true)
	serverBytes.enabled.Store(true)
	client.measuring.Store(true)

	fmt.Fprintf(os.Stderr, "measuring for %v...\n", opts.duration)
	start := time.Now()
	select {
	case <-time.After(opts.duration):
	case <-interrupt:
	}
	elapsed := time.Since(start)

	client.measuring.Store(false)
	clientBytes.enabled.Store(false)
	serverBytes.enabled.Store(false)
	memAfter := snapshotMem()

	close(stop)
	wg.Wait()

	if err := profiles.stop(); err != nil {
		return err
	}

	res := summarise(opts, elapsed, client, hist, clientBytes, memAfter.since(memBefore))

	if n := server.errors.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "warning: the acceptor failed to answer %d orders\n", n)
	}

	if !opts.quiet {
		res.writeText(os.Stdout)
	}
	if opts.jsonOut != "" {
		w := io.Writer(os.Stdout)
		if opts.jsonOut != "-" {
			f, err := os.Create(opts.jsonOut)
			if err != nil {
				return fmt.Errorf("creating %s: %w", opts.jsonOut, err)
			}
			defer f.Close()
			w = f
		}

		if err := res.writeJSON(w); err != nil {
			return fmt.Errorf("writing JSON results: %w", err)
		}
	}

	return nil
}

// pump sends messages on one session at the requested rate until stopped.
// build stamps seq into each message as its correlation ID (ClOrdID or IOIID),
// which is how the client finds the send time when the message or its reply
// arrives.
func pump(
	client *clientApp, sessionID gofix.SessionID, entries []tape.Entry, rate int,
	build func(tape.Entry, uint64) *gofix.Message, stop <-chan struct{},
) {
	var interval time.Duration
	if rate > 0 {
		interval = time.Second / time.Duration(rate)
	}

	next := time.Now()
	var seq uint64

	for {
		select {
		case <-stop:
			return
		default:
		}

		seq++
		entry := entries[int(seq)%len(entries)]
		msg := build(entry, seq)

		// Stamp the send time before handing the message to the engine, so the
		// measured round trip includes the engine's own outbound work.
		atomic.StoreInt64(&client.sendNanos[seq&client.ringMask], time.Now().UnixNano())

		if err := gofix.SendToTarget(msg, sessionID); err != nil {
			return
		}
		if client.measuring.Load() {
			client.sent.Add(1)
		}

		if interval == 0 {
			continue
		}

		next = next.Add(interval)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		} else if d < -time.Second {
			// Fell more than a second behind; stop trying to catch up or the
			// pacer will burst forever.
			next = time.Now()
		}
	}
}

// counterparty returns the acceptor's view of an initiator session.
func counterparty(id gofix.SessionID) gofix.SessionID {
	return gofix.SessionID{
		BeginString:      id.BeginString,
		SenderCompID:     id.TargetCompID,
		SenderSubID:      id.TargetSubID,
		SenderLocationID: id.TargetLocationID,
		TargetCompID:     id.SenderCompID,
		TargetSubID:      id.SenderSubID,
		TargetLocationID: id.SenderLocationID,
		Qualifier:        id.Qualifier,
	}
}

func awaitLogon(client *clientApp, want int, timeout time.Duration) ([]gofix.SessionID, error) {
	var ids []gofix.SessionID
	deadline := time.After(timeout)

	for len(ids) < want {
		select {
		case id := <-client.loggedOn:
			ids = append(ids, id)
		case <-deadline:
			return nil, fmt.Errorf("only %d of %d sessions logged on within %v", len(ids), want, timeout)
		}
	}

	return ids, nil
}

func summarise(
	opts options, elapsed time.Duration,
	client *clientApp, hist *histogram, wire *byteCounter, mem memSnapshot,
) results {
	sent := client.sent.Load()
	received := client.received.Load()
	secs := elapsed.Seconds()

	// Every order produces one ExecutionReport, so the engine handles roughly
	// twice the client's send count end to end. An IOI is built and parsed once
	// with no reply, so there each one counts once.
	engineMessages := sent + received
	if opts.scenario == scenarioIOI {
		engineMessages = received
	}
	if engineMessages == 0 {
		engineMessages = 1
	}

	return results{
		Config: map[string]string{
			"scenario":   opts.scenario,
			"mix":        opts.mix,
			"rate":       strconv.Itoa(opts.rate),
			"sessions":   strconv.Itoa(opts.sessions),
			"store":      opts.store,
			"persist":    strconv.FormatBool(opts.persist),
			"validate":   strconv.FormatBool(opts.validate),
			"reuse":      strconv.FormatBool(opts.reuse),
			"gomaxprocs": strconv.Itoa(runtime.GOMAXPROCS(0)),
			"seed":       strconv.FormatInt(opts.seed, 10),
		},

		DurationSec: secs,
		Sent:        sent,
		Received:    received,
		BytesSent:   wire.out.Load(),
		BytesRecv:   wire.in.Load(),
		Unmatched:   client.unmatched.Load(),

		SendRate:   float64(sent) / secs,
		RecvRate:   float64(received) / secs,
		Throughput: float64(wire.out.Load()+wire.in.Load()) / secs / (1 << 20),

		LatencyMeanNs:  hist.mean().Nanoseconds(),
		LatencyP50Ns:   hist.quantile(0.50).Nanoseconds(),
		LatencyP90Ns:   hist.quantile(0.90).Nanoseconds(),
		LatencyP99Ns:   hist.quantile(0.99).Nanoseconds(),
		LatencyP999Ns:  hist.quantile(0.999).Nanoseconds(),
		LatencyMaxNs:   int64(atomic.LoadUint64(&hist.max)),
		LatencySamples: atomic.LoadUint64(&hist.count),

		Mallocs:         mem.mallocs,
		TotalAllocBytes: mem.totalAlloc,
		AllocsPerMsg:    float64(mem.mallocs) / float64(engineMessages),
		BytesPerMsg:     float64(mem.totalAlloc) / float64(engineMessages),
		NumGC:           mem.numGC,
		GCPauseTotalNs:  mem.pauseNs,
	}
}

func (a *clientApp) resetCounters() {
	a.sent.Store(0)
	a.received.Store(0)
	a.unmatched.Store(0)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}
