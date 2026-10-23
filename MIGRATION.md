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
