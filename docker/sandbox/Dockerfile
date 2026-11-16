FROM node:22-slim
# git and curl for the work; every supported agent CLI so any of them can be
# chosen per session. To add an agent, install its CLI here (see docs/adding-an-agent.md).
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates openssh-client curl \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code @openai/codex @google/gemini-cli \
    && mkdir /workspace && chown node:node /workspace
USER node
ENV HOME=/home/node
WORKDIR /workspace
