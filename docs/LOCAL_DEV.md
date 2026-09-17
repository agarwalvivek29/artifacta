# Local development

Run the real code paths on your laptop — Postgres metadata store, S3 (MinIO) blob
store, and browser SSO via Keycloak — with one command. No hand-configuring
containers, no guessing at credentials.

> Everything here is **dev-only**: weak passwords, no TLS, in-memory Keycloak.
> Never point this stack at anything real.

## TL;DR

```bash
./scripts/dev.sh up      # start Postgres + MinIO + Keycloak, then run the API
# open http://localhost:8099 → "Sign in with SSO" → demo / demo
./scripts/dev.sh seed    # (in another terminal) publish sample artifacts
./scripts/dev.sh down    # stop and wipe everything
```

`up` runs the backing services in Docker (`infra/docker-compose.dev.yml`) and the
**API on the host** (`go run ./cmd/artifacta serve`). Ctrl-C stops the app; the
services keep running. `./scripts/dev.sh down` removes containers **and** volumes.

## Why the app runs on the host (not in compose)

OIDC tokens embed the issuer URL, and it must be identical for the Go process
(server-side token validation) and your browser (the login redirect). If the API
ran inside the compose network it would see the issuer as `http://keycloak:8080`
while your browser sees `http://localhost:8085` — the mismatch breaks token
validation. Running the API on the host keeps one issuer, `http://localhost:8085/realms/artifacta`,
for both. So: **services in Docker, app on the host.**

## What comes up

| Service       | URL / host                       | Root credentials            | Notes                                         |
| ------------- | -------------------------------- | --------------------------- | --------------------------------------------- |
| API (host)    | http://localhost:8099            | via SSO                     | `go run`, OIDC mode, upload UI on             |
| Postgres      | `localhost:55433` db `artifacta` | `postgres` / `pass`         | metadata store (`ARTIFACTA_STORE=postgres`)   |
| MinIO (S3)    | http://localhost:9000            | `minioadmin` / `minioadmin` | bucket `artifacta-bundles` (auto-created)     |
| MinIO console | http://localhost:9001            | `minioadmin` / `minioadmin` | browse the stored bundles                     |
| Keycloak      | http://localhost:8085            | `admin` / `admin`           | realm `artifacta` (admin console at `/admin`) |

Host ports 55433 and 8085 are deliberately off the defaults (5432 / 8080) so the
stack doesn't clash with anything already running locally.

### Login users (realm `artifacta`)

| Username | Password | Email              | Use for                                          |
| -------- | -------- | ------------------ | ------------------------------------------------ |
| `demo`   | `demo`   | demo@corp.example  | the primary user (owns most seeded artifacts)    |
| `alice`  | `alice`  | alice@corp.example | a second user (org + shared-with-demo artifacts) |

Users, the OIDC client (`artifacta-web`, secret `artifacta-dev-secret`), redirect
URIs, and scopes are all imported from `infra/keycloak/artifacta-realm.json` on
every Keycloak boot — the OIDC config is version-controlled, not clicked together.

## Commands

| Command                       | What it does                                                                             |
| ----------------------------- | ---------------------------------------------------------------------------------------- |
| `./scripts/dev.sh up`         | services + wait for health + run the API (foreground)                                    |
| `./scripts/dev.sh deps`       | just the services (run the app yourself)                                                 |
| `./scripts/dev.sh seed`       | publish ~10 sample artifacts (needs the app running) — Mine, Org, and one Shared-with-me |
| `./scripts/dev.sh stop`       | stop containers, keep data volumes                                                       |
| `./scripts/dev.sh down`       | stop + remove containers **and** volumes (full reset)                                    |
| `./scripts/dev.sh logs [svc]` | tail compose logs                                                                        |
| `./scripts/dev.sh info`       | reprint the URLs + credentials banner                                                    |

The app's dev configuration (every `ARTIFACTA_*` env var) lives in one block at the
top of `scripts/dev.sh` — that is the single source of truth. Dev-only app state
(the isolated `ARTIFACTA_HOME`) is written under `.dev/` in the repo, which is
gitignored.

## Prerequisites

- Docker (Desktop or Engine) running
- Go (matching `services/artifacta-api/go.mod`)
- `python3` and `curl` (used by the seed step)

## Reset / troubleshooting

- **Full reset** (fresh DB, empty bucket, fresh realm): `./scripts/dev.sh down && ./scripts/dev.sh up`.
- **Port already allocated** (`8085`/`9000`/`55433`): something else holds the host
  port. Stop it, or change the mapping in `infra/docker-compose.dev.yml` (and the
  matching `ARTIFACTA_*` value in `scripts/dev.sh`).
- **SSO bounces back to the sign-in page**: the API must run on the host with the
  env from `dev.sh` (issuer `http://localhost:8085/...`) — a bare `go run ./cmd/artifacta serve`
  without those vars falls back to local-token auth and SSO won't work.
- **Inspect stored bundles**: MinIO console at http://localhost:9001.
- **Inspect metadata**: `psql postgres://postgres:pass@localhost:55433/artifacta`.

## Not this stack

`infra/docker-compose.prod.yml` is the reproducible **self-host** unit (API +
Postgres + MinIO, no dev seeding, real config). This dev stack is for local
iteration only.
