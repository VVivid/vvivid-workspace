# Codex Cloud development

The repository contains shared development skills under root `.agents/skills/`. Codex discovers repository skills from the working directory up to the repository root. Personal skills on a local computer are not synced to cloud tasks. See the official [skills documentation](https://learn.chatgpt.com/docs/build-skills) and [cloud environment documentation](https://learn.chatgpt.com/docs/environments/cloud-environments).

## Skills and permissions

| Skill | Purpose | Additional requirements |
| --- | --- | --- |
| [Build -> Translate -> Review](../.agents/skills/build-translate-review/SKILL.md) | Each agreed coding checkpoint's implementation, readability, validation, and independent review | Independent agents; exact required model/effort and host runtime evidence |
| [Modern Go Guidelines](../.agents/skills/use-modern-go/SKILL.md) | Version-specific guidance before Go edits | POSIX shell and Go on Linux; writable cache; download access on first use |

Use these repository copies, including from project subdirectories; do not reference a developer's home directory or plugin cache. The Go skill includes both Linux/macOS and PowerShell wrappers, a pinned CLI version, UI metadata, [license](../.agents/skills/use-modern-go/LICENSE), and [provenance](../.agents/skills/use-modern-go/references/UPSTREAM.md). Do not commit installed binaries or tool caches. The CLI is downloaded rather than vendored; the repository is portable, not self-contained for offline first use.

**Never push anything to a remote without the user's explicit permission for that push**, including branches, tags, and deletions. Cloud setup, environment preparation, plan approval, and PR creation requests do not supply that permission. The uncommitted local skills must eventually be included in an authorized commit and push before a remote checkout can receive them; this guide does not authorize publication.

## Prepare the selected projects

Inspect the target checkout's manifests and Makefiles rather than assuming the cloud image has every tool. Provision only the selected task's prerequisites through cloud environment setup; the commands below do not start services or deploy anything.

| Area | Development prerequisites |
| --- | --- |
| Shared | Git, Make, POSIX shell, writable caches, and a compatible Go toolchain |
| AgentShoppingList | Go 1.26.9 or compatible newer toolchain; Node.js 24/npm; `npm ci --ignore-scripts` before CSS builds |
| GoogleHealthMCP | Go compatible with its `go.mod`; SQLite driver is pure Go |
| SSHMCP | Go compatible with its `go.mod`; Python 3.10+ for local checks; VM/lab exercises have separate Multipass/SSH/runtime requirements |
| XTrace | Go compatible with its main module; C compiler for SQLite/race checks; Docker Compose for Compose validation or the separately requested full demo |

The XTrace Traefik plugin is a nested Go 1.20 module interpreted by Traefik. Target it explicitly when requesting guidelines; a newer installed toolchain does not authorize newer syntax there. Application `go.mod` files and project instructions remain authoritative if versions change.

Configure download access for project dependencies and the pinned Go CLI according to the cloud environment's network policy. Go normally uses `proxy.golang.org` and `sum.golang.org`; toolchain downloads and proxy redirects may need additional destinations. Package access is separate from credentials and remote-write authorization. Test actual setup requests and report blocked destinations without weakening network or checksum verification.

From the repository root, prewarm the pinned CLI using an explicit version; this reads guidance without changing application source:

```sh
sh .agents/skills/use-modern-go/scripts/run-tool.sh --version
sh .agents/skills/use-modern-go/scripts/run-tool.sh list --go-version 1.26
```

The wrapper installs `github.com/JetBrains/go-modern-guidelines` at the version in `scripts/VERSION` into `$XDG_CACHE_HOME/go-modern-guidelines/<version>` or `$HOME/.cache/go-modern-guidelines/<version>`. It needs Go 1.25+ to build the pinned v0.1.1 CLI (or permitted automatic toolchain download). It verifies the installed version. A prepared cache can avoid subsequent CLI downloads, but target-version detection and project builds still need suitable tools. Leave `GO_MODERN_GUIDELINES_DEV` unset during ordinary development.

Before an actual Go edit, use the full list for the target file and explain only relevant IDs:

```sh
sh .agents/skills/use-modern-go/scripts/run-tool.sh list --file-path AgentShoppingList/cmd/shopping/main.go
```

Do not truncate or filter guideline output. Run project validation selected by [the development guide](../DEVELOPMENT.md); dependency installation is not a passing build or test.

## Verify cloud capabilities before coding

The repository workflow requires Builder and Review on `gpt-6.1-sol` with `max`, and Translation on `gpt-6.1-sol` with `xhigh`. Verify independent-agent support and actual runtime model/effort evidence in the target cloud host. Requested settings, repository metadata, or an agent's self-report are insufficient. If unavailable or unverifiable, stop the affected coding workflow incomplete; do not substitute a single-agent review or silently change models. Vendoring the skill does not provision these capabilities.

Follow the shared branch/worktree policy. A cloud task checkout is not automatically proof of the required dedicated branch/worktree or a fresh base; inspect it before editing and use a task worktree when needed. Missing `origin/main`, restricted Git metadata, or unavailable worktree support is a prerequisite to resolve, not permission to edit `main`.

Ignored `.development/<task>/` records are local working state. They do not accompany Git clones, pushes, or new tasks. For an authorized local-to-cloud handoff, supply unfinished state in the cloud chat or explicitly transfer the task's records outside Git. Never commit them for transport. Treat cloud workspace retention as temporary; consult current host behavior before relying on recovery.

Cloud credentials, MCP connections, desktop tools, private services, Docker/VM access, and browser capabilities must be verified independently. This change configures none of them and does not authorize live authentication, business operations, deployment, or infrastructure changes.

## Validation boundary

Local portability checks exercise repository files without the personal skill installation. They do not establish Linux execution, actual cloud skill discovery, independent stage launches, or application acceptance. A real Codex Cloud task must verify those capabilities and run its scoped project checks before claiming cloud readiness.

The local macOS validation used a fresh temporary copy of the repository skills and an empty CLI/build cache. First-use installation returned v0.1.1 with Go checksum verification enabled; it reused the local Go module cache and allowed registry/checksum requests. The copied CLI resolved the shopping module's Go 1.26 guidelines and the nested Traefik module's Go 1.20 guidelines, and `explain` succeeded. A prewarmed CLI worked with Go absent from PATH; an empty CLI cache without Go failed clearly. Relative links, shell syntax, YAML metadata, imported-file hashes, and absence of developer-specific paths/symlinks were checked. The Windows wrapper was copied unchanged but not executed. Neither a Linux VM nor a real Codex Cloud task was exercised.
