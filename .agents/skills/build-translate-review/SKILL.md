---
name: build-translate-review
description: Implement application features, fixes, and refactors through sequential Builder, Translation, and independent Review agents. Use when a coding goal needs working code that future contributors can understand. Excludes read-only investigations and documentation-only changes.
---

# Build -> Translate -> Review

Deliver the user's coding goal through three sequential agents. Treat code readability as an acceptance criterion: a future contributor must understand the behavior without this conversation. Translation means improving the actual code for human readers, not translating languages or merely explaining the implementation.

Invocation example:

```text
$build-translate-review Add pagination to this API.
```

## Coordination and boundaries

**MUST: Never push anything to any remote without the user's explicit permission for that push**, covering the remote and refs. This includes branches, tags, and deletions through Git, APIs, other tools, or delegated agents. Plan approval, implementation, local commits, successful review, PR creation requests, and release preparation do not imply push permission. Prepare and validate locally, then wait for explicit authorization before publication.

Read root AGENTS.md, applicable project instructions, and the existing implementation. Follow the [shared development workflow](../../../docs/DEVELOPMENT-WORKFLOW.md). Before implementation, present ordered checkpoint outcomes, scope, affected projects and consumers, dependencies, exit criteria, and validation commands; obtain the user's agreement once. An explicit instruction to implement an already agreed plan satisfies this gate. Seek renewed agreement for material scope or acceptance changes; resolve routine implementation choices independently. Respect host planning mode.

Before editing for a new task, create or reuse its dedicated branch/worktree following the shared workflow. Name branches <type>/<area>/<task>; use one branch/worktree for the task, all its checkpoints, and sequential stages, including cross-project work. Supply its path, branch, and verified base SHA to every stage. Read-only investigation may use the current checkout. Follow the shared PR, synchronization, and cleanup rules; the user controls merging.

Run the complete Builder -> Translation -> final validation -> independent Review sequence for each coding checkpoint. Advance automatically only when exit criteria pass and Review returns Approved. A Denied verdict pauses work as specified below.

This skill explicitly calls for delegation to Builder, Translation, and Review. Launch every stage with exactly `model="gpt-6.1-sol"`. Builder and Review require `reasoning_effort="max"`; Translation requires `reasoning_effort="xhigh"`. Set both values explicitly for every launch, including corrections and repeat reviews. Use `fork_turns="none"` when supplying overrides. Do not inherit defaults or substitute models or efforts. In custom-agent files, use `model_reasoning_effort` with the stage's required value and verify the file before using it; a conflicting custom-agent setting can override launch arguments.

Before accepting a stage's work, verify its effective model and reasoning effort from host launch or runtime metadata. Record the stage, agent ID, verified model, verified effort, and evidence source in the conversation. Requested arguments, saved configuration, and an agent's self-report are not runtime proof. If settings are unavailable, mismatched, or cannot be verified, or independent-agent capability is unavailable, stop and leave the workflow incomplete. Do not present a single-agent pass as independent review.

Run stages sequentially, with one editing stage active at a time. Launch fresh agents with `fork_turns="none"` where supported. Give them the original request, scoped plan and acceptance criteria, target directory, applicable project instructions, current changes or diff, and validation commands and results. Give each agent this skill's relevant instructions. Do not supply earlier approval verdicts or persuasive explanations as evidence of correctness.

Each agent must read the applicable project instructions before acting. The skill grants no additional permission for files, tests, Git operations, dependency changes, service execution, deployment, database writes, or external actions. Preserve unrelated user changes. Keep the agreed plan and progress in the affected project's ignored .development/<task>/CHECKPOINTS.md and RESUME.md as described in the shared workflow. Keep stage evidence there and in the conversation; never stage, commit, or force-add temporary records. Use project and language guidance where applicable without requiring a particular service, language, plugin, or external API.

Before Go edits, follow the repository-local [Modern Go Guidelines skill](../use-modern-go/SKILL.md). Use its full target-file guideline list; preserve module-specific language constraints. For cloud execution, verify [cloud prerequisites](../../../docs/CLOUD-DEVELOPMENT.md) before claiming the workflow is available.

## 1. Builder

Implement the smallest complete solution within the authorized scope. Trace relevant entry points, callers, validation, persistence, and side effects before changing behavior. Prefer straightforward, idiomatic code and established relevant patterns; consult maintained implementations when they resolve a concrete design question.

Make domain decisions, state changes, side effects, and failure handling visible. Avoid unnecessary wrappers, speculative abstractions, duplicate implementations, and unrelated cleanup. Preserve supported contracts and data invariants. Comments should explain reasons or constraints that code cannot express.

Return changed files, the resulting behavior, important state changes and side effects, validation evidence, and unresolved issues. Readability is the Builder's responsibility from the start.

## 2. Translation

Read the implementation independently as a future contributor. Identify exact files and symbols where understanding requires hidden context or unnecessary reconstruction. Challenge vague names, scattered rules, unnecessary indirection, dense control flow, and hidden side effects.

Improve the actual code within scope. Every edit must have a concrete readability benefit; clear code may pass unchanged. Preserve behavior, public contracts, validation, authorization, transaction boundaries, idempotency, side-effect ordering, and error handling. Do not replace useful boundaries or idioms just to shorten the code.

If a correctness issue is discovered or a proposed edit would change semantics, return the issue to Builder and stop Translation edits. After Builder corrects it, have Translation inspect the revised implementation before advancing. Do not loop indefinitely on the same unresolved issue; report the blocker when progress requires a user decision or unavailable capability.

Finish with one concrete normal-case trace and one failure-case trace through the final code. Confirm that a contributor can locate the entry point, follow decisions and state changes, understand failures, and find where to make the next change safely. Disclose unresolved readability problems.

## Final validation

After Translation finishes, apply the project's formatter and complete its required build or equivalent validation on the final code. Run relevant tests only when permitted by the user's authorization and project instructions. Establish commands from project configuration rather than assuming a language or toolchain.

Obtain any required authorization for generated artifacts or other side effects before running commands. Earlier validation does not cover subsequent edits. Failed or unavailable required validation leaves the work incomplete; report the specific limitation and do not advance to a completion verdict.

## 3. Review

Launch a fresh independent, read-only reviewer after final validation. Check the implementation against the original goal, acceptance criteria, scope, project and language rules, security, data integrity, formatting, and validation evidence. Inspect the code and relevant consumers; prior stage reports are context, not proof. Check that Translation preserved behavior and that changes introduce no unnecessary compatibility paths or speculative machinery.

Return concise findings with file and symbol references and exactly one final verdict: `Approved` or `Denied`. A blocking correctness, scope, readability, or required-validation issue must result in `Denied`.

The coordinator must relay the report faithfully. On `Denied`, stop and wait for the user's next instruction; do not automatically fix, soften, or override the verdict. An authorized subsequent correction must return through Builder, Translation, final validation, and a fresh Review.

## Handoff

Report completion only after all stages finish, required validation passes, and Review returns `Approved`. Provide a short reading order with file links, one input-to-result walkthrough, one failure case, important state changes and side effects, validation results and limitations, and the reviewer report. Include each stage's agent ID and verified runtime model and reasoning effort. Clearly distinguish checks actually run from cases inspected by reading code.

## Temporary checkpoint lifecycle

Use separate .development/<task>/ directories for tasks in affected projects, or at the repository root for repository-wide work. For cross-project work, designate one authoritative plan and link other local records to it. Verify records are ignored before creating them. At meaningful pauses, record actual checks, failures, unresolved work, stage evidence, and the next action. Inspect files and Git state when resuming and continue the first incomplete agreed checkpoint.

Keep records while the task is incomplete, blocked, denied, or awaiting user action. After the whole task passes acceptance and the final handoff is delivered, delete only its temporary directories. Preserve lasting architecture, usage, and operational information in maintained docs without copying the development diary. Never delete another task's records or treat deletion as completion.
