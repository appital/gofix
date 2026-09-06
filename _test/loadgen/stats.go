package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"runtime"
	"sort"
	"sync/atomic"
	"time"
)

// histogram is a fixed-bucket latency histogram. Buckets are logarithmic, so a
// few hundred of them span nanoseconds to seconds at better than 2% resolution.
// Recording is a shift, a bit count and an atomic add - no allocation, nothing
// that would perturb the measurement it is part of.
type histogram struct {
	buckets [nBuckets]uint64
	count   uint64
	sum     uint64
	max     uint64
}

const (
	subBucketBits = 5
	subBuckets    = 1 << subBucketBits
	nBuckets      = 64 * subBuckets
)

func bucketOf(v uint64) int {
	if v < subBuckets {
		return int(v)
	}

	exp := 63 - leadingZeros(v)
	shift := uint(exp - subBucketBits)
	mantissa := (v >> shift) - subBuckets

	return int((uint64(shift)+1)<<subBucketBits + mantissa)
}

func bucketValue(i int) uint64 {
	if i < subBuckets {
		return uint64(i)
	}

	shift := uint(i>>subBucketBits) - 1
	mantissa := uint64(i&(subBuckets-1)) + subBuckets

	return mantissa << shift
}

func leadingZeros(v uint64) int {
	n := 0
	for i := 63; i >= 0; i-- {
		if v&(1<<uint(i)) != 0 {
			return n
		}
		n++
	}
	return 64
}

func (h *histogram) record(nanos int64) {
	if nanos < 0 {
		return
	}
	v := uint64(nanos)

	atomic.AddUint64(&h.buckets[bucketOf(v)], 1)
	atomic.AddUint64(&h.count, 1)
	atomic.AddUint64(&h.sum, v)

	for {
		cur := atomic.LoadUint64(&h.max)
		if v <= cur || atomic.CompareAndSwapUint64(&h.max, cur, v) {
			break
		}
	}
}

func (h *histogram) reset() {
	for i := range h.buckets {
		atomic.StoreUint64(&h.buckets[i], 0)
	}
	atomic.StoreUint64(&h.count, 0)
	atomic.StoreUint64(&h.sum, 0)
	atomic.StoreUint64(&h.max, 0)
}

// quantile returns the latency below which q of all samples fall.
func (h *histogram) quantile(q float64) time.Duration {
	total := atomic.LoadUint64(&h.count)
	if total == 0 {
		return 0
	}

	target := uint64(math.Ceil(q * float64(total)))
	var seen uint64
	for i := range h.buckets {
		seen += atomic.LoadUint64(&h.buckets[i])
		if seen >= target {
			return time.Duration(bucketValue(i))
		}
	}

	return time.Duration(atomic.LoadUint64(&h.max))
}

func (h *histogram) mean() time.Duration {
	total := atomic.LoadUint64(&h.count)
	if total == 0 {
		return 0
	}
	return time.Duration(atomic.LoadUint64(&h.sum) / total)
}

// memSnapshot captures the allocator counters that matter for this exercise.
type memSnapshot struct {
	mallocs    uint64
	totalAlloc uint64
	numGC      uint32
	pauseNs    uint64
}

func snapshotMem() memSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	return memSnapshot{
		mallocs:    ms.Mallocs,
		totalAlloc: ms.TotalAlloc,
		numGC:      ms.NumGC,
		pauseNs:    ms.PauseTotalNs,
	}
}

func (a memSnapshot) since(b memSnapshot) memSnapshot {
	return memSnapshot{
		mallocs:    a.mallocs - b.mallocs,
		totalAlloc: a.totalAlloc - b.totalAlloc,
		numGC:      a.numGC - b.numGC,
		pauseNs:    a.pauseNs - b.pauseNs,
	}
}

// results is everything a run reports. The JSON form is what the compare script
// consumes.
type results struct {
	Config map[string]string `json:"config"`

	DurationSec float64 `json:"duration_sec"`
	Sent        uint64  `json:"sent"`
	Received    uint64  `json:"received"`
	BytesSent   uint64  `json:"bytes_sent"`
	BytesRecv   uint64  `json:"bytes_received"`
	Unmatched   uint64  `json:"unmatched_replies"`

	SendRate   float64 `json:"send_rate_per_sec"`
	RecvRate   float64 `json:"recv_rate_per_sec"`
	Throughput float64 `json:"throughput_mb_per_sec"`

	LatencyMeanNs  int64  `json:"latency_mean_ns"`
	LatencyP50Ns   int64  `json:"latency_p50_ns"`
	LatencyP90Ns   int64  `json:"latency_p90_ns"`
	LatencyP99Ns   int64  `json:"latency_p99_ns"`
	LatencyP999Ns  int64  `json:"latency_p999_ns"`
	LatencyMaxNs   int64  `json:"latency_max_ns"`
	LatencySamples uint64 `json:"latency_samples"`

	Mallocs         uint64  `json:"mallocs"`
	TotalAllocBytes uint64  `json:"total_alloc_bytes"`
	AllocsPerMsg    float64 `json:"allocs_per_msg"`
	BytesPerMsg     float64 `json:"alloc_bytes_per_msg"`
	NumGC           uint32  `json:"num_gc"`
	GCPauseTotalNs  uint64  `json:"gc_pause_total_ns"`
}

func (r results) writeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func (r results) writeText(w io.Writer) {
	fmt.Fprintf(w, "\n=== quickfix loadgen ===\n")

	keys := make([]string, 0, len(r.Config))
	for k := range r.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-14s %s\n", k, r.Config[k])
	}

	fmt.Fprintf(w, "\nthroughput\n")
	fmt.Fprintf(w, "  duration       %.2fs\n", r.DurationSec)
	fmt.Fprintf(w, "  sent           %d (%.0f/s)\n", r.Sent, r.SendRate)
	fmt.Fprintf(w, "  received       %d (%.0f/s)\n", r.Received, r.RecvRate)
	fmt.Fprintf(w, "  wire           %.2f MB/s (%d B out, %d B in)\n", r.Throughput, r.BytesSent, r.BytesRecv)
	if r.Unmatched > 0 {
		fmt.Fprintf(w, "  unmatched      %d replies could not be timed\n", r.Unmatched)
	}

	fmt.Fprintf(w, "\nround-trip latency (%d samples)\n", r.LatencySamples)
	fmt.Fprintf(w, "  mean           %v\n", time.Duration(r.LatencyMeanNs))
	fmt.Fprintf(w, "  p50            %v\n", time.Duration(r.LatencyP50Ns))
	fmt.Fprintf(w, "  p90            %v\n", time.Duration(r.LatencyP90Ns))
	fmt.Fprintf(w, "  p99            %v\n", time.Duration(r.LatencyP99Ns))
	fmt.Fprintf(w, "  p99.9          %v\n", time.Duration(r.LatencyP999Ns))
	fmt.Fprintf(w, "  max            %v\n", time.Duration(r.LatencyMaxNs))

	fmt.Fprintf(w, "\nallocation\n")
	fmt.Fprintf(w, "  allocs/msg     %.1f\n", r.AllocsPerMsg)
	fmt.Fprintf(w, "  bytes/msg      %.0f\n", r.BytesPerMsg)
	fmt.Fprintf(w, "  total allocs   %d\n", r.Mallocs)
	fmt.Fprintf(w, "  total bytes    %.1f MB\n", float64(r.TotalAllocBytes)/(1<<20))
	fmt.Fprintf(w, "  GC cycles      %d (%v paused)\n", r.NumGC, time.Duration(r.GCPauseTotalNs))
	fmt.Fprintln(w)
}
