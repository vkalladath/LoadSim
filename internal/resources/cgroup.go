package resources

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// cgroupRoot is where the container's own cgroup is normally mounted. Inside a
// Kubernetes pod this is the container's cgroup thanks to the cgroup namespace,
// so the files can be read directly.
const cgroupRoot = "/sys/fs/cgroup"

type cgroupInfo struct {
	version         int
	cpuLimitCores   float64
	cpuLimitSource  string
	cpuSharesCores  float64
	cpuSharesSource string
	memLimitBytes   float64
	memLimitSource  string
}

// cgroupPaths returns the candidate directories to look in: the mount root
// first, then the path from /proc/self/cgroup for hosts without a cgroup
// namespace.
func cgroupPaths() []string {
	paths := []string{cgroupRoot}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return paths
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		// v2: "0::/kubepods/burstable/pod.../container"
		// v1: "4:cpu,cpuacct:/kubepods/..."
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 || parts[2] == "" || parts[2] == "/" {
			continue
		}
		if parts[0] == "0" {
			paths = append(paths, cgroupRoot+parts[2])
			continue
		}
		for _, ctrl := range strings.Split(parts[1], ",") {
			if ctrl == "cpu" || ctrl == "memory" {
				paths = append(paths, cgroupRoot+"/"+ctrl+parts[2])
			}
		}
	}
	return paths
}

// readFirst returns the trimmed contents of the first readable candidate file
// together with the relative name it was found under.
func readFirst(names ...string) (string, string, bool) {
	for _, dir := range cgroupPaths() {
		for _, n := range names {
			b, err := os.ReadFile(dir + "/" + n)
			if err == nil {
				s := strings.TrimSpace(string(b))
				if s != "" {
					return s, n, true
				}
			}
		}
	}
	return "", "", false
}

// readCgroup reads the CPU and memory entitlements from cgroup v2, falling
// back to v1.
func readCgroup() cgroupInfo {
	var cg cgroupInfo

	// --- CPU quota ---
	if s, _, ok := readFirst("cpu.max"); ok { // v2: "<quota|max> <period>"
		cg.version = 2
		fields := strings.Fields(s)
		if len(fields) == 2 && fields[0] != "max" {
			quota, err1 := strconv.ParseFloat(fields[0], 64)
			period, err2 := strconv.ParseFloat(fields[1], 64)
			if err1 == nil && err2 == nil && period > 0 {
				cg.cpuLimitCores, cg.cpuLimitSource = quota/period, "cgroup:cpu.max"
			}
		}
	} else if q, _, ok := readFirst("cpu.cfs_quota_us"); ok { // v1
		cg.version = 1
		p, _, _ := readFirst("cpu.cfs_period_us")
		quota, err1 := strconv.ParseFloat(q, 64)
		period, err2 := strconv.ParseFloat(p, 64)
		if err1 == nil && err2 == nil && quota > 0 && period > 0 {
			cg.cpuLimitCores, cg.cpuLimitSource = quota/period, "cgroup:cpu.cfs_quota_us"
		}
	}

	// --- CPU request, inferred from the scheduler weight/shares ---
	if s, _, ok := readFirst("cpu.weight"); ok && cg.version == 2 {
		if w, err := strconv.ParseFloat(s, 64); err == nil && w > 0 {
			// Inverse of the kubelet's shares -> weight conversion.
			shares := ((w-1)*262142)/9999 + 2
			cg.cpuSharesCores = shares / 1024
			cg.cpuSharesSource = "cgroup:cpu.weight (estimate)"
		}
	} else if s, _, ok := readFirst("cpu.shares"); ok {
		if sh, err := strconv.ParseFloat(s, 64); err == nil && sh > 0 {
			cg.cpuSharesCores = sh / 1024
			cg.cpuSharesSource = "cgroup:cpu.shares (estimate)"
		}
	}
	// cpu.weight defaults to 100 (~2 shares) when no request is set; that is
	// not a meaningful request, so drop implausibly small values.
	if cg.cpuSharesCores > 0 && cg.cpuSharesCores < 0.01 {
		cg.cpuSharesCores, cg.cpuSharesSource = 0, ""
	}

	// --- Memory limit ---
	if s, name, ok := readFirst("memory.max", "memory.limit_in_bytes"); ok {
		if cg.version == 0 {
			if name == "memory.max" {
				cg.version = 2
			} else {
				cg.version = 1
			}
		}
		if s != "max" {
			if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 && v < 1<<62 {
				cg.memLimitBytes, cg.memLimitSource = v, "cgroup:"+name
			}
		}
	}
	return cg
}

// CgroupMemoryCurrent reports the cgroup's current memory charge in bytes,
// which is the number the kubelet and OOM killer look at. Returns 0 when
// unavailable.
func CgroupMemoryCurrent() float64 {
	s, _, ok := readFirst("memory.current", "memory.usage_in_bytes")
	if !ok {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// CPUThrottling reports cumulative CFS throttling for the container.
func CPUThrottling() (periods, throttledPeriods, throttledSeconds float64, ok bool) {
	s, name, found := readFirst("cpu.stat")
	if !found {
		return 0, 0, 0, false
	}
	_ = name
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "nr_periods":
			periods, ok = v, true
		case "nr_throttled":
			throttledPeriods, ok = v, true
		case "throttled_usec":
			throttledSeconds, ok = v/1e6, true
		case "throttled_time": // cgroup v1, nanoseconds
			throttledSeconds, ok = v/1e9, true
		}
	}
	return periods, throttledPeriods, throttledSeconds, ok
}

// ProcRSSBytes reports the process resident set size from /proc/self/statm.
func ProcRSSBytes() float64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		return 0
	}
	return pages * float64(os.Getpagesize())
}

// machineMemory reports MemTotal from /proc/meminfo.
func machineMemory() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		if !strings.HasPrefix(sc.Text(), "MemTotal:") {
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			return 0
		}
		kb, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
