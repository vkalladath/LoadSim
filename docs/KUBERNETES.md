# Running LoadSim on Kubernetes

- [Deploy](#deploy)
- [Telling LoadSim its own size](#telling-loadsim-its-own-size)
- [Choosing how to configure the pod](#choosing-how-to-configure-the-pod)
- [Experiments](#experiments)
- [Things that will surprise you](#things-that-will-surprise-you)

## Deploy

The manifests in `deploy/k8s/` already point at the published image,
`registry.gitlab.com/vkalladath/loadsim:v0.2.0`. It lives in a **private** GitLab registry, so
create the pull secret once per namespace first:

```sh
kubectl create secret docker-registry gitlab-registry \
  --docker-server=registry.gitlab.com \
  --docker-username='<deploy-token-username>' \
  --docker-password='<deploy-token>'      # read_registry scope is enough

kubectl apply -k deploy/k8s
kubectl port-forward deploy/loadsim 8080:8080
open http://localhost:8080/
```

The manifests reference the secret by name (`gitlab-registry`). If you would
rather attach it to the service account, do that and delete the
`imagePullSecrets` block:

```sh
kubectl patch serviceaccount default \
  -p '{"imagePullSecrets":[{"name":"gitlab-registry"}]}'
```

To run your own build instead, publish it and repoint the manifests - see
[RELEASING.md](RELEASING.md):

```sh
GITLAB_PROJECT=vkalladath/loadsim scripts/build-image.sh --tag v0.3.0 --push
cd deploy/k8s && kustomize edit set image loadsim=registry.gitlab.com/vkalladath/loadsim:v0.3.0
```

`deploy/k8s/` contains:

| File | What it is |
| --- | --- |
| `configmap.yaml` | the profile, mounted at `/etc/loadsim/profile.yaml` |
| `deployment.yaml` | the full example: downward API, probes, pull secret, read-only root, non-root user |
| `deployment-args-only.yaml` | the minimal version, profile passed as args |
| `service.yaml` | ClusterIP for scraping and port-forwarding |
| `servicemonitor.yaml` | Prometheus Operator scrape config |
| `hpa.yaml` | an HPA targeting 60% CPU utilisation |
| `job.yaml` | run the profile once and exit |
| `kustomization.yaml` | ties the first three together, and sets the image to `v0.2.0` |

The container needs no privileges: no root, no capabilities, read-only root
filesystem, and it only ever *reads* `/proc/self` and `/sys/fs/cgroup`.

Changing the ConfigMap does not restart pods; roll the Deployment to pick up a
new profile:

```sh
kubectl rollout restart deploy/loadsim
```

## Telling LoadSim its own size

Percentages resolve against the container's request and limit. LoadSim can read
the limit from its cgroup on its own, but the *request* only exists in the API
server, so pass it with the downward API:

```yaml
env:
  - name: LOADSIM_CPU_REQUEST_MILLI
    valueFrom:
      resourceFieldRef:
        containerName: loadsim
        resource: requests.cpu
        divisor: 1m
  - name: LOADSIM_CPU_LIMIT_MILLI
    valueFrom:
      resourceFieldRef:
        containerName: loadsim
        resource: limits.cpu
        divisor: 1m
  - name: LOADSIM_MEMORY_REQUEST_BYTES
    valueFrom:
      resourceFieldRef:
        containerName: loadsim
        resource: requests.memory
        divisor: "1"
  - name: LOADSIM_MEMORY_LIMIT_BYTES
    valueFrom:
      resourceFieldRef:
        containerName: loadsim
        resource: limits.memory
        divisor: "1"
```

The divisors matter: `1m` makes the CPU values plain integer millicores and
`"1"` makes the memory values plain integer bytes, which is what the `*_MILLI`
and `*_BYTES` variables expect.

If a request or limit is absent, `resourceFieldRef` falls back to the node's
allocatable capacity - which is a big number. Either set both requests and
limits, or pin the numbers explicitly:

```yaml
args: ["--cpu-request=500m", "--cpu-limit=1", "--memory-request=256Mi", "--memory-limit=512Mi"]
```

Check what LoadSim concluded before trusting a run:

```console
$ kubectl logs deploy/loadsim | head -3
INFO  resolved resources cpu_request=500m cpu_limit=1.000 memory_request=256.0Mi memory_limit=512.0Mi
      sources=cpu_limit=cgroup:cpu.max cpu_request=env:LOADSIM_CPU_REQUEST_MILLI ...
```

## Choosing how to configure the pod

| Style | Use when |
| --- | --- |
| `args: ["--cpu=60%", "--memory=70%"]` | one or two constant targets |
| `args: ["--phases=..."]` or `LOADSIM_PHASES` | a multi-phase profile you want visible in the pod spec |
| `args: ["--preset=spiky"]` | a standard shape, possibly adjusted with more flags |
| ConfigMap + `--config` | anything long, commented, or shared between deployments |
| `LOADSIM_CONFIG_YAML` | a full profile without a volume, e.g. from Helm values |

All of them can be mixed: a preset for the shape, a ConfigMap for the details,
an env var for the one thing that differs per environment.

## Experiments

### Does my monitoring agree with reality?

Deploy `deploy/k8s/deployment.yaml`, scrape LoadSim's own metrics, then compare
`loadsim_cpu_actual_cores` with your platform's number for the same pod. See
[METRICS.md](METRICS.md#example-queries) for the queries. A steady profile
(`--preset steady`) isolates pipeline error; a fast profile
(`--phases 'f:10m:cpu=10%->95%@sine/20s' --loop`) isolates resolution error.

### HPA behaviour

```sh
kubectl apply -f deploy/k8s/hpa.yaml
kubectl set env deploy/loadsim LOADSIM_PRESET=sawtooth   # env backs every flag
kubectl get hpa loadsim -w
```

The sawtooth crosses the 60% target twice per cycle, so scale-up delay,
scale-down stabilisation and flapping are all observable - and repeatable,
because the profile is deterministic (`seed` fixes even the jitter).

### Right-sizing recommendations

Run several deployments with different, known profiles and compare each
recommendation against the profile you asked for:

```sh
for p in oversized spiky startup-burst; do
  kubectl create deployment loadsim-$p --image registry.gitlab.com/vkalladath/loadsim:v0.2.0
  kubectl set env deployment/loadsim-$p LOADSIM_PRESET=$p
  kubectl set resources deployment/loadsim-$p --requests=cpu=500m,memory=256Mi --limits=cpu=1,memory=512Mi
done
# these bypass the manifests, so the pull secret has to come from the
# service account (see Deploy above)
```

`oversized` should be recommended down, `spiky` should keep headroom for its
p99, and `startup-burst` is the interesting one: a recommender that only looks
at steady state will squeeze the request until restarts get slow.

### Throttling

```sh
kubectl set env deploy/loadsim LOADSIM_PRESET=saturated
```

`loadsim_cgroup_cpu_throttled_seconds_total` climbs immediately; compare it with
`container_cpu_cfs_throttled_seconds_total` from cAdvisor.

### OOM kill and restart alerting

```sh
kubectl set env deploy/loadsim LOADSIM_PRESET=memory-leak
kubectl get pod -l app.kubernetes.io/name=loadsim -w
```

Expect `OOMKilled`, exit code 137 and a `CrashLoopBackOff` if you leave it
running - which is exactly what an alert rule for OOM kills should catch. Verify
that the pod's memory limit and the profile's peak are what you think they are
first: `loadsim plan --preset memory-leak`.

### Rollout and probe timing

```sh
kubectl set env deploy/loadsim LOADSIM_INIT_DELAY=30s LOADSIM_READY_DELAY=50s
kubectl rollout status deploy/loadsim
```

Readiness stays closed for 50s while the container uses no CPU for the first
30s, which is the shape that makes rolling updates slow and readiness gates
matter.

### Node-level pressure

To load a node rather than a pod, size against the machine and run one pod per
node with a DaemonSet:

```sh
loadsim --cpu 40%node --memory 20%node
```

Be careful: this competes with everything else on the node, including the
kubelet.

## Things that will surprise you

**Swap changes the memory story.** With swap enabled (some nodes, and most
laptops running Docker or Podman) the kernel can page out untouched anonymous
memory instead of OOM-killing the container: RSS then stops tracking the
target. Set `engine.memory.touch_interval` (the `memory-leak` preset uses 10s)
to keep pages hot, or disable swap for the test.

**Very short spikes may not be visible to your metrics.** A 10s spike scraped
every 30s is a coin flip. That is often the finding rather than a problem - but
check `/status` or the status log to confirm the spike really happened.

**CPU targets above the limit cannot be reached, by design.** The CFS quota
wins; you get throttling instead. `loadsim_cpu_saturated` tells you when
LoadSim knows it is being held back.

**`GOMAXPROCS` and small limits.** A container with a 100m limit still sees all
of the node's CPUs. LoadSim sizes its spin pool from the profile's peak target,
not from the node, so this is usually harmless; use `--max-procs` if you want
the Go runtime constrained too.

**The memory limit and the Go GC.** By default LoadSim sets a soft memory limit
at 90% of the container limit so that GC overshoot does not cause an
*accidental* OOM. When you want a *deliberate* OOM, set
`engine.memory.soft_limit: false` (as the `memory-leak` preset does).

**Requests are not enforced.** A pod with a 100m request and no limit can use a
whole core; `--cpu 500%request` will happily do so. Percentages of requests are
about *sizing signals*, not about what the pod is allowed to do.
