FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git jq openssh-client \
 && rm -rf /var/lib/apt/lists/*
RUN git config --system user.name "Claude" \
 && git config --system user.email "noreply@anthropic.com" \
 && git config --system --add safe.directory '*'

COPY LICENSE NOTICE /usr/share/doc/ploeg/
COPY ops/claude-runner/claude-code.env /opt/ploeg/claude-runner/claude-code.env
COPY --chmod=0755 ops/claude-runner/install-claude-code /usr/local/bin/ploeg-install-claude-code
COPY --chmod=0755 ops/claude-runner/claude /usr/local/bin/claude
COPY --chmod=0755 ops/claude-runner/checkout ops/claude-runner/post-session /opt/ploeg/claude-runner/hooks/
RUN useradd --create-home --uid 1000 runner \
 && install -d -m 0755 /opt/claude-code
COPY --chown=runner:runner ops/claude-runner/claude-home/ /home/runner/.claude/
RUN chmod 0755 /home/runner/.claude/hooks/save-final-message

USER runner
WORKDIR /home/runner
ENTRYPOINT ["claude"]
