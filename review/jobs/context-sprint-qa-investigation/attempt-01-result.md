Frozen commits verified. Let me check existing review artifacts and the skill references relevant to context building.This is an ongoing gauntlet with a canonical surface graph. Let me read the state, my job's existing attempt, and the canonical surface definition.The flow-state pack shows the expected depth and format. Let me check my job's recorded prompt to confirm the expected deliverable location.Assignment confirmed; deliverable goes to `review/surfaces/context-sprint-qa-investigation.md`. Now reading the six primary source files in full.Primary sources read. Now mapping the wiring: app-level commands, writer-fence minting, helper functions, and flow-state QA consumption.Wiring mapped. Now reading the QA test files.Now checking PrepareReview coupling, the runtime interface, and remaining helper wiring.All evidence gathered. Checking the job result format before writing deliverables.Writing the context pack now.Pack written. Now recording the job result summary.Context pack complete. Target and workspace repos verified clean (frozen commits untouched).

**Deliverables:**
- `review/surfaces/context-sprint-qa-investigation.md` — 132-line neutral context pack
- `review/jobs/context-sprint-qa-investigation/result.md` — job summary

**Pack coverage:**
- **Entrypoints/control flow**: all 7 CLI actions with dispatch paths, TUI/web shared runner, full RunQA ordering (double PrepareReview gating, resume-vs-fresh attempt selection, worker pool abort semantics), per-shard fail-closed gates (identity-before/drift-after/map revalidation/permission verification/turn budget/retry classification), QAMap determinism pipeline, synthesis mechanics, RecoverQA + boot reconcile.
- **State**: private verification tree with ID grammar, pointer-last publication, immutable-map byte CAS, strict 0700/0600/symlink/128 MiB load grammar, retention rules, flow-pointer preservation and DB-mirror relationship.
- **Trust boundaries**: writer-fence tied to outer-lease heartbeats, self-approval rejection, catalog-owned checks, target-drift detection, closed enums, lower-only budgets.
- **Contracts/tests**: TRD §18D item 2, ARCHITECTURE L708, cli-reference budget table; 30 tests itemized by pinned property with factual gaps noted.
- **12 explicit unknowns** left for reviewers (challenger wire-up status, dead `qaWorkspaceInputRef` helper, unexpanded context requests, resume-mismatch fallthrough, heartbeat-during-cancellation interplay, chmod-walk side effects, etc.).

No defect claims made; descriptive only per doctrine.