Now the gitpublish package — publication idempotency and push-after-skip semantics matter here.Publisher refutes my push-after-skip hypothesis (push runs even when the tree is unchanged). Now the runtime contract and adapter, config defaults, and DB mirror.Each task gets its own scoped runtime-store DB. Now check how the OpenCode adapter resolves sessions for continuation.Empirical confirmation obtained. Now verify the config default executable and trace the full wiring once more to nail down the finding.Finding #1 nailed down end-to-end. Now the test evidence map and durable operations wiring.Review complete. Baseline `go test ./internal/sprint/ ./internal/platform/runtime/` green at frozen commit `5d54525`. Findings below; each was stress-tested against callers, guards, and tests before reporting.

---

## F1 — Per-task OpenCode database isolation makes cross-task session continuation impossible; every multi-task execute deterministically fails at task 2

**Severity: high · Confidence: very high (mechanism empirically reproduced with the real runtime binary)**

**Claim.** Since `3672aec` ("Per-task runtime stores…", 2026-08-24), each execute turn gets its own isolated OpenCode SQLite database (`OPENCODE_DB`), while the queue design passes task 1's `SessionID` into task 2's continuation request. Session lookup is database-scoped, so the continuation fails with "Session not found", which UltraPlan's own policy maps to a hard stop. Any plan with ≥2 same-model tasks cannot complete through either documented flow (fresh run or `--resume`).

**Observable bad outcome.** Task 1 completes; task 2 fails with diagnostic `runtime-failed` ("Session not found"); stop-on-failure halts the queue; command exits ExitPartial with "execute completed with failed tasks". Retrying `--resume` reproduces identically forever: reconcile keeps task 1 complete, reuse gate picks task 1's session ID again (execute.go:186-193), and the same not-found error recurs. The sprint wedges permanently unless the operator edits models per task or splits the plan.

**Trigger/preconditions.** Execute of a plan with ≥2 top-level tasks where the second inherits the batch model (the default). Also hits: cross-process `--resume` that continues a *different* task's session, and same-task resume after `CleanupRuntimeStores` expires the retained store (>72h, store.go:221-227) — there is no pre-flight session-existence check and no fresh-fallback on this error class.

**Evidence / path.**
1. execute.go:222,246-249 — `continueSession` true → `runtimeReq.SessionID = batchSessionID` (session created under task N's store).
2. service.go:1137-1139 — `storeOwner` embeds `metadata["task"]` → `ScopedRuntimeStorePath` yields a distinct DB per task (store.go:48-51).
3. internal/platform/runtime/runtime.go:576-581 — injects `MetadataDatabasePath` = that path; agentwrap `processSpec` sets `OPENCODE_DB` and `--session <id>` (agentwrap@dccd575/opencode/runtime.go:143,155-160).
4. Empirical reproduction (opencode 1.18.23, the configured default executable, config.go:193):
   ```
   OPENCODE_DB=…/db1/opencode.db opencode run … # → creates ses_fc2cb44…
   OPENCODE_DB=…/db2/opencode.db opencode run --session ses_fc2cb44… …
   → exit=1, Error: Session not found
   ```
5. internal/platform/runtime/opencode.go:133-138 — `missingSessionPolicy` returns `PolicyDecisionStop`; no retry, no fallback to an independent full prompt (the implemented `fresh-fallback` mode triggers only when no reusable ID exists *upfront*, never on mid-run session errors).
6. Classification: execute.go:299-301 → failed/"runtime-failed" → break at :320.

**Controls/counter-evidence searched.** Tests pass because `batchExecutionRuntime` (efficiency_improvements_test.go:388-397) accepts any SessionID and ignores `RuntimeStorePath`; `TestExecuteUsesOneSessionWithCompactPerTaskContinuations` asserts the broken wiring as correct. No production code shares one store across queued tasks; user-configured global `OPENCODE_DB` is overridden per-request (agentwrap `setEnvValue`). `deleteCompletedSessions` preferring whole-store deletion (flow.go:37-42) confirms isolation is intentional — the sprint queue was just never adapted.

**Regression test.** A test asserting the continuation request's `RuntimeStorePath` equals the prior task's recorded store path (or an adapter-level integration test continuing a real session across two scoped stores). Product fix options: scope execute stores per batch rather than per task, or fall back to independent full prompts on session-not-found.

---

## F2 — `--resume` silently discards unreadable/malformed/unsupported run state and rebuilds fresh, destroying terminal history and re-executing completed tasks without diagnostics

**Severity: medium-high · Confidence: high**

**Claim.** execute.go:177 (`if existing, loadErr := LoadExecuteRunState(...); loadErr == nil && req.Resume`) treats every load failure — malformed JSON, unsupported schemaVersion, unreadable file, invalid DB record — the same as "no prior state", then unconditionally overwrites it at :180. The only guard, `validateResolvedResumeTasks` (execute.go:386-421), fires exclusively when plan.md contains `[x]`/`[/]` markers — but execute never writes plan.md and the documented resting state is unchecked (cli-reference: deferral "leaves the plan checkbox visibly unchecked"), so the common resume path is unprotected.

**Observable bad outcome.** After a corrupt/unreadable/forward-schema `.run-state.json` (or invalid productstate record in DB-authoritative mode), `sprint <p> <s> execute --resume` re-runs *all* tasks from scratch: completed work is redone in the target repo (double mutation/cost), evidence, attempts, session IDs, and deferral rationale are permanently overwritten, and the command reports nothing anomalous. In DB mode the authoritative record is likewise replaced by `saveExecuteStateDatabase`.

**Trigger/preconditions.** Resume + all-unchecked plan + state that fails `LoadExecuteRunState` (malformed file, `schemaVersion != 1`, permission/read error, DB record failing validation). Note the asymmetry proving intent: the checked-marker branch *does* hard-block with "checked tasks lack execution state", i.e., unreadable state during resume was already considered a blocking condition — the guard just doesn't cover unchecked plans. Startup reconcile surfaces `ErrExecuteRunStateMalformed` (locks.go:46-66, locks_test.go) but runs only from web startup (web_usecases.go:562-583), never on the CLI execute path.

**Contract conflict.** ultraplan-workspace sprint-23 requires malformed/unsupported schemas be "rejected with actionable diagnostics" (CURRENT-CONTRACT); this path silently does the opposite. Distinguishing `ErrExecuteRunStateMissing` (legitimately fresh) from Malformed/Unsupported/read-errors would close it.

**Regression test.** Seed a valid state with a complete task plus a corrupted `.run-state.json`, run `Execute` with `Resume: true` on an unchecked plan, assert a blocking finding and that no runtime call occurs / no file overwrite happens.

---

## F3 — Mid-stream session checkpointing races post-turn mutation and persistence when the runtime event pump outlives `StartRun`

**Severity: medium · Confidence: high on mechanism, medium on frequency**

**Claim.** The adapter returns from `StartRun` while its event-pump goroutine may still be delivering events into `req.OnEvent`: agentwrap closes `done` *before* `events` (LIFO defers in `run()`), and ultraplan's adapter abandons the pump after a 1-second grace on success (runtime.go:385-401) or 5 seconds on cancellation (:362-377). Execute's `OnEvent` closure (execute.go:253-268) mutates `task.Runtime`, `task.UpdatedAt`, `state.UpdatedAt` and calls `SaveExecuteRunState` — none of that synchronized against the main goroutine, which immediately mutates the same record (:275-284) and persists the terminal outcome (:314) once `startSprintRuntime` returns.

**Observable bad outcome.** A genuine Go data race (undefined behavior) on shared record fields; two concurrent atomic writers racing the rename of `.run-state.json` such that a late checkpoint can land over/near the terminal write; JSON marshaled from mid-mutation state can persist inconsistent snapshots; a late checkpoint failure sets `sessionSaveErr` after it was sampled exactly once (:270-272), silently lost. Reaching the window needs the drain+callback tail to exceed 1s — realistic because every event also performs a run-control DB append with retry (`RecordOperationEvent` → `appendRunEventWithRetry`) on the same goroutine; the cancellation path widens it to 5s+.

**Counter-evidence searched.** `sessionMu` serializes only OnEvent-vs-OnEvent and the single sample read; tests call `OnEvent` synchronously before returning (execute_state_test.go:35-37), so `-race` never sees the overlap.

**Regression test.** Fake runtime that spawns a goroutine invoking `req.OnEvent` after `StartRun` returns (sleep >0), run under `-race` with `Resume`-style loop; assert no race and that terminal status survives as the final persisted snapshot. Fix: hold `sessionMu` around all shared field writes and the terminal save, or make `StartRun` guarantee pump completion.

---

## F4 (minor) — Resumed execution after a target-directory change records stale provenance and publishes the new repo with old continuity

**Severity: low-medium · Confidence: medium**

execute.go:186 compares `state.Target.Path` to the current target *only* to gate session reuse; `reconcileExecuteState` preserves the old header target, execution proceeds with `WorkDir = target.Path` (:245), and `publishExecuteStage` commits `{Root: target.Path, All: true}`. If project-index's target moved mid-sprint (worktree created after a first raw-source run, or a genuinely different repo), remaining tasks execute and get wholesale-committed in the new location while `.run-state.json` still names the old one, and statuses carry over by ID across unrelated codebases. Mitigations exist (`validateSprintWorkspace` pins worktree↔source-root when `.workspace.json` is present), so this needs operator-level index edits; consequence is misleading provenance plus partial-sprint publication, hence minor.

---

## Defended / non-issues (actively refuted)

- **Publish retry idempotency**: suspected "commit succeeded, push timed out → rerun skips push". Refuted: `commitPaths` returns the parent commit on tree-equality and the push stage still runs (publisher.go:118-166), so resume-and-republish converges.
- **Persist-before-launch / terminal-before-advance ordering**: sound; sabotage tests prove no runtime launch after a failed running-save and failed classification on checkpoint-save errors (execute_state_test.go:233-282).
- **Stale-running recovery**: three consistent layers; the in-loop conversion (:203-207) is effectively unreachable after reconcile but harmless defense.
- **Deferral protocol**: rationale enforced at CLI parse (:1310-1315), marker extraction (execute_plan.go:77-84), state validation (execute_state.go:230-244), and read-back classification; stable-ID preservation under marker/model-annotation edits is test-asserted.
- **Containment**: `WorkDir = target.Path` direct assignment has no bypass path on this surface; unused-but-tested `ValidateExecuteWorkdir` is dead control, not a hole (sandbox enforcement owned by runtime policy config).
- **Exit-code classification** by substring (`strings.Contains(err, "failed tasks"/"runtime")`, sprint_commands.go:360-365) misfiles "persist runtime session…" checkpoint errors into ExitRuntime — brittle but bounded consequence; noted, not reported as a defect.
- **`PlanFingerprint`** is computed, persisted, and validated for charset, but never compared anywhere (only copied into the review handoff, execution_handoff.go:32). Mid-run drift is partially covered by per-turn deferral read-back revalidation; flagging as an observation, not a current-contract violation.