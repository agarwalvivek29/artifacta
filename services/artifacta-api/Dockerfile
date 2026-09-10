# ─────────────────────────────────────────────────────────────────────────────
# Production image for artifacta — the single binary is BOTH the server and the
# CLI (subcommands: serve, publish, login, ls, share, versions, audit).
#
#   docker run ... ghcr.io/agarwalvivek29/artifacta            # -> serve
#   docker run ... ghcr.io/agarwalvivek29/artifacta publish f  # -> CLI
#
# Build context is the REPO ROOT (the Go workspace spans two modules:
# services/artifacta-api and packages/schema/generated/go).
# Build:  docker build -f services/artifacta-api/Dockerfile -t artifacta .
# ─────────────────────────────────────────────────────────────────────────────

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
# Workspace + both modules (module files first for layer caching).
COPY go.work go.work.sum ./
COPY packages/schema/generated/go/go.mod packages/schema/generated/go/go.sum ./packages/schema/generated/go/
COPY services/artifacta-api/go.mod services/artifacta-api/go.sum ./services/artifacta-api/
RUN cd services/artifacta-api && go mod download
COPY packages/schema/generated/go ./packages/schema/generated/go
COPY services/artifacta-api ./services/artifacta-api
# Static, stripped, version-stamped, cross-compiled to the target platform.
RUN cd services/artifacta-api && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X github.com/agarwalvivek29/here.now/services/artifacta-api/internal/cli.Version=${VERSION}" \
      -o /out/artifacta ./cmd/artifacta
# A writable data dir owned by the distroless nonroot uid (65532).
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/artifacta /artifacta
COPY --from=build --chown=65532:65532 /out/data /data
ENV ARTIFACTA_ADDR=:8080 ARTIFACTA_DATA_DIR=/data
EXPOSE 8080
VOLUME ["/data"]
USER 65532:65532
ENTRYPOINT ["/artifacta"]
CMD ["serve"]
