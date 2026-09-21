# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.27.1-bookworm
ARG NODE_IMAGE=node:24.21.0-bookworm-slim
ARG RUNTIME_IMAGE=ubuntu:24.04
ARG DOCKER_IMAGE=docker:29.8.1-cli
FROM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/swarmjet ./cmd/swarmjet

FROM ${DOCKER_IMAGE} AS docker-cli
FROM ${RUNTIME_IMAGE} AS runtime
ARG META_VERSION=0.2.22
ARG TARGETARCH
ARG META_RELEASE_URL=https://github.com/gitkb/meta/releases/download
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git jq tini libstdc++6 \
 && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$arch" in amd64) meta_arch=x64;; arm64) meta_arch=arm64;; *) exit 1;; esac; \
    archive="meta-linux-${meta_arch}.tar.gz"; \
    cd /tmp; \
    curl -fsSLO "${META_RELEASE_URL}/v${META_VERSION}/${archive}"; \
    curl -fsSLO "${META_RELEASE_URL}/v${META_VERSION}/${archive}.sha256"; \
    sha256sum -c "${archive}.sha256"; \
    tar -xzf "$archive" -C /usr/local/bin; \
    chmod 0755 /usr/local/bin/meta* /usr/local/bin/loop; \
    rm "$archive" "${archive}.sha256"; \
    meta --version
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/ /usr/local/libexec/docker/cli-plugins/
COPY --from=build /out/swarmjet /usr/local/bin/swarmjet
COPY --from=web /src/web/dist /app/web/dist
COPY scripts/askpass.sh /usr/local/bin/swarmjet-askpass
RUN chmod 0755 /usr/local/bin/swarmjet-askpass \
 && docker --version && docker compose version
WORKDIR /app
ENV STATIC_DIR=/app/web/dist LISTEN_ADDRESS=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/swarmjet"]
CMD ["serve"]
