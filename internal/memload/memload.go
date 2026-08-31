// Package memload holds a configurable amount of resident memory.
//
// Memory is held as a list of equally sized chunks. Every page of every chunk
// is written when it is allocated, which forces the kernel to back it with real
// pages so that RSS - and therefore the container's memory metric - actually
// moves. Chunks are filled with pseudo-random bytes so that page
// deduplication, compression (zswap/zram) or ballooning cannot quietly reclaim
// them.
package memload

import (
	"context"
	"math"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"loadsim/internal/resources"
)

// Fill selects the byte pattern written into freshly allocated chunks.
type Fill string

const (
	// FillRandom writes pseudo-random bytes: incompressible and dedup-proof.
	FillRandom Fill = "random"
	// FillZero leaves pages zeroed. Cheapest, but such pages may be
	// deduplicated or compressed by the host.
	FillZero Fill = "zero"
)

// Options configures the engine.
type Options struct {
	// ChunkSize is the allocation granularity (default 4Mi). Smaller chunks
	// track a ramp more precisely at the cost of more bookkeeping.
	ChunkSize int
	// Interval is how often the engine reconciles held memory with the target
	// (default 200ms).
	Interval time.Duration
	// MaxStepBytes caps how much memory may be allocated in one reconcile
	// tick; 0 means unlimited.
	MaxStepBytes int64
	// TouchInterval, when > 0, rewrites one byte per page of all held memory
	// on that interval, keeping the pages hot and un-reclaimable.
	TouchInterval time.Duration
	// ReleaseInterval rate-limits the forced return of freed pages to the OS,
	// which is a stop-the-world operation (default 2s).
	ReleaseInterval time.Duration
	// ReleaseThresholdBytes is the smallest shrink worth a forced release.
	// Below it the chunks are simply left to the ordinary garbage collector,
	// because a stop-the-world collection stalls the CPU workers and shows up
	// as the process missing its CPU target (default 32Mi).
	ReleaseThresholdBytes int64
	// Fill selects the byte pattern (default FillRandom).
	Fill Fill
	// SoftLimitBytes, when > 0, is passed to the Go runtime as a soft memory
	// limit so the GC becomes aggressive before the container is OOM-killed.
	SoftLimitBytes int64
	// Compensate makes the target mean "total process RSS" instead of "bytes
	// held by the engine", by subtracting the runtime's own footprint. This is
	// what makes a "50% of the limit" target show up as 50% in container
	// metrics (default true).
	Compensate bool
}

// Engine is a running memory load generator.
type Engine struct {
	opts Options

	targetBits   atomic.Uint64 // float64 bytes
	held         atomic.Int64  // bytes currently allocated
	rssBytes     atomic.Int64
	cgroupUsed   atomic.Int64
	overheadBits atomic.Uint64 // float64 bytes: RSS not held by the engine
	peakHeld     atomic.Int64  // high-water mark since the last forced release
	touches      atomic.Uint64

	mu     sync.Mutex
	chunks [][]byte
	rnd    *rand.Rand
}

// New creates an engine.
func New(opts Options) *Engine {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 4 << 20
	}
	if opts.Interval <= 0 {
		opts.Interval = 200 * time.Millisecond
	}
	if opts.ReleaseInterval <= 0 {
		opts.ReleaseInterval = 2 * time.Second
	}
	if opts.ReleaseThresholdBytes <= 0 {
		opts.ReleaseThresholdBytes = 32 << 20
	}
	if opts.Fill == "" {
		opts.Fill = FillRandom
	}
	e := &Engine{opts: opts, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
	if opts.SoftLimitBytes > 0 {
		debug.SetMemoryLimit(opts.SoftLimitBytes)
	}
	return e
}

// SetTarget sets the desired amount of resident memory in bytes.
func (e *Engine) SetTarget(bytes float64) {
	if bytes < 0 || math.IsNaN(bytes) {
		bytes = 0
	}
	e.targetBits.Store(math.Float64bits(bytes))
}

// Stats is a snapshot of the engine state.
type Stats struct {
	TargetBytes    float64 `json:"target_bytes"`
	AllocatedBytes int64   `json:"allocated_bytes"`
	RSSBytes       int64   `json:"rss_bytes"`
	CgroupBytes    int64   `json:"cgroup_bytes"`
	// OverheadBytes is the resident memory the process uses outside the
	// engine's chunks (Go runtime, stacks, the binary itself).
	OverheadBytes float64 `json:"overhead_bytes"`
	Chunks        int     `json:"chunks"`
	ChunkSize     int     `json:"chunk_size"`
	TouchPasses   uint64  `json:"touch_passes"`
	HeapBytes     uint64  `json:"heap_bytes"`
}

// Stats returns the current engine state.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	chunks := len(e.chunks)
	e.mu.Unlock()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return Stats{
		TargetBytes:    math.Float64frombits(e.targetBits.Load()),
		AllocatedBytes: e.held.Load(),
		RSSBytes:       e.rssBytes.Load(),
		CgroupBytes:    e.cgroupUsed.Load(),
		OverheadBytes:  math.Float64frombits(e.overheadBits.Load()),
		Chunks:         chunks,
		ChunkSize:      e.opts.ChunkSize,
		TouchPasses:    e.touches.Load(),
		HeapBytes:      ms.HeapAlloc,
	}
}

// Run reconciles held memory with the target until ctx is cancelled, then
// releases everything.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(e.opts.Interval)
	defer tick.Stop()

	var touchTick <-chan time.Time
	if e.opts.TouchInterval > 0 {
		t := time.NewTicker(e.opts.TouchInterval)
		defer t.Stop()
		touchTick = t.C
	}
	lastRelease := time.Now()

	for {
		select {
		case <-ctx.Done():
			e.releaseAll()
			return
		case <-touchTick:
			e.touchAll()
		case <-tick.C:
			e.sample()
			e.reconcile()
			// Only a material net shrink earns a forced release: it is a
			// stop-the-world collection, so doing it for every few megabytes of
			// jitter would cost the process its CPU target.
			if e.releaseDue() && time.Since(lastRelease) >= e.opts.ReleaseInterval {
				// Give the pages back so RSS follows the target downwards;
				// without this the Go runtime would keep them cached.
				debug.FreeOSMemory()
				lastRelease = time.Now()
				e.peakHeld.Store(e.held.Load())
			}
		}
	}
}

// sample refreshes the resident-memory readings and the overhead estimate.
func (e *Engine) sample() {
	rss := resources.ProcRSSBytes()
	e.rssBytes.Store(int64(rss))
	e.cgroupUsed.Store(int64(resources.CgroupMemoryCurrent()))
	if rss <= 0 {
		return
	}
	// Overhead is whatever is resident but not held by the engine. It is
	// smoothed because RSS moves in steps as the Go heap grows and shrinks.
	overhead := rss - float64(e.held.Load())
	if overhead < 0 {
		overhead = 0
	}
	prev := math.Float64frombits(e.overheadBits.Load())
	if prev == 0 {
		e.overheadBits.Store(math.Float64bits(overhead))
		return
	}
	const alpha = 0.2
	e.overheadBits.Store(math.Float64bits(prev*(1-alpha) + overhead*alpha))
}

// releaseDue reports whether held memory has fallen far enough below its
// high-water mark to be worth a forced return of pages to the OS. Measuring
// against the high-water mark rather than summing every shrink means a target
// that merely oscillates never triggers one, while a genuine ramp down does.
func (e *Engine) releaseDue() bool {
	held := e.held.Load()
	peak := e.peakHeld.Load()
	if held >= peak {
		e.peakHeld.Store(held)
		return false
	}
	return peak-held >= e.opts.ReleaseThresholdBytes
}

// reconcile grows or shrinks the chunk list towards the target and reports how
// many bytes it dropped.
func (e *Engine) reconcile() (released int64) {
	target := math.Float64frombits(e.targetBits.Load())
	if e.opts.Compensate {
		// The target describes total resident memory, so hold only the part the
		// runtime is not already using.
		target -= math.Float64frombits(e.overheadBits.Load())
	}
	if target < 0 {
		target = 0
	}
	want := int(math.Round(target / float64(e.opts.ChunkSize)))
	if want < 0 {
		want = 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	switch {
	case want > len(e.chunks):
		add := want - len(e.chunks)
		if e.opts.MaxStepBytes > 0 {
			maxAdd := int(e.opts.MaxStepBytes / int64(e.opts.ChunkSize))
			if maxAdd < 1 {
				maxAdd = 1
			}
			if add > maxAdd {
				add = maxAdd
			}
		}
		for i := 0; i < add; i++ {
			e.chunks = append(e.chunks, e.newChunk())
		}
	case want < len(e.chunks):
		// Drop from the end and clear the slots so the backing arrays are
		// unreachable and collectable.
		for i := want; i < len(e.chunks); i++ {
			e.chunks[i] = nil
		}
		released = int64(len(e.chunks)-want) * int64(e.opts.ChunkSize)
		e.chunks = e.chunks[:want]
	}
	e.held.Store(int64(len(e.chunks)) * int64(e.opts.ChunkSize))
	return released
}

// newChunk allocates one chunk and faults in every page.
func (e *Engine) newChunk() []byte {
	b := make([]byte, e.opts.ChunkSize)
	if e.opts.Fill == FillZero {
		// Touch one byte per page: enough to fault the page in.
		for i := 0; i < len(b); i += pageSize {
			b[i] = 1
		}
		return b
	}
	// A cheap xorshift fill is fast enough to allocate hundreds of MiB per
	// second while remaining incompressible.
	x := e.rnd.Uint64() | 1
	for i := 0; i+8 <= len(b); i += 8 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
		b[i+1] = byte(x >> 8)
		b[i+2] = byte(x >> 16)
		b[i+3] = byte(x >> 24)
		b[i+4] = byte(x >> 32)
		b[i+5] = byte(x >> 40)
		b[i+6] = byte(x >> 48)
		b[i+7] = byte(x >> 56)
	}
	return b
}

// touchAll rewrites one byte per page of every held chunk.
func (e *Engine) touchAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.chunks {
		for i := 0; i < len(c); i += pageSize {
			c[i]++
		}
	}
	e.touches.Add(1)
}

func (e *Engine) releaseAll() {
	e.mu.Lock()
	e.chunks = nil
	e.mu.Unlock()
	e.held.Store(0)
	debug.FreeOSMemory()
}

// pageSize is the host page size; writing one byte per page is what makes a
// chunk resident.
var pageSize = os.Getpagesize()
