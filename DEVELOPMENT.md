# Workspace development

My personal workspace for open-source projects, examples, experiments, and my website.

## Agent development method

Start with [AGENTS.md](AGENTS.md), the [project map](docs/PROJECT-MAP.md), and the [shared workflow](docs/DEVELOPMENT-WORKFLOW.md). Coding uses the [repository-local skill](.agents/skills/build-translate-review/SKILL.md). Agree checkpoints first; keep task records in ignored project-local `.development/<task>/` directories and delete them after completion and final handoff. Project instructions may add local rules.

Before Go edits, use [Modern Go Guidelines](.agents/skills/use-modern-go/SKILL.md) from the repository. Both development skills are stored under root `.agents/skills/` and do not depend on a personal plugin installation. See [Codex Cloud prerequisites](docs/CLOUD-DEVELOPMENT.md) for toolchains, first-use downloads, and capability verification.

## Projects

| Project | Purpose | Run instructions |
| --- | --- | --- |
| `ArtificialMemory` | Hosted MCP decision journal; design prepared, implementation pending | [Memory README](ArtificialMemory/README.md) |
| `SSHMCP` | Authenticated MCP gateway and persistent SSH execution runner | [SSHMCP README](SSHMCP/README.md) |
| `GoogleHealthMCP` | Google Health / Fitbit importer backed by SQLite | [Google Health README](GoogleHealthMCP/README.md) |
| `AgentNewsfeed` | Private reading feed with browser OIDC and direct/gateway MCP | [Newsfeed README](AgentNewsfeed/README.md) |
| `AgentShoppingList` | Agent-managed mobile shopping app with built-in MCP | [Shopping README](AgentShoppingList/README.md) |
| `WebhookControl` | Standalone JSON webhook router with durable delivery | [Webhook Control README](WebhookControl/README.md) |
| `examples/xtrace-example` | A runnable distributed tracing demonstration | [XTrace README](examples/xtrace-example/README.md#run) |
| `examples/go-lang-case-study` | Go case studies, tests, and benchmarks | [Go case studies README](examples/go-lang-case-study/README.md) |
| `vvivid.dev` | Personal website built with Hugo Extended | [Website README](vvivid.dev/README.md) |

Each project owns its dependencies, build configuration, and tests. The root Makefile delegates to project Makefiles, so projects can use different languages and tools without sharing a dependency manager. Git is managed at this repository's root.

## Build and check

Run `make sshmcp-check`, `make google-health-check`, `make shopping-check`, or `make webhook-control-check` for an application. Each also has matching `-test` and `-build` targets. Use `make website-check`, `make website-test`, or `make website-build` for the personal website; each renders and validates the production Hugo output. Install the pinned Hugo Extended version from `vvivid.dev/.hugo-version` or set `HUGO=/absolute/path/to/hugo`; the [website README](vvivid.dev/README.md) covers installation and Docker alternatives. `make check`, `make test`, and `make build` cover all projects. Each project remains independently runnable; Docker builds use its directory as the build context.

For XTrace, install Make, Go 1.26 or newer, a C compiler for SQLite and the race detector, and Docker with the Compose plugin. Run from the repository root:

```sh
make check        # Project checks, including XTrace Compose validation
make test         # Tests for applications; production render/check for the website
make build        # Build all applications, the XTrace demo, and the website
```

The application binary is written to `examples/xtrace-example/bin/xtrace-demo`, which Git ignores. Checks stop on the first failure and do not start the Compose stack.

Use `make xtrace-check`, `make xtrace-test`, or `make xtrace-build` to select XTrace explicitly. These delegate to the same `check`, `test`, and `build` targets in its directory. As projects are added, give each its own Makefile and extend the root targets to delegate to them.

Use `make go-case-study-check`, `make go-case-study-test`, or `make go-case-study-build` for the Go 1.25 case-study module in `examples/go-lang-case-study`.

## Pull request checks

The [website workflow](.github/workflows/website.yml), [AgentNewsfeed workflow](.github/workflows/newsfeed.yml) and [AgentShoppingList workflow](.github/workflows/shopping.yml) are enabled. Other projects use manual root/project Make commands. Select the affected projects explicitly before a PR.

Website PR checks have read-only repository permissions and build both public base URLs without tailnet or publishing secrets. Main pushes run checks. After separate activation, `release/vvivid.dev/X.Y.Z` publishes staging artifacts and `vvivid.dev/vX.Y.Z` publishes production artifacts to private Zot, then records exact desired state in `VVivid/homelab-gitops`. Fleet reconciles only explicitly enabled website paths. The [publication contract](vvivid.dev/deploy/gitops/README.md) owns setup, credential boundaries and recovery; main and the old `release/vvivid-preview` branch never deploy automatically.

AgentNewsfeed PR/main checks include frontend, Go/race/build, Compose, real Linux amd64 container and Helm/release policy tests. Only canonical `AgentNewsfeed/vX.Y.Z` production tags can publish after `NEWSFEED_GITOPS_ENABLED=true` and shared environment activation. Its [production contract](AgentNewsfeed/deploy/gitops/README.md) reuses the existing artifact-publication identity/App/lock; no website behavior changes.

AgentShoppingList PR/main checks include browser sharing regressions, Go/race/build, real Linux amd64 container and Helm/release/publication tests. Only canonical `AgentShoppingList/vX.Y.Z` tags can publish after `SHOPPING_GITOPS_ENABLED=true` and shared environment activation. Its [production contract](AgentShoppingList/deploy/gitops/README.md) reuses the same identity/App/lock; data migration and live cutover remain separate.

Account for path-filtered PR checks being skipped on unrelated changes before making them required globally. Future service CI must reuse the monorepo's shared artifact-publication environment and existing Tailscale CI trust; each app remains a separate release and adoption decision.

## Independent versions

Projects are released independently using annotated Git tags:

```text
<project-directory>/v<major>.<minor>.<patch>
```

For example, the first XTrace release can be `examples/xtrace-example/v0.1.0`, and website releases use `vvivid.dev/vX.Y.Z`. Future projects use their own directory prefix and version sequence. A tag records the whole repository snapshot; its prefix identifies the released project.

For versions starting at `1.0.0`, increment the major version for breaking changes, minor for backward-compatible features, and patch for fixes. Use `0.x` while a project's interface is still evolving, and describe breaking changes in its release notes.

## Releasing a project

1. Follow the [branch and worktree procedure](docs/DEVELOPMENT-WORKFLOW.md#branches-and-worktrees). New editing tasks use one dedicated branch/worktree based on verified current `origin/main`. Branch names use `<type>/<area>/<task>`, for example `feature/xtrace/stale-replica-demo`. Read-only investigation may use the current checkout.
2. Complete agreed checkpoints and the project's validation. When instructed to create a PR, commit only intended task files. **Never push anything to a remote without the user's explicit permission for that push.** A PR creation request does not imply push permission. Prepare locally, obtain authorization covering the remote and branch, then push normally and open the PR against `main`. For example, from the repository root within the task worktree:

   ```sh
   git add -- examples/xtrace-example
   git diff --cached
   git commit -m "feat(xtrace): add distributed tracing demo"
   # Run only with explicit permission to push this branch to origin:
   git push -u origin feature/xtrace/stale-replica-demo
   ```

   Inspect the staged diff before committing; temporary `.development/` records and unrelated changes must be excluded. The example assumes this is the verified task branch. Include any required shared-file changes explicitly.
3. The user controls merging. After the PR is merged, identify the exact merged commit and verify it contains the intended project changes. Release publication and deployment remain separate actions requiring their corresponding instruction.
4. For an explicitly requested release, create the project-prefixed annotated tag at that verified merged commit. Obtain explicit permission covering the tag push before publishing it; release preparation alone does not authorize pushing. Substitute the actual verified commit for `MERGED_SHA`:

   ```sh
   git tag -a examples/xtrace-example/v0.1.0 MERGED_SHA -m "XTrace example v0.1.0"
   # Run only with explicit permission to push this tag to origin:
   git push origin examples/xtrace-example/v0.1.0
   ```

Never publish task changes by pushing directly to `main`. Branch naming and review rules are documented policy; they do not establish that server-side branch protection is configured.

Use a new version for subsequent releases; keep published tags unchanged. GitHub Releases can use these same tags with project-specific release notes.

Publishable libraries may need additional language-specific version metadata. The XTrace demo currently runs locally and does not need a published Go module path.

## Repository migration

SSHMCP and AgentShoppingList were imported with their original commit histories. GoogleHealthMCP was imported from its local source directory, which had no Git history. Existing standalone releases remain in their archived repositories. New releases use directory-prefixed tags such as `SSHMCP/v1.0.1` and `AgentShoppingList/v0.1.0-beta.6`.

`vvivid.dev/` was imported from an attached source snapshot without original Git history. It has its own pinned Hugo build and vendored theme; the archived GitLab deployment and operational scripts are retained as [legacy references](vvivid.dev/deploy/legacy/README.md). Their external infrastructure has not been migrated. See the [migration report](MIGRATION.md#website-snapshot-import--october-24-2026) for source and validation limits.

Development continues here. Use this repository for new issues and pull requests. Runtime credentials, databases, generated binaries, and local lab state are excluded. Moving source does not deploy or restart any service.
