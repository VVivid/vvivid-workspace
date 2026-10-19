# Shopping callback subject mapper correction

October19, 2026. Application image remains0.1.0-beta.2 from7970b5c; no application code or image changed. The human completed Keycloak authentication but the callback failed closed. Initial admin diagnostic was automatically rejected; the human explicitly authorized the narrow server-memory credential/account/claim diagnostic and shopping-only correction, after which it executed.

## Diagnosis and live correction

Example shopping-web access claims had the correct issuer, soleAPIaudience, client, Bearer type,300s lifetime and shoppingread/listwrite scopes, but no sub. No token, subject or credential was printed/exported. Keycloak26.8.1's tagged SubMapper confirms oidc-sub-mapper sets the authenticated session user's subject: https://github.com/keycloak/keycloak/blob/26.8.1/services/src/main/java/org/keycloak/protocol/oidc/mappers/SubMapper.java.

Added only direct shopping-subject mapper to shopping-web/codex/chatgpt with access.token.claim=true and introspection.token.claim=true. On-server evaluated-claim assertions passed for all3: subject equals enrolled human, sole intendedAPI/MCPaudience, expectedclient/Bearer, atmost300s and shopping-onlyscopes. ChatGPT remains disabled. No validation weakened; no password changed; no production backup. Fresh browser page reopened for the human. Actual callback/session/refresh acceptance is pending.

## Sequential local artifact workflow

Host turn_context metadata verified each runtime before release; requested settings/self-report were not proof.

| Stage | Agent | Session | Effective runtime |
| --- | --- | --- | --- |
| Builder | /root/subject_mapper_builder |01a15465-ab83-7842-8e40-fc2fb0332cda|gpt-6.1-sol/max|
| Translation | /root/subject_mapper_translation |01a15467-5557-70e1-8899-3e980ffb118d|gpt-6.1-sol/xhigh|
| Review | /root/subject_mapper_review |01a15468-6be5-78d3-9eea-d6478d252379|gpt-6.1-sol/max|

Builder updated only docs/keycloak/clients.template.json and docs/AUTH.md, retaining every other client property. Duplicate-key-aware JSON checks/exact mapper assertions/property comparison/diff checks passed. Translation passed unchanged. Normal trace: mapper supplies sub, access validation verifies it, browser compares IDtoken subject and resolves enrolled owner before creating a durable session. Failure trace: missing sub fails verification, revokes new grant and creates no session. These are code traces, not new successful live login evidence.

Coordinator post-Translation make fmt/assets/check/build, JSON parse and diff checks passed using cached Go1.26.9; ordinary tests cached. Log .cache/acceptance/subject-mapper-final.log. Fresh read-only Review returned Approved/no actionable findings; independently verified exact3mappers, unchangedotherproperties, documentation/application consistency and validation. Reviewer made no edits/live calls.

Next: human completes fresh browser sign-in; verify callback/session/refresh then actualCodex/ChatGPT/phone and remaining live acceptance. Keep gateway rollback watchdog deadline under active control.
