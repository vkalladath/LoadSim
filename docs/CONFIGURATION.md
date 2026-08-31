# Configuration reference

Everything LoadSim does is controlled by one configuration model. It can be
expressed as a YAML file, as command line flags, as environment variables, or
as any mixture of the three.

- [Precedence](#precedence)
- [Quantities](#quantities)
- [Profiles and phases](#profiles-and-phases)
- [The inline phase DSL](#the-inline-phase-dsl)
- [Config file keys](#config-file-keys)
- [Flags and environment variables](#flags-and-environment-variables)
- [Resource discovery](#resource-discovery)
- [Subcommands](#subcommands)
- [Validation and warnings](#validation-and-warnings)

## Precedence

```
defaults  <  --preset  <  --config file  <  LOADSIM_CONFIG_YAML  <  environment  <  flags
```

Each layer only overrides the keys it actually sets, so `--preset spiky
--cpu-limit 2` means "the spiky profile, but sized against a 2 core limit".

Two special cases keep the layering intuitive:

- `--phases` replaces the phase list from a preset or file entirely.
- `--cpu`/`--memory` (the single-phase shorthand) also replace the phase list,
  because otherwise a preset's phases would silently win over the flag you just
  typed.

`loadsim config` prints the merged result, with a comment naming each layer:

```console
$ loadsim config --preset spiky --cpu-limit 2
# effective configuration (defaults -> preset:spiky -> overrides:cpu-limit,preset)
# resources: cpu request=- limit=2.000 / memory request=- limit=512Mi / node cpus=16 mem=62076Mi cgroup=v2
name: spiky
...
```

## Quantities

CPU quantities resolve to cores, memory quantities to bytes.

| Form | Example | Meaning |
| --- | --- | --- |
| millicores | `250m`, `1500m` | 0.25 / 1.5 cores |
| cores | `0.25`, `2`, `2c`, `2cores` | cores |
| binary bytes | `512Mi`, `1Gi`, `1GiB` | 1 Mi = 1048576 |
| decimal bytes | `500MB`, `1G`, `1024` | 1 MB = 1000000; a bare number is bytes |
| percentage | `50%` | 50% of the base named by `percent_base` |
| explicit base | `50%limit`, `80%request`, `25%node` | 50% of the limit / 80% of the request / 25% of the machine |

Aliases: `%l`, `%lim`, `%limit`, `%limits`; `%r`, `%req`, `%request`;
`%n`, `%node`, `%total`, `%host`, `%machine`. Percentages above 100% are
allowed and are how you provoke throttling and OOM kills.

**Fallback.** If a relative quantity names a base that is not known - a pod
with no limits, say - LoadSim falls back to the next best number rather than
failing: `limit -> request -> node`, `request -> limit -> node`,
`node -> limit -> request`. If nothing is known the target resolves to 0 and a
warning is logged.

Durations accept Go syntax (`500ms`, `30s`, `1m30s`, `2h`), plus `2d` for days
and a bare number for seconds.

## Profiles and phases

A profile is an ordered list of phases. Each phase has a duration and, for each
resource, a *segment*: a start value, an end value and a curve between them.

```yaml
phases:
  - name: warmup            # optional; defaults to phase-1, phase-2, ...
    duration: 90s           # required, except on the last phase
    cpu: 10% -> 90% @ease-in-out
    memory:
      from: 64Mi
      to: 512Mi
      curve: linear
      jitter: "3%"
    repeat: 1               # expand into N consecutive copies
    jitter: "5%"            # default noise for both resources in this phase
```

Rules:

- Only the **last** phase may omit `duration`; that means "hold these targets
  until the pod is deleted". Any other phase without a duration is an error.
- `hold_last: true` (the default) holds the last phase forever when the profile
  ends. `hold_last: false` makes the process **exit** when the profile ends,
  which is what a Kubernetes Job wants.
- `loop: true` restarts the profile from phase one. Every phase, including the
  last, then needs a duration.
- If `from` is omitted it defaults to `to` (and vice versa), so
  `cpu: {to: 50%}` is a constant.
- The default curve is `step` when `from` equals `to`, and `linear` otherwise.

Segments accept three notations, which are exactly equivalent:

```yaml
cpu: 50%                                   # constant
cpu: 10% -> 90% @ease-in-out               # inline
cpu: {from: 10%, to: 90%, curve: ease-in-out}
```

The inline form is `<from> -> <to> @<curve>[/<period>][^<exponent>]`; `=>` and
`..` also work as the arrow. Curves are listed in
[PROFILES.md](PROFILES.md).

There is also a top-level shorthand for the common "one constant phase" case,
used when `phases` is absent:

```yaml
cpu: 60%
memory: 70%
```

## The inline phase DSL

`--phases` (and `LOADSIM_PHASES`) takes a whole profile on one line, which is
what makes an args-only or env-only Deployment practical.

```
phases := phase (";" phase)*
phase  := field (":" field)*
field  := NAME | DURATION | key "=" value
key    := cpu|c | memory|mem|m|ram | duration|dur|for|time|t | repeat|x | jitter|j | name|n
value  := <the inline segment form: 10%->90%@ease-in/2m>
```

The first bare field is the phase name; a bare field that parses as a duration
is the duration. Examples:

```sh
# two phases: a burst that decays, then a steady tail
--phases 'burst:90s:cpu=100%->25%@ease-out:mem=20%->60%;steady:cpu=25%:mem=60%'

# one phase, an hour long, oscillating every five minutes with noise
--phases 'wave:1h:cpu=20%->90%@sine/5m:mem=50%:jitter=5%'

# three identical 2-minute bursts
--phases 'burst:2m:repeat=3:cpu=95%:mem=70%'
```

## Config file keys

The annotated version of this table, ready to copy, is
[examples/reference.yaml](../examples/reference.yaml).

### Top level

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | `loadsim` | run name, in logs, `/status` and `loadsim_info` |
| `description` | - | free-form documentation |
| `listen` | `:8080` | HTTP address; `off` disables the server |
| `metrics_path` | `/metrics` | Prometheus path |
| `pprof` | `false` | expose `/debug/pprof` |
| `loop` | `false` | restart the profile when it ends |
| `hold_last` | `true` | hold the last phase forever; `false` exits |
| `jitter` | `0%` | default noise applied to every sample |
| `seed` | `0` | fix the RNG for reproducible jitter/random curves |
| `percent_base` | `limit` | what a bare `%` means: `limit`, `request`, `node` |
| `exit_after` | `0` | stop the process after this long |
| `cpu`, `memory` | - | single-phase shorthand (ignored when `phases` is set) |
| `phases` | - | the profile |

### `resources` - what percentages resolve against

| Key | Default | Meaning |
| --- | --- | --- |
| `detect` | `true` | read the downward API env vars and the cgroup |
| `cpu.request`, `cpu.limit` | - | explicit override, e.g. `500m`, `2` |
| `memory.request`, `memory.limit` | - | explicit override, e.g. `256Mi`, `1Gi` |

### `startup` - simulated slow starts

| Key | Default | Meaning |
| --- | --- | --- |
| `init_delay` | `0` | idle before the profile clock starts; targets stay at 0 |
| `ready_delay` | `0` | `/readyz` returns 503 for this long after start |

### `logging`

| Key | Default | Meaning |
| --- | --- | --- |
| `interval` | `15s` | status line interval; `0` is quiet |
| `format` | `text` | `text` or `json` |

Phase transitions are always logged, regardless of `interval`.

### `engine.cpu`

| Key | Default | Meaning |
| --- | --- | --- |
| `workers` | `0` | spinning goroutines; 0 derives from the profile's peak target |
| `slice` | `50ms` | duty-cycle period per worker |
| `control_interval` | `500ms` | how often measured usage is compared to the target |
| `feedback` | `true` | correct the duty cycle from measured process CPU |
| `gain` | `0.5` | correction strength per control tick |
| `work` | `int` | instruction mix: `int`, `float`, `sqrt` |
| `stagger` | `true` | spread worker duty cycles across the slice |
| `max_procs` | `0` | override `GOMAXPROCS` |

### `engine.memory`

| Key | Default | Meaning |
| --- | --- | --- |
| `chunk_size` | `4Mi` | allocation granularity; the accuracy floor |
| `interval` | `200ms` | reconcile interval |
| `max_step` | - | cap on allocation per tick, e.g. `64Mi`, to model a slow allocator |
| `touch_interval` | `0` | re-touch every held page this often; defeats swap and reclaim |
| `release_interval` | `2s` | minimum interval between returning freed pages to the OS |
| `release_threshold` | `32Mi` | smallest net drop below the high-water mark worth a forced release; forcing one is stop-the-world, so small oscillations are left to the ordinary GC |
| `fill` | `random` | `random` (incompressible) or `zero` (cheap, dedupable) |
| `soft_limit` | `true` | tell the Go GC about the memory limit to avoid accidental OOM |
| `soft_limit_fraction` | `0.9` | soft limit = fraction x memory limit |
| `compensate` | `true` | targets mean total RSS, not just bytes held by the engine |

## Flags and environment variables

Every flag has an environment variable: uppercase the name, replace `-` with
`_`, prefix `LOADSIM_`. So `--mem-touch-interval 30s` is
`LOADSIM_MEM_TOUCH_INTERVAL=30s`. Flags win over environment variables.

Boolean flags follow Go's convention: bare to enable (`--loop`), `=false` to
disable (`--loop=false`). `--loop false` does **not** work.

### Profile selection

| Flag | Env | Notes |
| --- | --- | --- |
| `--config` | `LOADSIM_CONFIG` | path to a YAML file |
| - | `LOADSIM_CONFIG_YAML` | a whole config file, inline |
| `--preset` | `LOADSIM_PRESET` | built-in profile; see `loadsim presets` |
| `--phases` | `LOADSIM_PHASES` | inline profile DSL |
| `--cpu`, `--memory` | `LOADSIM_CPU`, `LOADSIM_MEMORY` | single-phase shorthand |
| `--name` | `LOADSIM_NAME` | run name |
| `--percent-base` | `LOADSIM_PERCENT_BASE` | `limit`, `request`, `node` |
| `--jitter`, `--seed` | `LOADSIM_JITTER`, `LOADSIM_SEED` | noise and reproducibility |
| `--loop`, `--hold-last` | `LOADSIM_LOOP`, `LOADSIM_HOLD_LAST` | end-of-profile behaviour |
| `--exit-after` | `LOADSIM_EXIT_AFTER` | stop after a duration |

### Sizing

| Flag | Env |
| --- | --- |
| `--cpu-request`, `--cpu-limit` | `LOADSIM_CPU_REQUEST`, `LOADSIM_CPU_LIMIT` |
| `--memory-request`, `--memory-limit` | `LOADSIM_MEMORY_REQUEST`, `LOADSIM_MEMORY_LIMIT` |
| `--detect-resources` | `LOADSIM_DETECT_RESOURCES` |

### Server, logging, startup

| Flag | Env |
| --- | --- |
| `--listen`, `--metrics-path`, `--pprof` | `LOADSIM_LISTEN`, `LOADSIM_METRICS_PATH`, `LOADSIM_PPROF` |
| `--log-interval`, `--log-format` | `LOADSIM_LOG_INTERVAL`, `LOADSIM_LOG_FORMAT` |
| `--init-delay`, `--ready-delay` | `LOADSIM_INIT_DELAY`, `LOADSIM_READY_DELAY` |

### Engine tuning

| Flag | Env |
| --- | --- |
| `--cpu-workers`, `--cpu-slice`, `--cpu-control-interval` | `LOADSIM_CPU_WORKERS`, ... |
| `--cpu-feedback`, `--cpu-gain`, `--cpu-work`, `--cpu-stagger` | `LOADSIM_CPU_FEEDBACK`, ... |
| `--max-procs` | `LOADSIM_MAX_PROCS` |
| `--mem-chunk-size`, `--mem-interval`, `--mem-max-step` | `LOADSIM_MEM_CHUNK_SIZE`, ... |
| `--mem-touch-interval`, `--mem-release-interval`, `--mem-release-threshold`, `--mem-fill` | `LOADSIM_MEM_TOUCH_INTERVAL`, ... |
| `--mem-soft-limit`, `--mem-soft-limit-fraction`, `--mem-compensate` | `LOADSIM_MEM_SOFT_LIMIT`, ... |

`loadsim help` prints the authoritative list, with each flag's environment
variable shown next to it.

## Resource discovery

Relative targets need to know the container's request and limit. LoadSim looks,
in order:

1. **Explicit configuration** - `resources.*` in the file, or
   `--cpu-limit`/`--memory-limit` and friends.
2. **Environment variables**, normally injected with the downward API:

   | Variable | Value |
   | --- | --- |
   | `LOADSIM_CPU_REQUEST_MILLI`, `LOADSIM_CPU_LIMIT_MILLI` | plain integer millicores (`resourceFieldRef` with `divisor: 1m`) |
   | `LOADSIM_MEMORY_REQUEST_BYTES`, `LOADSIM_MEMORY_LIMIT_BYTES` | plain integer bytes (`divisor: "1"`) |
   | `LOADSIM_CPU_REQUEST`, `LOADSIM_CPU_LIMIT` | suffixed, e.g. `500m`, `2` |
   | `LOADSIM_MEMORY_REQUEST`, `LOADSIM_MEMORY_LIMIT` | suffixed, e.g. `256Mi` |

3. **The container's cgroup** - `cpu.max` and `memory.max` on cgroup v2,
   `cpu.cfs_quota_us`/`cpu.cfs_period_us` and `memory.limit_in_bytes` on v1.
   The CPU *request* is estimated from `cpu.weight`/`cpu.shares`, and is
   discarded when it contradicts the limit or the machine size, because
   container runtimes often leave the default weight in place.
4. **The machine**, for `%node` only: `runtime.NumCPU()` and
   `/proc/meminfo`.

The resolved numbers and where each came from are logged at startup and
exposed on `/status`:

```json
"resources": {
  "cpu_limit_cores": 1,
  "memory_limit_bytes": 536870912,
  "sources": {
    "cpu_limit": "cgroup:cpu.max",
    "memory_request": "env:LOADSIM_MEMORY_REQUEST_BYTES"
  }
}
```

## Subcommands

| Command | Purpose |
| --- | --- |
| `loadsim run` (default) | generate load |
| `loadsim plan` | chart the profile and exit; `--width`, `--height`, `--window` |
| `loadsim config` | print the effective configuration as YAML and exit |
| `loadsim presets` | list built-in profiles; `presets show <name>` prints one |
| `loadsim version` | version and Go build info |
| `loadsim help` | usage and the full flag list |

## Validation and warnings

Configuration errors are fatal and specific: an unknown curve names the known
ones, a bad quantity is quoted back, an unknown YAML key is rejected outright
(so a typo like `memroy:` fails instead of being ignored).

Warnings are logged but do not stop the run:

- peak CPU target above the CPU limit - expect CFS throttling
- peak CPU target above the number of visible CPUs - the target is unreachable
- peak memory target within 5% of the memory limit - expect an OOM kill
- both targets resolving to zero - usually a `percent_base` pointing at a
  request or limit that is not set
- top-level `cpu`/`memory` ignored because `phases` is set
- `loop` ignored because the profile is a single open-ended phase
