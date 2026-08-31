// Package server exposes the HTTP surface: health probes, a human readable
// status page, JSON status, the effective configuration and Prometheus metrics.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"loadsim/internal/plan"
	"loadsim/internal/profile"
	"loadsim/internal/status"
	"loadsim/internal/units"
)

// Provider supplies the current state and the static bits needed to render it.
type Provider interface {
	Snapshot() status.Snapshot
	Ready() bool
	ConfigYAML() []byte
	Profile() *profile.Profile
	Bases() (cpu units.Bases, mem units.Bases)
}

// Options configures the server.
type Options struct {
	Addr        string
	MetricsPath string
	Pprof       bool
}

// Server wraps an http.Server.
type Server struct {
	opts Options
	srv  *http.Server
	p    Provider
}

// New builds the HTTP server. Addr "off" or "" disables it (Run returns nil).
func New(opts Options, p Provider) *Server {
	if opts.MetricsPath == "" {
		opts.MetricsPath = "/metrics"
	}
	s := &Server{opts: opts, p: p}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/livez", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/plan", s.handlePlan)
	mux.HandleFunc(opts.MetricsPath, s.handleMetrics)
	if opts.Pprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/heap", pprof.Handler("heap").ServeHTTP)
	}
	s.srv = &http.Server{
		Addr:              opts.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Disabled reports whether the server is switched off.
func (s *Server) Disabled() bool {
	a := strings.ToLower(strings.TrimSpace(s.opts.Addr))
	return a == "" || a == "off" || a == "none" || a == "disabled"
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if s.Disabled() {
		<-ctx.Done()
		return nil
	}
	errc := make(chan error, 1)
	go func() {
		err := s.srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdown)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.p.Ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "starting up")
		return
	}
	fmt.Fprintln(w, "ready")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.p.Snapshot()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(s.p.ConfigYAML())
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var sb strings.Builder
	writeMetrics(&sb, s.p.Snapshot())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(sb.String()))
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	snap := s.p.Snapshot()
	cpuBases, memBases := s.p.Bases()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, plan.Render(s.p.Profile(), cpuBases, memBases, plan.Options{
		Marker:     snap.Elapsed,
		ShowMarker: true,
	}))
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	snap := s.p.Snapshot()
	cpuBases, memBases := s.p.Bases()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "loadsim %s - profile %q\n\n", snap.Version, snap.Name)
	fmt.Fprintf(w, "uptime            %s\n", snap.Uptime.Round(time.Second))
	fmt.Fprintf(w, "profile elapsed   %s (iteration %d)\n", snap.Elapsed.Round(time.Second), snap.Targets.Iteration)
	fmt.Fprintf(w, "phase             %s (%d/%d, %.0f%% complete)\n", snap.Phase, snap.Targets.PhaseIndex+1, snap.PhaseCount, snap.Targets.Progress*100)
	fmt.Fprintf(w, "ready             %v\n\n", snap.Ready)
	fmt.Fprintf(w, "cpu     target %-10s actual %-10s duty %.2f over %d worker(s)\n",
		units.FormatCores(snap.CPU.TargetCores), units.FormatCores(snap.CPU.ActualCores), snap.CPU.Duty, snap.CPU.Workers)
	fmt.Fprintf(w, "memory  target %-10s rss %-13s allocated %s in %d chunk(s)\n",
		units.FormatBytes(snap.Memory.TargetBytes), units.FormatBytes(float64(snap.Memory.RSSBytes)),
		units.FormatBytes(float64(snap.Memory.AllocatedBytes)), snap.Memory.Chunks)
	fmt.Fprintf(w, "limits  %s\n", snap.Resources.String())
	if snap.Throttling.Available {
		fmt.Fprintf(w, "throttle %.0f/%.0f periods, %.2fs total\n",
			snap.Throttling.ThrottledPeriods, snap.Throttling.Periods, snap.Throttling.ThrottledSeconds)
	}
	fmt.Fprintf(w, "\n%s\n", plan.Render(s.p.Profile(), cpuBases, memBases, plan.Options{
		Marker:     snap.Elapsed,
		ShowMarker: true,
	}))
	fmt.Fprintf(w, "\nendpoints: /healthz /readyz /status /config /plan %s\n", s.opts.MetricsPath)
}
