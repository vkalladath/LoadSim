package server

import (
	"fmt"
	"runtime"
	"strings"

	"loadsim/internal/status"
)

// writeMetrics renders the Prometheus text exposition format. The exposition is
// hand written so that the image stays dependency free and tiny.
func writeMetrics(sb *strings.Builder, s status.Snapshot) {
	g := func(name, help string, value float64) {
		fmt.Fprintf(sb, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, value)
	}
	c := func(name, help string, value float64) {
		fmt.Fprintf(sb, "# HELP %s %s\n# TYPE %s counter\n%s %g\n", name, help, name, name, value)
	}
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}

	fmt.Fprintf(sb, "# HELP loadsim_info Build and profile identification.\n# TYPE loadsim_info gauge\n")
	fmt.Fprintf(sb, "loadsim_info{version=%q,profile=%q,go_version=%q} 1\n",
		escape(s.Version), escape(s.Name), escape(runtime.Version()))

	fmt.Fprintf(sb, "# HELP loadsim_phase The currently active phase (value is always 1).\n# TYPE loadsim_phase gauge\n")
	fmt.Fprintf(sb, "loadsim_phase{phase=%q} 1\n", escape(s.Phase))

	g("loadsim_phase_index", "Zero-based index of the active phase.", float64(s.Targets.PhaseIndex))
	g("loadsim_phase_count", "Number of phases in the profile.", float64(s.PhaseCount))
	g("loadsim_phase_progress_ratio", "Progress through the active phase, 0 to 1.", s.Targets.Progress)
	g("loadsim_profile_elapsed_seconds", "Seconds since the profile clock started.", s.ElapsedSec)
	g("loadsim_profile_total_seconds", "Length of one pass through the profile.", s.ProfileTotalSec)
	g("loadsim_profile_iteration", "Completed passes through the profile (looping profiles).", float64(s.Targets.Iteration))
	g("loadsim_uptime_seconds", "Seconds since process start.", s.UptimeSec)
	g("loadsim_ready", "1 when the readiness gate is open.", b(s.Ready))
	g("loadsim_initializing", "1 while simulating startup init work.", b(s.Initializing))

	g("loadsim_cpu_target_cores", "Configured CPU target in cores.", s.CPU.TargetCores)
	g("loadsim_cpu_actual_cores", "Measured process CPU usage in cores.", s.CPU.ActualCores)
	g("loadsim_cpu_duty_cycle_ratio", "Fraction of each slice that a worker spins.", s.CPU.Duty)
	g("loadsim_cpu_workers", "Number of spinning goroutines.", float64(s.CPU.Workers))
	g("loadsim_cpu_saturated", "1 when the CPU target cannot be reached.", b(s.CPU.Saturated))
	g("loadsim_cpu_request_cores", "Detected CPU request in cores (0 if unknown).", s.Resources.CPURequestCores)
	g("loadsim_cpu_limit_cores", "Detected CPU limit in cores (0 if unlimited).", s.Resources.CPULimitCores)
	g("loadsim_cpu_limit_utilization_ratio", "Measured CPU usage divided by the CPU limit.", s.CPULimitFraction())

	g("loadsim_memory_target_bytes", "Configured memory target in bytes.", s.Memory.TargetBytes)
	g("loadsim_memory_allocated_bytes", "Bytes currently held by the memory engine.", float64(s.Memory.AllocatedBytes))
	g("loadsim_memory_rss_bytes", "Process resident set size in bytes.", float64(s.Memory.RSSBytes))
	g("loadsim_memory_cgroup_bytes", "Container memory charge from the cgroup (0 if unavailable).", float64(s.Memory.CgroupBytes))
	g("loadsim_memory_overhead_bytes", "Resident memory used outside the load engine (Go runtime, binary).", s.Memory.OverheadBytes)
	g("loadsim_memory_heap_bytes", "Go heap in use.", float64(s.Memory.HeapBytes))
	g("loadsim_memory_chunks", "Number of held allocation chunks.", float64(s.Memory.Chunks))
	g("loadsim_memory_chunk_size_bytes", "Allocation granularity.", float64(s.Memory.ChunkSize))
	g("loadsim_memory_request_bytes", "Detected memory request in bytes (0 if unknown).", s.Resources.MemRequestBytes)
	g("loadsim_memory_limit_bytes", "Detected memory limit in bytes (0 if unlimited).", s.Resources.MemLimitBytes)
	g("loadsim_memory_limit_utilization_ratio", "RSS divided by the memory limit.", s.MemoryLimitFraction())
	c("loadsim_memory_touch_passes_total", "Completed passes re-touching all held pages.", float64(s.Memory.TouchPasses))

	g("loadsim_node_cpu_cores", "CPUs visible to the container.", s.Resources.NodeCPUCores)
	g("loadsim_node_memory_bytes", "Total machine memory.", s.Resources.NodeMemBytes)
	g("loadsim_goroutines", "Running goroutines.", float64(runtime.NumGoroutine()))

	if s.Throttling.Available {
		c("loadsim_cgroup_cpu_periods_total", "CFS periods elapsed for this container.", s.Throttling.Periods)
		c("loadsim_cgroup_cpu_throttled_periods_total", "CFS periods in which the container was throttled.", s.Throttling.ThrottledPeriods)
		c("loadsim_cgroup_cpu_throttled_seconds_total", "Time the container spent throttled.", s.Throttling.ThrottledSeconds)
	}
}

// escape quotes a Prometheus label value.
func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}
