# Agent instructions

## Start here

1. Read this file and the [project map](docs/PROJECT-MAP.md); select affected projects and consumers.
2. Read applicable project `AGENTS.md` files, READMEs, and contracts. Project instructions add domain rules and commands to the shared methodology.
3. Inspect current code and Git state. Resume a matching unfinished local task before proposing new work.
4. Follow the [development workflow](docs/DEVELOPMENT-WORKFLOW.md). Agree checkpoints with the user before implementation; an explicit instruction to implement an agreed plan is sufficient.

## Project map

| Area | Purpose | Start here |
| --- | --- | --- |
| `AgentShoppingList/` | Agent-managed mobile shopping app with built-in MCP | [README](AgentShoppingList/README.md), [local instructions](AgentShoppingList/AGENTS.md) |
| `GoogleHealthMCP/` | Google Health / Fitbit importer with SQLite persistence | [README](GoogleHealthMCP/README.md) |
| `SSHMCP/` | Authenticated MCP gateway and persistent SSH execution runner | [README](SSHMCP/README.md) |
| `WebhookControl/` | Standalone JSON webhook router with durable delivery | [README](WebhookControl/README.md), [architecture](WebhookControl/ARCHITECTURE.md) |
| `examples/xtrace-example/` | Distributed tracing demonstration | [README](examples/xtrace-example/README.md) |
| `vvivid.dev/` | Personal website built with Hugo Extended and a vendored theme | [README](vvivid.dev/README.md), [local instructions](vvivid.dev/AGENTS.md) |
| Repository root | Shared build delegation, CI, documentation, and agent skills | [Development guide](DEVELOPMENT.md) |

## Shared rules

- **MUST: Never push anything to any remote without the user's explicit permission for that push.** This includes task branches, tags, and deletions, through Git or any API/tool. Plan approval, implementation, local commits, successful review, PR creation requests, and release preparation do not imply push permission. Prepare and validate locally, then wait for explicit authorization covering the remote and refs. Do not bypass this requirement through another agent or tool.
- Use the repository-local [Build -> Translate -> Review skill](.agents/skills/build-translate-review/SKILL.md) for application features, fixes, and refactors. It is authoritative here even if a personal skill shares its name. Complete every coding checkpoint through all stages and final validation. Documentation-only work uses direct relevant checks.
- Before Go edits, use the repository-local [Modern Go Guidelines skill](.agents/skills/use-modern-go/SKILL.md). Run its wrapper's full `list --file-path <target.go>` output and request `explain` only for relevant guideline IDs. Respect the target module's Go version, including nested modules; do not upgrade dependencies merely to apply newer idioms.
- Shared development skills live in root `.agents/skills/` with their required scripts, metadata, and licenses. Use repository copies rather than personal plugin paths. For Codex Cloud, follow [cloud development prerequisites](docs/CLOUD-DEVELOPMENT.md); copied skills do not establish runtime or independent-agent readiness.
- Translation improves actual code readability while preserving behavior. Independent Review is read-only; relay its exact Approved or Denied verdict. Stop on Denied and await the user's instruction.
- Keep task plans and handoffs in ignored `.development/<task>/` directories within affected projects. Never stage, commit, or force-add them. Preserve incomplete work; delete only completed task records after acceptance and final handoff. See the workflow for details.
- Preserve unrelated changes. Identify affected projects and supported consumers before editing. Broaden validation for shared tooling and cross-project changes.
- Each project owns its dependencies, build context, tests, runtime configuration, and version. Git branches and worktrees cover the whole repository; project folders do not isolate concurrent Git operations.
- For each new editing task, create or reuse its dedicated branch and worktree before editing, including documentation changes. Read-only investigation may use the current checkout. Name branches `<type>/<area>/<task>`; use `feature`, `bugfix`, `docs`, `refactor`, `chore`, or `release`, and a project area or `workspace` for shared/cross-project work. Follow the [branching procedure](docs/DEVELOPMENT-WORKFLOW.md#branches-and-worktrees).
- Keep all checkpoints and sequential agent stages for one task in the same worktree; reuse it for follow-ups. Deliver through a PR targeting `main`; commit, push, PR creation, merge, and release actions require the corresponding user instruction. The user controls merging. Never push directly to `main`, force-push, or rewrite published history.
- Current CI watches project directories; root-only changes can skip all project workflows. Select checks explicitly rather than relying on skipped workflows.
- Keep source completion, independent approval, release, deployment, and runtime verification distinct. Planning does not authorize live infrastructure or data changes.
- Keep secrets and personal/runtime data out of source, docs, fixtures, and logs. Retain lasting contracts and operating instructions in maintained documentation; development diaries stay local.
- When adding or moving projects, update both maps and relevant build/CI entry points. Optional project `AGENTS.md` files add local guidance without duplicating the shared methodology.
