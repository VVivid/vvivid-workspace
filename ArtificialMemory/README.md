# Artificial Memory

A decision journal for agents. Preserve what was decided, why it was decided,
which alternatives were rejected, and how the reasoning changed.

The independent Go module implements the storage library: an account-owned,
append-only SQLite journal with indexed, bounded retrieval. The authenticated MCP
service, command, container, Helm chart, and recovery tooling are later milestones.
There is no runnable network service yet.

## Build and test

Requires Go 1.26.9 or a compatible later toolchain and Make. The SQLite driver is
pure Go; the race detector additionally requires a supported C compiler.

```sh
cd ArtificialMemory
make check test race build
```

`build` compiles library packages. Root delegation uses
`make artificial-memory-check artificial-memory-test artificial-memory-race artificial-memory-build`.
Tests use only synthetic records in temporary private databases.

## Reading order

- [Public domain and storage interface](memory/memory.go): requests, records,
  response bounds and typed relationships.
- [SQLite entry point](store/sqlite/store.go), [schema](store/sqlite/schema.go),
  [append transaction](store/sqlite/remember.go), and
  [retrieval](store/sqlite/retrieve.go).
- [Behavioral tests](store/sqlite/store_test.go) and
  [design and future MCP contract](DESIGN.md).

## Storage contract

`sqlite.Open(ctx, path)` opens a disk database, creates missing parent directories
with mode 0700 and a new database with mode 0600, and refuses nonprivate files,
symlink database files, SQLite URI/memory paths, unrelated schemas, and unsupported
schema versions. Existing parent permissions are preserved. Use a dedicated
private directory. It verifies WAL mode and enables foreign keys, full synchronous
commits, a five-second busy wait, and one connection per store. A persisted
application ID and `user_version` identify the schema. This is one writer process;
multiple independently opened stores are not a coordinated write pool.

The public adapter accepts **trusted internal account and client IDs** supplied by
its caller. It is not an authentication boundary. The future service must verify
the original issuer/subject and resolve explicit active local enrollment before
calling this API; tool arguments must never choose an owner. Identity management
is separate from immutable journal records.

Every account-owned space has its own generated physical table in one database.
Space names are data, including punctuation; they are never interpolated into
SQL. A shared registry provides direct span/topic lookup, with shared tags, typed
links, and an FTS5 projection. Journal tables, registry, catalog, tags, links,
idempotency results and cursor settings reject SQL UPDATE and DELETE. FTS is a
derived projection. No public operation exposes SQL or edits existing entries.

### Remember

`Remember(ctx, actor, request)` accepts a space, idempotency key, optional existing
topic ID, and 1–100 ordered entries. Without a topic ID, the first entry must be a
parentless `topic`; its generated span ID is the topic ID. All other entries have
distinct span IDs and that topic ID. Omitted parents use the topic root. Explicit
parents must be earlier nodes in that same topic and space. `local_id` names an
entry for later parents/links in the batch; a reference supplies either `span_id`
or `local_id`. Links may target any earlier node in the same account, including
another topic or space. Foreign and unavailable references return `ErrNotFound`.

The entire batch, new table/catalog entry, tags, links, registry, FTS rows, and
durable result commit together. Failed references roll back DDL too. A repeated
account/key and equivalent input returns the original IDs, sequences, timestamps,
and records. Changed input, including a changed client, returns `ErrConflict`.
Canonical comparison normalizes JSON member order/whitespace, explicit version 1
versus the default, missing nondecision payload versus `{}`, UTC timestamp offsets,
space-name surrounding whitespace, and tag/link ordering and duplicates. Numbers
retain their exact JSON representation and precision (`1` and `1.0` differ).
Ordered entries, sources, reasoning arrays, and local IDs remain significant.
Omitted contemporary times are hashed before server time is assigned.

Kinds are `topic`, `context`, `proposal`, `decision`, `note`, and `retraction`.
Versioned object payloads preserve additional fields. Every decision version
requires the v1 core: nonempty `question` and `outcome`, `reasons` array and
`alternatives` array. Each alternative requires `option`, a disposition of
`rejected`, `deferred`, or `considered`, and `reasons`. Empty arrays explicitly
represent missing evidence. Sources/excerpts and attributions remain untrusted
assertions and are returned as data, never interpreted as instructions or proof
of human approval.

`occurred_at` is separate from server `recorded_at` and global append sequence.
Omitted contemporary times default to server time. Historical entries must set
`historical=true` and supply a time or explicitly set `time_unknown=true`; unknown
event time stays null. Instants use UTC fixed-width nanoseconds in SQLite and
years 1–9999. Append sequence describes accepted writes, not inferred causality.

### Retrieve

`Retrieve(ctx, accountID, query)` requires nonempty selected `spaces` or explicit
`all_spaces=true`. Span IDs, topic IDs, external topic reference, kinds, keywords,
tags, and occurred/recorded bounds intersect. All tags must match. Supplied
unavailable, foreign, or out-of-selected-space span/topic IDs return `ErrNotFound`.
An external reference selects the history of each matching topic in the selected
spaces; it is not globally unique and later ticket reuse does not merge topics.

Keyword input accepts at most 32 whitespace-separated phrases, joined as quoted
conjunctions. FTS5 expression syntax is not accepted. FTS5 indexes title, context,
external reference, and the
decision question/outcome/reasons/alternatives, including rejected choices.
Keyword default order is deterministic relevance followed by newest event time
(recorded time when unknown), then sequence. Relevance sums per-phrase literal,
case-insensitive field presence: title/reference 4, context 2, reasoning 1. It
does not depend on corpus-wide BM25 statistics, so appends cannot reorder existing
pages. SQLite `lower` supplies ASCII case folding for scoring; FTS5 `unicode61`
supplies Unicode matching. Explicit topic/reference queries default to
chronological event time with recorded-time fallback, then sequence. Other
queries default to newest. Order may be selected explicitly.

Time ranges are half-open `[from,before)`. Alternatively supply `calendar` with
`start_date` and exclusive `end_date` (`YYYY-MM-DD`) and an IANA timezone, default
`America/New_York`. Calendar and instant bounds cannot be mixed in the same range.
The response returns applied UTC bounds. DST days may have 23 or 25 hours;
unknown event times do not match occurred-time ranges.

Matches are separate from required roots/ancestors, which ignore keyword, tag,
kind, and date filters. A recursive union walks all primary ancestors; payloads
load once per participating area, plus one batched tag query. Relationship
references cover both directions around the matches and their complete ancestry,
including incoming corrections, supersessions and retractions outside the match
filters. Cross-space references identify same-account nodes without expanding
their payloads. Retrieve their space explicitly to expand them. Competing branches
are preserved; no chronology or overlapping tag implies a dependency or winner.

Each query freezes the account's current maximum sequence. A signed persisted
cursor binds the snapshot, account, normalized query, limits and stream offsets.
Keep the same query and pass `next_cursor` until empty. Each match page emits its
matches once; subsequent support pages emit more ancestors/relationships with
the same `match_ids`, before moving to the next match page. Context may repeat
across different match pages. All streams are bounded, and the completeness flags
describe that match page and snapshot. `matches_complete` means there are no later
match pages; `complete` means all streams are exhausted. A completed single-span
query does **not** claim a topic's full history. Appends after the snapshot,
including new annotations, appear in a fresh query. Cursors survive reopening the
same database; they are invalid with another owner/query/database.

### Bounds

Default/max matching entries are 20/100; context and relationship references are
100/200 each per response. At most 100 topic or span selectors and 32 tag selectors
are accepted. Accounts have at most 1000 spaces, so `ListSpaces` is bounded. Each
entry has at most 32 tags, 32 links and 16 sources. Byte limits are: title 512,
context 16384, JSON payload 32768, tag/local ID 64, external reference 256,
source reference 2048, source excerpt 4096, attribution 128. Keywords are 512
bytes; account/client IDs 256 and keys/space names 128. Oversized or malformed
input returns `ErrInvalid`. Relation pagination prevents arbitrarily many later
corrections from expanding one result without limit.

## Remaining milestones

1. Authenticated Streamable HTTP MCP tools (`remember`, `retrieve`, `list_spaces`)
   and production command using the existing MCPGateway identity contract.
2. Container/k3s packaging, supported backup/isolated restore, and a runnable
   synthetic agent example.

Development follows [root instructions](../AGENTS.md) and the
[shared workflow](../docs/DEVELOPMENT-WORKFLOW.md). Publication, identity
provisioning, real-memory writes, and live deployment are separate actions.
