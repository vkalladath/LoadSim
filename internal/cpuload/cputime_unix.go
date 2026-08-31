//go:build unix

package cpuload

import "syscall"

// processCPUSeconds returns the CPU time consumed by the whole process (user +
// system). This is deliberately process-wide: it is the same quantity a
// container CPU metric reports, so closing the control loop on it makes the
// measured container usage match the configured target.
func processCPUSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	sec := func(tv syscall.Timeval) float64 {
		return float64(tv.Sec) + float64(tv.Usec)/1e6
	}
	return sec(ru.Utime) + sec(ru.Stime)
}
