package units

import "testing"

func TestParseCPU(t *testing.T) {
	bases := Bases{Limit: 2, Request: 0.5, Node: 16, Default: BaseLimit}
	cases := []struct {
		in   string
		want float64
	}{
		{"500m", 0.5},
		{"1", 1},
		{"1.5", 1.5},
		{"2cores", 2},
		{"250millicores", 0.25},
		{"50%", 1},           // 50% of the 2 core limit
		{"50%request", 0.25}, // 50% of the 0.5 core request
		{"25%node", 4},
		{"0", 0},
	}
	for _, c := range cases {
		q, err := ParseCPU(c.in)
		if err != nil {
			t.Fatalf("ParseCPU(%q): %v", c.in, err)
		}
		if got := q.Resolve(bases); got != c.want {
			t.Errorf("ParseCPU(%q).Resolve() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseBytes(t *testing.T) {
	bases := Bases{Limit: 512 << 20, Request: 256 << 20, Node: 1 << 30, Default: BaseLimit}
	cases := []struct {
		in   string
		want float64
	}{
		{"512Mi", 512 << 20},
		{"1Gi", 1 << 30},
		{"1GiB", 1 << 30},
		{"1024", 1024},
		{"1MB", 1e6},
		{"50%", 256 << 20},
		{"200%request", 512 << 20},
		{"10%node", float64(1<<30) / 10},
	}
	for _, c := range cases {
		q, err := ParseBytes(c.in)
		if err != nil {
			t.Fatalf("ParseBytes(%q): %v", c.in, err)
		}
		if got := q.Resolve(bases); got != c.want {
			t.Errorf("ParseBytes(%q).Resolve() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"abc", "10%bogus", "-1", "12Xi"} {
		if _, err := ParseCPU(in); err == nil {
			t.Errorf("ParseCPU(%q) should fail", in)
		}
	}
	for _, in := range []string{"abc", "10%bogus", "-5Mi"} {
		if _, err := ParseBytes(in); err == nil {
			t.Errorf("ParseBytes(%q) should fail", in)
		}
	}
}

func TestResolveFallback(t *testing.T) {
	// A profile written against limits still runs when no limit is set.
	q, err := ParseCPU("50%limit")
	if err != nil {
		t.Fatal(err)
	}
	got := q.Resolve(Bases{Request: 1, Node: 8, Default: BaseLimit})
	if got != 0.5 {
		t.Errorf("fallback to request: got %v, want 0.5", got)
	}
	got = q.Resolve(Bases{Node: 8, Default: BaseLimit})
	if got != 4 {
		t.Errorf("fallback to node: got %v, want 4", got)
	}
	if got := q.Resolve(Bases{Default: BaseLimit}); got != 0 {
		t.Errorf("nothing known: got %v, want 0", got)
	}
}

func TestFormat(t *testing.T) {
	if got := FormatBytes(512 << 20); got != "512.0Mi" {
		t.Errorf("FormatBytes = %q", got)
	}
	if got := FormatCores(0.25); got != "250m" {
		t.Errorf("FormatCores = %q", got)
	}
}
