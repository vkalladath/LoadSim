package profile

import (
	"math"
	"testing"
	"time"

	"loadsim/internal/units"
)

func bases() (units.Bases, units.Bases) {
	return units.Bases{Limit: 2, Request: 1, Node: 8, Default: units.BaseLimit},
		units.Bases{Limit: 512 << 20, Request: 256 << 20, Node: 1 << 30, Default: units.BaseLimit}
}

func build(t *testing.T, specs []PhaseSpec, loop, hold bool) *Profile {
	t.Helper()
	p, err := Build("test", specs, loop, hold, 0, 42)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

func TestSegmentShorthand(t *testing.T) {
	cases := []struct {
		in       string
		from, to string
		curve    string
		period   time.Duration
		exponent float64
	}{
		{in: "50%", from: "50%", to: "50%"},
		{in: "10%->90%", from: "10%", to: "90%"},
		{in: "10% -> 90% @ease-in-out", from: "10%", to: "90%", curve: "ease-in-out"},
		{in: "0->1@sine/2m", from: "0", to: "1", curve: "sine", period: 2 * time.Minute},
		{in: "10%->90%@exp^6", from: "10%", to: "90%", curve: "exp", exponent: 6},
	}
	for _, c := range cases {
		got, err := ParseSegmentShorthand(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got.From != c.from || got.To != c.to || got.Curve != c.curve ||
			got.Period.D() != c.period || got.Exponent != c.exponent {
			t.Errorf("%q -> %+v", c.in, got)
		}
	}
}

func TestConstantPhase(t *testing.T) {
	cb, mb := bases()
	p := build(t, []PhaseSpec{{
		Name:   "steady",
		CPU:    SegmentSpec{From: "25%", To: "25%"},
		Memory: SegmentSpec{From: "128Mi", To: "128Mi"},
	}}, false, true)

	for _, at := range []time.Duration{0, time.Second, time.Hour} {
		got := p.Eval(at, cb, mb)
		if got.CPUCores != 0.5 {
			t.Errorf("at %s: cpu = %v, want 0.5", at, got.CPUCores)
		}
		if got.MemoryBytes != 128<<20 {
			t.Errorf("at %s: memory = %v", at, got.MemoryBytes)
		}
		if got.Finished {
			t.Errorf("at %s: should not be finished while holding", at)
		}
	}
}

func TestLinearRampAndPhaseOrder(t *testing.T) {
	cb, mb := bases()
	p := build(t, []PhaseSpec{
		{Name: "ramp", Duration: Duration(10 * time.Second), CPU: SegmentSpec{From: "0", To: "1", Curve: "linear"}},
		{Name: "steady", CPU: SegmentSpec{From: "250m", To: "250m"}},
	}, false, true)

	if got := p.Eval(0, cb, mb); got.CPUCores != 0 || got.PhaseName != "ramp" {
		t.Errorf("start: %+v", got)
	}
	if got := p.Eval(5*time.Second, cb, mb); math.Abs(got.CPUCores-0.5) > 1e-9 {
		t.Errorf("midpoint: cpu = %v, want 0.5", got.CPUCores)
	}
	got := p.Eval(20*time.Second, cb, mb)
	if got.PhaseName != "steady" || got.CPUCores != 0.25 {
		t.Errorf("after ramp: %+v", got)
	}
	if p.Total() != 10*time.Second {
		t.Errorf("total = %v", p.Total())
	}
}

func TestLoopAndFinish(t *testing.T) {
	cb, mb := bases()
	specs := []PhaseSpec{
		{Name: "a", Duration: Duration(time.Minute), CPU: SegmentSpec{From: "1", To: "1"}},
		{Name: "b", Duration: Duration(time.Minute), CPU: SegmentSpec{From: "2", To: "2"}},
	}

	loop := build(t, specs, true, false)
	// 150s into a 120s profile: back in phase "a", second iteration.
	if got := loop.Eval(150*time.Second, cb, mb); got.PhaseName != "a" || got.Iteration != 1 || got.CPUCores != 1 {
		t.Errorf("looping: %+v", got)
	}
	if got := loop.Eval(210*time.Second, cb, mb); got.PhaseName != "b" || got.Iteration != 1 {
		t.Errorf("looping into phase b: %+v", got)
	}

	once := build(t, specs, false, false)
	if got := once.Eval(5*time.Minute, cb, mb); !got.Finished || got.CPUCores != 0 {
		t.Errorf("finished: %+v", got)
	}

	hold := build(t, specs, false, true)
	if got := hold.Eval(5*time.Minute, cb, mb); got.Finished || got.CPUCores != 2 {
		t.Errorf("hold last: %+v", got)
	}
}

func TestCurveShapes(t *testing.T) {
	// Every monotonic curve must start at "from" and end at "to".
	for _, c := range []Curve{CurveLinear, CurveEaseIn, CurveEaseOut, CurveEaseInOut, CurveExp, CurveLog} {
		if got := c.shape(shapeInput{T: 0}); math.Abs(got) > 1e-9 {
			t.Errorf("%s at t=0: %v, want 0", c, got)
		}
		if got := c.shape(shapeInput{T: 1}); math.Abs(got-1) > 1e-9 {
			t.Errorf("%s at t=1: %v, want 1", c, got)
		}
		prev := -1.0
		for i := 0; i <= 20; i++ {
			v := c.shape(shapeInput{T: float64(i) / 20})
			if v < prev-1e-12 {
				t.Errorf("%s is not monotonic at t=%.2f", c, float64(i)/20)
			}
			prev = v
		}
	}
	// ease-in is slower than linear at the midpoint, ease-out faster.
	if CurveEaseIn.shape(shapeInput{T: 0.5}) >= 0.5 {
		t.Error("ease-in should lag at the midpoint")
	}
	if CurveEaseOut.shape(shapeInput{T: 0.5}) <= 0.5 {
		t.Error("ease-out should lead at the midpoint")
	}
	// step ignores progress.
	if CurveStep.shape(shapeInput{T: 0}) != 1 {
		t.Error("step should jump immediately")
	}
}

func TestSquareDuty(t *testing.T) {
	seg := Segment{
		From:   units.Quantity{Amount: 0.1, Raw: "0.1"},
		To:     units.Quantity{Amount: 1, Raw: "1"},
		Curve:  CurveSquare,
		Period: 10 * time.Second,
		Duty:   0.2,
	}
	if got := seg.value(0, 0, units.Bases{}); got != 1 {
		t.Errorf("start of period: %v, want 1 (spike)", got)
	}
	if got := seg.value(0.5, 5*time.Second, units.Bases{}); got != 0.1 {
		t.Errorf("after the spike: %v, want 0.1 (baseline)", got)
	}
	if got := seg.value(0.9, 11*time.Second, units.Bases{}); got != 1 {
		t.Errorf("next period: %v, want 1", got)
	}
}

func TestSineOscillates(t *testing.T) {
	seg := Segment{
		From:   units.Quantity{Amount: 0, Raw: "0"},
		To:     units.Quantity{Amount: 1, Raw: "1"},
		Curve:  CurveSine,
		Period: 60 * time.Second,
	}
	if got := seg.value(0, 0, units.Bases{}); math.Abs(got) > 1e-9 {
		t.Errorf("t=0: %v, want 0", got)
	}
	if got := seg.value(0.5, 30*time.Second, units.Bases{}); math.Abs(got-1) > 1e-9 {
		t.Errorf("half period: %v, want 1", got)
	}
	if got := seg.value(1, 60*time.Second, units.Bases{}); math.Abs(got) > 1e-9 {
		t.Errorf("full period: %v, want 0", got)
	}
}

func TestJitterStaysInBand(t *testing.T) {
	p := build(t, []PhaseSpec{{
		Name: "j", CPU: SegmentSpec{From: "1", To: "1", Jitter: "10%"},
	}}, false, true)
	cb, mb := bases()
	for i := 0; i < 200; i++ {
		v := p.Eval(time.Duration(i)*time.Second, cb, mb).CPUCores
		if v < 0.9-1e-9 || v > 1.1+1e-9 {
			t.Fatalf("jittered value %v outside +/-10%%", v)
		}
	}
}

func TestPhaseJitterAppliesToBothResources(t *testing.T) {
	p := build(t, []PhaseSpec{{
		Name: "j", Jitter: "10%",
		CPU:    SegmentSpec{From: "1", To: "1"},
		Memory: SegmentSpec{From: "100Mi", To: "100Mi", Jitter: "0%"},
	}}, false, true)
	if p.Phases[0].CPU.Jitter != 0.1 {
		t.Errorf("cpu jitter = %v, want 0.1 inherited from the phase", p.Phases[0].CPU.Jitter)
	}
	if p.Phases[0].Memory.Jitter != 0 {
		t.Errorf("memory jitter = %v, want the segment's own 0", p.Phases[0].Memory.Jitter)
	}
}

func TestPeakResolution(t *testing.T) {
	cb, mb := bases()
	p := build(t, []PhaseSpec{
		{Name: "a", Duration: Duration(time.Minute), CPU: SegmentSpec{From: "10%", To: "90%"}, Memory: SegmentSpec{From: "10Mi", To: "100Mi"}},
		{Name: "b", CPU: SegmentSpec{From: "50%", To: "50%"}, Memory: SegmentSpec{From: "200Mi", To: "200Mi"}},
	}, false, true)
	if got := p.PeakCPU(cb); math.Abs(got-1.8) > 1e-9 {
		t.Errorf("PeakCPU = %v, want 1.8", got)
	}
	if got := p.PeakMemory(mb); got != 200<<20 {
		t.Errorf("PeakMemory = %v", got)
	}
}

func TestRepeatExpandsPhases(t *testing.T) {
	p := build(t, []PhaseSpec{{
		Name: "burst", Duration: Duration(time.Minute), Repeat: 3,
		CPU: SegmentSpec{From: "1", To: "1"},
	}}, false, true)
	if len(p.Phases) != 3 || p.Total() != 3*time.Minute {
		t.Fatalf("repeat: %d phases, total %v", len(p.Phases), p.Total())
	}
	if p.Phases[0].Name != "burst-1" || p.Phases[2].Name != "burst-3" {
		t.Errorf("names: %q %q", p.Phases[0].Name, p.Phases[2].Name)
	}
}

func TestParseDurationExtras(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30s":   30 * time.Second,
		"1h30m": 90 * time.Minute,
		"2d":    48 * time.Hour,
		"90":    90 * time.Second,
		"1m30s": 90 * time.Second,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestBuildRejectsUnknownCurve(t *testing.T) {
	_, err := Build("t", []PhaseSpec{{CPU: SegmentSpec{From: "1", To: "2", Curve: "banana"}}}, false, true, 0, 1)
	if err == nil {
		t.Fatal("expected an error for an unknown curve")
	}
}
