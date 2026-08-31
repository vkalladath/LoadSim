package profile

import (
	"testing"
	"time"
)

func TestParsePhaseDSL(t *testing.T) {
	got, err := ParsePhaseDSL("burst:90s:cpu=100%->25%@ease-out:mem=64Mi->256Mi;steady:cpu=25%:mem=60%")
	if err != nil {
		t.Fatalf("ParsePhaseDSL: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d phases, want 2", len(got))
	}
	if got[0].Name != "burst" || got[0].Duration.D() != 90*time.Second {
		t.Errorf("phase 0: %+v", got[0])
	}
	if got[0].CPU.From != "100%" || got[0].CPU.To != "25%" || got[0].CPU.Curve != "ease-out" {
		t.Errorf("phase 0 cpu: %+v", got[0].CPU)
	}
	if got[0].Memory.From != "64Mi" || got[0].Memory.To != "256Mi" {
		t.Errorf("phase 0 memory: %+v", got[0].Memory)
	}
	if got[1].Name != "steady" || got[1].Duration != 0 || got[1].CPU.To != "25%" {
		t.Errorf("phase 1: %+v", got[1])
	}
}

func TestParsePhaseDSLKeys(t *testing.T) {
	got, err := ParsePhaseDSL("wave:cpu=20%->90%@sine/5m:for=1h:repeat=2:jitter=5%")
	if err != nil {
		t.Fatalf("ParsePhaseDSL: %v", err)
	}
	p := got[0]
	if p.Duration.D() != time.Hour || p.Repeat != 2 {
		t.Errorf("duration/repeat: %+v", p)
	}
	if p.CPU.Curve != "sine" || p.CPU.Period.D() != 5*time.Minute || p.CPU.Jitter != "5%" || p.Memory.Jitter != "5%" {
		t.Errorf("cpu: %+v", p.CPU)
	}
}

func TestParsePhaseDSLErrors(t *testing.T) {
	for _, in := range []string{"a:b:c", "x:bogus=1", "phase:cpu=10%->90%@nope"} {
		if _, err := ParsePhaseDSL(in); err == nil {
			t.Errorf("ParsePhaseDSL(%q) should fail", in)
		}
	}
}
