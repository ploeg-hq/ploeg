# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
ARG REGISTRY_DOCKERHUB=docker.io
ARG REGISTRY_CHAINGUARD=cgr.dev

FROM --platform=$BUILDPLATFORM ${REGISTRY_DOCKERHUB}/golang:1.27.4-trixie@sha256:430985542100b871e108ccc00e8dcc5eef08ca14ee5825e98ebf563d8ffec4ee AS builder
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
    -o /out/ploeg-worker ./cmd/ploeg-worker && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_VERSION}" \
    -o /out/ploeg-credential-gateway ./cmd/ploeg-credential-gateway && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" \
    -o /out/ploeg-mcp-stdio-bridge ./cmd/ploeg-mcp-stdio-bridge

FROM ${REGISTRY_CHAINGUARD}/chainguard/wolfi-base:latest@sha256:5172a213d2bba8b1c9ca11957db65562b52b0f9844583a0347318ea5fd8e3aa5 AS rootfs
RUN printf 'https://packages.wolfi.dev/os\n' > /etc/apk/repositories \
 && apk add --no-cache --no-scripts --root /rootfs --initdb --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
      wolfi-baselayout ca-certificates-bundle git \
 && rm -rf /rootfs/var/cache/apk /rootfs/etc/apk/repositories \
 && printf 'nonroot:x:65532:65532:nonroot:/home/nonroot:/sbin/nologin\n' >> /rootfs/etc/passwd \
 && printf 'nonroot:x:65532:\n' >> /rootfs/etc/group \
 && install -d -o 65532 -g 65532 -m 0750 /rootfs/home/nonroot \
 && install -d -m 1777 /rootfs/tmp \
 && chroot /rootfs /usr/bin/git --version

FROM scratch
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
COPY --from=rootfs /rootfs/ /
COPY LICENSE NOTICE THIRD-PARTY-NOTICES /usr/share/doc/ploeg/
COPY --from=builder /out/ploeg-worker /usr/local/bin/ploeg-worker
COPY --from=builder /out/ploeg-credential-gateway /usr/local/bin/ploeg-credential-gateway
COPY --from=builder /out/ploeg-mcp-stdio-bridge /usr/local/bin/ploeg-mcp-stdio-bridge
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/ploeg-worker"]
