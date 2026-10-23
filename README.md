# vvivid-workspace

My personal workspace for open-source projects, examples, experiments, and my website.

## Projects

| Project | Purpose | Run instructions |
| --- | --- | --- |
| `SSHMCP` | Authenticated MCP gateway and persistent SSH execution runner | [SSHMCP README](SSHMCP/README.md) |
| `GoogleHealthMCP` | Google Health / Fitbit importer backed by SQLite | [Google Health README](GoogleHealthMCP/README.md) |
| `AgentShoppingList` | Agent-managed mobile shopping app with built-in MCP | [Shopping README](AgentShoppingList/README.md) |
| `examples/xtrace-example` | A runnable distributed tracing demonstration | [XTrace README](examples/xtrace-example/README.md#run) |

Each project owns its dependencies, build configuration, and tests. The root Makefile delegates to project Makefiles, so projects can use different languages and tools without sharing a dependency manager. Git is managed at this repository's root.

## Build and check

Run `make sshmcp-check`, `make google-health-check`, or `make shopping-check` for an application. `make check` and `make build` cover all projects. Each project remains independently runnable; Docker builds use its directory as the build context.

For XTrace, install Make, Go 1.26 or newer, a C compiler for SQLite and the race detector, and Docker with the Compose plugin. Run from the repository root:

```sh
make check        # Project checks, including XTrace Compose validation
make test         # Tests for all projects
make build        # Build all applications and the XTrace demo
```

The application binary is written to `examples/xtrace-example/bin/xtrace-demo`, which Git ignores. Checks stop on the first failure and do not start the Compose stack.

Use `make xtrace-check`, `make xtrace-test`, or `make xtrace-build` to select XTrace explicitly. These delegate to the same `check`, `test`, and `build` targets in its directory. As projects are added, give each its own Makefile and extend the root targets to delegate to them.

## Pull request checks

The [XTrace workflow](.github/workflows/xtrace-example.yml) runs automatically only for pull requests that change `examples/xtrace-example/**` and for matching pushes to `main`. It runs `make -C examples/xtrace-example check` directly, independently of the root Makefile. Changes confined to root files or workflow configuration do not trigger it; it can still be started manually.

SSHMCP, GoogleHealthMCP, and AgentShoppingList each have a workflow filtered to their own directory. A change confined to another project does not trigger the XTrace workflow.

If merge rules are added later, account for path-filtered workflows being skipped on unrelated pull requests before making their checks required globally.

This configures CI. Deployment automation can be added per project when a deployment destination and release credentials are defined; the XTrace demo currently runs locally.

## Independent versions

Projects are released independently using annotated Git tags:

```text
<project-directory>/v<major>.<minor>.<patch>
```

For example, the first XTrace release can be `examples/xtrace-example/v0.1.0`. Future projects use their own directory prefix and version sequence. A tag records the whole repository snapshot; its prefix identifies the released project.

For versions starting at `1.0.0`, increment the major version for breaking changes, minor for backward-compatible features, and patch for fixes. Use `0.x` while a project's interface is still evolving, and describe breaking changes in its release notes.

## Releasing a project

1. Run the project's documented checks and review the changes.
2. Commit the files for that project from the repository root. For XTrace:

   ```sh
   git add examples/xtrace-example
   git commit -m "feat(xtrace-example): add distributed tracing demo"
   ```

   On the initial repository commit, also stage this root README, Makefile, `.gitignore`, and `.github` workflow configuration.

3. Confirm `git status` is clean and the commit contains everything intended for the release. Create the annotated tag:

   ```sh
   git tag -a examples/xtrace-example/v0.1.0 -m "XTrace example v0.1.0"
   ```

4. Fetch and reconcile any existing GitHub history before pushing. Then publish the branch and the specific tag:

   ```sh
   git push -u origin main
   git push origin examples/xtrace-example/v0.1.0
   ```

Use a new version for subsequent releases; keep published tags unchanged. GitHub Releases can use these same tags with project-specific release notes.

Publishable libraries may need additional language-specific version metadata. The XTrace demo currently runs locally and does not need a published Go module path.

## Repository migration

SSHMCP and AgentShoppingList were imported with their original commit histories. GoogleHealthMCP was imported from its local source directory, which had no Git history. Existing standalone releases remain in their archived repositories. New releases use directory-prefixed tags such as `SSHMCP/v1.0.1` and `AgentShoppingList/v0.1.0-beta.6`.

Development continues here. Use this repository for new issues and pull requests. Runtime credentials, databases, generated binaries, and local lab state are excluded. Moving source does not deploy or restart any service.
