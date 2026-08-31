//go:build !unix

package cpuload

// processCPUSeconds is unavailable on this platform; the CPU engine then runs
// open-loop (no feedback correction).
func processCPUSeconds() float64 { return 0 }
