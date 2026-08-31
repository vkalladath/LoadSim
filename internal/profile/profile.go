// Package profile turns a declarative list of phases into the CPU and memory
// targets that should be in force at any point in a run.
package profile

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"loadsim/internal/units"
)

// Kind distinguishes the two resources, which parse quantities differently.
type Kind string

const (
	KindCPU    Kind = "cpu"
	KindMemory Kind = "memory"
)

// Duration is a time.Duration that also accepts bare seconds in YAML/JSON.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String renders the duration Go-style ("1m30s").
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML accepts "30s", "1h30m" or a number of seconds.
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw interface{}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case string:
		parsed, err := ParseDuration(v)
		if err != nil {
			return err
		}
		*d = Duration(parsed)
	case int:
		*d = Duration(time.Duration(v) * time.Second)
	case float64:
		*d = Duration(time.Duration(v * float64(time.Second)))
	default:
		return fmt.Errorf("invalid duration %v", raw)
	}
	return nil
}

// MarshalYAML renders the duration as a string.
func (d Duration) MarshalYAML() (interface{}, error) { return d.String(), nil }

// MarshalJSON renders the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(d.String())), nil }

// ParseDuration parses a Go duration, additionally accepting days ("2d") and
// bare numbers (seconds).
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err == nil {
			return time.Duration(days * 24 * float64(time.Hour)), nil
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(f * float64(time.Second)), nil
	}
	return time.ParseDuration(s)
}

// SegmentSpec is the unparsed configuration for one resource within one phase.
//
// It accepts three YAML forms:
//
//	cpu: 50%                          # constant
//	cpu: 10% -> 90% @ease-in-out      # ramp, inline
//	cpu: {from: 10%, to: 90%, curve: ease-in-out, jitter: 5%}
type SegmentSpec struct {
	From     string  `yaml:"from,omitempty" json:"from,omitempty"`
	To       string  `yaml:"to,omitempty" json:"to,omitempty"`
	Curve    string  `yaml:"curve,omitempty" json:"curve,omitempty"`
	Exponent float64 `yaml:"exponent,omitempty" json:"exponent,omitempty"`
	// Period is the oscillation period for sine/sawtooth/square/noise curves.
	Period Duration `yaml:"period,omitempty" json:"period,omitempty"`
	// Duty is the high fraction of each period for the square curve (0-1).
	Duty float64 `yaml:"duty,omitempty" json:"duty,omitempty"`
	// Jitter adds uniform noise of +/- this fraction to every sample
	// ("5%" or 0.05).
	Jitter string `yaml:"jitter,omitempty" json:"jitter,omitempty"`
}

// IsZero reports whether nothing was configured for this resource.
func (s SegmentSpec) IsZero() bool {
	return s.From == "" && s.To == "" && s.Curve == ""
}

// UnmarshalYAML supports the scalar shorthand as well as the mapping form.
func (s *SegmentSpec) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var scalar string
	if err := unmarshal(&scalar); err == nil {
		parsed, err := ParseSegmentShorthand(scalar)
		if err != nil {
			return err
		}
		*s = parsed
		return nil
	}
	type plain SegmentSpec
	var p plain
	if err := unmarshal(&p); err != nil {
		// Rewrite the internal type name out of the message: a user seeing this
		// wrote a cpu:/memory: mapping, not a Go type.
		return fmt.Errorf("%s (valid keys: from, to, curve, exponent, period, duty, jitter)",
			strings.ReplaceAll(err.Error(), "type profile.plain", "a cpu/memory target"))
	}
	*s = SegmentSpec(p)
	return nil
}

// ParseSegmentShorthand parses the inline form used by scalars, CLI flags and
// the phase DSL: "50%", "10%->90%", "10%->90%@ease-in", "0->512Mi@linear",
// "20%->80%@sine/2m".
func ParseSegmentShorthand(s string) (SegmentSpec, error) {
	var spec SegmentSpec
	body := strings.TrimSpace(s)
	if body == "" {
		return spec, nil
	}
	// Trailing "@curve" (optionally "@curve/period", "@curve^exponent").
	if i := strings.Index(body, "@"); i >= 0 {
		tail := strings.TrimSpace(body[i+1:])
		body = strings.TrimSpace(body[:i])
		if j := strings.Index(tail, "/"); j >= 0 {
			d, err := ParseDuration(tail[j+1:])
			if err != nil {
				return spec, fmt.Errorf("invalid period in %q: %w", s, err)
			}
			spec.Period, tail = Duration(d), strings.TrimSpace(tail[:j])
		}
		if j := strings.Index(tail, "^"); j >= 0 {
			e, err := strconv.ParseFloat(strings.TrimSpace(tail[j+1:]), 64)
			if err != nil {
				return spec, fmt.Errorf("invalid exponent in %q: %w", s, err)
			}
			spec.Exponent, tail = e, strings.TrimSpace(tail[:j])
		}
		if _, err := ParseCurve(tail); err != nil {
			return spec, fmt.Errorf("in %q: %w", s, err)
		}
		spec.Curve = tail
	}
	// "from -> to" or a single constant value.
	for _, sep := range []string{"->", "=>", ".."} {
		if i := strings.Index(body, sep); i >= 0 {
			spec.From = strings.TrimSpace(body[:i])
			spec.To = strings.TrimSpace(body[i+len(sep):])
			return spec, nil
		}
	}
	spec.From, spec.To = body, body
	return spec, nil
}

// PhaseSpec is the unparsed configuration for one phase of a profile.
type PhaseSpec struct {
	Name     string      `yaml:"name,omitempty" json:"name,omitempty"`
	Duration Duration    `yaml:"duration,omitempty" json:"duration,omitempty"`
	CPU      SegmentSpec `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory   SegmentSpec `yaml:"memory,omitempty" json:"memory,omitempty"`
	// Jitter applies noise to both resources in this phase, unless the
	// resource sets its own ("5%" or 0.05).
	Jitter string `yaml:"jitter,omitempty" json:"jitter,omitempty"`
	// Repeat expands the phase into N consecutive copies, which is handy for
	// building a burst pattern out of one definition.
	Repeat int `yaml:"repeat,omitempty" json:"repeat,omitempty"`
}

// Segment is a compiled resource ramp.
type Segment struct {
	From     units.Quantity
	To       units.Quantity
	Curve    Curve
	Exponent float64
	Period   time.Duration
	Duty     float64
	Jitter   float64

	rng   *rand.Rand
	state *curveState
}

// Phase is a compiled phase.
type Phase struct {
	Name     string
	Duration time.Duration
	CPU      Segment
	Memory   Segment
}

// Profile is a compiled, runnable load profile.
type Profile struct {
	Name     string
	Phases   []Phase
	Loop     bool
	HoldLast bool

	total time.Duration
}

// Targets is the state of the profile at one instant.
type Targets struct {
	PhaseIndex    int           `json:"phase_index"`
	PhaseName     string        `json:"phase_name"`
	Iteration     int           `json:"iteration"`
	PhaseElapsed  time.Duration `json:"-"`
	PhaseDuration time.Duration `json:"-"`
	Progress      float64       `json:"phase_progress"`
	CPUCores      float64       `json:"cpu_cores"`
	MemoryBytes   float64       `json:"memory_bytes"`
	// Finished is true once a non-looping profile has run out of phases and
	// hold_last is disabled.
	Finished bool `json:"finished"`
}

// Total returns the wall-clock length of one pass through the profile.
func (p *Profile) Total() time.Duration { return p.total }

// Eval computes the targets in force at elapsed, resolving relative quantities
// against the supplied bases.
func (p *Profile) Eval(elapsed time.Duration, cpuBases, memBases units.Bases) Targets {
	if len(p.Phases) == 0 {
		return Targets{PhaseName: "idle", Finished: true}
	}
	if elapsed < 0 {
		elapsed = 0
	}
	iteration := 0
	if elapsed >= p.total && p.total > 0 {
		if p.Loop {
			iteration = int(elapsed / p.total)
			elapsed = elapsed % p.total
		} else if p.HoldLast {
			last := p.Phases[len(p.Phases)-1]
			return p.sample(len(p.Phases)-1, last, last.Duration, iteration, cpuBases, memBases)
		} else {
			return Targets{
				PhaseIndex: len(p.Phases) - 1,
				PhaseName:  "finished",
				Progress:   1,
				Finished:   true,
			}
		}
	}
	var acc time.Duration
	for i := range p.Phases {
		ph := p.Phases[i]
		if elapsed < acc+ph.Duration || i == len(p.Phases)-1 {
			return p.sample(i, ph, elapsed-acc, iteration, cpuBases, memBases)
		}
		acc += ph.Duration
	}
	return Targets{Finished: true}
}

func (p *Profile) sample(idx int, ph Phase, in time.Duration, iteration int, cpuBases, memBases units.Bases) Targets {
	if in < 0 {
		in = 0
	}
	progress := 1.0
	if ph.Duration > 0 {
		progress = float64(in) / float64(ph.Duration)
		if progress > 1 {
			progress = 1
		}
	}
	return Targets{
		PhaseIndex:    idx,
		PhaseName:     ph.Name,
		Iteration:     iteration,
		PhaseElapsed:  in,
		PhaseDuration: ph.Duration,
		Progress:      progress,
		CPUCores:      ph.CPU.value(progress, in, cpuBases),
		MemoryBytes:   ph.Memory.value(progress, in, memBases),
	}
}

// value resolves the segment at the given phase progress.
func (s *Segment) value(progress float64, elapsed time.Duration, bases units.Bases) float64 {
	if s.From.IsZero() && s.To.IsZero() {
		return 0
	}
	from, to := s.From.Resolve(bases), s.To.Resolve(bases)
	f := s.Curve.shape(shapeInput{
		T:        progress,
		Elapsed:  elapsed,
		Period:   s.Period,
		Exponent: s.Exponent,
		Duty:     s.Duty,
		Rand:     s.rng,
		State:    s.state,
	})
	v := from + (to-from)*f
	if s.Jitter > 0 && s.rng != nil {
		v *= 1 + (s.rng.Float64()*2-1)*s.Jitter
	}
	if v < 0 {
		return 0
	}
	return v
}

// PeakCPU returns the largest CPU target the profile can reach, used to size
// the worker pool up front.
func (p *Profile) PeakCPU(bases units.Bases) float64 {
	peak := 0.0
	for i := range p.Phases {
		for _, q := range []units.Quantity{p.Phases[i].CPU.From, p.Phases[i].CPU.To} {
			if v := q.Resolve(bases); v > peak {
				peak = v
			}
		}
	}
	return peak
}

// PeakMemory returns the largest memory target the profile can reach.
func (p *Profile) PeakMemory(bases units.Bases) float64 {
	peak := 0.0
	for i := range p.Phases {
		for _, q := range []units.Quantity{p.Phases[i].Memory.From, p.Phases[i].Memory.To} {
			if v := q.Resolve(bases); v > peak {
				peak = v
			}
		}
	}
	return peak
}

// buildOptions carries profile-wide defaults into segment compilation.
type buildOptions struct {
	Jitter float64
	Seed   int64
}

// Build compiles phase specs into a runnable Profile. defaultJitter is applied
// to any segment that does not set its own.
func Build(name string, specs []PhaseSpec, loop, holdLast bool, defaultJitter float64, seed int64) (*Profile, error) {
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	opts := buildOptions{Jitter: defaultJitter, Seed: seed}
	p := &Profile{Name: name, Loop: loop, HoldLast: holdLast}
	for i, spec := range specs {
		reps := spec.Repeat
		if reps < 1 {
			reps = 1
		}
		for r := 0; r < reps; r++ {
			ph, err := buildPhase(i, r, reps, spec, opts)
			if err != nil {
				return nil, err
			}
			p.Phases = append(p.Phases, ph)
			p.total += ph.Duration
		}
	}
	if len(p.Phases) == 0 {
		return nil, fmt.Errorf("profile has no phases")
	}
	return p, nil
}

func buildPhase(idx, rep, reps int, spec PhaseSpec, opts buildOptions) (Phase, error) {
	name := spec.Name
	if name == "" {
		name = fmt.Sprintf("phase-%d", idx+1)
	}
	if reps > 1 {
		name = fmt.Sprintf("%s-%d", name, rep+1)
	}
	if spec.Duration.D() < 0 {
		return Phase{}, fmt.Errorf("phase %q: duration must not be negative", name)
	}
	// A phase-level jitter is the default for both resources.
	if spec.Jitter != "" {
		if spec.CPU.Jitter == "" {
			spec.CPU.Jitter = spec.Jitter
		}
		if spec.Memory.Jitter == "" {
			spec.Memory.Jitter = spec.Jitter
		}
	}
	cpu, err := buildSegment(name, KindCPU, spec.CPU, opts)
	if err != nil {
		return Phase{}, err
	}
	mem, err := buildSegment(name, KindMemory, spec.Memory, opts)
	if err != nil {
		return Phase{}, err
	}
	return Phase{Name: name, Duration: spec.Duration.D(), CPU: cpu, Memory: mem}, nil
}

func buildSegment(phase string, kind Kind, spec SegmentSpec, opts buildOptions) (Segment, error) {
	parse := units.ParseCPU
	if kind == KindMemory {
		parse = units.ParseBytes
	}
	// A missing "from" inherits "to" (and vice versa) so that constants and
	// one-sided ramps both work.
	fromRaw, toRaw := spec.From, spec.To
	if fromRaw == "" {
		fromRaw = toRaw
	}
	if toRaw == "" {
		toRaw = fromRaw
	}
	from, err := parse(fromRaw)
	if err != nil {
		return Segment{}, fmt.Errorf("phase %q %s: %w", phase, kind, err)
	}
	to, err := parse(toRaw)
	if err != nil {
		return Segment{}, fmt.Errorf("phase %q %s: %w", phase, kind, err)
	}
	curveName := spec.Curve
	if curveName == "" {
		if fromRaw == toRaw {
			curveName = string(CurveStep)
		} else {
			curveName = string(CurveLinear)
		}
	}
	curve, err := ParseCurve(curveName)
	if err != nil {
		return Segment{}, fmt.Errorf("phase %q %s: %w", phase, kind, err)
	}
	jitter := opts.Jitter
	if spec.Jitter != "" {
		j, err := ParseFraction(spec.Jitter)
		if err != nil {
			return Segment{}, fmt.Errorf("phase %q %s jitter: %w", phase, kind, err)
		}
		jitter = j
	}
	if spec.Duty < 0 || spec.Duty > 1 {
		return Segment{}, fmt.Errorf("phase %q %s: duty must be between 0 and 1", phase, kind)
	}
	// Seed per (phase, kind) so runs are reproducible for a given seed but the
	// two resources do not move in lockstep.
	h := fnv.New64a()
	fmt.Fprintf(h, "%s/%s/%d", phase, kind, opts.Seed)
	return Segment{
		From:     from,
		To:       to,
		Curve:    curve,
		Exponent: spec.Exponent,
		Period:   spec.Period.D(),
		Duty:     spec.Duty,
		Jitter:   jitter,
		rng:      rand.New(rand.NewSource(int64(h.Sum64()))),
		state:    &curveState{},
	}, nil
}

// ParseFraction parses "10%", "0.1" or "10 %" into 0.1.
func ParseFraction(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "%") {
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid percentage %q", s)
		}
		return f / 100, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid fraction %q", s)
	}
	return f, nil
}
