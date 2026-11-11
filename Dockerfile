# The Dash hub, built from source (see docs/deploy.md#run-the-hub-with-docker).
#
#   docker build --build-arg VERSION=$(git describe --tags --always) -t dash:local .
#
# Release images are built by GoReleaser from the release binaries instead
# (deploy/Dockerfile.release); both end up the same: a static `dash` on
# distroless, as a non-root user, with its state in /data.
#
# The image holds the hub only. It has no `claude`, no git and no project
# toolchains, so it runs no tasks itself: work runs on runners
# (`dash runner`, installed natively on each computer).

# Keep in step with the go line in go.mod (CI checks).
ARG GO_VERSION=1.27.4

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
# Stamped like the release binaries, for `dash version`.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/klahr/dash/internal/buildinfo.Version=${VERSION}" \
      -o /out/dash ./cmd/dash \
 && mkdir -p /out/data

# CA certificates and time zones included; runs as nonroot (65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/dash /usr/local/bin/dash
# A new volume on /data takes this directory's owner, so Dash can write it.
COPY --from=build --chown=65532:65532 /out/data /data
ENV DASH_PROJECTS=/data/projects
WORKDIR /data
VOLUME /data
EXPOSE 7788
HEALTHCHECK --interval=30s --timeout=10s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/dash", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/dash"]
# Listen on all of the container's interfaces (publish the port only to a
# proxy) and keep state in /data. The hub never runs Claude or tasks:
# runners do. Behind an HTTPS proxy add -trust-proxy, as
# deploy/docker-compose.yml does (with -allow-unsafe=proxy-addr: only the
# proxy can reach the container's port).
CMD ["-addr", "0.0.0.0:7788", "-data", "/data"]
