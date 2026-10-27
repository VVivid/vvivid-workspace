# Compatibility validation

This bundle targets the exact shopping-compatible gateway source recorded in `baseline.sha256`. It is an offline source patch, test fixture and checker; it does not deploy a gateway or modify a live configuration. The application owns its MCP endpoint directly.

The reviewed implementation passed these checks with Go 1.26.9:

- Exact input inventory and SHA-256 verification, with the input source unchanged after checking.
- Verified Go module checksums and reproduction of the current baseline binary.
- Zero-fuzz patch application, expected changed-file hashes, formatting and `go vet ./...`.
- Gateway and runner unit tests and race tests, including existing shopping compatibility tests.
- Resource isolation for newsfeed, SSH and shopping; disabled newsfeed configuration; unknown clients; duplicate, Basic/body and refresh-omission cases; canonical origins and unchanged PKCE/upstream decisions.
- Candidate build, reverse-patch restoration and full baseline hash comparison.
- Rejection of unexpected files, modified input, symlinks, existing/nested output and source-local caches.

The checker must be run against the matching, separately obtained source snapshot. See [README.md](README.md) for the invocation and the source manifests for exact identities. Full historical commands, local artifacts and development-stage records remain outside version control.

| Reviewed Linux/amd64 artifact | SHA-256 |
| --- | --- |
| Newsfeed-compatible gateway | `272a6fc6324233549227af4764b2cba3c46c0bf1a7f2319660ffcf0c416d568a` |
| Reproduced prior baseline | `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917` |

The Newsfeed application's checks are independent: formatting, vet, unit/race tests, both binary builds, synthetic Compose validation and hardened-container persistence acceptance passed locally. The container tests use synthetic identities and articles, no published ports, a read-only root and a temporary volume.

Synthetic tests do not prove a real client's callback, consent, token issuance or browser layout. ChatGPT connection acceptance remains separate from application implementation. Applying or restarting the shared gateway requires its own narrowly scoped authorization, independent recovery and rollback safeguards; source publication grants none of those actions.
