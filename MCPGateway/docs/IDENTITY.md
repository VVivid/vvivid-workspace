# Gateway identity contract v1

Transport: `Authorization: Bearer <JWT>` over HTTPS or a trusted private local
network. Token type is the protected JOSE header `typ=mcp-gateway+jwt`, algorithm
RS256, with a nonempty `kid` selecting a pinned public RSA key (at least 2048 bits)
from an operator-installed JWKS. Backends reject external access tokens and plain
identity headers in gateway mode. No token-selected URLs or signing keys are used.

| Claim | Meaning |
| --- | --- |
| `iss` | Exact configured gateway public origin |
| `aud` | Exactly one configured backend audience |
| `sub` | Original verified user subject |
| `identity_issuer` | Original verified identity-provider issuer |
| `client_id` | Verified external OAuth client identity |
| `scope` | Granted scopes intersected with route's explicit forwarding allowlist |
| `iat`, `nbf`, `exp` | Issued/not-before/expiry times; maximum lifetime 60 seconds, capped at the external token expiry |
| `jti` | Random token identifier; not a write-idempotency key |

Backend ownership uses `(identity_issuer, sub)`, never gateway issuer alone,
email, a user-supplied owner field or an HTTP session ID. The token is bearer
proof and can be replayed until expiry if stolen. Use protected networking;
application idempotency, revisions and authorization still apply. No account
linking or service-account substitution occurs.

The gateway alone holds the private key. Backends hold public keys only. A
rotation is deliberate: install both public keys on all backends and restart,
then change the gateway signer and restart; retain the previous public key until
all 60-second tokens and in-flight requests expire. Emergency removal requires
updating/restarting all backends. V1 does not fetch a dynamic gateway JWKS or
provide instant revocation. Keep clocks synchronized.

`MCPGateway/trust/token.go` is the reference implementation. Backends carry small
wire-compatible implementations to remain independently buildable with their
own Docker contexts and without a monorepo `replace` dependency. Identical
conformance tests and `scripts/check-contract.py` prevent unnoticed divergence.
This avoids exposing the runner's internal Go types to the shared gateway.

Trusting the gateway delegates identity verification to it. A compromised gateway
signing key can impersonate users to configured backends; least-privilege route
policy does not protect against that key compromise. Restrict signing-key access
and route administration. Backend domain checks still limit what a given enrolled
identity may do.
