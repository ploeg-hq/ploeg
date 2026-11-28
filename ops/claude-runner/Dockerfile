ARG REGISTRY_CHAINGUARD=cgr.dev

FROM ${REGISTRY_CHAINGUARD}/chainguard/wolfi-base:latest@sha256:5172a213d2bba8b1c9ca11957db65562b52b0f9844583a0347318ea5fd8e3aa5

RUN printf 'https://packages.wolfi.dev/os\n' > /etc/apk/repositories \
 && apk upgrade --no-cache \
 && apk add --no-cache bash ca-certificates-bundle coreutils curl gawk git gpgv grep jq openssh-client sed
RUN git config --system user.name "Claude" \
 && git config --system user.email "noreply@anthropic.com" \
 && git config --system --add safe.directory '*'

COPY LICENSE NOTICE /usr/share/doc/ploeg/
COPY ops/claude-runner/claude-code.env /opt/ploeg/claude-runner/claude-code.env
COPY --chmod=0755 ops/claude-runner/install-claude-code /usr/local/bin/ploeg-install-claude-code
COPY --chmod=0755 ops/claude-runner/claude /usr/local/bin/claude
COPY --chmod=0755 ops/claude-runner/checkout ops/claude-runner/post-session /opt/ploeg/claude-runner/hooks/
COPY --chmod=0755 ops/claude-runner/spawn-runner /opt/ploeg/claude-runner/orchestrator-hooks/
COPY --chmod=0755 ops/claude-runner/runner-from-work-order /usr/local/bin/ploeg-claude-runner-from-work-order
RUN adduser -D -u 1000 -s /bin/bash runner \
 && install -d -m 0755 /opt/claude-code
COPY --chown=runner:runner ops/claude-runner/claude-home/ /home/runner/.claude/
RUN chmod 0755 /home/runner/.claude/hooks/save-final-message

USER runner
WORKDIR /home/runner
ENTRYPOINT ["claude"]
