# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
# Base registries ARG-overridable so in-cluster builds route Docker Hub pulls through the
# Harbor pull-through proxy. distroless lives on gcr.io — no Harbor proxy project yet
# (see homelab-cluster harbor-proxy-config) — pulled direct until then.
ARG REGISTRY_DOCKERHUB=docker.io
ARG REGISTRY_GCR=gcr.io

FROM --platform=$BUILDPLATFORM ${REGISTRY_DOCKERHUB}/golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d AS builder
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG IMAGE_VERSION=0.0.0-dev
ENV CGO_ENABLED=0
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_VERSION}" \
    -o /out/ploegd ./cmd/ploegd && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_VERSION}" \
    -o /out/ploeg-worker ./cmd/ploeg-worker && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_VERSION}" \
    -o /out/ploeg-mcp ./cmd/ploeg-mcp

FROM ${REGISTRY_GCR}/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
ARG IMAGE_CREATED=1970-01-01T00:00:00Z
ARG IMAGE_VERSION=0.0.0-dev
ARG IMAGE_REVISION=unknown
LABEL org.opencontainers.image.title="ploegd" \
      org.opencontainers.image.description="Ploeg dispatch plane daemon (pre-alpha)" \
      org.opencontainers.image.source="https://github.com/ploeg-hq/ploeg" \
      org.opencontainers.image.url="https://github.com/ploeg-hq/ploeg" \
      org.opencontainers.image.vendor="Ploeg contributors" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.created="${IMAGE_CREATED}" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}"
COPY LICENSE NOTICE THIRD-PARTY-NOTICES /usr/share/doc/ploeg/
COPY --from=builder /out/ploegd /usr/local/bin/ploegd
# ploeg-worker rides along; the executor's initContainer extracts it via
# `ploeg-worker install <dst>` (self-copy — distroless has no shell).
COPY --from=builder /out/ploeg-worker /usr/local/bin/ploeg-worker
COPY --from=builder /out/ploeg-mcp /usr/local/bin/ploeg-mcp
ENTRYPOINT ["/usr/local/bin/ploegd"]
