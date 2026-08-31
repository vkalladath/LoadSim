package memload

import (
	"context"
	"testing"
	"time"
)

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestGrowsAndShrinks(t *testing.T) {
	const chunk = 1 << 20
	e := New(Options{
		ChunkSize:       chunk,
		Interval:        20 * time.Millisecond,
		ReleaseInterval: 50 * time.Millisecond,
		Fill:            FillRandom,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	e.SetTarget(16 * chunk)
	waitFor(t, "16Mi to be held", func() bool { return e.Stats().AllocatedBytes == 16*chunk })
	if got := e.Stats().Chunks; got != 16 {
		t.Errorf("chunks = %d, want 16", got)
	}

	e.SetTarget(4 * chunk)
	waitFor(t, "the release down to 4Mi", func() bool { return e.Stats().AllocatedBytes == 4*chunk })

	e.SetTarget(0)
	waitFor(t, "everything to be released", func() bool { return e.Stats().AllocatedBytes == 0 })
}

func TestRoundsToNearestChunk(t *testing.T) {
	const chunk = 4 << 20
	e := New(Options{ChunkSize: chunk, Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// 10Mi with a 4Mi granularity rounds to 3 chunks (12Mi).
	e.SetTarget(10 << 20)
	waitFor(t, "rounding to 3 chunks", func() bool { return e.Stats().Chunks == 3 })
}

func TestMaxStepLimitsGrowth(t *testing.T) {
	const chunk = 1 << 20
	e := New(Options{
		ChunkSize:    chunk,
		Interval:     time.Hour, // only the manual reconcile below runs
		MaxStepBytes: 2 * chunk,
	})
	e.SetTarget(10 * chunk)
	for i := 1; i <= 3; i++ {
		e.reconcile()
		if want := int64(2 * chunk * i); e.Stats().AllocatedBytes != want {
			t.Fatalf("after %d ticks: held %d, want %d", i, e.Stats().AllocatedBytes, want)
		}
	}
}

func TestRSSFollowsAllocation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RSS measurement in -short mode")
	}
	const chunk = 4 << 20
	e := New(Options{ChunkSize: chunk, Interval: 20 * time.Millisecond, Fill: FillRandom})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	before := e.Stats().RSSBytes
	waitFor(t, "an initial RSS sample", func() bool { return e.Stats().RSSBytes > 0 })
	before = e.Stats().RSSBytes

	e.SetTarget(64 << 20)
	waitFor(t, "RSS to grow by at least 48Mi", func() bool {
		return e.Stats().RSSBytes-before > 48<<20
	})
}

func TestCompensationMakesRSSMatchTheTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RSS measurement in -short mode")
	}
	const chunk = 1 << 20
	const target = 96 << 20
	e := New(Options{
		ChunkSize:  chunk,
		Interval:   20 * time.Millisecond,
		Fill:       FillRandom,
		Compensate: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	e.SetTarget(target)
	waitFor(t, "RSS to converge on the target", func() bool {
		s := e.Stats()
		diff := s.RSSBytes - target
		if diff < 0 {
			diff = -diff
		}
		return s.AllocatedBytes > 0 && diff < 8<<20
	})
	if s := e.Stats(); s.AllocatedBytes >= target {
		t.Errorf("with compensation the engine should hold less than the target: held %d, target %d",
			s.AllocatedBytes, target)
	}
}

// An oscillating target must never trigger a forced release: it is a
// stop-the-world collection, and repeating it costs the process its CPU target
// (measured at roughly -10% before this was added).
func TestOscillationDoesNotForceRelease(t *testing.T) {
	const chunk = 1 << 20
	e := New(Options{
		ChunkSize:             chunk,
		Interval:              time.Hour, // drive reconcile by hand
		ReleaseThresholdBytes: 32 << 20,
	})
	step := func(target int) bool {
		e.SetTarget(float64(target))
		e.reconcile()
		return e.releaseDue()
	}

	if step(300 * chunk) {
		t.Fatal("growing should not ask for a release")
	}
	// Jitter of a few percent, up and down, many times over.
	for i := 0; i < 20; i++ {
		for _, target := range []int{288, 312, 295, 306, 291} {
			if step(target * chunk) {
				t.Fatalf("a +/-12Mi wobble around 300Mi asked for a release (cycle %d)", i)
			}
		}
	}
	// A genuine ramp down does ask, and clears the high-water mark afterwards.
	if !step(200 * chunk) {
		t.Error("a 100Mi drop should ask for a release")
	}
	e.peakHeld.Store(e.held.Load()) // what Run does after releasing
	if step(199 * chunk) {
		t.Error("a 1Mi drop after a release should not ask for another")
	}
}

func TestTouchPasses(t *testing.T) {
	const chunk = 1 << 20
	e := New(Options{
		ChunkSize:     chunk,
		Interval:      10 * time.Millisecond,
		TouchInterval: 20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	e.SetTarget(4 * chunk)
	waitFor(t, "a re-touch pass", func() bool { return e.Stats().TouchPasses > 0 })
}
