# skills

ArtifactA's **assistant skills** — loadable by any skill-capable AI agent (Claude Code and
others) working with a self-hosted ArtifactA (`here.now`) instance.

- [`artifacta-design/SKILL.md`](./artifacta-design/SKILL.md) — **load this first**, before
  authoring an artifact. It pushes the design past the LLM default and keeps it valid inside
  ArtifactA's sandboxed viewer (null-origin iframe, strict CSP, no `eval`, single file,
  no phone-home). ArtifactA's equivalent of Claude's `/artifact-design`.
- [`artifacta-publish/SKILL.md`](./artifacta-publish/SKILL.md) — the publish surface: send a
  finished artifact to the instance and get a private link. A thin wrapper over the CLI/REST
  publish path ([ADR-0012](../docs/adr/0012-assistant-agnostic-publish-surfaces.md)).

Typical flow: **design with `artifacta-design` → `artifacta publish` via `artifacta-publish`.**
