# Resume Google Health collector

Development continues in `vvivid-workspace/GoogleHealthMCP`. This directory was imported from local source without prior Git history. See README.md and ARCHITECTURE.md for build instructions and contracts.

The collector implements authorization, bounded historical import, daily synchronization, read-only status, schema migrations, and atomic publication with resumable pagination. Dedicated SQL query access remains future work.

Private operational handoffs remain in the original local project directory and are excluded from this public source import. Source migration does not establish current OAuth, scheduled collection, history completion, or deployed-image status. Inspect actual runtime state before any operational changes; never replay an uncertain writer dispatch.

See the workspace migration report for checks actually executed during migration.
