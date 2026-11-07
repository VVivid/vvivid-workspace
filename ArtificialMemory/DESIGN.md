# Artificial Memory design

This specifies the first proof of concept. The indexed journal, bounded
retrieval, and gateway-authenticated MCP service are implemented;
[README](README.md) documents exact normalization, pagination, receipts and limits.
The [identity guide](docs/AUTH.md) covers implemented configuration and enrollment.
Project-local container/Helm packaging and consistent backup/isolated restore
are implemented; [the runbook](docs/RUNBOOK.md) distinguishes local synthetic
verification from real login, publication and deployment.

## Purpose and boundaries

An external agent records structured summaries of actual discussions: context,
decisions, reasons, alternatives, and sources. The service persists and retrieves
them. It does not infer missing reasons, ingest whole transcripts, or run an
internal model. Later agents can answer both why an option was chosen and why
another was rejected, citing record IDs and source references.

Use an independent Go module, the official MCP Go SDK, and SQLite behind a storage
interface. Storage-specific SQL, physical tables, and indexing stay inside the
adapter. Future adapters preserve the logical contract without changing MCP tools.
Semantic search, a browser UI, automatic extraction, and other adapters are outside
the first version.

## Topics, entries, and relationships

- A space is an account-owned area, such as GuestUp or CIS570.
- A topic is a durable root record, such as PAYM-123 and its initial context in
  GuestUp. Its `topic_id` anchors retrieval. An optional `external_ref` stores a
  ticket or other domain reference independently of that ID.
- Each immutable journal entry has a unique `span_id` and retains its `topic_id`.
  The topic root's span ID is also its topic ID.
- A non-root entry has a `parent_id` pointing at its root or an earlier entry in
  the same topic. Roots have no parent. All node IDs share a registry namespace.
- Typed links express `depends_on`, `related_to`, `supersedes`, `corrects`, and
  `retracts`. Links may cross topics and spaces within the same account. Parents
  stay within a topic. Reject foreign-owner references.
- Parents describe the primary tree; links describe the additional graph.
  Shared tags and timestamps never imply dependency automatically.

For example, PAYM-123 can contain separate Go and SQLite decisions with rejected
alternatives. A later PAYM-1234 topic can link to PAYM-123 and depend on its SQLite
decision. Only a genuine replacement appends a `supersedes` link; a task that
reuses a decision does not automatically replace it.

The model borrows trace/span relationships without requiring an OpenTelemetry
span lifecycle or encoding. Operational telemetry IDs remain separate provenance.

## Record envelope

Every area has the same schema: `span_id`, `topic_id`, `parent_id`, `kind`,
`schema_version`, `title`, `context`, structured JSON `payload`, `occurred_at`, and
provenance. The server records `recorded_at`, append sequence, authenticated account
and MCP client, and request identity. Tags and typed links are indexed relations.

Initial kinds include topic, context, proposal, decision, note, and retraction.
Versioned domain-specific payloads allow architecture, coursework, bugs, and other
uses to share the envelope. A decision payload records the question, chosen outcome,
reasons, and alternatives. Each alternative records an option, disposition
(rejected, deferred, or considered), and reasons. An alternative can remain inside
the atomic decision or have a separate node when its history deserves one.

Provenance includes source references, supporting excerpts, and attribution such
as user-confirmed or agent-proposed. These are attributed assertions, not verified
human approval. Authentication supplies the actual writer. Missing reasons,
sources, decision makers, and historical timestamps must not be invented.

`occurred_at` describes the event; `recorded_at` describes persistence. Default to
server time for omitted timestamps on contemporaneous entries. Historical imports
must supply a known time or explicitly mark it unknown. Store known instants in
UTC. Append sequence orders accepted writes, not causality. Calendar queries use
an explicit IANA timezone, default America/New_York.

## Append-only storage and indexes

Use separate memory tables per space in one SQLite database. A shared space catalog
maps names to server-generated SQL identifiers; user names never become raw SQL
identifiers. A shared node registry locates entries without scanning all area
tables. Shared tags, links, and a full-text projection enable cross-area queries.

Journal records, tags, links, and durable idempotency results reject UPDATE and
DELETE. Corrections, replacements, and withdrawals append new nodes and links.
Derived indexes may be rebuilt. Identity administration is separate from the
immutable memory journal.

Create the following access paths with each area's standard schema:

| Access pattern | Index or mechanism |
| --- | --- |
| Individual node | Unique span ID in registry and area table |
| Topic by domain reference | Account, space, external reference |
| Topic history | Topic ID, occurred time, append sequence |
| Immediate children | Parent ID, append sequence |
| Kind and event date | Kind, occurred time, append sequence |
| Saved date | Recorded time, append sequence |
| Tags | Account/tag/span and account/span/tag indexes |
| Typed relationships | Account/source/type/target and account/target/type/source |
| Reasoning keywords | SQLite FTS5 over title, context, decisions, reasons, alternatives |

Recurring query metadata belongs in typed columns; arbitrary domain details stay
in JSON. Add expression indexes only for an established query need. Apply ownership
and selected-space filters before limiting results. Fetch topic entries and
supporting references in batches, avoiding one query per decision.

## MCP capabilities

### remember

Accept a space, idempotency key, optional existing topic ID, and ordered `entries`
array. Single entries and batches use the same contract. Creating a topic includes
its root entry and returns its generated topic ID. Otherwise append under the
selected topic. Entries carry parents and optional typed links; batch-local
references may target earlier entries in the same call.

Create a missing space on its first successful write. Commit records, catalog,
registry, tags, links, search projection, and idempotency result atomically. Invalid
input rolls back the complete batch, including newly created tables. Retrying the
same account/key and equivalent input returns the original result; changed input
with that key fails. Return a compact receipt with generated IDs, sequences and
server/event timestamps. Retrieve the IDs to expand their data. Full records stay
in durable adapter replay state; escaped source text cannot make a successful
commit permanently unacknowledgeable.

One request can save many nodes; one topic can span many requests. Request IDs
identify write batches, not topics. Batch compaction must never discard source
nodes. A later sourced summary is another appended entry.

### retrieve

Require selected spaces or an explicit account-wide request. Accept topic IDs,
span IDs, keywords, tags, timestamp ranges, sorting, and bounded continuation.
Supplied filters intersect; all supplied tags must match. Defaults: 20 matching
entries, maximum 100; relevance then newest for keyword search; newest otherwise;
chronological order for explicit topic history.

Return matches separately from supporting topic/root/ancestor context, including
older context outside the keyword/tag/date filters. Bound contextual expansion
as well and expose continuation and completeness. A single-span lookup must
identify its topic and ancestry instead of presenting it as complete history.

Expose incoming replacement, correction, and withdrawal references even when the
newer entry fails the original filter. Preserve conflicting branches instead of
declaring the newest timestamp authoritative. Include related-topic references;
expanding a linked tree is explicit and respects selected-space/account scope.

Accept timestamp bounds or calendar-date/timezone selections. Resolve calendar
dates to half-open UTC ranges and return the applied bounds. Unknown occurred
times do not acquire fabricated dates. Date filters constrain matching decisions,
not the older context supplied to explain them.

### list_spaces

List the authenticated account's areas so agents can discover names before writing
or searching. Tools expose neither arbitrary SQL nor agent-chosen owner identities.

## Identity and hosted operation

Follow the existing [MCPGateway identity contract](../MCPGateway/docs/IDENTITY.md).
Serve Streamable HTTP MCP behind a fixed gateway route. Verify pinned signatures,
exact issuer/audience/type/time, allowed clients, and operation permissions. Resolve
original `(identity_issuer, sub)` against explicit active local enrollment on every
request. Define `memory:read` and `memory:write`. All data remains account-owned even
when only one person is enrolled.

The implemented command binds loopback by default. Exact canonical Host/Origin,
pinned compact RS256 public-key verification, caller allowlist, positive bounded
60-second times, and active enrollment are checked before application work.
Stateless MCP requests do not cache enrollment or operation scopes. Mutable
versioned enrollment tables retain stable internal account IDs without rewriting
the immutable journal. Local admin commands enroll/disable original identities;
the memory tools cannot administer accounts. See [the guide](docs/AUTH.md).

Live and ready probes are separate; readiness checks migrated storage without
writing memories. Header/body/response/concurrency/time bounds and graceful
SIGINT/SIGTERM shutdown are implemented. Only lifecycle events are logged. The
official SDK HTTP suite and real production-process restart tests use synthetic
gateway signatures; they do not provision or prove a real login integration.

The chart prepares one k3s replica (zero for maintenance), Recreate strategy,
retained persistent volume, private ClusterIP Service, executable probes,
graceful shutdown, and a nonroot image with read-only root filesystem. It reuses
operator-provided namespace/gateway/identity/public JWKS and requires an image
digest plus explicit node/storage inputs. SQLite uses WAL, foreign keys, bounded
lock waits and in-memory temporary sorts. Private 0700 `/data/sqlite` and
OnRootMismatch volume ownership handling preserve 0600 database permissions;
operators must verify compatible CSI/provisioner behavior. Multiple
SQLite-owning replicas must not share the volume.

Operator backup and restore use SQLite `VACUUM INTO`, private new destinations,
and integrity/application/schema validation. A live WAL snapshot and isolated
reopened restore test preserve records, exact JSON, rejected reasons, links,
search, enrollment, idempotency and cursor signing state. Copying a live main
database file alone is not a backup procedure. Cutover quiesces the writer and
selects a fresh verified volume, retaining the original. Publication, actual
client login setup, enrollment, and deployment remain separate actions.

## Acceptance scenarios

- Explain actual recorded Go/SQLite choices and rejected alternatives with source
  and record references, distinguishing original reasons from later changes.
- Reconstruct a multi-batch topic and preserve older explanatory context despite
  keyword, tag, and date filters.
- Follow explicit links to an overlapping ticket and across an owner's areas.
- Reject foreign-owner reads, writes, parents, links, and guessed record IDs.
- Reject UPDATE/DELETE; append corrections and expose later replacement/withdrawal
  references outside original filters.
- Replay committed-but-unacknowledged writes without duplicates, reject changed
  idempotency inputs, and roll back invalid batches completely.
- Verify query plans, date/timezone boundaries, pagination completeness, reopening
  persisted data, and isolated backup restoration.
- Exercise real MCP discovery/calls over HTTP, read-only scopes, wrong identity,
  audience/client rejection, and clean shutdown/restart.
