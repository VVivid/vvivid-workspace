# Project migration — October 23, 2026

The canonical development directories are now `SSHMCP/`, `GoogleHealthMCP/`, and `AgentShoppingList/` at this repository's root. Each project retains its own module, build context, dependencies, and runtime configuration. There are no nested Git repositories.

## Source and history

- SSHMCP: full history imported without squashing through `4945d2c`.
- AgentShoppingList: full history imported without squashing through `ff47c9b`, including four commits after standalone GitHub main `cee0d4f`.
- GoogleHealthMCP: source imported from the local directory; it had no Git metadata. No accessible standalone repository was found at `VVivid/GoogleHealthMCP`; there is no known repository to archive for this project.
- Existing standalone release tags/assets remain at their original locations. New releases use the directory-prefixed naming documented in README.md.
- The original local checkouts remain available: SSHMCP has workspace-bound private lab state, and AgentShoppingList has a separate linked worktree. They are retained to preserve those records; use the monorepo for new development. No ignored local files, credentials, databases, or lab keys were published. Google Health's private operational handoff notes remain only in its original local directory; public resume/checkpoint files describe source status.

## Validation executed from the new directories

- SSHMCP: `make runner-check` (Go race tests and vet), all 90 Python unit tests, and `go build ./...` passed.
- GoogleHealthMCP: `make check build` (vet, race tests, binary build) passed.
- AgentShoppingList: `make check build` passed using Go 1.26.9 and Node 24; formatting, all 52 browser-script unit tests, vet, all ordinary Go tests, and both binaries passed.
- XTrace: `make check` passed, including both modules' vet/race tests/builds and Compose configuration validation.
- Both original source commits are ancestors of the workspace history. Project source trees match the originals except for documented migration links and handoffs. Each application's CI uses its own directory filter and working directory.

These checks used local synthetic fixtures. No VM lifecycle, production deployment, personal database, OAuth grant, scheduled collector, or live application state was changed. Hosted CI results are separate from these local results.

## Publication and archived repositories

Workspace source was published at `11a20f92ae30d2cd11791ec51b99ac18a484f938`.

- [SSHMCP](https://github.com/VVivid/sshmcp): migration notice committed as `308e437`; archive confirmed through GitHub settings and repository metadata.
- [AgentShoppingList](https://github.com/VVivid/AgentShoppingList): migration notice committed as `e7892a7`; archive confirmed through GitHub settings. Its existing private visibility was retained.
- Both notices link directly to their new project directories and direct new issues and pull requests to the workspace.
- GoogleHealthMCP has no confirmed accessible standalone GitHub repository to update or archive.

Hosted GitHub Actions validation for the imported source:

- [SSHMCP](https://github.com/VVivid/vvivid-workspace/actions/runs/39992255425): passed.
- [GoogleHealthMCP](https://github.com/VVivid/vvivid-workspace/actions/runs/39992046489): passed.
- [AgentShoppingList](https://github.com/VVivid/vvivid-workspace/actions/runs/39992046448): passed.
- [XTrace example](https://github.com/VVivid/vvivid-workspace/actions/runs/39992435297): passed.

## Website snapshot import — October 24, 2026

`vvivid.dev/` was imported from the attached personal website archive as a source snapshot, without original Git history. Content, static assets, and asset overrides were preserved. Hugo compatibility changes move legacy template locations and replace removed template accessors without changing content front matter or URLs.

The archive contained an empty theme directory and no recoverable theme revision. Hugo Blog Awesome `v2.0.1` at commit `46d1ab1a9cc8ad24a3d482af618771d708d91c2f` is vendored with its MIT license and [provenance](vvivid.dev/themes/hugo-blog-awesome/UPSTREAM.md). This supports a reproducible build, but exact visual parity with the original site cannot be established. Hugo Extended `0.162.1` is pinned in the project version file and Docker builder image.

Local validation of the imported source passed: production Hugo build (71 pages, 16 static files), original content/asset byte comparisons, required routes, internal generated references, sitemap targets, and case-insensitive agent-guide exclusion. A disposable leaked-guide fixture and wrong-version Hugo fixture were rejected. The standalone Docker image built successfully; an ephemeral nginx container passed configuration and local route checks and rejected a nested mixed-case guide URL. These checks did not verify browser rendering or external services.

The former GitLab pipeline is retained under [deploy/legacy/](vvivid.dev/deploy/legacy/README.md). Its registry, external `/infra/compose.yml`, preview/live services, Traefik network, and credentials were not migrated. The new GitHub workflow builds and checks the site only. These website results describe a local source import and local validation; the workspace publication and hosted CI results above apply to the earlier project migration. No website publication or live deployment is established by this import.
