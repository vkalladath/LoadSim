package cpuload

import (
	"context"
	"math"
	"runtime"
	"testing"
	"time"
)

func TestSuggestWorkers(t *testing.T) {
	cases := []struct {
		peak  float64
		procs int
		want  int
	}{
		{0.05, 8, 1},
		{1, 8, 2},
		{2.4, 8, 3},
		{8, 4, 4}, // cannot exceed the CPUs available
		{0, 8, 1},
	}
	for _, c := range cases {
		if got := SuggestWorkers(c.peak, c.procs); got != c.want {
			t.Errorf("SuggestWorkers(%v, %d) = %d, want %d", c.peak, c.procs, got, c.want)
		}
	}
}

func TestDutyCycleTracksTarget(t *testing.T) {
	e := New(Options{Workers: 4, Slice: 20 * time.Millisecond})
	e.SetTarget(2)
	if got := e.Stats().Duty; math.Abs(got-0.5) > 1e-9 {
		t.Errorf("duty for 2 cores over 4 workers = %v, want 0.5", got)
	}
	e.SetTarget(0)
	if got := e.Stats().Duty; got != 0 {
		t.Errorf("duty for a zero target = %v", got)
	}
	e.SetTarget(99)
	s := e.Stats()
	if s.Duty != 1 || !s.Saturated {
		t.Errorf("an unreachable target should saturate: %+v", s)
	}
}

// TestEngineHitsTarget is an end-to-end check that the generator produces
// roughly the requested number of cores.
func TestEngineHitsTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive test in -short mode")
	}
	if runtime.NumCPU() < 2 {
		t.Skip("needs at least 2 CPUs")
	}
	const target = 0.5
	e := New(Options{
		Workers:         2,
		Slice:           20 * time.Millisecond,
		ControlInterval: 200 * time.Millisecond,
		Feedback:        true,
		Stagger:         true,
	})
	e.SetTarget(target)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	start := processCPUSeconds()
	time.Sleep(2 * time.Second)
	used := processCPUSeconds() - start
	cancel()
	<-done

	got := used / 2.0
	if math.Abs(got-target) > 0.15 {
		t.Errorf("measured %.3f cores over 2s, want %.3f +/- 0.15 (duty %.2f)", got, target, e.Stats().Duty)
	}
}

func TestEngineIdlesAtZero(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive test in -short mode")
	}
	e := New(Options{Workers: 2, Slice: 20 * time.Millisecond})
	e.SetTarget(0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	start := processCPUSeconds()
	time.Sleep(500 * time.Millisecond)
	used := processCPUSeconds() - start
	cancel()
	<-done

	if used/0.5 > 0.1 {
		t.Errorf("idle engine burned %.3f cores", used/0.5)
	}
}
