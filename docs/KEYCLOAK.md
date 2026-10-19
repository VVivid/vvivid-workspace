# Existing Keycloak context

Evidence from read-only SSHMCP inspection in the design chat on October 18, 2026 Eastern. This file is a historical observation, not a claim of current runtime behavior. Re-check before changes.

## Observed deployment

- Keycloak container: sshmcp-auth-keycloak-1.
- Runtime version command returned Keycloak 26.8.1.
- Compose directory: /home/admin/sshmcp/deployment.
- Realm import file: /home/admin/sshmcp/deployment/realm.json.
- Keycloak loopback listener: 127.0.0.1:18082 → container8080.
- Existing issuer: https://ssh-mcp.vvivid.dev/realms/sshmcp.
- Existing MCP resource: https://ssh-mcp.vvivid.dev/mcp.
- Existing protected-resource metadata advertises scope sshmcp:admin.
- Existing server runs Traefik and other applications; avoid changing unrelated services.

## Evidence strength

Live Keycloak discovery returned the issuer, auth/token/JWKS endpoints and S256 capability. Read-only database queries confirmed:
- sshmcp-codex is a public client, authorization code flow enabled, PKCE S256.
- sshmcp-chatgpt is confidential, authorization code flow enabled, PKCE S256, consent required.
- Password grants and service accounts are disabled for both clients.

Deployment realm.json contained SSH resource audience mappings, Codex loopback callback and ChatGPT callback configuration. These file values were not all independently read back from the live client database. Do not treat an import file as the complete current realm state.

## OAuth proxy constraint

Inspected deployed source:
- /home/admin/sshmcp/source/internal/gateway/authproxy.go
- /home/admin/sshmcp/source/cmd/sshmcp-gateway/main.go

Source has an SSH realm endpoint prefix, matching for existing SSH client IDs, a configured resource guard, and special refresh-resource handling. This is source evidence; shopping behavior was not runtime-tested.

Shopping must explicitly support its own client-to-resource bindings and refresh/audience handling. Options are a carefully scoped extension of the existing authorization routing or an isolated shopping OAuth route to the same Keycloak instance. No separate shopping MCP gateway is required by either option.

Do not bypass the guard or give shopping tokens SSH audiences/scopes to make login work. Preserve existing SSH authentication. Configure and test resource requests, refresh, callbacks, consent, PKCE and discovery with the actual target clients.

## Proposed shopping setup

Reuse the existing instance and initially its issuer. Create separate shopping web, Codex and ChatGPT registrations, shopping scopes, and distinct API/MCP audience identifiers.

Use issuer+subject as the account key. Explicitly enroll shopping accounts. OAuth client identity and user identity are different: many users may authorize the same client. Never reuse one admin/service-account token for all users.

No client secrets, access tokens, refresh tokens, admin credentials or personally identifying subject values are stored in this handoff.

## External references

- https://www.keycloak.org/securing-apps/oidc-layers
- https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization

Pin and validate the protocol/client versions used at implementation time.

## October19 local implementation

User selected https://shopping.vvivid.dev, own-account-only initial enrollment, and Codex plus ChatGPT. Local CP03 implements sessions, separate bearer validation and operator enrollment; [AUTH.md](AUTH.md) contains configuration/fragments and live acceptance. No live shopping registrations, proxy changes or enrollment occurred. These choices/tests do not refresh October18 observations. Automatic approval review rejected the coordinator's later SSHMCP connect attempt; it was not retried or bypassed.

## October19 authorized discovery refresh

Read-only live discovery now confirms Keycloak26.8.1, the existing HTTPSissuer/discovery endpoints and canonical SSHresource. Live read-only database queries confirm the two SSHclients, S256, callbacks, audience mappers and scope assignments. No shopping clients/resources exist yet, and the realm has one enabled account (no subject/username/email selected). A live shopping-resource probe returns400invalid_target from the existing single-resource login guard. The deployed proxy source differs materially from the localSSHMCPcheckout; do not substitute that older checkout. See [DEPLOYMENT-PLAN.md](DEPLOYMENT-PLAN.md) for observed values, sourcehashes, evidence jobs and proposed shopping-only change set.

Both read-only connections closed; no live changes. Automatic approval review rejected exporting the credential-free deployedGo source for local compatibility patch preparation as sensitive egress beyond discovery scope. No export was obtained or bypass attempted. Explicit source-transfer authorization is now needed before a tested exact-baseline patch can be prepared.
