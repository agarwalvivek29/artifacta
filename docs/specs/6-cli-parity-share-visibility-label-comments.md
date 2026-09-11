# Spec: CLI parity — visibility, invite-by-email, unshare, label, comments

**Issue**: N/A (maintainer-driven)
**Status**: Approved
**Author**: Vivek Agarwal
**Date**: 2026-09-11
**Services Affected**: `artifacta-api` (CLI only — no server/proto changes)

---

## Summary

Bring the `artifacta` CLI to feature parity with the Share UI for a headless / scripted
workflow. A UI-vs-CLI parity audit found five operations the server already exposes but the CLI
could not reach: explicit visibility changes, invite-by-email grants, revoking a grant, claiming a
subdomain label, and comments (add/read/resolve). Every one of these is served by an existing,
already-shipped API endpoint — this spec adds only the CLI surface over them.

---

## Background and Motivation

The server, proto types, and ADRs already implement visibility (ADR-0018), invite-by-email grants
(ADR-0019), grant revocation (ADR-0019), subdomain labels (ADR-0017), and comments with threads
(ADR-0014/0016). The CLI, however, only had `share <slug> <grantee-sub>` (which flips visibility to
`invited` and posts a subject-only grant). An operator or agent driving ArtifactA from a script
therefore could not: set an artifact to `org`/`private`/`link`, invite someone by email, revoke
access, claim a friendly subdomain, or participate in review comments — all of which the web UI can
do. That gap forces users back into the browser for routine sharing/admin, undercutting the
"one-command from the terminal" value of the CLI.

---

## Scope

### In Scope

- `artifacta visibility <slug> <private|invited|org|link>` → `PATCH /artifacts/{slug}/visibility`.
- `artifacta share <slug> <email-or-sub>` → auto-detect email vs subject; still flips visibility to
  `invited` first. (Modifies the existing `share` command + `addGrantRemote` helper.)
- `artifacta unshare <slug> <email-or-sub>` → `DELETE /artifacts/{slug}/grants/{grantee}`.
- `artifacta label <slug> <label>` → `PATCH /artifacts/{slug}/label`.
- `artifacta comment add <slug> <text> [--reply <parent-id>]` → `POST /artifacts/{slug}/comments`.
- `artifacta comment ls <slug>` → `GET /artifacts/{slug}/comments`.
- `artifacta comment resolve <slug> <id>` → `POST /artifacts/{slug}/comments/{id}/resolve`.

### Behavior change

- **`share` becomes remote-only.** Its former local-dev store path is removed so the whole
  sharing/mutation surface (visibility/share/unshare/label/comment) behaves identically: it requires
  a remote target and errors with a login prompt in local-dev mode. This removes a latent bug where
  local `share a@b.com` stored the email as a bogus subject, and the asymmetry of "share works
  locally but unshare does not." Local-dev retains `publish` + `ls`; `serve` / `audit verify` are
  unchanged.

### Out of Scope

- Any server, proto, generated-code, or ADR change — every endpoint and type already exists.
- Anchored/threaded comment authoring beyond `--reply` (text anchors need a viewer text selection
  the CLI can't produce). `comment ls` still _reads_ anchored/reply/resolved rows the UI created.
- OS-keychain token storage (pre-existing `TODO(hardening)`).

---

## Acceptance Criteria

- [ ] Given a logged-in CLI, `artifacta visibility <slug> org` sets the artifact to org visibility;
      an invalid level (e.g. `bogus`) is rejected before any network call.
- [ ] Given `artifacta share <slug> a@b.com`, the CLI flips visibility to `invited` and posts an
      `{"email":"a@b.com"}` grant; `artifacta share <slug> local:friend` posts `{"grantee_sub":...}`.
- [ ] Given `artifacta unshare <slug> a@b.com`, the matching grant is revoked (200); an unknown
      grantee surfaces the server's 404 as an error.
- [ ] Given `artifacta label <slug> my-app`, the CLI prints the subdomain URL; on a server with no
      `RootDomain` it prints the claimed label + the canonical `/a/{slug}` link and a note instead
      of a blank line.
- [ ] `artifacta comment add <slug> "text"` prints the new comment id; `--reply <id>` posts a reply
      carrying `parent_id`.
- [ ] `artifacta comment ls <slug>` lists comments, grouping replies under their root and marking
      resolved rows; `artifacta comment resolve <slug> <id>` resolves a thread (200).
- [ ] Every sharing/mutation command (including `share`) errors with the login prompt when run
      without a remote target, and `share` no longer writes the local store in local-dev mode.

---

## Technical Design

### API Changes

None. All endpoints already exist (`internal/api/server.go`):

```
PATCH  /artifacts/{slug}/visibility      {"visibility":"private|invited|org|link"}  → 200
POST   /artifacts/{slug}/grants          {"email"} | {"grantee_sub"}                → 201
DELETE /artifacts/{slug}/grants/{grantee}                                           → 200
PATCH  /artifacts/{slug}/label           {"label"}                → 200 {slug,label,subdomain_url}
POST   /artifacts/{slug}/comments        {"body","parent_id?"}    → 201 (commentView)
GET    /artifacts/{slug}/comments                                 → 200 [commentView]
POST   /artifacts/{slug}/comments/{id}/resolve                    → 200
```

### CLI

```
Run() dispatch += visibility | unshare | label | comment
each new command: config.Load → require remoteTarget (else login-prompt error) → freshToken →
                  remote helper → concise confirmation

remote.go (all via doRemote):
  addGrantRemote(...)        MODIFIED: email arg → {email}, else {grantee_sub}  (looksLikeEmail)
  removeGrantRemote(...)     DELETE, PathEscape(slug)+PathEscape(grantee), want 200
  setLabelRemote(...)        PATCH {label}, decode {subdomain_url}, want 200
  addCommentRemote(...,parentID)  POST {body,(parent_id)}, decode commentRow, want 201
  listCommentsRemote(...)    GET []commentRow, want 200
  resolveCommentRemote(...)  POST resolve, want 200
  commentRow                 mirrors api.commentView (id,version,author_email,created_at,body,resolved,parent_id)
  looksLikeEmail(s)          @-plus-dot check (server re-validates)

share (MODIFIED): remote-only — remove local-dev store branch; remote path = PATCH visibility→invited
                  then addGrantRemote (auto email/sub).
comment: two-level sub-router (own usage; unknown/empty subcommand → usage error).
label: fall back to /a/{slug} link + note when subdomain_url is "".
comment ls: group replies under root; mark resolved (and anchored) rows.
usage const: list every new command.
```

### Dependencies

None new — reuses the existing `doRemote` / `freshToken` request spine and `net/url`.

---

## Security Considerations

- All commands send the OIDC id_token as `Authorization: Bearer` via the existing `freshToken`
  refresh path; owner-only enforcement and the fail-closed 401/404 behavior live server-side and
  are unchanged.
- `looksLikeEmail` only selects the request shape; the server's `CanView` / grant-matching and
  `validEmail` remain the authority.
- No new secrets, env vars, or endpoints.

---

## Testing Plan

### Unit Tests (`internal/cli`)

- Helper tests (httptest, per `cli_test.go`): `removeGrantRemote`, `setLabelRemote`,
  `addCommentRemote` (with/without `parent_id`), `listCommentsRemote`, `resolveCommentRemote` —
  method/path/Bearer/body/decode + a non-2xx error case each. Update
  `TestAddGrantRemoteSendsBearerAndPost` to cover both email and subject bodies. `looksLikeEmail`
  table test.
- Command guard tests (`footgun_test.go` style): each sharing command errors with the login prompt
  when not remote-targeted; `share` in local-dev mode errors and writes nothing locally;
  `visibility` rejects an invalid level before the network; `comment` rejects an unknown subcommand.

### Integration Tests

None added — the endpoints already have server-side coverage (`grants_test.go`,
`grant_email_test.go`, `comments_test.go`, `dashboard_test.go`).

---

## Migration / Rollout Plan

- **Breaking changes**: one — `share` no longer writes the local store (local-dev mode now errors
  with a login prompt, like the other sharing commands). Local `publish`/`ls` are unaffected.
- **Feature flag**: none.
- **Rollback plan**: revert the PR; the new commands and the `share` change are additive to the CLI
  binary and touch no server state or schema.

---

## References

- ADRs: [0014](../adr/0014-artifact-comments.md), [0016](../adr/0016-comment-threads.md),
  [0017](../adr/0017-subdomain-artifact-addressing.md),
  [0018](../adr/0018-anonymous-vpn-gated-visibility.md),
  [0019](../adr/0019-invite-by-email-grants.md)
- Related spec: [3-cli-login-and-refresh.md](3-cli-login-and-refresh.md)
