FROM node:22-slim
# git and curl for the work; every supported agent CLI so any of them can be
# chosen per session. To add an agent, install its CLI here (see docs/adding-an-agent.md).
#
# Claude Code and Codex are pinned, so an upstream release cannot change an agent's
# output format under a running install. Update them on purpose: bump the numbers,
# build, and the CI smoke test (the sandbox-smoke job) checks each CLI still speaks
# the protocol Unire reads. Antigravity's installer has no version option, so it is
# whatever was latest at build time; the same smoke test is what catches a change.
ARG CLAUDE_CODE_VERSION=2.1.325
ARG CODEX_VERSION=0.169.0
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates openssh-client curl \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} @openai/codex@${CODEX_VERSION} \
    && mkdir /workspace && chown node:node /workspace

# Antigravity CLI (agy), from Google's own installer (it checks a SHA-512). It goes in
# /usr/local/bin, not the home folder, because the home folder is replaced by the login
# volume when a sandbox starts. The binary updates itself when it can write to its
# folder; here it cannot, so the version in the image is the version that runs.
RUN curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin \
    && agy --version
# The command an agent runs to ask Unire for something (see internal/tools).
COPY --chmod=755 unire /usr/local/bin/unire
USER node
ENV HOME=/home/node
WORKDIR /workspace
