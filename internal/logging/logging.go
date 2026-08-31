// Package logging emits human or machine readable status output.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"loadsim/internal/status"
	"loadsim/internal/units"
)

// Format selects the output style.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Logger writes timestamped lines to a writer, in text or JSON.
type Logger struct {
	format string
	out    io.Writer
	mu     sync.Mutex
}

// New creates a logger. An empty or unknown format means text.
func New(format string, out io.Writer) *Logger {
	if out == nil {
		out = os.Stderr
	}
	if strings.ToLower(format) != FormatJSON {
		format = FormatText
	}
	return &Logger{format: strings.ToLower(format), out: out}
}

// Field is one structured key/value pair.
type Field struct {
	Key   string
	Value interface{}
}

// F is shorthand for building a field.
func F(key string, value interface{}) Field { return Field{key, value} }

func (l *Logger) event(level, msg string, fields ...Field) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UTC()
	if l.format == FormatJSON {
		rec := map[string]interface{}{
			"ts":    now.Format(time.RFC3339Nano),
			"level": level,
			"msg":   msg,
		}
		for _, f := range fields {
			rec[f.Key] = f.Value
		}
		b, err := json.Marshal(rec)
		if err != nil {
			fmt.Fprintf(l.out, "{\"level\":\"error\",\"msg\":\"log marshal failed\"}\n")
			return
		}
		l.out.Write(append(b, '\n'))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %-5s %s", now.Format("2006-01-02T15:04:05.000Z"), strings.ToUpper(level), msg)
	for _, f := range fields {
		fmt.Fprintf(&sb, " %s=%v", f.Key, f.Value)
	}
	sb.WriteByte('\n')
	io.WriteString(l.out, sb.String())
}

// Infof logs an informational message.
func (l *Logger) Infof(format string, args ...interface{}) {
	l.event("info", fmt.Sprintf(format, args...))
}

// Warnf logs a warning.
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.event("warn", fmt.Sprintf(format, args...))
}

// Errorf logs an error.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.event("error", fmt.Sprintf(format, args...))
}

// Fields logs a message with structured fields.
func (l *Logger) Fields(msg string, fields ...Field) { l.event("info", msg, fields...) }

// Status logs the periodic one-line summary of targets versus reality.
func (l *Logger) Status(s status.Snapshot) {
	l.event("info", "status", statusFields(s)...)
}

// PhaseChange logs a phase transition.
func (l *Logger) PhaseChange(s status.Snapshot) {
	l.event("info", "phase change", statusFields(s)...)
}

func statusFields(s status.Snapshot) []Field {
	fields := []Field{
		F("phase", fmt.Sprintf("%s(%d/%d)", s.Phase, s.Targets.PhaseIndex+1, s.PhaseCount)),
		F("progress", fmt.Sprintf("%.0f%%", s.Targets.Progress*100)),
		F("elapsed", time.Duration(s.ElapsedSec*float64(time.Second)).Round(time.Second).String()),
		F("cpu_target", units.FormatCores(s.CPU.TargetCores)),
		F("cpu_actual", units.FormatCores(s.CPU.ActualCores)),
		F("cpu_duty", fmt.Sprintf("%.2f", s.CPU.Duty)),
		F("mem_target", units.FormatBytes(s.Memory.TargetBytes)),
		F("mem_rss", units.FormatBytes(float64(s.Memory.RSSBytes))),
	}
	if s.Resources.CPULimitCores > 0 {
		fields = append(fields, F("cpu_pct_limit", fmt.Sprintf("%.0f%%", s.CPULimitFraction()*100)))
	}
	if s.Resources.MemLimitBytes > 0 {
		fields = append(fields, F("mem_pct_limit", fmt.Sprintf("%.0f%%", s.MemoryLimitFraction()*100)))
	}
	if s.Throttling.Available && s.Throttling.ThrottledSeconds > 0 {
		fields = append(fields, F("throttled", fmt.Sprintf("%.1fs", s.Throttling.ThrottledSeconds)))
	}
	if s.CPU.Saturated {
		fields = append(fields, F("saturated", true))
	}
	if s.Memory.TouchPasses > 0 {
		fields = append(fields, F("touch_passes", s.Memory.TouchPasses))
	}
	return fields
}

// Sources logs where configuration came from, in a stable order.
func (l *Logger) Sources(sources []string, extra map[string]string) {
	fields := []Field{F("chain", strings.Join(sources, " -> "))}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fields = append(fields, F(k, extra[k]))
	}
	l.event("info", "configuration", fields...)
}
