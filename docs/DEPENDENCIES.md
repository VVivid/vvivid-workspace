# Pinned implementation tools

Selected October 19, 2026 from current primary documentation. These versions are deliberate inputs to the following checkpoints, not evidence that unfinished adapters have been tested.

| Component | Pin | Where used |
| --- | --- | --- |
| Go | 1.26.5 | Installed toolchain verified; `go.mod` and `.go-version` |
| Module | `agentshoppinglist` | Local imports; no invented remote repository path |
| Tailwind CSS and CLI | 4.3.0 | Exact `package.json` versions and npm lockfile; build-time only |
| Official MCP Go SDK | `github.com/modelcontextprotocol/go-sdk` v1.8.0 | CP06 typed stateless HTTP adapter in go.mod/go.sum |
| SQLite driver | `modernc.org/sqlite` v1.60.1 | CP02 store/go.mod/go.sum, exact upstream libc v1.77.1 retained |
| OIDC discovery/ID-token/JWKS | `github.com/coreos/go-oidc/v3` v3.21.0 | CP03 exact issuer, web ID tokens and rotating keys |
| JOSE/JWT | `github.com/go-jose/go-jose/v4` v4.1.4 | CP03 RS256 access tokens; go-oidc upstream pin |
| OAuth2/PKCE | `golang.org/x/oauth2` v0.37.0 | CP03 code exchange and S256 helpers |
| Migrations | Application-owned embedded numbered SQL | CP02 transactional checksum/version history; no separate process/library |

CP01 used the standard library only. CP02/03 add actual persistence/authentication requirements/checksums; CP06 adds the MCP SDK and its pinned transitive libraries. Verify upstream advisories and pins again before release.

Tailwind 4.3.3 was initially evaluated. Its CLI dependency chain produced four npm high-severity findings from `braces` via `micromatch` and `@parcel/watcher` ([advisory](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm)). Pin both CSS and CLI to 4.3.0 to avoid that chain; use the lockfile rather than upgrading automatically. This affects build tooling only. Actual audit validation is recorded with CP01 evidence.

The pure-Go SQLite driver supports the intended Darwin development and Linux deployment targets without a C toolchain. Its documentation requires keeping `modernc.org/libc` at the exact version selected by the driver's own `go.mod`; CP02 must preserve that constraint. Do not upgrade the transitive library independently.

CP02 verified downloaded sqlite v1.60.1 requires libc v1.77.1 and retained that exact version. DSN settings configure every newly opened/replacement connection. The read pool is file-read-only and query-only; one writer uses immediate transactions. Standard-library timezone data is embedded for minimal images.

The official MCP SDK v1.8.0 supports protocol revision `2026-07-28`. CP06 should use stateless Streamable HTTP as described by that release, and must separately verify selected clients, authentication, and per-user cache behavior. CP01 mounted no MCP handler; CP06 now mounts the protected stateless handler when identity is configured. SDK HTTP tests do not establish actual Codex/ChatGPT OAuth interoperability.

## Migration plan for CP02

Keep ordered SQL files embedded in the Go binary beside the SQLite store. Apply one pending migration per short transaction before serving requests. Store the applied version and content checksum in `schema_migrations`; reject altered migration history or a database schema newer than the binary supports. A failed transaction must leave the previous version usable. Forward migrations are the default; recovery from an incompatible migration uses a verified consistent backup, not an assumed reverse migration.

CP02 must test fresh creation, upgrade with preserved records, rollback on failure, and foreign-key configuration on every connection path. CP10 supplies the release backup/restore procedure. CP01 creates no database or schema.

## Sources

- [go-oidc v3.21.0](https://github.com/coreos/go-oidc/releases/tag/v3.21.0), [go-jose v4.1.4](https://pkg.go.dev/github.com/go-jose/go-jose/v4@v4.1.4), and [OAuth2 v0.37.0](https://pkg.go.dev/golang.org/x/oauth2@v0.37.0), consulted October19. Installed exact pins are in go.mod/go.sum. ID tokens never authenticate bearer requests.
- [Tailwind CLI installation](https://tailwindcss.com/docs/installation/tailwind-cli) and [v4.3.0 release](https://github.com/tailwindlabs/tailwindcss/releases/tag/v4.3.0).
- [Official MCP Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0).
- [SQLite driver v1.60.1 documentation](https://pkg.go.dev/modernc.org/sqlite@v1.60.1).
- [SQLite WAL](https://sqlite.org/wal.html) and [online backup](https://sqlite.org/backup.html).
