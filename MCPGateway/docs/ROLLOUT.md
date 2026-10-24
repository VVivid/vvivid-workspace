# Standalone acceptance and later rollout

The standalone MVP runs with synthetic callers and the included harmless sample
application. Accept it first with `make check build integration demo`, including
a copy containing only this module. No real service integration, account changes,
provider administration, live grants or deployment are part of that scenario.
Integrating an actual service requires separate approval after MVP acceptance.

For a later approved rollout:

1. Select the canonical HTTPS origin and exact per-route resources. Register
   clients, callbacks, S256 PKCE and consent at the selected issuer. Verify
   issued token profiles, sole resource audiences, client identities, scopes
   and positive issuance/expiry times. Set each route's maximum external token
   lifetime and issue provider tokens within it. Test both issued and remaining
   lifetime enforcement and expiry-based revocation.
2. Review the service's independent implementation of [identity contract
   v1](IDENTITY.md), account mapping, allowed clients, domain permissions and
   ownership checks. Preserve original issuer+subject. Matching email or subject
   text alone must never link accounts. Decide explicitly how unknown and
   disabled identities are handled before accepting domain operations.
3. Generate a gateway signing key outside source control; deliver only public
   JWKS to backends. Review rotation, emergency removal, clock synchronization
   and private-key access. Keep backend ingress private to the gateway.
4. Add one fixed stateless MCP route, its own audience and explicit permission
   allowlist per service. Configure canonical Host and streaming at the HTTPS
   reverse proxy. Verify health checks, unauthenticated route challenges and
   per-route protected-resource metadata.
5. Use the actual intended clients to test login, consent, refresh, tool
   discovery and calls with the current MCP protocol. Verify wrong issuers,
   signatures, audiences, client IDs, scopes and expired/overlong tokens fail.
   Prove one route's token cannot access another route or backend. Test at least
   two original identity pairs against the service's stored-data ownership rules.
   A second real issuer needs its own disposable acceptance setup before
   claiming provider portability.
6. Test cancellation, process restart and uncertain outcomes. The gateway
   propagates request cancellation but does not roll back work accepted by a
   service. Services with writes need their own idempotency, status and recovery
   contract; never replay an uncertain write automatically.
7. Review the exact production configuration and compatible artifacts before
   deployment. Retire any previous endpoint only after explicit acceptance,
   preserving a configuration rollback path and application data.

Local automation does not establish real-provider or actual-client acceptance.
The gateway does not administer providers, manage user accounts or implement
backend application policy.
