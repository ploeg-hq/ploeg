# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
ARG REGISTRY_DOCKERHUB=docker.io
ARG REGISTRY_GCR=gcr.io

FROM --platform=$BUILDPLATFORM ${REGISTRY_DOCKERHUB}/golang:1.27-trixie@sha256:3b77fc618ec235a1ab412de7737f120dd507c57e8d87de4cbb7994fb94275ed5 AS builder
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG IMAGE_VERSION=0.0.0-dev
ENV CGO_ENABLED=0
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_VERSION}" \
    -o /out/ploeg-worker ./cmd/ploeg-worker

FROM ${REGISTRY_DOCKERHUB}/debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad AS git
RUN apt-get update \
 && apt-get install -y --no-install-recommends git \
 && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    programs="/usr/bin/git /usr/lib/git-core/git /usr/lib/git-core/git-remote-http /usr/lib/git-core/git-remote-https"; \
    libraries=$(ldd $programs | awk '$2 == "=>" && $3 ~ /^\// { print $3 }' | sort -u | grep -Ev '/(libc|libm|libpthread|libdl|librt|libresolv)\.so|/ld-linux'); \
    for file in $programs $libraries; do \
      directory=$(readlink -f "$(dirname "$file")"); \
      mkdir -p "/rootfs$directory"; \
      cp -L "$file" "/rootfs$directory/$(basename "$file")"; \
    done; \
    packages=$(for file in $programs $libraries; do dpkg -S "$(readlink -f "$file")" 2>/dev/null || dpkg -S "$file"; done | cut -d: -f1 | sort -u); \
    mkdir -p /rootfs/var/lib/dpkg/status.d /rootfs/usr/share/doc; \
    for package in $packages; do \
      dpkg-query -s "$package" > "/rootfs/var/lib/dpkg/status.d/$package"; \
      mkdir -p "/rootfs/usr/share/doc/$package"; \
      cp "/usr/share/doc/$package/copyright" "/rootfs/usr/share/doc/$package/copyright"; \
    done; \
    mkdir -p /rootfs/usr/share/git-core/templates; \
    printf '%s\n' $packages > /rootfs/usr/share/doc/ploeg-worker-git-packages.txt; \
    /usr/bin/git --version

FROM ${REGISTRY_GCR}/distroless/base-debian13:nonroot@sha256:f9518b74815721f537a7458cc693b4406208c04fce23bbffab511516117f34fd
ARG IMAGE_CREATED=1970-01-01T00:00:00Z
ARG IMAGE_VERSION=0.0.0-dev
ARG IMAGE_REVISION=unknown
LABEL org.opencontainers.image.title="ploeg-worker" \
      org.opencontainers.image.description="Ploeg worker: holds a Run's credentials and drives the harness in its own container (ADR-0138)" \
      org.opencontainers.image.source="https://github.com/ploeg-hq/ploeg" \
      org.opencontainers.image.url="https://github.com/ploeg-hq/ploeg" \
      org.opencontainers.image.vendor="Ploeg contributors" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.created="${IMAGE_CREATED}" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}"
COPY --from=git /rootfs/ /
COPY LICENSE NOTICE THIRD-PARTY-NOTICES /usr/share/doc/ploeg/
COPY --from=builder /out/ploeg-worker /usr/local/bin/ploeg-worker
ENTRYPOINT ["/usr/local/bin/ploeg-worker"]
