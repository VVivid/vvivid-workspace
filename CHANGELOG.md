# Changelog

> **Anonymized example:** Personal host addresses, account paths, private chat/page references and session identifiers in this document use fictional example values. Operational narratives illustrate the procedure; they are not an inventory of a current deployment or evidence that an example command was executed. Resolve real values from private operator configuration before use. Public product endpoints and versioned software contracts retain their documented meanings.

## 1.0.0 — 2026-10-18

Initial release of the authenticated SSHMCP gateway, native execution runner,
and supporting private testing lab.

- Three MCP tools: `connect`, `run_command`, and `disconnect`.
- Configured SSH targets, pinned host keys, and OAuth authentication to the gateway.
- Independent persistent Bash connections with durable asynchronous jobs,
  incremental bounded output, idempotency, deadlines, and cancellation.
- One active job per connection and up to sixteen active jobs across the target.
- Opt-in Multipass infrastructure, integration checks, and deployment, repair,
  upgrade, rollback, and interruption exercises with recorded evidence.

Release validation covers the local Python regression suite, Go race tests,
and Go vet. The example-production connector also completed a live connect,
hostname/whoami/pwd batch, and disconnect check before release preparation.
That live check does not establish that the deployed binary matches this tag.

The live sample observed approximately five seconds per connector call, while
the three-command batch executed on the server in about 24 milliseconds.
The overhead has not been isolated or optimized. Public deployment readiness
and broad production acceptance are not established by this release; see
lab/VALIDATION.md and the example reports for the scope of recorded checks.
