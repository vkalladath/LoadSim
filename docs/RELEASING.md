# Building and publishing the image

The current published image is **`registry.gitlab.com/vkalladath/loadsim:v0.2.0`**, also tagged
`:latest`. The registry is public, so pulling it needs no credentials and
Kubernetes needs no pull secret. Publishing, of course, still needs a token.

LoadSim ships two scripts and a GitLab pipeline. The scripts are the single
implementation: CI calls exactly what you would call by hand, so a pipeline
failure is always reproducible locally.

| Script | Does |
| --- | --- |
| `scripts/build-image.sh` | builds the image, tags it, optionally pushes it |
| `scripts/push-image.sh` | authenticates and pushes tags that already exist |
| `scripts/lib.sh` | shared helpers (sourced, not run) |

Both accept `--help`, both accept `--dry-run`, and neither ever prints a
credential.

- [Quick reference](#quick-reference)
- [Naming and tags](#naming-and-tags)
- [Publishing from a laptop](#publishing-from-a-laptop)
- [Multi-architecture images](#multi-architecture-images)
- [The GitLab pipeline](#the-gitlab-pipeline)
- [Pulling the image in Kubernetes](#pulling-the-image-in-kubernetes)
- [Troubleshooting](#troubleshooting)

## Quick reference

```sh
# local image, tagged from git describe
scripts/build-image.sh

# see exactly what a publish would do, without doing it
scripts/build-image.sh --push --dry-run

# publish one tag to GitLab
GITLAB_PROJECT=vkalladath/loadsim GITLAB_USER=me GITLAB_TOKEN=glpat-xxxx \
  scripts/build-image.sh --tag v0.2.0 --latest --push

# publish a multi-arch release (the published v0.2.0 is amd64 only)
GITLAB_PROJECT=vkalladath/loadsim scripts/build-image.sh \
  --tag v0.3.0 --latest --platforms linux/amd64,linux/arm64 --push

# push tags that are already built
scripts/push-image.sh --tag v0.2.0 --tag latest
```

Or through make, which passes `GITLAB_PROJECT` / `IMAGE_REPO` straight through:

```sh
make image                   # local build, tagged loadsim:dev
make image-plan              # dry run of a publish to registry.gitlab.com/vkalladath/loadsim
make image-push  RELEASE_TAG=v0.3.0
make image-release RELEASE_TAG=v0.3.0    # multi-arch, plus :latest
```

`RELEASE_REPO` defaults to `registry.gitlab.com/vkalladath/loadsim` and `RELEASE_TAG` to
`v0.2.0`; override either on the command line.

## Naming and tags

The image repository is resolved in this order:

| Source | Example result |
| --- | --- |
| `IMAGE_REPO` | used verbatim |
| `CI_REGISTRY_IMAGE` (set by GitLab CI) | `registry.gitlab.com/group/project` |
| `REGISTRY` + `GITLAB_PROJECT` | `registry.gitlab.com/group/project` |
| nothing | `loadsim` - a local name that cannot be pushed |

`IMAGE_NAME` appends a sub-repository when you want several images under one
project: `GITLAB_PROJECT=g/tools IMAGE_NAME=loadsim` gives
`registry.gitlab.com/g/tools/loadsim`.

Attempting to push a local-only name fails with instructions rather than a
registry error.

The default tag comes from `VERSION`, then `CI_COMMIT_TAG`, then
`git describe --tags --always --dirty`, then the CI short sha, then `dev`. Tags
are sanitised to what a registry accepts (`/` and other stray characters become
`-`). `--latest` and `--sha` add the usual extra tags.

Every image carries provenance labels, so a running pod can be traced back:

```console
$ skopeo inspect docker://registry.gitlab.com/vkalladath/loadsim:v0.2.0 --format '{{.Labels}}'
{
  "org.opencontainers.image.version": "v0.2.0",
  "org.opencontainers.image.revision": "57bd19ccc48c56fc01132a3e388e9c20f1ca2384",
  "org.opencontainers.image.source": "https://github.com/vkalladath/LoadSim.git",
  "org.opencontainers.image.created": "2026-08-31T06:28:59Z"
}
```

## Publishing from a laptop

You need a token with the `write_registry` scope. Any of these work:

| Token | Where | Username to use |
| --- | --- | --- |
| Personal access token | User settings → Access tokens | your GitLab username |
| Project deploy token | Project → Settings → Repository → Deploy tokens | the deploy token's username |
| Group deploy token | Group → Settings → Repository → Deploy tokens | the deploy token's username |

```sh
export GITLAB_PROJECT=vkalladath/loadsim   # or IMAGE_REPO for a self-hosted host
export GITLAB_USER=me
export GITLAB_TOKEN=glpat-xxxxxxxxxxxx     # never commit this
scripts/build-image.sh --tag v0.2.0 --latest --push
```

For a self-hosted GitLab, set the registry host too:

```sh
export REGISTRY=registry.gitlab.example.com
```

The token is piped to `docker login --password-stdin`, so it never appears in
the process list, the shell history of the script, or the log. If you are
already logged in (`docker login registry.gitlab.com`), pass `--skip-login` and
set no token at all.

## Multi-architecture images

The Dockerfile's build stage is pinned to `--platform=$BUILDPLATFORM` and
cross-compiles with `GOOS`/`GOARCH`, so building `linux/arm64` on an amd64
machine needs **no qemu**: only the tiny scratch layer differs per
architecture.

- **podman**: the script creates a manifest list, builds every platform into
  it, and pushes it with `podman manifest push --all`.
- **docker**: the script uses `docker buildx`, creating a `docker-container`
  builder named `loadsim` if one does not exist (override with
  `BUILDX_BUILDER`). buildx cannot load a multi-arch image into the local
  image store, so a multi-arch build without `--push` only validates that it
  builds.

Verified locally against a throwaway registry:

```console
$ scripts/build-image.sh --tag multi --platforms linux/amd64,linux/arm64 --push
...
==> built localhost:5000/acme/loadsim:multi for linux/amd64,linux/arm64
# the published index really contains both:
#   {'architecture': 'amd64', 'os': 'linux'}
#   {'architecture': 'arm64', 'os': 'linux'}
```

## The GitLab pipeline

`.gitlab-ci.yml` needs no configuration: GitLab provides `CI_REGISTRY_IMAGE`
and the job token, and the scripts pick them up.

| Job | When | Tags pushed |
| --- | --- | --- |
| `test` | every pipeline | - (gofmt, vet, `go test -short`) |
| `test:timing` | manual | - (the full suite, including timing-sensitive tests) |
| `image:merge-request` | merge requests | none; the build is only validated |
| `image:branch` | non-default branches | `<branch-slug>`, `<short-sha>` |
| `image:default-branch` | default branch | `latest`, `<short-sha>`, multi-arch |
| `image:release` | git tags | `<tag>`, `latest`, multi-arch, then smoke-tested |

The timing-sensitive engine tests (CPU accuracy, RSS convergence) are skipped
in the normal `test` job with `go test -short`, because a busy shared runner
will fail them for reasons that have nothing to do with the code. Run
`test:timing` manually, or drop `-short` if you have a dedicated runner.

Requirements on the GitLab side:

- The Container Registry must be enabled for the project (Settings → General →
  Visibility → Container registry).
- The runner must be able to build images. The pipeline uses `docker:dind`,
  which needs a runner with `privileged = true`. On a runner that cannot do
  that, swap the `.image` job for buildah or kaniko - the scripts do not care
  which CLI is used as long as `CONTAINER_TOOL` points at something
  docker-compatible.
- To keep releases immutable, protect your tag pattern (Settings → Repository →
  Protected tags) so only maintainers can publish `v*`.

Releasing is then just:

```sh
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

Consider adding a registry cleanup policy (Settings → Packages and registries →
Clean up image tags) that keeps `latest` and `v*` and expires branch and sha
tags after a week - the per-commit tags add up quickly.

## Pulling the image in Kubernetes

The image published here is public, so `deploy/k8s/` works as-is. A GitLab
registry is private by default though, so for your own private project the
cluster needs credentials: create a **deploy token** with the `read_registry`
scope, then:

```sh
kubectl create secret docker-registry gitlab-registry \
  --docker-server=registry.gitlab.com \
  --docker-username='<deploy-token-username>' \
  --docker-password='<deploy-token>'
```

Reference it from the pod spec:

```yaml
spec:
  imagePullSecrets:
    - name: gitlab-registry
  containers:
    - name: loadsim
      image: registry.gitlab.com/vkalladath/loadsim:v0.2.0
```

`deploy/k8s/` carries this block, commented out, next to each container.

or attach it to the service account so every pod in the namespace gets it:

```sh
kubectl patch serviceaccount default \
  -p '{"imagePullSecrets":[{"name":"gitlab-registry"}]}'
```

Point the example manifests at your image:

```sh
cd deploy/k8s
kustomize edit set image loadsim=registry.gitlab.com/vkalladath/loadsim:v0.3.0   # only to change it
kubectl apply -k .
```

(without kustomize, edit the `images:` block in `deploy/k8s/kustomization.yaml`).

`scripts/push-image.sh` prints these commands, filled in with the repository it
just pushed to.

## Troubleshooting

**`denied: requested access to the resource is denied`** - the token lacks
`write_registry`, or the username does not match the token (a deploy token has
its own username, which is not yours). Check with
`docker login registry.gitlab.com` by hand.

**`unauthorized: authentication required` when pulling into Kubernetes** - the
`imagePullSecret` is missing, is in a different namespace (they are
namespaced), or its token only has `write_registry`. `read_registry` is the one
pulling needs.

**`multiple platforms feature is currently not supported for docker driver`** -
the buildx builder was not created; the script does this for you, but if you
call `docker buildx build` directly, run
`docker buildx create --name loadsim --driver docker-container --use` first.

**`error during connect ... /var/run/docker.sock`** in CI - the dind service is
not running or the runner is not privileged. Confirm `docker info` succeeds in
`before_script`.

**`x509: certificate signed by unknown authority`** against a self-hosted
registry - mount your CA into the runner image, or, for a local test registry
only, pass `--insecure` (podman).

**`cannot push to the local image name 'loadsim'`** - neither `IMAGE_REPO`,
`CI_REGISTRY_IMAGE` nor `GITLAB_PROJECT` was set, so there is no registry to
push to.

**The version is `dev` and the revision is `unknown`** - the working copy is
not a git repository (or has no tags). Set `VERSION=` explicitly, or
`git init` and tag it.
