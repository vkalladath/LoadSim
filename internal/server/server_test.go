package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loadsim/internal/cpuload"
	"loadsim/internal/memload"
	"loadsim/internal/profile"
	"loadsim/internal/resources"
	"loadsim/internal/status"
	"loadsim/internal/units"
)

type fakeProvider struct {
	snap  status.Snapshot
	ready bool
	prof  *profile.Profile
}

func (f *fakeProvider) Snapshot() status.Snapshot { return f.snap }
func (f *fakeProvider) Ready() bool               { return f.ready }
func (f *fakeProvider) ConfigYAML() []byte        { return []byte("cpu: 50%\n") }
func (f *fakeProvider) Profile() *profile.Profile { return f.prof }
func (f *fakeProvider) Bases() (units.Bases, units.Bases) {
	return units.Bases{Limit: 1, Default: units.BaseLimit},
		units.Bases{Limit: 512 << 20, Default: units.BaseLimit}
}

func newFake(t *testing.T) *fakeProvider {
	t.Helper()
	p, err := profile.Build("test", []profile.PhaseSpec{
		{Name: "warmup", Duration: profile.Duration(time.Minute), CPU: profile.SegmentSpec{From: "10%", To: "90%"}, Memory: profile.SegmentSpec{From: "10%", To: "50%"}},
		{Name: "steady", CPU: profile.SegmentSpec{From: "40%", To: "40%"}, Memory: profile.SegmentSpec{From: "50%", To: "50%"}},
	}, false, true, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeProvider{
		prof: p,
		snap: status.Snapshot{
			Name:       "test",
			Version:    "v0",
			Phase:      "warmup",
			PhaseCount: 2,
			Targets:    profile.Targets{PhaseName: "warmup", Progress: 0.5, CPUCores: 0.5, MemoryBytes: 256 << 20},
			CPU:        cpuload.Stats{TargetCores: 0.5, ActualCores: 0.49, Duty: 0.5, Workers: 2},
			Memory:     memload.Stats{TargetBytes: 256 << 20, AllocatedBytes: 256 << 20, RSSBytes: 260 << 20, Chunks: 64, ChunkSize: 4 << 20},
			Resources:  resources.Info{CPULimitCores: 1, MemLimitBytes: 512 << 20, NodeCPUCores: 8, Sources: map[string]string{}},
			Throttling: status.Throttling{Available: true, Periods: 100, ThrottledPeriods: 3, ThrottledSeconds: 0.25},
		},
	}
}

func serve(t *testing.T, p Provider, path string) *httptest.ResponseRecorder {
	t.Helper()
	s := New(Options{Addr: ":0", MetricsPath: "/metrics"}, p)
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestReadyzGatesOnReadiness(t *testing.T) {
	f := newFake(t)
	if got := serve(t, f, "/readyz").Code; got != http.StatusServiceUnavailable {
		t.Errorf("/readyz before ready = %d, want 503", got)
	}
	f.ready = true
	if got := serve(t, f, "/readyz").Code; got != http.StatusOK {
		t.Errorf("/readyz when ready = %d, want 200", got)
	}
	if got := serve(t, f, "/healthz").Code; got != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 always", got)
	}
}

func TestMetricsExposition(t *testing.T) {
	body := serve(t, newFake(t), "/metrics").Body.String()
	for _, want := range []string{
		`loadsim_info{version="v0",profile="test",`,
		`loadsim_phase{phase="warmup"} 1`,
		"loadsim_cpu_target_cores 0.5",
		"loadsim_cpu_actual_cores 0.49",
		"loadsim_memory_rss_bytes 2.7262976e+08",
		"loadsim_memory_limit_bytes 5.36870912e+08",
		"loadsim_cgroup_cpu_throttled_seconds_total 0.25",
		"# TYPE loadsim_cpu_duty_cycle_ratio gauge",
		"# TYPE loadsim_memory_touch_passes_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output is missing %q\n%s", want, body)
		}
	}
	// Every metric line must have a HELP and TYPE header.
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.SplitN(strings.SplitN(line, " ", 2)[0], "{", 2)[0]
		if !strings.Contains(body, "# TYPE "+name+" ") {
			t.Errorf("metric %q has no TYPE header", name)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	rec := serve(t, newFake(t), "/status")
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("/status is not valid JSON: %v", err)
	}
	if out["phase"] != "warmup" {
		t.Errorf("phase = %v", out["phase"])
	}
}

func TestRootAndPlanRender(t *testing.T) {
	for _, path := range []string{"/", "/plan"} {
		body := serve(t, newFake(t), path).Body.String()
		if !strings.Contains(body, "phases:") {
			t.Errorf("%s did not render the plan:\n%s", path, body)
		}
	}
	if got := serve(t, newFake(t), "/nope").Code; got != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", got)
	}
}

func TestConfigEndpoint(t *testing.T) {
	if body := serve(t, newFake(t), "/config").Body.String(); !strings.Contains(body, "cpu: 50%") {
		t.Errorf("/config = %q", body)
	}
}

func TestDisabled(t *testing.T) {
	for _, addr := range []string{"", "off", "none"} {
		if !New(Options{Addr: addr}, newFake(t)).Disabled() {
			t.Errorf("addr %q should disable the server", addr)
		}
	}
	if New(Options{Addr: ":8080"}, newFake(t)).Disabled() {
		t.Error(":8080 should not be disabled")
	}
}
