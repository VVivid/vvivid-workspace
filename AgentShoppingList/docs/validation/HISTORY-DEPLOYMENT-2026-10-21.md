# History UI production rollout — October21,2026

The owner requested deployment of the independently reviewed History change. It is live at https://shopping.vvivid.dev/history as `0.1.0-beta.4`, application commit `51ed114`, built2026-10-21T15:53:04Z, Go1.26.9/linuxamd64. Aisles, agent reordering and the separate shared-product-details worktree are excluded.

## Source and tested artifact

- Local release commit `51ed114` on main, parentcee0d4f. [Implementation and independent review](HISTORY-TOTALS-2026-10-21.md) completed before deployment. No subsequent application source change.
- Build input was an exact `git archive` of committed Docker/build/application inputs. Pinned Dockerfile and dependencies unchanged.
- Tag `agentshoppinglist:history-51ed114-amd64`.
- Runtime image/index `sha256:2cbf3414cf916052f736fc4bd68af80e626fcd46f93bccccb3b581b9d05418f1`.
- Manifest `sha256:52dd7a8d306a08b404206f7b632f5b610d3d0aa6648d85777d32751e8cbd194b`; config `sha256:5af2d7ee3e8ce91962ef215476d785d51034b7bd4608ebf9eab05fae3e362d49`.
- Archive11251200bytes, SHA256 `41e55ad3fda0b530a4a464141abe9c0b1b1e70d5159595e1302bf812bdb2175d`; local ignored `.cache/image-audit/history-51ed114-amd64.tar`, transferred to existing private deployment under `releases/`.
- Exact image `make container-test` passed: synthetic two-owner OIDC/PKCE/SDK purchase journey, retry/crash/durable-session checks and actual stop/start with identical durable counts/totals. Log `.cache/acceptance/history-51ed114-container.log`. Recovery-tool acceptance uses synthetic local data only; no production backups.
- Pinned offline Trivy image `sha256:2416d1f74045b57274df1a340eee6b510cacd73c66a6ccb8da092b117eb7d222`, public DB updated2026-10-19T00:45:30.553047111Z, reported zero known vulnerabilities in exact archive. `.cache/image-audit/history-51ed114-amd64-report.json`. Initial readonly scan-cache failure was corrected with in-memory scan cache; no scanner network or Docker socket access.

## Live replacement and preservation

Preflight verified healthy beta.3/applicationb739622, schema7, existing volume/routing and readonly SQLite integrity/foreign-key checks. Verified existing direct SSH for archive transfer; SSHMCP used for bounded runtime checks. An initial read-only preflight stopped after `find` encountered the protected secrets directory; no secret contents were printed, and a fresh connection completed explicit bounded checks.

Server verified archive SHA and loaded image/revision/platform before stopping only Shopping. All table-row fingerprints and runtime-secret fingerprints stayed in server-process memory. Only `SHOPPING_IMAGE` in the existing private environment was replaced; no Compose, identity or routing configuration was changed. Existing data volume and secret mounts retained.

First replacement restored the previous image automatically when environment **list order** differed. Database-row preservation had already passed. Separate server-side comparison confirmed identical names/values. Corrected the operational check to compare environment maps, then retried the same tested bytes. This changed no application source. Final replacement passed all-row equality, integrity/foreign keys, identical secrets/mounts/environment values/Traefik labels and unchanged data filenames. Docker healthy, exact beta.4/51ed114/image verified. Schema remains7; no migration, backup, snapshot, off-server production copy or recovery key was made. Existing-volume Compose warning was informational; the same verified volume was reused.

## Public and browser verification

- System-trusted HTTPS `/health/ready`:200/ready; unauthenticated `/mcp`:401.
- Public History sign-in shell:200, private records not disclosed. Deployed stylesheet includes new price layout. Python urllib initially lacked its local CA chain; system curl verified TLS normally, without disabling verification.
- Refreshed owner's existing signed-in Edge History tab: two real purchase headers show right-aligned USD amounts and dates below. No personal amounts/items were copied into this report. Initial browser Refresh failed message during replacement disappeared after successful reload.
- Native live pointer expansion/collapse and visible receipt lines passed. Screenshot visually inspected. New app version established from exact healthy container; no synthetic receipts or shopping writes were created in production.
- Temporary unauthenticated verification tab closed; owner tab remains open. SSHMCP connection closed after checks. Physical-phone acceptance and existing CP03/06/10 beta follow-up remain unexecuted.

## Repository publication

Automatic approval review rejected `git push origin main`: it requires explicit authorization for the consequential default-branch remote write and did not accept the transcript as establishing that authorization. The push was not executed or bypassed. Verified remote main remainscee0d4f. Local release and deployment handoff commits are retained for a later explicitly authorized push; the deployed binary remains51ed114 regardless of handoff-only commits.
