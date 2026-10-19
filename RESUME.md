# Resume AgentShoppingList

Updated October 19, 2026, America/New_York.

## Current state

Project: /Users/oudavid/works/opensource/AgentShoppingList

**CP00–CP02 complete after Translation/final checks/Review Approved. CP03 local identity workflow is independently Approved; live acceptance remains. V1 is not beta-ready.**

Go1.26.5 serves the embedded four-tab shell. SQLite startup applies four checksum migrations, enables private WAL persistence and closes after HTTP shutdown. Exact dependency pins are in go.mod and docs/DEPENDENCIES.md.

Tenant-safe initial records, composite FKs, audit/idempotency and immutable purchase snapshots remain intact. CP03 adds a complete optional Keycloak bundle, code+PKCE/state/nonce, one-use encrypted login attempts, AES-GCM persistent sessions/external keys, secure host cookies, CSRF/exact Origin, refresh/logout/revocation, and strict API/MCP access JWT verification. Shared verified Principal resolves only active enrolled owners. Operator shopping-admin enroll/disable requires explicit subject/preferences; login never enrolls. No seeded users, runtime identity bypass or shopping API/MCP tools exist. Domain workflows remain later checkpoints. Readiness stays503.

Git remains main, no commits/remote. Reference assets preserved. No live Keycloak, OAuth proxy, Traefik or deployment changes.

## Read in order

1. AGENTS.md and this file.
2. CHECKPOINTS.md for acceptance/stage evidence.
3. REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md.
4. docs/DECISIONS.md and docs/DEPENDENCIES.md.
5. docs/AUTH.md, README.md and docs/validation/CP03-2026-10-19.md for setup/results.

## Next action

Continue preparatory CP04–CP06 product/list services, mobile UI/API and built-in MCP over the verified Principal. CP03 local code completed Builder, Translation, final checks/build and independent Review Approved; keep CP03 In progress until actual Keycloak/phone/client acceptance passes. Preserve all work.

## Actual validation

- Modern Go Guidelines read for Go1.26.5 before Go edits.
- Project-cache make fmt/assets/check/build passed formatting/CSS/vet/full tests and both binaries; identity/store/admin race tests passed. New exact go-oidc3.21.0/go-jose4.1.4/OAuth2v0.37.0 pins; sqlite/libc retained.
- Real loopback issuer fixtures execute discovery/code/PKCE/state/nonce/exchange/JWKS, two-user enrollment/isolation, invalid claims/signatures/scopes, CSRF/origin/Host, durable store+Manager reopen, refresh races/failures/target/subject, logout, operator disable and signing/session-key rotation. Existing persistence regressions pass.
- Built binary twice8091/same temporary empty DB: schema4/WAL/0600/zero accounts, shell/live200, ready503, unconfigured auth/API/MCP404, SIGTERM0 both. Partial identity config exit1 before listening. Processes/tempDB cleaned.
- Sandbox DNS/loopback failures resolved by scoped escalation; no unresolved Builder failures. Actual browser/phone/Keycloak/Codex/ChatGPT auth and CP09 process acceptance remain unexecuted.
- Coordinator verified old8090 preview stopped: no listener/connection refused. Session37805 expired; final exit code unavailable. No Builder-owned process remains.

Use GOMODCACHE=$PWD/.cache/go-mod GOCACHE=$PWD/.cache/go-build for validation in this restricted workspace. See evidence note for exact commands and traces.

## Context to preserve

- User opens a ready Walmart list, recognizes products and checks them; agents manage records.
- Four compact tabs, no dashboard. Category icons on rows; product photos belong in details.
- Polar Black Cherry Seltzer 8 × 12 fl oz is a recognition example, never a fabricated purchase.
- Browser/MCP share ownership, validation, revision and idempotency services.
- Checkoff records picked-up intent; confirmed purchases produce spending/quantity metrics.
- Reuse Keycloak/Traefik with separate shopping clients/scopes/audiences; no separate MCP gateway.
- Existing OAuth proxy is SSH-specific. Client registration alone is not shopping integration proof.
- October18 Keycloak observations remain unrefreshed, not current runtime acceptance.

## Open decisions

User selected https://shopping.vvivid.dev, own-account-only initial enrollment, and Codex plus ChatGPT. Exact registrations/callbacks/subject and explicit timezone/currency/retailer/week start remain inputs, with no chat-derived defaults. Live issuer/resource/proxy compatibility, optional background consent, remote repository and recovery targets remain pending. See docs/DECISIONS.md and AUTH.md. Prepared config is not runtime proof.

## Latest coordinator handoff

CP02 remains Approved; its P3 sidecar-ignore follow-up is fixed in CP03. Local CP03 Builder checks/smoke pass; Translation/review pending. Coordinator verified Builder host turn_context session01a15270-47c7-7ac2-942c-3a7daebbf320 as gpt-6.1-sol/max. Continue the overall goal after local review.

Automatic approval review rejected the production SSHMCP connection as outside local authorization; no connection opened or bypass was attempted. Request explicit bounded live infrastructure authorization when the prepared local result is concrete. Hostname/enrollment/client choices above are answered and authorize preparation, not live infrastructure mutation. No live enrollment, secrets, clients, proxy/DNS/Traefik or deployment changes occurred.

Latest CP03 local review found no blocking issues and Approved. Final tests passed after scoped loopback retry; runtime stage IDs/settings are in CHECKPOINTS.md. No listeners remain. Next local domain/adapters can proceed while live integration is pending.
