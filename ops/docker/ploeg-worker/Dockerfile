# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
ARG REGISTRY_DOCKERHUB=docker.io
ARG REGISTRY_CHAINGUARD=cgr.dev

FROM --platform=$BUILDPLATFORM ${REGISTRY_DOCKERHUB}/golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4 AS builder
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
    -o /out/ploeg-worker ./cmd/ploeg-worker

FROM ${REGISTRY_CHAINGUARD}/chainguard/wolfi-base:latest@sha256:360896efb0753a5701c48f860e17130c205eae5a264797aa3e7ea126ee2923a7 AS rootfs
RUN apk add --no-cache --no-scripts --root /rootfs --initdb --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
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
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/ploeg-worker"]
