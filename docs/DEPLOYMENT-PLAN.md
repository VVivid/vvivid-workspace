# Shopping beta deployment plan

Prepared October 19, 2026, after explicitly authorized read-only discovery on `personal_prd`. This plan has not been applied. The existing application release remains locally approved; live compatibility and daily-beta acceptance remain incomplete.

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

A credential-free source export of `go.mod`, `go.sum`, `internal/gateway` and `cmd/sshmcp-gateway` was proposed to prepare/test a patch against the actual deployed baseline. Automatic approval review rejected this production-source transfer as sensitive egress beyond discovery authorization. The export was not obtained; its lookup was also rejected and not bypassed. Both discovery connections are closed. No patch is implemented or reviewed yet. Explicit source-transfer authorization is the next prerequisite. Exclude private config, environment files, keys, credentials, databases and user records.

Once obtained, prepare the patch in an isolated local directory without modifying either live services or the existing local SSHMCP checkout. Run Builder → Translation → final checks → independent Review, plus both original SSH and new shopping resource/refresh isolation tests. Produce an exact source patch, baseline hashes, binary identity, rollout and rollback steps before asking approval to apply it. Do not weaken the existing guard or work around it with SSH privileges.

## Prepared shopping artifacts

Approved code: `05ff303f5c543e5a932e1f8b36ffc31c2e44c210`, version0.1.0-beta.1, Go1.26.9. Select the amd64 candidate for this server. Its exact image passed process/restart/recovery and offline advisory checks locally; the amd64 execution used Docker emulation on the Mac. This does not establish live server runtime behavior.

- Local image tag: `agentshoppinglist:beta-amd64`.
- Local index ID: `sha256:f5ba662e527b769409ef04a3de75a4e72d29f1a2016efd2ebad195a7b57b3d94`.
- Image config ID: `sha256:090adaf1361737077e20c384c42d72747000fa3616d83bafb3edc1d647d7ecce`.
- Local image archive: `.cache/image-audit/beta-amd64.tar`,11293696bytes, SHA256 `1f98acc0466300ab1ce564498f47111c15591a81a165e417274398006e9a3129`.
- Minimal committed build-source archive: `.cache/release/agent-shopping-list-05ff303-source.tar.gz`,137771bytes, SHA256 `7656a1c75feb098058ad5ccfd6df0729071cccb90d66435c48cd7695d583bf21`. It includes only Docker/build inputs and application code/assets from that commit.

Neither archive has been transferred or published. Choose an authenticated artifact-transfer method during rollout planning; verify archive SHA and loaded image identity before execution. Do not create a public download endpoint or publish a registry image without authorization.

`deploy/personal-prd.environment.prepared` records observed routing inputs and proposed shopping-only names/paths. Those proposed directories, volume and secrets do not exist as a result of this work. `deploy/compose.yaml` publishes no app port and preserves unrelated services. It still requires actual secret provisioning and an available exact image.

## Shopping-only live change set to review after the proxy patch

1. Preserve the existing gateway binary/config, nonsecret routing configuration and a consistent Keycloak database recovery point. Verify the backup/rollback path without exposing secrets. Record which existing SSH checks pass before changing login routing.
2. Apply only the independently reviewed login compatibility patch against verified deployed hashes, with immediate rollback if existing SSH login/resource/refresh checks regress. Do not change the SSH MCP tool path, audience or scope.
3. Create separate shopping web, Codex and ChatGPT clients/scopes and canonical API/MCP resource registrations through supported Keycloak administration, not direct database writes or whole-realm reimport. Web gets read/list-write only. Agent clients get explicitly consented shopping permissions. Disable password/service-account/device grants and inherited unrelated scopes/audiences; use S256 and access lifetime at most300s. Background/offline consent remains deferred.
4. Use the exact web callback `https://shopping.vvivid.dev/auth/callback`. Existing SSH registrations currently use Codex `http://127.0.0.1:18443/callback` and ChatGPT `https://chatgpt.com/connector_platform_oauth_redirect`; these are observed reference values, not proof of the callbacks for newly configured shopping clients. Verify the actual selected clients before registering exact shopping callbacks; no wildcard.
5. Provision proposed `/home/admin/agent-shopping-list` and private host secret files owned/readable by65532, mode0400/0600; stable session keys stay separate from the image/database. Create only the dedicated shopping volume. Enroll the verified existing account using a private server-side subject file and explicit America/New_York, USD, Walmart, Monday preferences. Do not print subject or secret values.
6. Transfer/verify/load the exact amd64 image and render the prepared Compose inputs. Start one application instance on `proxy`, preserve canonical Host, enable only shopping routers on the observed public HTTPS entrypoint with `letsencrypt`. Add shopping HTTP-to-HTTPS routing deliberately if required. No direct public application port or extra proxy service.
7. Create the approved shopping DNS record pointing to the verified server, after checking the authoritative zone for collisions and the intended direct/proxy mode. Record DNS and TLS acceptance, rather than assuming the server resolver proves zone state.
8. Test live readiness/TLS/discovery, browser PKCE/session/refresh/logout, actual Codex/ChatGPT scopes/consent/resource refresh, wrong-audience denials, revocation bounds and shopping↔SSH isolation. Recheck existing SSH authentication. Run the Polar/check/no spending/confirmed purchase/history/insights/retry journey on the real app, using explicitly synthetic acceptance evidence separately from personal receipts. Verify the user's phone.
9. Select encrypted off-server storage and a recovery key/kit under the user's control. Encrypt and copy a consistent shopping backup, decrypt/restore from that stored copy into isolation, compare records/totals/session recovery and record elapsed time. Daily retention/RPO24h/RTO1h remain proposed targets until configured and measured.

No live write in this change set is currently authorized or executed. Stop for a concrete rollout approval after the compatibility patch and actual client callback/storage inputs are reviewable. CP03/06/10 remain incomplete until real acceptance passes.

## Discovery evidence

Read-only SSHMCP jobs: architecture `c74fc2c6167d354e27411b1873bfa980`; routing `db574706180361e86609f3aa0815140c`; public/proxy source `89d452d186b97b968030a1cd8d52d756`; client inventory `26589343166d587674f5356f3ed677d9`; OAuth mappings/config `8c4ead01784abbbb9a60695e8657c9ea`; runtime rejection/source hash `53a0e110276891804bbca023f53f618f`. Database queries used explicit read-only transactions and selected no secret or user-identifying columns. No service/account/DNS/deployment change occurred.
