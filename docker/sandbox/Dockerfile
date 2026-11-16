FROM node:22-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates openssh-client curl \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code \
    && mkdir /workspace && chown node:node /workspace
USER node
ENV HOME=/home/node
WORKDIR /workspace
