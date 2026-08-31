# LoadSim

A container that pretends to be a busy application. You describe a CPU and
memory load profile - flat, ramped, spiky, sawtooth, leaking, whatever - and
LoadSim reproduces it accurately inside a Kubernetes pod, so you can test the
things that watch workloads rather than the workloads themselves: metrics
pipelines, dashboards, alert rules, autoscalers and right-sizing
recommendations.

```console
$ loadsim --preset startup-burst
INFO  loadsim 0.1.0 starting (go1.25.14 linux/amd64, GOMAXPROCS=16)
INFO  configuration chain=defaults -> preset:startup-burst -> overrides:preset one_pass=1m30s percent_base=limit phases=2 profile=startup-burst
INFO  resolved resources cpu_request=500m cpu_limit=1.000 memory_request=256.0Mi memory_limit=512.0Mi sources=cpu_limit=cgroup:cpu.max cpu_request=env:LOADSIM_CPU_REQUEST_MILLI memory_limit=cgroup:memory.max memory_request=env:LOADSIM_MEMORY_REQUEST_BYTES
INFO  phase index=1 name=startup starts_at=0s duration=1m30s cpu=1.000->250m ease-out memory=102.4Mi->307.2Mi ease-in-out
INFO  phase index=2 name=steady starts_at=1m30s duration=hold cpu=250m (25%) memory=307.2Mi (60%)
INFO  status phase=startup(1/2) progress=17% elapsed=15s cpu_target=771m cpu_actual=764m cpu_duty=0.39 mem_target=117.6Mi mem_rss=118.2Mi cpu_pct_limit=76% mem_pct_limit=23%
```

Why this instead of `stress-ng` or a `while true` loop: those produce *load*,
not a *profile*. LoadSim's targets are expressed as a percentage of the pod's
own request or limit, it moves between them along a chosen curve, it reports
what it is aiming for next to what it actually achieved, and it does all of
that from a 7MB static image with one config file.

- [Quick start](#quick-start)
- [Configuring a profile](#configuring-a-profile)
- [HTTP endpoints and metrics](#http-endpoints-and-metrics)
- [How accurate is it](#how-accurate-is-it)
- [Publishing the image](#publishing-the-image)
- [Documentation](#documentation)
- [Development](#development)

## Quick start

The published image is `registry.gitlab.com/vkalladath/loadsim:v0.2.0` - public, so it needs
no credentials:

```sh
docker pull registry.gitlab.com/vkalladath/loadsim:v0.2.0
```

Or build it yourself - multi-stage, static, non-root, ~7MB:

```sh
make image                     # docker or podman, whichever is installed
# or: docker build -t loadsim:dev .
```

Run it with pod-like limits and watch it hold 50% of them:

```sh
docker run --rm -p 8080:8080 --cpus 1 --memory 512m \
  registry.gitlab.com/vkalladath/loadsim:v0.2.0 --cpu 50% --memory 50%

curl -s localhost:8080/           # human readable status + the profile chart
curl -s localhost:8080/metrics    # Prometheus exposition
```

Some other shapes, all equivalent whether they come from flags, environment
variables or a file:

```sh
# absolute targets
loadsim --cpu 250m --memory 256Mi

# a built-in profile (loadsim presets lists all ten)
loadsim --preset spiky

# a multi-phase profile on one line
loadsim --phases 'burst:90s:cpu=100%->25%@ease-out:mem=20%->60%;steady:cpu=25%:mem=60%'

# the same thing from the environment, for a Deployment with no ConfigMap
LOADSIM_PHASES='burst:90s:cpu=100%:mem=80%;steady:cpu=25%:mem=60%' loadsim

# a file
loadsim --config examples/startup-burst.yaml
```

Check a profile before you deploy it - `plan` resolves every percentage and
charts the result without generating any load:

```console
$ loadsim plan --preset spiky --cpu-limit 1 --memory-limit 512Mi
profile "spiky": 1 phase(s), one pass 30m, looping
    phases  spikes                                                      
            |___________________________________________________________
CPU (cores)  peak 997m
      997m |-  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  
           |-  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  
      665m |-  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  
           |-  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  
           |-  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  -  
      166m |------------------------------------------------------------
         0 +------------------------------------------------------------
```

On Kubernetes - the manifests already point at the public `v0.2.0` image, so
there is nothing to configure:

```sh
kubectl apply -k deploy/k8s        # ConfigMap + Deployment + Service
kubectl port-forward deploy/loadsim 8080:8080
```

`deploy/k8s/` also has an args-only Deployment (no ConfigMap), a one-shot Job,
an HPA and a ServiceMonitor. See [docs/KUBERNETES.md](docs/KUBERNETES.md).

## Configuring a profile

### The three things you configure

**1. Quantities.** Absolute or relative to the pod's own sizing:

| Written as | Means |
| --- | --- |
| `250m`, `0.25`, `1`, `2cores` | CPU in cores |
| `256Mi`, `1Gi`, `500MB`, `1048576` | memory in bytes |
| `50%` | 50% of the *limit* (or of whatever `percent_base` says) |
| `80%request` | 80% of the request - the sizing case |
| `120%limit` | above the limit: throttling for CPU, an OOM kill for memory |
| `25%node` | 25% of the whole machine, ignoring the pod's sizing |

Requests and limits are discovered automatically: from the downward API
environment variables if the Deployment provides them, otherwise from the
container's cgroup (v2 and v1), otherwise overridable with
`--cpu-limit`/`--memory-limit`. Nothing needs to be told twice.

**2. Phases.** A profile is a list of phases, each with a duration. The last
phase may omit its duration, which means "hold this forever":

```yaml
phases:
  - name: cold-start
    duration: 90s
    cpu: 100% -> 30% @ease-out       # from -> to @curve
    memory: 15% -> 55% @ease-in-out
  - name: steady
    cpu: 25%
    memory: 60%
    jitter: 4%                       # +/- 4% noise on every sample
```

**3. Curves.** How a phase moves from `from` to `to`:

`step` `linear` `ease-in` `ease-out` `ease-in-out` `exp` `log` `sine`
`triangle` `sawtooth` `square` `noise` `random-walk`

The periodic ones (`sine` onwards) oscillate between `from` and `to` with a
configurable `period`, so one phase can hold a whole traffic pattern.
[docs/PROFILES.md](docs/PROFILES.md) charts every curve and lists recipes.

### Where configuration comes from

Layered, lowest priority first, so a preset can be adjusted by a file, a file
by the environment, and the environment by a flag:

```
defaults  <  --preset  <  --config file  <  LOADSIM_CONFIG_YAML  <  environment  <  flags
```

Every flag has an environment variable: `--cpu-workers` is
`LOADSIM_CPU_WORKERS`, `--memory` is `LOADSIM_MEMORY`. Boolean flags are set
bare (`--loop`) and cleared with `=false` (`--loop=false`).
`LOADSIM_CONFIG_YAML` holds a whole config file inline, which is convenient
when you would rather not mount a ConfigMap.

`loadsim config` prints the effective configuration after all the layering,
annotated with where each layer came from - the fastest way to answer "why is
it doing that".

Full key-by-key reference: [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
Annotated example with every option: [examples/reference.yaml](examples/reference.yaml).

### Built-in presets

| Preset | Shape |
| --- | --- |
| `steady` | flat 50% of both limits |
| `startup-burst` | 90s burst decaying to a modest steady state |
| `ramp-up` | S-curve from idle to the limit over 10 minutes |
| `sawtooth` | 5-minute sawtooth between 10% and 90%, looping |
| `spiky` | 15% baseline with a 95% spike every 3 minutes |
| `memory-leak` | flat CPU, memory climbing past the limit until OOM |
| `daily-cycle` | sine wave over a compressed one-hour "day" |
| `oversized` | uses 5% of its CPU request: the downscale case |
| `saturated` | 98% of the CPU limit: sustained throttling |
| `idle` | 10m CPU, 32Mi memory: a control pod |

`loadsim presets show <name>` prints the preset as config-file YAML, ready to
copy and edit.

## HTTP endpoints and metrics

| Path | Purpose |
| --- | --- |
| `/healthz`, `/livez` | liveness: 200 while the process is alive |
| `/readyz` | readiness: 503 until `startup.ready_delay` has passed |
| `/` | human readable status, plus the profile chart with a "now" marker |
| `/status` | the same state as JSON |
| `/plan` | just the chart |
| `/config` | the effective configuration as YAML |
| `/metrics` | Prometheus exposition (no client library, no dependencies) |
| `/debug/pprof/` | only with `--pprof` |

The metrics report both sides of every number - what LoadSim was aiming for and
what it measured - which is exactly what you want when the question is whether
your pipeline is telling the truth:

```promql
# does the pipeline agree with the load generator?
container_cpu_usage_seconds_total{pod=~"loadsim.*"} - loadsim_cpu_actual_cores

# is the pod being throttled?
rate(loadsim_cgroup_cpu_throttled_seconds_total[5m])

# how close to the memory limit?
loadsim_memory_limit_utilization_ratio
```

Full list with descriptions: [docs/METRICS.md](docs/METRICS.md).

## How accurate is it

Measured in a 1-CPU, 512Mi container on an idle host:

| Target | Reported | Notes |
| --- | --- | --- |
| 500m CPU | 491m - 507m | within ~2% once the control loop settles (~1s) |
| 256Mi memory | 257.2Mi RSS | within half an allocation chunk |
| 1.5 cores in a 1-core limit | 1.003 cores, throttling accumulates | the limit wins, as it should |
| memory > limit, `soft_limit: false` | OOM killed, exit 137 | verified on a node without swap |

Two design decisions are worth knowing about:

- **CPU is closed-loop on the whole process.** Workers busy-spin for a fraction
  of each 50ms slice, and a control loop compares the process's real CPU time
  against the target and trims the duty cycle. So `--cpu 500m` means "this
  container uses 500m", including the Go runtime, the HTTP server and page
  faulting - not "the spinners use 500m and everything else is extra".
- **Memory targets mean resident memory.** The engine subtracts its own
  overhead (~10Mi) so that `--memory 256Mi` shows up as 256Mi of RSS rather
  than 266Mi. Pages are filled with pseudo-random bytes and can be re-touched
  periodically, so page deduplication, compression and swap cannot quietly
  reclaim them. Turn compensation off with `--mem-compensate=false` if you want
  the raw "hold exactly N bytes" behaviour.

[docs/DESIGN.md](docs/DESIGN.md) explains both engines and their failure modes.

## Publishing the image

Published (publicly) at `registry.gitlab.com/vkalladath/loadsim:v0.2.0` and `:latest`. Two
scripts build and publish the image; the GitLab pipeline calls exactly the same scripts, so
anything CI does can be reproduced on a laptop.

```sh
scripts/build-image.sh                       # local image, tagged from git
scripts/build-image.sh --push --dry-run      # show what a publish would do

GITLAB_PROJECT=vkalladath/loadsim GITLAB_USER=me GITLAB_TOKEN=glpat-xxxx \
  scripts/build-image.sh --tag v0.3.0 --latest --push

# multi-arch: cross-compiled, so no qemu is needed
scripts/build-image.sh --tag v0.3.0 --platforms linux/amd64,linux/arm64 --push
```

`.gitlab-ci.yml` tests, then builds and pushes to the project's own registry:
merge requests build only, branches get `<branch>` and `<sha>` tags, the default
branch gets multi-arch `latest`, and a `vX.Y.Z` git tag gets a multi-arch
release that is smoke-tested after publication. Credentials come from GitLab's
own variables, so there is nothing to configure.

Details, tokens, pull secrets and troubleshooting: [docs/RELEASING.md](docs/RELEASING.md).

## Documentation

| Document | Contents |
| --- | --- |
| [docs/CONFIGURATION.md](docs/CONFIGURATION.md) | every config key, flag, environment variable and the phase DSL |
| [docs/PROFILES.md](docs/PROFILES.md) | curve gallery and ready-made scenarios |
| [docs/METRICS.md](docs/METRICS.md) | metrics, endpoints and example PromQL |
| [docs/KUBERNETES.md](docs/KUBERNETES.md) | deploying, downward API, HPA/VPA/OOM/throttling experiments |
| [docs/DESIGN.md](docs/DESIGN.md) | how the load engines work and why |
| [docs/RELEASING.md](docs/RELEASING.md) | building, tagging and publishing to the GitLab registry |
| [examples/](examples/) | annotated profiles, including a full reference |
| [deploy/k8s/](deploy/k8s/) | Deployment, ConfigMap, Service, Job, HPA, ServiceMonitor |

## Development

No local Go toolchain is required - every target has a containerised twin.

```sh
make check            # gofmt, go vet, go test (needs local Go)
make test-container   # the same, inside a golang image
make image            # build the container image
make image-push       # build and push to registry.gitlab.com/vkalladath/loadsim
make run-image        # run it with a 1 CPU / 512Mi limit
make plan PROFILE=examples/spiky.yaml
```

Layout:

```
cmd/loadsim          CLI: run, plan, config, presets, version
internal/units       quantity parsing ("500m", "75%request") and resolution
internal/profile     phases, curves, evaluation, the one-line phase DSL
internal/config      layering of defaults/preset/file/env/flags, validation
internal/resources   request and limit discovery (downward API, cgroup v1/v2)
internal/cpuload     duty-cycle CPU generator with a feedback control loop
internal/memload     resident-memory holder with RSS compensation
internal/runner      the tick loop that drives the engines from the profile
internal/server      HTTP endpoints and the Prometheus exposition
internal/plan        ASCII charting of a profile
internal/logging     text and JSON status output
scripts/             build-image.sh, push-image.sh (used by the pipeline too)
```

The only dependency is `gopkg.in/yaml.v3`; the Prometheus exposition is written
by hand to keep the image small and the build offline-friendly.

No licence file is included yet - add one before publishing this anywhere.
