# Shopping beta deployment plan

Prepared October 19, 2026, after explicitly authorized read-only discovery on `personal_prd`. This plan has not been applied. The OAuth compatibility bundle is independently Approved locally and unapplied. The application no-backup correction is in its local workflow; replacement images, live compatibility and daily-beta acceptance remain incomplete.

## Verified deployment inputs

| Input | Observed value |
| --- | --- |
| Server platform | Linux x86_64 / amd64 |
| Docker Engine | 29.1.3 |
| Existing Traefik | v3.7.5, network `proxy` |
| Public HTTPS entrypoint | `immich-public-websecure` (public443 maps to container8443) |
| Public HTTP entrypoint | `immich-public-web` (public80 maps to container8088) |
| Certificate resolver | `letsencrypt` |
| Existing Keycloak | 26.8.1; loopback127.0.0.1:18082 |
| Issuer | `https://ssh-mcp.vvivid.dev/realms/sshmcp` |
| Proposed shopping origin | `https://shopping.vvivid.dev` |
| Public server address | 144.91.70.238, also current SSHMCP DNS address |

Existing issuer discovery and protected-resource metadata returned HTTPS200. Discovery advertises S256 and the existing SSH scope. The gateway systemd service is active. Successful SSHMCP connect/commands demonstrate the current authorized administration path works; they do not prove a fresh client login/refresh flow.

The server resolver could not resolve `shopping.vvivid.dev`. No shopping router or shopping client/resource registration was found. This DNS observation is from the server resolver, not an authoritative DNS-zone inventory. The realm has one enabled account; no subject, username, email or credentials were copied into project evidence.

## Required OAuth compatibility work before live approval

The existing login proxy rejects the shopping resource before Keycloak. A bounded live GET probe for `shopping-codex` and resource `https://shopping.vvivid.dev/mcp` returned400 with `invalid_target`. Inspected deployed source agrees: it guards a single configured SSH resource.

Prepare an explicit client-to-resource mapping preserving the SSH bindings:

| Client | Only permitted resource |
| --- | --- |
| sshmcp-codex / sshmcp-chatgpt | https://ssh-mcp.vvivid.dev/mcp |
| shopping-web | https://shopping.vvivid.dev/api/v1 |
| shopping-codex / shopping-chatgpt | https://shopping.vvivid.dev/mcp |

Authorization, code exchange and refresh must reject cross-resource targets, duplicate/conflicting client parameters, noncanonical routes and Basic/body client disagreement. Preserve current required-resource rules for SSH clients. Shopping grants must never include SSH scopes/audiences. Keycloak continues handling user authentication, PKCE, consent and signing. This extends the existing login routing only; shopping tools remain in the shopping application, with no separate MCP gateway.

The deployed `/home/admin/sshmcp/source/internal/gateway/authproxy.go` SHA256 is `98d4ed1e2aaf98c4acf34abdafc88abba3bd25e88543eb3b68d22399c6970741`. The local SSHMCP checkout differs (`840ac17e507da67dae699db4ef09a941f140a67610e8429923801c5ce661fc0a`) and has an older constructor/interface. Do not build or replace production from that checkout blindly.

A credential-free source export was initially rejected under the discovery-only authorization. The human then explicitly authorized the narrow deployed-source transfer and subsequently the required `internal/runner` dependency. Both completed, all connections closed, with no live writes. Only `go.mod`, `go.sum`, `internal/gateway`, `cmd/sshmcp-gateway`, and `internal/runner` were copied. No private config, environment, key, credential, database or user record was exported. Source archive SHA256: `5ac441596d754fc711cec2c309b8bdd22958293122f8f492590287d2a354e177`; runner archive: `bfcf3ae6d21ae9abebc46f2cece58c488093c86318ce7a669e59c0a5419d016b`. Historical export rejection is cleared for this specific approved payload, not for unrelated production data.

[Local compatibility bundle](../deploy/oauth-compat/README.md) contains the exact-baseline source patch, added tests, original/patched file manifests, an offline scratch-only apply/test/build/reverse checker, and future atomic apply/rollback steps. The original export and full working source remain ignored. Stage evidence is [recorded separately](../deploy/oauth-compat/VALIDATION.md). Initial Translation returned a root-module packaging failure; Builder corrected it by storing the unchanged test as `oauth_compat_test.go.txt`, which the checker stages at its real gateway test path. Fresh Translation, coordinator final root/checker validation and independent Review have now completed, with Review Approved and no actionable findings. The optional `shopping_origin` enables only the three fixed shopping client bindings; original SSH/realm behavior and upstream Keycloak grant/PKCE decisions remain. No separate gateway, issuer change or dynamic registration is included. The separate application no-backup correction is not covered by that proxy verdict.

Observed unit `sshmcp-gateway.service` uses `/usr/local/bin/sshmcp-gateway --config /etc/sshmcp/gateway.json` from `/etc/systemd/system/sshmcp-gateway.service`. The private config was not read/exported. Future application must stage the reviewed binary and a private one-property config merge, preserve all SSH settings, and use an independent recovery path plus approved rollback watchdog: this gateway also carries the SSHMCP administration channel and its restart can interrupt that channel. No restart or live apply is authorized/executed. Actual selected-client callback inputs are tracked in [CLIENT-SETUP.md](CLIENT-SETUP.md).

The human confirmed a working direct SSH administration path independent of SSHMCP on October 19. This records user-reported availability; this local workflow did not open or verify a direct SSH session. Confirm that path and the approved rollback watchdog before a future authorized gateway restart. The confirmation does not authorize a restart or live write.

## Historical shopping artifacts — superseded candidate

Previously approved code: `05ff303f5c543e5a932e1f8b36ffc31c2e44c210`, version0.1.0-beta.1, Go1.26.9. These exact images passed process/restart and synthetic recovery-tool/offline advisory checks locally; amd64 execution used Docker emulation on the Mac. They still contain the rejected automatic pre-migration backup behavior and are superseded as deployment candidates. Do not transfer or deploy them. After the no-backup workflow, rebuild/test/scan replacement final-commit images and select amd64 for this server. Existing local evidence does not establish live server runtime behavior.

- Local image tag: `agentshoppinglist:beta-amd64`.
- Local index ID: `sha256:f5ba662e527b769409ef04a3de75a4e72d29f1a2016efd2ebad195a7b57b3d94`.
- Image config ID: `sha256:090adaf1361737077e20c384c42d72747000fa3616d83bafb3edc1d647d7ecce`.
- Local image archive: `.cache/image-audit/beta-amd64.tar`,11293696bytes, SHA256 `1f98acc0466300ab1ce564498f47111c15591a81a165e417274398006e9a3129`.
- Minimal committed build-source archive: `.cache/release/agent-shopping-list-05ff303-source.tar.gz`,137771bytes, SHA256 `7656a1c75feb098058ad5ccfd6df0729071cccb90d66435c48cd7695d583bf21`. It includes only Docker/build inputs and application code/assets from that commit.

Neither historical archive has been transferred or published. Replacement image/source identities must be recorded after the new workflow. Choose an authenticated artifact-transfer method during rollout planning; verify replacement archive SHA and loaded image identity before execution. Do not create a public download endpoint or publish a registry image without authorization.

`deploy/personal-prd.environment.prepared` records observed routing inputs and proposed shopping-only names/paths. Those proposed directories, volume and secrets do not exist as a result of this work. `deploy/compose.yaml` publishes no app port and preserves unrelated services. It still requires actual secret provisioning and an available exact image.

## Shopping-only live change set to review after the proxy patch

1. Record nonsecret source/binary hashes and current SSH acceptance without creating binary/config/database backups. Establish an independent administrative channel. Prepare a tested rollback by rebuilding the original gateway behavior from the already-authorized source and removing only the new shopping_origin property; do not retain a copy of the live private config or data.
2. Apply only the independently reviewed login compatibility patch against verified deployed hashes, with immediate rollback if existing SSH login/resource/refresh checks regress. Do not change the SSH MCP tool path, audience or scope.
3. Create separate shopping web, Codex and ChatGPT clients/scopes and canonical API/MCP resource registrations through supported Keycloak administration, not direct database writes or whole-realm reimport. Web gets read/list-write only. Agent clients get explicitly consented shopping permissions. Disable password/service-account/device grants and inherited unrelated scopes/audiences; use S256 and access lifetime at most300s. Background/offline consent remains deferred.
4. Use the exact web callback `https://shopping.vvivid.dev/auth/callback`. Existing SSH registrations currently use Codex `http://127.0.0.1:18443/callback` and ChatGPT `https://chatgpt.com/connector_platform_oauth_redirect`; these are observed reference values, not proof of the callbacks for newly configured shopping clients. Verify the actual selected clients before registering exact shopping callbacks; no wildcard.
5. Provision proposed `/home/admin/agent-shopping-list` and private host secret files owned/readable by65532, mode0400/0600; stable session keys stay separate from the image/database. Create only the dedicated shopping volume. Enroll the verified existing account using a private server-side subject file and explicit America/New_York, USD, Walmart, Monday preferences. Do not print subject or secret values.
6. Transfer/verify/load the exact replacement amd64 image after its final-commit checks and render the prepared Compose inputs. Start one application instance on `proxy`, preserve canonical Host, enable only shopping routers on the observed public HTTPS entrypoint with `letsencrypt`. Add shopping HTTP-to-HTTPS routing deliberately if required. No direct public application port or extra proxy service.
7. Create the approved shopping DNS record pointing to the verified server, after checking the authoritative zone for collisions and the intended direct/proxy mode. Record DNS and TLS acceptance, rather than assuming the server resolver proves zone state.
8. Test live readiness/TLS/discovery, browser PKCE/session/refresh/logout, actual Codex/ChatGPT scopes/consent/resource refresh, wrong-audience denials, revocation bounds and shopping↔SSH isolation. Recheck existing SSH authentication. Run the Polar/check/no spending/confirmed purchase/history/insights/retry journey on the real app, using explicitly synthetic acceptance evidence separately from personal receipts. Verify the user's phone.
9. Keep shopping SQLite data and runtime secrets only on the server. Do not create backups, snapshots, off-server copies, recovery keys on the Mac or backup schedules. The user explicitly selected this policy on October19. The local correction removes automatic application pre-migration backup creation while preserving migration validation, transactions and durable records; its required Translation/final validation/Review and replacement image work remain before deployment.

No live write in this change set is currently authorized or executed. Stop for a concrete rollout approval after the compatibility patch and actual client callback/storage inputs are reviewable. CP03/06/10 remain incomplete until real acceptance passes.

## Discovery evidence

Read-only SSHMCP jobs: architecture `c74fc2c6167d354e27411b1873bfa980`; routing `db574706180361e86609f3aa0815140c`; public/proxy source `89d452d186b97b968030a1cd8d52d756`; client inventory `26589343166d587674f5356f3ed677d9`; OAuth mappings/config `8c4ead01784abbbb9a60695e8657c9ea`; runtime rejection/source hash `53a0e110276891804bbca023f53f618f`. Database queries used explicit read-only transactions and selected no secret or user-identifying columns. No service/account/DNS/deployment change occurred.

## Latest user policy and final local patch checks

The user explicitly rejected backups: keep everything on the server, do not back up anything. This supersedes prior recovery/retention/off-server targets. Historical synthetic backup tests remain evidence of tests already run; no production backup was created. The current working-tree correction removes the automatic pre-migration archive/field/log/error claim and retains the schema-history guards. Builder's formatter/assets/vet/all ordinary tests/both binaries, uncached SQLite and SQLite/admin race suites, actual local process acceptance, JS/shell/diff checks passed; details are in [the correction validation note](validation/CP10-no-backup-2026-10-19.md). Fresh Translation, coordinator final validation, Review and replacement images remain pending. No backup-related live action is authorized.

The OAuth patch passed coordinator post-Translation root make fmt/assets/check/build, JS/diff checks and full isolated checker: vet/all gateway+runner/races, static Linuxamd64 build, 23 baseline hashes unchanged and exact reversal. Patch `91dbd6261754f5ad1c5344ac8eb17b1680ff8bcc4a4bbaa454acee8e8a7d10c4`; checked binary `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917`. Fresh independent proxy Review returned Approved with no actionable findings. The separate application no-backup correction is the current local slice; no live patch or application deployment has occurred.

## Replacement release images — final local acceptance

Approved application commit `7970b5c`, version0.1.0-beta.2, built2026-10-19T13:25:45Z with pinned Go1.26.9. Both images passed the full synthetic production-process/browser/SDK journey, crash/retry checks and actual container stop/start with identical durable reports. amd64 ran under Docker emulation (journey1.34s); arm64 journey0.23s. Synthetic manual recovery-tool checks do not authorize production backups. All temporary containers/volumes were cleaned.

| Architecture | Local image index | Config identity | Archive SHA256 | Bytes |
| --- | --- | --- | --- | --- |
| amd64 | 9d268276169da14740fa9a05e2e5765f01eb94c1b4e346aa2129cf8c13c596dd | efcd5b19be9e3b3ceef3c470bc61c1547769bb9e79278ef74581d6413dbd13f0 | c4ddcba2bda6693a496a5000484bf0477637fc9383e07a4c29c486d67ccbd2cc |11223552|
| arm64 | 4e6dcd09a02543ac20aa42cf1de661c3ca002e384b043e32d2650361a5a561be | feb2a1c13d1da66caedeaddbac753036491c45625174688442f95d77b82c50bc | d49bfd5051bc8ee6ab9c09fe0f1fe2f5c6fc087bc2446b390c75cd19bfbf770b |10589184|

Local tags agentshoppinglist:beta2-amd64 / beta2-arm64; archives `.cache/image-audit/beta2-{amd64,arm64}.tar`. Image config labels match source/version/time. The prepared personal_prd environment now selects the tested amd64 config identity. No archive was transferred or published.

Both exact archives were scanned in network-disabled/read-only/cap-drop/no-new-privileges pinned Trivy0.76.0 containers with only cached public DB and one readonly candidate mount, no Docker socket/credentials. Both reported zero known vulnerabilities across Debian13.7 six packages and two Go binaries. DB UpdatedAt2026-10-19T00:45:30.553047111Z; scanner index2416d1f74045b57274df1a340eee6b510cacd73c66a6ccb8da092b117eb7d222. Reports `.cache/image-audit/beta2-{amd64,arm64}-report.json`, acceptance logs `.cache/acceptance/beta2-{amd64,arm64}.log`. An initial network-disabled build could not fetch uncached public modules; retry with normal build networking downloaded pinned public dependencies successfully. Candidate scanning remained offline throughout. No new source/dependency vulnerability scan is claimed; dependencies are unchanged and both final Go binaries were scanned.

Remaining: one explicit live rollout approval, independent direct SSH verification/watchdog, DNS/TLS, scoped Keycloak registrations with actual callbacks, server-only secrets/volume/owner enrollment, gateway/app rollout, actual Codex/ChatGPT and phone acceptance. No production backups or other live writes occurred. CP03/06/10 remain open.

## Authorized rollout — October19, in progress

The human explicitly authorized the complete shopping-only rollout after reviewing beta.2. Direct SSH alias personal_prd resolves to admin@144.91.70.238:30896; BatchMode/sudo succeeded independently. Connection reuse now uses a task-only local control socket. One initial later connection was transiently refused; the reused connection succeeds.

Created DNS-only A shopping.vvivid.dev→144.91.70.238 TTL300 after authoritative Cloudflare collision check; recordf1b09577e5dc816fcf189026c7034cf7. Transferred the approved image and gateway binaries to private server release directory and verified all SHA256 values. Loaded image index9d268276169da14740fa9a05e2e5765f01eb94c1b4e346aa2129cf8c13c596dd. The server Docker engine cannot address the image by configdigest; prepared environment corrected to its verified immutable index ID (same tested bytes, no rebuild).

Keycloak admin API authenticated with credentials kept solely in server-process memory. Created7 reviewed shopping scopes,2 canonical bearer-only resource registrations and3 shopping clients. Browser enabled with exact callback; Codex/ChatGPT initially disabled and no callbacks pending actual client setup verification. Removed all inherited default/optional scopes and attached only reviewed shopping scopes/audiences. Password/service/device/CIBA grants disabled. Generated server-only web/ChatGPT secrets and session keyring; sole enabled human subject selected privately, enrolled with America/New_York/USD/Walmart/Monday. No value exported.

Installed reviewed gateway SHA0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917 and atomically merged only shopping_origin. The installed old gateway hash exactly matched the locally rebuilt baseline2a22fcc08639e846bf0d49cf90c5ac3ad1a0259c8a4ac97a1e7a50bc442b038a. A30-minute systemd shopping-gateway-rollback timer is armed to remove only shopping_origin and reinstall that verified baseline if not canceled after checks. No live binary/config/database backup created. Existing issuer discovery200 and a fresh SSHMCP connect/run/disconnect passed after restart (job0c65963d3d6bd3b2723c579a04b25b25); this uses the connector's existing grant, not a new interactive authorization/refresh proof.

Created only dedicated shopping volume; started reviewed image with existingproxy network/Traefik. Public verified TLS readiness200, protected-resource metadata200, unauthenticatedMCP401. Initial certificate verification failed while issuance was pending; subsequent server and Mac requests pass without disabling TLS validation. Browser reaches existing Keycloak with API resource, webclient and S256. User asked to sign in privately in the browser/phone; no password requested in chat. Full browser code/refresh, actual Codex/ChatGPT, negative token isolation and live durable-restart acceptance remain. Rollback timer must be canceled or intentionally handled before its deadline; current app deployment is real but v1 acceptance remains incomplete.

### Live validation update

Actual server container restart passed with readiness restored and identical in-memory compared integrity/count/currency-total reports; no DB archive/copy was made. Public OAuth authorization routing: shopping MCP target200/login, SSH target400, API cross-target400, omitted resource400, SSH-client→shopping400. These are routing/denial checks, not authenticated token issuance/refresh evidence. Python's independent CA bundle failed verification; system curl with normal verification passed.

The first actual Codex add attempted the issuer-advertised default scope set (including SSH/offline) and duplicated the explicit resource with discovered resource. This request could not authorize: shopping-codex was disabled and the resource guard denies duplicates. Canceled only that task-owned process. Corrected only the local shopping configuration: discovery supplies resource once; fixed callback18444; explicit five shopping scopes and no offline/SSH. Actual Codex URL verified clientshopping-codex, single MCPresource, exact http://127.0.0.1:18444/callback and five shopping scopes. Enabled that registration with only this verified callback. Local login process remains pending human sign-in; no token copied into chat/source/logs. ChatGPT remains disabled pending actual callback.

User reported screenshot Client disabled; server diagnostics summarized onlyclient/errorcounts: that error belonged to the earlier disabled Codex attempt. shopping-web is enabled; its separate attempts returned user_not_found/invalid_user_credentials. No account/password changed. Explained using existing realm credentials and reopened the live app for human sign-in. Browser automation briefly encountered stale/blocked auth navigation; fresh root tab opened successfully and user can click Sign in. No credentials inspected or entered by agent. Gateway rollback timer remains armed while authenticated acceptance is pending.

### Callback diagnosis and authorized correction

The human reached the shopping callback, which returned Sign-in could not be verified. Automatic approval review rejected the first admin diagnostic as credential probing beyond its interpreted scope; no rejected command executed. The human then explicitly authorized existing admin credential use only in server memory, shopping mapper/example-claim inspection and shopping-only fixes. The same bounded diagnostic then executed. Example shopping-web access claims had correct issuer, soleAPIaudience, azp, Bearer,300s lifetime and shoppingread/listwrite scopes but no sub. No subject/token/password was printed or exported.

Tagged Keycloak26.8.1 SubMapper source confirms oidc-sub-mapper adds the session user's subject (https://raw.githubusercontent.com/keycloak/keycloak/26.8.1/services/src/main/java/org/keycloak/protocol/oidc/mappers/SubMapper.java). Added only direct shopping-subject mapper to shopping-web/codex/chatgpt with access.token.claim=true and introspection.token.claim=true. On-server assertions passed for all3: subject equals the single enrolled account, sole intendedAPI/MCPaudience, expected azp/Bearer, atmost300s and no unrelatedscopes. ChatGPT stays disabled. Application validation was not weakened or codechanged. Real fresh browser sign-in reopened for user; successfulcallback/session/refresh remains pending. Local fragment/docs correction assigned to a narrow Builder→Translation→Review workflow. ActualCodexlogin attempt timedout while waiting for human callback; no success claimed.
