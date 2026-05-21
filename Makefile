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
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=bin/crd-staging
	# Copy the five bundle CRDs into the chart as templates, gated by
	# .Values.crds.install and with the helm.sh/resource-policy: keep
	# annotation gated by .Values.crds.keep (Helm owns CRDs).
	@for kind in cloudflarednsrecords cloudflarerulesets cloudflaretunnels cloudflarezoneconfigs cloudflarezones; do \
		src=bin/crd-staging/cloudflare.io_$$kind.yaml ; \
		dst=chart/templates/crd-$$kind.yaml ; \
		{ \
		  echo '{{- if .Values.crds.install }}' ; \
		  awk '/controller-gen\.kubebuilder\.io\/version:/ {print $$0; print "{{- if .Values.crds.keep }}"; print "    helm.sh/resource-policy: keep"; print "{{- end }}"; next} {print}' "$$src" ; \
		  echo '{{- end }}' ; \
		} > "$$dst" ; \
		grep -q '{{- if .Values.crds.install }}' "$$dst" || { echo "ERROR: crds.install gate not injected into $$dst" ; exit 1 ; } ; \
		grep -q 'helm.sh/resource-policy: keep' "$$dst" || { echo "ERROR: resource-policy line not injected into $$dst" ; exit 1 ; } ; \
	done

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
