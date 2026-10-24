# Local agent demo

Run from the module directory:

```sh
make demo
```

Requirements: Go matching `go.mod` (1.26.9 or compatible newer), Make, writable
temporary storage and permission to bind local loopback ports. First builds
may download the pinned Go dependencies. No credentials or account setup are
required. The command exits nonzero if any expected result or denial fails.

The local demo starts:

- The built production `bin/mcp-gateway` executable with generated configuration.
- A development-only mock issuer exposing OIDC discovery and public signing keys.
- Two HTTP copies of the same sample backend used by `bin/example-mcp`, each with
  its own audience and scope. Service A returns JSON; service B returns SSE.
- An agent-style official MCP SDK client for synthetic Alice and Bob.

For each route and identity the client initializes protocol `2026-07-28`,
discovers exactly `add`, `echo` and `whoami`, calls `echo` with `hello`, adds 2
and 3, and checks the verified issuer, subject, client and attenuated scope.
Spoofed client identity headers cannot change the caller. Denial checks cover
missing, forged, expired and overlong external tokens, wrong audiences, wrong
clients, missing permissions and external/internal token substitution. Denied
gateway requests must never reach a backend. A signed service-A backend identity
must fail at service B.

Expected ending:

```text
service-a: protocol 2026-07-28; discovered add, echo, whoami; echo(hello)=hello; add(2,3)=5
service-b: protocol 2026-07-28; discovered add, echo, whoami; echo(hello)=hello; add(2,3)=5
Alice and Bob: original issuer/subject and client preserved; forwarded scopes attenuated
Denied: missing/forged/expired/long-lived tokens, wrong client/scope/audience, and token substitution
PASS: local agent scenario completed; temporary keys, listeners and gateway process cleaned up
```

All listeners use ephemeral `127.0.0.1` ports. Private keys use mode 0600 in a
mode-0700 temporary directory; issuer signing material stays in memory. Tokens
and keys are never printed. The scenario has a 30-second budget. It handles
SIGINT/SIGTERM and cleans up its child process, servers and temporary directory,
with bounded graceful shutdown and forced child termination if needed. As with
any process, SIGKILL or host failure can prevent cleanup; temporary demo key
files are not production credentials.

The mock issuer has no login, registration, refresh or token API. It exists
only inside `cmd/local-demo` and tagged integration tests; it is absent from the
production gateway binary and container. The gateway still performs its normal
issuer discovery, signature, audience, profile, client, scope and lifetime
checks. Production use requires a real configured issuer and separately
managed signing material. There is no production authentication bypass.

The implementation starts at `cmd/local-demo/main.go`, the scenario is in
`internal/localdemo/demo.go`, and the sample tools/authentication are in
`internal/examplemcp/backend.go`. `make integration` reruns the same scenario
under Go's race detector. The core gateway tests independently cover JSON/SSE,
current protocol headers, cancellation, no replay, limits, trailers and
informational-response policy.

This proves local routing and the documented identity contract with synthetic
callers. The sample has no persistent records, so `whoami` proves distinct
identity propagation, not ownership of stored application data. Actual provider
OAuth, refresh, consent, client UI use and real-service account mapping remain
separate integration work after standalone acceptance and approval.
