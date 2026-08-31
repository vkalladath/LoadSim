package config

import (
	"fmt"
	"sort"
	"strings"
)

// Preset is a built-in, ready to run profile. Presets are stored as config-file
// YAML so that "loadsim presets show <name>" doubles as documentation and as a
// starting point for a custom profile.
type Preset struct {
	Name        string
	Description string
	YAML        string
}

var presets = []Preset{
	{
		Name:        "steady",
		Description: "Constant 50% of the CPU and memory limit. The baseline sanity check.",
		YAML: `name: steady
percent_base: limit
cpu: 50%
memory: 50%
`,
	},
	{
		Name:        "startup-burst",
		Description: "Heavy 90s startup burst decaying to a modest steady state - the classic JVM/warm-cache shape that breaks request sizing.",
		YAML: `name: startup-burst
percent_base: limit
phases:
  # Cold start: saturate the CPU limit, then decay as caches warm up.
  - name: startup
    duration: 90s
    cpu: 100% -> 25% @ease-out
    memory: 20% -> 60% @ease-in-out
  # Long steady tail. No duration means "hold until the pod is deleted".
  - name: steady
    cpu: 25%
    memory: 60%
`,
	},
	{
		Name:        "ramp-up",
		Description: "Slow S-curve ramp from idle to the limit over 10 minutes, then hold. Good for autoscaler and alert threshold testing.",
		YAML: `name: ramp-up
percent_base: limit
phases:
  - name: idle
    duration: 1m
    cpu: 5%
    memory: 10%
  - name: ramp
    duration: 10m
    cpu: 5% -> 95% @ease-in-out
    memory: 10% -> 85% @linear
  - name: peak
    cpu: 95%
    memory: 85%
`,
	},
	{
		Name:        "sawtooth",
		Description: "Repeating 5-minute sawtooth between 10% and 90% of the limit. Exercises metric resolution and averaging windows.",
		YAML: `name: sawtooth
percent_base: limit
loop: true
phases:
  - name: saw
    duration: 30m
    cpu:
      from: 10%
      to: 90%
      curve: sawtooth
      period: 5m
    memory:
      from: 20%
      to: 80%
      curve: triangle
      period: 5m
`,
	},
	{
		Name:        "spiky",
		Description: "Low baseline with a short 95% spike every 3 minutes. The shape that p50-based sizing gets wrong.",
		YAML: `name: spiky
percent_base: limit
loop: true
phases:
  - name: spikes
    duration: 30m
    cpu:
      from: 15%     # baseline
      to: 95%       # spike height
      curve: square
      period: 3m
      duty: 0.1     # 18s of every 3m is a spike
      jitter: 5%
    memory: 40%
`,
	},
	{
		Name:        "memory-leak",
		Description: "Flat CPU with memory climbing past the limit over 30 minutes, ending in an OOM kill. Tests OOM detection and restart alerting.",
		YAML: `name: memory-leak
percent_base: limit
phases:
  - name: leak
    duration: 30m
    cpu: 20%
    memory: 25% -> 120% @linear
  - name: past-the-limit
    cpu: 20%
    memory: 120%
engine:
  memory:
    # Let the process actually die instead of letting the Go GC hold the line.
    soft_limit: false
    # Keep every page hot so the kernel cannot swap or reclaim its way out of
    # the OOM (matters on nodes with swap enabled).
    touch_interval: 10s
`,
	},
	{
		Name:        "daily-cycle",
		Description: "Smooth sine wave over a compressed 'day' (1 hour by default). Good for testing trend-based recommendations.",
		YAML: `name: daily-cycle
percent_base: limit
loop: true
phases:
  - name: day
    duration: 1h
    cpu:
      from: 10%
      to: 85%
      curve: sine
      period: 1h
      jitter: 4%
    memory:
      from: 35%
      to: 70%
      curve: sine
      period: 1h
`,
	},
	{
		Name:        "oversized",
		Description: "Uses far less than it requests (5% CPU, 20% memory of request). The right-sizing downscale case.",
		YAML: `name: oversized
percent_base: request
cpu: 5%
memory: 20%
`,
	},
	{
		Name:        "saturated",
		Description: "Sits at 98% of the CPU limit and 90% of the memory limit: sustained CFS throttling without an OOM kill.",
		YAML: `name: saturated
percent_base: limit
cpu: 98%
memory: 90%
`,
	},
	{
		Name:        "idle",
		Description: "Almost nothing: 10m CPU, 32Mi memory. Useful as a control pod.",
		YAML: `name: idle
cpu: 10m
memory: 32Mi
`,
	},
}

// PresetNames returns the preset names in alphabetical order.
func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for _, p := range presets {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

// Presets returns all built-in presets.
func Presets() []Preset { return presets }

// LookupPreset finds a preset by name.
func LookupPreset(name string) (Preset, error) {
	for _, p := range presets {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("unknown preset %q (available: %s)", name, strings.Join(PresetNames(), ", "))
}
