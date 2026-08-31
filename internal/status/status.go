// Package status defines the snapshot of runtime state shared by the HTTP
// endpoints, the metrics exposition and the periodic status log.
package status

import (
	"time"

	"loadsim/internal/cpuload"
	"loadsim/internal/memload"
	"loadsim/internal/profile"
	"loadsim/internal/resources"
)

// Snapshot is the state of a LoadSim process at one instant.
type Snapshot struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	StartedAt time.Time     `json:"started_at"`
	Uptime    time.Duration `json:"-"`
	UptimeSec float64       `json:"uptime_seconds"`
	// Elapsed is the profile clock, which starts after startup.init_delay.
	Elapsed    time.Duration `json:"-"`
	ElapsedSec float64       `json:"profile_elapsed_seconds"`
	// ProfileTotalSec is the length of one pass through the profile.
	ProfileTotalSec float64 `json:"profile_total_seconds"`
	Looping         bool    `json:"looping"`

	Ready        bool   `json:"ready"`
	Initializing bool   `json:"initializing"`
	Phase        string `json:"phase"`
	PhaseIndex   int    `json:"phase_index"`
	PhaseCount   int    `json:"phase_count"`

	Targets profile.Targets `json:"targets"`
	CPU     cpuload.Stats   `json:"cpu"`
	Memory  memload.Stats   `json:"memory"`

	Resources  resources.Info `json:"resources"`
	Throttling Throttling     `json:"throttling"`
}

// Throttling is the container's cumulative CFS throttling.
type Throttling struct {
	Available        bool    `json:"available"`
	Periods          float64 `json:"periods"`
	ThrottledPeriods float64 `json:"throttled_periods"`
	ThrottledSeconds float64 `json:"throttled_seconds"`
}

// ReadThrottling samples the cgroup CPU pressure counters.
func ReadThrottling() Throttling {
	p, tp, ts, ok := resources.CPUThrottling()
	return Throttling{Available: ok, Periods: p, ThrottledPeriods: tp, ThrottledSeconds: ts}
}

// Utilization returns target usage as a fraction of the limit, or 0 when no
// limit is known.
func (s Snapshot) CPULimitFraction() float64 {
	if s.Resources.CPULimitCores <= 0 {
		return 0
	}
	return s.CPU.ActualCores / s.Resources.CPULimitCores
}

// MemoryLimitFraction returns held memory as a fraction of the memory limit.
func (s Snapshot) MemoryLimitFraction() float64 {
	if s.Resources.MemLimitBytes <= 0 {
		return 0
	}
	return float64(s.Memory.RSSBytes) / s.Resources.MemLimitBytes
}
