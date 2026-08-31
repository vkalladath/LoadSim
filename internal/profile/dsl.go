package profile

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePhaseDSL parses the compact one-line phase syntax used by the --phases
// flag and the LOADSIM_PHASES environment variable, so a complete profile can
// be expressed without a config file.
//
// Grammar:
//
//	phases   := phase (";" phase)*
//	phase    := field (":" field)*
//	field    := NAME | DURATION | key "=" value
//	key      := cpu | c | memory | mem | m | duration | for | repeat | jitter
//	value    := <segment shorthand, see ParseSegmentShorthand>
//
// Examples:
//
//	"burst:60s:cpu=100%:mem=80%;steady:cpu=30%:mem=50%"
//	"ramp:10m:cpu=0->1@ease-in-out:mem=64Mi->512Mi"
//	"wave::cpu=20%->90%@sine/5m:for=1h"
func ParsePhaseDSL(s string) ([]PhaseSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []PhaseSpec
	for _, chunk := range strings.Split(s, ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		spec, err := parsePhaseChunk(chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no phases found in %q", s)
	}
	return out, nil
}

func parsePhaseChunk(chunk string) (PhaseSpec, error) {
	var spec PhaseSpec
	fields := splitTopLevel(chunk, ':')
	for i, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		key, value, hasKV := strings.Cut(f, "=")
		if !hasKV {
			// A positional field is the duration if it parses as one, else the
			// phase name (only allowed first).
			if d, err := ParseDuration(f); err == nil && spec.Duration == 0 {
				spec.Duration = Duration(d)
				continue
			}
			if i == 0 && spec.Name == "" {
				spec.Name = f
				continue
			}
			return spec, fmt.Errorf("phase %q: cannot interpret field %q", chunk, f)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "name", "n":
			spec.Name = value
		case "duration", "dur", "for", "time", "t":
			d, err := ParseDuration(value)
			if err != nil {
				return spec, fmt.Errorf("phase %q: invalid duration %q", chunk, value)
			}
			spec.Duration = Duration(d)
		case "cpu", "c":
			seg, err := ParseSegmentShorthand(value)
			if err != nil {
				return spec, fmt.Errorf("phase %q: %w", chunk, err)
			}
			spec.CPU = seg
		case "memory", "mem", "m", "ram":
			seg, err := ParseSegmentShorthand(value)
			if err != nil {
				return spec, fmt.Errorf("phase %q: %w", chunk, err)
			}
			spec.Memory = seg
		case "repeat", "x":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return spec, fmt.Errorf("phase %q: invalid repeat %q", chunk, value)
			}
			spec.Repeat = n
		case "jitter", "j":
			spec.CPU.Jitter, spec.Memory.Jitter = value, value
		default:
			return spec, fmt.Errorf("phase %q: unknown key %q", chunk, key)
		}
	}
	return spec, nil
}

// splitTopLevel splits on sep, ignoring separators inside {...} so the mapping
// form can be embedded if someone wants to.
func splitTopLevel(s string, sep rune) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + len(string(sep))
			}
		}
	}
	return append(out, s[start:])
}
