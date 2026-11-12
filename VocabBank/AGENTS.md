# VocabBank contributor instructions

VocabBank is an independent English vocabulary app in this monorepo. Follow the
root development workflow, then read README.md and
[identity/setup](docs/setup.md) before changing authentication or persistence.
Production starts empty. Fixtures contain synthetic terms and identities only.
Keep captured text, notes, sources, provider subjects, tokens, credentials and
SQLite files out of source. There is no browser extension or built-in AI.

The browser API and official SDK MCP share owner-qualified store rules. Preserve
captures when completing learning content, optimistic revisions, transactional
writes, exact retry snapshots, and case-insensitive duplicate conflicts. Treat
saved text and URLs as untrusted data. Never add an anonymous or test-login flag
to production. The opt-in synthetic issuer is only reachable from Go test code.

One process owns SQLite and rotating browser refresh grants. Preserve exact
issuer/audience/client/scope/Host/callback/CSRF checks, encrypted server-side
refresh, and the distinct gateway grant contract. Stable protected session keys
must survive restarts. This repository has no authority over real shared
identity, gateway, hosting, DNS or releases.

Background authentication failures must preserve the mounted editor, its
capture fields, expected revision and exact retry intent. Drafts live only in
memory, bound to the opaque enrolled owner that opened the editor. Recheck the
session after separate-tab sign-in; never save another owner's draft with a new
account's CSRF token. Only explicit cancel/signout or closing/reloading the tab
discards unsaved text.

Use the Modern Go guidance for go.mod. Run `make format` after edits. Required
checks: `make check test race build`, `make browser-install browser-test`, and
`make docker-build` where Docker is available. Tests use loopback listeners;
restricted sandboxes may require local-network permission. Browser tests serve
the actual built export through the real Go handler and CSP. These checks do
not prove real-provider or external-client acceptance.
