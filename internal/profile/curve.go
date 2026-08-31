package profile

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// Curve is the shape a phase uses to move from its "from" value to its "to"
// value. Monotonic curves consume the phase progress t in [0,1]; periodic
// curves ignore t and oscillate with the segment's Period instead.
type Curve string

// Supported curves. Aliases are resolved by ParseCurve.
const (
	CurveStep       Curve = "step"        // jump to "to" immediately and hold
	CurveLinear     Curve = "linear"      // straight ramp
	CurveEaseIn     Curve = "ease-in"     // slow start, fast finish (t^k)
	CurveEaseOut    Curve = "ease-out"    // fast start, slow finish
	CurveEaseInOut  Curve = "ease-in-out" // smoothstep, S-shaped
	CurveExp        Curve = "exp"         // exponential, most of the rise at the end
	CurveLog        Curve = "log"         // logarithmic, most of the rise at the start
	CurveSine       Curve = "sine"        // smooth from -> to -> from every Period
	CurveTriangle   Curve = "triangle"    // linear up then down every Period
	CurveSawtooth   Curve = "sawtooth"    // linear up, instant reset every Period
	CurveSquare     Curve = "square"      // spikes to "to" for Duty of every Period
	CurveNoise      Curve = "noise"       // uniform random in [from,to], resampled each Period
	CurveRandomWalk Curve = "random-walk" // bounded random walk between from and to
)

// AllCurves lists every curve, in documentation order.
var AllCurves = []Curve{
	CurveStep, CurveLinear, CurveEaseIn, CurveEaseOut, CurveEaseInOut,
	CurveExp, CurveLog, CurveSine, CurveTriangle, CurveSawtooth,
	CurveSquare, CurveNoise, CurveRandomWalk,
}

var curveAliases = map[string]Curve{
	"":            CurveLinear,
	"step":        CurveStep,
	"const":       CurveStep,
	"constant":    CurveStep,
	"flat":        CurveStep,
	"hold":        CurveStep,
	"linear":      CurveLinear,
	"lin":         CurveLinear,
	"ramp":        CurveLinear,
	"ease-in":     CurveEaseIn,
	"easein":      CurveEaseIn,
	"ease_in":     CurveEaseIn,
	"pow":         CurveEaseIn,
	"ease-out":    CurveEaseOut,
	"easeout":     CurveEaseOut,
	"ease_out":    CurveEaseOut,
	"ease-in-out": CurveEaseInOut,
	"easeinout":   CurveEaseInOut,
	"ease_in_out": CurveEaseInOut,
	"s-curve":     CurveEaseInOut,
	"sigmoid":     CurveEaseInOut,
	"smoothstep":  CurveEaseInOut,
	"exp":         CurveExp,
	"exponential": CurveExp,
	"log":         CurveLog,
	"logarithmic": CurveLog,
	"sine":        CurveSine,
	"sin":         CurveSine,
	"cosine":      CurveSine,
	"wave":        CurveSine,
	"triangle":    CurveTriangle,
	"tri":         CurveTriangle,
	"sawtooth":    CurveSawtooth,
	"saw":         CurveSawtooth,
	"square":      CurveSquare,
	"pulse":       CurveSquare,
	"spike":       CurveSquare,
	"spikes":      CurveSquare,
	"noise":       CurveNoise,
	"random":      CurveNoise,
	"jitter":      CurveNoise,
	"random-walk": CurveRandomWalk,
	"randomwalk":  CurveRandomWalk,
	"random_walk": CurveRandomWalk,
	"walk":        CurveRandomWalk,
	"brownian":    CurveRandomWalk,
}

// ParseCurve resolves a curve name or alias.
func ParseCurve(s string) (Curve, error) {
	c, ok := curveAliases[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return "", fmt.Errorf("unknown curve %q (known: %s)", s, CurveNames())
	}
	return c, nil
}

// CurveNames returns the canonical curve names as a comma separated list.
func CurveNames() string {
	names := make([]string, 0, len(AllCurves))
	for _, c := range AllCurves {
		names = append(names, string(c))
	}
	return strings.Join(names, ", ")
}

// Periodic reports whether the curve oscillates rather than ramping once.
func (c Curve) Periodic() bool {
	switch c {
	case CurveSine, CurveTriangle, CurveSawtooth, CurveSquare, CurveNoise, CurveRandomWalk:
		return true
	}
	return false
}

// UnmarshalYAML accepts a curve as a plain string.
func (c *Curve) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := ParseCurve(s)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// shapeInput carries everything a curve needs to produce a value in [0,1].
type shapeInput struct {
	// T is the phase progress, 0 at the start and 1 at the end.
	T float64
	// Elapsed is the time spent in the current phase.
	Elapsed time.Duration
	// Period is the oscillation period for periodic curves.
	Period time.Duration
	// Exponent tunes ease-in/ease-out/exp/log sharpness.
	Exponent float64
	// Duty is the fraction of each period spent high, for square waves.
	Duty float64
	// Rand is a per-segment RNG, nil for deterministic curves.
	Rand *rand.Rand
	// State is per-segment mutable state (used by random-walk).
	State *curveState
}

type curveState struct {
	lastTick int64
	value    float64
	seeded   bool
}

// shape maps the curve onto [0,1]; 0 selects "from" and 1 selects "to".
func (c Curve) shape(in shapeInput) float64 {
	t := clamp01(in.T)
	k := in.Exponent
	switch c {
	case CurveStep:
		return 1
	case CurveLinear:
		return t
	case CurveEaseIn:
		if k <= 0 {
			k = 2
		}
		return math.Pow(t, k)
	case CurveEaseOut:
		if k <= 0 {
			k = 2
		}
		return 1 - math.Pow(1-t, k)
	case CurveEaseInOut:
		return t * t * (3 - 2*t)
	case CurveExp:
		if k == 0 {
			k = 4
		}
		return (math.Exp(k*t) - 1) / (math.Exp(k) - 1)
	case CurveLog:
		if k <= 0 {
			k = 9
		}
		return math.Log1p(k*t) / math.Log1p(k)
	}

	// Periodic curves: tau is the position within the current period.
	period := in.Period
	if period <= 0 {
		period = 30 * time.Second
	}
	tau := math.Mod(in.Elapsed.Seconds(), period.Seconds()) / period.Seconds()
	switch c {
	case CurveSine:
		return 0.5 - 0.5*math.Cos(2*math.Pi*tau)
	case CurveTriangle:
		if tau < 0.5 {
			return tau * 2
		}
		return 2 - tau*2
	case CurveSawtooth:
		return tau
	case CurveSquare:
		duty := in.Duty
		if duty <= 0 {
			duty = 0.5
		}
		if tau < duty {
			return 1
		}
		return 0
	case CurveNoise:
		return in.sampleEachPeriod(period, func(r *rand.Rand, _ float64) float64 {
			return r.Float64()
		})
	case CurveRandomWalk:
		step := 0.25
		if in.Exponent > 0 {
			step = in.Exponent
		}
		return in.sampleEachPeriod(period, func(r *rand.Rand, prev float64) float64 {
			return clamp01(prev + (r.Float64()*2-1)*step)
		})
	}
	return t
}

// sampleEachPeriod resamples next() once per period and holds the value in
// between, so random curves produce steps rather than per-tick static.
func (in shapeInput) sampleEachPeriod(period time.Duration, next func(r *rand.Rand, prev float64) float64) float64 {
	st := in.State
	if st == nil || in.Rand == nil {
		return 0.5
	}
	tick := int64(in.Elapsed / period)
	if !st.seeded {
		st.seeded, st.lastTick, st.value = true, tick, next(in.Rand, 0.5)
		return st.value
	}
	for st.lastTick < tick {
		st.lastTick++
		st.value = next(in.Rand, st.value)
	}
	return st.value
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
