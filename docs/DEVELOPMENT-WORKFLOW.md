# Shared development workflow

This methodology applies to every project. Read [root instructions](../AGENTS.md), the [project map](PROJECT-MAP.md), and applicable project instructions. Project `AGENTS.md` files may add local contracts, constraints, and commands. The repository-local [Build -> Translate -> Review skill](../.agents/skills/build-translate-review/SKILL.md) owns coding-stage instructions and required agent settings; do not substitute a personal installation.

Before Go edits, follow the repository-local [Modern Go Guidelines skill](../.agents/skills/use-modern-go/SKILL.md), including its version-aware `list` step. Shared skills and required resources belong under root `.agents/skills/`. See [cloud prerequisites](CLOUD-DEVELOPMENT.md) before using this methodology in Codex Cloud; host capabilities and runtime verification remain required.

## Agree before implementing

Inspect code, consumers, documentation, Git state, and matching unfinished local tasks. Present a plan containing:

- Goal, expected behavior, exclusions, affected projects and consumers.
- Ordered checkpoints with reviewable outcomes, dependencies, exit criteria, and validation commands.
- Contract changes, migrations, operational effects, and separately required authorization.

Obtain the user's agreement once before implementation. An explicit instruction to implement an agreed plan satisfies this gate. Resolve routine implementation choices independently. Seek renewed agreement for material changes to scope, behavior, contracts, or acceptance criteria. Agreement does not authorize unrelated deployment, live data, or infrastructure actions.

## Branches and worktrees

Read-only investigation may use the current checkout. Before edits for a new task, including documentation or instruction changes, create a dedicated task branch and worktree. Inspect existing branches/worktrees first; reuse only the exact same task and its follow-ups. All checkpoints, editing stages, final validation, and independent Review use that worktree. One cross-project task uses one repository-wide branch/worktree; checkpoints do not require separate branches.

Branch names use `<type>/<area>/<task>` with lowercase hyphenated slugs:

| Part | Convention |
| --- | --- |
| Type | `feature`, `bugfix`, `docs`, `refactor`, `chore`, or `release` |
| Area | `shopping`, `google-health`, `sshmcp`, `xtrace`, or `workspace` for shared/cross-project work |
| Task | Concise outcome, optionally prefixed by a real issue number; never invent a ticket |

Examples: `feature/shopping/aisle-order`, `bugfix/sshmcp/cancel-job`, and `docs/workspace/branching-policy`. Add an area when introducing a new project. A `release` branch is optional and used only for explicitly requested release preparation; routine changes target `main` through PRs. Project-specific default branches and a permanent `develop` branch are not part of this workflow.

For a new branch, verify the configured remote and its default branch, refresh the exact `main` ref, and record the resolved base SHA. Use current `origin/main` as the base. If the remote/default differs or freshness cannot be established, resolve that discrepancy before edits rather than silently choosing another base. Local task setup is a prerequisite of an authorized editing task; it does not authorize publication or releases.

Prefer a managed task worktree when available; otherwise use `git worktree add -b <branch> <task-path> origin/main`, with a path outside the primary checkout. State the actual path, branch, and base SHA in the agreed plan and local records. Preserve unrelated edits; never copy them into the task worktree. Continuing unfinished work from an older checkout requires preserving it and arranging an explicit, scoped transfer before relocation. This policy applies to new editing tasks; adoption does not automatically relocate existing work.

When synchronization is needed, use a clean task worktree, fetch the verified ref, and merge `origin/main` into the task branch. Do not automatically rebase, amend, force-push, reset, or rewrite published history. Preserve dirty state and report unresolved conflicts. Source changes after validation or Review invalidate affected evidence and require applicable checks and fresh Review before delivery.

**MUST: Never push anything to any remote without the user's explicit permission for that push.** Permission must cover the intended remote and refs, including branch/tag creation, updates, and deletion. This applies to Git, APIs, other tools, and delegated agents. Plan approval, implementation, local commits, successful review, a PR creation request, or release preparation never implies push permission. Prepare the complete local result and validation before requesting permission. If authorization already explicitly covers the exact push, use it without asking again; changed refs or further updates need authorization covering that scope.

Deliver through a PR to `main`. Commit, PR creation, merge, and release actions require the corresponding user instruction. A PR creation request covers necessary task-file commits but does not authorize pushing; wait for explicit push permission before publishing a local branch. Inspect the complete staged diff, exclude unrelated changes and temporary records, and include affected projects and actual validation in the PR. The user controls merging; an agent may merge only on an explicit instruction for that PR after verifying its exact head, checks, and review state. Never push directly to `main`. Project release tags identify verified merged commits, not unmerged task branches.

After completion and handoff, remove only the task's temporary records. Before removing its worktree or local branch, confirm the PR is merged, intended work is present in the merged result, no uncommitted/untracked work or local-only evidence is needed, and no other task depends on it. Preserve unmerged/closed tasks for recovery unless explicitly discarded. Use managed archival for managed worktrees; never force-delete a dirty worktree. Remote branch deletion requires a request or already configured repository automation.

## Local checkpoint records

Use `<project>/.development/<task>/CHECKPOINTS.md` and `RESUME.md`, with distinct task slugs for concurrent work. Repository-wide work uses root `.development/<task>/`. Root `.gitignore` excludes `.development/` at every depth. Verify with `git check-ignore <record-path>` before creating records. Never stage, commit, or force-add them, even temporarily on a branch; inspect the staged diff before commits. Deleting a committed record later does not remove it from history.

`CHECKPOINTS.md` records the agreed plan, agreement evidence, status, exit criteria, actual validation and review evidence, and unresolved work. Use pending, in progress, blocked, and complete statuses. `RESUME.md` records current state, affected paths, relevant commits, failures and limitations, the first incomplete checkpoint, and one concrete next action. Record stage agent IDs, verified runtime model/effort, and evidence sources as required by the skill. Keep secrets and personal data out of records too.

For cross-project work, designate one affected project to own the authoritative plan. Other affected projects keep local records linking to it and identifying their progress. One coordinator owns shared changes; folders do not isolate Git operations. Do not overwrite another task's records.

## Complete each checkpoint

For each coding checkpoint, run the full repository skill sequence:

1. **Builder** implements the smallest complete agreed outcome and supplies validation evidence.
2. **Translation** independently improves code readability while preserving semantics. Correctness issues return to Builder before Translation resumes.
3. **Final validation** checks the final code after Translation using project formatting, build, and relevant tests.
4. **Independent Review** uses a fresh read-only reviewer returning exactly `Approved` or `Denied`.

The skill specifies models, effort, runtime verification, neutral stage inputs, and handoff evidence. If required settings or independence cannot be verified, leave the workflow incomplete. Distinguish checks actually run from code inspection.

Advance automatically only when exit criteria pass and Review returns `Approved`. On `Denied`, relay findings faithfully and stop for the user's instruction. An authorized correction repeats the prescribed stages and fresh Review. Failed or unavailable required checks leave the checkpoint incomplete. Documentation-only and instruction-only changes use direct relevant validation rather than application coding stages.

## Monorepo validation and delivery

Select checks from actual project configuration. Validate affected projects and supported consumers; shared tooling and cross-project contracts require broader impact checks. Projects retain independent dependencies, build contexts, runtime configuration, versions, and deployments. Do not introduce shared packages or synchronized releases without an agreed need.

Branches and worktrees contain the whole repository. Preserve unrelated changes, coordinate shared-file edits, and inspect the complete diff before staging. Current CI filters watch individual project directories: root-only changes can skip all project workflows, and workflow-only edits can skip the workflow they change. Select explicit local validation. CI changes require their own scope.

Keep source completion, independent approval, publication, deployment, and actual runtime verification distinct. Follow [the development guide](../DEVELOPMENT.md) for build and release conventions.

## Pause, resume, and clean up

At meaningful pauses, update both records with actual changes, checks, blockers, and next action. Resume by comparing source and Git state against the records and continuing the first incomplete agreed checkpoint. Missing records in a fresh clone are normal; establish a new agreed plan from maintained documentation and source instead of inventing prior acceptance. Ignored records do not travel with commits, clones, or new worktrees: an authorized handoff must transfer them explicitly or supply their state in chat.

Keep records while the task is incomplete, blocked, denied, or awaiting user action. Completing one checkpoint does not delete context needed by later checkpoints. After all agreed acceptance criteria pass, prepare the final handoff with changes, validation, limitations, and review evidence. Retain lasting architecture, usage, and operational facts in maintained documentation without copying the diary. After final handoff delivery, delete only that task's temporary directory and cross-project companion records. Do not delete imported unfinished work or another task's files. Deletion itself never establishes completion.

Previously tracked handoffs are preserved in ignored project-local storage before removal from the current tree; Git history is not rewritten. Historical validation reports may mention their former names; those references are historical context, not instructions to recreate tracked records.
