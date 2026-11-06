# Artificial Memory

A hosted MCP decision journal for agents. Preserve what was decided, why it was
decided, which alternatives were rejected, and how the reasoning changed.

The design checkpoint is complete. The storage adapter, MCP server, and deployment
artifacts are not implemented yet; this directory has no runnable service.

## Intended behavior

- Save structured, sourced reasoning summaries in an append-only journal.
- Give each account-owned area, such as GuestUp or CIS570, a separate memory table
  inside one SQLite database.
- Organize discussions under stable topic IDs, with individually addressable
  decisions, parent relationships, and explicit cross-topic links.
- Retrieve topic and ancestor context alongside matching decisions, including
  recorded rejection reasons and later replacements or corrections.
- Search indexed metadata, tags, dates, and full text.
- Expose `remember`, `retrieve`, and `list_spaces` through authenticated Streamable
  HTTP MCP using the existing MCPGateway identity contract.

Read [the design and tool contract](DESIGN.md) for the implementation target.
Development follows [AGENTS.md](../AGENTS.md) and the
[shared workflow](../docs/DEVELOPMENT-WORKFLOW.md).

## Implementation milestones

1. Indexed SQLite journal, topic reconstruction, and storage conformance checks.
2. Authenticated MCP tools, production command, and transport/ownership checks.
3. Container and k3s Helm packaging, backup/restore checks, and a synthetic
   agent-driven example.

Each coding milestone requires the repository's Builder, Translation, validation,
and independent Review stages before its local checkpoint commit. Temporary task
records stay in ignored `.development/` directories and are not project source.
Publication, identity provisioning, and live deployment remain separate actions.
