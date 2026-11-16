FROM node:22-slim
# git and curl for the work; every supported agent CLI so any of them can be
# chosen per session. To add an agent, install its CLI here (see docs/adding-an-agent.md).
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates openssh-client curl \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code @openai/codex \
    && mkdir /workspace && chown node:node /workspace

# Antigravity CLI (agy), from Google's own installer (it checks a SHA-512). It goes in
# /usr/local/bin, not the home folder, because the home folder is replaced by the login
# volume when a sandbox starts. The binary updates itself when it can write to its
# folder; here it cannot, so the version in the image is the version that runs.
RUN curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin \
    && agy --version
USER node
ENV HOME=/home/node
WORKDIR /workspace
