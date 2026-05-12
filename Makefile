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
	-cp internal/bootstrap/crds/cloudflare.io_cloudflareoperators.yaml chart/cloudflare-operator/templates/crd.yaml

.PHONY: test
test: tools
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test ./... -coverprofile=cover.out -race

.PHONY: build
build:
	CGO_ENABLED=0 go build -o bin/manager ./cmd/manager

.PHONY: lint
lint:
	go vet ./...
	gofmt -l . | tee /dev/stderr | (! read)

.PHONY: clean
clean:
	rm -rf bin cover.out
