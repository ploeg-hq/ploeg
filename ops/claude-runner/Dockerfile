# Image for ADR-0084: a Team's self-hosted Claude runner, and the worker that
# dispatches to it (harness claude-environment). Claude Code is pinned by
# version and checksum; bump both together from the release's manifest.json.
FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8

ARG CLAUDE_CODE_VERSION=2.1.296
ARG CLAUDE_CODE_SHA256=24972e3bc859fab2b46ed4c1e51f7d6130f06d3bd550811a114640de3370d0de

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git jq openssh-client \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://downloads.claude.ai/claude-code-releases/${CLAUDE_CODE_VERSION}/linux-x64/claude" -o /usr/local/bin/claude \
 && echo "${CLAUDE_CODE_SHA256}  /usr/local/bin/claude" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/claude
RUN git config --system user.name "Claude" \
 && git config --system user.email "noreply@anthropic.com" \
 && git config --system --add safe.directory '*'

COPY LICENSE NOTICE /usr/share/doc/ploeg/
COPY --chmod=0755 ops/claude-runner/checkout ops/claude-runner/post-session /opt/ploeg/claude-runner/hooks/
RUN useradd --create-home --uid 1000 runner
COPY --chown=runner:runner ops/claude-runner/claude-home/ /home/runner/.claude/
RUN chmod 0755 /home/runner/.claude/hooks/save-final-message

USER runner
WORKDIR /home/runner
ENTRYPOINT ["claude"]
