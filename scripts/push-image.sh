#!/usr/bin/env bash
#
# Push already-built LoadSim images to a container registry, GitLab's included.
#
# Typical use:
#   GITLAB_PROJECT=my-group/loadsim GITLAB_USER=me GITLAB_TOKEN=glpat-... \
#     scripts/push-image.sh --tag v0.2.0 --tag latest
#
# In GitLab CI no credentials need to be passed: CI_REGISTRY_USER and
# CI_REGISTRY_PASSWORD (the job token) are used automatically.
#
# Credentials are never printed and are passed to the CLI on stdin.
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() {
  cat <<'USAGE'
Usage: scripts/push-image.sh [options]

Options:
  -t, --tag TAG      tag to push; repeatable (default: version from git or $VERSION)
      --repo REPO    image repository (default: from CI_REGISTRY_IMAGE / GITLAB_PROJECT)
      --manifest     the tags are multi-arch manifest lists (podman)
      --login-only   authenticate and exit
      --skip-login   assume the registry is already authenticated
      --insecure     allow an untrusted TLS certificate or plain HTTP (podman only;
                     for a local test registry, not for GitLab)
      --dry-run      print the commands without running them
  -h, --help         this text

Credentials, in the order they are tried:
  CI_REGISTRY_USER + CI_REGISTRY_PASSWORD    provided by GitLab CI
  gitlab-ci-token  + CI_JOB_TOKEN            provided by GitLab CI
  GITLAB_USER      + GITLAB_TOKEN            a personal access token or deploy
                                             token with the write_registry scope
  REGISTRY_USER    + REGISTRY_PASSWORD       any other registry
  none                                       an existing docker/podman login
USAGE
}

TAGS=()
MANIFEST=0
INSECURE="${INSECURE:-0}"
LOGIN_ONLY=0
SKIP_LOGIN="${SKIP_LOGIN:-0}"

while [ $# -gt 0 ]; do
  case "$1" in
    -t|--tag)     [ -n "${2:-}" ] || die "--tag needs a value (an unset variable in the caller?)"
                  TAGS+=("$2"); shift 2 ;;
    --repo)       IMAGE_REPO="$2"; shift 2 ;;
    --manifest)   MANIFEST=1; shift ;;
    --login-only) LOGIN_ONLY=1; shift ;;
    --skip-login) SKIP_LOGIN=1; shift ;;
    --insecure)   INSECURE=1; shift ;;
    --dry-run)    DRY_RUN=1; shift ;;
    -h|--help)    usage; exit 0 ;;
    *)            die "unknown option: $1 (try --help)" ;;
  esac
done
export DRY_RUN="${DRY_RUN:-0}"

cd "$(repo_root)"

TOOL=$(container_tool)
REPO=$(resolve_repo)
is_pushable "$REPO" || die "'$REPO' has no registry host.
Set IMAGE_REPO=registry.gitlab.com/group/project, or GITLAB_PROJECT=group/project."

HOST=$(registry_host "$REPO")

# --- authentication -----------------------------------------------------------
login() {
  local user="" pass="" source=""
  if [ -n "${CI_REGISTRY_PASSWORD:-}" ]; then
    user="${CI_REGISTRY_USER:-gitlab-ci-token}"; pass="$CI_REGISTRY_PASSWORD"; source="CI_REGISTRY_PASSWORD"
  elif [ -n "${CI_JOB_TOKEN:-}" ]; then
    user="gitlab-ci-token"; pass="$CI_JOB_TOKEN"; source="CI_JOB_TOKEN"
  elif [ -n "${GITLAB_TOKEN:-}" ]; then
    user="${GITLAB_USER:-${USER:-}}"; pass="$GITLAB_TOKEN"; source="GITLAB_TOKEN"
    [ -n "$user" ] || die "GITLAB_TOKEN is set but GITLAB_USER is not; use your GitLab username or the deploy token's username"
  elif [ -n "${REGISTRY_PASSWORD:-}" ]; then
    user="${REGISTRY_USER:-}"; pass="$REGISTRY_PASSWORD"; source="REGISTRY_PASSWORD"
    [ -n "$user" ] || die "REGISTRY_PASSWORD is set but REGISTRY_USER is not"
  else
    warn "no credentials in the environment; using whatever '$TOOL login' has cached for $HOST"
    return 0
  fi

  log "authenticating to $HOST as $user (from $source)"
  if [ "$DRY_RUN" = "1" ]; then
    printf '%s+%s %s login --username %s --password-stdin %s\n' "$_c_dim" "$_c_off" "$TOOL" "$user" "$HOST" >&2
    return 0
  fi
  local login_args=(login --username "$user" --password-stdin)
  [ "$INSECURE" = "1" ] && [ "$TOOL" = "podman" ] && login_args+=(--tls-verify=false)
  printf '%s' "$pass" | "$TOOL" "${login_args[@]}" "$HOST" \
    || die "login to $HOST failed"
}

[ "$SKIP_LOGIN" = "1" ] || login
[ "$LOGIN_ONLY" = "1" ] && exit 0

# --- push ---------------------------------------------------------------------
if [ ${#TAGS[@]} -eq 0 ]; then
  TAGS=("$(sanitize_tag "$(resolve_version)")")
fi

tls_args=()
if [ "$INSECURE" = "1" ]; then
  [ "$TOOL" = "podman" ] || die "--insecure is only supported with podman; configure the docker daemon instead"
  tls_args=(--tls-verify=false)
fi

for t in "${TAGS[@]}"; do
  ref="$REPO:$t"
  if [ "$MANIFEST" = "1" ] && [ "$TOOL" = "podman" ]; then
    # --all pushes every architecture in the list, not just the local one.
    run "$TOOL" manifest push --all "${tls_args[@]}" "$ref" "docker://$ref"
  else
    run "$TOOL" push "${tls_args[@]}" "$ref"
  fi
  log "pushed $ref"
done

cat >&2 <<SUMMARY

Pull it with:
  docker pull $REPO:${TAGS[0]}

Use it in Kubernetes:
  kubectl create secret docker-registry gitlab-registry \\
    --docker-server=$HOST --docker-username='<user>' --docker-password='<token>'
  kubectl patch serviceaccount default \\
    -p '{"imagePullSecrets":[{"name":"gitlab-registry"}]}'
  cd deploy/k8s && kustomize edit set image loadsim=$REPO:${TAGS[0]} && kubectl apply -k .
SUMMARY
