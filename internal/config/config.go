// Package config assembles the effective LoadSim configuration from defaults,
// a built-in preset, a config file, inline YAML, environment variables and
// command line flags, then compiles it into runnable objects.
//
// Precedence, lowest to highest:
//
//	defaults < preset < config file < inline YAML (LOADSIM_CONFIG) < env < flags
package config

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"loadsim/internal/cpuload"
	"loadsim/internal/memload"
	"loadsim/internal/profile"
	"loadsim/internal/resources"
	"loadsim/internal/units"
)

// Config is the full, declarative configuration. Every field is optional.
type Config struct {
	// Name labels the run in logs, /status and the loadsim_profile_info metric.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Description is free-form documentation carried with the profile.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Listen is the address for the HTTP endpoints (default ":8080").
	// Set to "off" to disable the server entirely.
	Listen string `yaml:"listen,omitempty" json:"listen,omitempty"`
	// MetricsPath is where Prometheus metrics are exposed (default "/metrics").
	MetricsPath string `yaml:"metrics_path,omitempty" json:"metrics_path,omitempty"`
	// Pprof enables net/http/pprof under /debug/pprof.
	Pprof bool `yaml:"pprof,omitempty" json:"pprof,omitempty"`

	// Loop restarts the profile from the first phase once it ends.
	Loop bool `yaml:"loop,omitempty" json:"loop"`
	// HoldLast keeps the final phase's targets in force forever when the
	// profile ends and Loop is false (default true).
	HoldLast *bool `yaml:"hold_last,omitempty" json:"hold_last,omitempty"`
	// Jitter is the default noise applied to every target ("5%").
	Jitter string `yaml:"jitter,omitempty" json:"jitter,omitempty"`
	// Seed makes jitter and random curves reproducible. 0 = random per run.
	Seed int64 `yaml:"seed,omitempty" json:"seed,omitempty"`
	// PercentBase is what a bare "%" refers to: limit (default), request, node.
	PercentBase string `yaml:"percent_base,omitempty" json:"percent_base,omitempty"`
	// ExitAfter stops the process after this long. 0 runs forever.
	ExitAfter profile.Duration `yaml:"exit_after,omitempty" json:"exit_after,omitempty"`

	Resources ResourcesConfig `yaml:"resources,omitempty" json:"resources"`
	Engine    EngineConfig    `yaml:"engine,omitempty" json:"engine"`
	Startup   StartupConfig   `yaml:"startup,omitempty" json:"startup"`
	Logging   LoggingConfig   `yaml:"logging,omitempty" json:"logging"`

	// CPU and Memory are the single-phase shorthand, used when Phases is empty.
	CPU    profile.SegmentSpec `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory profile.SegmentSpec `yaml:"memory,omitempty" json:"memory,omitempty"`

	// Phases is the full profile. Only the last phase may omit its duration,
	// which means "hold forever".
	Phases []profile.PhaseSpec `yaml:"phases,omitempty" json:"phases,omitempty"`
}

// ResourcePair is a request/limit pair as written by the user.
type ResourcePair struct {
	Request string `yaml:"request,omitempty" json:"request,omitempty"`
	Limit   string `yaml:"limit,omitempty" json:"limit,omitempty"`
}

// ResourcesConfig tells LoadSim what the container's request and limit are,
// which is what relative targets are resolved against.
type ResourcesConfig struct {
	// Detect enables discovery from the downward API and cgroup (default true).
	Detect *bool        `yaml:"detect,omitempty" json:"detect,omitempty"`
	CPU    ResourcePair `yaml:"cpu,omitempty" json:"cpu"`
	Memory ResourcePair `yaml:"memory,omitempty" json:"memory"`
}

// EngineConfig tunes how load is produced. The defaults are good; these exist
// for reproducing specific pathologies.
type EngineConfig struct {
	CPU    CPUEngineConfig `yaml:"cpu,omitempty" json:"cpu"`
	Memory MemEngineConfig `yaml:"memory,omitempty" json:"memory"`
}

// CPUEngineConfig tunes the CPU generator.
type CPUEngineConfig struct {
	// Workers is the number of spinning goroutines. 0 sizes the pool from the
	// profile's peak CPU target.
	Workers int `yaml:"workers,omitempty" json:"workers,omitempty"`
	// Slice is the duty-cycle period of each worker (default 50ms). Values at
	// or above the 100ms CFS period produce visible throttling patterns.
	Slice profile.Duration `yaml:"slice,omitempty" json:"slice,omitempty"`
	// ControlInterval is how often measured usage is compared to the target.
	ControlInterval profile.Duration `yaml:"control_interval,omitempty" json:"control_interval,omitempty"`
	// Feedback closes the loop on measured process CPU so that total container
	// usage matches the target (default true). Disable for a fixed duty cycle.
	Feedback *bool `yaml:"feedback,omitempty" json:"feedback,omitempty"`
	// Gain is the feedback correction strength per tick (default 0.5).
	Gain float64 `yaml:"gain,omitempty" json:"gain,omitempty"`
	// Work is the instruction mix: int (default), float or sqrt.
	Work string `yaml:"work,omitempty" json:"work,omitempty"`
	// Stagger spreads worker duty cycles across the slice (default true).
	Stagger *bool `yaml:"stagger,omitempty" json:"stagger,omitempty"`
	// MaxProcs overrides GOMAXPROCS. 0 leaves the Go runtime default.
	MaxProcs int `yaml:"max_procs,omitempty" json:"max_procs,omitempty"`
}

// MemEngineConfig tunes the memory generator.
type MemEngineConfig struct {
	// ChunkSize is the allocation granularity (default 4Mi).
	ChunkSize string `yaml:"chunk_size,omitempty" json:"chunk_size,omitempty"`
	// Interval is how often held memory is reconciled with the target.
	Interval profile.Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	// MaxStep caps allocation per reconcile tick, e.g. "64Mi", to model a slow
	// allocator. Empty means unlimited.
	MaxStep string `yaml:"max_step,omitempty" json:"max_step,omitempty"`
	// TouchInterval re-touches every held page on this interval, keeping pages
	// hot so the kernel cannot reclaim them. 0 disables.
	TouchInterval profile.Duration `yaml:"touch_interval,omitempty" json:"touch_interval,omitempty"`
	// ReleaseInterval rate-limits returning freed pages to the OS (default 2s).
	ReleaseInterval profile.Duration `yaml:"release_interval,omitempty" json:"release_interval,omitempty"`
	// ReleaseThreshold is the smallest shrink worth a forced release to the OS
	// (default 32Mi). Forcing one is stop-the-world, so releasing on every few
	// megabytes of jitter costs the process its CPU target.
	ReleaseThreshold string `yaml:"release_threshold,omitempty" json:"release_threshold,omitempty"`
	// Fill is the byte pattern: random (default) or zero.
	Fill string `yaml:"fill,omitempty" json:"fill,omitempty"`
	// SoftLimit tells the Go GC about the container memory limit so the process
	// does not OOM from GC overshoot (default true). Set false to allow the
	// process to be OOM-killed on purpose.
	SoftLimit *bool `yaml:"soft_limit,omitempty" json:"soft_limit,omitempty"`
	// SoftLimitFraction is the fraction of the memory limit used as the soft
	// limit (default 0.9).
	SoftLimitFraction float64 `yaml:"soft_limit_fraction,omitempty" json:"soft_limit_fraction,omitempty"`
	// Compensate makes a memory target mean total process RSS rather than
	// bytes held by the engine, so container metrics match the target
	// (default true).
	Compensate *bool `yaml:"compensate,omitempty" json:"compensate,omitempty"`
}

// StartupConfig models slow or expensive container startup.
type StartupConfig struct {
	// InitDelay idles before the profile clock starts, simulating init work.
	InitDelay profile.Duration `yaml:"init_delay,omitempty" json:"init_delay,omitempty"`
	// ReadyDelay keeps /readyz failing for this long after start, so readiness
	// probes and rollout timing can be exercised.
	ReadyDelay profile.Duration `yaml:"ready_delay,omitempty" json:"ready_delay,omitempty"`
}

// LoggingConfig controls the periodic status line.
type LoggingConfig struct {
	// Interval between status lines (default 15s). 0 disables.
	Interval profile.Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	// Format is "text" (default) or "json".
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
}

// ptrBool returns a pointer to its own copy of b. Each optional bool needs a
// distinct pointer: YAML decoding writes *through* a non-nil pointer, so a
// shared one would let "soft_limit: false" in a config file silently clear
// every other optional bool.
func ptrBool(b bool) *bool { return &b }

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Listen:      ":8080",
		MetricsPath: "/metrics",
		HoldLast:    ptrBool(true),
		PercentBase: "limit",
		Resources:   ResourcesConfig{Detect: ptrBool(true)},
		Engine: EngineConfig{
			CPU: CPUEngineConfig{
				Slice:           profile.Duration(50 * time.Millisecond),
				ControlInterval: profile.Duration(500 * time.Millisecond),
				Feedback:        ptrBool(true),
				Gain:            0.5,
				Work:            string(cpuload.WorkInt),
				Stagger:         ptrBool(true),
			},
			Memory: MemEngineConfig{
				ChunkSize:         "4Mi",
				Interval:          profile.Duration(200 * time.Millisecond),
				ReleaseInterval:   profile.Duration(2 * time.Second),
				ReleaseThreshold:  "32Mi",
				Fill:              string(memload.FillRandom),
				SoftLimit:         ptrBool(true),
				Compensate:        ptrBool(true),
				SoftLimitFraction: 0.9,
			},
		},
		Logging: LoggingConfig{
			Interval: profile.Duration(15 * time.Second),
			Format:   "text",
		},
	}
}

// mergeYAML decodes doc on top of cfg: keys present in doc win, keys absent
// keep their current value.
func mergeYAML(cfg *Config, doc []byte, origin string) error {
	dec := yaml.NewDecoder(strings.NewReader(string(doc)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("%s: %w", origin, err)
	}
	return nil
}

// LoadFile reads a YAML (or JSON, which is valid YAML) config file.
func LoadFile(cfg *Config, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	return mergeYAML(cfg, b, path)
}

// Compiled is the configuration turned into runnable objects.
type Compiled struct {
	Config    *Config
	Profile   *profile.Profile
	Resources resources.Info
	CPUBases  units.Bases
	MemBases  units.Bases
	CPUEngine cpuload.Options
	MemEngine memload.Options
	// PeakCPUCores and PeakMemoryBytes are the highest targets the profile can
	// reach, after resolving percentages.
	PeakCPUCores    float64
	PeakMemoryBytes float64
	// Warnings are non-fatal configuration problems worth logging.
	Warnings []string
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Compile validates the configuration and builds the profile, resource bases
// and engine options.
func (c *Config) Compile() (*Compiled, error) {
	out := &Compiled{Config: c}

	base, err := units.ParsePercentBase(c.PercentBase)
	if err != nil {
		return nil, err
	}

	// --- resource discovery ---
	ov := resources.Overrides{}
	if s := c.Resources.CPU.Request; s != "" {
		q, err := units.ParseCPU(s)
		if err != nil {
			return nil, fmt.Errorf("resources.cpu.request: %w", err)
		}
		ov.CPURequestCores = q.Amount
	}
	if s := c.Resources.CPU.Limit; s != "" {
		q, err := units.ParseCPU(s)
		if err != nil {
			return nil, fmt.Errorf("resources.cpu.limit: %w", err)
		}
		ov.CPULimitCores = q.Amount
	}
	if s := c.Resources.Memory.Request; s != "" {
		q, err := units.ParseBytes(s)
		if err != nil {
			return nil, fmt.Errorf("resources.memory.request: %w", err)
		}
		ov.MemRequestBytes = q.Amount
	}
	if s := c.Resources.Memory.Limit; s != "" {
		q, err := units.ParseBytes(s)
		if err != nil {
			return nil, fmt.Errorf("resources.memory.limit: %w", err)
		}
		ov.MemLimitBytes = q.Amount
	}
	out.Resources = resources.Detect(ov, boolOr(c.Resources.Detect, true))
	out.CPUBases = units.Bases{
		Limit:   out.Resources.CPULimitCores,
		Request: out.Resources.CPURequestCores,
		Node:    out.Resources.NodeCPUCores,
		Default: base,
	}
	out.MemBases = units.Bases{
		Limit:   out.Resources.MemLimitBytes,
		Request: out.Resources.MemRequestBytes,
		Node:    out.Resources.NodeMemBytes,
		Default: base,
	}

	// --- profile ---
	phases := c.Phases
	if len(phases) == 0 {
		if c.CPU.IsZero() && c.Memory.IsZero() {
			return nil, fmt.Errorf("nothing to do: set phases, or cpu/memory, or --preset (available presets: %s)",
				strings.Join(PresetNames(), ", "))
		}
		// Shorthand: one open-ended phase.
		phases = []profile.PhaseSpec{{Name: "main", CPU: c.CPU, Memory: c.Memory}}
	} else if !c.CPU.IsZero() || !c.Memory.IsZero() {
		out.Warnings = append(out.Warnings,
			"top-level cpu/memory ignored because phases are set")
	}
	for i, p := range phases {
		if p.Duration.D() <= 0 && i != len(phases)-1 {
			return nil, fmt.Errorf("phase %d (%q): only the last phase may omit its duration", i+1, p.Name)
		}
	}
	jitter, err := profile.ParseFraction(c.Jitter)
	if err != nil {
		return nil, fmt.Errorf("jitter: %w", err)
	}
	name := c.Name
	if name == "" {
		name = "loadsim"
	}
	prof, err := profile.Build(name, phases, c.Loop, boolOr(c.HoldLast, true), jitter, c.Seed)
	if err != nil {
		return nil, err
	}
	if c.Loop && prof.Phases[len(prof.Phases)-1].Duration <= 0 {
		if len(prof.Phases) == 1 {
			// A single open-ended phase repeats to itself; looping is a no-op.
			prof.Loop = false
			out.Warnings = append(out.Warnings, "loop ignored: the profile is a single phase with no duration")
		} else {
			return nil, fmt.Errorf("loop requires a duration on every phase, including the last one")
		}
	}
	out.Profile = prof
	out.PeakCPUCores = prof.PeakCPU(out.CPUBases)
	out.PeakMemoryBytes = prof.PeakMemory(out.MemBases)

	// --- engines ---
	if c.Engine.CPU.MaxProcs > 0 {
		runtime.GOMAXPROCS(c.Engine.CPU.MaxProcs)
	}
	work := cpuload.Work(strings.ToLower(strings.TrimSpace(c.Engine.CPU.Work)))
	switch work {
	case "", cpuload.WorkInt:
		work = cpuload.WorkInt
	case cpuload.WorkFloat, cpuload.WorkSqrt:
	default:
		return nil, fmt.Errorf("engine.cpu.work: unknown value %q (want int, float or sqrt)", c.Engine.CPU.Work)
	}
	workers := c.Engine.CPU.Workers
	if workers <= 0 {
		workers = cpuload.SuggestWorkers(out.PeakCPUCores, runtime.GOMAXPROCS(0))
	}
	out.CPUEngine = cpuload.Options{
		Workers:         workers,
		Slice:           c.Engine.CPU.Slice.D(),
		ControlInterval: c.Engine.CPU.ControlInterval.D(),
		Feedback:        boolOr(c.Engine.CPU.Feedback, true),
		Gain:            c.Engine.CPU.Gain,
		Work:            work,
		Stagger:         boolOr(c.Engine.CPU.Stagger, true),
	}

	chunk := 4 << 20
	if s := c.Engine.Memory.ChunkSize; s != "" {
		q, err := units.ParseBytes(s)
		if err != nil {
			return nil, fmt.Errorf("engine.memory.chunk_size: %w", err)
		}
		if q.Base != units.BaseAbsolute || q.Amount < 4096 {
			return nil, fmt.Errorf("engine.memory.chunk_size must be an absolute size of at least 4Ki")
		}
		chunk = int(q.Amount)
	}
	var maxStep int64
	if s := c.Engine.Memory.MaxStep; s != "" {
		q, err := units.ParseBytes(s)
		if err != nil {
			return nil, fmt.Errorf("engine.memory.max_step: %w", err)
		}
		maxStep = int64(q.Resolve(out.MemBases))
	}
	fill := memload.Fill(strings.ToLower(strings.TrimSpace(c.Engine.Memory.Fill)))
	switch fill {
	case "", memload.FillRandom:
		fill = memload.FillRandom
	case memload.FillZero:
	default:
		return nil, fmt.Errorf("engine.memory.fill: unknown value %q (want random or zero)", c.Engine.Memory.Fill)
	}
	var releaseThreshold int64
	if s := c.Engine.Memory.ReleaseThreshold; s != "" {
		q, err := units.ParseBytes(s)
		if err != nil {
			return nil, fmt.Errorf("engine.memory.release_threshold: %w", err)
		}
		releaseThreshold = int64(q.Resolve(out.MemBases))
	}
	var soft int64
	if boolOr(c.Engine.Memory.SoftLimit, true) && out.Resources.MemLimitBytes > 0 {
		frac := c.Engine.Memory.SoftLimitFraction
		if frac <= 0 {
			frac = 0.9
		}
		soft = int64(out.Resources.MemLimitBytes * frac)
	}
	out.MemEngine = memload.Options{
		ChunkSize:             chunk,
		Interval:              c.Engine.Memory.Interval.D(),
		MaxStepBytes:          maxStep,
		TouchInterval:         c.Engine.Memory.TouchInterval.D(),
		ReleaseInterval:       c.Engine.Memory.ReleaseInterval.D(),
		ReleaseThresholdBytes: releaseThreshold,
		Fill:                  fill,
		SoftLimitBytes:        soft,
		Compensate:            boolOr(c.Engine.Memory.Compensate, true),
	}

	switch strings.ToLower(c.Logging.Format) {
	case "", "text", "json":
	default:
		return nil, fmt.Errorf("logging.format: want text or json, got %q", c.Logging.Format)
	}

	// --- sanity warnings ---
	if out.PeakCPUCores > out.Resources.NodeCPUCores {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"peak cpu target %.2f cores exceeds the %.0f cpus visible to this container; the target cannot be reached",
			out.PeakCPUCores, out.Resources.NodeCPUCores))
	}
	if out.Resources.CPULimitCores > 0 && out.PeakCPUCores > out.Resources.CPULimitCores*1.001 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"peak cpu target %.2f cores exceeds the cpu limit %.2f; expect CFS throttling",
			out.PeakCPUCores, out.Resources.CPULimitCores))
	}
	if out.Resources.MemLimitBytes > 0 && out.PeakMemoryBytes > out.Resources.MemLimitBytes*0.95 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"peak memory target %s is within 5%% of (or above) the memory limit %s; expect an OOM kill",
			units.FormatBytes(out.PeakMemoryBytes), units.FormatBytes(out.Resources.MemLimitBytes)))
	}
	if out.PeakMemoryBytes == 0 && out.PeakCPUCores == 0 {
		out.Warnings = append(out.Warnings, "both cpu and memory targets resolve to zero; is percent_base pointing at an unset request/limit?")
	}
	return out, nil
}

// YAML renders the effective configuration.
func (c *Config) YAML() ([]byte, error) { return yaml.Marshal(c) }
