# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/obsidian-mcp ./cmd/obsidian-mcp

# Build obsidian-headless separately: better-sqlite3 has no musl prebuilds
# and needs a node-gyp toolchain; build intermediates are stripped before
# the copy into the runtime stage.
FROM node:22-alpine AS headless
RUN apk add --no-cache python3 make g++ \
    && npm install -g obsidian-headless \
    && cd /usr/local/lib/node_modules/obsidian-headless/node_modules/better-sqlite3 \
    && rm -rf deps src build/deps build/Release/obj build/Release/obj.target

# Bare Alpine runtime: apk nodejs tracks the same Node 22 ABI as the build
# stage, ripgrep backs search_notes, and tini reaps the ob sync children.
FROM alpine:3.22
RUN apk add --no-cache nodejs ripgrep tini libstdc++ \
    && adduser -D -h /home/obsidian obsidian
COPY --from=headless /usr/local/lib/node_modules/obsidian-headless /usr/local/lib/node_modules/obsidian-headless
RUN ln -s ../lib/node_modules/obsidian-headless/cli.js /usr/local/bin/ob
COPY --from=build /out/obsidian-mcp /usr/local/bin/obsidian-mcp
USER obsidian
ENV HOME=/home/obsidian
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["obsidian-mcp"]
