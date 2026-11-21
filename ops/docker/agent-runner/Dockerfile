# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
ARG REGISTRY_CHAINGUARD=cgr.dev
ARG REGISTRY_GHCR=ghcr.io
ARG GOOSE_VERSION=1.60.1
ARG GOOSE_SHA256_AMD64=15478c207e7ad9196d3671b158c6c8559833ab5712fcea24f972773fbbf05cd5
ARG GOOSE_SHA256_ARM64=9cf89d2f549631021b9faac6ef2cc464d60e20516ee40a4077b47b759743ad94

FROM ${REGISTRY_GHCR}/astral-sh/uv:0.13.9@sha256:d3b601f0c7b118bc1753febf7c41cc80981df678445e0254284eecf84db79a7d AS uv

FROM ${REGISTRY_CHAINGUARD}/chainguard/wolfi-base:latest@sha256:be019c1c74fe69204a42c4e10bc4d886a49bf5403ffa74374ef7baa408fc9a26 AS node
RUN printf 'https://packages.wolfi.dev/os\n' > /etc/apk/repositories \
 && apk add --no-cache nodejs-24 npm
COPY ops/docker/agent-runner/tools/moon/package.json ops/docker/agent-runner/tools/moon/package-lock.json /opt/moon/
COPY ops/docker/agent-runner/tools/openspec/package.json ops/docker/agent-runner/tools/openspec/package-lock.json /opt/openspec/
COPY ops/docker/agent-runner/tools/qwen-code/package.json ops/docker/agent-runner/tools/qwen-code/package-lock.json /opt/qwen-code/
COPY ops/docker/agent-runner/tools/opencode/package.json ops/docker/agent-runner/tools/opencode/package-lock.json /opt/opencode/
RUN for tool in moon openspec qwen-code opencode; do \
      npm ci --prefix "/opt/${tool}" --omit=dev --no-audit --no-fund || exit 1; \
    done \
 && (cd /opt/opencode/node_modules/opencode-ai && node postinstall.mjs) \
 && rm -rf /opt/qwen-code/node_modules/@lydell /opt/qwen-code/node_modules/@qwen-code/qwen-code/node_modules/@lydell

FROM scratch AS goose-archives
ARG GOOSE_VERSION
ARG GOOSE_SHA256_AMD64
ARG GOOSE_SHA256_ARM64
ADD --checksum=sha256:${GOOSE_SHA256_AMD64} https://github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}/goose-x86_64-unknown-linux-musl.tar.gz /amd64.tar.gz
ADD --checksum=sha256:${GOOSE_SHA256_ARM64} https://github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}/goose-aarch64-unknown-linux-musl.tar.gz /arm64.tar.gz

FROM ${REGISTRY_CHAINGUARD}/chainguard/wolfi-base:latest@sha256:be019c1c74fe69204a42c4e10bc4d886a49bf5403ffa74374ef7baa408fc9a26

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
    UV_PYTHON=python3.12 \
    UV_PYTHON_DOWNLOADS=never \
    UV_NO_CACHE=1

RUN printf 'https://packages.wolfi.dev/os\n' > /etc/apk/repositories \
 && apk upgrade --no-cache \
 && apk add --no-cache bash ca-certificates-bundle git nodejs-24 python-3.12 \
 && install -d /usr/local/bin \
 && ln -sf /usr/bin/python3.12 /usr/local/bin/python3 \
 && python3 --version && node --version

COPY --from=uv /uv /uvx /usr/local/bin/
COPY ops/docker/agent-runner/openhands-${OPENHANDS_VERSION}.constraints.txt /tmp/openhands.constraints.txt
COPY ops/docker/agent-runner/openhands-overrides.txt /tmp/openhands.overrides.txt
RUN uv tool install "openhands==${OPENHANDS_VERSION}" --constraints /tmp/openhands.constraints.txt --overrides /tmp/openhands.overrides.txt \
 && rm /tmp/openhands.constraints.txt /tmp/openhands.overrides.txt \
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
 && node --input-type=module -e 'for (const m of ["@lydell/node-pty", "node-pty"]) { if (await import(m).then(() => true, () => false)) { console.error(`${m} still resolves; Qwen Code runs its shell without a PTY in this image`); process.exit(1); } }' \
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

RUN uv pip install --system --break-system-packages --no-deps "pyyaml==${PYYAML_VERSION}" \
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
