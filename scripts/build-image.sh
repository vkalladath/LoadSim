#!/usr/bin/env bash
#
# Build the LoadSim container image, optionally for several architectures and
# optionally pushing it to a registry.
#
# Typical use:
#   scripts/build-image.sh                                  # local image, tagged from git
#   scripts/build-image.sh --tag v0.2.0 --latest
#   GITLAB_PROJECT=my-group/loadsim scripts/build-image.sh --push
#   scripts/build-image.sh --platforms linux/amd64,linux/arm64 --push
#
# In GitLab CI everything is derived from the predefined variables, so
# "scripts/build-image.sh --push" needs no arguments.
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() {
  cat <<'USAGE'
Usage: scripts/build-image.sh [options]

Options:
  -t, --tag TAG           image tag; repeatable (default: version from git or $VERSION)
      --latest            also tag :latest
      --sha               also tag with the short commit sha
      --platforms LIST    comma separated, e.g. linux/amd64,linux/arm64
      --push              push after building (implies a registry repository)
      --skip-login        push without authenticating (an existing login, or an open registry)
      --insecure          allow an untrusted TLS certificate or plain HTTP (podman)
      --repo REPO         full image repository, e.g. registry.gitlab.com/group/project
      --shell             build on alpine instead of scratch, so the image has a shell
      --no-cache          build without a layer cache
      --build-arg K=V     extra build argument; repeatable
      --dry-run           print the commands without running them
  -h, --help              this text

Environment:
  IMAGE_REPO              full repository, overrides everything else
  CI_REGISTRY_IMAGE       set by GitLab CI; used automatically
  GITLAB_PROJECT          group/project, for local builds aimed at GitLab
  REGISTRY                registry host (default registry.gitlab.com)
  IMAGE_NAME              optional sub-repository under the project path
  VERSION                 version label and default tag
  CONTAINER_TOOL          docker or podman (auto-detected)
  GO_VERSION              Go toolchain image tag (default 1.25)
USAGE
}

TAGS=()
BUILD_ARGS=()
PUSH_FLAGS=()
PLATFORMS="${PLATFORMS:-}"
PUSH=0
ADD_LATEST=0
ADD_SHA=0
NO_CACHE=0
RUNTIME_BASE="${RUNTIME_BASE:-scratch}"

while [ $# -gt 0 ]; do
  case "$1" in
    -t|--tag)       [ -n "${2:-}" ] || die "--tag needs a value (an unset variable in the caller?)"
                    TAGS+=("$2"); shift 2 ;;
    --latest)       ADD_LATEST=1; shift ;;
    --sha)          ADD_SHA=1; shift ;;
    --platforms)    PLATFORMS="$2"; shift 2 ;;
    --push)         PUSH=1; shift ;;
    --skip-login)   PUSH_FLAGS+=(--skip-login); shift ;;
    --insecure)     PUSH_FLAGS+=(--insecure); shift ;;
    --repo)         IMAGE_REPO="$2"; shift 2 ;;
    --shell)        RUNTIME_BASE="alpine:3.22"; shift ;;
    --no-cache)     NO_CACHE=1; shift ;;
    --build-arg)    BUILD_ARGS+=(--build-arg "$2"); shift 2 ;;
    --dry-run)      DRY_RUN=1; shift ;;
    -h|--help)      usage; exit 0 ;;
    *)              die "unknown option: $1 (try --help)" ;;
  esac
done
export DRY_RUN="${DRY_RUN:-0}"

cd "$(repo_root)"

# An explicit --tag names the release, so it becomes the version label too
# unless VERSION or a CI tag says otherwise. Without this, "--tag v0.2.0" would
# publish an image labelled with a bare commit description.
if [ -z "${VERSION:-}" ] && [ -z "${CI_COMMIT_TAG:-}" ]; then
  for t in "${TAGS[@]}"; do
    [ "$t" = "latest" ] && continue
    VERSION="$t"
    break
  done
fi

TOOL=$(container_tool)
VERSION=$(resolve_version)
REVISION=$(resolve_revision)
SOURCE_URL=$(resolve_source_url)
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
REPO=$(resolve_repo)

if [ ${#TAGS[@]} -eq 0 ]; then
  TAGS=("$(sanitize_tag "$VERSION")")
else
  mapfile -t TAGS < <(for t in "${TAGS[@]}"; do sanitize_tag "$t"; echo; done)
fi
[ "$ADD_LATEST" = "1" ] && TAGS+=("latest")
if [ "$ADD_SHA" = "1" ]; then
  short="${CI_COMMIT_SHORT_SHA:-$(git rev-parse --short HEAD 2>/dev/null || true)}"
  [ -n "$short" ] && TAGS+=("$(sanitize_tag "$short")")
fi

if [ "$PUSH" = "1" ] && ! is_pushable "$REPO"; then
  die "cannot push to the local image name '$REPO'.
Set one of:
  IMAGE_REPO=registry.gitlab.com/group/project
  GITLAB_PROJECT=group/project        (with optional REGISTRY, default registry.gitlab.com)
or run inside GitLab CI, where CI_REGISTRY_IMAGE is provided."
fi

log "image      $REPO"
log "tags       ${TAGS[*]}"
log "version    $VERSION"
log "revision   $REVISION"
log "platforms  ${PLATFORMS:-<native>}"
log "tool       $TOOL"

# push_image hands over to the push script, which owns all credential handling.
# Usage: push_image [--manifest] [--login-only] [tag...]
push_image() {
  local args=(--repo "$REPO")
  [ ${#PUSH_FLAGS[@]} -gt 0 ] && args+=("${PUSH_FLAGS[@]}")
  while [ $# -gt 0 ]; do
    case "$1" in
      --manifest|--login-only) args+=("$1"); shift ;;
      *) args+=(--tag "$1"); shift ;;
    esac
  done
  [ "$DRY_RUN" = "1" ] && args+=(--dry-run)
  "$(repo_root)/scripts/push-image.sh" "${args[@]}"
}

COMMON_ARGS=(
  --build-arg "VERSION=$VERSION"
  --build-arg "REVISION=$REVISION"
  --build-arg "SOURCE_URL=$SOURCE_URL"
  --build-arg "BUILD_DATE=$BUILD_DATE"
  --build-arg "RUNTIME_BASE=$RUNTIME_BASE"
  --build-arg "GO_VERSION=${GO_VERSION:-1.25}"
)
[ ${#BUILD_ARGS[@]} -gt 0 ] && COMMON_ARGS+=("${BUILD_ARGS[@]}")
[ "$NO_CACHE" = "1" ] && COMMON_ARGS+=(--no-cache)

# --- single architecture ------------------------------------------------------
if [ -z "$PLATFORMS" ] || [ "$PLATFORMS" = "${PLATFORMS%,*}" ]; then
  args=("$TOOL" build "${COMMON_ARGS[@]}")
  [ -n "$PLATFORMS" ] && args+=(--platform "$PLATFORMS")
  for t in "${TAGS[@]}"; do args+=(-t "$REPO:$t"); done
  args+=(.)
  run "${args[@]}"

  if [ "$PUSH" = "1" ]; then
    push_image "${TAGS[@]}"
  fi
  log "built $REPO:${TAGS[0]}"
  exit 0
fi

# --- multiple architectures ---------------------------------------------------
# The Dockerfile cross-compiles from the build machine's architecture, so no
# emulation is needed; only the tiny runtime layer is per-architecture.
if [ "$TOOL" = "docker" ]; then
  # The default buildx driver cannot build more than one platform, so make sure
  # a docker-container builder exists.
  builder="${BUILDX_BUILDER:-loadsim}"
  if ! docker buildx inspect "$builder" >/dev/null 2>&1; then
    run docker buildx create --name "$builder" --driver docker-container --bootstrap
  fi
  args=(docker buildx build --builder "$builder" "${COMMON_ARGS[@]}" --platform "$PLATFORMS")
  for t in "${TAGS[@]}"; do args+=(-t "$REPO:$t"); done
  # buildx cannot leave a multi-arch image in the local docker store, so a
  # multi-arch build either pushes or is discarded.
  if [ "$PUSH" = "1" ]; then
    push_image --login-only
    args+=(--push)
  else
    warn "docker buildx cannot load a multi-arch image locally; building without --push only validates the build"
  fi
  args+=(.)
  run "${args[@]}"
  log "built $REPO:${TAGS[0]} for $PLATFORMS"
  exit 0
fi

# podman: build one manifest list, then push it as a whole.
manifest="$REPO:${TAGS[0]}"
run "$TOOL" manifest rm "$manifest" 2>/dev/null || true
run "$TOOL" manifest create "$manifest"
run "$TOOL" build "${COMMON_ARGS[@]}" --platform "$PLATFORMS" --manifest "$manifest" .
for t in "${TAGS[@]:1}"; do
  run "$TOOL" tag "$manifest" "$REPO:$t"
done
if [ "$PUSH" = "1" ]; then
  push_image --manifest "${TAGS[@]}"
fi
log "built $manifest for $PLATFORMS"
