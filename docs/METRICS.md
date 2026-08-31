# Metrics and endpoints

LoadSim exposes what it was *aiming for* alongside what it *measured*, from
inside the container. That pairing is the point: when the question is whether a
monitoring pipeline is accurate, you need a second opinion that does not come
from the pipeline.

- [Endpoints](#endpoints)
- [Metrics](#metrics)
- [Example queries](#example-queries)
- [Scraping](#scraping)

## Endpoints

| Path | Content type | Purpose |
| --- | --- | --- |
| `/healthz`, `/livez` | text | liveness: always 200 while the process runs |
| `/readyz` | text | readiness: 503 until `startup.ready_delay` has passed, then 200 |
| `/` | text | human readable status plus the profile chart with a "now" marker |
| `/status` | JSON | the full snapshot: targets, measurements, resources, throttling |
| `/plan` | text | the profile chart on its own |
| `/config` | YAML | the effective configuration, with its provenance in a comment |
| `/metrics` | Prometheus text | 37 series, no client library involved |
| `/debug/pprof/{,profile,heap}` | pprof | only with `--pprof` |

`--listen off` disables the server entirely, for cases where a listening port
is unwelcome. The status log line keeps working.

`/status` is the machine-readable version of everything on `/`:

```json
{
  "name": "steady",
  "uptime_seconds": 6.0,
  "profile_elapsed_seconds": 6.0,
  "ready": true,
  "phase": "main",
  "targets": { "cpu_cores": 0.5, "memory_bytes": 268435456, "phase_progress": 1 },
  "cpu": { "target_cores": 0.5, "actual_cores": 0.4985, "duty_cycle": 0.5077, "workers": 1, "saturated": false },
  "memory": { "target_bytes": 268435456, "allocated_bytes": 260046848, "rss_bytes": 269733888, "overhead_bytes": 10087318, "chunks": 62 },
  "resources": { "cpu_limit_cores": 1, "memory_limit_bytes": 536870912, "sources": { "cpu_limit": "cgroup:cpu.max" } },
  "throttling": { "available": true, "periods": 138, "throttled_periods": 3, "throttled_seconds": 0.06 }
}
```

## Metrics

### Identity and profile position

| Metric | Type | Meaning |
| --- | --- | --- |
| `loadsim_info{version,profile,go_version}` | gauge | build and profile identification, always 1 |
| `loadsim_phase{phase}` | gauge | the active phase, always 1; only the current phase is emitted |
| `loadsim_phase_index` | gauge | zero-based index of the active phase |
| `loadsim_phase_count` | gauge | number of phases in the profile |
| `loadsim_phase_progress_ratio` | gauge | progress through the active phase, 0 to 1 |
| `loadsim_profile_elapsed_seconds` | gauge | seconds since the profile clock started |
| `loadsim_profile_total_seconds` | gauge | length of one pass through the profile |
| `loadsim_profile_iteration` | gauge | completed passes, for looping profiles |
| `loadsim_uptime_seconds` | gauge | seconds since process start |
| `loadsim_ready` | gauge | 1 once the readiness gate is open |
| `loadsim_initializing` | gauge | 1 while simulating `startup.init_delay` |

### CPU

| Metric | Type | Meaning |
| --- | --- | --- |
| `loadsim_cpu_target_cores` | gauge | what the profile asks for right now |
| `loadsim_cpu_actual_cores` | gauge | measured process CPU usage, from `getrusage` |
| `loadsim_cpu_duty_cycle_ratio` | gauge | fraction of each slice a worker spins |
| `loadsim_cpu_workers` | gauge | size of the spin pool |
| `loadsim_cpu_saturated` | gauge | 1 when the target is out of reach (throttled, or more cores than workers) |
| `loadsim_cpu_request_cores`, `loadsim_cpu_limit_cores` | gauge | discovered sizing; 0 means unknown |
| `loadsim_cpu_limit_utilization_ratio` | gauge | measured usage / CPU limit |

### Memory

| Metric | Type | Meaning |
| --- | --- | --- |
| `loadsim_memory_target_bytes` | gauge | what the profile asks for right now |
| `loadsim_memory_rss_bytes` | gauge | process RSS, from `/proc/self/statm` |
| `loadsim_memory_allocated_bytes` | gauge | bytes actually held by the engine |
| `loadsim_memory_overhead_bytes` | gauge | resident memory outside the engine (runtime, stacks, binary) |
| `loadsim_memory_cgroup_bytes` | gauge | the container's memory charge, from `memory.current` |
| `loadsim_memory_heap_bytes` | gauge | Go heap in use |
| `loadsim_memory_chunks`, `loadsim_memory_chunk_size_bytes` | gauge | allocation bookkeeping |
| `loadsim_memory_touch_passes_total` | counter | completed passes re-touching every held page |
| `loadsim_memory_request_bytes`, `loadsim_memory_limit_bytes` | gauge | discovered sizing; 0 means unknown |
| `loadsim_memory_limit_utilization_ratio` | gauge | RSS / memory limit |

With `compensate: true` (the default) `target_bytes` describes total RSS, so
`allocated_bytes` sits below it by roughly `overhead_bytes`.

### Container pressure and process

| Metric | Type | Meaning |
| --- | --- | --- |
| `loadsim_cgroup_cpu_periods_total` | counter | CFS periods elapsed |
| `loadsim_cgroup_cpu_throttled_periods_total` | counter | periods in which the container was throttled |
| `loadsim_cgroup_cpu_throttled_seconds_total` | counter | time spent throttled |
| `loadsim_node_cpu_cores`, `loadsim_node_memory_bytes` | gauge | machine capacity |
| `loadsim_goroutines` | gauge | running goroutines |

The three cgroup series are only present when a cgroup could be read.

## Example queries

**Is the pipeline accurate?** LoadSim's own measurement against cAdvisor's:

```promql
  rate(container_cpu_usage_seconds_total{pod=~"loadsim.*", container="loadsim"}[1m])
- on(pod) group_left loadsim_cpu_actual_cores
```

**Is LoadSim doing what it was told?** Anything but a flat zero means the
generator is off target - usually because of throttling:

```promql
loadsim_cpu_target_cores - loadsim_cpu_actual_cores
```

**Memory reported vs held:**

```promql
container_memory_working_set_bytes{pod=~"loadsim.*"} - on(pod) group_left loadsim_memory_rss_bytes
```

**Throttling, as a fraction of time:**

```promql
rate(loadsim_cgroup_cpu_throttled_seconds_total[5m])
```

**Where in the profile is each pod?** Useful when correlating a metric shape
with the phase that produced it:

```promql
loadsim_phase * on(pod) group_left() loadsim_phase_progress_ratio
```

**Right-sizing sanity check** - what fraction of its request is a pod actually
using:

```promql
loadsim_cpu_actual_cores / loadsim_cpu_request_cores
```

**Alert that fires when the generator cannot keep up** (a useful canary for a
node that is oversubscribed):

```yaml
- alert: LoadSimSaturated
  expr: loadsim_cpu_saturated == 1
  for: 2m
  annotations:
    summary: "LoadSim cannot reach its CPU target (throttled or out of CPUs)"
```

## Scraping

With annotations (see `deploy/k8s/deployment.yaml`):

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8080"
  prometheus.io/path: "/metrics"
```

With the Prometheus Operator, apply `deploy/k8s/servicemonitor.yaml`. A short
interval (15s or less) is worth it: for fast-changing profiles the scrape
interval is often exactly what you are trying to measure.

Because targets change continuously, gauges are sampled - not averaged - at
scrape time. If you need the true integral of CPU usage, use the counter your
platform already provides (`container_cpu_usage_seconds_total`) and compare it
with `loadsim_cpu_target_cores` over the same window.
