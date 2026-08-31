// Command loadsim generates a configurable CPU and memory load profile.
//
// It is meant to be run as a container in Kubernetes to exercise metrics
// pipelines, autoscalers, right-sizing recommendations and alerting rules with
// a load shape you control exactly.
//
// Usage:
//
//	loadsim [command] [flags]
//
// Commands:
//
//	run       generate load (default)
//	plan      print the profile as an ASCII chart and exit
//	config    print the effective configuration as YAML and exit
//	presets   list the built-in profiles, or show one
//	version   print the version
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"loadsim/internal/config"
	"loadsim/internal/logging"
	"loadsim/internal/plan"
	"loadsim/internal/profile"
	"loadsim/internal/runner"
	"loadsim/internal/server"
	"loadsim/internal/status"
	"loadsim/internal/units"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "loadsim: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "version":
		fmt.Printf("loadsim %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	case "presets":
		return cmdPresets(args)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	case "run", "plan", "config":
	default:
		return fmt.Errorf("unknown command %q (try 'loadsim help')", cmd)
	}

	fs := flag.NewFlagSet("loadsim "+cmd, flag.ContinueOnError)
	fs.Usage = func() { usage(os.Stderr) }
	flags := config.Register(fs)
	// plan-only presentation flags
	width := fs.Int("width", 78, "chart width in characters (plan only)")
	height := fs.Int("height", 12, "chart height in characters (plan only)")
	window := fs.String("window", "", "time span to chart, e.g. '30m' (plan only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	flags.ApplyEnv()

	cfg, sources, err := flags.Load()
	if err != nil {
		return err
	}
	compiled, err := cfg.Compile()
	if err != nil {
		return err
	}

	switch cmd {
	case "config":
		y, err := cfg.YAML()
		if err != nil {
			return err
		}
		fmt.Printf("# effective configuration (%s)\n", strings.Join(sources, " -> "))
		fmt.Printf("# resources: %s\n", compiled.Resources.String())
		for _, w := range compiled.Warnings {
			fmt.Printf("# warning: %s\n", w)
		}
		os.Stdout.Write(y)
		return nil
	case "plan":
		var win time.Duration
		if *window != "" {
			win, err = profile.ParseDuration(*window)
			if err != nil {
				return fmt.Errorf("--window: %w", err)
			}
		}
		fmt.Printf("resources: %s\n", compiled.Resources.String())
		for _, w := range compiled.Warnings {
			fmt.Printf("warning: %s\n", w)
		}
		fmt.Print(plan.Render(compiled.Profile, compiled.CPUBases, compiled.MemBases, plan.Options{
			Width:  *width,
			Height: *height,
			Window: win,
		}))
		return nil
	}
	return cmdRun(compiled, cfg, sources)
}

func cmdRun(compiled *config.Compiled, cfg *config.Config, sources []string) error {
	log := logging.New(cfg.Logging.Format, os.Stdout)
	log.Infof("loadsim %s starting (%s %s/%s, GOMAXPROCS=%d)",
		version, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0))
	log.Sources(sources, map[string]string{
		"profile":      compiled.Profile.Name,
		"phases":       fmt.Sprint(len(compiled.Profile.Phases)),
		"one_pass":     compiled.Profile.Total().String(),
		"percent_base": cfg.PercentBase,
	})
	log.Fields("resolved resources",
		logging.F("cpu_request", units.FormatCores(compiled.Resources.CPURequestCores)),
		logging.F("cpu_limit", units.FormatCores(compiled.Resources.CPULimitCores)),
		logging.F("memory_request", units.FormatBytes(compiled.Resources.MemRequestBytes)),
		logging.F("memory_limit", units.FormatBytes(compiled.Resources.MemLimitBytes)),
		logging.F("sources", formatSources(compiled.Resources.Sources)),
	)
	for _, w := range compiled.Warnings {
		log.Warnf("%s", w)
	}
	var acc time.Duration
	for i, ph := range compiled.Profile.Phases {
		log.Fields("phase",
			logging.F("index", i+1),
			logging.F("name", ph.Name),
			logging.F("starts_at", acc.String()),
			logging.F("duration", phaseDuration(ph.Duration)),
			logging.F("cpu", describeSegment(ph.CPU, compiled.CPUBases, units.FormatCores)),
			logging.F("memory", describeSegment(ph.Memory, compiled.MemBases, units.FormatBytes)),
		)
		acc += ph.Duration
	}

	r := runner.New(compiled, version, log)
	log.Fields("engines ready",
		logging.F("cpu_workers", r.Workers()),
		logging.F("cpu_peak_target", units.FormatCores(compiled.PeakCPUCores)),
		logging.F("memory_peak_target", units.FormatBytes(compiled.PeakMemoryBytes)),
		logging.F("memory_chunk", units.FormatBytes(float64(compiled.MemEngine.ChunkSize))),
	)

	prov := &provider{compiled: compiled, runner: r}
	if y, err := cfg.YAML(); err == nil {
		prov.configYAML = append([]byte(fmt.Sprintf("# effective configuration (%s)\n", strings.Join(sources, " -> "))), y...)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(server.Options{
		Addr:        cfg.Listen,
		MetricsPath: cfg.MetricsPath,
		Pprof:       cfg.Pprof,
	}, prov)
	if srv.Disabled() {
		log.Infof("http server disabled")
	} else {
		log.Infof("http server listening on %s (metrics at %s)", cfg.Listen, cfg.MetricsPath)
	}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Run(ctx) }()

	runErr := r.Run(ctx)
	stop()
	if err := <-srvErr; err != nil {
		log.Errorf("http server: %v", err)
	}
	log.Status(r.Snapshot())
	if runErr != nil && ctx.Err() != nil {
		log.Infof("shutting down on signal")
		return nil
	}
	log.Infof("loadsim stopped")
	return runErr
}

func phaseDuration(d time.Duration) string {
	if d == 0 {
		return "hold"
	}
	return d.String()
}

func describeSegment(s profile.Segment, bases units.Bases, format func(float64) string) string {
	from, to := s.From.Resolve(bases), s.To.Resolve(bases)
	if from == to || s.Curve == profile.CurveStep {
		return fmt.Sprintf("%s (%s)", format(to), s.To)
	}
	out := fmt.Sprintf("%s->%s %s", format(from), format(to), s.Curve)
	if s.Curve.Periodic() && s.Period > 0 {
		out += "/" + s.Period.String()
	}
	if s.Jitter > 0 {
		out += fmt.Sprintf(" jitter=%.0f%%", s.Jitter*100)
	}
	return out
}

func formatSources(m map[string]string) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

// provider adapts the runner and compiled config to the server's needs.
type provider struct {
	compiled   *config.Compiled
	runner     *runner.Runner
	configYAML []byte
}

func (p *provider) Snapshot() status.Snapshot { return p.runner.Snapshot() }
func (p *provider) Ready() bool               { return p.runner.Ready() }
func (p *provider) ConfigYAML() []byte        { return p.configYAML }
func (p *provider) Profile() *profile.Profile { return p.compiled.Profile }
func (p *provider) Bases() (units.Bases, units.Bases) {
	return p.compiled.CPUBases, p.compiled.MemBases
}

func cmdPresets(args []string) error {
	if len(args) > 0 && (args[0] == "show" || args[0] == "get") {
		if len(args) < 2 {
			return fmt.Errorf("usage: loadsim presets show <name>")
		}
		p, err := config.LookupPreset(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("# preset %s: %s\n%s", p.Name, p.Description, p.YAML)
		return nil
	}
	fmt.Println("Built-in presets (use --preset <name>, inspect with 'loadsim presets show <name>'):")
	for _, p := range config.Presets() {
		fmt.Printf("  %-14s %s\n", p.Name, p.Description)
	}
	return nil
}

func usage(w *os.File) {
	fmt.Fprintf(w, `loadsim %s - configurable CPU and memory load generator for Kubernetes

Usage:
  loadsim [command] [flags]

Commands:
  run       generate load (default)
  plan      print the profile as an ASCII chart and exit
  config    print the effective configuration as YAML and exit
  presets   list the built-in profiles ('presets show <name>' for one)
  version   print the version

Quick examples:
  loadsim --cpu 250m --memory 256Mi
  loadsim --cpu 60%% --memory 70%%                       # %% of the container limit
  loadsim --preset startup-burst
  loadsim --phases 'burst:90s:cpu=100%%:mem=80%%;steady:cpu=25%%:mem=60%%'
  loadsim --config /etc/loadsim/profile.yaml
  loadsim plan --preset spiky --cpu-limit 1 --memory-limit 512Mi

Configuration is layered, lowest priority first:
  defaults < --preset < --config file < %s (inline YAML) < environment < flags

Every flag has an environment variable: --cpu-workers is %sCPU_WORKERS.
Boolean flags are set bare (--loop) and cleared with =false (--loop=false).

Flags:
`, version, config.EnvInlineYAML, config.EnvPrefix)
	fs := flag.NewFlagSet("loadsim", flag.ContinueOnError)
	config.Register(fs)
	fs.SetOutput(w)
	fs.PrintDefaults()
}
