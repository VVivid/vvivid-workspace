# AgentNewsfeed

A private reading feed managed through three built-in MCP tools. The responsive browser page shows each saved article's title, short introduction and source link, with a local placeholder or a safely proxied source-provided thumbnail. One Go service serves the browser, read API, direct Keycloak OIDC authentication and MCP; SQLite persists private ownership, articles, encrypted sessions and retry results. It has no in-app agent, scraping, metadata discovery, sharing or delete tool.

Deployed privately at https://newsfeed.vvivid.dev on October 27, 2026, with one explicitly verified owner and nine sourced introductory article cards. Live HTTPS readiness, sign-in routing, anonymous-access denial and SSH/shopping resource isolation checks passed. The website client is enabled; the ChatGPT client remains disabled until its actual callback and user consent are completed. Authenticated article rendering still awaits the owner’s manual sign-in. Six cards use source thumbnails through the approved exact hosts `arxiv.org`, `blog.postman.com`, `cdn.prod.website-files.com`, `engineering.fb.com` and `www-cdn.anthropic.com`; three retain placeholders. All five unique images passed the production proxy and JPEG decoding checks. Authenticated thumbnail rendering still needs browser acceptance. Local protocol tests are synthetic and do not substitute for that remaining browser/client acceptance.

## Build and run

Use Go **1.26.9** and Make. Dependencies are pinned to the shopping project's versions, in a separate module. The UI uses embedded `html/template` and handwritten responsive CSS; it requires no JavaScript or Node asset build. Identity/session and SQLite migration infrastructure is adapted into this independent project from AgentShoppingList. It imports no shopping package and changes no shopping or gateway runtime.

```sh
cd AgentNewsfeed
make check race build
./bin/newsfeed
```

The default listener is `127.0.0.1:8091`. With identity entirely absent the app serves a 503 setup shell, `/health/live` returns 200, `/health/ready` returns 503, and private/auth/MCP paths return 503. No account or article is seeded. A partial identity bundle or failed discovery prevents startup. `newsfeed version` reports the build version; `make build VERSION=...` sets it. SIGINT/SIGTERM allows ten seconds for graceful shutdown.

`newsfeed healthcheck live` and `newsfeed healthcheck ready` exit zero only for the matching HTTP 200 response. These commands contact only loopback at the configured listener port, use a two-second deadline, ignore ambient proxies, refuse redirects and never load identity credentials or open SQLite. Bind to loopback or all interfaces when using the built-in probe. Readiness means configured identity plus working persistence/current migrations; an unconfigured but live process intentionally fails the readiness probe.

| Setting | Value |
| --- | --- |
| `NEWSFEED_HTTP_ADDR` | Default `127.0.0.1:8091`; IP, `localhost`, or empty host plus port 1–65535 |
| `NEWSFEED_DB_PATH` | Default `data/newsfeed.sqlite`; private persistent file, never memory-only |
| `NEWSFEED_THUMBNAIL_HOSTS` | Empty by default. Optional comma-separated list of at most 16 exact lowercase DNS names, e.g. `images.example.com`; no wildcards, suffix matching or ports |

The process reads environment variables directly, never `.env` files. The database uses private file permissions, WAL, per-connection foreign keys, one immediate-transaction writer, a bounded read pool and two-second busy handling. Versioned migrations reject changed/newer/gapped history and unidentified databases. Readiness checks configured identity plus available persistence and complete migration history; it does not contact Keycloak on every probe. Liveness means the HTTP server is responding. Keep the database, WAL and session keys persistent across restarts and outside source control.

## Local container packaging

[Dockerfile](Dockerfile) uses the same pinned Go 1.26.9 build image and distroless Debian 13 runtime digests as the shopping project. Both Go binaries and embedded UI are built from the module; no host binary, data, development record, secret or tool cache is in the allowlisted build context. The image runs as UID/GID **65532**, with `/data` owned by that user and mode **0700**. Its built-in Docker healthcheck runs `/newsfeed healthcheck ready`. No shell or curl is required in the runtime.

```sh
make compose-check container-build container-test
docker image inspect --format '{{.Id}}' agentnewsfeed:local
```

`container-build` downloads only the pinned base images and declared Go modules, verifies module checksums, and writes the resulting local image ID under the ignored task directory. `TEST_IMAGE` defaults to `agentnewsfeed:local`; tests resolve it to its immutable local image ID before running. Nothing is pushed. A local image ID is content-addressed evidence about that image, not a registry publication digest.

Standard `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` build arguments are forwarded without embedding their values in the image. In an environment requiring a private build-network CA, `make container-build BUILD_CA_FILE=/path/to/public-ca-bundle.pem` mounts that public certificate bundle through BuildKit only for verified module downloads. It never copies the bundle into the runtime image or disables TLS/Go checksum verification. No CA override is needed on an ordinary trusted build network.

`container-test` uses synthetic enrollment/article/retry fixtures in a throwaway dedicated volume. It runs the real production executable as UID65532 with a read-only root, no capabilities, no privilege escalation, no published ports and `--network none`; checks fail-closed unconfigured health/private routes, proves writes outside `/data` fail, and verifies SQLite/article/retry persistence across process restarts and actual container stop/start. Docker's readiness healthcheck must mark the deliberately unconfigured container unhealthy. All test containers and their volume are removed; the image and ignored test evidence remain. This test does not configure or claim a live identity integration.

[deploy/compose.yaml](deploy/compose.yaml) is prepared for the existing HTTPS Traefik network. It publishes **no backend host port**, runs one instance with a read-only root, drops capabilities, disallows privilege escalation, and mounts a dedicated named SQLite volume. Its required variables have no production defaults; [deploy/environment.example](deploy/environment.example) contains only placeholders. `compose-check` resolves a synthetic configuration and rejects missing required inputs without loading an operator `.env` or reading secret contents. CI performs the same local packaging checks.

For separately authorized activation, select a verified release image **with registry digest**, dedicated volume name, exact origin/issuer/client/audiences, exact existing Traefik network/HTTPS entrypoint/certificate resolver, and two private host file paths. Local Compose bind-mounts file secrets using the source files' ownership/modes; it does not remap their UID or fix permissions. Prepare secret files owned/readable by UID65532 with exactly **0400 or 0600** before use, keep them outside this checkout and the SQLite volume, and mount them read-only through Compose secrets. A fresh named volume receives the image's private `/data` ownership; an existing volume must already have the correct UID and mode. Do not start multiple SQLite-owning replicas or share the volume with shopping.

The operator can validate their private environment file with `docker compose --env-file "$newsfeed_private_environment_file" -f deploy/compose.yaml config --quiet`. This is configuration validation, not deployment. No `up`, route creation, real client/secret/grant provisioning or production volume operation is part of these local packaging commands.

## Exact identity configuration

All eight settings below are required together. The origin is an operator-selected HTTPS origin with no path or trailing slash. The examples are placeholders; they are not a selected live hostname or realm. Secret values belong in private runtime files outside this source tree and must never be printed in logs or chat.

| Setting | Required contract |
| --- | --- |
| `NEWSFEED_ORIGIN` | Exact HTTPS origin, e.g. `https://newsfeed.example.com` |
| `NEWSFEED_OIDC_ISSUER` | Exact verified Keycloak issuer, e.g. `https://identity.example.com/realms/personal` |
| `NEWSFEED_WEB_CLIENT_ID` | Dedicated confidential browser client, e.g. `newsfeed-web` |
| `NEWSFEED_WEB_CLIENT_SECRET_FILE` | Private regular file containing that client's generated secret |
| `NEWSFEED_API_AUDIENCE` | Exactly `$NEWSFEED_ORIGIN/api/v1` |
| `NEWSFEED_MCP_AUDIENCE` | Exactly `$NEWSFEED_ORIGIN/mcp` |
| `NEWSFEED_MCP_CLIENT_IDS` | Exact allowlist of dedicated registered MCP clients, initially `newsfeed-chatgpt` |
| `NEWSFEED_SESSION_KEYS_FILE` | Private JSON keyring containing an active key and one to three named AES-256 keys |

Secret files must be regular, at most 16 KiB and mode 0400 or 0600. The session keyring shape is `{"active":"k1","keys":{"k1":"BASE64_OF_32_RANDOM_BYTES"}}`; this placeholder is not a key. Generate real random keys privately through the operator's secret workflow and keep them stable across ordinary restarts. Rotate by adding a new active key while retaining old decrypt-only keys for at least 12 hours 10 minutes, then restart; refreshed sessions reseal under the active key. Removing an old key early signs out its sessions. No new key/plaintext fallback exists.

Configure Keycloak directly, without importing or replacing an existing realm:

1. Register the browser client as confidential, standard authorization-code flow only, PKCE **S256**, exact redirect `$NEWSFEED_ORIGIN/auth/callback`, exact post-logout redirect `$NEWSFEED_ORIGIN/`, exact web origin, and `client_secret_basic` token authentication. Disable implicit, password, service-account, wildcard redirect and dynamic-registration paths. Register each MCP client's exact callback verified from the actual client; do not guess it or copy the shopping callback blindly.
2. Define `newsfeed:read` and `newsfeed:articles:write`. The browser requests only `openid newsfeed:read`, and receives only the API audience. MCP grants receive only their selected newsfeed permissions and the MCP audience. Remove unrelated inherited audiences/scopes. Offline/background grants are not supported. Standard `openid`, `profile`, `email` identity scopes may accompany tokens; no unrelated permission scopes may accompany a newsfeed token.
3. Use an exact resource audience mapper per client and emit `sub` from the authenticated user's subject (`oidc-sub-mapper`, `access.token.claim=true`). Access tokens require RS256, JOSE type `JWT` or `at+jwt`, body `typ=Bearer`, exact `iss`, nonempty `sub`, a **single** intended `aud`, registered `azp`, `iat`, `exp`, valid `nbf` when present, and lifetime at most **300 seconds**. Validate newly issued tokens after mapper changes. ID tokens never authorize MCP or the read API.
4. Verify discovery advertises S256 and authorization, token, JWKS, revocation and end-session endpoints on the exact issuer origin. The app requests the API `resource` during browser authorization, exchange and refresh. Ensure the issuer/proxy accepts this new resource independently without changing shopping/SSH enforcement.
5. Explicitly enroll the permitted issuer+subject through the offline operator command below. Signing in never creates an account. Initially enroll only the intended owner; other acceptance users belong in synthetic tests or separately authorized accounts.

```sh
./bin/newsfeed-admin enroll --db "$NEWSFEED_DB_PATH" \
  --issuer "$NEWSFEED_OIDC_ISSUER" --subject-file "$newsfeed_verified_subject_file"

./bin/newsfeed-admin disable --db "$NEWSFEED_DB_PATH" \
  --issuer "$NEWSFEED_OIDC_ISSUER" --subject-file "$newsfeed_verified_subject_file"
```

The subject file must be private, at most 256 bytes, containing the exact verified subject with at most one terminal newline. Enrollment prints only an opaque local account ID and rejects duplicates. Disable atomically blocks future owner resolution and revokes all that account's local sessions, preserving articles. No HTTP/MCP route can enroll, disable or choose an account.

After independently verifying the intended owner's issuer/subject and enrolling it, the offline operator can seed a vetted manifest without waiting for an MCP consent flow:

```sh
./bin/newsfeed-admin seed --db "$NEWSFEED_DB_PATH" \
  --issuer "$NEWSFEED_OIDC_ISSUER" --subject-file "$newsfeed_verified_subject_file" \
  --articles-file "$newsfeed_vetted_seed_manifest"
```

The manifest is one JSON array of **1–50** `add_article` inputs, at most **1 MiB**, with a distinct, stable `idempotency_key` for every request and `article: {url,title,summary,thumbnail_url?}`. Unknown fields, trailing documents and invalid content are rejected. Keep the actual vetted manifest outside source/image; it is user data, never an embedded fixture. The initial seed uses text/placeholders with no image hosts. The operator command uses the same `NEWSFEED_THUMBNAIL_HOSTS` policy as the application, empty by default.

Seed resolves an **existing active** exact issuer+subject enrollment and never auto-enrolls or constructs an HTTP principal. All content/key/thumbnail validation completes before the first write, using the feed's shared pure normalization. Writes use the existing owner-checked canonical deduplication and atomic article/retry-result transactions; there are no deletes, replacements or unchecked SQL. Each request commits separately. A later storage/key conflict can leave earlier requests committed; the error reports how many completed, and retrying the **unchanged** manifest safely replays them. A changed intent needs a new key, and an existing article's introduction remains unchanged by deduplicated adds. The success message counts processed requests, not newly inserted rows. No browser or MCP capability is added by this operator command.

The browser uses authorization code + PKCE, nonce, state, optional issuer response binding, verified ID/access subject equality, and hashed host-only Secure/HttpOnly/SameSite=Lax cookies. AES-GCM session ciphertext is bound to the cookie hash, owner, client and purpose. Browser sessions persist encrypted OAuth/CSRF state, refresh at least every 60 seconds or near access expiry, and expire absolutely after 12 hours. Refresh failure revokes the local session. Logout requires POST, exact Origin and CSRF, revokes locally first, then revokes the issuer refresh grant. If provider revocation fails it returns 502 while remaining signed out locally. MCP checks active local enrollment on every request; provider revocation of existing JWTs is bounded by their accepted 300-second lifetime, without introspection or back-channel logout.

Run behind HTTPS termination preserving the canonical Host. The application checks Host and any supplied Origin against its configured origin; forwarded identity/host headers cannot authenticate or override them. Proxy request limits and timeouts must permit MCP Streamable HTTP, preserving Authorization/Accept/MCP protocol headers. Browser cookies remain Secure even with the HTTP backend listener. Loopback HTTP issuers are accepted only for local protocol tests; public HTTP issuers are rejected.

The existing shared Keycloak login proxy needs the separately reviewed [newsfeed compatibility bundle](deploy/oauth-compat/README.md) before it can accept the new resources. It adds only an optional `newsfeed_origin` and fixed `newsfeed-web`/`newsfeed-chatgpt` resource bindings; no Codex binding or gateway for newsfeed tools. The deployed Keycloak resource-indicator behavior also requires canonical bearer-only URI resource registrations for the API and MCP, in addition to those interactive clients and their correct scopes/audiences. Actual ChatGPT callback/consent remains pending the new setup. Shared-proxy application/restart has its own action-time approval and independent recovery/rollback boundary; preparing the bundle performs no live change.

## Routes and MCP contracts

| Route | Access |
| --- | --- |
| `/` | Sign-in gate or the authenticated account's responsive feed; newest-saved first, cursor pagination |
| `/api/v1/articles?limit=20&cursor=...` | Browser session + `newsfeed:read`, read only; limit 1–50 |
| `/thumbnails/{article_id}` | Browser session + read permission and owned article; no URL fetch argument |
| `/auth/login`, `/auth/callback`, `/auth/logout` | Direct OIDC flow; logout is a CSRF-protected POST |
| `/mcp` | Stateless official Go SDK Streamable HTTP, bearer authentication on every request |
| `/.well-known/oauth-protected-resource/mcp` and `.../api/v1` | Exact resource, issuer and permitted scopes |
| `/health/live`, `/health/ready` | Separate liveness/readiness |

Private/auth responses use `Cache-Control: no-store`, restrictive CSP and no-referrer policy. Templates escape article text, external links use `noopener noreferrer`, and no third-party script, font or image loads in the browser. Introductory text is plain data, never HTML or executable instructions.

Exactly three MCP tools exist:

| Tool | Input and behavior |
| --- | --- |
| `add_article` | `idempotency_key`, `article: {url,title,summary,thumbnail_url?}`. Requires `newsfeed:articles:write`. Returns `{article,created}`; duplicate canonical URLs return the existing article unchanged with `created=false`. |
| `list_articles` | Optional `limit` 1–50 and opaque `cursor`; requires `newsfeed:read`. Returns `{articles,next_cursor?}` ordered by saved time then opaque ID. Write permission does not imply read permission. |
| `update_article` | `idempotency_key`, `article_id`, `expected_revision`, and full replacement `article`; requires write permission. Revision or canonical URL collisions conflict without partial changes. Omit `thumbnail_url` to clear it. |

Titles are 1–300 UTF-8 bytes; summaries are 1–1200 bytes of short source-backed introduction. Both reject control characters and surrounding whitespace. Keys are 1–128 bytes. Successful writes persist the exact result atomically with the article; retries with the same owner/key/operation/normalized content return that original result even after later edits or restart. Reusing a key for another intent conflicts. Failed validation/revision/collision writes reserve no key. Retry records have no automatic expiry. There is no deletion or full-text crawling.

URLs must be bounded HTTPS links with a public DNS hostname, no credentials, nondefault port, dot path segments or whitespace. Canonicalization lowercases DNS names, removes default port 443 and fragments, normalizes unreserved escapes, sorts query keys, and removes `utm_*`, `fbclid`, `gclid`, `dclid`, `msclkid`, `mc_cid`, `mc_eid`. Meaningful query values, repeated-value order, encoded slashes, trailing slashes and path case remain significant. `www` and non-`www` hosts remain distinct; no redirect/page probing infers equivalence. Source links are validated syntactically and never fetched by the server.

## Thumbnail policy

Omit `thumbnail_url` for the local placeholder. A supplied URL must be source-provided and match the operator's exact `NEWSFEED_THUMBNAIL_HOSTS` allowlist; otherwise the write is rejected. No Open Graph, favicon, screenshot, article metadata or image discovery runs. Select only intended publisher/CDN hosts after review. Default configuration allows no remote thumbnail at all.

Only an authenticated owned-article request can fetch a persisted thumbnail. The proxy sends no browser cookies, Authorization, proxy credentials or environment proxy settings. Every initial/redirect destination must use HTTPS on implicit port 443 and an exact permitted host. Each connection resolves DNS, rejects the whole answer set if any address is private, loopback, link-local, multicast, reserved, mapped IPv6, tunnel/translation, documentation or known metadata/platform space, then dials a pinned public IP while preserving TLS hostname verification. Redirects are limited to two and revalidated on every hop. No request query selects the fetched URL.

Fetching has a five-second total deadline, two-second connection/TLS/header limits, 16 KiB headers, four concurrent fetches and at most 2 MiB body. Only JPEG/PNG raster images are decoded; SVG, compressed HTTP bodies, HTML, malformed images, tiny images under 64 pixels on either axis, dimensions over 2048 or more than 1,048,576 pixels are rejected. The output is re-encoded as JPEG to strip metadata and bounded again. Rejected/unavailable images leave the local placeholder. A proxy conceals the browser's IP/cookies from the image host; an allowed host can still observe server-side requests. Local tests use controlled image servers; production image checks are separate from those tests.

## Reading order and validation

Start with [feed service](internal/feed/service.go), [private persistence](internal/store/sqlite/articles.go), [identity](internal/identity/identity.go), [HTTP composition](internal/web/handler.go), [MCP adapter](internal/mcp/handler.go), and [thumbnail proxy](internal/thumbnail/proxy.go). Root `make newsfeed-check newsfeed-test newsfeed-build` delegates here; [CI](../.github/workflows/newsfeed.yml) runs check, race tests, build and synthetic local packaging acceptance.

`make check race build` formats/checks Go, runs vet, regular/race tests and builds both binaries. Tests cover two-owner isolation, exact enrollment, disabled accounts, canonicalization, concurrent deduplication, replay after restart/later edits, revision collisions, failed-key rollback, pagination, actual SDK initialize/list/call operations, browser PKCE/session/refresh/logout, negative JWT claims/signatures, SSRF/DNS pinning, raster bounds/metadata removal, escaping, and fail-closed health behavior. These local tests do not establish live Keycloak client registrations, production secrets, HTTPS proxy compatibility or physical-device layout acceptance.
