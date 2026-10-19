# Sharing production rollout — October 19, 2026

The user authorized committing, merging and deploying the reviewed feature. Temporary sharing is deployed at https://shopping.vvivid.dev as `0.1.0-beta.3`, application commit `b739622` (Go 1.26.9, linux/amd64, built 2026-10-19T21:39:49Z).

## Source and artifact

- Reviewed feature commit: `43be95d`, branch `codex/temporary-item-sharing`.
- Original-checkout handoff preservation: `f308659`.
- Merge: `b73962278a3530259baf107c3d949cc4e9c06ca3`, pushed to origin/main and verified remotely. Only RESUME.md and CHECKPOINTS.md differ from reviewed feature source, preserving both coordination histories.
- Image: `agentshoppinglist:sharing-b739622-amd64`.
- Runtime image/index: `sha256:79427436f3543877cd2346199be4226f14775fb77a2bb741729181d518e5c0fc`.
- Manifest: `sha256:e08fc7b0438c836f9b6131114d7185b22f362f48de179e5cdd4d700faddb98d5`.
- Config: `sha256:4b0e5e79b7a40b9224e989e35a93452bd6c839bf5e37b6548cae2ecbcfb487ef`.
- Transferred archive: 11,249,664 bytes; SHA-256 `4c1576927133ed0232743640b070d6b7bab354b88542b452eb3adc032d77064d`.
- No new tag, GitHub Release or public registry publication was created. Subsequent handoff documentation commits do not change the running application source.

## Validation before rollout

The final sequential Builder/Translation/coordinator checks and independent Review are recorded in [implementation validation](SHARING-2026-10-19.md). Final Review: Approved, no blocking findings. Full checks/build and all 52 production-script regressions passed.

The exact merged amd64 image passed `SHOPPING_TEST_IMAGE=agentshoppinglist:sharing-b739622-amd64 make container-test`: production-binary OIDC/PKCE and official SDK journey with two synthetic owners, interrupted/concurrent replay, blocked SQLite retry, sessions/preferences, stop/start durability, and synthetic backup/restore compatibility. This used synthetic local data, never production backups.

Offline pinned Trivy scanned the exact image archive with no network, Docker socket or runtime secrets. Scanner digest: `sha256:2416d1f74045b57274df1a340eee6b510cacd73c66a6ccb8da092b117eb7d222`; database updated 2026-10-19T00:45:30.553047111Z. Result: zero known vulnerabilities in Debian 13.7, shopping and shopping-admin. Local ignored report: `.cache/image-audit/sharing-b739622-amd64-report.json`. This is a dated database result, not a guarantee against unknown vulnerabilities.

## Actual production rollout and preservation

Transferred the archive to the existing private deployment, verified its checksum, loaded and verified the exact amd64 image and revision. Validated existing Compose configuration; stopped only Shopping to drain requests. Before migration, readonly SQLite integrity and foreign-key checks passed. Complete original-table row fingerprints and secret fingerprints were compared in server memory only; no records, secrets or fingerprints were copied off-server.

Changed only SHOPPING_IMAGE in the private deployment environment and recreated Shopping with `--no-deps --pull never`. The existing volume, secrets, Traefik labels/routing, identity infrastructure and neighboring services were preserved. No production backup, snapshot, recovery key or data archive was created.

The healthy running container reports beta.3/b739622 and the expected image/index. Schema migrated 6 → 7. Comparison confirmed every preexisting table's rows unchanged (original migration history compared through version 6), unchanged runtime secrets, unchanged data volume and routing, and no new data archive files. All four new sharing tables were empty after migration. Compose's existing-volume ownership warning did not prevent reuse of the verified volume.

Traefik briefly returned 404 while the replacement container was starting; public routing recovered once Docker health became healthy.

## Actual public and browser checks

- `/health/ready`: 200, ready; container healthy and exact runtime version confirmed.
- Unauthenticated MCP: 401; unauthenticated share creation: 401.
- `/static/share.js`: 200.
- Invalid share path: 404 with generic unavailable page, `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, and `X-Robots-Tag: noindex,nofollow,noarchive`.
- Cross-origin public PATCH: 403 before mutation.
- Authenticated live browser: My list shows Share; entering selection replaces pickup controls with share selection, disables creation at zero selected, and selecting one item enables “Create link · 1 item”. Cancel restores the original unchecked pickup controls. No public link or guest write was created from personal production groceries during this check. No personal item names, screenshot, share code or capability are stored in this report.

Actual six-hour wall-clock waiting, new production guest creation/checkoff, and a production restart with a new share were not performed. Server deadline/replay/isolation and share persistence were validated locally as described in implementation evidence; the exact image separately passed process/container persistence tests. Existing CP03/06/10 physical-phone, receipt and client acceptance gaps remain open.

## Use

My list → Share → select items → Create link → Copy link. The copy modal contains only the URL and Copy link button. Anyone with the five-letter link can use the separate checklist for six hours; guest checkoff does not alter the owner's list, purchases or Insights.
