# LoadSim - build, test and container image targets.
#
# A local Go toolchain is optional: every target has a containerised twin
# (test-container, build-container) that runs the toolchain in an image.

BINARY      ?= loadsim
IMAGE       ?= loadsim
TAG         ?= dev
# Where images are published. Either set GITLAB_PROJECT=group/project (with
# REGISTRY, default registry.gitlab.com) or IMAGE_REPO for the full path.
GITLAB_PROJECT ?=
IMAGE_REPO  ?=
export GITLAB_PROJECT IMAGE_REPO VERSION
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO_VERSION  ?= 1.25
PLATFORMS   ?= linux/amd64,linux/arm64
CONTAINER   ?= $(shell command -v docker 2>/dev/null || command -v podman 2>/dev/null)
LDFLAGS     := -s -w -X main.version=$(VERSION)
GOIMAGE     := docker.io/library/golang:$(GO_VERSION)-alpine
CACHE       ?= $(CURDIR)/.gocache

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into ./loadsim
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/loadsim

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: test-short
test-short: ## Run tests, skipping the timing-sensitive ones
	go test -short ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format the source
	gofmt -l -w .

.PHONY: check
check: fmt vet test ## Format, vet and test

.PHONY: test-container
test-container: ## Run the tests inside a Go container (no local Go needed)
	$(CONTAINER) run --rm -v $(CURDIR):/src:z -w /src \
		-e GOFLAGS=-mod=mod -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomodcache \
		$(GOIMAGE) sh -c 'gofmt -l . && go vet ./... && go test ./...'

.PHONY: image
image: ## Build the container image
	scripts/build-image.sh --tag $(TAG)

.PHONY: image-shell
image-shell: ## Build the image on alpine, so it has a shell for debugging
	scripts/build-image.sh --tag $(TAG)-shell --shell

.PHONY: image-push
image-push: ## Build and push to the registry (set GITLAB_PROJECT or IMAGE_REPO)
	scripts/build-image.sh --tag $(TAG) --push

.PHONY: image-release
image-release: ## Build and push a multi-arch release (TAG plus latest)
	scripts/build-image.sh --tag $(TAG) --latest --platforms $(PLATFORMS) --push

.PHONY: push
push: ## Push already-built tags (set GITLAB_PROJECT or IMAGE_REPO)
	scripts/push-image.sh --tag $(TAG)

.PHONY: image-plan
image-plan: ## Show what a build and push would do, without doing it
	scripts/build-image.sh --tag $(TAG) --push --dry-run

.PHONY: run
run: build ## Run locally with a constant 25% of one core and 128Mi
	./$(BINARY) --cpu 250m --memory 128Mi --cpu-limit 1 --memory-limit 1Gi

.PHONY: run-image
run-image: image ## Run the image with a 1 CPU / 512Mi limit, like a pod
	$(CONTAINER) run --rm -it --cpus 1 --memory 512m -p 8080:8080 \
		$(IMAGE):$(TAG) --preset startup-burst

.PHONY: plan
plan: build ## Chart a profile without running it: make plan PROFILE=examples/startup-burst.yaml
	./$(BINARY) plan --config $(or $(PROFILE),examples/startup-burst.yaml)

.PHONY: presets
presets: build ## List the built-in profiles
	./$(BINARY) presets

.PHONY: deploy
deploy: ## Apply the example Kubernetes manifests
	kubectl apply -k deploy/k8s

.PHONY: undeploy
undeploy: ## Delete the example Kubernetes manifests
	kubectl delete -k deploy/k8s

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BINARY) dist $(CACHE)
