# ArtifactA

**Self-hostable host for AI-generated artifacts.** Publish an artifact your AI assistant
generated (an HTML page, report, dashboard) and get an **access-controlled link on
infrastructure you own** — instead of a share link on a third-party vendor's cloud.
Storage, access control, and an inbuilt audit trail all stay on infra you control.

> **Naming:** the product, the CLI binary (`artifacta`), the service (`artifacta-api`), the
> proto package (`artifacta.v1`), and env vars (`ARTIFACTA_HOME`) are all **artifacta**. The one
> remaining exception is the Git repo / Go module base, still `github.com/agarwalvivek29/here.now`
> — renaming that requires renaming the GitHub repository itself (a separate step, not yet done).

See [PRODUCT.md](PRODUCT.md) for the product, [ARCHITECTURE.md](ARCHITECTURE.md) for the
technical design.

---

## What's in This Repository

A monorepo with built-in guardrails for controlled, spec-driven, agentic development.

```
apps/          Frontend apps (the artifact-runtime viewer fork — later phase)
services/      Backend services — artifacta-api (Go): publish, viewer, RBAC, audit
packages/      Shared libraries — schema/ (protobuf domain types, generated to Go)
infra/         Local dev infrastructure (docker-compose)
docs/          PRODUCT, ARCHITECTURE, specs, ADRs, conventions, core rules
scripts/       Scaffold and utility scripts
```

---

## Quick Start

```bash
# Go 1.26 toolchain; buf for schema codegen. Git hooks:
pnpm install

# Build + run the wedge (single binary, file store, zero external deps)
cd services/artifacta-api
go build -o /tmp/artifacta ./cmd/artifacta
ARTIFACTA_HOME=~/.artifacta /tmp/artifacta login
/tmp/artifacta serve &                 # viewer on http://localhost:8080
/tmp/artifacta publish ./some-artifact.html
# open the printed /a/{slug} link; visit the login?token=… URL once to set the cookie
```

Regenerate schema types after editing proto: `cd packages/schema && ./scripts/generate.sh`.

---

## The Rules

All contributors (human and AI) follow the same rules:

- **[Core Rules](docs/CORE_RULES.md)** — the binding rules for this repository
- **[Conventions](docs/CONVENTIONS.md)** — language-specific coding standards
- **[ADR Process](docs/adr/README.md)** — how architectural decisions are recorded

**The short version:**

1. Open a GitHub Issue before starting any work
2. Write a spec in `docs/specs/` before writing any feature code
3. Create an ADR before making architectural decisions
4. Get a plan approved before implementing non-trivial changes
5. All commits follow [Conventional Commits](https://www.conventionalcommits.org/)
6. All changes go through a PR — no direct pushes to `main`

---

## For AI Agents

- **Claude Code**: read [CLAUDE.md](CLAUDE.md) first
- **Other AI tools**: read [AGENTS.md](AGENTS.md) first
- **Working in a service**: read `services/[name]/AGENTS.md` before touching any code

---

## Architecture

Key decisions are recorded in [docs/adr/](docs/adr/).

| ADR                                         | Decision                                              | Status   |
| ------------------------------------------- | ----------------------------------------------------- | -------- |
| [0001](docs/adr/0001-monorepo-structure.md) | Monorepo with per-service isolation                   | Accepted |
| [0002](docs/adr/0002-auth-model.md)         | Pluggable OIDC/local/forward-auth + per-artifact RBAC | Accepted |

---

## Stack

| Layer   | Technology                       |
| ------- | -------------------------------- |
| Backend | Go (single binary: CLI + server) |
| Schema  | Protobuf + buf (Go codegen)      |
| Store   | File store (v0) → PostgreSQL     |
| Blob    | Filesystem (v0) → S3-compatible  |
| Infra   | Docker Compose (default) → Helm  |

---

## Contributing

1. Find or create a GitHub Issue
2. Write a spec: `docs/specs/[ISSUE-NUMBER]-[name].md`
3. Create an ADR if needed
4. Branch: `git checkout -b feat/[ISSUE-NUMBER]-[desc]`
5. Implement following [docs/CONVENTIONS.md](docs/CONVENTIONS.md)
6. Open a PR using the PR template

## License

[Apache-2.0](LICENSE).
