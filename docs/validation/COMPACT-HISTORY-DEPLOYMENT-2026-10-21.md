# Compact History production rollout — October21,2026

The owner selected the original preview, supplied its screenshot, and asked to implement it after the local implementation/deployment distinction was explained. The reviewed compact History layout is now live at https://shopping.vvivid.dev/history as `0.1.0-beta.5`, application `faafb3d`, built2026-10-22T01:36:32Z (October21 Eastern), Go1.26.9/linuxamd64.

## Source and release validation

- Release commit `faafb3d`, parent5025980, includes the exact locally Approved application source plus preserved handoffs. [Implementation, actual browser checks and independent Review](COMPACT-HISTORY-2026-10-21.md) remain the source approval evidence. No application edits after Review.
- Exact committed Docker/build/application inputs supplied through `git archive`; existing pinned Dockerfile/dependencies unchanged. Tag `agentshoppinglist:compact-history-faafb3d-amd64`.
- Image/index `sha256:64b7118329c4d26cc34fe9d964e120eeafe9457ea899d6e74b0f2ee0b195ea0c`; manifest `sha256:b4b70cb5cdff9a10e960be6ed07906aead648769bf551d958c4aa67ec7f71a42`; config `sha256:524644511519d51c0c44d1d3fa00c9a07fb5cb2ee562c143f4e1c90b528c317a`.
- Archive11253248bytes; SHA256 `b217de388b536f5b6120e273cdf88aaffbc4c0ffb1c5e44324d32a9f5e954ce7`. Local ignored `.cache/image-audit/compact-history-faafb3d-amd64.tar`; private server release directory received the same archive.
- Exact-image container acceptance passed production-process two-owner/OIDC/PKCE/SDK/purchase/retry/crash/durable-session journey and actual container stop/start with identical durable reports. `.cache/acceptance/compact-history-faafb3d-container.log`. Synthetic recovery-tool checks ran only against disposable local data; no production backup.
- Exact archive offline Trivy scan reported zero known vulnerabilities. Same pinned scanner `sha256:2416d1f74045b57274df1a340eee6b510cacd73c66a6ccb8da092b117eb7d222`, cached public DB updated2026-10-19T00:45:30.553047111Z. Dated scan, not a fresh advisory guarantee. `.cache/image-audit/compact-history-faafb3d-amd64-report.json`.

## Live rollout and preservation

Preflight verified healthy beta.4/application51ed114 and existing Compose configuration. SCP transferred only the image archive; SSHMCP performed bounded preflight and deployment commands. Server verified archive checksum, new amd64 image/revision/version and expected previous image before replacing Shopping.

Stopped only Shopping, then kept all SQLite table-row fingerprints and runtime-secret fingerprints in server-process memory. Replaced only SHOPPING_IMAGE in the existing private environment, preserving its ownership/mode; recreated with `--no-deps --pull never`. Old image restoration was prepared for any verification failure; no restoration was needed. Compared environment maps and mount records by destination so array order cannot produce a false difference.

Final assertions passed: all original table rows identical, SQLite integrity/foreign keys valid, secrets identical, same volume/mounts/environment values/Traefik labels and identical data filenames. Schema remains7; no migration, backup, snapshot, production data archive/off-server database copy or recovery key. Compose's existing-volume warning was informational; the verified existing volume was reused. Docker healthy and exact beta.5/faafb3d/image established.

Deployment job `bdf87eaebbd55b63e905fe007e20103e` completed exit0. Preflight job `6a14286d538f23faff4b0990d2a9ed7b` completed exit0. SSHMCP connection closed after checks.

## Public and actual owner-browser verification

- System-trusted public HTTPS ready response passed; unauthenticated MCP401. Deployed CSS includes compact receipt and secondary disclosure styles.
- Refreshed the owner's existing signed-in Edge History tab and expanded its first real receipt. Verified compact saved product/quantity labels, right-aligned amounts, one Tax row, no product SVGs and Receipt details initially closed. No personal item names, amounts or location were copied into this document or another artifact.
- At320 and390 actual document clientWidth equaled scrollWidth; zero overlapping name/price rows and aligned amount right edges at both sizes. Opening Receipt details exposed the retained information and available Buy again controls; closed it afterward. No real Buy again, purchase/list mutation or synthetic production record was created.
- Owner's tab remains open; temporary viewport override reset. Browser inspection is live acceptance; physical-phone acceptance and remaining CP03/06/10 beta gaps remain unexecuted.

## Boundaries and repository

Only compact expanded History shipped. Aisles, agent reorder capability, unrelated shared-product-details worktree and identity/infrastructure are unchanged. Prior direct-main GitHub push remains unauthorized/rejected; no retry, workaround, new push, tag, GitHub Release or registry publication was attempted. Local release/deployment commits are retained. Subsequent handoff-only commits do not change the deployed applicationfaafb3d.
