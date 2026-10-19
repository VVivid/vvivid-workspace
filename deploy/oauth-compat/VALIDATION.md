# OAuth compatibility: local validation evidence

October19,2026, local only. Initial Translation returned a confirmed root-module packaging failure to Builder. The correction preserves the gateway patch and test contents; fresh Translation, final coordinator validation and fresh independent Review remain pending. CP03/06/10 remain In progress. No deployment, private config read/export, gateway execution or production write occurred.

## Baseline and changes

The human explicitly authorized the credential-free deployed source export, then the required runner dependency. Read-only export jobs were `ebd9f2c111668ef2c0edd9f7639d7b0f` and `8078d39ba1f38c0eddc88f32bdb4129b`; connections closed. Original source archive SHA256 `5ac441596d754fc711cec2c309b8bdd22958293122f8f492590287d2a354e177`; runner archive `bfcf3ae6d21ae9abebc46f2cece58c488093c86318ce7a669e59c0a5419d016b`. The first scoped export lacked `internal/runner`; the separately approved second export closed that build dependency gap. No substitute/stub package was used.

Ignored baseline `.cache/proxy-compat/deployed` remains unchanged. Ignored working copy `.cache/proxy-compat/work` contains only the three patch modifications and added `internal/gateway/oauth_compat_test.go`; the old local SSHMCP checkout was neither edited nor used. Module files/pins and all runner code stay original. Baseline manifest lists all23 Go/module files and excludes only AppleDouble `._*` metadata.

Proposed source changes are optional canonical HTTPS `shopping_origin` plus the fixed client/resource decisions described in [README.md](README.md). Existing test changes adapt calls to the new internal helper signature without removing assertions. Added tests cover the five known clients and legacy unknown/realm-client compatibility, resource omission/emptiness/duplicates, wrong/encoded/unknown targets, API↔MCP↔SSH denial, Basic/body conflicts and identity encodings, malformed/duplicate Authorization, query/body conflicts and64KiB form limits. Existing suites retain origin, canonical path, method, JWT/ownership/SSH behavior checks. Fake upstream tests verify unchanged grants, PKCE parameters, Basic credentials, status/body/redirect decisions and exactly one upstream call. They do not exercise real Keycloak grant issuance.

Builder self-check identified a parser difference: Go returns a raw Basic username while Keycloak26.8.1 `BasicAuthHelper.RFC6749.parseHeader` form-decodes it, as used by `ClientIdAndSecretAuthenticator.authenticateClient`. The patch decodes the comparison identity once and retains the original header/secret. Regressions include encoded shopping→SSH denial, encoded SSH→shopping denial, encoded Basic/body match/conflict, double encoding remaining an unknown identity, malformed Basic and repeated headers. Exact tagged primary-source links are in README.md.

Coordinator verified Builder `/root/oauth_compat_builder`, host `turn_context` session `01a1542e-f785-7c42-83ed-78ed138f9cca`, as gpt-6.1-sol/max before edits. Full Modern Go Guidelines list for Go1.26 was read. This is Builder evidence, not an independent review verdict.

## Executed checks

Original Builder run, before Translation and the packaging correction:

```sh
GO="$PWD/.cache/go-mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go" \
  python3 deploy/oauth-compat/check.py .cache/proxy-compat/deployed \
  --output .cache/proxy-compat/builder-handoff-checked
```

The checker ran with offline module/toolchain fetching. Actual Go toolchain: `go1.26.9 darwin/arm64`; the original module directive remains Go1.26.0.

| Check | Actual result |
| --- | --- |
| All23 original source hashes before/after | Passed; original export unchanged |
| Standard patch dry-run/apply, zero fuzz; patched file hashes | Passed in scratch |
| gofmt on all four changed/new Go files | Passed |
| `go vet ./...` | Passed |
| `go test -count=1 ./...` | Passed; gateway1.024s, runner7.247s; command package has no tests |
| `go test -race -count=1 ./internal/gateway ./internal/runner` | Passed; gateway4.609s, runner31.548s |
| Linux/amd64 `CGO_ENABLED=0 go build -trimpath -buildvcs=false ./cmd/sshmcp-gateway` | Passed; actual gateway with original runner package |
| Standard reverse-patch dry-run/apply and all23 restored hashes | Passed in scratch |
| Checker changed-source/symlink/existing-output probes | Passed; fail before Go/patch, no output/overwrite; mutated test copy preserved unchanged |
| Python syntax compilation; Git tracked diff whitespace | Passed |

The isolated checker report and full binary metadata are `.cache/proxy-compat/builder-handoff-checked/report.json` and `build-info.txt`. Built file is statically linked ELF x86-64; `go version -m` records go1.26.9, `GOOS=linux`, `GOARCH=amd64`, `GOAMD64=v1`, `CGO_ENABLED=0` and original dependency versions. The binary was not executed. These are Builder artifact identities and must be refreshed after any Translation edit:

| Artifact | SHA256 |
| --- | --- |
| `compatibility.patch` | `6c8feadd6ce018a826892608df1c21c54c52719bd1851ec64154c51c5e266f0c` |
| `oauth_compat_test.go` (now packaged as `oauth_compat_test.go.txt`, unchanged contents) | `271a4897dddc68645c4b2c6e3fdca4111a15e1585621eaff62d5cafd7b61d4ec` |
| `check.py` | `daf89da7b245b4abde09f5bee8a1676ac3c4137b356fe54395b1796c18d8e3df` |
| `.cache/proxy-compat/builder-handoff-checked/sshmcp-gateway-linux-amd64` | `429ab5eaca7addd70d12ab73fc34826e42e0ac2ab9378cd963b0eeedc157fb6b` |

Failures resolved during preparation: exact deployed dependency versions were absent from the app cache, and restricted network DNS prevented downloading them. Scoped permission downloaded only pinned public modules through proxy.golang.org with Go checksum verification; no private package fetch occurred. Restricted loopback listener binding prevented the first test run; scoped local test permission resolved it. A formatter invocation initially used repository-relative paths from the isolated module directory and exited before modifying files; the corrected invocation passed. Earlier successful build hashes are superseded by the final table above after the Basic/parser and HTTPS-issuer checks.

All temporary checker copies/test listeners/synthetic worker processes closed. No application preview was started. The final local binary/report and source exports remain ignored. Git baseline `3ca32bd` on main; no commit or remote operation performed. The approved shopping application code/images remain `05ff303f5c543e5a932e1f8b36ffc31c2e44c210`.

## Remaining acceptance

Initial Translation inspected the actual applied working source and checker/instructions, refreshed the patch/manifests, then returned the root-module packaging failure to Builder. After the correction, fresh Translation, final coordinator checks and a fresh read-only independent Review are required. Local tests do not prove current deployed login/refresh, token scope/sole-audience issuance, Keycloak refresh behavior with the new registrations, selected Codex/ChatGPT callbacks/interoperability, phone acceptance, revocation, TLS/DNS or off-server recovery. Those remain explicit live acceptance work after separate rollout authorization. The current proxy still rejects shopping requests in production.

Future application requires a verified independent administration/recovery path and separately approved rollback watchdog before restarting `sshmcp-gateway.service`, since the gateway also carries SSHMCP control traffic. No remote execution or rollback automation is installed by this bundle. Actual future config merge must preserve all private fields and add only `shopping_origin`; full application/DNS/Keycloak provisioning is separate.

## Initial Translation — returned packaging failure

Coordinator verified Translation `/root/oauth_compat_translation`, host `turn_context` session `01a1543a-052d-77d2-ade4-9406cc72c1ef`, as gpt-6.1-sol/xhigh before edits. Translation independently read the applied patch, constructor/config consumers, existing and added tests, checker and rollout instructions. The full Go1.26 Modern Go Guidelines list was read before editing. Translation subsequently found that `deploy/oauth-compat/oauth_compat_test.go` was compiled as `agentshoppinglist/deploy/oauth-compat` by root `go vet ./...` and `go test ./...`, where `Config`, `guardResource` and `authorizationProxy` are undefined. Translation stopped and returned this correctness issue to Builder; the earlier no-blocker statement did not cover root-module packaging and is superseded.

Only `internal/gateway/authproxy.go` changed from the Builder source: map membership is named `hasClientBinding` rather than `registered`, since Keycloak owns actual client registration. The code distinguishes `permittedResource` from `requestedResources` and names wrong-target and required-target failures separately. A comment explains why fixed shopping IDs have empty bindings until configured. These are local names, comments and equivalent pure boolean expressions; binding values, errors, grant forwarding, validation, authorization and side-effect ordering are unchanged. The standard patch and patched manifest were regenerated; the additional test file and all23 baseline files remain byte-for-byte unchanged.

Executed after these edits: gofmt on `authproxy.go`; uncached offline `go test -count=1 ./internal/gateway` passed in0.798s; all23 baseline and four patched hashes plus bundled-test equality passed; standard zero-fuzz apply and reverse restored every baseline hash in a temporary scratch copy; `git diff --check` passed. An initial invocation used the isolated module as its working directory for repository-relative formatter/generator paths and therefore failed before those commands changed files; corrected root paths passed. The first sandboxed gateway test attempt could not bind its local fixture listener; the unchanged suite passed with scoped local listener permission. No gateway binary was executed and no production connection was opened. Local test fixture connections are separate from production SSH. Builder binary/hash identities above describe the pre-Translation build; final coordinator checker/build results are still required.

Current Translation patch SHA256 is `91dbd6261754f5ad1c5344ac8eb17b1680ff8bcc4a4bbaa454acee8e8a7d10c4`; patched-manifest SHA256 is `380196b4354d339463c8eb3725f322bbcb34d65239f26565defd1e0fe1195a02`. No post-Translation binary had been built at that stage; subsequent correction evidence is recorded below.

Entry point inspected: `cmd/sshmcp-gateway` decodes the strict private config → `NewAuthorizationProxy` validates it → `authorizationProxy` installs the Host/bounds checks and `guardResource`. Local tests construct the proxy directly with a fake upstream; they do not start the gateway command.

Normal trace exercised by local tests: a canonical token POST with Basic client `shopping-chatgpt`, grant `refresh_token` and the shopping MCP resource enters `authorizationProxy` → `guardResource`; form/duplicate checks pass; one Basic form-decoding step selects `shopping-chatgpt`; its configured MCP binding matches; the original Authorization header, secret and grant body reach the fake Keycloak peer once, and its response passes through. An omitted refresh resource follows the same path without adding a target. The separate Basic-identity test exercises encoded `shopping%2Dchatgpt` normalization and header preservation at the guard boundary.

Failure trace, inspected in code and exercised by local tests: the same encoded Basic shopping client requesting the SSH resource selects the shopping binding after decoding; `wrongResource` is true; the guard returns400 `invalid_target` with `no-store` before calling upstream. A matching shopping authorization/code request without a resource instead takes `missingResource`. Contributors can change the fixed mapping in `guardResource`, canonical origin constraints in `Config.Validate`, and regression cases in the bundled `oauth_compat_test.go.txt` (staged as `internal/gateway/oauth_compat_test.go`); Keycloak grant rules remain upstream.

## Packaging correction — Builder handoff

Coordinator verified correction Builder `/root/oauth_packaging_builder`, host `turn_context` session `01a1543e-7de4-72a0-ab2f-0e06ce43979b`, as gpt-6.1-sol/max before edits. The full Go1.26 Modern Go Guidelines list was read. This is Builder validation; fresh Translation, coordinator final validation and independent Review remain pending.

Before editing, direct cached Go1.26.9 `go vet ./deploy/oauth-compat` reproduced the packaging failure: `undefined: Config`. The unchanged test artifact is now named `oauth_compat_test.go.txt`; `check.py` copies it to `internal/gateway/oauth_compat_test.go` only in the exported module's scratch copy. The target in `patched.sha256` is unchanged. No dummy package, build tag, proxy semantic change or bound reduction is involved. Complete baseline and working-copy inventory hashes, patch/manifests and test-content equality were checked against an ignored before-edit snapshot; all are unchanged.

Executed with the direct cached Go executable and offline module fetching:

```sh
env PATH="$PWD/.cache/go-mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin:$PATH" \
  GOTOOLCHAIN=local GOWORK=off GOENV=off GOPROXY=off GOSUMDB=off \
  GOMODCACHE="$PWD/.cache/go-mod" GOCACHE="$PWD/.cache/go-build" \
  make fmt assets check build

GO="$PWD/.cache/go-mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go" \
  python3 deploy/oauth-compat/check.py .cache/proxy-compat/deployed \
  --output .cache/proxy-compat/packaging-builder-checked
```

| Check | Actual result |
| --- | --- |
| Root formatter, Tailwind assets, vet, ordinary tests, both app binaries | Passed; ordinary test results reused Go's cache |
| Scratch patch dry-run/apply and all four patched hashes | Passed with zero fuzz; unchanged test contents staged as real Go tests |
| Scratch gofmt and `go vet ./...` | Passed |
| Scratch `go test -count=1 ./...` | Passed; gateway1.004s, runner7.272s; command package has no tests |
| Scratch gateway/runner `-race -count=1` | Passed; gateway4.828s, runner31.405s |
| Actual gateway Linux/amd64, CGO0 build | Passed; static ELF x86-64, original runner and module pins |
| Reverse patch and all23 restored/baseline hashes | Passed; full deployed/work inventories unchanged |
| Node/shell syntax, Python source compilation, tracked diff whitespace | Passed |

Report and full build metadata: `.cache/proxy-compat/packaging-builder-checked/report.json` and `build-info.txt`. Gateway binary SHA256: `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917`. Patch remains `91dbd6261754f5ad1c5344ac8eb17b1680ff8bcc4a4bbaa454acee8e8a7d10c4`; patched manifest remains `380196b4354d339463c8eb3725f322bbcb34d65239f26565defd1e0fe1195a02`; test artifact remains `271a4897dddc68645c4b2c6e3fdca4111a15e1585621eaff62d5cafd7b61d4ec`; corrected checker is `45084700104cfbee7b1953af751a183f6263f7cbf48c70b33381b1aa46e99584`.

Scoped local listener permission was used for the required fixtures. The suites start/connect to synthetic loopback Keycloak/SSH peers and synthetic worker subprocesses, which closed when the tests completed. No production SSH connection, gateway binary execution, existing SSHMCP checkout edit, live write, commit or remote operation occurred. Temporary scratch was removed by the checker. Git baseline remains `3ca32bd`; approved shopping application code/images remain `05ff303`. The correction is frozen for fresh Translation, then coordinator validation and fresh Review; CP03/06/10 remain In progress.

## Fresh Translation after packaging correction

Coordinator verified Translation `/root/oauth_packaging_translation`, host `turn_context` session `01a15441-e99f-7093-8bcd-81c6771e917c`, as gpt-6.1-sol/xhigh before this pass. Translation independently read the applied source, configuration/constructor consumers, existing and added tests, checker, patch/manifests and bundle instructions after Builder froze the correction. No further code/checker/patch/test edits had a concrete readability benefit. The separate `TEST_ARTIFACT` and `TEST_FILE` names make the inert bundle and executable scratch paths explicit; the staging call is immediately followed by patched-hash verification. Prior Translation's client-binding and permitted/requested-resource names expose the relevant decisions. No correctness or unresolved readability blocker was found. This is Translation evidence, not an independent Review verdict.

Checks actually executed with the direct cached Go1.26.9 executable, `GOTOOLCHAIN=local`, `GOWORK=off`, `GOENV=off`, empty `GOFLAGS`, project caches and offline module fetching:

- Root `go list ./...` passed and lists no `agentshoppinglist/deploy/oauth-compat` package.
- All23 baseline and four patched manifest hashes passed; `oauth_compat_test.go.txt` equals the actual working gateway test byte-for-byte; the old active bundle `oauth_compat_test.go` is absent. Python source compilation of `check.py` passed without writing bytecode.
- Patch, patched-manifest, test artifact and checker hashes match the packaging Builder handoff. The actual Builder binary hash matches its report's `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917`; the binary was not executed.
- `go test -count=1 ./internal/gateway -run '^(TestClientResourceBindings|TestResourceClientCompatibilityBoundary|TestShoppingRejectsAmbiguousParameters|TestBasicClientIdentityMatchesKeycloakDecoding|TestShoppingOriginConfiguration)$'` passed in0.612s in `.cache/proxy-compat/work`. These focused tests use request recorders without opening listeners or starting workers.
- `git diff --check` passed. This pass did not rerun root tests, the complete checker, race suites or gateway build; coordinator final validation remains required.

Normal code trace: a canonical token POST supplies Basic username `shopping%2Dchatgpt`, its original secret/header, grant `refresh_token` and resource `https://shopping.vvivid.dev/mcp`. `authorizationProxy` validates Host and bounds the request; `guardResource` parses/recreates the form, rejects ambiguous parameters, decodes the Basic comparison identity once, and selects the fixed `shopping-chatgpt` MCP binding. Neither resource-failure condition holds, so the original credentials and grant reach the next handler. Focused Basic/resource tests execute the guard path, assert one call and preserve the original Authorization header. They do not execute a real Keycloak exchange in this pass. A contributor can locate the mapping and resource decisions in `guardResource`, origin validation in `Config.Validate`, and corresponding cases in the text test artifact.

Failure code trace: the same encoded Basic shopping client requests `https://ssh-mcp.vvivid.dev/mcp`. Identity decoding still selects the shopping binding, `wrongResource` is true, and `authorizationError` returns400 `invalid_target` with `Cache-Control: no-store` before `next`. Focused tests exercise400/no-call behavior; the error code/header follow direct code inspection. Separately, `check.py` verifies baseline hashes and patch scope before patching scratch, stages the inert text test at its real gateway path, and verifies its hash before formatting/vet/tests. The root package inventory now excludes this artifact while the scratch gateway suite includes it normally.

Only this evidence and the checkpoint/resume handoff were updated. Source exports, working Go source, checker, patch/manifests and test contents remain unchanged. No production connection, gateway binary execution, existing SSHMCP checkout edit, live write, commit or remote operation occurred. Coordinator final validation and a fresh independent Review are next; all live acceptance and rollout authorization boundaries remain unchanged.

## Fresh independent review — Approved

Reviewer /root/oauth_compat_review, hostturn_context session01a15446-d097-7962-add4-fe8be9c95caa, verifiedgpt-6.1-sol/max. ReturnedApproved withnoactionablefindings. Independently ranvet,uncachedgateway/runner/races, staticLinuxamd64build, exactreversal; verified23baselinehashes/patchedhashes/inerttestpackaging/failclosedcheckerprobes andbinarySHA0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917. Report.cache/proxy-compat/reviewer-checked/report.json. Coordinator postTranslation rootmakefmtassetscheckbuild/JS/diff andfullcheckerpassedbeforeReview. No productionconnection/binaryexecution/edits byReviewer. Approvalcoverslocalproxybundleonly; appno-backupcorrection andliveissuer/client/rolloutacceptanceremainpending.

CorrectionBuilder /root/oauth_packaging_builder session01a1543e-7de4-72a0-ab2f-0e06ce43979b verifiedgpt-6.1-sol/max. FreshTranslation /root/oauth_packaging_translation01a15441-e99f-7093-8bcd-81c6771e917c verifiedgpt-6.1-sol/xhigh; nocodechangeafterpackagingfix. EarlierBuilder /root/oauth_compat_builder01a1542e-f785-7c42-83ed-78ed138f9cca gpt-6.1-sol/max andinitialTranslation /root/oauth_compat_translation01a1543a-052d-77d2-ade4-9406cc72c1ef gpt-6.1-sol/xhigh runtimeverified bycoordinator; initialTranslationcorrectlyreturnedpackagingissuetoBuilder.
