# syntax=docker/dockerfile:1
#
# Multi-stage build producing a static, ~5MB image that runs as a non-root user.
#
#   docker build -t loadsim:dev .
#   docker build --build-arg RUNTIME_BASE=alpine:3.22 -t loadsim:dev-shell .   # with a shell
ARG GO_VERSION=1.25
ARG RUNTIME_BASE=scratch

# The builder always runs on the *build* machine's architecture and
# cross-compiles, so multi-arch images need no qemu emulation.
FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Dependencies first, so edits to the source do not invalidate this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
# TARGETOS/TARGETARCH are set automatically when --platform is used; the
# defaults keep a plain "docker build" working.
ARG TARGETOS=linux
ARG TARGETARCH
# CGO off keeps the binary static, so it runs on scratch and on any distro.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/loadsim ./cmd/loadsim

FROM ${RUNTIME_BASE}
# Provenance, so a pulled image can be traced back to a commit and pipeline.
ARG VERSION=dev
ARG REVISION=unknown
ARG SOURCE_URL=""
ARG BUILD_DATE=""
LABEL org.opencontainers.image.title="loadsim" \
      org.opencontainers.image.description="Configurable CPU and memory load profile simulator for Kubernetes" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.created="${BUILD_DATE}"

COPY --from=build /out/loadsim /usr/local/bin/loadsim

# 65532 is the conventional "nonroot" uid; no privileges are needed to read
# /proc/self or /sys/fs/cgroup.
USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/loadsim"]
# Default: half the container's CPU and memory limit, forever.
CMD ["--cpu", "50%", "--memory", "50%"]
