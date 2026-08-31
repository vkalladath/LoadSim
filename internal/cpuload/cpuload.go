// Package cpuload generates a configurable amount of CPU load.
//
// A pool of worker goroutines busy-spins for a fraction of each control slice
// and sleeps for the rest, so fractional core targets ("300m") are produced by
// duty cycling rather than by pinning whole cores. A control loop compares the
// process's real CPU consumption against the target and trims the duty cycle,
// which keeps the *container's* reported usage on target even though the Go
// runtime, the HTTP server and the memory engine also burn cycles.
package cpuload

import (
	"context"
	"math"
	"math/bits"
	"sync"
	"sync/atomic"
	"time"
)

// Work selects the instruction mix used to burn CPU.
type Work string

const (
	// WorkInt is an integer mixing loop: cheap, cache friendly, very stable.
	WorkInt Work = "int"
	// WorkFloat is a floating point multiply-add loop.
	WorkFloat Work = "float"
	// WorkSqrt is a floating point square-root loop, the heaviest mix.
	WorkSqrt Work = "sqrt"
)

// Options configures the engine.
type Options struct {
	// Workers is the number of spinning goroutines. 0 selects a value from the
	// profile's peak target.
	Workers int
	// Slice is the duty-cycle period of each worker (default 50ms).
	Slice time.Duration
	// ControlInterval is how often the feedback loop samples CPU usage
	// (default 500ms).
	ControlInterval time.Duration
	// Feedback enables closed-loop correction against measured process CPU.
	Feedback bool
	// Gain is the correction strength per control tick (default 0.5).
	Gain float64
	// Work selects the instruction mix (default WorkInt).
	Work Work
	// Stagger offsets worker start times so the pool does not spike in unison.
	Stagger bool
}

// Engine is a running CPU load generator.
type Engine struct {
	opts    Options
	workers int

	targetBits atomic.Uint64 // float64 cores
	dutyBits   atomic.Uint64 // float64 per-worker duty cycle
	scaleBits  atomic.Uint64 // float64 feedback correction
	actualBits atomic.Uint64 // float64 measured cores

	startOnce sync.Once
	stopped   atomic.Bool
	sink      atomic.Uint64
}

// New creates an engine. Workers must already be resolved to >= 1.
func New(opts Options) *Engine {
	if opts.Slice <= 0 {
		opts.Slice = 50 * time.Millisecond
	}
	if opts.ControlInterval <= 0 {
		opts.ControlInterval = 500 * time.Millisecond
	}
	if opts.Gain <= 0 {
		opts.Gain = 0.5
	}
	if opts.Work == "" {
		opts.Work = WorkInt
	}
	workers := opts.Workers
	if workers < 1 {
		workers = 1
	}
	e := &Engine{opts: opts, workers: workers}
	e.scaleBits.Store(math.Float64bits(1))
	return e
}

// Workers returns the size of the spin pool.
func (e *Engine) Workers() int { return e.workers }

// SetTarget sets the desired total process CPU usage in cores.
func (e *Engine) SetTarget(cores float64) {
	if cores < 0 || math.IsNaN(cores) {
		cores = 0
	}
	e.targetBits.Store(math.Float64bits(cores))
	// Apply immediately so a ramp follows the profile instead of lagging one
	// control interval behind.
	e.updateDuty()
}

// Stats is a snapshot of the engine state.
type Stats struct {
	TargetCores float64 `json:"target_cores"`
	ActualCores float64 `json:"actual_cores"`
	Duty        float64 `json:"duty_cycle"`
	Workers     int     `json:"workers"`
	// Saturated is true when the target cannot be reached because every worker
	// is already spinning flat out.
	Saturated bool `json:"saturated"`
}

// Stats returns the current engine state.
func (e *Engine) Stats() Stats {
	duty := math.Float64frombits(e.dutyBits.Load())
	target := math.Float64frombits(e.targetBits.Load())
	actual := math.Float64frombits(e.actualBits.Load())
	// Saturated means the workers are spinning flat out and the target is still
	// out of reach: either more cores were asked for than there are workers, or
	// the container is being throttled below its target.
	saturated := duty >= 0.999 && target > 0 &&
		(target > float64(e.workers)*0.99 || (actual > 0 && actual < target*0.9))
	return Stats{
		TargetCores: target,
		ActualCores: actual,
		Duty:        duty,
		Workers:     e.workers,
		Saturated:   saturated,
	}
}

// Run starts the worker pool and the control loop, and blocks until ctx is
// cancelled.
func (e *Engine) Run(ctx context.Context) {
	var wg sync.WaitGroup
	e.startOnce.Do(func() {
		for i := 0; i < e.workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				e.worker(ctx, idx)
			}(i)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.control(ctx)
		}()
	})
	wg.Wait()
	e.stopped.Store(true)
}

// control runs the feedback loop that keeps measured usage on target.
func (e *Engine) control(ctx context.Context) {
	tick := time.NewTicker(e.opts.ControlInterval)
	defer tick.Stop()
	lastCPU, lastWall := processCPUSeconds(), time.Now()
	for {
		// Recompute the duty cycle on every tick, and also immediately so a
		// new target takes effect without waiting a full interval.
		e.updateDuty()
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			cpu := processCPUSeconds()
			wall := now.Sub(lastWall).Seconds()
			if wall > 0 && cpu > 0 {
				actual := (cpu - lastCPU) / wall
				e.actualBits.Store(math.Float64bits(actual))
				if e.opts.Feedback {
					e.correct(actual)
				}
			}
			lastCPU, lastWall = cpu, now
		}
	}
}

// correct nudges the scale factor towards the ratio of target to actual usage.
func (e *Engine) correct(actual float64) {
	target := math.Float64frombits(e.targetBits.Load())
	scale := math.Float64frombits(e.scaleBits.Load())
	if target <= 0 {
		e.scaleBits.Store(math.Float64bits(1))
		return
	}
	// Ignore the sample until the load is measurable; below ~20m the rusage
	// resolution dominates.
	if actual < 0.02 && target < 0.02 {
		return
	}
	err := (target - actual) / target
	scale *= 1 + e.opts.Gain*err
	scale = clamp(scale, 0.25, 4)
	e.scaleBits.Store(math.Float64bits(scale))
}

// updateDuty converts the target (cores) into a per-worker duty cycle.
func (e *Engine) updateDuty() {
	target := math.Float64frombits(e.targetBits.Load())
	scale := math.Float64frombits(e.scaleBits.Load())
	if !e.opts.Feedback {
		scale = 1
	}
	duty := clamp(target*scale/float64(e.workers), 0, 1)
	e.dutyBits.Store(math.Float64bits(duty))
}

func (e *Engine) worker(ctx context.Context, idx int) {
	slice := e.opts.Slice
	if e.opts.Stagger && e.workers > 1 {
		// Spread worker phases across the slice so the pool's aggregate load
		// is smooth instead of arriving in synchronised bursts.
		offset := time.Duration(int64(slice) * int64(idx) / int64(e.workers))
		if !sleepCtx(ctx, offset) {
			return
		}
	}
	for {
		if ctx.Err() != nil {
			return
		}
		duty := math.Float64frombits(e.dutyBits.Load())
		switch {
		case duty <= 0.0005:
			if !sleepCtx(ctx, slice) {
				return
			}
		case duty >= 0.9995:
			e.burn(slice)
		default:
			busy := time.Duration(float64(slice) * duty)
			e.burn(busy)
			if !sleepCtx(ctx, slice-busy) {
				return
			}
		}
	}
}

// burn spins for approximately d, doing arithmetic the compiler cannot elide.
func (e *Engine) burn(d time.Duration) {
	if d <= 0 {
		return
	}
	deadline := time.Now().Add(d)
	acc := e.sink.Load() | 1
	var f float64 = 1.000001
	for {
		switch e.opts.Work {
		case WorkFloat:
			for i := 0; i < 4096; i++ {
				f = f*1.0000001 + 1.0000001
				if f > 1e30 {
					f = 1.000001
				}
			}
			acc += uint64(f)
		case WorkSqrt:
			for i := 0; i < 2048; i++ {
				f = math.Sqrt(f) + 1.7
			}
			acc += uint64(f)
		default:
			for i := 0; i < 4096; i++ {
				acc = acc*6364136223846793005 + 1442695040888963407
				acc ^= bits.RotateLeft64(acc, 27)
			}
		}
		// Publishing the accumulator keeps the loop observably live.
		e.sink.Store(acc)
		if !time.Now().Before(deadline) {
			return
		}
	}
}

// sleepCtx sleeps for d and reports whether it completed (false = cancelled).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SuggestWorkers picks a spin-pool size for a peak target of peakCores,
// bounded by the CPUs actually available.
func SuggestWorkers(peakCores float64, maxProcs int) int {
	if maxProcs < 1 {
		maxProcs = 1
	}
	// 25% headroom lets the feedback loop push the duty cycle up when other
	// parts of the process (GC, HTTP, page touching) are not using their share.
	want := int(math.Ceil(peakCores * 1.25))
	if want < 1 {
		want = 1
	}
	if want > maxProcs {
		want = maxProcs
	}
	return want
}
