# syntax=docker/dockerfile:1.7
#
# Multi-stage, multi-arch (linux/amd64, linux/arm64) build for bns.
# Final image: gcr.io/distroless/static-debian12:nonroot.
#
# Build args:
#   GO_VERSION  — Go toolchain image tag (default 1.26)

ARG GO_VERSION=1.26

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

# Empty cache directory created here so we can COPY --chown into the
# distroless runtime (distroless has no shell / mkdir).
RUN mkdir -p /out/var/cache/bns

# Runtime: distroless static, nonroot uid (65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build                          /out/bns           /usr/local/bin/bns
COPY --from=build --chown=nonroot:nonroot  /out/var/cache/bns /var/cache/bns
COPY deploy/docker/config.yaml                                /etc/bns/config.yaml
VOLUME /var/cache/bns
EXPOSE 5354/udp 5354/tcp 9090/tcp
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/bns","serve","-c","/etc/bns/config.yaml"]
