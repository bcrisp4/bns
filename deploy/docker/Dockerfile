# syntax=docker/dockerfile:1.7
#
# Multi-stage, multi-arch (linux/amd64, linux/arm64) build for bns.
# Final image: gcr.io/distroless/static-debian12:nonroot.
#
# Build args:
#   GO_VERSION  — Go toolchain image tag (default 1.26)
#   HAGEZI_TAG  — pinned hagezi/dns-blocklists release tag baked into the image

ARG GO_VERSION=1.26
ARG HAGEZI_TAG=37522026.139.67736

# Blocklist fetched once on the build host (arch-independent).
FROM --platform=$BUILDPLATFORM alpine:3 AS blocklist
ARG HAGEZI_TAG
RUN apk add --no-cache curl ca-certificates \
 && curl -fsSL --retry 3 \
      -o /pro.txt \
      "https://raw.githubusercontent.com/hagezi/dns-blocklists/${HAGEZI_TAG}/domains/pro.txt"

# Cross-compile bns on the build host for the target arch.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/bns ./cmd/bns

# Runtime: distroless static, nonroot uid (65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build     /out/bns                       /usr/local/bin/bns
COPY --from=blocklist /pro.txt                       /etc/bns/blocklists/pro.txt
COPY deploy/docker/config.yaml                       /etc/bns/config.yaml
EXPOSE 5354/udp 5354/tcp 9090/tcp
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/bns","serve","-c","/etc/bns/config.yaml"]
