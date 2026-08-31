// Package plan renders a compiled profile as an ASCII chart, so a profile can
// be sanity checked before it is deployed ("loadsim plan") and inspected while
// it runs (the "/" endpoint).
package plan

import (
	"fmt"
	"strings"
	"time"

	"loadsim/internal/profile"
	"loadsim/internal/units"
)

// Options controls the rendering.
type Options struct {
	// Width and Height are the chart size in characters (defaults 78 x 12).
	Width, Height int
	// Window is the time span to draw. 0 picks one full pass through the
	// profile, or 10 minutes for an open-ended profile.
	Window time.Duration
	// Marker, when >= 0, draws a "^" under the given elapsed time.
	Marker time.Duration
	// ShowMarker enables the Marker.
	ShowMarker bool
}

// Render draws the CPU and memory timelines of a profile.
func Render(p *profile.Profile, cpuBases, memBases units.Bases, o Options) string {
	if o.Width <= 0 {
		o.Width = 78
	}
	if o.Height <= 0 {
		o.Height = 12
	}
	window := o.Window
	if window <= 0 {
		window = p.Total()
	}
	if window <= 0 {
		window = 10 * time.Minute
	}
	if o.Window <= 0 {
		switch {
		case p.Loop:
			// Two iterations make the repetition obvious.
			window = p.Total() * 2
		case len(p.Phases) > 0 && p.Phases[len(p.Phases)-1].Duration == 0:
			// The final phase holds forever; show some of it.
			window = window * 4 / 3
		}
	}

	// Each column covers a slice of time, and short spikes are easy to miss
	// with point sampling, so every column is summarised by the minimum and
	// maximum target within its slice.
	const subSamples = 12
	cpu := make([]band, o.Width)
	mem := make([]band, o.Width)
	for i := 0; i < o.Width; i++ {
		for k := 0; k < subSamples; k++ {
			frac := (float64(i) + float64(k)/subSamples) / float64(o.Width)
			at := time.Duration(float64(window) * frac)
			t := p.Eval(at, cpuBases, memBases)
			cpu[i].add(t.CPUCores)
			mem[i].add(t.MemoryBytes)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "profile %q: %d phase(s), one pass %s%s\n",
		p.Name, len(p.Phases), fmtDur(p.Total()), loopSuffix(p))
	sb.WriteString(phaseBar(p, window, o.Width))
	sb.WriteString("\n")
	sb.WriteString(chart("CPU (cores)", cpu, o, units.FormatCores))
	sb.WriteString("\n")
	sb.WriteString(chart("Memory", mem, o, units.FormatBytes))
	sb.WriteString("\n")
	sb.WriteString(axis(window, o))
	sb.WriteString(legend(p, cpuBases, memBases))
	return sb.String()
}

func loopSuffix(p *profile.Profile) string {
	switch {
	case p.Loop:
		return ", looping"
	case p.HoldLast:
		return ", then holds the last phase"
	default:
		return ", then exits"
	}
}

// band is the range of values a single chart column covers.
type band struct {
	min, max float64
	seeded   bool
}

func (b *band) add(v float64) {
	if !b.seeded {
		b.min, b.max, b.seeded = v, v, true
		return
	}
	if v < b.min {
		b.min = v
	}
	if v > b.max {
		b.max = v
	}
}

// chart draws one series with a y axis. Solid "#" is the floor of each column
// and "-" is the range it varies over.
func chart(title string, series []band, o Options, format func(float64) string) string {
	maxV := 0.0
	for _, v := range series {
		if v.max > maxV {
			maxV = v.max
		}
	}
	if maxV <= 0 {
		return fmt.Sprintf("%s: flat zero\n", title)
	}
	top := maxV
	labelW := 10
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s  peak %s\n", title, format(maxV))
	for row := o.Height - 1; row >= 0; row-- {
		hi := top * float64(row+1) / float64(o.Height)
		lo := top * float64(row) / float64(o.Height)
		label := ""
		switch row {
		case o.Height - 1:
			label = format(top)
		case o.Height / 2, 0:
			label = format(hi)
		}
		fmt.Fprintf(&sb, "%*s |", labelW, label)
		// The tolerance keeps a value that is exactly at a row boundary from
		// falling into the row above because of float rounding.
		tol := top * 1e-9
		for _, v := range series {
			switch {
			case v.min >= hi-tol:
				sb.WriteString("#")
			case v.max > lo:
				sb.WriteString("-")
			default:
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "%*s +%s\n", labelW, "0", strings.Repeat("-", len(series)))
	return sb.String()
}

// phaseBar draws phase boundaries and names above the charts.
func phaseBar(p *profile.Profile, window time.Duration, width int) string {
	row := make([]byte, width)
	names := make([]byte, width)
	for i := range row {
		row[i], names[i] = '_', ' '
	}
	var acc time.Duration
	for _, ph := range p.Phases {
		col := int(float64(acc) / float64(window) * float64(width))
		if col >= 0 && col < width {
			row[col] = '|'
			// Write as much of the phase name as fits before the next phase.
			label := ph.Name
			for j := 0; j < len(label) && col+j < width; j++ {
				names[col+j] = label[j]
			}
		}
		acc += ph.Duration
		if ph.Duration == 0 {
			break
		}
	}
	return fmt.Sprintf("%*s  %s\n%*s  %s", 10, "phases", string(names), 10, "", string(row))
}

// niceSteps are the tick intervals the time axis is allowed to use, so labels
// come out as "5m" rather than "4m37s".
var niceSteps = []time.Duration{
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// tickStep picks the largest nice interval that still gives about six ticks.
func tickStep(window time.Duration) time.Duration {
	want := window / 6
	for i := len(niceSteps) - 1; i >= 0; i-- {
		if niceSteps[i] <= want {
			return niceSteps[i]
		}
	}
	return window
}

// axis draws the time axis labels under the charts.
func axis(window time.Duration, o Options) string {
	labels := make([]byte, o.Width)
	for i := range labels {
		labels[i] = ' '
	}
	step := tickStep(window)
	for at := time.Duration(0); at <= window && step > 0; at += step {
		col := int(float64(at) / float64(window) * float64(o.Width-1))
		s := fmtDur(at)
		start := col - len(s)/2
		if at == 0 {
			start = col
		}
		if start+len(s) > o.Width {
			start = o.Width - len(s)
		}
		if start < 0 {
			start = 0
		}
		for j := 0; j < len(s) && start+j < o.Width; j++ {
			labels[start+j] = s[j]
		}
	}
	out := fmt.Sprintf("%*s  %s\n", 10, "time", string(labels))
	if o.ShowMarker && window > 0 {
		col := int(float64(o.Marker) / float64(window) * float64(o.Width))
		if col >= 0 && col < o.Width {
			out += fmt.Sprintf("%*s  %s^ now (%s)\n", 10, "", strings.Repeat(" ", col), fmtDur(o.Marker))
		}
	}
	return out
}

// legend lists each phase with its resolved start and end targets.
func legend(p *profile.Profile, cpuBases, memBases units.Bases) string {
	var sb strings.Builder
	sb.WriteString("\nphases:\n")
	var acc time.Duration
	for i, ph := range p.Phases {
		dur := fmtDur(ph.Duration)
		if ph.Duration == 0 {
			dur = "hold"
		}
		fmt.Fprintf(&sb, "  %2d. %-16s %-8s at %-7s cpu %-22s memory %s\n",
			i+1, ph.Name, dur, fmtDur(acc),
			segDesc(ph.CPU, cpuBases, units.FormatCores),
			segDesc(ph.Memory, memBases, units.FormatBytes))
		acc += ph.Duration
	}
	return sb.String()
}

func segDesc(s profile.Segment, bases units.Bases, format func(float64) string) string {
	from, to := s.From.Resolve(bases), s.To.Resolve(bases)
	if s.Curve == profile.CurveStep || from == to {
		return format(to)
	}
	extra := ""
	if s.Curve.Periodic() && s.Period > 0 {
		extra = "/" + fmtDur(s.Period)
	}
	return fmt.Sprintf("%s->%s %s%s", format(from), format(to), s.Curve, extra)
}

func fmtDur(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d < time.Second:
		return d.String()
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		if d%time.Minute == 0 {
			return fmt.Sprintf("%.0fm", d.Minutes())
		}
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		if d%time.Hour == 0 {
			return fmt.Sprintf("%.0fh", d.Hours())
		}
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
