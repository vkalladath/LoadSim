# Profiles: curves and scenarios

A phase moves each resource from a start value to an end value along a *curve*.
Monotonic curves consume the phase's progress; periodic curves oscillate with a
`period` and ignore progress, so a single phase can carry a whole traffic
pattern.

Every chart below was produced by `loadsim plan`, which resolves percentages
and draws the profile without generating any load. Try it on your own profiles:

```sh
loadsim plan --config my-profile.yaml --cpu-limit 2 --memory-limit 1Gi
loadsim plan --phases 'c:10m:cpu=0->1@ease-in-out' --cpu-limit 1 --width 100
```

In the charts, `#` is the floor of each column and `-` is the range the target
varies over inside that column, so a spike shorter than one column still shows
up.

- [Monotonic curves](#monotonic-curves)
- [Periodic curves](#periodic-curves)
- [Tuning knobs](#tuning-knobs)
- [Scenario recipes](#scenario-recipes)

## Monotonic curves

Each chart ramps CPU from 0 to one core over ten minutes:

### `step`

holds the end value from the first instant

```
     1.000 |########################################################
           |########################################################
           |########################################################
      571m |########################################################
           |########################################################
           |########################################################
      143m |########################################################
         0 +--------------------------------------------------------
```

### `linear`

constant rate of change

```
      999m |                                                --------
           |                                        --------########
           |                                --------################
      571m |                        --------########################
           |                --------################################
           |        --------########################################
      143m |--------################################################
         0 +--------------------------------------------------------
```

### `ease-in`

slow start, fast finish (t^exponent, default 2)

```
      997m |                                                   -----
           |                                               -----####
           |                                          ------########
      570m |                                    -------#############
           |                             --------###################
           |                     ---------##########################
      142m |----------------------##################################
         0 +--------------------------------------------------------
```

### `ease-out`

fast start, slow finish - warm-up decay

```
     1000m |                                  ----------------------
           |                          ---------#####################
           |                   --------#############################
      571m |             -------####################################
           |        ------##########################################
           |    -----###############################################
      143m |-----###################################################
         0 +--------------------------------------------------------
```

### `ease-in-out`

smoothstep: gentle at both ends, an S-curve

```
     1000m |                                          --------------
           |                                    -------#############
           |                              -------###################
      571m |                         ------#########################
           |                   -------##############################
           |             -------####################################
      143m |--------------##########################################
         0 +--------------------------------------------------------
```

### `exp`

exponential: almost all the rise at the end (exponent tunes sharpness)

```
      994m |                                                     ---
           |                                                   ---##
           |                                                ----####
      568m |                                            -----#######
           |                                       ------###########
           |                              ----------################
      142m |-------------------------------#########################
         0 +--------------------------------------------------------
```

### `log`

logarithmic: almost all the rise at the start

```
      999m |                                      ------------------
           |                          -------------#################
           |                 ---------##############################
      571m |          -------#######################################
           |     ------#############################################
           |  ----##################################################
      143m |---#####################################################
         0 +--------------------------------------------------------
```


## Periodic curves

Each chart oscillates CPU between 100m and one core with `period: 2m` over ten
minutes. Written inline that is `cpu=100m->1@sine/2m`, or in the mapping form:

```yaml
cpu:
  from: 100m
  to: "1"
  curve: sine
  period: 2m
```

### `sine`

smooth from -> to -> from, no discontinuity

```
     1.000 |    ----       ----       ----       ----        ---    
           |   --##-      --##-      --##--      -##--      -##--   
           |  --###--     -####-     -####-     --###-     --###--  
      571m |  -#####-    --####--   --####--   --####--    -#####-  
           | --#####--  --######-   -######-   -######--  --#####-- 
           |--#######-- -#######-- --#######- --#######----#######--
      143m |-#########--#########---########---#########--#########-
         0 +--------------------------------------------------------
```

### `triangle`

linear up then linear down

```
     1.000 |    ---        ---         --         ---        ---    
           |   --#--       -#--       ----       --#-       --#--   
           |   -###--     --##--     --##--     --###-     --###--  
      571m |  -#####--   --####--   --####--   --####--   --#####-  
           | --######-- --######-- --######-- --######--  -#######- 
           |--########---########---########---########---########--
      143m |-##########-##########-##########-##########--#########-
         0 +--------------------------------------------------------
```

### `sawtooth`

linear climb, instant drop

```
      999m |         ---        ---        ---         --         --
           |       ---#-      ---#-       --#-       ----       ---#
           |     ---###-     --###-     ---##-     ---##-     ---###
      571m |    --#####-   ---####-   ---####-   ---####-   ---#####
           |  ---######- ---######- ---######- ---######-  --#######
           |---########---########---########---########----########
      143m |-##########-##########-##########--#########--##########
         0 +--------------------------------------------------------
```

### `square`

a spike to "to" for "duty" of each period, baseline "from" otherwise

```
     1.000 |#####-     -####-     -#####     -#####-    -#####-     
           |#####-     -####-     -#####     -#####-    -#####-     
           |#####-     -####-     -#####     -#####-    -#####-     
      571m |#####-     -####-     -#####     -#####-    -#####-     
           |#####-     -####-     -#####     -#####-    -#####-     
           |#####-     -####-     -#####     -#####-    -#####-     
      143m |#####-------####-------#####------#####------#####------
         0 +--------------------------------------------------------
```

### `noise`

uniform random between from and to, resampled once per period

```
      974m |                                 -##########-           
           |------------                     -##########-           
           |###########-                     -##########------------
      557m |###########-                     -######################
           |###########-                     -######################
           |###########-          ------------######################
      139m |###########------------#################################
         0 +--------------------------------------------------------
```

### `random-walk`

bounded random walk; "exponent" is the step size

```
      490m |           -##########------------                      
           |------------#####################-          ------------
           |#################################------------###########
      280m |########################################################
           |########################################################
           |########################################################
       70m |########################################################
         0 +--------------------------------------------------------
```

## Tuning knobs

| Knob | Applies to | Effect |
| --- | --- | --- |
| `period` | periodic curves | length of one oscillation (default 30s) |
| `duty` | `square` | fraction of each period spent at `to` (default 0.5) |
| `exponent` | `ease-in`, `ease-out` | power (default 2); higher is sharper |
| `exponent` | `exp`, `log` | curvature (default 4 / 9) |
| `exponent` | `random-walk` | step size per period (default 0.25) |
| `jitter` | any curve | uniform +/- noise on every sample, e.g. `5%` |
| `seed` (top level) | jitter, `noise`, `random-walk` | makes a run reproducible |

Inline, these are written as `@curve/period` and `@curve^exponent`:

```sh
--phases 'a:10m:cpu=10%->90%@exp^8'          # very late rise
--phases 'b:10m:cpu=10%->90%@square/90s'     # spike every 90 seconds
```

Jitter is applied *after* the curve, and is clamped at zero, so a jittered
target never goes negative.

## Scenario recipes

### Slow start that breaks CPU requests

The classic: a container that needs a core for the first minute and 250m
thereafter. Sized on steady state it starts slowly; sized on startup it wastes
three quarters of its request.

```sh
loadsim --preset startup-burst
```

```yaml
phases:
  - name: cold-start
    duration: 90s
    cpu: 100% -> 30% @ease-out
    memory: 15% -> 55% @ease-in-out
  - name: steady
    cpu: 25%
    memory: 60%
```

### Sustained CFS throttling

Ask for more CPU than the limit allows. `loadsim_cpu_saturated` goes to 1 and
`loadsim_cgroup_cpu_throttled_seconds_total` climbs, next to whatever your
monitoring says.

```sh
loadsim --cpu 150% --memory 20%
```

### OOM kill on a schedule

Memory climbing past the limit. `soft_limit: false` stops the Go GC from
holding the line, and `touch_interval` keeps pages hot so a node with swap
cannot page its way out of the kill.

```sh
loadsim --preset memory-leak
# or a faster version:
loadsim --phases 'leak:3m:cpu=10%:mem=30%->150%@linear' \
        --mem-soft-limit=false --mem-touch-interval 10s
```

### Autoscaler exercise

A shape that crosses a 60% utilisation target twice per cycle, forever:

```sh
loadsim --preset sawtooth
```

Pair it with `deploy/k8s/hpa.yaml`. Because the profile is deterministic, the
same run can be repeated after changing the HPA's stabilisation windows.

### Right-sizing / VPA

A workload that is honestly over-provisioned, expressed relative to its request
so the profile stays meaningful after a recommender changes the request:

```sh
loadsim --preset oversized
# 5% of the CPU request, 20% of the memory request
```

Its opposite, for the upscale case:

```sh
loadsim --cpu 180%request --memory 150%request --cpu-limit 2 --memory-limit 1Gi
```

### Percentile-sensitive traffic

A low baseline with brief tall spikes: the mean says 20%, the p99 says 95%, and
sizing on the wrong one is visibly wrong.

```sh
loadsim --preset spiky
```

### Compressed day/night cycle

A sine wave over an hour, for trend and seasonality logic without waiting a
day:

```sh
loadsim --preset daily-cycle
# stretch it: --phases 'day:6h:cpu=10%->85%@sine/6h:mem=40%->70%@sine/6h' --loop
```

### Metrics-pipeline fidelity check

Anything that changes faster than the scrape interval. Compare LoadSim's own
`loadsim_cpu_actual_cores` (which it measures from inside the container) with
what the pipeline reports for the same pod:

```sh
loadsim --phases 'fast:10m:cpu=10%->95%@sine/20s' --loop
```

If the two disagree, the disagreement is the finding.

### Startup and probe behaviour

Model a container that is slow to become useful: 30s of init work during which
it uses nothing, then 20s more before readiness opens.

```sh
loadsim --init-delay 30s --ready-delay 50s --preset startup-burst
```

`/readyz` returns 503 until `ready_delay` has elapsed, so rollout timing,
`minReadySeconds` and probe thresholds all come into play.

### One-shot load for a Job

```sh
loadsim --phases 'ramp:5m:cpu=10%->95%@linear' --hold-last=false
```

The process exits when the profile ends, so the Job completes.
