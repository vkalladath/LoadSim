package config

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"loadsim/internal/profile"
)

// EnvPrefix is prepended to a flag name to derive its environment variable:
// --cpu-workers becomes LOADSIM_CPU_WORKERS.
const EnvPrefix = "LOADSIM_"

// EnvInlineYAML holds a complete config file inline, which avoids needing a
// ConfigMap volume mount.
const EnvInlineYAML = "LOADSIM_CONFIG_YAML"

// optValue is a flag whose "unset" state is distinguishable from its zero
// value, so that flags, environment variables and files can be layered.
type optValue struct {
	v        string
	boolLike bool
}

func (o *optValue) String() string     { return o.v }
func (o *optValue) Set(s string) error { o.v = s; return nil }

// IsBoolFlag lets bool-ish flags be written bare (--loop). Use --loop=false to
// turn one off.
func (o *optValue) IsBoolFlag() bool { return o.boolLike }

// Flags holds the registered command line flags.
type Flags struct {
	fs   *flag.FlagSet
	vals map[string]*optValue
	// Source records where each applied value came from, for /config and logs.
	Source map[string]string
}

type flagDef struct {
	name     string
	usage    string
	boolLike bool
}

// flagDefs is the single source of truth for flags, environment variables and
// the help text.
var flagDefs = []flagDef{
	{name: "config", usage: "path to a YAML config file"},
	{name: "preset", usage: "built-in profile to start from (see 'loadsim presets')"},
	{name: "phases", usage: "inline profile, e.g. 'burst:60s:cpu=100%:mem=80%;steady:cpu=30%'"},
	{name: "name", usage: "name of this run, reported in logs and metrics"},
	{name: "cpu", usage: "single-phase CPU target, e.g. '500m', '50%', '10%->90%@ease-in'"},
	{name: "memory", usage: "single-phase memory target, e.g. '256Mi', '75%', '64Mi->512Mi'"},
	{name: "percent-base", usage: "what a bare % means: limit, request or node"},
	{name: "loop", usage: "restart the profile when it finishes", boolLike: true},
	{name: "hold-last", usage: "hold the last phase forever when the profile ends", boolLike: true},
	{name: "jitter", usage: "default noise added to every target, e.g. '5%'"},
	{name: "seed", usage: "RNG seed for jitter and random curves (0 = random)"},
	{name: "exit-after", usage: "exit after this long, e.g. '30m' (0 = never)"},
	{name: "listen", usage: "HTTP listen address, or 'off' to disable the server"},
	{name: "metrics-path", usage: "path for Prometheus metrics"},
	{name: "pprof", usage: "expose net/http/pprof under /debug/pprof", boolLike: true},
	{name: "cpu-request", usage: "override the detected CPU request, e.g. '500m'"},
	{name: "cpu-limit", usage: "override the detected CPU limit, e.g. '2'"},
	{name: "memory-request", usage: "override the detected memory request, e.g. '256Mi'"},
	{name: "memory-limit", usage: "override the detected memory limit, e.g. '1Gi'"},
	{name: "detect-resources", usage: "detect request/limit from the downward API and cgroup", boolLike: true},
	{name: "cpu-workers", usage: "spinning goroutines (0 = derive from the peak target)"},
	{name: "cpu-slice", usage: "duty-cycle period per worker, e.g. '50ms'"},
	{name: "cpu-control-interval", usage: "feedback sampling interval, e.g. '500ms'"},
	{name: "cpu-feedback", usage: "correct the duty cycle against measured CPU usage", boolLike: true},
	{name: "cpu-gain", usage: "feedback correction strength per tick (default 0.5)"},
	{name: "cpu-work", usage: "instruction mix: int, float or sqrt"},
	{name: "cpu-stagger", usage: "spread worker duty cycles across the slice", boolLike: true},
	{name: "max-procs", usage: "override GOMAXPROCS"},
	{name: "mem-chunk-size", usage: "memory allocation granularity, e.g. '4Mi'"},
	{name: "mem-interval", usage: "how often memory is reconciled with the target"},
	{name: "mem-max-step", usage: "cap on memory allocated per reconcile tick, e.g. '64Mi'"},
	{name: "mem-touch-interval", usage: "re-touch all held pages this often (0 = never)"},
	{name: "mem-release-interval", usage: "minimum interval between returning pages to the OS"},
	{name: "mem-release-threshold", usage: "smallest shrink worth a forced release to the OS, e.g. '32Mi'"},
	{name: "mem-fill", usage: "byte pattern for allocated memory: random or zero"},
	{name: "mem-soft-limit", usage: "tell the Go GC about the memory limit to avoid accidental OOM", boolLike: true},
	{name: "mem-soft-limit-fraction", usage: "fraction of the memory limit used as the GC soft limit"},
	{name: "mem-compensate", usage: "treat memory targets as total process RSS, not just held bytes", boolLike: true},
	{name: "init-delay", usage: "idle for this long before the profile starts"},
	{name: "ready-delay", usage: "fail /readyz for this long after start"},
	{name: "log-interval", usage: "status log interval (0 = quiet)"},
	{name: "log-format", usage: "status log format: text or json"},
}

// Register defines every flag on fs.
func Register(fs *flag.FlagSet) *Flags {
	f := &Flags{fs: fs, vals: map[string]*optValue{}, Source: map[string]string{}}
	for _, d := range flagDefs {
		v := &optValue{boolLike: d.boolLike}
		f.vals[d.name] = v
		fs.Var(v, d.name, d.usage+envHint(d.name))
	}
	return f
}

func envHint(name string) string {
	return " [" + EnvName(name) + "]"
}

// EnvName returns the environment variable that backs a flag.
func EnvName(flagName string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// ApplyEnv fills in any flag that was not given on the command line from its
// environment variable.
func (f *Flags) ApplyEnv() {
	explicit := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { explicit[fl.Name] = true })
	for name, v := range f.vals {
		if explicit[name] {
			f.Source[name] = "flag"
			continue
		}
		if s, ok := os.LookupEnv(EnvName(name)); ok && strings.TrimSpace(s) != "" {
			v.v = strings.TrimSpace(s)
			f.Source[name] = "env:" + EnvName(name)
		}
	}
}

// get returns the value of a flag and whether it was set at all.
func (f *Flags) get(name string) (string, bool) {
	v, ok := f.vals[name]
	if !ok || v.v == "" {
		return "", false
	}
	return v.v, true
}

// Load builds the effective configuration from every source in precedence
// order. Sources describes what was layered, for logging.
func (f *Flags) Load() (cfg *Config, sources []string, err error) {
	cfg = Default()
	sources = []string{"defaults"}

	if name, ok := f.get("preset"); ok {
		p, err := LookupPreset(name)
		if err != nil {
			return nil, nil, err
		}
		if err := mergeYAML(cfg, []byte(p.YAML), "preset "+p.Name); err != nil {
			return nil, nil, err
		}
		sources = append(sources, "preset:"+p.Name)
	}
	if path, ok := f.get("config"); ok {
		if err := LoadFile(cfg, path); err != nil {
			return nil, nil, err
		}
		sources = append(sources, "file:"+path)
	}
	if doc := strings.TrimSpace(os.Getenv(EnvInlineYAML)); doc != "" {
		if err := mergeYAML(cfg, []byte(doc), EnvInlineYAML); err != nil {
			return nil, nil, err
		}
		sources = append(sources, "env:"+EnvInlineYAML)
	}
	if err := f.apply(cfg); err != nil {
		return nil, nil, err
	}
	if len(f.Source) > 0 {
		names := make([]string, 0, len(f.Source))
		for n := range f.Source {
			names = append(names, n)
		}
		sort.Strings(names)
		sources = append(sources, "overrides:"+strings.Join(names, ","))
	}
	return cfg, sources, nil
}

// apply layers flag and environment values onto cfg.
func (f *Flags) apply(cfg *Config) error {
	str := func(name string, dst *string) {
		if v, ok := f.get(name); ok {
			*dst = v
		}
	}
	boolPtr := func(name string, dst **bool) error {
		v, ok := f.get(name)
		if !ok {
			return nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("--%s: want true or false, got %q", name, v)
		}
		*dst = &b
		return nil
	}
	boolVal := func(name string, dst *bool) error {
		v, ok := f.get(name)
		if !ok {
			return nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("--%s: want true or false, got %q", name, v)
		}
		*dst = b
		return nil
	}
	intVal := func(name string, dst *int) error {
		v, ok := f.get(name)
		if !ok {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("--%s: want an integer, got %q", name, v)
		}
		*dst = n
		return nil
	}
	int64Val := func(name string, dst *int64) error {
		v, ok := f.get(name)
		if !ok {
			return nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("--%s: want an integer, got %q", name, v)
		}
		*dst = n
		return nil
	}
	floatVal := func(name string, dst *float64) error {
		v, ok := f.get(name)
		if !ok {
			return nil
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("--%s: want a number, got %q", name, v)
		}
		*dst = n
		return nil
	}
	durVal := func(name string, dst *profile.Duration) error {
		v, ok := f.get(name)
		if !ok {
			return nil
		}
		d, err := profile.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("--%s: %w", name, err)
		}
		*dst = profile.Duration(d)
		return nil
	}
	segVal := func(name string, dst *profile.SegmentSpec) error {
		v, ok := f.get(name)
		if !ok {
			return nil
		}
		seg, err := profile.ParseSegmentShorthand(v)
		if err != nil {
			return fmt.Errorf("--%s: %w", name, err)
		}
		*dst = seg
		return nil
	}

	// A profile given on the command line replaces any phases from the file or
	// preset, so a preset can be reused with a different shape.
	if spec, ok := f.get("phases"); ok {
		ph, err := profile.ParsePhaseDSL(spec)
		if err != nil {
			return fmt.Errorf("--phases: %w", err)
		}
		cfg.Phases = ph
		cfg.CPU, cfg.Memory = profile.SegmentSpec{}, profile.SegmentSpec{}
	}
	_, hasCPU := f.get("cpu")
	_, hasMem := f.get("memory")
	if hasCPU || hasMem {
		if _, ok := f.get("phases"); !ok {
			cfg.Phases = nil
		}
	}

	str("name", &cfg.Name)
	str("percent-base", &cfg.PercentBase)
	str("jitter", &cfg.Jitter)
	str("listen", &cfg.Listen)
	str("metrics-path", &cfg.MetricsPath)
	str("cpu-request", &cfg.Resources.CPU.Request)
	str("cpu-limit", &cfg.Resources.CPU.Limit)
	str("memory-request", &cfg.Resources.Memory.Request)
	str("memory-limit", &cfg.Resources.Memory.Limit)
	str("cpu-work", &cfg.Engine.CPU.Work)
	str("mem-chunk-size", &cfg.Engine.Memory.ChunkSize)
	str("mem-max-step", &cfg.Engine.Memory.MaxStep)
	str("mem-release-threshold", &cfg.Engine.Memory.ReleaseThreshold)
	str("mem-fill", &cfg.Engine.Memory.Fill)
	str("log-format", &cfg.Logging.Format)

	for _, e := range []error{
		segVal("cpu", &cfg.CPU),
		segVal("memory", &cfg.Memory),
		boolVal("loop", &cfg.Loop),
		boolVal("pprof", &cfg.Pprof),
		boolPtr("hold-last", &cfg.HoldLast),
		boolPtr("detect-resources", &cfg.Resources.Detect),
		boolPtr("cpu-feedback", &cfg.Engine.CPU.Feedback),
		boolPtr("cpu-stagger", &cfg.Engine.CPU.Stagger),
		boolPtr("mem-soft-limit", &cfg.Engine.Memory.SoftLimit),
		boolPtr("mem-compensate", &cfg.Engine.Memory.Compensate),
		int64Val("seed", &cfg.Seed),
		intVal("cpu-workers", &cfg.Engine.CPU.Workers),
		intVal("max-procs", &cfg.Engine.CPU.MaxProcs),
		floatVal("cpu-gain", &cfg.Engine.CPU.Gain),
		floatVal("mem-soft-limit-fraction", &cfg.Engine.Memory.SoftLimitFraction),
		durVal("exit-after", &cfg.ExitAfter),
		durVal("cpu-slice", &cfg.Engine.CPU.Slice),
		durVal("cpu-control-interval", &cfg.Engine.CPU.ControlInterval),
		durVal("mem-interval", &cfg.Engine.Memory.Interval),
		durVal("mem-touch-interval", &cfg.Engine.Memory.TouchInterval),
		durVal("mem-release-interval", &cfg.Engine.Memory.ReleaseInterval),
		durVal("init-delay", &cfg.Startup.InitDelay),
		durVal("ready-delay", &cfg.Startup.ReadyDelay),
		durVal("log-interval", &cfg.Logging.Interval),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

// FlagNames returns every flag name, sorted, for documentation.
func FlagNames() []string {
	out := make([]string, 0, len(flagDefs))
	for _, d := range flagDefs {
		out = append(out, d.name)
	}
	sort.Strings(out)
	return out
}
