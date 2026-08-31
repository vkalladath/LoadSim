// Package resources discovers how much CPU and memory the container is
// entitled to, so that profiles expressed as percentages of request/limit work
// without being told the numbers twice.
//
// Discovery order, highest priority first:
//
//  1. explicit configuration (resources.* in the config file, or CLI flags)
//  2. environment variables populated from the Kubernetes downward API
//  3. the container's own cgroup (v2 then v1)
//  4. the capacity of the machine, for the "node" base only
package resources

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Info is the resolved view of the container's entitlements. Zero means
// "unknown"; callers treat unknown limits as unlimited.
type Info struct {
	CPURequestCores float64 `json:"cpu_request_cores"`
	CPULimitCores   float64 `json:"cpu_limit_cores"`
	MemRequestBytes float64 `json:"memory_request_bytes"`
	MemLimitBytes   float64 `json:"memory_limit_bytes"`
	NodeCPUCores    float64 `json:"node_cpu_cores"`
	NodeMemBytes    float64 `json:"node_memory_bytes"`
	// Sources records where each number came from, and is surfaced on /status
	// so a surprising target is easy to explain.
	Sources map[string]string `json:"sources"`
	// CgroupVersion is 2, 1 or 0 when no cgroup could be read.
	CgroupVersion int `json:"cgroup_version"`
}

// Env var names understood for downward-API injection. The *_MILLI and *_BYTES
// forms take a plain integer, which is what resourceFieldRef produces with
// divisor 1m and 1 respectively.
const (
	EnvCPURequest      = "LOADSIM_CPU_REQUEST"
	EnvCPULimit        = "LOADSIM_CPU_LIMIT"
	EnvCPURequestMilli = "LOADSIM_CPU_REQUEST_MILLI"
	EnvCPULimitMilli   = "LOADSIM_CPU_LIMIT_MILLI"
	EnvMemRequest      = "LOADSIM_MEMORY_REQUEST"
	EnvMemLimit        = "LOADSIM_MEMORY_LIMIT"
	EnvMemRequestBytes = "LOADSIM_MEMORY_REQUEST_BYTES"
	EnvMemLimitBytes   = "LOADSIM_MEMORY_LIMIT_BYTES"
)

// Overrides are explicitly configured values, in cores and bytes. Zero values
// are ignored.
type Overrides struct {
	CPURequestCores float64
	CPULimitCores   float64
	MemRequestBytes float64
	MemLimitBytes   float64
}

// Detect resolves the container's entitlements. If detect is false, only the
// overrides and machine capacity are used.
func Detect(ov Overrides, detect bool) Info {
	in := Info{Sources: map[string]string{}}
	in.NodeCPUCores = float64(runtime.NumCPU())
	in.NodeMemBytes = machineMemory()

	if detect {
		if v, src, ok := envMillicores(EnvCPURequest, EnvCPURequestMilli); ok {
			in.CPURequestCores, in.Sources["cpu_request"] = v, src
		}
		if v, src, ok := envMillicores(EnvCPULimit, EnvCPULimitMilli); ok {
			in.CPULimitCores, in.Sources["cpu_limit"] = v, src
		}
		if v, src, ok := envBytes(EnvMemRequest, EnvMemRequestBytes); ok {
			in.MemRequestBytes, in.Sources["memory_request"] = v, src
		}
		if v, src, ok := envBytes(EnvMemLimit, EnvMemLimitBytes); ok {
			in.MemLimitBytes, in.Sources["memory_limit"] = v, src
		}

		cg := readCgroup()
		in.CgroupVersion = cg.version
		if in.CPULimitCores == 0 && cg.cpuLimitCores > 0 {
			in.CPULimitCores, in.Sources["cpu_limit"] = cg.cpuLimitCores, cg.cpuLimitSource
		}
		// The weight/shares derived request is only a hint; ignore it when it
		// contradicts the limit or the machine size (container runtimes often
		// leave the default weight in place when no request was set).
		if in.CPURequestCores == 0 && cg.cpuSharesCores > 0 &&
			(in.CPULimitCores == 0 || cg.cpuSharesCores <= in.CPULimitCores) &&
			cg.cpuSharesCores <= in.NodeCPUCores {
			in.CPURequestCores, in.Sources["cpu_request"] = cg.cpuSharesCores, cg.cpuSharesSource
		}
		if in.MemLimitBytes == 0 && cg.memLimitBytes > 0 {
			in.MemLimitBytes, in.Sources["memory_limit"] = cg.memLimitBytes, cg.memLimitSource
		}
	}

	if ov.CPURequestCores > 0 {
		in.CPURequestCores, in.Sources["cpu_request"] = ov.CPURequestCores, "config"
	}
	if ov.CPULimitCores > 0 {
		in.CPULimitCores, in.Sources["cpu_limit"] = ov.CPULimitCores, "config"
	}
	if ov.MemRequestBytes > 0 {
		in.MemRequestBytes, in.Sources["memory_request"] = ov.MemRequestBytes, "config"
	}
	if ov.MemLimitBytes > 0 {
		in.MemLimitBytes, in.Sources["memory_limit"] = ov.MemLimitBytes, "config"
	}
	if in.NodeCPUCores > 0 {
		in.Sources["node_cpu"] = "runtime.NumCPU"
	}
	return in
}

func envMillicores(nameSuffixed, nameMilli string) (float64, string, bool) {
	if s := strings.TrimSpace(os.Getenv(nameMilli)); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			return f / 1000, "env:" + nameMilli, true
		}
	}
	if s := strings.TrimSpace(os.Getenv(nameSuffixed)); s != "" {
		if f, ok := parseCoreString(s); ok && f > 0 {
			return f, "env:" + nameSuffixed, true
		}
	}
	return 0, "", false
}

func parseCoreString(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	if strings.HasSuffix(s, "m") {
		s, mult = strings.TrimSuffix(s, "m"), 0.001
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f * mult, true
}

func envBytes(nameSuffixed, nameBytes string) (float64, string, bool) {
	if s := strings.TrimSpace(os.Getenv(nameBytes)); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			return f, "env:" + nameBytes, true
		}
	}
	if s := strings.TrimSpace(os.Getenv(nameSuffixed)); s != "" {
		if f, ok := parseByteString(s); ok && f > 0 {
			return f, "env:" + nameSuffixed, true
		}
	}
	return 0, "", false
}

func parseByteString(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, sfx := range []struct {
		s string
		m float64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
		{"ki", 1 << 10}, {"mi", 1 << 20}, {"gi", 1 << 30}, {"ti", 1 << 40},
		{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12},
		{"k", 1e3}, {"m", 1e6}, {"g", 1e9}, {"t", 1e12}, {"b", 1},
	} {
		if strings.HasSuffix(s, sfx.s) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(s, sfx.s), 64)
			if err != nil {
				return 0, false
			}
			return f * sfx.m, true
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// String renders the info for log output.
func (in Info) String() string {
	f := func(v float64, unknown string, format func(float64) string) string {
		if v <= 0 {
			return unknown
		}
		return format(v)
	}
	cores := func(v float64) string { return fmt.Sprintf("%.3f", v) }
	mib := func(v float64) string { return fmt.Sprintf("%.0fMi", v/(1<<20)) }
	cgroup := "none"
	if in.CgroupVersion > 0 {
		cgroup = fmt.Sprintf("v%d", in.CgroupVersion)
	}
	return fmt.Sprintf("cpu request=%s limit=%s / memory request=%s limit=%s / node cpus=%.0f mem=%s cgroup=%s",
		f(in.CPURequestCores, "-", cores), f(in.CPULimitCores, "-", cores),
		f(in.MemRequestBytes, "-", mib), f(in.MemLimitBytes, "-", mib),
		in.NodeCPUCores, mib(in.NodeMemBytes), cgroup)
}
