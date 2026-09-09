# artifacta-api

> Replace this with a one-paragraph description of what this service does and why it exists.

**Language**: go
**Type**: [REST API / GraphQL / WebSocket / gRPC / Worker / CLI / Agentic]
**Owner**: [team or person]
**Issue**: #1
**Spec**: [docs/specs/ISSUE-NUMBER-name.md](../../docs/specs/)
**ADR**: [docs/adr/NNNN-title.md](../../docs/adr/)

---

## Prerequisites

- Docker & Docker Compose
- [Language-specific: Node.js 22 / Python 3.12 / Go 1.23 / Rust stable]

---

## Local Development

```bash
# 1. Start required infrastructure
docker compose -f ../../infra/docker-compose.yml up -d

# 2. Set up environment
cp .env.example .env
# Edit .env — fill in real local values

# 3. Install dependencies
# TypeScript:  pnpm install
# Python:      uv sync
# Go:          go mod download
# Rust:        cargo build

# 4. Run in dev mode (hot reload)
# TypeScript:  pnpm dev
# Python:      uv run python -m artifacta-api
# Go:          go run ./cmd/server
# Rust:        cargo run
```

---

## Running Tests

```bash
# TypeScript:  pnpm test
# Python:      uv run pytest -v
# Go:          go test ./...
# Rust:        cargo test
```

---

## API Reference

[Link to OpenAPI spec or describe key endpoints]

```
GET  /health      — Health check
GET  /metrics     — Prometheus metrics
```

---

## Architecture

[Brief description of the service's internal structure. Reference AGENTS.md for agent-specific details.]

```
src/
├── api/        Routes and request handlers
├── domain/     Business logic (framework-free)
├── infra/      DB clients, queue clients, external calls
└── config.ts   Environment variable validation
```

---

## Configuration

See `.env.example` for all environment variables. All variables are validated at startup — the service will fail fast if required variables are missing.

---

## Deployment

[Link to deployment docs or describe the deploy process]

---

## Troubleshooting

| Problem               | Solution                                                    |
| --------------------- | ----------------------------------------------------------- |
| Service won't start   | Check `.env` has all required variables from `.env.example` |
| DB connection refused | Ensure `docker compose up -d` has been run                  |
