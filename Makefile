SHELL := /usr/bin/env bash
GOBIN := $(shell pwd)/bin
CONTROLLER_GEN := $(GOBIN)/controller-gen
ENVTEST := $(GOBIN)/setup-envtest
HELM_DOCS := $(GOBIN)/helm-docs
CRD_REF_DOCS := $(GOBIN)/crd-ref-docs
ENVTEST_K8S_VERSION ?= 1.30.0

.PHONY: all
all: generate test build

.PHONY: tools
tools:
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen
	GOBIN=$(GOBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest
	GOBIN=$(GOBIN) go install github.com/norwoodj/helm-docs/cmd/helm-docs@latest
	GOBIN=$(GOBIN) go install github.com/elastic/crd-ref-docs@latest

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
	# Regenerate chart/README.md from values.yaml + README.md.gotmpl.
	$(HELM_DOCS) --chart-search-root=chart --template-files=README.md.gotmpl --output-file=README.md
	# Regenerate docs/crd-reference.md from the api/v2alpha1 Go types.
	# Templates forked under hack/crd-ref-docs-templates/ to promote CRDs
	# (types with a GVK) to H2 with horizontal-rule separators; sub-types
	# stay at H4 nested under whichever CRD they're referenced from.
	$(CRD_REF_DOCS) \
		--source-path=api/v2alpha1 \
		--config=hack/crd-ref-docs-config.yaml \
		--renderer=markdown \
		--templates-dir=hack/crd-ref-docs-templates \
		--output-path=docs/crd-reference.md

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
