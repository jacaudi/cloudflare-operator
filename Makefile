SHELL := /usr/bin/env bash
GOBIN := $(shell pwd)/bin
CONTROLLER_GEN := $(GOBIN)/controller-gen
ENVTEST := $(GOBIN)/setup-envtest
ENVTEST_K8S_VERSION ?= 1.30.0

.PHONY: all
all: generate test build

.PHONY: tools
tools:
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest

.PHONY: generate
generate: tools
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=internal/bootstrap/crds
	# Copy CRD YAML to chart for the CloudflareOperator only
	-cp internal/bootstrap/crds/cloudflare.io_cloudflareoperators.yaml chart/templates/crd.yaml
	# Inject helm.sh/resource-policy: keep annotation to preserve CRD on helm uninstall
	@awk '/controller-gen\.kubebuilder\.io\/version:/ {print $$0; print "    helm.sh/resource-policy: keep"; next} {print}' \
		chart/templates/crd.yaml > chart/templates/crd.yaml.tmp && \
		mv chart/templates/crd.yaml.tmp chart/templates/crd.yaml
	@grep -q "helm.sh/resource-policy: keep" chart/templates/crd.yaml \
		|| (echo "ERROR: failed to inject helm.sh/resource-policy into chart/templates/crd.yaml"; exit 1)

.PHONY: test
test: tools
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test ./... -coverprofile=cover.out -race

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build
build:
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o bin/manager ./cmd/manager

.PHONY: lint
lint:
	go tool golangci-lint run ./...
	gofmt -l . | tee /dev/stderr | (! read)

.PHONY: clean
clean:
	rm -rf bin cover.out
