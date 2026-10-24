ARG REGISTRY_CHAINGUARD=cgr.dev

FROM ${REGISTRY_CHAINGUARD}/chainguard/wolfi-base:latest@sha256:360896efb0753a5701c48f860e17130c205eae5a264797aa3e7ea126ee2923a7

RUN apk add --no-cache bash ca-certificates-bundle coreutils curl gawk git gpgv grep jq openssh-client sed
RUN git config --system user.name "Claude" \
 && git config --system user.email "noreply@anthropic.com" \
 && git config --system --add safe.directory '*'

COPY LICENSE NOTICE /usr/share/doc/ploeg/
COPY ops/claude-runner/claude-code.env /opt/ploeg/claude-runner/claude-code.env
COPY --chmod=0755 ops/claude-runner/install-claude-code /usr/local/bin/ploeg-install-claude-code
COPY --chmod=0755 ops/claude-runner/claude /usr/local/bin/claude
COPY --chmod=0755 ops/claude-runner/checkout ops/claude-runner/post-session /opt/ploeg/claude-runner/hooks/
RUN adduser -D -u 1000 -s /bin/bash runner \
 && install -d -m 0755 /opt/claude-code
COPY --chown=runner:runner ops/claude-runner/claude-home/ /home/runner/.claude/
RUN chmod 0755 /home/runner/.claude/hooks/save-final-message

USER runner
WORKDIR /home/runner
ENTRYPOINT ["claude"]
