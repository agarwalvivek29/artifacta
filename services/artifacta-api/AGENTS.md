# AGENTS.md — artifacta-api

> This file is the agent contract for the `artifacta-api` service.
> Every agent (Claude Code, Copilot, Codex, Cursor, or other AI) MUST read this file before modifying anything in this service directory.
> Keep this file up to date as the service evolves.

---

## Service Overview

**Name**: `artifacta-api`
**Purpose**: [One paragraph describing what this service does and why it exists]
**Owner**: [team or person]
**Created**: 2026-07-07
**Issue**: #1
**Spec**: `docs/specs/1-[service-name].md`
**ADR**: `docs/adr/[NNNN]-[title].md`

---

## Tech Stack

- **Language**: go
- **Runtime**: [Node.js 22 / Bun / Python 3.12 / Go 1.23 / Rust stable]
- **Framework**: [Express / Fastify / FastAPI / net/http / Axum / etc.]
- **Database**: [PostgreSQL / MongoDB / Redis / None]
- **Queue**: [Kafka / RabbitMQ / Redis / Celery / None]
- **Protocol**: [REST / GraphQL / gRPC / WebSocket / CLI / Worker]

---

## Repository Layout

```
services/artifacta-api/
├── src/                 # Source code
│   ├── api/             # Route handlers / controllers
│   ├── domain/          # Business logic (no framework dependencies)
│   ├── infra/           # DB clients, queue clients, external HTTP calls
│   └── config.ts        # Environment variable validation
├── tests/               # Integration tests
├── migrations/          # Database migrations (if applicable)
├── Dockerfile
├── .env.example
└── README.md
```

---

## Key Entry Points

- **Main**: `src/main.ts` (or `main.py`, `cmd/server/main.go`, `src/main.rs`)
- **Routes**: `src/api/routes.ts`
- **Config**: `src/config.ts` — all env vars validated here; import `config` not `process.env`

---

## Environment Variables

See `.env.example` for all required variables.

Key variables:

| Variable       | Description                  | Example            |
| -------------- | ---------------------------- | ------------------ |
| `PORT`         | HTTP server port             | `3000`             |
| `DATABASE_URL` | PostgreSQL connection string | `postgresql://...` |
| [Add more]     |                              |                    |

---

## Interfaces

### Exposes

- `POST /v1/[resource]` — [description]
- [Add all endpoints]

### Consumes

- `[SERVICE_B] POST /v1/[resource]` — [description]
- `[TOPIC_NAME]` (Kafka/RabbitMQ) — [event schema]

### Events Published

- `[EVENT_NAME]` → topic `[topic-name]` — [payload schema]

---

## Local Development

```bash
# From the repo root
docker compose -f infra/docker-compose.yml up -d artifacta-api-db

# From the service directory
cp .env.example .env
# Edit .env with local values

# TypeScript
pnpm install && pnpm dev

# Python
uv sync && uv run python -m artifacta-api

# Go
go run ./cmd/server

# Rust
cargo run
```

---

## Running Tests

```bash
# TypeScript
pnpm test

# Python
uv run pytest

# Go
go test ./...

# Rust
cargo test
```

---

## Auth Middleware

This service uses dual-mode authentication middleware on **all routes** except `/health` and `/metrics`.

Accepted credentials (checked in this order):

1. `X-API-Key: <value>` — must match `API_KEY` env var (service-to-service)
2. `Authorization: Bearer <jwt>` — verified with `JWT_SECRET` env var (user or service token)

**Required env vars** (must be set in `.env`, validated at startup):

- `JWT_SECRET` — minimum 32 characters
- `API_KEY` — minimum 16 characters
- `JWT_EXPIRY_SECONDS` — default 3600

**Agent rules:**

- Never add an unprotected route without explicit human approval
- Never log the value of `JWT_SECRET`, `API_KEY`, or any token
- Auth middleware must be the first middleware applied (before logging, rate-limiting)
- Use the language-specific pattern from `docs/CONVENTIONS.md → Authentication & Middleware`

---

## Schema Package Usage

This service's data types are defined in `packages/schema/proto/artifacta-api/v1/`.

**Schema-First Rule**: NEVER define a type, interface, struct, enum, or class for a business domain concept in service code. All types come from the generated schema.

```
# Import generated types (language-specific):
# TypeScript:  import { User, UserStatus } from '@schema/artifacta-api/v1/...'
# Go:          import "[module]/packages/schema/generated/go/artifacta-api/v1"
# Python:      from schema.artifacta-api.v1 import User, UserStatus
# Rust:        mod proto { include!(concat!(env!("OUT_DIR"), "/artifacta-api.v1.rs")); }
```

If you need a new type:

1. Add it to `packages/schema/proto/artifacta-api/v1/`
2. Run `cd packages/schema && ./scripts/generate.sh`
3. Commit proto + generated output
4. Import the generated type here

---

## Testing Requirements

Backend services MUST have both test directories:

- `tests/unit/` — one test per public domain function
- `tests/e2e/` — happy path test per API endpoint and queue handler

Test commands:

```bash
# TypeScript
pnpm test           # unit (vitest)
pnpm test:e2e       # e2e (supertest)

# Python
uv run pytest tests/unit/   # unit
uv run pytest tests/e2e/    # e2e

# Go
go test ./internal/...      # unit
go test ./tests/e2e/...     # e2e

# Rust
cargo test --lib            # unit (inline)
cargo test --test '*'       # e2e (tests/ directory)
```

Agents must write tests alongside code — not as a separate step. CI will fail if coverage decreases.

---

## Forbidden Actions for Agents

> These actions require explicit human approval and must NOT be performed autonomously.

- Modifying database migrations in `migrations/` (run them, create new ones only after discussion)
- Changing the service's public API contract (adding/removing endpoints, changing response shape)
- Adding new external service dependencies (new HTTP clients, new queue topics)
- Changing authentication/authorization logic
- Modifying `Dockerfile` for production builds
- Changing environment variable names (breaks deployments)
- Any write operation to production databases or queues

---

## gstack Workflow

Use these skills at the right moments when working in this service:

| When                     | Skill              |
| ------------------------ | ------------------ |
| Before exiting plan mode | `/plan-eng-review` |
| Before pushing code      | `/review`          |
| CI failing or stuck      | `/debug`           |
| Creating the PR          | `/ship`            |

See top-level `CLAUDE.md` for the full skill reference.

---

## Agent Capabilities

Agents working on this service may:

- Use any available MCP servers relevant to this service (e.g., filesystem, database inspection in local/dev, web search)
- Download and configure additional MCP servers if needed — document them in this file under "MCP Servers in Use"
- Use web search to research library options, patterns, and bug fixes
- Execute tests and linters locally

### MCP Servers in Use

| MCP Server | Purpose | Added by |
| ---------- | ------- | -------- |
| (none yet) |         |          |

---

## Architectural Constraints

[List constraints specific to this service. Examples:]

- All business logic must be in `domain/` — zero framework imports there
- Database access only through repository interfaces in `infra/`
- No direct calls to other internal services — use the event bus

---

## Known Issues and Gotchas

[Document anything non-obvious that a new contributor (human or AI) would stumble on]

- [Gotcha 1]

---

## Related ADRs

- [ADR 0001](../../docs/adr/0001-monorepo-structure.md) — Monorepo structure
- [ADR 0006](../../docs/adr/0006-s3-blob-adapter-backend-only.md) — S3-compatible blob adapter, backend-only (no presigned URLs)
- [ADR 0013](../../docs/adr/0013-artifact-versioning.md) — Artifact versioning (immutable versions, explicit update)
- [ADR 0022](../../docs/adr/0022-observability-and-server-lifecycle.md) — Server timeouts + graceful shutdown, `slog` request logs, Prometheus `/metrics`

---

## Operations

- **`serve`** runs an explicit `http.Server` with timeouts (ReadHeader 10s / Read 120s / Idle 120s,
  WriteTimeout 0 to not cut large downloads) and drains gracefully on SIGINT/SIGTERM (ADR-0022).
- **Logging**: structured JSON request logs on stdout via the `api.Observe` middleware; level from
  `ARTIFACTA_LOG_LEVEL` (default `info`). Query strings are never logged (token/PII safe).
- **Metrics**: `GET /metrics` (Prometheus) — `http_requests_total`, `http_request_duration_seconds`,
  `http_requests_in_flight`, `go_*`. Route labels are the matched `r.Pattern` (cardinality-safe).
- **Healthcheck**: `artifacta healthcheck` (GET `/health`, exit 0 on 200) — the distroless-friendly
  container probe.
- **Deploy**: `infra/docker-compose.prod.yml` (API + Postgres + MinIO) is the reproducible self-host
  unit. Non-OIDC deploys must set `ARTIFACTA_TOKEN` (serve now fails loud if it's empty).

---

## Changelog

| Date       | Change                                                                                                                                                                                                                 | Author        |
| ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- |
| 2026-07-07 | Service created                                                                                                                                                                                                        | [name]        |
| 2026-08-25 | Immutable artifact versioning: `POST /artifacts/{slug}/versions`, `GET /a/{slug}/v/{n}/raw`, `GET /artifacts/{slug}` metadata (ADR-0013)                                                                               | Vivek Agarwal |
| 2026-09-11 | S3-compatible blob backend, backend-only (ADR-0006): `internal/infra/blob_s3.go`, selected via `ARTIFACTA_BLOB=s3`; AWS SDK v2 dep; MinIO self-host default. No presigned URLs — bytes stay CanView-gated + audited.   | Vivek Agarwal |
| 2026-09-11 | CLI login-by-URL + refresh tokens (ADR-0020): discovery `GET /.well-known/artifacta-cli`, `GET /me`, `GET /artifacts`; `artifacta login <url>` / `doctor` / `whoami`; footgun fixes (no silent local writes).          | Vivek Agarwal |
| 2026-09-11 | CLI upgrade check (ADR-0021): `GET /version` (deployed version + min-CLI/capabilities) and `artifacta upgrade` — server-aware advice via a `min-server-version` release marker.                                        | Vivek Agarwal |
| 2026-09-11 | Fixed CLI redirect URI for strict-redirect IdPs (Okta): `ARTIFACTA_OIDC_CLI_REDIRECT_URI` advertised via discovery; the CLI binds that exact loopback port instead of a random one (ADR-0020 update).                  | Vivek Agarwal |
| 2026-09-11 | Ops hardening (ADR-0022): server timeouts + graceful shutdown; `slog` JSON request logs + real Prometheus `/metrics`; `artifacta healthcheck`; `infra/docker-compose.prod.yml`; serve fails loud on empty local token. | Vivek Agarwal |
