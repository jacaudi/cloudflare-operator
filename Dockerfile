# Build the manager binary
FROM golang:1.26.3 AS builder
ARG TARGETOS
ARG TARGETARCH
# Version stamping — mirrors `make build` LDFLAGS so the binary's
# `--version` reports real values. Defaults keep plain `docker build`
# working; CI passes the real values via --build-arg.
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=

WORKDIR /workspace
# Copy the Go module manifests and download deps first so this layer is
# cached unless go.mod/go.sum change.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the source needed to build ./cmd/manager.
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# Build the operator binary (single binary, three roles via --mode).
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o manager ./cmd/manager

# Minimal final image: scratch + CA certs, non-root.
FROM scratch
WORKDIR /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
