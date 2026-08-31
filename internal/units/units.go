// Package units parses the human-friendly resource quantities used in LoadSim
// configuration and resolves them against the pod's requests/limits.
//
// A quantity is either absolute ("500m", "1.5", "512Mi") or relative to a base
// ("80%", "80%limit", "50%request", "25%node"). Relative values are resolved at
// runtime, so the same profile can be reused across pods with different sizing.
package units

import (
	"fmt"
	"strconv"
	"strings"
)

// Base selects what a relative (percentage) quantity is a percentage of.
type Base string

const (
	// BaseAbsolute means the value is already in cores or bytes.
	BaseAbsolute Base = ""
	// BaseLimit resolves against the container's CPU/memory limit.
	BaseLimit Base = "limit"
	// BaseRequest resolves against the container's CPU/memory request.
	BaseRequest Base = "request"
	// BaseNode resolves against the total capacity visible to the process
	// (number of CPUs, total machine memory).
	BaseNode Base = "node"
	// BaseDefault defers the choice to the profile's percent_base setting.
	BaseDefault Base = "default"
)

// Bases holds the concrete numbers a relative quantity can be resolved against.
// CPU values are in cores, memory values in bytes. A zero value means "unknown"
// and resolution falls back to the next known base (limit -> request -> node).
type Bases struct {
	Limit   float64
	Request float64
	Node    float64
	// Default is the base used for a bare "%" quantity.
	Default Base
}

// Quantity is a parsed, not-yet-resolved resource quantity.
type Quantity struct {
	// Amount is cores/bytes when Base is BaseAbsolute, otherwise a fraction
	// (0.8 for "80%").
	Amount float64
	Base   Base
	// Raw is the original text, kept for diagnostics and /config output.
	Raw string
}

// IsZero reports whether the quantity was left unset.
func (q Quantity) IsZero() bool { return q.Raw == "" }

// String renders the quantity the way it was written.
func (q Quantity) String() string {
	if q.Raw != "" {
		return q.Raw
	}
	return "0"
}

// Resolve turns the quantity into an absolute number of cores or bytes.
// Unknown bases degrade gracefully instead of erroring so that a profile
// written against limits still runs in a pod with no limits set.
func (q Quantity) Resolve(b Bases) float64 {
	if q.Base == BaseAbsolute {
		return q.Amount
	}
	base := q.Base
	if base == BaseDefault || base == "" {
		base = b.Default
		if base == "" {
			base = BaseLimit
		}
	}
	// Ordered fallback: whatever was asked for first, then the next best
	// known quantity.
	order := []Base{base}
	switch base {
	case BaseLimit:
		order = append(order, BaseRequest, BaseNode)
	case BaseRequest:
		order = append(order, BaseLimit, BaseNode)
	case BaseNode:
		order = append(order, BaseLimit, BaseRequest)
	}
	for _, o := range order {
		var v float64
		switch o {
		case BaseLimit:
			v = b.Limit
		case BaseRequest:
			v = b.Request
		case BaseNode:
			v = b.Node
		}
		if v > 0 {
			return q.Amount * v
		}
	}
	return 0
}

// MarshalYAML keeps round-tripped configuration readable.
func (q Quantity) MarshalYAML() (interface{}, error) { return q.String(), nil }

// MarshalJSON keeps /config output readable.
func (q Quantity) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(q.String())), nil
}

func parseBase(s string) (Base, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return BaseDefault, true
	case "l", "lim", "limit", "limits":
		return BaseLimit, true
	case "r", "req", "request", "requests":
		return BaseRequest, true
	case "n", "node", "t", "total", "machine", "host", "capacity":
		return BaseNode, true
	}
	return BaseDefault, false
}

// splitPercent detects a relative quantity and returns the numeric part and base.
func splitPercent(s string) (num string, base Base, rel bool, err error) {
	i := strings.Index(s, "%")
	if i < 0 {
		return s, BaseAbsolute, false, nil
	}
	b, ok := parseBase(s[i+1:])
	if !ok {
		return "", BaseDefault, true, fmt.Errorf("unknown percentage base %q (want limit, request or node)", s[i+1:])
	}
	return s[:i], b, true, nil
}

// ParseCPU parses a CPU quantity into cores.
//
//	"500m", "0.5", "1", "2cores"  -> absolute cores
//	"80%", "80%limit", "50%req"   -> relative
func ParseCPU(s string) (Quantity, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Quantity{}, nil
	}
	body := strings.ToLower(raw)
	num, base, rel, err := splitPercent(body)
	if err != nil {
		return Quantity{}, err
	}
	num = strings.TrimSpace(num)
	if rel {
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return Quantity{}, fmt.Errorf("invalid cpu percentage %q", raw)
		}
		return Quantity{Amount: f / 100, Base: base, Raw: raw}, nil
	}

	mult := 1.0
	switch {
	case strings.HasSuffix(num, "m"):
		num, mult = strings.TrimSuffix(num, "m"), 0.001
	case strings.HasSuffix(num, "millicores"):
		num, mult = strings.TrimSuffix(num, "millicores"), 0.001
	case strings.HasSuffix(num, "milli"):
		num, mult = strings.TrimSuffix(num, "milli"), 0.001
	case strings.HasSuffix(num, "cores"):
		num = strings.TrimSuffix(num, "cores")
	case strings.HasSuffix(num, "core"):
		num = strings.TrimSuffix(num, "core")
	case strings.HasSuffix(num, "c"):
		num = strings.TrimSuffix(num, "c")
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return Quantity{}, fmt.Errorf("invalid cpu quantity %q", raw)
	}
	if f < 0 {
		return Quantity{}, fmt.Errorf("cpu quantity %q must not be negative", raw)
	}
	return Quantity{Amount: f * mult, Base: BaseAbsolute, Raw: raw}, nil
}

var byteSuffixes = []struct {
	suffix string
	mult   float64
}{
	{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
	{"ki", 1 << 10}, {"mi", 1 << 20}, {"gi", 1 << 30}, {"ti", 1 << 40},
	{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12},
	{"k", 1e3}, {"m", 1e6}, {"g", 1e9}, {"t", 1e12},
	{"b", 1},
}

// ParseBytes parses a memory quantity into bytes.
//
//	"512Mi", "1GiB", "500MB", "1048576" -> absolute bytes
//	"75%", "75%limit", "150%request"    -> relative
//
// Binary (Ki/Mi/Gi) and decimal (KB/MB/GB) suffixes follow the Kubernetes
// convention; a bare "M"/"G" is decimal, matching kubectl.
func ParseBytes(s string) (Quantity, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Quantity{}, nil
	}
	body := strings.ToLower(raw)
	num, base, rel, err := splitPercent(body)
	if err != nil {
		return Quantity{}, err
	}
	num = strings.TrimSpace(num)
	if rel {
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return Quantity{}, fmt.Errorf("invalid memory percentage %q", raw)
		}
		return Quantity{Amount: f / 100, Base: base, Raw: raw}, nil
	}
	mult := 1.0
	for _, sfx := range byteSuffixes {
		if strings.HasSuffix(num, sfx.suffix) {
			num, mult = strings.TrimSuffix(num, sfx.suffix), sfx.mult
			break
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return Quantity{}, fmt.Errorf("invalid memory quantity %q", raw)
	}
	if f < 0 {
		return Quantity{}, fmt.Errorf("memory quantity %q must not be negative", raw)
	}
	return Quantity{Amount: f * mult, Base: BaseAbsolute, Raw: raw}, nil
}

// ParsePercentBase parses the profile-level percent_base setting.
func ParsePercentBase(s string) (Base, error) {
	b, ok := parseBase(s)
	if !ok || b == BaseDefault {
		if strings.TrimSpace(s) == "" {
			return BaseLimit, nil
		}
		return BaseLimit, fmt.Errorf("unknown percent base %q (want limit, request or node)", s)
	}
	return b, nil
}

// FormatBytes renders a byte count with a binary suffix, for logs and /status.
func FormatBytes(b float64) string {
	abs := b
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1<<30:
		return fmt.Sprintf("%.2fGi", b/(1<<30))
	case abs >= 1<<20:
		return fmt.Sprintf("%.1fMi", b/(1<<20))
	case abs >= 1<<10:
		return fmt.Sprintf("%.1fKi", b/(1<<10))
	default:
		return fmt.Sprintf("%.0fB", b)
	}
}

// FormatCores renders cores as millicores when small, for logs and /status.
func FormatCores(c float64) string {
	if c < 1 {
		return fmt.Sprintf("%.0fm", c*1000)
	}
	return fmt.Sprintf("%.3f", c)
}
