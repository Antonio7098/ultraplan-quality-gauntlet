# Context Pack: `sprint-execute-resume` — Sprint execute queue and resume

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen, clean tree).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: governed-sprint-delivery. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

Executes the top-level task checkboxes of a validated `plan.md` through a reusable agent session, with product-owned durability around every agent turn:

- **Queue execution**: one ordered pass over plan tasks; first turn gets shared sprint context + full queue; later turns are compact continuations in the same session when a reusable session ID exists (fallback: independent full prompts).
- **Persist-before-launch checkpointing**: each task is persisted as `running` before the runtime call, and its terminal outcome is persisted before the next task starts; session IDs observed mid-stream are also checkpointed.
- **Stop-on-failure discipline**: queue halts on the first failed or cancelled task.
- **Stale-running reconcile**: `running` records left by dead owners become `failed` with diagnostics at three distinct points (startup recovery, resume reconciliation, pre-execution check).
- **Deferral protocol**: CLI `--defer --reason` (operator) or an agent-authored plan marker `- [/] Task N: ... — Deferred: <reason>` (agent); both require rationale and produce durable `deferred` outcomes; the plan checkbox stays visibly unchecked.
- **Resume validation**: `--resume` allows checked/deferred plan markers only when the run state already records matching terminal outcomes per stable task ID.
- **Containment**: the agent's working directory is set to the resolved approved target (worktree preferred), with in-prompt safety constraints forbidding git mutation and out-of-scope artifacts.
- **Publication**: when every task resolves (complete|deferred), git publication commits the implementation worktree (`All=true`) then the workspace artifacts (`execute.md`, `.run-state.json`), config-gated.

Primary code: `internal/sprint/execute.go`, `execute_plan.go`, `execute_state.go`, `execute_target.go`, `execute_model.go`, plus `internal/sprint/publication.go:57-78` and `internal/app/git_publication.go`.

## 2. Entrypoints and control flow

### CLI dispatch (`internal/app/sprint_commands.go:329-368`)
- Flag parsing `parseSprintExecuteArgs` (1193-1233): `--task <id>`, `--dry-run|--prompt`, `--resume`, `--defer --reason <text>`, `--model <provider/model>`. Constraints: `--defer` requires `--task`; `--defer` cannot combine with `--dry-run`, `--resume`, or `--model`. Unknown flags → ExitUsage.
- Non-dry-run, non-defer runs are wrapped as durable operations: `beginDurableCLICommand(OperationExecuteStart, ...Task: req.TaskID)` (durable_operations.go:49-138 — run-control acceptance, owner claim with lease+fencing, running lifecycle event, heartbeat/cancel-watch goroutine). The command context becomes cancellation-aware. Service is rebuilt via `sprintRuntimeService` (685-711) with the real runtime, publisher (`stagePublisher`: nil publisher when `git.stage_completion=off`), stage runtime models, QA/smoke settings.
- Deferral runs `DeferExecuteTask` on the plain (non-runtime) service against `deps.ctx`; no durable wrapper.
- Error classification after render: findings present → ExitValidation; error contains "failed tasks" → ExitPartial; contains "runtime" → ExitRuntime; otherwise `mapSprintError`.
- Output rendering `renderSprintExecute` (1372-1402): project/sprint, dry-run prompt dump, result message, run-state/summary paths, per-task `id status attempts=N` lines, findings list. Help text at 1530-1531, 1571-1580.

### Web / TUI / flow entrypoints
- Web operation spec `"execute"|"execute-start"` maps to `OperationExecuteStatus | OperationExecuteDryRun | OperationExecuteResume | OperationExecuteStart` based on options (`internal/web/operation_handlers.go:646-666`). TUI exposes Execute Status/Dry Run/Start/Resume nav items (`internal/tui/model.go:480-483`).
- `internal/app/operation_runner.go:53-58`: execute-start/resume → `service.Execute(ctx, p, s, ExecuteRequest{TaskID, ModelOverride, Resume: kind==OperationExecuteResume})`; dry-run variant at `operations.go:443`.
- Flow integration (`internal/sprint/flow.go`): execute is the 8th cumulative stage; `runFlowStage` case StageExecute → `s.Execute(ctx, ..., ExecuteRequest{DryRun: req.DryRun, Resume: true})` (flow.go:241) — flow always resumes. Flow skips its generic planning-stage publication for execute (execute publishes itself, flow.go:142-155, 167) and uses `ExecuteComplete` for the skip check (flow.go:307-308 → verify.go:134-140).

### `Service.Execute` main loop (`internal/sprint/execute.go:129-349`)
1. Acquire mutation lock unless DryRun (`acquireMutationContext`, locks.go:112-126 — cross-process file lease shared by flow/execute/review/smoke/verify; reentrant via context marker).
2. `prepareExecute` (351-384): resolve sprint inputs → `planManifest` → `resolveSprintTarget(create=false)` → read plan artifact → `extractExecutePlanTasks(data, manifest, allowChecked=req.Resume)` → if Resume, `validateResolvedResumeTasks` → TaskID existence check → `executeModelSelection(override)` (override > planning.execute_model > planning.plan_model > runtime provider/model; unresolved model ⇒ finding). Any finding aborts before any write ("execute prerequisites failed validation").
3. Read planning inputs; `prepareSharedPromptContext(..., persistCache=true)` — code-context-derived shared prefix, cached context pack, fails closed when code-context exists but is invalid (`prompt_context.go:93-123`).
4. DryRun: composes the first (or selected) task's prompt and returns; no state writes, no runtime.
5. Fresh vs resumed state: `records := ExecuteTasksToRecords(tasks, now)`; `state := NewExecuteRunState(sp, target, planPath, PlanFingerprint(mustReadPlan), records, now)`. If `req.Resume` AND load succeeds → `state = reconcileExecuteState(existing, records, now)` (existing records substituted by ID into planned order; stale `running` → `failed` with "recovered stale running task on resume"). Load errors on resume are ignored (fresh state wins). Then unconditional `SaveExecuteRunState`.
6. `executionQueue := executeQueueFromState(state.Tasks, tasks, req.TaskID)` — planned tasks whose record is not complete/deferred, optionally narrowed to one ID.
7. Session reuse gate (186-193): only when `req.Resume` and `state.Target.Path == target.Path`; picks the last filtered record whose Runtime.Model equals the first queued task's effective model and has a SessionID (`reusableExecuteSession`, 563-574).
8. Per-task loop over `state.Tasks` (skips others when TaskID set):
   - Skip `complete`; `running` → `failed` + "stale-running ... before resume" diagnostic; proceed only if now pending|failed|cancelled.
   - Mark running, `Attempts++`, StartedAt; persist — failure aborts before launch ("persist running execute task %q").
   - Per-task model: inline plan annotation `<!-- model: provider/model -->` overrides batch selection (`executeSelectionForTask`, 739-744).
   - `continueSession := batchSessionID != "" && models match` → compact `RenderExecuteContinuationPrompt` with empty prefix and `SessionAction="continue"`; otherwise full `renderExecuteSessionPrompt` (RenderExecutePrompt + ordered queue section + direct-input packet: project definition inputs, sprint-index, technical-handbook, reasoning directory inputs, reasoning.md, plan.md).
   - Metadata: session_mode initial|continue|fresh-fallback, execution_turn, execution_queue_size, model_source, stage, task.
   - `runtimeRequest` (service.go:1122-1169): scoped runtime store owner/path under the sprint dir, PromptRef with sha256 checksum, trace ID, stable-prefix cache directive. `runtimeReq.WorkDir = target.Path` (execute.go:245). `OnEvent` wrapper checkpoints any new SessionID into run state under a mutex (save errors captured, surfaced post-run).
   - `startSprintRuntime` (runtime_metrics.go:116-125): `pruntime.CleanupRuntimeStores(sp.Path, 72h, 2GiB, false)` then `runtime.StartRun`; runtime metrics persisted separately (failure degrades to warning).
   - Post-run classification precedence (287-313): checkpoint save error → failed/"state-save-failed"; deferral read-back error → failed/"invalid-deferral"; deferral reason non-empty → deferred + rationale diagnostic; ctx cancelled → cancelled; runtime error → failed/"runtime-failed"; ≥1 artifact → complete + evidence entries (paths sanitized by `safeArtifactPath`); warning containing "diagnostic-only" → complete + "diagnostic-only-completion"; else failed/"missing-evidence".
   - Terminal state persisted ("persist terminal execute task"); checkpoint errors abort the whole command afterwards. Failed or cancelled breaks the loop; single-task mode always breaks.
   - On success, batchSessionID/batchSessionModel advance from the run result (or task.Runtime fallback).
9. `WriteExecuteSummary` → `projects/<p>/sprints/<s>/execute.md` (plain `os.WriteFile` 0644 inside contained path): counts per status, per-task attempts/evidence/diagnostics lines.
10. Any failed/cancelled ⇒ returns result plus error "execute completed with failed tasks".
11. Best-effort `_ = s.deleteCompletedSessions(...)` per runtime result (deletes session(s)/runtime store; flow.go:26-56).
12. If `allExecuteTasksResolved` (≥1 task, all complete|deferred) → `publishExecuteStage` (publication.go:57-78): implementation publish `{Root: target.Path, All: true}` then workspace publish of `execute.md` + `.run-state.json`; identical commit message "ultraplan: sprint <p>/<s> complete execute" with `UltraPlan-Publication:` identity trailer; skipped results filtered when publisher disabled.

### `DeferExecuteTask` (execute.go:29-79)
Trims reason; requires non-empty taskID+reason, ≤1000 chars, no `\x00\r\n`. Mutation lock; loads state; rejects deferring complete/deferred tasks; marks deferred (+CompletedAt, "deferred" diagnostic with redacted reason); saves state and summary; unknown ID errors. Does not modify plan.md.

### Resume validation (`validateResolvedResumeTasks`, execute.go:386-421)
When resuming and plan.md has `[x]` or `[/]` top-level markers: requires a loadable valid run state ("checked tasks lack execution state" otherwise); each resolved marker must map to matching status (`[x]`→complete, `[/]`→deferred) by task ID; mismatches become blocking findings.

### Target resolution (`ResolveExecuteTarget` / `resolveSprintTarget`, execute_target.go)
- Extract `**Target Implementation Directory:**` (bold or plain bullet, backtick-trimmed) from project-index.md; absolute or workspace-root-relative; must stat to an existing directory.
- If `.workspace.json` exists it wins (Source ".workspace.json"): validated against current source root (clean-path equality), worktree dir exists and is a dir, same git common dir as source, current branch equals recorded branch. Invalid → findings; unreadable/malformed → findings; absent + create=false → raw source target.
- Worktree creation happens at the code-context stage (`create=true` in flow.go:138), not here: `createSprintWorkspace` requires the source be a clean git worktree root (no uncommitted changes), creates `ultraplan/<project>/<slug>` branch at HEAD under sibling `.basename-ultraplan-worktrees/` dir, writes `.workspace.json` atomically (rollback removes worktree+branch if the record write fails).

### Publication mechanics (`internal/platform/gitpublish/publisher.go`)
Modes off|commit|commit-and-push. Resolves repo root + current branch (detached HEAD rejected), acquires `ultraplan-publish.lock` in the git common dir, builds a temporary index (`read-tree parent`, `add -A -- paths`, `write-tree`), skips commit when tree equals parent tree, else `commit-tree -p parent` + `update-ref <branch> commit parent` (CAS against concurrent branch movement) + index reconcile. Push honors upstream remote/ref, else pushes HEAD to configured remote with `--set-upstream`; `GIT_TERMINAL_PROMPT=0`, push timeout (default 2m). Path requests are containment-checked against the repo root.

## 3. Inputs / outputs

Inputs:
- argv flags; workspace root; cwd/env via workspace discovery.
- `project-index.md` `Project Scope > Target Implementation Directory` line (agent-writable planning artifact; selects both the agent workdir and the wholesale-commit root).
- `plan.md` content: top-level `- [ ] Task N: Name` checkboxes (bold-tolerant), nested `- [ ]` steps, `>` quote lines scanned for `Decision N` / `REQ-N-N` / `AC-N` refs, evidence heuristics (step text containing evidence/verification/test/check), `- [/]` deferral markers with `— Deferred:`/`- Deferred:` suffix reasons, inline `<!-- model: provider/model -->` annotations. Plan must pass `ValidatePlanContent` against the plan manifest first.
- Planning inputs packet (requirements/code-context/sprint-index/handbook/reasoning dirs/reasoning.md/plan.md) rendered into the first-turn prompt.
- `.run-state.json` or productstate DB record (schemaVersion 1) for resume/status; legacy schemaVersion-0 terminal files recognized read-only.
- `.workspace.json` worktree record; config keys (`planning.execute_model`, `planning.plan_model`, `models.primary/default`, `git.stage_completion|remote|push_timeout`, runtime config); live agent runtime.

Outputs:
- Human stdout render; exit codes (0 ok / 2 usage / validation-class / partial for failed tasks / runtime-class).
- Files written: `.run-state.json` (atomic temp+fsync+rename+dir-sync, JSON indent+newline), `execute.md` (plain truncate-write), runtime metrics file, disposable context-pack cache.
- Git commits and optional pushes in two repos (implementation worktree, workspace) when all tasks resolved and publishing enabled.
- Durable run-control events (accept/claim/lifecycle/heartbeat/terminal) for non-dry-run CLI executions.
- Runtime side effects delegated to the agent: file edits inside WorkDir=target.Path, test execution, etc.

## 4. Authoritative state

- **`.run-state.json` schemaVersion 1** (`domain.go:12,122-133`): header {schemaVersion, project, sprint, target{path,source}, planPath (contained relative), planFingerprint `sha256:` of whitespace-normalized lowercase plan text, createdAt, updatedAt} + ordered `tasks[]` {id `task-<12 hex>`, identity{name, planLine≥1, decisions[], requirements[], evidence[]}, status, attempts≥0, createdAt, updatedAt, startedAt?, completedAt?, diagnostics[], evidence[], runtime?}.
- Statuses: pending / running / complete / deferred / failed / cancelled; terminal = complete|deferred|failed|cancelled (`domain.go:63-70`, execute_state.go:271-273).
- **Validation contract** `ValidateExecuteRunState` (execute_state.go:184-268): strict schemaVersion==1; project/sprint identity match; safe contained planPath; fingerprint charset (no `\x00\r\n`); ≥1 task; unique IDs; safe identity text; deferred requires completedAt + a `deferred` diagnostic with non-blank rationale; running requires startedAt; terminal requires completedAt; diagnostics need code+message+time and safe charset; evidence needs kind+summary and optionally a safe relative path.
- **Dual-home behavior**: when the productstate DB is enabled, the DB record is authoritative — `Save` writes DB first, then mirrors the file only once every task is terminal (checkpoint semantics, execute_state.go:105-130); `Load` prefers the DB record (state_database.go:38-55). Otherwise the file is primary. Migration hooks exist (`MigrateExecuteStateToDatabase`).
- **Legacy compatibility**: schemaVersion-0 `{status: complete|failed|cancelled, files[], testsRun[], blockers[]}` files are historical evidence only (`LegacyTerminalExecuteStatus`), never resumable; `requireCompleteExecute` (verify.go:100-132) additionally accepts that legacy shape (status complete/completed with files[] plus non-empty execute.md) as completion evidence.
- **`.workspace.json`** (`execute_target.go:16-27`): {schemaVersion:1, sourceRoot, path, branch, baseline, createdAt}; created at code-context stage; validated on every execute target resolution.
- **`execute.md`**: derived human summary; plain WriteFile (not atomic); regenerated after every run/deferral.
- **`plan.md`**: user-owned input; execute never writes it. The *agent* may edit markers during a turn; execute re-reads it after each turn (`agentDeferredTaskReason`, 423-450: full manifest + extraction revalidation, returning the marker reason only for the exact task). The recorded `PlanFingerprint` is captured once at state creation (via `mustReadPlan`, which swallows read errors) and is not re-verified later in the loop.
- Note (descriptive): a resume attempt with unchecked plan tasks but an unreadable/malformed `.run-state.json` passes prepareExecute (no resolved markers ⇒ no findings; load error ignored at execute.go:177) and proceeds with fresh state, overwriting the prior file on first save.

## 5. Invariants (as implemented)

- No runtime launch before the `running` transition is durably saved; no queue advance before the terminal outcome is saved; mid-stream session-ID events are also checkpointed.
- Queue stops at the first failed or cancelled task; completed/deferred tasks are never re-executed within a run.
- Resume reconciliation matches strictly by deterministic ID (normalized name+decisions+requirements+evidence hash); renamed/edited tasks get new IDs and their old records drop out of the reconciled list; deferral markers and model annotations deliberately do not change IDs (test-asserted).
- Stale-running recovery exists in three layers: `ReconcileInterruptedMutation` (startup, code "recovery-interrupted"), `reconcileExecuteState` (resume), and the in-loop pre-execution check ("before resume").
- Deferral requires rationale at every layer: CLI flag validation, plan-marker regex requiring a reason, state-validation rationale diagnostic, and `invalid-deferral` classification when the read-back fails.
- Completion gating: success requires ≥1 runtime artifact or an explicit "diagnostic-only" warning; otherwise failed/"missing-evidence".
- Agent workdir is exactly the resolved target path (worktree preferred when valid); evidence paths stored in state must be safe relative paths or dropped.
- Publication happens only when all tasks are terminal-resolved (complete|deferred); implementation repo commits before workspace artifacts; both silently skipped when publishing is off.
- All persisted messages/diagnostics route through redaction/truncation helpers (`safeExecuteText` → `config.RedactValue`, ≤180 chars via `safeError`).
- Session reuse additionally requires unchanged target path and equal effective model; otherwise fresh full prompts ("fresh-fallback").

## 6. Trust boundaries

- **plan.md as control input**: checkbox syntax, task names, decision/requirement references, deferral reasons, and `<!-- model: ... -->` annotations are parsed from an agent-writable artifact. The annotation string flows into runtime model selection without charset restriction beyond the annotation regex `[A-Za-z0-9._~/-]+`; marker deferral reasons are captured by regex without length limits at parse time (charset-checked later at state validation; redacted at persistence).
- **project-index.md** decides which directory the agent may mutate and which repository gets committed wholesale (`All=true`) at publication.
- **Agent output → durable state**: artifacts/warnings/session IDs from the runtime become state records; evidence URI must pass `safeRelPath` else stored empty; all free-text redacted.
- **Agent constraints are prompt-level** (`ExecuteSafetyInstructions`, execute_target.go:220-227): stay inside approved target; do not create smoke/review/issues artifacts; do not run/request git mutation; no cross-sprint/TUI/hosted work. Technical enforcement of sandbox/permissions belongs to the opencode-agent-runtime surface (configured sandbox/permission policy travels on the runtime request).
- **gitpublish** executes git with sanitized env (`GIT_TERMINAL_PROMPT=0`, LC_ALL=C), validates remote names (no leading dash, restricted charset), and CAS-updates the branch ref.
- **Mutation serialization**: cross-process sprint mutation lease (file lock) plus durable run-control owner fencing serialize execute against flow/review/smoke/verify and other owners.

## 7. External effects & lifecycle semantics

- Mutates the implementation repo (through the delegated agent), can commit and push it, commits workspace artifacts, deletes completed sessions/runtime stores best-effort, prunes stale runtime stores (>72h, >2GiB).
- **Cancellation**: durable-context cancel during a turn classifies the task cancelled (+diagnostic), breaks the queue, persists, returns result with error; run-control finish records cancelled. The in-flight runtime call receives the same ctx.
- **Crash/restart**: orphaned `running` records recover to failed on startup reconcile or next resume; cleanup-uncertain marker forces explicit operator handling instead of silent reconcile (locks.go:40-44,101-108); legacy terminal states remain readable.
- **Retry**: rerunning without `--resume` rebuilds fresh state (prior file replaced on first save); failed/cancelled/pending tasks restart with Attempts++; `--resume` keeps complete/deferred history and continues the remaining queue, reusing the session when target and model still match.
- **Partial failure**: mixed-status state persisted, execute.md reflects counts, command exits ExitPartial-class with "execute completed with failed tasks".
- Publication failure after full resolution surfaces as command error while leaving all tasks resolved and state saved (commit may exist without push; push timeout bounded).

## 8. Immediate surface dependencies

- **sprint-planning-chain**: `resolveSprintInputs`, `planManifest`, `ValidatePlanContent`, plan artifact reads, direct-input packet composition, shared prompt prefix/context pack.
- **sprint-flow-state**: flow dispatches execute (always Resume:true), skips its own publish step for execute, gates skip-checks via `ExecuteComplete`; execute itself does not write flow-state.json.
- **opencode-agent-runtime** (`internal/platform/runtime`): Request/Result/Event contract — SessionID/SessionAction continuation, OnEvent stream, Artifacts/Warnings driving completion classification, RuntimeStore scoping/cleanup, usage/metrics summaries.
- **repo-publication** (`internal/platform/gitpublish`): policy modes, repo lock, temp-index commit strategy, push semantics.
- **product-state-mirror** (`internal/productstate`): authoritative DB record {header, items keyed by task ID with ordinals} behind `ExecuteStateInDatabase`.
- **run-control / durable operations**: acceptance, claim, fencing, heartbeats, cancellation watch, terminal proposals wrapping every non-dry-run CLI execute.
- **workspace scaffold**: embedded `prompts/execute-sprint.md` documents the superseded agent-authored execution mode (writes legacy schemaVersion-0-style `.run-state.json` itself); still materialisable via `defaults install`.

## 9. Contracts (CURRENT-CONTRACT evidence)

From target-repo `docs/cli-reference.md` (~L306-330, execute section):
- One reusable agent session; first turn carries shared context + ordered queue; later tasks are compact continuations; UltraPlan checkpoints each task before advancing; stops on failure/cancellation; writes `.run-state.json` and `execute.md`; requires runtime evidence or a safe diagnostic before marking complete; constrains work to the project-index target; falls back to complete independent prompts when no reusable session ID is supplied.
- Deferral: `--task <id> --defer --reason "<rationale>"` is durable and terminal, shown in execute.md and status counts; plan checkbox remains unchecked; review accepts deferrals only with a matching governed deferred outcome; arbitrary unchecked/manually checked tasks cannot bypass execution; `[/]` marker without `— Deferred: <reason>` is invalid.

From ultraplan-workspace sprint 23 (`projects/ultraplan-go/sprints/23-execute-stage/requirements.md` ~L22-49, L104):
- Versioned, atomic, strictly-loaded, resumable `.run-state.json` recording statuses with attempts, timestamps, diagnostics, runtime metadata summaries; stale running tasks recovered to retryable-or-failed with diagnostics without redoing complete tasks; sprint status reflects execute readiness without runtime calls; execute.md cites plan.md and records counts/evidence/diagnostics; same-directory atomic writes; malformed/unsupported schemas rejected with actionable diagnostics.
- Neutral divergence fact: sprint-23 enumerates five statuses (pending/running/complete/failed/cancelled); the implementation adds `deferred` as a sixth, documented instead in cli-reference.md.

From ultraplan-workspace PRD (~L223, L576, L605): controlled execution from validated plan.md tasks is in scope; automatic Git-mutation *commands* and issue tracking remain deferred product non-goals (config-gated publication of completed work is implemented separately from the agent-side git ban).

In-prompt contract (`ExecuteSafetyInstructions` + RenderExecutePrompt tail, execute.go:471-477): verifiable evidence or explicit safe diagnostic required; `[/]` + `— Deferred: <reason>` protocol; never mark deferred work complete.

HISTORY (context only): scaffold prompt `prompts/execute-sprint.md` describes agents writing their own legacy-shape `.run-state.json`; retained for materialisation compatibility and read back only by `LegacyTerminal*`/verify legacy paths.

## 10. Tests (evidence map)

`internal/sprint`:
- `execute_plan_test.go`: happy-path extraction (traceability refs, evidence heuristics); deterministic stable IDs across whitespace normalization and re-id on identity change; rejection matrix (unsupported syntax, floating nested item, duplicate IDs, all-checked plan); resume-mode keeps checked parents; `[/]` requires reason and preserves stable ID; model annotation parses, stays out of the name, preserves ID; `executeSelectionForTask` annotation precedence; `ValidateExecute` end-to-end validity incl. rejecting fully-checked plans.
- `execute_state_test.go`: strict load/save round-trip; atomic write preserves prior file when rename hook blocks; 12-case validation failure table (schema, mismatch, unsafe paths, missing fields/timestamps, unsafe diagnostic/evidence, duplicate IDs, negative attempts); ErrMissing/ErrMalformed classification; legacy terminal state loads as historical only; deferred-without-rationale invalid and not counted failed; `DeferExecuteTask` persists rationale + summary; running-persist failure prevents any runtime call; session-checkpoint persist failure yields failed/state-save-failed task plus command error.
- `execute_target_test.go`: absolute + workspace-relative target resolution; missing/unavailable targets; `ValidateExecuteWorkdir` containment matrix (inside ok, outside/relative rejected); safety-instruction content.
- `execute_model_test.go`: full selection precedence chain including no-model error.
- `efficiency_improvements_test.go`: TestExecuteUsesOneSessionWithCompactPerTaskContinuations (two tasks → two requests; request 1 initial w/ queue size 2 and shared context; request 2 continue on recorded session with compact prompt; both tasks retain the shared session); TestExecuteFallsBackToFullPromptsWithoutReusableRuntimeSession (fresh-fallback full prompts); TestExecuteStopsSharedQueueAfterTaskFailure (one runtime call; task 2 stays pending; command errors).
- `publication_test.go`: TestPublishExecuteUsesTargetBeforeWorkspace (target-root All=true request precedes workspace paths request).
- `locks_test.go` (reconcile path): malformed run-state surfaces ErrExecuteRunStateMalformed.
- `prompt_context_test.go`, `efficiency_metrics_test.go`: PromptExecute composition, handbook injection, cache-directive metadata.

Test-topology facts (neutral, for reviewer prioritization): the `--resume` branch internals — `reconcileExecuteState`, `validateResolvedResumeTasks`, `reusableExecuteSession`, the target-changed reuse guard (execute.go:186), and stale-running-on-resume — have no direct test hits (`Resume: true` appears only in production flow.go and app wiring; no sprint-package test constructs a resume run). `ValidateExecuteWorkdir` is unit-tested but no production caller was found; containment at the runtime boundary currently comes from direct assignment `runtimeReq.WorkDir = target.Path` (execute.go:245). Baseline `go test ./...` green at frozen commit.

## 11. Explicit unknowns / open questions

1. Intended enforcement point for workdir containment: `ValidateExecuteWorkdir` exists and is tested but unused in production; whether construction-by-assignment (`WorkDir = target.Path`) is the deliberate sole mechanism is undocumented.
2. OpenCode adapter semantics for `SessionAction="continue"` and cross-process session reuse (whether a recorded SessionID remains continuable after process exit; whether OnEvent-reported SessionIDs can differ from the final Result.SessionID) — owned by the opencode-agent-runtime surface.
3. productstate enablement conditions and migration timing (when `.run-state.json` flips from primary to mirror-once-terminal) — owned by product-state-mirror surface.
4. Whether the single PlanFingerprint captured at state creation is meant to detect plan drift during long multi-task runs (it is not re-checked before publication); the deferral read-back revalidates structure per turn, but fingerprint staleness semantics are unspecified.
5. Concurrency between execute and manual user edits of plan.md mid-turn beyond the mutation lease (lease serializes product mutators, not human editors); partial coverage exists via `agentDeferredTaskReason` revalidation failures classifying tasks as invalid-deferral.
6. Windows support depth: atomic rename/fsync/dir-sync assumptions in `saveExecuteRunStateWithHooks` and lock behavior appear POSIX-oriented; no platform-specific tests found.
7. Whether the agent-authored legacy execution mode (scaffold prompt writing its own `.run-state.json`) is still a supported workflow or purely historical compatibility.
8. Push credential expectations for `commit-and-push` (auth assumed pre-configured; interactive prompting disabled) and behavior when upstream ref configuration is inconsistent.
