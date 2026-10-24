# Gateway identity contract v1

Transport: `Authorization: Bearer <JWT>` over HTTPS or a trusted private local
network. The protected JOSE header is `typ=mcp-gateway+jwt`, with algorithm RS256
and a nonempty `kid` selecting a pinned public RSA key of at least 2048 bits from
an operator-installed JWKS. Backends reject external access tokens and plain
identity headers. No token-selected URLs or signing keys are used.

| Claim | Meaning |
| --- | --- |
| `iss` | Exact configured gateway public origin |
| `aud` | Exactly one configured backend audience |
| `sub` | Original verified user subject |
| `identity_issuer` | Original verified identity-provider issuer |
| `client_id` | Verified external OAuth client identity |
| `scope` | Granted scopes intersected with the route's explicit forwarding allowlist |
| `iat`, `nbf`, `exp` | Issued/not-before/expiry; maximum lifetime 60 seconds, capped at external token expiry |
| `jti` | Random token identifier, independent of application idempotency |

Backend ownership uses `(identity_issuer, sub)`. Email, a user-supplied owner
field, matching subject text from another issuer and HTTP session IDs do not
establish ownership. Each backend owns its internal account mapping and its
rules for enrollment, disabled accounts, permissions and data access. A valid
identity does not itself enroll an account or grant an application operation.
The sample `whoami` tool demonstrates the identity pair without storing accounts
or records.

A backend first verifies the signature, protected token type, exact gateway
issuer, sole backend audience and time validity. It then checks its own allowed
clients, granted scopes and account/ownership rules before doing application
work. `trust/token.go` is this module's reference verifier and
`trust/token_test.go` contains conformance cases. A separate service may import
this independent Go module or implement the documented wire contract in its own
language. The gateway has no dependency on such implementations.

External provider signature/resource/profile verification and token lifetime
policy belong to the gateway. Each route requires a positive
`max_token_lifetime_seconds` that bounds both issued and remaining external
lifetimes. The provider's expiry therefore bounds revocation propagation across
repeated gateway requests. Backends receive only the signed internal identity,
never the provider token. Separate application login flows remain an
application concern.

The internal token is bearer proof and may be replayed until expiry if stolen.
Use protected networking. Application authorization, idempotency and revision
checks still apply; the gateway does not replay failed tool calls automatically.

Only the gateway holds its private signing key. Backends hold public keys. To
rotate deliberately, install both public keys on all backends and restart, then
change the gateway signer and restart. Retain the old public key until all
60-second tokens and in-flight requests expire. Emergency removal requires
updating/restarting all backends. V1 does not fetch dynamic gateway JWKS or offer
instant revocation. Keep clocks synchronized.

Trusting the gateway delegates identity verification to it. A compromised
signing key can impersonate users to configured backends; route policy does not
protect against that compromise. Restrict signing-key and route administration.
Backend domain checks still constrain operations for each identity.
