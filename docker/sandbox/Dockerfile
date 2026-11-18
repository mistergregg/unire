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
# Developer tools, so an agent can check its work instead of guessing. The image is pulled
# once and reused, so its size is a one-time cost. Every version is pinned on purpose;
# bump them together and let the sandbox-smoke job confirm each one still runs.
#   - Go toolchain, copied from the official image (also what gopls and the linters need)
#   - golangci-lint, gosec, semgrep: static analysis, plain commands with no daemon
#   - gopls and typescript-language-server, wrapped by unire-lsp (find references, definitions)
#   - Chromium, used by unire-shot to screenshot a page served on localhost
COPY --from=golang:1.26-bookworm /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
ARG GOLANGCI_LINT_VERSION=v2.5.0
ARG GOSEC_VERSION=v2.22.9
ARG GOPLS_VERSION=v0.20.0
ARG SEMGREP_VERSION=1.100.0
ARG TS_LSP_VERSION=4.3.4
ARG TYPESCRIPT_VERSION=5.8.3
RUN apt-get update && apt-get install -y --no-install-recommends chromium fonts-liberation python3 python3-venv \
    && rm -rf /var/lib/apt/lists/*
# One go install per tool: go refuses several modules in one command. Each is its own layer,
# so a bump of one version rebuilds only that tool, and the module cache is removed (it is
# read-only, hence the chmod) so it does not stay in the image.
RUN GOPATH=/tmp/gopath GOBIN=/usr/local/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && chmod -R u+w /tmp/gopath && rm -rf /tmp/gopath /root/.cache
RUN GOPATH=/tmp/gopath GOBIN=/usr/local/bin go install github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION} \
    && chmod -R u+w /tmp/gopath && rm -rf /tmp/gopath /root/.cache
RUN GOPATH=/tmp/gopath GOBIN=/usr/local/bin go install golang.org/x/tools/gopls@${GOPLS_VERSION} \
    && chmod -R u+w /tmp/gopath && rm -rf /tmp/gopath /root/.cache
RUN python3 -m venv /opt/semgrep && /opt/semgrep/bin/pip install --no-cache-dir --default-timeout=120 --retries 5 semgrep==${SEMGREP_VERSION} \
    && ln -s /opt/semgrep/bin/semgrep /usr/local/bin/semgrep
RUN npm install -g typescript@${TYPESCRIPT_VERSION} typescript-language-server@${TS_LSP_VERSION}
# No telemetry, no version check: the sandbox network does not allow them anyway.
ENV SEMGREP_SEND_METRICS=off SEMGREP_ENABLE_VERSION_CHECK=0
# Caches go to /tmp, which is thrown away with each sandbox. The home folder is the shared
# login volume (every chat uses it), so Go's module and build caches, golangci-lint's and
# the rest would otherwise pile up there for ever.
ENV GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod GOPATH=/tmp/gopath \
    GOLANGCI_LINT_CACHE=/tmp/golangci-lint XDG_CACHE_HOME=/tmp/cache
COPY --chmod=755 unire-shot /usr/local/bin/unire-shot
COPY --chmod=755 unire-lsp /usr/local/bin/unire-lsp
# The command an agent runs to ask Unire for something (see internal/tools).
COPY --chmod=755 unire /usr/local/bin/unire
USER node
ENV HOME=/home/node
WORKDIR /workspace
