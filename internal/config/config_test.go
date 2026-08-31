package config

import (
	"flag"
	"io"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"loadsim/internal/plan"
	"loadsim/internal/units"
)

// load builds a configuration the way the command line does.
func load(t *testing.T, args ...string) (*Config, *Compiled) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := Register(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	f.ApplyEnv()
	cfg, _, err := f.Load()
	if err != nil {
		t.Fatalf("load %v: %v", args, err)
	}
	c, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile %v: %v", args, err)
	}
	return cfg, c
}

func loadErr(t *testing.T, args ...string) error {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := Register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	f.ApplyEnv()
	cfg, _, err := f.Load()
	if err != nil {
		return err
	}
	_, err = cfg.Compile()
	return err
}

const noDetect = "--detect-resources=false"

func TestShorthandFlags(t *testing.T) {
	_, c := load(t, noDetect, "--cpu", "250m", "--memory", "128Mi")
	if len(c.Profile.Phases) != 1 {
		t.Fatalf("want a single synthesised phase, got %d", len(c.Profile.Phases))
	}
	got := c.Profile.Eval(time.Minute, c.CPUBases, c.MemBases)
	if got.CPUCores != 0.25 || got.MemoryBytes != 128<<20 {
		t.Errorf("targets: %+v", got)
	}
}

func TestRelativeToExplicitLimits(t *testing.T) {
	_, c := load(t, noDetect, "--cpu", "50%", "--memory", "75%",
		"--cpu-limit", "2", "--memory-limit", "512Mi")
	got := c.Profile.Eval(0, c.CPUBases, c.MemBases)
	if got.CPUCores != 1 {
		t.Errorf("cpu = %v, want 1", got.CPUCores)
	}
	if got.MemoryBytes != 384<<20 {
		t.Errorf("memory = %v, want 384Mi", got.MemoryBytes)
	}
}

func TestPercentBaseRequest(t *testing.T) {
	_, c := load(t, noDetect, "--cpu", "80%", "--percent-base", "request",
		"--cpu-request", "500m", "--cpu-limit", "2")
	if got := c.Profile.Eval(0, c.CPUBases, c.MemBases).CPUCores; got != 0.4 {
		t.Errorf("cpu = %v, want 0.4 (80%% of the request)", got)
	}
}

func TestPhasesFlagOverridesPreset(t *testing.T) {
	_, c := load(t, noDetect, "--preset", "steady", "--cpu-limit", "1", "--memory-limit", "1Gi",
		"--phases", "burst:30s:cpu=100%:mem=50%;calm:cpu=10%:mem=20%")
	if len(c.Profile.Phases) != 2 {
		t.Fatalf("phases: %d", len(c.Profile.Phases))
	}
	if got := c.Profile.Eval(0, c.CPUBases, c.MemBases).CPUCores; got != 1 {
		t.Errorf("burst cpu = %v, want 1", got)
	}
	if got := c.Profile.Eval(time.Minute, c.CPUBases, c.MemBases).CPUCores; got != 0.1 {
		t.Errorf("calm cpu = %v, want 0.1", got)
	}
}

func TestCPUFlagReplacesPresetPhases(t *testing.T) {
	// A preset with phases plus an explicit --cpu must not silently keep the
	// preset's phases.
	_, c := load(t, noDetect, "--preset", "startup-burst", "--cpu-limit", "1",
		"--memory-limit", "1Gi", "--cpu", "300m", "--memory", "64Mi")
	if len(c.Profile.Phases) != 1 {
		t.Fatalf("phases: %d, want 1", len(c.Profile.Phases))
	}
	if got := c.Profile.Eval(0, c.CPUBases, c.MemBases).CPUCores; got != 0.3 {
		t.Errorf("cpu = %v, want 0.3", got)
	}
}

func TestEnvBacksEveryFlag(t *testing.T) {
	t.Setenv("LOADSIM_CPU", "700m")
	t.Setenv("LOADSIM_MEMORY", "200Mi")
	t.Setenv("LOADSIM_LOOP", "true")
	t.Setenv("LOADSIM_DETECT_RESOURCES", "false")
	cfg, c := load(t)
	if !cfg.Loop {
		t.Error("LOADSIM_LOOP not applied")
	}
	got := c.Profile.Eval(0, c.CPUBases, c.MemBases)
	if math.Abs(got.CPUCores-0.7) > 1e-9 || got.MemoryBytes != 200<<20 {
		t.Errorf("targets from env: %+v", got)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	t.Setenv("LOADSIM_CPU", "700m")
	t.Setenv("LOADSIM_DETECT_RESOURCES", "false")
	_, c := load(t, "--cpu", "100m")
	if got := c.Profile.Eval(0, c.CPUBases, c.MemBases).CPUCores; got != 0.1 {
		t.Errorf("cpu = %v, want the flag value 0.1", got)
	}
}

func TestInlineYAMLEnv(t *testing.T) {
	t.Setenv(EnvInlineYAML, "name: inline\npercent_base: limit\ncpu: 1\nmemory: 64Mi\n")
	cfg, c := load(t, noDetect)
	if cfg.Name != "inline" {
		t.Errorf("name = %q", cfg.Name)
	}
	if got := c.Profile.Eval(0, c.CPUBases, c.MemBases).CPUCores; got != 1 {
		t.Errorf("cpu = %v", got)
	}
}

func TestConfigFileLayering(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/profile.yaml"
	doc := `
name: from-file
percent_base: limit
resources:
  cpu:
    limit: "2"
  memory:
    limit: 1Gi
phases:
  - name: warmup
    duration: 1m
    cpu: 10% -> 90% @ease-in-out
    memory: 10% -> 50% @linear
  - name: steady
    cpu: 40%
    memory: 50%
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, c := load(t, noDetect, "--config", path)
	if cfg.Name != "from-file" || len(c.Profile.Phases) != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if got := c.Profile.Eval(30*time.Second, c.CPUBases, c.MemBases).CPUCores; got <= 0.2 || got >= 1.8 {
		t.Errorf("midpoint cpu = %v", got)
	}
	if got := c.Profile.Eval(2*time.Minute, c.CPUBases, c.MemBases); got.PhaseName != "steady" || got.CPUCores != 0.8 {
		t.Errorf("steady: %+v", got)
	}
	// A flag still wins over the file.
	_, c2 := load(t, noDetect, "--config", path, "--cpu-limit", "4")
	if got := c2.Profile.Eval(2*time.Minute, c2.CPUBases, c2.MemBases).CPUCores; got != 1.6 {
		t.Errorf("with overridden limit: %v, want 1.6", got)
	}
}

func TestErrors(t *testing.T) {
	cases := map[string][]string{
		"nothing configured":            {noDetect},
		"unknown preset":                {noDetect, "--preset", "nope"},
		"unknown curve":                 {noDetect, "--cpu", "1->2@banana"},
		"bad quantity":                  {noDetect, "--memory", "12 bananas"},
		"non-final phase without time":  {noDetect, "--phases", "a:cpu=1;b:cpu=2"},
		"loop without a final duration": {noDetect, "--loop", "--phases", "a:30s:cpu=1;b:cpu=2"},
		"bad work mix":                  {noDetect, "--cpu", "1", "--cpu-work", "cosmic"},
		"bad fill":                      {noDetect, "--cpu", "1", "--mem-fill", "sparkles"},
		"bad bool":                      {noDetect, "--cpu", "1", "--hold-last=maybe"},
	}
	for name, args := range cases {
		if err := loadErr(t, args...); err == nil {
			t.Errorf("%s: expected an error for %v", name, args)
		}
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bad.yaml"
	if err := os.WriteFile(path, []byte("cpu: 1\ntypo_here: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadErr(t, noDetect, "--config", path); err == nil {
		t.Error("a misspelled config key should be rejected")
	}
}

func TestAllPresetsCompileAndRender(t *testing.T) {
	for _, p := range Presets() {
		p := p
		t.Run(p.Name, func(t *testing.T) {
			_, c := load(t, noDetect, "--preset", p.Name,
				"--cpu-request", "500m", "--cpu-limit", "1",
				"--memory-request", "256Mi", "--memory-limit", "512Mi")
			if c.PeakCPUCores <= 0 && c.PeakMemoryBytes <= 0 {
				t.Fatalf("preset %s resolves to no load at all", p.Name)
			}
			out := plan.Render(c.Profile, c.CPUBases, c.MemBases, plan.Options{Width: 40, Height: 6})
			if !strings.Contains(out, "phases:") {
				t.Errorf("plan output looks wrong:\n%s", out)
			}
		})
	}
}

func TestLoopIgnoredForSinglePhase(t *testing.T) {
	_, c := load(t, noDetect, "--loop", "--cpu", "1")
	if c.Profile.Loop {
		t.Error("a single open-ended phase cannot loop")
	}
	if len(c.Warnings) == 0 {
		t.Error("expected a warning that loop was ignored")
	}
}

// A false in a config file must not leak into the other optional booleans:
// they are decoded through pointers, so they must not share one.
func TestOptionalBoolsAreIndependent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/one-false.yaml"
	doc := "cpu: 50%\nengine:\n  memory:\n    soft_limit: false\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, c := load(t, "--config", path, "--cpu-limit", "2", "--memory-limit", "1Gi")
	if *cfg.Engine.Memory.SoftLimit {
		t.Error("soft_limit should be false")
	}
	for name, got := range map[string]*bool{
		"hold_last":                cfg.HoldLast,
		"resources.detect":         cfg.Resources.Detect,
		"engine.cpu.feedback":      cfg.Engine.CPU.Feedback,
		"engine.cpu.stagger":       cfg.Engine.CPU.Stagger,
		"engine.memory.compensate": cfg.Engine.Memory.Compensate,
	} {
		if got == nil || !*got {
			t.Errorf("%s should still be true, got %v", name, got)
		}
	}
	if !c.Profile.HoldLast || !c.MemEngine.Compensate || !c.CPUEngine.Feedback {
		t.Errorf("compiled options lost their defaults: %+v", c.CPUEngine)
	}
}

func TestPresetKeepsDetectionOn(t *testing.T) {
	// The memory-leak preset sets engine.memory.soft_limit: false.
	t.Setenv("LOADSIM_CPU_LIMIT_MILLI", "1000")
	_, c := load(t, "--preset", "memory-leak")
	if c.Resources.CPULimitCores != 1 {
		t.Errorf("detection should still run: cpu limit = %v", c.Resources.CPULimitCores)
	}
}

// A CPU request guessed from cpu.weight must not survive as a value above an
// explicitly configured limit; percentages of "request" would then be wrong.
func TestImplausibleRequestEstimateDropped(t *testing.T) {
	_, c := load(t, "--cpu", "50%", "--cpu-limit", "1", "--memory-limit", "512Mi")
	if r := c.Resources.CPURequestCores; r > c.Resources.CPULimitCores {
		t.Errorf("cpu request %v exceeds the configured limit %v", r, c.Resources.CPULimitCores)
	}
	if src, ok := c.Resources.Sources["cpu_request"]; ok && strings.Contains(src, "estimate") {
		t.Errorf("an implausible estimated request was kept (source %q)", src)
	}
}

func TestWarnings(t *testing.T) {
	_, c := load(t, noDetect, "--cpu", "150%", "--memory", "150%",
		"--cpu-limit", "1", "--memory-limit", "256Mi")
	if len(c.Warnings) < 2 {
		t.Errorf("expected throttling and OOM warnings, got %v", c.Warnings)
	}
}

func TestEngineDefaults(t *testing.T) {
	_, c := load(t, noDetect, "--cpu", "1500m", "--memory", "64Mi", "--max-procs", "8")
	if c.CPUEngine.Workers < 2 {
		t.Errorf("workers = %d, want at least 2 for a 1.5 core target", c.CPUEngine.Workers)
	}
	if c.MemEngine.ChunkSize != 4<<20 {
		t.Errorf("chunk size = %d", c.MemEngine.ChunkSize)
	}
	if !c.CPUEngine.Feedback {
		t.Error("feedback should default on")
	}
	if c.MemEngine.SoftLimitBytes != 0 {
		t.Error("no memory limit is known, so no soft limit should be set")
	}
}

func TestSoftLimitFromMemoryLimit(t *testing.T) {
	_, c := load(t, noDetect, "--memory", "50%", "--memory-limit", "1Gi")
	limit := float64(int64(1) << 30)
	want := int64(limit * 0.9)
	if c.MemEngine.SoftLimitBytes != want {
		t.Errorf("soft limit = %d, want %d", c.MemEngine.SoftLimitBytes, want)
	}
	_, c = load(t, noDetect, "--memory", "50%", "--memory-limit", "1Gi", "--mem-soft-limit=false")
	if c.MemEngine.SoftLimitBytes != 0 {
		t.Errorf("soft limit should be disabled, got %d", c.MemEngine.SoftLimitBytes)
	}
}

func TestEffectiveConfigRoundTrips(t *testing.T) {
	cfg, _ := load(t, noDetect, "--preset", "spiky", "--cpu-limit", "1", "--memory-limit", "512Mi")
	y, err := cfg.YAML()
	if err != nil {
		t.Fatal(err)
	}
	round := Default()
	if err := mergeYAML(round, y, "roundtrip"); err != nil {
		t.Fatalf("the rendered config must be loadable again: %v", err)
	}
	if _, err := round.Compile(); err != nil {
		t.Fatalf("compile round-tripped config: %v", err)
	}
}

func TestDownwardAPIEnv(t *testing.T) {
	t.Setenv("LOADSIM_CPU_LIMIT_MILLI", "1500")
	t.Setenv("LOADSIM_MEMORY_LIMIT_BYTES", "536870912")
	_, c := load(t, "--cpu", "50%", "--memory", "50%")
	if c.Resources.CPULimitCores != 1.5 {
		t.Errorf("cpu limit = %v, want 1.5", c.Resources.CPULimitCores)
	}
	if c.Resources.MemLimitBytes != 512<<20 {
		t.Errorf("memory limit = %v", c.Resources.MemLimitBytes)
	}
	if got := c.Profile.Eval(0, c.CPUBases, c.MemBases); got.CPUCores != 0.75 || got.MemoryBytes != 256<<20 {
		t.Errorf("targets: %+v", got)
	}
}

func TestBasesDefaultChoice(t *testing.T) {
	_, c := load(t, noDetect, "--cpu", "1", "--percent-base", "node")
	if c.CPUBases.Default != units.BaseNode {
		t.Errorf("default base = %q", c.CPUBases.Default)
	}
}
