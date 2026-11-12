# VocabBank

A private English vocabulary bank. Save a word or expression alone, then ask a
connected agent to fill in plain English meanings, when to use it, contextual
examples, and common phrases through MCP. Your original sentence, source URL and
personal note remain separate from the illustrative examples.

The app opens to a plain word grid with search, three or four columns and
30/50/100 words per page. Click a word for the learner popup; multiple senses
have keyboard-accessible tabs. Light/dark and layout preferences are the only
items saved in localStorage. Vocabulary and session secrets are never stored
there. New installations start empty and do not open a sample popup. There is
no browser extension, built-in AI, provider API key, scraping, or seeded dictionary.

Source lives in `VocabBank/` inside
[VVivid/vvivid-workspace](https://github.com/VVivid/vvivid-workspace). Run the
commands below from that directory, or use the root `vocab-check`, `vocab-test`
and `vocab-build` Make targets. The app owns its module, dependencies and image.

## Architecture

Next.js statically exports React. One Go process serves that export, the protected
browser API, and an official Go MCP SDK streamable HTTP endpoint. SQLite stores
owner-qualified words, encrypted browser refresh sessions, login attempts and
idempotency results, using WAL, foreign keys, FULL synchronous writes and one
connection/writer. Schema version 1 is initialized transactionally; newer unknown
schemas fail startup. There is no Node production server.

The identity implementation and regression tests are adapted from
[TrackApplyEasy](https://github.com/VVivid/TrackApplyEasy/tree/cfc8c507d8010ab45e1f0c365789a25e58e1d408).
Only its identity patterns and relevant pinned dependencies are reused. This
project has its own database, module, environment variables, cookies and scopes.

A word is `needs_details` until a complete set of 1–8 senses is accepted. Each
sense requires a supported part of speech, definition, when-to-use guidance,
2–6 contextual examples and 1–10 common phrases; a usage tip is optional. These
checks enforce structure and bounds, not linguistic accuracy. Definitions and
learning content are English by contract; the app does not run language detection
or silently translate user captures. An agent must verify the quality of its own
content. The footer labels examples as illustrative learning material, not quotations.

Add/edit normalizes term whitespace and uses Go Unicode lowercase for duplicate
keys. Thus ` In   Hindsight ` conflicts with `in hindsight` for the same owner;
accents are not removed and language-specific case folding is not performed.
Duplicates return a conflict instead of merging or overwriting captures. User
fields can be edited. Changing the actual normalized term clears learning about
the previous term; changing case/spacing or notes preserves senses. V1 has no delete.

Writes require a stable idempotency key. An exact successful retry returns the
original result snapshot even if the word changed afterward; it never reapplies
that old intent. Different input under the same owner/operation/key conflicts.
Update/completion also require the current revision. A stale revision or invalid
completion changes nothing. An editor retains its input and presents an explicit
latest-version review before using newer saved fields. Agent completion cannot
replace the original sentence, source or note. The visible UI refreshes on focus
and every 12 seconds, so external MCP updates appear without a full-page reload.

If sign-in expires during Add or Edit, the dialog keeps every capture field,
revision and retry key in this tab's memory. Save stays disabled while signed
out. Choose **Sign in in a new tab**, sign in with the account that opened the
editor, and return to the original tab. It rechecks sign-in on focus; **Check
sign-in** retries explicitly. A different enrolled account cannot save that
draft. Sign in with the original account, or copy the text before cancelling.
The authenticated session exposes an opaque enrolled owner ID for this check;
provider subjects and tokens remain server-side. Reloading or closing the tab,
Cancel and explicit Sign out discard unsaved work. Drafts are never persisted in
localStorage, sessionStorage, IndexedDB or URLs.

## Build and validate

Use Go 1.26.5 in the 1.26 series, Node 24.19 and npm 11. Versions are pinned in
Go modules and the npm lockfile. Docker build stages retain the reference's
pinned Node 24.19.0 and Go 1.26.9 images; the final image is nonroot distroless.

```sh
make deps
make format check test race build
make browser-install browser-test
make docker-build
```

`make check` runs gofmt verification, go vet, TypeScript and Prettier. Domain
tests cover reopen durability, isolation/scopes, duplicates, complete/multi-sense
preservation, malformed input, stale concurrent writes, pagination and exact retry
semantics. Transport tests sign in against a synthetic issuer and use the actual
official SDK to create/read/complete across HTTP and MCP, including forbidden
calls, CSRF and owner-input rejection. Adapted identity regressions exercise
strict claims, host binding, rotating refresh, retryable provider/JWKS failures,
restart recovery and logout. No real identity is used.

The Chromium suite serves the **production export** through the actual Go handler
and CSP. It exercises sign-in, an empty start, a saved draft, completion through
MCP, live popup refresh, contextual examples, sense tabs, editor failure/retry,
conflict review, server pagination, mobile fit and display-only storage. It also
tests background/list/detail and direct-save authentication failures, full-field
retention, same-owner sign-in with fresh CSRF, different-owner blocking and exact
retry after a committed save's response was lost. Its
synthetic token helper exists only in `_test.go` and never in the production
binary. Test records live in temporary SQLite databases. Browser emulation is
local acceptance, not proof of physical-device or real-provider acceptance.

See [setup and protected configuration](docs/setup.md) for key generation,
explicit owner enrollment and running the service. See [agent connection and
MCP examples](docs/agents.md) for the external OAuth/gateway contract and tools.
This task prepares source and a Docker image; it does not configure a real
provider, gateway, route, deployment, repository release or external client.

## Read the code

1. `internal/store/model.go`, `validation.go`, `schema.go`, `words.go`: fields,
   bounds, schema, owner checks, transaction and retry ordering.
2. `internal/identity`: explicit enrollment lookup, browser OIDC/refresh, CSRF and
   separate MCP gateway verification.
3. `internal/httpapp/server.go`, `internal/mcptools/handler.go`: authenticated
   transport adapters sharing the store.
4. `web/components/vocabulary.tsx`, `editor.tsx`, `learning.tsx`: real pagination,
   display preferences, recoverable editing and the approved learner interface.
5. `cmd/vocabbank/main.go`: configuration, keygen/enrollment, and the single server.

For a normal trace, browser Save sends a capture and retry key. The verified
session supplies the owner; the store inserts an empty draft and remembers its
result in the same transaction. An agent lists `needs_details`, reads the current
revision and calls `complete_word`. Valid senses replace only learning content,
advance the revision and commit their exact result. The next visible refresh
renders those senses while retaining the captured sentence and note.

For a failure trace, the editor starts at revision 2 while MCP commits revision
3. Save with expected revision 2 returns HTTP 409 `stale_revision`; no user fields,
senses or retry result are written. The form keeps the user's input. Review
latest version loads the current saved fields for comparison; only an explicit
Use latest fields replaces the form. A malformed completion similarly leaves
the existing draft or previous learning content intact.

## CI and production preparation

The [dedicated workflow](../.github/workflows/vocabbank.yml) checks VocabBank PRs,
matching main pushes and its own tags. `make chart-test` checks the app-only Helm
chart plus release/publication/promotion policies; `make container-test` exercises
the actual Linux amd64 nonroot executable's protected-file and SQLite CLI paths.
Install check-only dependencies from `deploy/gitops/requirements.txt` in an
isolated Python environment.

Only canonical `VocabBank/vX.Y.Z` tags at verified merged main commits can publish
after the repository-level `VOCAB_GITOPS_ENABLED=true` activation and existing
shared publication environment's tag rule. The [production contract](deploy/gitops/README.md)
owns private Zot image/chart publication and the fixed private Fleet file; CI
preserves operator-owned claims, maintenance, resources, identity and TLS gates.
The chart targets new namespace `vocabbank` with a single retained SQLite writer.
Preparation does not establish actual identity, deployment or client acceptance.
