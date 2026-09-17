#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# dev.sh — one-command local development for artifacta-api.
#
#   ./scripts/dev.sh up      Bring up Postgres + MinIO + Keycloak, then run the
#                            API on the host (foreground). Open http://localhost:8099
#   ./scripts/dev.sh deps    Just the backing services (no app) — up + wait + banner
#   ./scripts/dev.sh seed     Publish sample artifacts (needs the app running)
#   ./scripts/dev.sh down     Stop and REMOVE everything (containers + volumes)
#   ./scripts/dev.sh stop     Stop containers but keep data volumes
#   ./scripts/dev.sh logs [svc]   Tail compose logs
#   ./scripts/dev.sh info     Print URLs + credentials again
#
# Everything here is DEV-ONLY (weak passwords, no TLS, in-memory Keycloak). The
# full contract — ports, root users, how SSO works, how to reset — is in
# docs/LOCAL_DEV.md. The app runs on the HOST (not in compose) so its OIDC issuer
# URL is identical for the Go process and your browser (see the compose header).
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE="docker compose -f $ROOT/infra/docker-compose.dev.yml"
SVC_DIR="$ROOT/services/artifacta-api"

# ── the single source of truth for the app's dev configuration ───────────────
export ARTIFACTA_ADDR=127.0.0.1:8099
export ARTIFACTA_BASE_URL=http://localhost:8099
export ARTIFACTA_STORE=postgres
export ARTIFACTA_DATABASE_URL="postgres://postgres:pass@localhost:55433/artifacta?sslmode=disable"
export ARTIFACTA_BLOB=s3
export ARTIFACTA_S3_ENDPOINT=http://localhost:9000
export ARTIFACTA_S3_REGION=us-east-1
export ARTIFACTA_S3_BUCKET=artifacta-bundles
export ARTIFACTA_S3_ACCESS_KEY_ID=minioadmin
export ARTIFACTA_S3_SECRET_ACCESS_KEY=minioadmin
export ARTIFACTA_S3_FORCE_PATH_STYLE=true
export ARTIFACTA_OIDC_ISSUER=http://localhost:8085/realms/artifacta
export ARTIFACTA_OIDC_CLIENT_ID=artifacta-web
export ARTIFACTA_OIDC_CLIENT_SECRET=artifacta-dev-secret
export ARTIFACTA_OIDC_REDIRECT_URL=http://localhost:8099/callback
export ARTIFACTA_SESSION_SECRET=dev-only-session-secret-change-me-0123456789
export ARTIFACTA_UPLOAD_UI=true
export ARTIFACTA_LOG_LEVEL=info
# Keep dev state out of the developer's real ~/.artifacta config.
export ARTIFACTA_HOME="$ROOT/.dev/artifacta-home"

KC_ISSUER="$ARTIFACTA_OIDC_ISSUER"
KC_TOKEN="$KC_ISSUER/protocol/openid-connect/token"

c()   { printf '\033[%sm%s\033[0m' "$1" "$2"; }
info_banner() {
  cat <<EOF

$(c '1;35' '╭──────────────────────────────────────────────────────────────╮')
$(c '1;35' '│')  ArtifactA local dev is up                                    $(c '1;35' '│')
$(c '1;35' '╰──────────────────────────────────────────────────────────────╯')

  App            http://localhost:8099        (run on the host)
  Sign in        "Sign in with SSO" → Keycloak → back to the dashboard
                 users:  demo / demo     alice / alice   (both @corp.example)

  Postgres       localhost:55433   db=artifacta   user=postgres   pass=pass
  MinIO S3 API   http://localhost:9000          key=minioadmin  secret=minioadmin
  MinIO console  http://localhost:9001          (same credentials)
  Keycloak       http://localhost:8085          admin / admin   realm=artifacta

  Seed sample artifacts:   ./scripts/dev.sh seed
  Tear everything down:    ./scripts/dev.sh down

EOF
}

wait_for() { # name url
  printf 'waiting for %s ' "$1"
  for _ in $(seq 1 60); do
    if curl -sf -o /dev/null "$2" 2>/dev/null; then echo "✓"; return 0; fi
    printf '.'; sleep 2
  done
  echo "✗ (timed out on $2)"; return 1
}

deps() {
  command -v docker >/dev/null || { echo "docker is required"; exit 1; }
  $COMPOSE up -d
  wait_for "postgres"  "http://localhost:9000/minio/health/live" >/dev/null 2>&1 || true
  # Poll the real readiness endpoints (compose healthchecks gate startup order,
  # but we still wait on the host-facing ports the app/browser actually use).
  printf 'waiting for minio ';  for _ in $(seq 1 60); do curl -sf -o /dev/null http://localhost:9000/minio/health/live && { echo '✓'; break; } || { printf '.'; sleep 1; }; done
  printf 'waiting for keycloak '; for _ in $(seq 1 90); do curl -sf -o /dev/null "$KC_ISSUER/.well-known/openid-configuration" && { echo '✓'; break; } || { printf '.'; sleep 1; }; done
  printf 'waiting for postgres '; for _ in $(seq 1 60); do docker exec artifacta-dev-pg pg_isready -U postgres -d artifacta >/dev/null 2>&1 && { echo '✓'; break; } || { printf '.'; sleep 1; }; done
  info_banner
}

run_app() {
  mkdir -p "$ARTIFACTA_HOME"
  echo "starting API on http://localhost:8099  (Ctrl-C stops the app; deps keep running)"
  cd "$SVC_DIR" && exec go run ./cmd/artifacta serve
}

# ── seed: publish a realistic set through the REAL auth path (password grant) ──
tok() { # username password → id_token
  curl -s -X POST "$KC_TOKEN" -d grant_type=password -d client_id=artifacta-web \
    -d client_secret="$ARTIFACTA_OIDC_CLIENT_SECRET" -d username="$1" -d password="$2" -d scope=openid \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["id_token"])'
}
sub_of() { python3 -c 'import sys,base64,json; p=sys.argv[1].split(".")[1]; p+="="*(-len(p)%4); print(json.loads(base64.urlsafe_b64decode(p))["sub"])' "$1"; }
_page() { printf '<!doctype html><html><head><meta charset="utf-8"></head><body style="font-family:system-ui,-apple-system,sans-serif;margin:0;padding:52px;background:%s"><div style="max-width:620px"><div style="font:600 12px/1 ui-monospace,monospace;letter-spacing:.12em;text-transform:uppercase;color:%s">ArtifactA</div><h1 style="margin:14px 0 10px;font-size:34px;color:#14142b">%s</h1><p style="color:#454a63;font-size:18px;line-height:1.6">%s</p></div></body></html>' "$2" "$4" "$1" "$3"; }
_pub() { curl -s -X POST "$ARTIFACTA_BASE_URL/artifacts?title=$2" -H "Authorization: Bearer $1" -H "Content-Type: text/html; charset=utf-8" --data-binary "$3" | python3 -c 'import sys,json;print(json.load(sys.stdin)["slug"])'; }
_vis() { curl -s -o /dev/null -X PATCH "$ARTIFACTA_BASE_URL/artifacts/$2/visibility" -H "Authorization: Bearer $1" -H "Content-Type: application/json" -d "{\"visibility\":\"$3\"}"; }
_grant_sub() { curl -s -o /dev/null -X POST "$ARTIFACTA_BASE_URL/artifacts/$2/grants" -H "Authorization: Bearer $1" -H "Content-Type: application/json" -d "{\"grantee_sub\":\"$3\"}"; }

seed() {
  curl -sf -o /dev/null "$ARTIFACTA_BASE_URL/health" || { echo "app not running — start it first with: ./scripts/dev.sh up"; exit 1; }
  local DEMO ALICE DEMO_SUB s
  DEMO=$(tok demo demo); ALICE=$(tok alice alice); DEMO_SUB=$(sub_of "$DEMO")
  echo "seeding as demo + alice…"
  _pub "$DEMO" "Q3+Revenue+Deck"     "$(_page 'Q3 Revenue Deck' '#fff5f8' 'Bookings up 24% QoQ; net revenue retention 118%.' '#e8265c')" >/dev/null
  s=$(_pub "$DEMO" "Engineering+Roadmap" "$(_page 'Engineering Roadmap' '#f4f7ff' 'H2: multi-region, self-serve onboarding, search + audit hardening.' '#5a5be0')"); _vis "$DEMO" "$s" org
  s=$(_pub "$DEMO" "Onboarding+Guide"    "$(_page 'Onboarding Guide' '#f2fbf9' 'Everything a new hire needs in week one.' '#0f857a')"); _vis "$DEMO" "$s" link
  _pub "$DEMO" "Pricing+Experiment"  "$(_page 'Pricing Experiment' '#ffffff' 'Usage-based tier vs seats. Early signal: +12% conversion.' '#e8265c')" >/dev/null
  s=$(_pub "$DEMO" "Board+Update+Sept"   "$(_page 'Board Update — September' '#fbf7ff' 'KPIs, cash position, and two decisions we need from the board.' '#8f5be0')"); _vis "$DEMO" "$s" invited
  _pub "$DEMO" "Brand+Guidelines"    "$(_page 'Brand Guidelines' '#fffdf5' 'Logo usage, type scale, core palette.' '#b8860b')" >/dev/null
  for t in "Company+OKRs" "Security+Policy" "All-Hands+Notes"; do
    s=$(_pub "$ALICE" "$t" "$(_page "${t//+/ }" '#f4f7ff' 'An org-wide document shared with everyone signed in.' '#5a5be0')"); _vis "$ALICE" "$s" org
  done
  # alice shares one with demo BY SUBJECT so it lands in demo's "Shared with me".
  s=$(_pub "$ALICE" "Partnership+Proposal" "$(_page 'Partnership Proposal' '#fff5f8' 'Draft co-marketing terms — needs your review.' '#e8265c')")
  _vis "$ALICE" "$s" invited; _grant_sub "$ALICE" "$s" "$DEMO_SUB"
  echo "seeded. reload http://localhost:8099 (sign in as demo / demo)."
}

case "${1:-up}" in
  up)    deps; run_app ;;
  deps)  deps ;;
  seed)  seed ;;
  down)  $COMPOSE down -v; echo "removed containers + volumes" ;;
  stop)  $COMPOSE stop; echo "stopped (data kept; ./scripts/dev.sh up to resume)" ;;
  logs)  $COMPOSE logs -f "${2:-}" ;;
  info)  info_banner ;;
  *)     echo "usage: ./scripts/dev.sh {up|deps|seed|down|stop|logs|info}"; exit 1 ;;
esac
