---
name: artifacta-publish
description: Publish, host, or share an artifact on ArtifactA (a self-hosted here.now instance) — use when the user asks to publish/host/share an HTML page, report, or dashboard on ArtifactA or their own self-hosted instance and get a private link back.
---

# Publish an artifact to ArtifactA

> **Design it first.** Before publishing anything you just authored, load the
> **`artifacta-design`** skill — it keeps the artifact from looking like the LLM default and,
> critically, keeps it valid inside ArtifactA's sandbox (no `eval`/Tailwind-Play-CDN, storage
> APIs throw, single file, no phone-home). Publishing a broken or bare artifact is a miss even
> when the upload succeeds.

## What ArtifactA is

ArtifactA (the `here.now` server) is a **self-hostable, access-controlled, audited host for
AI-generated artifacts**. The operator runs it on their own infra — the artifact's storage,
access control, and audit trail never leave infrastructure they control. Publishing an
artifact returns a **private-by-default** link (`<base>/a/<slug>`); only authorized viewers
can open it, and every view is recorded in a tamper-evident audit log.

## Prerequisite: the user is already logged in

The user runs `artifacta login` **once** at install. That performs an OIDC loopback sign-in and
the CLI holds the resulting session (ADR-0007). **Do not authenticate yourself** — you have no
separate identity and there is no token to manage. You act as the user by riding the CLI's
stored session.

If a publish fails with a "not logged in" error, stop and ask the user to run `artifacta login`
themselves. Do not attempt to log in on their behalf.

## How to publish

1. Run the publish command with the path to the file:

   ```bash
   artifacta publish <file>
   ```

2. Capture the link it prints to stdout — a single line of the form:

   ```
   <base>/a/<slug>
   ```

3. Return that link to the user.

To list what the user has already published, run `artifacta ls`.

## Sharing (a user action, not yours)

Published artifacts are **private by default** — the link works only for the owner until it is
shared. Sharing is the user's decision: tell them they can grant access from the ArtifactA
dashboard (`artifacta share` where available). Never widen an artifact's visibility on your own.

## Fallback surfaces (ADR-0012)

The `artifacta` CLI is the universal surface for shell-capable agents. If you cannot run a shell:

- **REST API** — the canonical, lowest-common-denominator surface: `POST /artifacts` on the
  instance. Every surface converges on this same API + auth + authz + audit path.
- **MCP** — an ergonomic tool surface for MCP-speaking assistants, shipping as a fast-follow.

Only use commands that exist: `login`, `publish <file>`, `ls`, `serve`. Do not invent flags.
