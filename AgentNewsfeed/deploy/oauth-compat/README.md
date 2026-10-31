# Newsfeed resource compatibility for the existing login proxy

This is an **offline** patch/checker bundle for the exact currently effective shopping-compatible gateway. It adds one optional `newsfeed_origin` property and two fixed client/resource bindings. Newsfeed tools run directly in AgentNewsfeed; this patch only allows the existing Keycloak login proxy to route their OAuth requests. No application gateway, service restart, live config merge, client registration, secret creation or deployment is performed by this bundle.

| Client | Only permitted resource |
| --- | --- |
| `newsfeed-web` | `newsfeed_origin` + `/api/v1` |
| `newsfeed-chatgpt` | `newsfeed_origin` + `/mcp` |

With no property, both new client IDs are reserved and fail closed, including refresh without a resource. There is **no newsfeed Codex binding**. Other realm clients retain the existing SSH/omitted-resource behavior, subject to Keycloak registration and grant checks; they cannot select a newsfeed resource. Existing SSH and shopping clients retain their exact bindings and request/refresh rules.

## Exact baseline and artifacts

The original authorized 23-file credential-free source snapshot matches the existing shopping bundle's [original manifest](../../../AgentShoppingList/deploy/oauth-compat/baseline.sha256). Applying the unchanged [shopping patch](../../../AgentShoppingList/deploy/oauth-compat/compatibility.patch) **in scratch only** and staging its [inert test artifact](../../../AgentShoppingList/deploy/oauth-compat/oauth_compat_test.go.txt) reconstructs the 24-file effective source identified by this bundle's [baseline.sha256](baseline.sha256). Its Linux/amd64 binary is reproduced byte for byte as `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917`, matching the installed/active baseline identity supplied by the coordinator. The original source export remains unchanged and ignored.

The new [compatibility.patch](compatibility.patch) changes only `internal/gateway/authproxy.go` and `config.go` relative to that effective baseline. [newsfeed_compat_test.go.txt](newsfeed_compat_test.go.txt) is inert in the newsfeed module; the checker stages its unchanged contents as `internal/gateway/newsfeed_compat_test.go`. The existing shopping compatibility tests remain in the effective baseline unchanged. [patched.sha256](patched.sha256) identifies the two new patched files and new test. Full exported source and built binaries remain ignored; only this minimal bundle is maintained.

To reconstruct the effective checker input, first verify the original snapshot has exactly the shopping original manifest's 23 files and hashes, then copy **only those manifest files** into a new ignored scratch directory. Apply the committed shopping patch there with `patch --batch --forward --fuzz=0 -p1`, stage its test artifact at `internal/gateway/oauth_compat_test.go`, and verify every shopping patched hash plus this bundle's complete effective-baseline manifest. Never apply either patch to the provided export or a running checkout. The checker intentionally rejects the original unpatched 23-file export.

From the repository root, with Go **1.26.9** and the exact public dependencies cached:

```sh
GO=/path/to/go1.26.9/bin/go \
GOMODCACHE=/path/to/verified/go-mod GOCACHE=/path/to/go-build \
python3 AgentNewsfeed/deploy/oauth-compat/check.py \
  "$newsfeed_effective_source_export" --output "$newsfeed_new_checked_output"
```

The input must contain exactly the manifest's 24 regular files, without symlinks or extra files. The output must be new and outside the input; caches and scratch must also be outside it. The checker validates these constraints before execution, copies only manifest files to temporary scratch, verifies modules, builds an exact current-baseline rollback binary, checks its known hash, applies the new patch with zero fuzz, verifies all changed/unchanged hashes, checks Go formatting, vets/tests every exported package, runs gateway/runner race tests, and builds a new Linux/amd64 CGO0 candidate. It then reverses the patch, removes only the new test, proves all 24 effective-baseline hashes restored, and verifies the input is still unchanged. Both binaries, full build metadata and `report.json` are saved only after all checks pass. Neither binary is executed by the checker.

`GOPROXY=off` and local toolchain selection prevent network module/toolchain downloads during checking. `GOSUMDB=sum.golang.org`, empty private/checksum bypass lists and `go mod verify` retain checksum verification. If dependencies are absent, prewarm only the exact exported module pins from a separate scratch copy through the standard Go registry/checksum service with verification enabled; never set `GOSUMDB=off`. Tests open synthetic loopback Keycloak/SSH peers and synthetic local Bash workers; they make no production connection.

## Preserved OAuth boundaries

`newsfeed_origin` must be a canonical HTTPS origin with no path/query/fragment/trailing slash, an HTTPS issuer, and a hostname separate from the configured SSH resource, issuer and shopping origin. The fixed mapping does not accept caller-chosen client maps or arbitrary resources.

Authorization GET/POST and authorization-code exchange require exactly one matching resource for a mapped client. Refresh may omit resource, preserving the original grant audience; an explicit empty or different target fails. Duplicate client/grant/resource parameters, encoded duplicate keys, Basic/body disagreement, repeated/malformed Authorization headers, token parameters in the query and noncanonical endpoint paths retain their existing rejection behavior. Basic comparison identity is form-decoded once as Keycloak expects, while original credentials are forwarded unchanged. Existing Host/forwarded-origin/upstream, request-size/concurrency/time limits and all SSH/shopping rules remain intact.

The proxy forwards grant, PKCE, scopes, callbacks, state and nonce unchanged and preserves Keycloak status/body/redirect decisions. Keycloak remains responsible for client authentication, grant ownership, PKCE, consent and signed sole-audience token issuance. Local routing tests do not prove real Keycloak grant issuance or actual ChatGPT interoperability.

Keycloak configuration is separately required for the exact named interactive clients **`newsfeed-web`** and **`newsfeed-chatgpt`**, plus two canonical **bearer-only URI resource registrations**:

- `https://newsfeed.vvivid.dev/api/v1`
- `https://newsfeed.vvivid.dev/mcp`

Those URI registrations are non-login/no-credential resources with interactive/service-account/password grants disabled; they do not create extra agent access. The deployed resource-indicator implementation needs them in addition to correct audience/scopes. The reader/archive browser source requests `openid newsfeed:read newsfeed:articles:write` for explicit manual archive/restore; existing read-only grants still read but cannot mutate. Enabling that web-client grant and signing in again is a separately authorized identity change. The proxy already forwards scopes unchanged and needs no additional patch. ChatGPT receives only explicitly selected newsfeed read/write permissions and the MCP audience. No offline or unrelated scope/audience is added. The browser callback is the exact newsfeed origin plus `/auth/callback`; the **new ChatGPT setup callback remains unresolved until supplied by the actual setup**, with no guessed/reused callback. No Codex registration or grant belongs to this initial scope.

## Proposed shared-gateway application and rollback

Applying this bundle/restarting the shared gateway requires separate action-time approval after independent Translation, final validation and Review. The reviewed action must identify both binary hashes, the exact property value and authenticated transfer channel. The only proposed private config merge is:

```json
{"newsfeed_origin":"https://newsfeed.vvivid.dev"}
```

This is a one-property merge into the existing private `/etc/sshmcp/gateway.json`, never a replacement document. Preserve every other field, SSH secret and existing `shopping_origin`; do not print, export or retain a backup of the config. The new property must equal the actual approved `NEWSFEED_ORIGIN`. No live config was read to prepare this bundle.

Before any application, establish and verify an administration/recovery path independent of this gateway and an explicitly approved bounded rollback watchdog using that path. Restarting the gateway can interrupt its own SSHMCP control channel; a call through it cannot be the recovery plan. The watchdog must remain active until the current SSH/shopping discovery/login/code/refresh/tool checks and new resource-isolation checks pass; define its deadline/cancellation criteria in the action-time approval. No watchdog or remote automation is installed by this bundle.

Stage only the hash-verified candidate and a private transient config replacement preserving current ownership/mode. Atomically install the candidate beside `/usr/local/bin/sshmcp-gateway`, then atomically merge the new property before restarting **only `sshmcp-gateway.service`** from the independent path. Keep the unit, listeners, Traefik login proxy, issuer and existing shopping registrations unchanged. Verify active binary identity, service health and existing SSH/shopping behavior first. Then verify newsfeed↔SSH/shopping and web↔MCP denial, malformed/duplicate/refresh-omission rules and separately authorized real-client PKCE/token issuance. Do not cancel recovery protection merely because the daemon is running.

On failed startup or any regression, atomically remove **only `newsfeed_origin`** from the current private config, preserving all other values without printing or backing it up. Atomically reinstall the locally rebuilt current-baseline binary with SHA256 `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917`, restart only the same service through the independent path, and verify prior SSH/shopping behavior. This baseline is rebuilt from the authorized credential-free source, not copied from a production binary. Reverse-patch restoration is proved only in scratch; no production source checkout is patched in place. Any separately created newsfeed registrations/grants have their own narrowly scoped rollback; never restore an entire realm over unrelated changes.

[VALIDATION.md](VALIDATION.md) records current local evidence and limitations. No live config/service, source checkout, shopping project or gateway project was changed by this preparation.
