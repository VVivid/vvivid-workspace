# Modern Go Guidelines provenance

Vendored from JetBrains' installed `modern-go-guidelines` plugin version **1.1.1** on October 23, 2026. The skill and both wrappers are copied unchanged; `scripts/VERSION` pins the downloaded CLI to **v0.1.1**. The plugin package version and CLI version are separate identifiers.

- [Upstream repository](https://github.com/JetBrains/go-modern-guidelines)
- [Pinned source](https://github.com/JetBrains/go-modern-guidelines/tree/v0.1.1)
- [Skill at the CLI source tag](https://github.com/JetBrains/go-modern-guidelines/blob/v0.1.1/plugin/skills/use-modern-go/SKILL.md): the installed plugin skill is a separately packaged revision; its imported hashes below identify the exact vendored files.
- [License](../LICENSE): Apache-2.0, copied from the matching v0.1.1 Go module source. No NOTICE file is present in that source.

Repository additions are `agents/openai.yaml` for discovery metadata and this provenance record. The repository's root instructions supply monorepo routing and permission rules without modifying the upstream skill. See [cloud development](../../../../docs/CLOUD-DEVELOPMENT.md) for setup and validation limits.

## Imported file SHA-256 hashes

| File | SHA-256 |
| --- | --- |
| `SKILL.md` | `fd36f201ed06df6511f3f13481a97ea33609b4904fae3ec9d7d66f57112da54b` |
| `scripts/VERSION` | `588f5d8e92b8639ca0f92b568a7f881a32531a06112de4bb7705f4fbeede5ae5` |
| `scripts/run-tool.ps1` | `8cd079da7625eaed81f48ef13eaa3d5957666fab12ba4554ff50f676d9ccc756` |
| `scripts/run-tool.sh` | `a593b2484834fc7d068e7cbe1a09f92b149ed61b9a07556ea50790d95f50a25b` |

For an intentional update, select the upstream release, review skill and wrapper changes and license requirements, update the pin and this record together, and validate from a clean cache. Do not silently download a different CLI or commit generated binaries/caches.
