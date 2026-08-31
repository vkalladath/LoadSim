# Design notes

How LoadSim produces load, why it is built this way, and where it stops being
accurate.

- [Shape of the program](#shape-of-the-program)
- [The CPU engine](#the-cpu-engine)
- [The memory engine](#the-memory-engine)
- [The profile clock](#the-profile-clock)
- [Accuracy and its limits](#accuracy-and-its-limits)
- [Why not an existing tool](#why-not-an-existing-tool)
- [Dependencies and image](#dependencies-and-image)

## Shape of the program

```
config  ->  profile  ->  runner  ->  cpuload / memload
   |            |           |
   |            |           +-> status snapshot -> server (/metrics, /status) + logs
   |            +-> plan (ASCII chart)
   +-> resources (downward API, cgroup)
```

- `internal/units` parses quantities and resolves relative ones against a set
  of bases (limit, request, node).
- `internal/profile` compiles phase specs into segments (from, to, curve,
  period, jitter) and evaluates them at a given elapsed time. It is pure: no
  clocks, no I/O, so it is trivially testable and the same code drives both the
  live run and `loadsim plan`.
- `internal/runner` ticks every 100ms, evaluates the profile, pushes targets
  into the two engines, and publishes an immutable snapshot for the HTTP
  handlers and the status log.
- The engines own no policy: they are told a number and try to hit it.

## The CPU engine

**Duty cycling, not core pinning.** A pool of worker goroutines repeats a fixed
slice (50ms by default): spin for `duty x slice`, sleep for the rest. Fractional
targets fall out naturally - 300m is one worker spinning 30% of each slice - and
a target above one core is spread over several workers.

The pool is sized from the profile's *peak* target with 25% headroom
(`ceil(peak x 1.25)`), capped by `GOMAXPROCS`. Worker phases are staggered
across the slice so the pool's aggregate load is smooth rather than arriving in
synchronised bursts; `--cpu-stagger=false` turns that off if you want the bursty
version.

**Closed loop on the whole process.** Every control interval (500ms) the engine
reads the process's real CPU time with `getrusage(RUSAGE_SELF)`, computes cores
used since the last tick, and adjusts a scale factor:

```
err    = (target - actual) / target
scale *= 1 + gain x err          # gain 0.5, scale clamped to [0.25, 4]
duty   = clamp(target x scale / workers, 0, 1)
```

Two consequences worth understanding:

- `--cpu 500m` means *this container* uses 500m, including the Go runtime, the
  HTTP server, GC and page faulting - not "the spinners use 500m plus
  overhead". That is what makes the number comparable with a container CPU
  metric.
- If the container is throttled, or the target exceeds the available CPUs, the
  loop pushes the duty cycle to 1.0 and gives up honestly:
  `loadsim_cpu_saturated` goes to 1 rather than the tool pretending.

`--cpu-feedback=false` runs open loop with a fixed duty cycle, which is useful
when you want a known *spin* pattern rather than a known *usage* number.

The work itself is a loop the compiler cannot elide (the accumulator is
published to an atomic each iteration batch). Three instruction mixes are
available - `int` (default), `float`, `sqrt` - because different mixes stress
different parts of a CPU, and a `sqrt`-heavy loop behaves differently under
frequency scaling and SMT contention.

## The memory engine

**Chunks, faulted in.** Memory is held as a list of equally sized chunks (4Mi
by default). Growing appends chunks and writes every page; shrinking drops
chunks and, at most every `release_interval`, calls `debug.FreeOSMemory()` so
the pages actually return to the OS and RSS follows the target back down.

**Pseudo-random fill.** Chunks are filled with a cheap xorshift stream. Zeroed
pages are attractive to page deduplication, zswap/zram compression and balloon
drivers - all of which would make the memory metric lie. `fill: zero` is
available when you want the cheapest possible allocation and do not care.

**RSS compensation.** The engine continuously estimates its own overhead as
`RSS - held`, smoothed with an EMA, and holds `target - overhead`. So
`--memory 256Mi` produces about 256Mi of *resident memory*, not 256Mi plus ~10Mi
of Go runtime. `--mem-compensate=false` reverts to holding exactly the target.

**Touching.** `touch_interval` rewrites one byte per page of everything held.
This keeps pages hot so the kernel cannot reclaim or swap them, which is what
makes a deliberate OOM reliable on a node with swap. It costs very little CPU
(one write per 4Ki page).

**The GC soft limit.** By default `debug.SetMemoryLimit` is set to 90% of the
container's memory limit. Without it, a heap-growth spike can OOM the container
*before* the profile intends to. It does not prevent a deliberate OOM: held
chunks are live, so no amount of GC pressure can free them. The `memory-leak`
preset disables it anyway, to make the kill prompt.

## The profile clock

`startup.init_delay` shifts the start of the profile clock, and during that
window both targets are pinned to zero - a container doing framework
initialisation, not work. `startup.ready_delay` gates `/readyz` independently,
so probe behaviour and load shape can be varied separately.

When the last phase has no duration, the profile holds it forever (`hold_last`).
With `hold_last: false` the process exits cleanly when the profile ends, which
is what a Job wants. `loop: true` wraps the clock modulo the profile length and
reports the iteration count.

Periodic curves are driven by *elapsed time within the phase*, not by phase
progress, so a `sine` with a 5m period behaves the same in a 20m phase as in a
1h phase. Random curves (`noise`, `random-walk`) resample once per period and
hold the value in between, which keeps them legible in a chart and in a metric.
Their seed is derived from the phase name, the resource and the profile's
`seed`, so a run is reproducible without CPU and memory moving in lockstep.

## Accuracy and its limits

Measured in a 1-CPU, 512Mi container on an otherwise idle 16-core host:

| Target | Result |
| --- | --- |
| 500m CPU, steady | 491m - 507m (about +/- 2%), settling within ~1s |
| 500m CPU, first second | may overshoot while the control loop converges |
| 1.5 cores against a 1-core limit | 1.003 cores, throttling accumulates, `saturated=1` |
| 256Mi memory | 257.2Mi RSS |
| Ramp of 100Mi/s | tracks within one chunk (4Mi) |

Known limits:

- **Chunk granularity.** Memory targets are rounded to the nearest chunk. Use
  `chunk_size: 1Mi` for finer control at the cost of more bookkeeping.
- **Fast ramps.** During a steep ramp the CPU control loop lags by up to one
  control interval, so a target that halves in 500ms will overshoot briefly.
  Lower `control_interval` (and `slice`) if that matters.
- **Small targets.** Below about 20m the `getrusage` resolution and the Go
  runtime's own housekeeping dominate; the feedback loop deliberately ignores
  samples that small.
- **Throttling interacts with the slice.** A 50ms slice inside a 100ms CFS
  period means a duty cycle of 1.0 can bunch usage into part of the period.
  Raising `slice` to 100ms makes throttling patterns more pronounced; lowering
  it smooths them.
- **Sub-scrape spikes.** LoadSim will faithfully produce a 2s spike; whether
  anything else notices is the experiment.
- **`memory.current` vs RSS.** The cgroup charge includes page cache and kernel
  memory, so it sits slightly above RSS. Both are exported; the OOM killer
  cares about the cgroup number.

## Why not an existing tool

`stress-ng`, `stress`, `cpuburn` and a `while true` loop all produce load. What
they do not produce is a *profile*: a target expressed as a share of the pod's
own request or limit, moving along a chosen curve, reporting target next to
achieved, and configurable from a single YAML file, a flag or an environment
variable. Wrapping them in a shell script to fake that means reimplementing the
interesting parts (unit parsing, request/limit discovery, curves, phase
scheduling, metrics) in bash, and the result cannot tell you how accurate it
was.

The one thing LoadSim deliberately does not do is simulate *other* resource
pressure - disk I/O, network, page cache, huge pages, NUMA effects. For those,
`stress-ng` remains the right tool, and it can run alongside LoadSim in a
sidecar.

## Dependencies and image

The only module dependency is `gopkg.in/yaml.v3`. The Prometheus exposition is
about 80 lines of hand-written text formatting: a client library would add a
large dependency tree for output that is trivially generated, and keeping the
build offline-friendly matters for a tool that is often built in a hurry inside
a cluster's own CI.

The image is a multi-stage build: `golang:alpine` compiles a static
`CGO_ENABLED=0` binary, which is copied into `scratch` and run as uid 65532.
That is ~7MB, which keeps image pull time out of the startup measurements you
are trying to make. `make image-shell` produces the same binary on Alpine when
you want a shell for debugging.
