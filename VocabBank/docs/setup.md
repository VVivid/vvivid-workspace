# Setup and identity

Use one process/replica for the database and rotating refresh grants. This source
does not create an identity realm/client, gateway route, TLS certificate or live
hosting. Real-provider and intended-agent acceptance are separate from local
tests. Do not reuse another app's credentials, SQLite database or session keyring.

## Protected files and owner enrollment

Build with `make deps build`. Create an owner-only directory and stable keyring:

```sh
umask 077
mkdir -p .secrets data
bin/vocabbank -keygen-file .secrets/session-keys.json
cp .env.example .env
chmod 600 .env
```

Edit `.env` for the actual origins and paths. The program reads environment
variables, not dotenv automatically. Put the browser client's secret in a
regular 0400/0600 file; use a protected editor or secret manager, not a secret on
the command line. The keyring must also be a regular 0400/0600 file. These files
are limited to 64 KiB. They are ignored by Git. Key generation uses exclusive
creation and refuses to replace an existing keyring.

Prepare `.secrets/enrollment.json` with the existing provider's exact verified
issuer, subject and a display label. The shape is:

```json
{"issuer":"https://id.example.test/realms/personal","subject":"synthetic-example-subject","label":"Owner"}
```

The shown subject is an example, not an identity to use for a real install. Keep
real subjects out of source, logs and chat. Set permissions to 0600, stop any app
process using the target SQLite file, then enroll explicitly:

```sh
chmod 600 .secrets/enrollment.json
bin/vocabbank -db "$PWD/data/vocabbank.sqlite" -enroll-file "$PWD/.secrets/enrollment.json"
```

Enrollment creates an enabled owner, or updates/re-enables that exact issuer and
subject's label. It does not print the subject. Login never signs up owners.
Every request looks up the currently enabled enrolled identity; request bodies
and MCP inputs cannot select an owner. Remove the temporary enrollment input
from runtime locations after use, retaining any required recovery copy securely.

## Browser/provider contract

Configure one or two distinct HTTPS origins without paths. The first is canonical;
`VOCAB_API_AUDIENCE` must equal that origin plus `/api/v1`. A confidential browser
OIDC client needs exact callbacks `<origin>/auth/callback`, code flow with S256
PKCE, nonce/state, `openid`, `offline_access`, `vocabbank:read` and
`vocabbank:write`. The provider must rotate refresh tokens and preserve this
exact resource audience/client/scopes on code exchange and refresh. Its discovery,
authorize/token/JWKS/revoke endpoints must share the configured issuer origin.
Discovery must advertise S256; authorization response `iss` is verified when
advertised. Client credentials use HTTP Basic at the token/revocation endpoint.

Access grants must be RS256 JWTs, exact issuer, a **single** expected resource
audience, `typ=Bearer` claim, explicit `azp` client, valid iat/exp/nbf and maximum
300-second lifetime. JWT header type must be `JWT` or `at+jwt`. Unexpected scopes
are rejected; negotiated OIDC scopes openid/offline_access/profile/email are
allowed along with the two app scopes. The web client cannot act as an MCP client.

Browser sessions are opaque, host-only `__Host-vocabbank-session` cookies with
Secure, HttpOnly and SameSite Lax. Login/session/CSRF are bound to the exact
configured Host. Forwarded headers cannot choose an origin. Mutation requests
require both the exact same Origin and the session's `X-CSRF-Token`. Tokens stay
server-side in SQLite encrypted with AES-256-GCM and authenticated host/session
context. The session endpoint returns only the opaque enrolled `owner_id`, label,
granted scopes and CSRF token. The ID binds an open in-memory editor to its
original account; it is not a provider subject or an input that selects an owner.
`/auth/login?reauthenticate=1` forces provider login with the fixed `prompt=login`
parameter. The editor opens this in a new tab so its unsaved fields and retry
intent survive sign-in, and resumes saving only after a same-owner session check.
If the cookie changes again before Save, the former session's CSRF cannot
authorize a write under the new session. Draft text is never stored in browser
persistence. Keep the original tab open until saving or copying the text.
Active use extends a 30-day idle expiry. Session keys must survive restarts;
rotation may keep up to two old decrypt-only keys with one active encryption key.

Refresh calls serialize per session. Temporary token/JWKS outages retain viable
grants and return retryable 503 after access expiry. A rotating response is
persisted encrypted before signature checks; pending tokens do not authorize.
Recovery verifies them at their trusted receipt time, then refreshes if expired.
`invalid_grant` or invalid identity claims require sign-in again. A lost refresh
response or a crash before persistence can require re-login. There is no change
to shared provider session policies. Sign out destroys the local session and
requests refresh-token revocation; HTTP 502 explicitly reports local logout with
unconfirmed provider revocation. A network/500 failure does not claim logout.

## Run behind HTTPS

Supply the environment from a protected shell file and run the built app:

```sh
set -a
. ./.env
set +a
bin/vocabbank -listen 127.0.0.1:8090 -db "$PWD/data/vocabbank.sqlite" -static "$PWD/web/out"
```

Put your chosen HTTPS reverse proxy in front, preserving the configured Host.
Configure the exact origins and callback routes before testing actual sign-in.
Go serves HTTP behind the proxy; Secure cookies require browser HTTPS. Loopback
HTTP URLs are accepted by configuration for test issuers/independent local
setups, but the production app has no anonymous mode or synthetic issuer.
`make browser-test` supplies isolated TLS and test identity only in a Go test
process. `next dev` alone cannot provide authenticated APIs.

The nonroot image listens on 8090, with static assets at `/static` and a default
database `/data/vocabbank.sqlite`. Use one writable persistent directory owned by
UID/GID 65532, mount protected secret files readable by that user at 0400/0600,
and pass environment/path references. Keep the root filesystem read-only and
publish to a loopback listener or private reverse proxy. A container invocation
must explicitly mount state/config; the image contains no credentials or owner.
The [app-only Helm/release contract](../deploy/gitops/README.md) prepares private Zot
publication and fixed Fleet promotion for `vocab.vvivid.dev`. Its chart uses
`/data/sqlite/vocabbank.sqlite`, with an init-only projected Secret copied to
exact 0600 files in memory and an app-only public JWKS ConfigMap. Actual shared
identity, credentials, TLS, gateway, Fleet enrollment, DNS and deployment remain
separate operator setup in the private GitOps repository.

## Storage and recovery

The SQLite file is created 0600 and its directory 0700 when the app creates it.
SQLite WAL sidecars share that private directory. Use one writer; do not start
enrollment or a second standalone instance against an active file. Take a
consistent online SQLite backup (or stop the writer before copying all state).
Keep the stable keyring and provider secret in a separate protected backup;
a database without the keyring cannot restore encrypted browser grants. A
retained directory alone is not a backup. Restore into an isolated location,
check SQLite integrity/foreign keys and exercise authenticated reads before
accepting recovery. Never overwrite a newer-schema database with an older binary.
There is no automatic downgrade or live migration from TrackApplyEasy.

## Local and real acceptance

Local checks establish domain durability, signed synthetic OIDC flows, gateway
claim verification, official SDK behavior and actual export/browser interaction.
A real install still needs explicit owner enrollment and proof of the intended
browser sign-in/refresh/logout, correct private route isolation, durable restart
and the actual external client's OAuth/consent/tool/refresh behavior. No such
live identity, gateway, DNS or deployment acceptance is claimed by this repository.
