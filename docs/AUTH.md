# Identity preparation and operator runbook

CP03 local implementation, October 19, 2026. The user selected `https://shopping.vvivid.dev`, initial enrollment of only their own account, and Codex plus ChatGPT as the first MCP clients. No shopping client, DNS, proxy, secret, live enrollment or deployment has been applied. Exact Keycloak subjects, registered callback URLs and generated client credentials remain operator inputs.

## Application configuration

The identity bundle in `.env.example` is all-or-nothing. Leaving it entirely absent serves the public shell. A partial/invalid bundle or failed OIDC discovery prevents startup. No header, environment setting or endpoint can authenticate a fixture user. Readiness remains 503 while integration and the later beta checkpoints are incomplete.

| Setting | Required value |
| --- | --- |
| `SHOPPING_ORIGIN` | `https://shopping.vvivid.dev`, no trailing slash |
| `SHOPPING_OIDC_ISSUER` | Exact, freshly verified Keycloak issuer; historical candidate `https://ssh-mcp.vvivid.dev/realms/sshmcp` |
| `SHOPPING_WEB_CLIENT_ID` | Registered confidential shopping web client; proposed name `shopping-web` |
| `SHOPPING_WEB_CLIENT_SECRET_FILE` | Private regular file with its generated secret |
| `SHOPPING_API_AUDIENCE` | `https://shopping.vvivid.dev/api/v1` |
| `SHOPPING_MCP_AUDIENCE` | `https://shopping.vvivid.dev/mcp` |
| `SHOPPING_MCP_CLIENT_IDS` | Exact comma-separated registered shopping agent clients; proposed `shopping-codex,shopping-chatgpt` |
| `SHOPPING_SESSION_KEYS_FILE` | Private JSON keyring described below |

Files must be regular, at most 16 KiB, with no group/other permission bits (use 0400 or 0600). Mount them outside the image, source tree and database directory. The application reads them at startup; keep their values stable across restarts. Cookie and audience validation require the configured HTTPS origin; the HTTP listener normally sits behind Traefik. Traefik must preserve the canonical Host. Forwarded headers cannot override it. HTTP is allowed only for a loopback issuer in protocol tests/local issuer development; this supplies no identity bypass and browser cookies remain Secure.

The keyring JSON shape is `{"active":"k1","keys":{"k1":"BASE64_OF_32_RANDOM_BYTES"}}`; the placeholder is not a usable key. It accepts at most three named AES-256 keys. To generate a new private file without printing its key, run this in an operator-owned secure directory, outside this repository:

```sh
umask 077
shopping_session_key=$(openssl rand -base64 32)
printf '{"active":"k1","keys":{"k1":"%s"}}\n' "$shopping_session_key" > shopping-session-keys.json
unset shopping_session_key
```

Do not regenerate this file on ordinary startup. Ciphertext is bound to its purpose, credential hash, owner and web client. SQLite contains encrypted OAuth tokens and CSRF state, with only hashed cookie credentials. Browser cookies are host-only, Secure, HttpOnly, SameSite=Lax, and live at most 12 hours. Logout is a CSRF-protected POST, revokes the durable local session first, revokes its refresh grant, then redirects to the standard Keycloak end-session endpoint. A revocation failure reports 502 after local sign-out; it does not restore the local session. There is no browser local-storage cache.

## Explicit enrollment

Build `bin/shopping-admin` with `make build`. Use a private file containing the exact subject of the user's verified Keycloak account, obtained through the authorized identity administration workflow. Do not put the subject or credentials in a committed document. Enrollment requires all account preferences explicitly; the following shell variables are operator inputs, not defaults:

```sh
./bin/shopping-admin enroll \
  --db "$shopping_db_path" --issuer "$shopping_verified_issuer" \
  --subject-file "$shopping_subject_file" \
  --timezone "$shopping_timezone" --currency "$shopping_currency" \
  --retailer "$shopping_retailer" --week-start "$shopping_week_start"
```

The command creates an opaque owner and preferences atomically. It fails on duplicate enrollment; logging in never creates an account. Initially enroll only the user's own account. Two-user acceptance uses test fixtures or separately authorized test accounts. The command prints only the opaque owner ID. It requires direct access to the private SQLite file and is never called by HTTP/MCP.

To disable shopping access while preserving records:

```sh
./bin/shopping-admin disable \
  --db "$shopping_db_path" --issuer "$shopping_verified_issuer" \
  --subject-file "$shopping_subject_file"
```

Disabling revokes all local sessions atomically and prevents new identity resolution and writes, including writes with an owner handle obtained before disable. It affects shopping enrollment, not the Keycloak account or SSHMCP.

## Keycloak preparation

`docs/keycloak/clients.template.json` and `scopes.template.json` are local fragments for review. They are not a realm import and must not replace the existing SSH realm. Codex/ChatGPT redirect placeholders must be replaced with callbacks verified from the actual selected clients. Client secrets are generated and delivered outside these files. No dynamic registration or offline grant is enabled by this preparation.

The browser requests only openid, shopping:read and shopping:lists:write. Attach only those permission scopes to its registration. Agent clients receive shopping-only optional permissions and their MCP audience. Turn off inherited scope/audience mappers that add account, SSH or unrelated audiences/scopes. The application requires exactly one intended audience, the registered `azp` client, access-token body `typ=Bearer`, and RS256 signing. Standard OIDC identity scopes may accompany a grant; management scopes cannot accompany a browser grant. An ID token is accepted only during server-side OIDC login/refresh and cannot authenticate API/MCP requests.

The code includes the API resource in authorization, code exchange and refresh. Its signed access JWT must resolve to the same subject as the verified ID token. Client-to-resource enforcement at the existing Keycloak/proxy still needs live validation. Test direct discovery against the existing issuer first; if an authorization routing change is required, prepare an isolated shopping binding or bounded extension without weakening the SSH resource guard. The shopping application remains the resource server and MCP host; no gateway is introduced.

Keep access-token lifetimes at or below 300 seconds. Validate the resulting tokens after any mapper change, including unwanted default audiences. Browser refresh requires an active per-user refresh grant; no service account or password grant is supported. Background/offline access requires a later explicit consent choice, not just presence of the optional token scope in server validation.

## Revocation and rotation limits

- Each browser request checks local session revocation and active enrollment. A request at least 60 seconds after the last provider check refreshes the grant before proceeding; it refreshes sooner near expiry. Failed refresh revokes the local session. The session has an absolute 12 hour limit. Concurrent requests within this single application instance serialize a cookie's rotating refresh token outside database transactions.
- MCP/API access JWT validation is stateless at the issuer boundary and checks active local enrollment every request. Provider revocation propagates by token expiry, at most 300 seconds for accepted tokens. There is no online introspection or back-channel logout endpoint. Confirm that this bound is acceptable and achieved by the actual Keycloak client settings before release. Operator disable blocks new authenticated requests immediately.
- Unknown signing keys trigger a bounded JWKS fetch through go-oidc. Publish the previous public signing key for at least the remaining 300 second token lifetime during planned rotation. A cached old key may validate a still-current token until that token expires. Emergency response must account for this bound; do not describe rotation as instant token revocation.
- For session-key rotation, add a new random key as active and retain the previous decrypt-only key, then restart the app. Refreshes reseal state with the active key. Retain old keys for at least 12 hours 10 minutes, and longer where needed to restore retained encrypted backups. Removing a key early signs out sessions encrypted with it. There is no plaintext or fresh-key fallback.

## Live acceptance still required

After bounded infrastructure authorization: re-check discovery and existing SSH behavior, provision only reviewed shopping clients/scopes, enroll the user's subject with explicit preferences, and test HTTPS/Traefik callbacks on the user's phone. Use actual Codex and ChatGPT clients for PKCE, consent, resource/audience binding, refresh, scope escalation and revocation timing. Verify shopping→SSH and SSH→shopping denial and preservation of existing SSH authentication. Local issuer tests are evidence about application behavior, not about those deployed integrations. CP03 remains In progress until this evidence exists.

Primary references: [go-oidc v3.21.0](https://github.com/coreos/go-oidc/releases/tag/v3.21.0), [Go OAuth2 PKCE helpers](https://pkg.go.dev/golang.org/x/oauth2@v0.37.0), [Keycloak OIDC endpoints](https://www.keycloak.org/securing-apps/oidc-layers), [Keycloak client scopes and audience mappers](https://www.keycloak.org/docs/latest/server_admin/index.html#_protocol-mappers), and [MCP authorization 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).
