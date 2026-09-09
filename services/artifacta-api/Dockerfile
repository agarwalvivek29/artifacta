# ─────────────────────────────────────────────────────────────────────────────
# PRODUCTION Dockerfile — artifacta-api
#
# For local development with hot-reload, use Dockerfile.dev instead.
#
# Choose ONE language section and delete the others.
# Then remove artifacta-api / artifacta-api placeholders.
# ─────────────────────────────────────────────────────────────────────────────

FROM golang:1.23-alpine AS go-build
WORKDIR /app
# Install protobuf compiler (needed if using prost/proto in build)
RUN apk add --no-cache protobuf-dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server ./cmd/server

FROM gcr.io/distroless/static:nonroot AS production
COPY --from=go-build /app/server /server
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/server", "healthcheck"]
CMD ["/server"]

