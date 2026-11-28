# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
# Build image for Anthropic's OSS Scanner (https://github.com/anthropics/oss-scanner).
# The scanner builds this with the repository as context and network access, then scans
# the finished image offline, so every module, tool and test binary is fetched here.
FROM docker.io/golang:1.27.4-trixie@sha256:430985542100b871e108ccc00e8dcc5eef08ca14ee5825e98ebf563d8ffec4ee

ARG HELM_VERSION=4.3.0
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git curl python3 \
    && rm -rf /var/lib/apt/lists/* \
    && arch="$(dpkg --print-architecture)" \
    && curl -fsSL "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${arch}.tar.gz" | tar -xz -C /tmp \
    && install -m 0755 "/tmp/linux-${arch}/helm" /usr/local/bin/helm \
    && rm -rf "/tmp/linux-${arch}"

# scanner contract: the checkout lives inside the image, at /src
COPY . /src
WORKDIR /src
# PostgreSQL-backed tests start an embedded server, and initdb refuses to run as root.
RUN useradd --create-home --uid 1000 scanner && chown -R scanner:scanner /src /go
USER scanner

RUN go mod download all
RUN go build ./... \
    && go build -o /src/bin/ ./cmd/...
RUN go vet ./... || echo "WARNING: go vet reported problems"
RUN go test -count=1 ./... > /src/bin/go-test.log 2>&1 \
    || echo "WARNING: go test reported failures; see /src/bin/go-test.log"

ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off PATH=/src/bin:$PATH
