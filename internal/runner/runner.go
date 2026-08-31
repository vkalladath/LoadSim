// Package runner drives a compiled profile: it evaluates the current targets on
// a fixed tick, pushes them into the CPU and memory engines, and publishes a
// snapshot for the HTTP endpoints and the status log.
package runner

import (
	"context"
	"sync/atomic"
	"time"

	"loadsim/internal/config"
	"loadsim/internal/cpuload"
	"loadsim/internal/logging"
	"loadsim/internal/memload"
	"loadsim/internal/status"
)

// TickInterval is how often targets are recomputed. 100ms is fine enough for
// any ramp a metrics pipeline can observe, and cheap.
const TickInterval = 100 * time.Millisecond

// Runner owns the load engines and the profile clock.
type Runner struct {
	c       *config.Compiled
	version string
	log     *logging.Logger

	cpu *cpuload.Engine
	mem *memload.Engine

	startedAt time.Time
	snap      atomic.Pointer[status.Snapshot]
	ready     atomic.Bool
	// Finished is set when a non-looping profile has run to completion.
	finished atomic.Bool
}

// New builds a Runner from a compiled configuration.
func New(c *config.Compiled, version string, log *logging.Logger) *Runner {
	r := &Runner{
		c:         c,
		version:   version,
		log:       log,
		cpu:       cpuload.New(c.CPUEngine),
		mem:       memload.New(c.MemEngine),
		startedAt: time.Now(),
	}
	r.publish(status.Snapshot{
		Name:      c.Profile.Name,
		Version:   version,
		StartedAt: r.startedAt,
		Phase:     "starting",
		Resources: c.Resources,
	})
	return r
}

// Snapshot returns the most recent published state.
func (r *Runner) Snapshot() status.Snapshot {
	if s := r.snap.Load(); s != nil {
		return *s
	}
	return status.Snapshot{}
}

// Ready reports whether the readiness gate has opened.
func (r *Runner) Ready() bool { return r.ready.Load() }

// Workers returns the CPU spin pool size.
func (r *Runner) Workers() int { return r.cpu.Workers() }

func (r *Runner) publish(s status.Snapshot) { r.snap.Store(&s) }

// Run blocks until ctx is cancelled, the configured exit_after elapses, or the
// profile finishes with hold_last disabled.
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	engines := make(chan struct{}, 2)
	go func() { r.cpu.Run(ctx); engines <- struct{}{} }()
	go func() { r.mem.Run(ctx); engines <- struct{}{} }()

	readyDelay := r.c.Config.Startup.ReadyDelay.D()
	initDelay := r.c.Config.Startup.InitDelay.D()
	exitAfter := r.c.Config.ExitAfter.D()

	// The profile clock starts after any simulated init work, so
	// "startup burst" phases line up with the container becoming useful.
	profileStart := r.startedAt.Add(initDelay)
	if initDelay > 0 {
		r.log.Infof("simulating %s of init work before the profile starts", initDelay)
	}

	tick := time.NewTicker(TickInterval)
	defer tick.Stop()

	var logTick <-chan time.Time
	if iv := r.c.Config.Logging.Interval.D(); iv > 0 {
		lt := time.NewTicker(iv)
		defer lt.Stop()
		logTick = lt.C
	}

	throttle := status.ReadThrottling()
	lastPhase := ""
	ticks := 0

	for {
		now := time.Now()
		uptime := now.Sub(r.startedAt)
		elapsed := now.Sub(profileStart)
		initializing := elapsed < 0

		if !r.ready.Load() && uptime >= readyDelay && !initializing {
			r.ready.Store(true)
			if readyDelay > 0 {
				r.log.Infof("readiness gate open after %s", uptime.Round(time.Millisecond))
			}
		}

		targets := r.c.Profile.Eval(max(elapsed, 0), r.c.CPUBases, r.c.MemBases)
		if initializing {
			// Hold everything at zero until init work is "done".
			targets.CPUCores, targets.MemoryBytes = 0, 0
			targets.PhaseName = "init"
		}
		r.cpu.SetTarget(targets.CPUCores)
		r.mem.SetTarget(targets.MemoryBytes)

		ticks++
		if ticks%10 == 0 {
			throttle = status.ReadThrottling()
		}

		snap := status.Snapshot{
			Name:            r.c.Profile.Name,
			Version:         r.version,
			StartedAt:       r.startedAt,
			Uptime:          uptime,
			UptimeSec:       uptime.Seconds(),
			Elapsed:         max(elapsed, 0),
			ElapsedSec:      max(elapsed, 0).Seconds(),
			ProfileTotalSec: r.c.Profile.Total().Seconds(),
			Looping:         r.c.Profile.Loop,
			Ready:           r.ready.Load(),
			Initializing:    initializing,
			Phase:           targets.PhaseName,
			PhaseIndex:      targets.PhaseIndex,
			PhaseCount:      len(r.c.Profile.Phases),
			Targets:         targets,
			CPU:             r.cpu.Stats(),
			Memory:          r.mem.Stats(),
			Resources:       r.c.Resources,
			Throttling:      throttle,
		}
		r.publish(snap)

		if snap.Phase != lastPhase {
			if lastPhase != "" {
				r.log.PhaseChange(snap)
			}
			lastPhase = snap.Phase
		}

		if targets.Finished && !r.finished.Load() {
			r.finished.Store(true)
			r.log.Infof("profile finished after %s", elapsed.Round(time.Second))
			// hold_last=false means "run the profile, then stop", which is what
			// a Job wants.
			cancel()
			<-engines
			<-engines
			return nil
		}
		if exitAfter > 0 && uptime >= exitAfter {
			r.log.Infof("exit_after %s reached, shutting down", exitAfter)
			cancel()
			<-engines
			<-engines
			return nil
		}

		select {
		case <-ctx.Done():
			<-engines
			<-engines
			return ctx.Err()
		case <-logTick:
			r.log.Status(r.Snapshot())
		case <-tick.C:
		}
	}
}
