# Staged rollout and live acceptance

This PR changes source and examples, not existing production listeners, provider
registrations, data or credentials. Run the gateway first in the cloud workspace
with synthetic services; deploy only after the release artifacts and exact
production configuration are reviewed.

1. Select canonical gateway origin and route resources. Register exact clients,
   callbacks, S256 PKCE and consent in the chosen provider. Verify newly issued
   token audiences, client identities, permissions and positive issuance/expiry
   times. Set each route's `max_token_lifetime_seconds` explicitly and configure
   provider access lifetimes within that limit: at most 300 seconds for Shopping;
   the SSH example also uses 300 seconds. Verify both issued and remaining lifetime
   enforcement and expiry-based revocation. Preserve existing issuer
   and user subjects during the initial rollout.
2. Generate and deliver a gateway signing key outside the repository. Install its
   public JWKS on the two backends. Restrict backend ingress to the gateway.
3. Start the independent SSH backend on a separate loopback port for comparison,
   preserving existing target pins/keys and runner state. Configure its gateway
   audience; test original issuer+subject ownership and all three tools.
4. Stage Shopping with complete gateway trust settings. Preserve its existing
   browser identity/session settings, database and enrollment. Confirm browser
   login, API and owner isolation before directing clients to the gateway.
   Verify two original issuer+subject pairs map to separate internal owners,
   unregistered/disabled pairs fail, and bad signatures/wrong backend audiences
   fail before domain access. Gateway login must never auto-enroll a user.
5. Apply HTTPS reverse-proxy routing and canonical Host preservation. Verify
   unauthenticated route challenges and per-route protected-resource metadata.
6. Use actual Codex and ChatGPT clients to authenticate separately to both
   endpoints. Verify login, consent, refresh and tools/list/call, including the
   negotiated protocol version and required MCP metadata at each backend. Prove Shopping
   tokens cannot access SSH, SSH tokens cannot access Shopping, and foreign user
   records/jobs remain inaccessible. Repeat with a second compatible issuer in a
   disposable deployment before claiming provider portability acceptance.
7. Interrupt gateway requests/restart the gateway after accepted work. Verify
   Shopping idempotent retries and SSH lookup preserve outcomes without duplicate
   writes, and that SSH cancellation still uses independent runner control.
8. Retire old externally reachable MCP endpoints only after acceptance. Keep a
   configuration rollback path and compatible binaries; never reset application
   data or replay uncertain jobs as part of rollback.

Do not route Shopping through the SSH execution service. Shared routing and
identity live in MCPGateway; application logic and persistence stay in each
backend. Do not broaden the old Keycloak resource guard to accept arbitrary
resources to make the new endpoints work.
