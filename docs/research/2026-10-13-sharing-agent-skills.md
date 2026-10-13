# How Agent Skills are shared, pinned and installed (October 2026)

Evidence for [ADR-0092](../adrs/0092-a-team-pins-its-skills-in-a-catalogue-that-ploegd-fetches-once-and-serves-by-digest.md).
The question: is there a standard Ploeg should follow to give a Team a pinned set of
Agent Skills, rather than inventing a format? Checked 2026-10-13; the URLs are the sources.

## Verdict

The format of a single skill is standard. Everything above one skill (versions,
manifests, lockfiles, registries) is per tool, and no proposal for it is merged. One
consumer manifest has ecosystem support: Microsoft's Agent Package Manager (`apm.yml`
plus `apm.lock.yaml`), which Renovate updates since release 44.59.0. Ploeg should read
that format, not define its own, and keep Ploeg-only fields out of `SKILL.md` except
under `metadata`.

## The skill format

The [Agent Skills specification](https://agentskills.io/specification) defines a skill
as a directory with a `SKILL.md`: YAML frontmatter, then Markdown.

| Field | Required | Notes |
| --- | --- | --- |
| `name` | yes | 1–64 of `a-z0-9-`, no leading, trailing or double hyphen, equals the directory name |
| `description` | yes | 1–1024 characters, says what the skill does and when to use it |
| `license` | no | |
| `compatibility` | no | up to 500 characters of environment needs |
| `metadata` | no | string to string map for client-specific keys; keys should be unique enough not to collide |
| `allowed-tools` | no | experimental |

There is no version, dependency, path or trigger field. The specification's own example
puts a version under `metadata.version`. `skills-ref validate` checks a skill. Open
proposals cover versioning and locks
([#46](https://github.com/agentskills/agentskills/issues/46), where the maintainer places
versioning in distribution rather than in `SKILL.md`), a `.well-known` index with a
sha256 per skill ([#254](https://github.com/agentskills/agentskills/pull/254)),
activation ([#57](https://github.com/agentskills/agentskills/issues/57)) and paths
(#115). None is merged.

The specification's guidance for clients
([adding skills support](https://github.com/agentskills/agentskills/blob/main/docs/client-implementation/adding-skills-support.mdx))
says to read the client's own directory and `.agents/skills` at project and user level,
and that a sandboxed agent should have user- and organisation-level skills provisioned
from outside. That is what this change does.

## Where harnesses look

| Harness | `.agents/skills` | Its own directory |
| --- | --- | --- |
| OpenHands | yes, preferred | `.openhands/skills` (legacy); adds `triggers` and `paths` ([docs](https://docs.openhands.dev/overview/skills)) |
| Claude Code | no | `.claude/skills`, `~/.claude/skills`, plugins; adds `paths`, `when_to_use` ([docs](https://code.claude.com/docs/en/skills)) |
| Codex | yes | `~/.agents/skills`, `/etc/codex/skills` ([docs](https://learn.chatgpt.com/docs/build-skills)) |
| Goose | yes, preferred | `.goose/skills`, `.claude/skills` ([docs](https://goose-docs.ai/docs/guides/context-engineering/using-skills)) |
| Qwen Code | unverified | `.qwen/skills`, `~/.qwen/skills` ([docs](https://qwenlm.github.io/qwen-code-docs/en/users/features/skills/)) |
| opencode, Cursor, VS Code, Gemini CLI | yes | their own directory as well |

Ploeg already installs into `.agents/skills` under the Run's home plus the harness's own
directory (`pkg/worker/adapters.go`); Qwen Code needs `.qwen/skills` added.

## Distribution tools

| Tool | Manifest | Pin | Lock | Digest | Renovate |
| --- | --- | --- | --- | --- | --- |
| [APM](https://microsoft.github.io/apm/) 0.33 | `apm.yml` `dependencies.apm` | git ref or tag | `apm.lock.yaml`: `repo_url`, `resolved_ref`, `resolved_commit`, `content_hash`, per-file hashes ([spec](https://github.com/microsoft/apm/blob/main/docs/src/content/docs/reference/lockfile-spec.md)) | sha256 | yes, `apm` manager ([#44390](https://github.com/renovatebot/renovate/pull/44390), merged 2026-09-02) |
| [`skills` CLI](https://github.com/vercel-labs/skills) 1.7 | none | `owner/repo#ref` | `skills-lock.json`: ref (branch or tag, not commit), folder hash | sha256 | no |
| Claude Code [plugin marketplaces](https://code.claude.com/docs/en/plugins/marketplace-reference) | `.claude-plugin/marketplace.json` (a catalogue a publisher offers, not a consumer pin) | `ref` + 40-character `sha`; `<plugin>--v<semver>` tags | none | `archive` sources take sha256 | open issue |
| `gh skill` (preview) | none | `@tag` or `--pin` | none | no | no |
| OCI | vendor-specific media types only (Alibaba ACR, Chainguard beta) | digest | registry | yes | no harness reads it |
| MCP Skills extension (SEP-2640) | `skills/list`, `skills/get` over MCP | n/a | n/a | n/a | n/a |

APM is the only format with a lockfile that records the commit and a content hash, and
the only one a dependency bot updates. It is a 0.x working draft (OpenAPM v0.1), so Ploeg
reads a strict subset and refuses unknown shapes rather than depending on the `apm`
binary.

## Supply chain

There is no signing standard. OWASP's [Agentic Skills Top 10, AST02](https://owasp.org/www-project-agentic-skills-top-10/ast02)
proposes signing a digest over `SKILL.md` and its files. Scanners exist
([Cisco skill-scanner](https://github.com/cisco-ai-defense/skill-scanner), skills.sh's
audits). Admins allowlist sources by host: Claude Code's `strictKnownMarketplaces`,
APM's `apm-policy.yml`. A content hash pinned in a reviewed lockfile plus a host
allowlist is the strongest control available that every tool understands.

## Choosing which skill to load

The only standard is progressive disclosure: the model sees name and description and
decides. Harnesses add their own hints (`paths` in Claude Code and OpenHands,
`when_to_use` in Claude Code, `triggers` in OpenHands). Ploeg's deterministic selection
(ADR-0083) has no standard counterpart; it should honour `paths` when a skill already
declares it, so an author does not write the same globs twice.

## Unverified

Qwen Code reading `.agents/skills`; Renovate's support for `<name>--v<semver>` tags in
one repository; the frontmatter fields `gh skill` writes.
