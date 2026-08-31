#!/usr/bin/env bash
# Shared helpers for the LoadSim build and publish scripts.
# Sourced, not executed.

# --- output -------------------------------------------------------------------

if [ -t 2 ] && [ -z "${NO_COLOR:-}" ]; then
  _c_dim=$'\033[2m'; _c_red=$'\033[31m'; _c_yellow=$'\033[33m'; _c_off=$'\033[0m'
else
  _c_dim=""; _c_red=""; _c_yellow=""; _c_off=""
fi

log()  { printf '%s==>%s %s\n' "$_c_dim" "$_c_off" "$*" >&2; }
warn() { printf '%swarning:%s %s\n' "$_c_yellow" "$_c_off" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$_c_red" "$_c_off" "$*" >&2; exit 1; }

# run echoes a command and runs it, honouring DRY_RUN=1.
run() {
  printf '%s+%s' "$_c_dim" "$_c_off" >&2
  printf ' %q' "$@" >&2
  printf '\n' >&2
  [ "${DRY_RUN:-0}" = "1" ] && return 0
  "$@"
}

# run_secret is run() for commands whose arguments must not be printed.
run_secret() {
  printf '%s+%s %s\n' "$_c_dim" "$_c_off" "$1 [arguments hidden]" >&2
  [ "${DRY_RUN:-0}" = "1" ] && return 0
  "$@"
}

# --- environment --------------------------------------------------------------

repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

# container_tool picks the container CLI: $CONTAINER_TOOL, then docker, podman.
container_tool() {
  if [ -n "${CONTAINER_TOOL:-}" ]; then
    command -v "$CONTAINER_TOOL" >/dev/null 2>&1 || die "CONTAINER_TOOL=$CONTAINER_TOOL not found in PATH"
    printf '%s' "$CONTAINER_TOOL"
    return
  fi
  for tool in docker podman; do
    if command -v "$tool" >/dev/null 2>&1; then
      printf '%s' "$tool"
      return
    fi
  done
  die "neither docker nor podman found; set CONTAINER_TOOL"
}

# sanitize_tag makes an arbitrary string a valid OCI tag:
# [A-Za-z0-9_][A-Za-z0-9._-]{0,127}
sanitize_tag() {
  local t="$1"
  t="${t//[^A-Za-z0-9._-]/-}"
  case "$t" in
    [A-Za-z0-9_]*) ;;
    *) t="v$t" ;;
  esac
  printf '%s' "${t:0:128}"
}

# resolve_version returns, in order: $VERSION, the CI tag, a git description,
# the CI commit sha, or "dev".
resolve_version() {
  if [ -n "${VERSION:-}" ]; then printf '%s' "$VERSION"; return; fi
  if [ -n "${CI_COMMIT_TAG:-}" ]; then printf '%s' "$CI_COMMIT_TAG"; return; fi
  local desc
  if desc=$(git describe --tags --always --dirty 2>/dev/null) && [ -n "$desc" ]; then
    printf '%s' "$desc"; return
  fi
  if [ -n "${CI_COMMIT_SHORT_SHA:-}" ]; then printf '%s' "$CI_COMMIT_SHORT_SHA"; return; fi
  printf 'dev'
}

# resolve_revision returns the full commit sha when one is available.
resolve_revision() {
  if [ -n "${CI_COMMIT_SHA:-}" ]; then printf '%s' "$CI_COMMIT_SHA"; return; fi
  git rev-parse HEAD 2>/dev/null || printf 'unknown'
}

# resolve_source_url returns the project URL for image provenance labels.
resolve_source_url() {
  if [ -n "${SOURCE_URL:-}" ]; then printf '%s' "$SOURCE_URL"; return; fi
  if [ -n "${CI_PROJECT_URL:-}" ]; then printf '%s' "$CI_PROJECT_URL"; return; fi
  git remote get-url origin 2>/dev/null || printf ''
}

# resolve_repo returns the image repository (registry + path, no tag).
#
#   IMAGE_REPO            used verbatim if set
#   CI_REGISTRY_IMAGE     set by GitLab CI, e.g. registry.gitlab.com/group/project
#   REGISTRY + GITLAB_PROJECT   for local pushes, e.g. registry.gitlab.com + group/project
#   otherwise             a local-only name, which cannot be pushed
#
# IMAGE_NAME (default empty) appends a sub-repository, so
# GITLAB_PROJECT=group/project IMAGE_NAME=loadsim gives
# registry.gitlab.com/group/project/loadsim.
resolve_repo() {
  local base=""
  if [ -n "${IMAGE_REPO:-}" ]; then
    printf '%s' "$IMAGE_REPO"
    return
  fi
  if [ -n "${CI_REGISTRY_IMAGE:-}" ]; then
    base="$CI_REGISTRY_IMAGE"
  elif [ -n "${GITLAB_PROJECT:-}" ]; then
    base="${REGISTRY:-registry.gitlab.com}/${GITLAB_PROJECT}"
  else
    printf '%s' "${LOCAL_IMAGE:-loadsim}"
    return
  fi
  base="${base%/}"
  if [ -n "${IMAGE_NAME:-}" ] && [ "${base##*/}" != "$IMAGE_NAME" ]; then
    base="$base/$IMAGE_NAME"
  fi
  printf '%s' "$base"
}

# is_pushable reports whether the repository has a registry host in it.
is_pushable() {
  local repo="$1" first
  first="${repo%%/*}"
  case "$repo" in
    */*) case "$first" in *.*|*:*|localhost) return 0 ;; esac ;;
  esac
  return 1
}

# registry_host returns the registry part of a repository reference.
registry_host() {
  local repo="$1"
  if is_pushable "$repo"; then
    printf '%s' "${repo%%/*}"
  else
    printf ''
  fi
}
