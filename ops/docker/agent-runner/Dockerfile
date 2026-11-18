# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
ARG REGISTRY_DOCKERHUB=docker.io
ARG REGISTRY_GHCR=ghcr.io
ARG GOOSE_VERSION=1.57.0
ARG GOOSE_SHA256_AMD64=6c3a05e30e1a425dc06a410dbe24b3f3697c9721b1ea2340434ddda6b64267ae
ARG GOOSE_SHA256_ARM64=00a32522f0421ed72fc06f36bfc44c8e51cffefb4daf22a81bb84ccd29d1a1ce

FROM ${REGISTRY_GHCR}/astral-sh/uv:0.13.7@sha256:96f40d3904aad8d0bd010539001d36c8e2188b981e1334471a90c4a1d93bb490 AS uv

FROM ${REGISTRY_DOCKERHUB}/node:24.22.3-alpine3.24@sha256:116b800c3754b440ee0aa3ed2f20f5bf3d99a554bfd9adeea09ea8017f6e85f5 AS node
COPY ops/docker/agent-runner/tools/moon/package.json ops/docker/agent-runner/tools/moon/package-lock.json /opt/moon/
COPY ops/docker/agent-runner/tools/openspec/package.json ops/docker/agent-runner/tools/openspec/package-lock.json /opt/openspec/
COPY ops/docker/agent-runner/tools/qwen-code/package.json ops/docker/agent-runner/tools/qwen-code/package-lock.json /opt/qwen-code/
COPY ops/docker/agent-runner/tools/opencode/package.json ops/docker/agent-runner/tools/opencode/package-lock.json /opt/opencode/
RUN for tool in moon openspec qwen-code opencode; do \
      npm ci --prefix "/opt/${tool}" --omit=dev --no-audit --no-fund || exit 1; \
    done \
 && rm -rf /opt/qwen-code/node_modules/@lydell /opt/qwen-code/node_modules/@qwen-code/qwen-code/node_modules/@lydell

FROM scratch AS goose-archives
ARG GOOSE_VERSION
ARG GOOSE_SHA256_AMD64
ARG GOOSE_SHA256_ARM64
ADD --checksum=sha256:${GOOSE_SHA256_AMD64} https://github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}/goose-x86_64-unknown-linux-musl.tar.gz /amd64.tar.gz
ADD --checksum=sha256:${GOOSE_SHA256_ARM64} https://github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}/goose-aarch64-unknown-linux-musl.tar.gz /arm64.tar.gz

FROM ${REGISTRY_DOCKERHUB}/python:3.12.16-alpine3.24@sha256:c5ba3deef00c7758561a0f03c26f44f10589263d40e732b8748e0d1f38f6dd89

ARG TARGETARCH
ARG IMAGE_CREATED=1970-01-01T00:00:00Z
ARG IMAGE_VERSION=0.0.0-dev
ARG IMAGE_REVISION=unknown
ARG GOOSE_VERSION
ARG OPENHANDS_VERSION=1.16.0
# renovate: datasource=pypi depName=pyyaml
ARG PYYAML_VERSION=6.0.3

ENV PYTHONUNBUFFERED=1 \
    OPENHANDS_SUPPRESS_BANNER=1 \
    PYTHONWARNINGS=ignore::DeprecationWarning \
    UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_TOOL_DIR=/opt/uv/tools \
    UV_PYTHON_DOWNLOADS=never \
    UV_NO_CACHE=1

RUN apk add --no-cache bash ca-certificates git libstdc++

COPY --from=uv /uv /uvx /usr/local/bin/
COPY ops/docker/agent-runner/openhands-${OPENHANDS_VERSION}.constraints.txt /tmp/openhands.constraints.txt
RUN uv tool install "openhands==${OPENHANDS_VERSION}" --constraints /tmp/openhands.constraints.txt \
 && rm /tmp/openhands.constraints.txt \
 && openhands --version \
 && /opt/uv/tools/openhands/bin/python -c "import os, shutil; os.setgid(1000); os.setuid(1000); assert shutil.which('openhands'), 'openhands is not executable as uid 1000, the restricted Pod Security Standard user Ploeg runs harnesses as'" \
 && /opt/uv/tools/openhands/bin/python -c "import inspect, lmnr; assert 'rollout_entrypoint' in str(inspect.signature(lmnr.observe)), 'lmnr.observe lacks rollout_entrypoint, which openhands-sdk tracing passes'"

RUN AGENT_STORE="$(find /opt/uv/tools/openhands -name agent_store.py -path '*/openhands_cli/*')" \
 && test -n "${AGENT_STORE}" \
 && grep -q 'load_public_skills=True' "${AGENT_STORE}" \
 && sed -i 's/load_public_skills=True/load_public_skills=False/' "${AGENT_STORE}" \
 && grep -q 'load_public_skills=False' "${AGENT_STORE}" \
 && ! grep -q 'load_public_skills=True' "${AGENT_STORE}" \
 && openhands --version | head -1 > /etc/openhands-version \
 && test -s /etc/openhands-version

ENV LITELLM_LOCAL_MODEL_COST_MAP=True

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -sf /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -sf /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && node --version && npm --version

COPY --from=node /opt/moon /opt/moon
COPY --from=node /opt/openspec /opt/openspec
RUN ln -s /opt/moon/node_modules/.bin/moon /usr/local/bin/moon \
 && ln -s /opt/moon/node_modules/.bin/moonx /usr/local/bin/moonx \
 && ln -s /opt/openspec/node_modules/.bin/openspec /usr/local/bin/openspec \
 && moon --version && moonx --help >/dev/null && openspec --version

ENV OPENSPEC_TELEMETRY=0 \
    DO_NOT_TRACK=1 \
    QWEN_USAGE_STATISTICS_ENABLED=false \
    GOOSE_TELEMETRY_OFF=1

COPY --from=node /opt/qwen-code /opt/qwen-code
RUN ln -s /opt/qwen-code/node_modules/.bin/qwen /usr/local/bin/qwen \
 && cd /opt/qwen-code/node_modules/@qwen-code/qwen-code \
 && node --input-type=module -e 'for (const m of ["@lydell/node-pty", "node-pty"]) { if (await import(m).then(() => true, () => false)) { console.error(`${m} still resolves: its glibc prebuild segfaults on musl`); process.exit(1); } }' \
 && test "$(qwen --version)" = "$(node -p 'require("/opt/qwen-code/package.json").dependencies["@qwen-code/qwen-code"]')"

COPY --from=node /opt/opencode /opt/opencode
RUN ln -s /opt/opencode/node_modules/.bin/opencode /usr/local/bin/opencode \
 && test "$(opencode --version)" = "$(node -p 'require("/opt/opencode/package.json").dependencies["opencode-ai"]')" \
 && opencode acp --help >/dev/null

RUN --mount=type=bind,from=goose-archives,target=/tmp/goose \
    tar -xzf "/tmp/goose/${TARGETARCH}.tar.gz" -C /usr/local/bin ./goose \
 && chown 0:0 /usr/local/bin/goose \
 && chmod 0755 /usr/local/bin/goose \
 && goose --version | grep -F "${GOOSE_VERSION}"

RUN pip install --no-cache-dir --no-deps "pyyaml==${PYYAML_VERSION}" \
 && python3 -c "import yaml; print('pyyaml', yaml.__version__)"

COPY LICENSE NOTICE /usr/share/doc/ploeg/
COPY --chmod=0755 ops/docker/agent-runner/litellm-key.mjs /usr/local/bin/litellm-key
COPY --chmod=0755 ops/docker/agent-runner/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN if litellm-key mint 2>/dev/null; then echo "litellm-key mint succeeded with no configuration" >&2; exit 1; fi

WORKDIR /workspace

LABEL org.opencontainers.image.title="agent-runner" \
      org.opencontainers.image.description="Ploeg agent image: the OpenHands, OpenCode, Qwen Code and goose harnesses with git, moon and openspec" \
      org.opencontainers.image.source="https://github.com/ploeg-hq/ploeg" \
      org.opencontainers.image.url="https://github.com/ploeg-hq/ploeg" \
      org.opencontainers.image.documentation="https://github.com/ploeg-hq/ploeg/blob/development/ops/docker/agent-runner/README.md" \
      org.opencontainers.image.vendor="Ploeg contributors" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.created="${IMAGE_CREATED}" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}"

ENTRYPOINT ["docker-entrypoint.sh"]
