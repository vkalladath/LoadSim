# Getting started with LoadSim

LoadSim is a small program that **pretends to be a busy application**. You tell
it how much CPU and memory to use, and it uses exactly that much - steadily, in
bursts, ramping up, whatever you ask for.

That is useful when you want to test the *things that watch* applications -
monitoring dashboards, alerts, capacity reports, autoscaling - without having to
find a real application that misbehaves in the way you need.

This page assumes you can copy and paste commands into a terminal. It assumes
**no Kubernetes knowledge**; the Kubernetes part at the end is optional.

- [What you need](#what-you-need)
- [Is this safe to run?](#is-this-safe-to-run)
- [1. Run it](#1-run-it)
- [2. See what it is doing](#2-see-what-it-is-doing)
- [3. Change the load](#3-change-the-load)
- [4. Optional: run it in Kubernetes](#4-optional-run-it-in-kubernetes)
- [When something looks wrong](#when-something-looks-wrong)
- [Where to go next](#where-to-go-next)

## What you need

Docker or Podman. Check which one you have:

```sh
docker --version    # or: podman --version
```

If you have Podman rather than Docker, every command below works if you type
`podman` where it says `docker`. Nothing else needs installing - LoadSim is a
single self-contained image, about 7 MB.

## Is this safe to run?

Yes, with one thing to keep in mind.

- It burns CPU and fills memory **inside its own container**, up to the budget
  you give it on the command line. Nothing outside that container is touched.
- It makes no network connections, reads nothing from your machine, and writes
  no files.
- It stops the moment you press `Ctrl-C`.

The thing to keep in mind: if you give it a large budget (say 8 CPUs) your
machine will genuinely be busy while it runs. The examples below use one CPU
core and 512 MB, which you will not notice.

## 1. Run it

```sh
docker run --rm -p 8080:8080 --cpus 1 --memory 512m \
  registry.gitlab.com/vkalladath/loadsim:v0.2.0 --cpu 50% --memory 50%
```

What each part does:

| Part | Meaning |
| --- | --- |
| `--cpus 1 --memory 512m` | the **budget**: this container may use 1 CPU core and 512 MB of memory |
| `--cpu 50% --memory 50%` | what LoadSim should actually use: half of that budget |
| `-p 8080:8080` | lets you view its status page at `http://localhost:8080` |
| `--rm` | delete the container when it stops |

The first two lines are the important idea: **percentages are relative to the
budget the container was given.** `--cpu 50%` with a 1 CPU budget means half a
core. The same command in a container with a 4 CPU budget would use two cores.
You never have to recalculate anything.

You should see something like this (timestamps trimmed here to keep the
lines short):

```
INFO  loadsim 57bd19c starting (go1.25.14 linux/amd64, GOMAXPROCS=16)
INFO  configuration chain=defaults -> overrides:cpu,memory one_pass=0s percent_base=limit phases=1 profile=loadsim
INFO  resolved resources cpu_request=0m cpu_limit=1.000 memory_request=0B memory_limit=512.0Mi sources=cpu_limit=cgroup:cpu.max memory_limit=cgroup:memory.max node_cpu=runtime.NumCPU
INFO  phase index=1 name=main starts_at=0s duration=hold cpu=500m (50%) memory=256.0Mi (50%)
INFO  engines ready cpu_workers=1 cpu_peak_target=500m memory_peak_target=256.0Mi memory_chunk=4.0Mi
INFO  http server listening on :8080 (metrics at /metrics)
INFO  status phase=main(1/1) progress=100% elapsed=15s cpu_target=500m cpu_actual=494m cpu_duty=0.50 mem_target=256.0Mi mem_rss=258.1Mi cpu_pct_limit=49% mem_pct_limit=50% throttled=0.0s
INFO  status phase=main(1/1) progress=100% elapsed=30s cpu_target=500m cpu_actual=503m cpu_duty=0.50 mem_target=256.0Mi mem_rss=257.4Mi cpu_pct_limit=50% mem_pct_limit=50% throttled=0.0s
```

Read the last line as: *"I was aiming for 500 millicores of CPU and 256 MiB of
memory; I measured 503 millicores and 257.4 MiB."* A new one appears every 15
seconds.

`500m` is shorthand for 500 millicores, which is half of one CPU core. CPU is
usually written this way: `250m` is a quarter of a core, `2` is two whole cores.

Leave it running and open a **second terminal** for the next step. When you are
done, press `Ctrl-C` in the first terminal to stop it.

## 2. See what it is doing

Open <http://localhost:8080> in a browser, or in the second terminal:

```sh
curl -s localhost:8080/
```

```
loadsim 57bd19c - profile "loadsim"

uptime            35s
profile elapsed   35s (iteration 0)
phase             main (1/1, 100% complete)
ready             true

cpu     target 500m       actual 509m       duty 0.50 over 1 worker(s)
memory  target 256.0Mi    rss 257.7Mi       allocated 248.0Mi in 62 chunk(s)
limits  cpu request=- limit=1.000 / memory request=- limit=512Mi / node cpus=16 mem=62076Mi cgroup=v2
throttle 6/351 periods, 0.05s total

...the profile chart follows.
```

The four numbers that matter:

| Number | What it tells you |
| --- | --- |
| `cpu target` | what LoadSim was asked to use |
| `cpu actual` | what it measured itself using - these should track each other closely |
| `memory rss` | memory actually resident, the number monitoring tools report |
| `throttle` | how often the container hit its CPU budget and was held back |

**Why "target" and "actual" are both shown:** if your monitoring says this
container is using 300 millicores while LoadSim says it is using 494, then your
monitoring, not the load, is the thing worth investigating. That comparison is
the whole point of the tool.

There is also a machine-readable version at `http://localhost:8080/metrics`,
which is the format Prometheus and most monitoring systems scrape:

```sh
curl -s localhost:8080/metrics | grep -E 'cpu_target|cpu_actual'
```

```
loadsim_cpu_target_cores 0.5
loadsim_cpu_actual_cores 0.49376437009865354
```

## 3. Change the load

Stop the running container with `Ctrl-C` first. In every example below,
`IMAGE` stands for `registry.gitlab.com/vkalladath/loadsim:v0.2.0` - set it
once and the commands get shorter:

```sh
IMAGE=registry.gitlab.com/vkalladath/loadsim:v0.2.0
```

**A fixed amount, ignoring the budget:**

```sh
docker run --rm --cpus 1 --memory 512m $IMAGE --cpu 250m --memory 128Mi
```

**Load that climbs from nearly nothing to nearly the whole budget over two
minutes, then holds:**

```sh
docker run --rm --cpus 1 --memory 512m $IMAGE \
  --phases 'ramp:2m:cpu=5%->90%:mem=10%->80%;steady:cpu=90%:mem=80%'
```

Read that as two stages separated by `;`: a stage named `ramp` lasting `2m`
where CPU goes from 5% to 90% of the budget, then a stage named `steady` with no
time limit that holds those values.

**Ready-made shapes.** Ten common patterns are built in:

```sh
docker run --rm $IMAGE presets
```

```
Built-in presets (use --preset <name>, inspect with 'loadsim presets show <name>'):
  steady         Constant 50% of the CPU and memory limit. The baseline sanity check.
  startup-burst  Heavy 90s startup burst decaying to a modest steady state - the classic JVM/warm-cache shape that breaks request sizing.
  ramp-up        Slow S-curve ramp from idle to the limit over 10 minutes, then hold. Good for autoscaler and alert threshold testing.
  sawtooth       Repeating 5-minute sawtooth between 10% and 90% of the limit. Exercises metric resolution and averaging windows.
  spiky          Low baseline with a short 95% spike every 3 minutes. The shape that p50-based sizing gets wrong.
  memory-leak    Flat CPU with memory climbing past the limit over 30 minutes, ending in an OOM kill. Tests OOM detection and restart alerting.
  daily-cycle    Smooth sine wave over a compressed 'day' (1 hour by default). Good for testing trend-based recommendations.
  oversized      Uses far less than it requests (5% CPU, 20% memory of request). The right-sizing downscale case.
  saturated      Sits at 98% of the CPU limit and 90% of the memory limit: sustained CFS throttling without an OOM kill.
  idle           Almost nothing: 10m CPU, 32Mi memory. Useful as a control pod.
```

Run one with `--preset`:

```sh
docker run --rm --cpus 1 --memory 512m $IMAGE --preset startup-burst
```

**Preview a shape without running it.** `plan` draws the load over time and
exits - no CPU is used, so this is the cheap way to check you asked for what you
meant:

```sh
docker run --rm --cpus 1 --memory 512m $IMAGE plan --preset startup-burst
```

```
resources: cpu request=- limit=1.000 / memory request=- limit=512Mi / node cpus=16 mem=62076Mi cgroup=v2
profile "startup-burst": 2 phase(s), one pass 1m30s, then holds the last phase
    phases  startup                                                   steady              
            |_________________________________________________________|___________________
CPU (cores)  peak 1.000
     1.000 |----                                                                          
           |###----                                                                       
           |######-----                                                                   
           |##########-----                                                               
           |##############------                                                          
      583m |###################------                                                     
           |########################-------                                               
           |###############################---------                                      
           |#######################################--------------------                   
           |##############################################################################
           |##############################################################################
       83m |##############################################################################
         0 +------------------------------------------------------------------------------

Memory  peak 307.2Mi
   307.2Mi |                                             --------------###################
           |                                       -------################################
           |                                  ------######################################
           |                             ------###########################################
           |                        ------################################################
   179.2Mi |                   ------#####################################################
           |             -------##########################################################
           |-------------#################################################################
           |##############################################################################
           |##############################################################################
           |##############################################################################
    25.6Mi |##############################################################################
         0 +------------------------------------------------------------------------------

      time  0       15s       30s      45s       1m       1m15s    1m30s     1m45s      2m

phases:
   1. startup          1m30s    at 0       cpu 1.000->250m ease-out   memory 102.4Mi->307.2Mi ease-in-out
   2. steady           hold     at 1m30s   cpu 250m                   memory 307.2Mi
```

That is a container that hammers the CPU for its first minute and a half and
then settles down - the shape that makes capacity planning hard, and a good
first thing to point your monitoring at.

## 4. Optional: run it in Kubernetes

Skip this section entirely if you are not using Kubernetes; everything above
works on its own.

> **Check which cluster you are pointed at first.** These commands create things
> in whatever cluster `kubectl` is currently configured for, which may be a real
> one:
>
> ```sh
> kubectl config current-context
> ```
>
> If that is not a test cluster you are happy to create things in, stop here and
> switch contexts (`kubectl config use-context <name>`). The steps below put
> everything in its own namespace and delete it again, but the safest cluster to
> learn in is one nobody else is using.

Three terms, and then you have enough:

- A **pod** is one or more containers running together. LoadSim is one container.
- A **request** is what a container is promised. A **limit** is the most it may
  use. Both are what LoadSim's percentages refer to, and it reads them from the
  cluster by itself.
- A **namespace** is a folder to keep things separate, so cleaning up is one
  command.

Clone the repository (the manifests live in it) and deploy into a fresh
namespace:

```sh
git clone https://github.com/vkalladath/LoadSim.git
cd LoadSim

kubectl create namespace loadsim-demo
kubectl apply -k deploy/k8s -n loadsim-demo
```

Watch the pod start, then look at the same status page as before:

```sh
kubectl get pods -n loadsim-demo -w        # Ctrl-C once it says Running
kubectl logs -n loadsim-demo deploy/loadsim --tail=5
kubectl port-forward -n loadsim-demo deploy/loadsim 8080:8080
```

With the port-forward running, <http://localhost:8080> shows the status page.

The deployed pod asks for `cpu: 500m` / `memory: 256Mi` and is limited to `1`
CPU / `512Mi`, and the profile it runs is a startup burst that settles down -
so its CPU graph in your monitoring should be tall for the first 90 seconds and
low afterwards. That is the thing to go and look for in whatever dashboard you
are testing.

To change the load, edit the profile in the ConfigMap and restart:

```sh
kubectl edit configmap loadsim-profile -n loadsim-demo     # change the numbers
kubectl rollout restart deploy/loadsim -n loadsim-demo
```

Clean up completely:

```sh
kubectl delete namespace loadsim-demo
```

## When something looks wrong

| What you see | What it means | What to do |
| --- | --- | --- |
| `cpu_actual` well below `cpu_target`, `throttled` climbing | You asked for more CPU than the container is allowed to use. Often deliberate - this is how you test throttling. | Lower the percentage, or raise `--cpus` |
| Warning: `both cpu and memory targets resolve to zero` | You used percentages but the container has no budget to be a percentage of. | Add `--cpus 1 --memory 512m`, or state the numbers directly: `--cpu-limit 1 --memory-limit 512Mi` |
| Container exits with code `137`, or `OOMKilled` | The memory target went above the container's memory budget, so the system killed it. Expected with `--preset memory-leak`. | Lower the memory target, or raise `--memory` |
| `port is already allocated` | Something else is using port 8080. | Use a different local port: `-p 18080:8080`, then browse to `localhost:18080` |
| Podman: an error mentioning `--cpus` or cgroups | Rootless Podman cannot always set a CPU budget. | Drop `--cpus`/`--memory` and tell LoadSim the numbers instead: `--cpu-limit 1 --memory-limit 512Mi` |
| `unknown curve` / `invalid cpu quantity` | A typo in a profile. The message lists the valid options. | Check it with `plan` before running it |
| Kubernetes pod stuck in `Pending` | The cluster has no room for the requested CPU/memory. | Lower the `requests` in `deploy/k8s/deployment.yaml` |

Every command also accepts `--help`:

```sh
docker run --rm $IMAGE --help
```

## Where to go next

| If you want to | Read |
| --- | --- |
| Understand every setting | [CONFIGURATION.md](CONFIGURATION.md) |
| See all the load shapes, and pick one for a scenario | [PROFILES.md](PROFILES.md) |
| Wire it into Prometheus and write queries | [METRICS.md](METRICS.md) |
| Run it properly on Kubernetes, test autoscaling | [KUBERNETES.md](KUBERNETES.md) |
| Know how accurate it is and how it works inside | [DESIGN.md](DESIGN.md) |
| Build and publish your own image | [RELEASING.md](RELEASING.md) |
