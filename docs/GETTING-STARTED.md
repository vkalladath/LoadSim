# Getting started with LoadSim on Kubernetes

LoadSim is a small program that **pretends to be a busy application**. You tell
it how much CPU and memory to use, and it uses exactly that much - steadily, in
bursts, climbing, whatever you ask for.

That is useful when you want to test the *things that watch* pods - monitoring
dashboards, alerts, capacity reports, autoscaling, right-sizing advice - without
having to find a real application that misbehaves in the way you need.

This page assumes you can copy and paste commands, and that you have a cluster
you can create things in. It assumes **no Kubernetes experience**; the terms you
need are explained as they come up.

- [Before you start](#before-you-start)
- [1. Deploy it](#1-deploy-it)
- [2. See what it is doing](#2-see-what-it-is-doing)
- [3. Change the load](#3-change-the-load)
- [4. High memory usage](#4-high-memory-usage)
- [5. Clean up](#5-clean-up)
- [Optional: run it on your laptop instead](#optional-run-it-on-your-laptop-instead)
- [When something looks wrong](#when-something-looks-wrong)
- [Where to go next](#where-to-go-next)

## Before you start

You need `kubectl` and a cluster. Nothing has to be built or downloaded: the
image is published and public.

> **Check which cluster you are pointed at.** Everything below creates things in
> whatever cluster `kubectl` is currently configured for, which may be a real
> one:
>
> ```sh
> kubectl config current-context
> ```
>
> If that is not a cluster you are happy to create things in, stop and switch
> with `kubectl config use-context <name>`. Everything here goes into its own
> namespace and is deleted again at the end, but the safest cluster to learn in
> is one nobody else is using.

Three terms, and then you have enough:

- A **pod** is one or more containers running together. LoadSim is one container
  in one pod.
- A **request** is what a container is promised; a **limit** is the most it may
  use. LoadSim reads its own request and limit from the cluster, and its
  percentages refer to them. This is the one idea that makes the tool convenient.
- A **namespace** is a folder that keeps things separate, so cleaning up is one
  command.

## 1. Deploy it

The deployment files live in the repository, so clone it first:

```sh
git clone https://github.com/vkalladath/LoadSim.git
cd LoadSim

kubectl create namespace loadsim-demo
kubectl apply -k deploy/k8s -n loadsim-demo
```

That creates three things:

| Thing | What it is |
| --- | --- |
| ConfigMap `loadsim-profile` | the load profile - the shape of the load, as YAML |
| Deployment `loadsim` | the pod that runs it |
| Service `loadsim` | a stable address, so Prometheus can scrape it |

The pod **requests** `500m` CPU and `256Mi` memory, and is **limited** to `1`
CPU and `512Mi`. Watch it start:

```sh
kubectl get pods -n loadsim-demo -w
```

`STATUS` goes `ContainerCreating`, then `Running`, within a few seconds. `READY`
stays at `0/1` for the first 20 seconds after that: the profile deliberately
delays readiness, so that rollouts and readiness probes behave like a real
application's. Press `Ctrl-C` to stop watching.

## 2. See what it is doing

LoadSim prints a status line every 15 seconds saying what it aimed for and what
it measured:

```sh
kubectl logs -n loadsim-demo deploy/loadsim --tail=4
```

```
INFO  resolved resources cpu_request=500m cpu_limit=1.000 memory_request=256.0Mi memory_limit=512.0Mi sources=cpu_limit=cgroup:cpu.max cpu_request=env:LOADSIM_CPU_REQUEST_MILLI ...
INFO  phase index=1 name=cold-start starts_at=0s duration=1m30s cpu=1.000->300m ease-out memory=76.8Mi->281.6Mi ease-in-out
INFO  phase index=2 name=steady starts_at=1m30s duration=hold cpu=250m (25%) memory=307.2Mi (60%)
INFO  status phase=steady(2/2) progress=100% elapsed=2m45s cpu_target=249m cpu_actual=256m cpu_duty=0.13 mem_target=307.2Mi mem_rss=306.4Mi cpu_pct_limit=26% mem_pct_limit=60% throttled=0.1s
```

Read the last line as: *"I was aiming for 249 millicores of CPU and 307.2 MiB of
memory; I measured 256 millicores and 306.4 MiB."* (The CPU target moves by a few
millicores because this profile adds 4% noise, so the load is not suspiciously
flat.)

`250m` means 250 millicores, a quarter of a CPU core. CPU is usually written
this way: `500m` is half a core, `2` is two whole cores.

**Why both numbers are printed:** if your monitoring says this pod is using
120 millicores while LoadSim says 248, then your monitoring - not the load - is
the thing worth investigating. That comparison is the whole point of the tool.

Two other ways to look at the same run:

```sh
# the cluster's own view (needs metrics-server installed)
kubectl top pod -n loadsim-demo
```

The `CPU` and `MEMORY` columns should land close to the `cpu_target` and
`mem_target` in the log. If they do not, that gap is a finding.

```sh
# LoadSim's own status page and metrics
kubectl port-forward -n loadsim-demo deploy/loadsim 8080:8080
```

With that running, open <http://localhost:8080> in a browser:

```
loadsim - profile "startup-burst"

uptime            2m55s
profile elapsed   2m55s (iteration 0)
phase             steady (2/2, 100% complete)
ready             true

cpu     target 248m       actual 257m       duty 0.13 over 2 worker(s)
memory  target 307.2Mi    rss 307.1Mi       allocated 296.0Mi in 74 chunk(s)
limits  cpu request=0.500 limit=1.000 / memory request=256Mi limit=512Mi / node cpus=16
throttle 3/1748 periods, 0.11s total

...the profile chart follows.
```

`http://localhost:8080/metrics` is the same information in the format
Prometheus and most monitoring systems scrape. Press `Ctrl-C` to end the
port-forward.

## 3. Change the load

There are two ways, and it is worth knowing which to use when.

**For a quick experiment, set environment variables.** Every setting has one,
and they override the profile in the ConfigMap. `kubectl set env` also restarts
the pod for you:

```sh
kubectl set env deploy/loadsim -n loadsim-demo LOADSIM_CPU=90% LOADSIM_MEMORY=40%
```

That is 90% of the pod's CPU limit - 900 millicores - and 40% of its memory
limit. Watch the effect:

```sh
kubectl logs -n loadsim-demo deploy/loadsim --tail=2 -f
```

**For the profile you actually want to keep, edit the ConfigMap:**

```sh
kubectl edit configmap loadsim-profile -n loadsim-demo
kubectl rollout restart deploy/loadsim -n loadsim-demo     # picks up the change
```

To undo an environment variable, name it with a trailing `-`:

```sh
kubectl set env deploy/loadsim -n loadsim-demo LOADSIM_CPU- LOADSIM_MEMORY-
```

Two things that will save you a confused half hour:

- **Set `LOADSIM_CPU` and `LOADSIM_MEMORY` together.** Either one on its own
  replaces the whole profile, so setting only `LOADSIM_MEMORY` leaves the CPU
  target at **zero**.
- **`LOADSIM_PRESET` has no effect on this Deployment.** A mounted profile file
  outranks a preset, and this Deployment mounts one. Use `LOADSIM_PHASES` (below)
  or edit the ConfigMap. Presets do work on the Deployment in
  `deploy/k8s/deployment-args-only.yaml`, which mounts nothing.

### Shapes, not just levels

`LOADSIM_PHASES` describes a sequence of stages on one line:

```sh
kubectl set env deploy/loadsim -n loadsim-demo \
  LOADSIM_PHASES='warmup:2m:cpu=10%->90%:mem=20%->60%;steady:cpu=30%:mem=60%'
```

Read it as two stages separated by `;`: a stage named `warmup` lasting `2m`
where CPU climbs from 10% to 90% of the limit, then a stage named `steady` with
no time limit that holds 30% and 60%.

### Preview a shape before deploying it

`plan` draws a profile and exits without generating any load. Run it as a
throwaway pod:

```sh
kubectl run loadsim-plan -n loadsim-demo --rm -it --restart=Never \
  --image=registry.gitlab.com/vkalladath/loadsim:v0.2.0 \
  -- plan --preset memory-leak --cpu-limit 1 --memory-limit 512Mi
```

```
warning: peak memory target 614.4Mi is within 5% of (or above) the memory limit 512.0Mi; expect an OOM kill
profile "memory-leak": 2 phase(s), one pass 30m, then holds the last phase
    phases  leak                                                   past-the-limit
            |______________________________________________________|______________
Memory  peak 614.4Mi
   614.4Mi |                                                --------##############
           |                                         --------#####################
           |                                  --------############################
           |                           --------###################################
   368.6Mi |                    --------##########################################
           |             --------#################################################
           |      --------########################################################
           |-------###############################################################
           |######################################################################
    61.4Mi |######################################################################
         0 +----------------------------------------------------------------------

phases:
   1. leak             30m      at 0       cpu 200m   memory 128.0Mi->614.4Mi linear
   2. past-the-limit   hold     at 30m     cpu 200m   memory 614.4Mi
```

## 4. High memory usage

Memory is where the interesting failure modes are, so it gets its own section.
All of these assume the pod deployed in step 1, with a `512Mi` memory limit.

### A constant high level

85% of the pod's memory limit, about 435 MiB:

```sh
kubectl set env deploy/loadsim -n loadsim-demo LOADSIM_CPU=20% LOADSIM_MEMORY=85%
```

Confirm it landed:

```sh
kubectl logs -n loadsim-demo deploy/loadsim --tail=1
# INFO status phase=main(1/1) cpu_target=200m cpu_actual=200m mem_target=435.2Mi
#      mem_rss=436.3Mi cpu_pct_limit=20% mem_pct_limit=85%
```

`mem_rss` is resident memory - the number `kubectl top`, cAdvisor and most
monitoring agents report. LoadSim writes to every page it holds, so this is real
resident memory, not a reservation.

### An exact amount instead of a percentage

```sh
kubectl set env deploy/loadsim -n loadsim-demo LOADSIM_CPU=20% LOADSIM_MEMORY=400Mi
```

### More memory than the limit allows

`512Mi` caps what the pod may use, so `LOADSIM_MEMORY=2Gi` cannot work. Raise
the pod's own limit and the percentage follows automatically:

```sh
kubectl set resources deploy/loadsim -n loadsim-demo \
  --requests=memory=1Gi --limits=memory=2Gi
```

The profile still says `85%`, and the pod now targets about 1.7 GiB. Nothing
about the load definition changed - that is the point of writing profiles as
percentages.

Two things to keep in mind: the node needs room for the bigger request, or the
pod will sit in `Pending`; and a pod cannot be resized in place on most
clusters, so this restarts it.

### Memory that climbs, then holds

```sh
kubectl set env deploy/loadsim -n loadsim-demo \
  LOADSIM_PHASES='fill:3m:cpu=20%:mem=20%->85%;hold:cpu=20%:mem=85%'
```

```
Memory  peak 435.2Mi
   435.2Mi |                                              ----------##################
           |                                     ----------###########################
           |                            ----------####################################
   272.0Mi |                   ----------#############################################
           |          ----------######################################################
           | ----------###############################################################
           |--########################################################################
    54.4Mi |##########################################################################
         0 +--------------------------------------------------------------------------
      time  0       30s      1m      1m30s     2m      2m30s     3m      3m30s      4m
```

This is the shape to point a memory alert at: it crosses any threshold you pick,
once, at a time you can predict.

### On purpose: run out of memory and get killed

Ask for more memory than the limit and Kubernetes kills the pod. That is a
feature - it is how you test OOM alerting and restart handling.

```sh
kubectl set env deploy/loadsim -n loadsim-demo \
  LOADSIM_PHASES='leak:3m:cpu=10%:mem=30%->130%@linear' \
  LOADSIM_MEM_SOFT_LIMIT=false
```

`LOADSIM_MEM_SOFT_LIMIT=false` matters: by default LoadSim tells the Go garbage
collector about the limit so it does not die *accidentally*. Turning that off
lets it die deliberately.

Within a few minutes:

```sh
kubectl get pods -n loadsim-demo
# RESTARTS starts climbing

kubectl describe pod -n loadsim-demo -l app.kubernetes.io/name=loadsim | grep -A3 'Last State'
# Last State:  Terminated
#   Reason:    OOMKilled
#   Exit Code: 137
```

Left alone, the pod will restart, fill up, and be killed again - eventually
landing in `CrashLoopBackOff`, exactly as a leaking application would. Stop it
by removing the two variables:

```sh
kubectl set env deploy/loadsim -n loadsim-demo LOADSIM_PHASES- LOADSIM_MEM_SOFT_LIMIT-
```

### The memory settings, in one table

| To do this | Set |
| --- | --- |
| A constant level, relative to the limit | `LOADSIM_MEMORY=85%` |
| A constant exact amount | `LOADSIM_MEMORY=400Mi` |
| Measure against the request instead of the limit | `LOADSIM_PERCENT_BASE=request` |
| Climb, then hold | `LOADSIM_PHASES='fill:3m:cpu=20%:mem=20%->85%;hold:cpu=20%:mem=85%'` |
| Get OOM-killed on purpose | a target above `100%`, plus `LOADSIM_MEM_SOFT_LIMIT=false` |
| Use bigger numbers | raise the pod's limit with `kubectl set resources` |
| Keep pages resident on a node with swap | `LOADSIM_MEM_TOUCH_INTERVAL=30s` |

Remember to set `LOADSIM_CPU` alongside `LOADSIM_MEMORY`, or the CPU target
drops to zero.

## 5. Clean up

```sh
kubectl delete namespace loadsim-demo
```

That removes everything this guide created.

## Optional: run it on your laptop instead

No cluster needed - useful for trying profiles quickly. The only difference is
that you state the budget with Docker flags instead of the cluster stating it:

```sh
docker run --rm -p 8080:8080 --cpus 1 --memory 512m \
  registry.gitlab.com/vkalladath/loadsim:v0.2.0 --cpu 50% --memory 85%
```

| Part | Meaning |
| --- | --- |
| `--cpus 1 --memory 512m` | the budget, the same role a pod's limits play |
| `--cpu 50% --memory 85%` | what LoadSim should use out of that budget |
| `-p 8080:8080` | serves the status page at `http://localhost:8080` |

`Ctrl-C` stops it. Podman works identically - type `podman` for `docker`.

Everything else behaves as it does in the cluster:

```sh
IMAGE=registry.gitlab.com/vkalladath/loadsim:v0.2.0

docker run --rm $IMAGE presets                                  # the ten built-in shapes
docker run --rm --cpus 1 --memory 512m $IMAGE plan --preset spiky
docker run --rm --cpus 1 --memory 512m $IMAGE --preset memory-leak
docker run --rm $IMAGE --help
```

Locally, `--preset` does work, because there is no mounted profile to outrank it.

It is safe to run: it burns CPU and fills memory inside its own container, up to
the budget you gave it, makes no network connections, writes no files, and stops
on `Ctrl-C`. Just do not hand it a budget so large that your machine suffers.

## When something looks wrong

| What you see | What it means | What to do |
| --- | --- | --- |
| Pod stuck in `Pending` | The node has no room for the requested CPU/memory. | Lower `requests` (`kubectl set resources ... --requests=cpu=100m,memory=64Mi`) |
| `ImagePullBackOff` | The cluster cannot reach the image. | Check the name; the image is public, so a proxy or air-gapped cluster is the usual cause |
| `RESTARTS` climbing, `OOMKilled` | The memory target went above the pod's memory limit. Deliberate in the OOM example above. | Lower the target, or raise the limit |
| `CrashLoopBackOff` | Same thing, repeatedly. | Remove the overrides: `kubectl set env deploy/loadsim -n loadsim-demo LOADSIM_PHASES- LOADSIM_MEM_SOFT_LIMIT-` |
| `cpu_actual` below `cpu_target`, `throttled` climbing | You asked for more CPU than the limit allows. Often deliberate - this is how throttling is tested. | Lower the CPU target, or raise the CPU limit |
| Memory target set, but `mem_rss` stays flat and low | Only `LOADSIM_MEMORY` was set, or the pod did not restart. | Set `LOADSIM_CPU` too; check `kubectl rollout status deploy/loadsim -n loadsim-demo` |
| `LOADSIM_PRESET` seems ignored | A mounted profile outranks a preset. | Use `LOADSIM_PHASES`, or edit the ConfigMap |
| Warning: `both cpu and memory targets resolve to zero` | Percentages were used where no request or limit is set. | Set limits on the pod, or give absolute values like `250m` and `400Mi` |
| `kubectl top` says `Metrics API not available` | metrics-server is not installed. | Use `kubectl logs` and the `/metrics` endpoint instead |
| `unable to listen on port 8080` | Something else holds the local port. | `kubectl port-forward ... 18080:8080` and browse to `localhost:18080` |
| Locally: Podman errors about `--cpus` | Rootless Podman cannot always set a budget. | Drop `--cpus`/`--memory` and pass `--cpu-limit 1 --memory-limit 512Mi` instead |

Every command accepts `--help`, and `kubectl logs` is almost always the fastest
answer - LoadSim logs its resolved targets and warns about impossible ones at
startup.

## Where to go next

| If you want to | Read |
| --- | --- |
| Understand every setting | [CONFIGURATION.md](CONFIGURATION.md) |
| See all the load shapes, and pick one for a scenario | [PROFILES.md](PROFILES.md) |
| Wire it into Prometheus and write queries | [METRICS.md](METRICS.md) |
| Test autoscaling, right-sizing, throttling properly | [KUBERNETES.md](KUBERNETES.md) |
| Know how accurate it is and how it works inside | [DESIGN.md](DESIGN.md) |
| Build and publish your own image | [RELEASING.md](RELEASING.md) |
