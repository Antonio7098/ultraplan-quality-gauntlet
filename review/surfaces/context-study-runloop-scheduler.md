# Context Pack: `study-runloop-scheduler` — Study durable run-loop scheduler

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen, clean).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: study-analysis. Risk: critical. Descriptive only — no defect judgments.

## 1. Purpose

`study run-loop` is the long-lived, resumable orchestrator for a study's task graph (per-dimension × per-source analysis tasks plus one synthesis task per dimension). This surface owns:

1. **Durable scheduling**: bounded worker slots refilled one-at-a-time as slots free (explicitly not a batch barrier), priority tiers by study dimension order with backfill of lower tiers, and synthesis gating on dependency completion.
2. **Admission control under resource pressure**: memory-pressure parallelism throttle with hysteresis (`/proc/meminfo`), disk-pressure admission pause + per-worker disk-headroom cap (`statfs`), and runtime-store GC (`CleanupRuntimeStores`) at startup and during pressure.
3. **Retry taxonomy**: terminal statuses mapped from runtime results; retryable categories (`rate_limit`, `timeout`, `provider_unavailable`, `runtime_unavailable`) get persisted `retry_after` derived from agent policy/attempt metadata or fixed defaults; scheduler sleeps until the earliest retry time.
4. **Atomic persistence**: every task transition persisted to run-state via debounced forced/coalesced atomic saves (temp+fsync+rename+dir-sync), with DB-authoritative mirroring when productstate is enabled.
5. **Resume correctness**: reconcile rebuilt task graphs against prior state, re-validate completed artifacts before trusting them on resume, restore completions from history when reconciliation transiently reopened tasks, and archive-and-rebuild on `--reset`.
6. **Single-mutator locking**: per-study O_EXCL pidfile with PID liveness, conservative stale handling, ownership-checked release, `--force-unlock`, and a cross-process SIGINT cancel lane (`CancelRunLoop`).
7. **Fail-closed interrupted-run reconciliation**: active task states left without a live owner become explicit cancelled evidence; a `cleanup-uncertain.json` marker forces failure rather than silent success when there is nothing to reconcile.
8. **Append-only history ledger**: `runs/tasks.jsonl` records each terminal transition exactly once per dedup key, crash-tolerant for a trailing partial line only, plus regenerated `runs/summary.md`.
9. **Resource diagnostics**: periodic JSONL samples of heap/RSS/children/disk/store inventory with size-capped rotation.

## 2. Entrypoints and control flow

### 2.1 CLI entry — `ultraplan study <study> run-loop`
- Dispatch at app/study_commands.go:48 → `runStudyRunLoop` (:197): `--help`; `parseRunLoopArgs` (:241) extracts `--force-unlock`, `--continue`, `--reset`, `--yes/-y`, remainder parsed by `parseRunAllArgs` (`--dimension`, `--source`, `--parallel N`, `--model`).
- `flags.reset` triggers `confirmRunLoopReplacement` (:277): stats `run-state.json`; unless `--yes`, prints counts and requires interactive "yes"/"y".
- `beginDurableCLICommand` (app/durable_operations.go:49) accepts the operation in run-control (`OperationStudyResume`) and returns a cancellation-aware context (SIGINT/SIGTERM arrive via signal.NotifyContext in cmd/ultraplan/main.go:19 into deps.ctx, wrapped by run-control claim/fence; `controlOperation` :223 additionally polls the repository for requested cancellation and heartbeats).
- `runLoopService` (:318) resolves effective config (default parallel from config, override validated ≥1), builds the controlled runtime and stage publisher; `RunLoopRequest.Command` is recorded as `"ultraplan study <ref> run-loop <args…>"` (:219) for the lock file.
- `service.RunLoop(durable.Context(), req)` runs; progress rendered by `renderRunLoopProgress` (:397); errors classified by `mapStudyRunLoopError` (:354): `ErrStudyLocked`→ExitPartial, `ErrRunStateMalformed/Unsupported`→ExitValidation, else `mapStudyExecutionError`. Result status maps through `classifyRunAllResult`. `finishDurableCLICommand` records the run-control terminal outcome (cancelled/failed/succeeded).

### 2.2 Service entry — `Service.RunLoop` (internal/study/run_loop.go:23)
Sequence:
1. Validate `Parallelism >= 1`; `ListStudy` resolves study/sources/dimensions/config; `AcquireRunLoopLock(listing.Study, req.Command, req.ForceUnlock, now)` (:31) — deferred release whose error overrides a nil run error (:35-39).
2. `resolveDimensions`/`resolveSources` validate filter refs.
3. Diagnostics initialized (:50) with runID set after state load (:58); startup runtime-store cleanup `CleanupRuntimeStores(study.Path, 72h, 2GiB, aggressive=false)` (:60) recorded in diagnostics.
4. `loadOrCreateRunLoopState` (:536): non-reset loads existing state (`LoadRunState`); missing state or reset → `archiveRunStateIfExists` (:559 renames `run-state.json` into `.ultraplan/archive/run-state-<UTC-timestamp>.json`) then `NewRunState` builds the deterministic applicable task graph (run_state.go:26,102) with applicability fingerprint (sha256 of dimension/source enumeration).
5. `LoadRunHistory` (:64); then three resume passes at `now`:
   - `ReconcileRunState` (run_state.go:67): rebuilds current graph, preserves prior per-ID task state while refreshing dimension/source/output-path/dependency projections from current inputs; a completed synthesis whose dependency set changed is reopened to pending (cleared CompletedAt/Validation/LastError).
   - `ResumeValidateRunState` (run_state.go:277): running/validating/waiting/cancelled → pending; retrying with elapsed/future RetryAfter handled; failed with future RetryAfter promoted back to retrying; **completed** tasks are revalidated — analysis against source report validator, synthesis against final report validator, with unknown source/dimension references failed as `validation.reference`; failures become `validation.report`.
   - `RestoreCompletedRunHistory` (skipped on Reset; run_state.go:366): pending tasks whose last history record is completed+passed AND whose artifact currently validates are restored to completed (CompletedAt taken from the record).
6. Initial `SaveRunState` (:75), `SyncRunHistory` (:80), `readRunHistoryKeys` (:83) seed the dedup cache.
7. Scheduler loop (:344-449):
   - First recorded error sets `stopScheduling` (no new claims; actives drain).
   - Memory pressure read each iteration (:348); `effectiveParallelism` moves ±1 toward requested based on Stretched/Recovered hysteresis; transitions emit throttled/restored progress and diagnostics events.
   - Disk pressure (:375): if pressured → runtime-store cleanup (aggressive iff critical or no active workers), re-read; still pressured → admission paused: if workers active wait on `<-done`, else sleep ≤30s on ctx-aware timer; message + diagnostics sample emitted.
   - `diskParallelismCap` (disk_pressure.go:21): available ≤1.5GiB ⇒ cap 0 (admission pause territory); otherwise `(avail − 1.5GiB)/512MiB` clamped [1..requested]; lowering emits throttled progress.
   - `runnableTaskIDs` (:600): iterate rank = dimension-order position (unknown dims share last rank), within rank synthesis before analysis; skip out-of-scope, attempted-but-not-retrying (`taskAttemptBlocked` :686), non-runnable statuses/pending retries (`taskRunnable` :690); synthesis additionally requires all dependencies complete OR all dependencies terminal (:625). Limit = `effectiveParallelism − active`.
   - Each claimed id: marked attempted, `active++`, Started event emitted at claim time (:428), goroutine spawned (:429). Slot refill: `<-done` decrements immediately after any completion (:434-439) — no batch barrier.
   - No runnable ids and no future retry ⇒ break (:441-443); otherwise `waitUntilRetry` (:481) sleeps in ≤1min slices until earliest `retry_after`, emitting waiting summaries each slice (:503).
8. `runTask` (:264): pre-checked `ctx.Err()` marks the claimed-but-unstarted task cancelled (`workflow.cancelled`) and records history; else snapshot task + clone state; dependency gate — incomplete+terminal deps ⇒ `synthesis.dependencies_failed` failed; incomplete+non-terminal ⇒ waiting; else claim update (status running, Attempts++, StartedAt=now, clears LastError/RetryAfter/Session/CompletedAt).
   - Dispatch `RunAnalysis`/`Synthesize` (run.go:19 / synthesize.go:8) with `DeferPublication=true`, `ResumeSession=task.Session`, `OnSession=checkpointSession` (persists session checkpoints mid-run, run_loop.go:307-313), `OnEvent` forwarding non-message/session/native_extension events (:591). Runtime errors convert to `ExecutionStatusRuntimeFailed` results (:319-321,327).
   - `applyExecutionResult` (:779): completed/skipped ⇒ completed (clear Session/RetryAfter); cancelled ⇒ cancelled; validation_failed/preflight_blocked ⇒ failed (`validation.failed`); other categories retry if `executionShouldRetry` (:841) with `RetryAfter` = min over agent policy decisions/attempts durations (runtime_metadata.go:171) else defaults 10m rate-limit / 2m others (:850). Agent metadata (provider/model/attempts/usage/cost/warnings/omissions) merged into TaskState (runtime_metadata.go:10).
   - Terminal result appended to history (`recordHistory` :210 clones state under mu, serializes under historyMu with shared key cache); completed results enqueued to buffered `publicationJobs` channel consumed by one publisher goroutine (:249-262); progress event per outcome mapping (:575).
9. Drain publications (close jobs channel, wait done) (:450-453). If firstErr non-nil ⇒ return early with populated result (:454-456) — final save/history-sync/publication are skipped on this path.
10. Final: `Complete=allTasksComplete` (:458), forced `save()` (:459), `SyncRunHistory` (:462), optional `publishRunLoopState` (publication.go:45 publishes run-state/tasks.jsonl/summary.md paths), compute Counts/ScopeCounts/Status (:474-477): cancelled if ctx cancelled or any cancelled task; completed if no failed/pending/skipped; partial if any completed; else validation_failed (:971).

### 2.3 Sibling entry — `Service.RunAll` (run_all.go:16)
Non-durable batch variant used by `<study> run-all`: per dimension-priority group, bounded workers drain an unbuffered task channel (batch barrier between groups), synthesis pass per dimension gated by `synthesisBlockers` (all analysis completed), summary.csv write, publication of summary on full success. No lock, no persisted state, no retries.

### 2.4 Cancel lane
- Same-process: SIGINT/SIGTERM → NotifyContext → durable-op context cancellation propagates into RunLoop's ctx; claimed-but-unstarted tasks cancelled, active runtime calls cancelled through the platform runtime boundary, loop drains, final save runs (unless firstErr path).
- Cross-process: `CancelRunLoop` (locks.go:141) reads the lock, requires alive PID, matching study name, and non-self PID, then `syscall.Kill(pid, SIGINT)`. Invoked from web/TUI operation `study-cancel` (app/operation_runner.go:152-162) and surfaced in dashboards via `RunLoopActive` (locks.go:131; app/study_usecases.go:130).
- Status surfaces read the lock without signaling: `LockInfoForStatus` (run_loop.go:1013) in `study status` (app/study_commands.go:1135).

### 2.5 Interrupted-run reconciliation
- Web server startup calls `ReconcileOperations` over all studies (app/web_usecases.go:573-592) → `ReconcileInterruptedRun` (cleanup_uncertain.go:66): acquires the same run-loop lock (live owner ⇒ `(false,nil)` no-op), probes `cleanup-uncertain.json` (invalid marker aborts fail-closed), converts running/validating/waiting/retrying tasks to cancelled with `workflow.interrupted`, saves + syncs history when changed; marker removed only when changed; marker present with nothing to reconcile ⇒ `ErrCleanupUncertain` retained (fail closed). Run-state missing + marker ⇒ `ErrCleanupUncertain`; missing without marker ⇒ no-op.
- Marker producer: web shutdown deadline exhaustion → `RecordOperationCleanupUncertain` (app/web_usecases.go:543 routes study branch) → `RecordCleanupUncertain` (cleanup_uncertain.go:31): deliberately does NOT take the run-loop lock; SchemaVersion=1, OwnerPID=self, Reason must equal `"server_shutdown"`, OperationID/Kind required without NUL/CR/LF; atomic write.

## 3. Inputs / outputs

Inputs: workspace tree (studies/<s>/{sources,dimensions} discovery, study.yml config, dimension order), effective workspace config (runtime/model/parallel defaults; `ULTRAPLAN_STUDY_MODEL` env override), persisted run-state (file or productstate DB row) re-read as trusted input, run-loop.lock JSON contents, cleanup-uncertain.json, runs/tasks.jsonl history, `/proc/meminfo`, `/proc/self/status`, `/proc/self/task/<pid>/children`, `/proc/<pid>/{cmdline,stat,status}`, `/proc/uptime`, statfs on the study path, wall clock, os.Getpid(), CLI flags/env.

Outputs: replaced `studies/<s>/.ultraplan/run-state.json` (atomic rename; or DB row + completion-time file checkpoint); `archive/run-state-<ts>.json` on reset; created/removed `run-loop.lock`; created/removed `cleanup-uncertain.json`; appended `runs/tasks.jsonl`; rewritten `runs/summary.md` (plain write); appended/rotated `diagnostics/run-loop-memory.jsonl(.1)`; report artifacts written by child runtime processes (source reports, final reports) then git-published (per-completion deferred queue + final run-state/history/summary publish); SIGINT delivered to another process; runtime-store directories created per task and GC'd; run-control operation records; CLI stdout/stderr progress lines and exit-code classes.

## 4. Authoritative state

- File authority: `studies/<s>/.ultraplan/run-state.json`, schema_version 1 (domain.go:4-6). Strict load grammar: sentinel errors `ErrRunStateMissing/Malformed/Unsupported`; validation covers schema version (0⇒malformed, ≠1⇒unsupported), run_id, study identity, timestamps, unique task IDs, kind/status enums, required fields (analysis needs dimension/source/sourceKind), session-checkpoint shape (sessionID ≤512 chars, no NUL/CR/LF, WorkDir/InputFingerprint required, ContinueFailures ≥0) (state.go:121-166).
- DB-authoritative mirror: productstate store kind `study_run`, scope `<study>` (state_database.go). Load prefers an existing DB record (header minus tasks + one item per task keyed by task.ID, ordinal order). Save routes to the DB whenever a record already exists there (`RunStateInDatabase`), writing the file too only when `state.Complete` (completion checkpoint). `MigrateRunStateToDatabase` imports file→DB. SQLite WAL/synchronous-FULL semantics live in the product-state-mirror surface.
- Ancillary state owned here: `run-loop.lock`, `cleanup-uncertain.json`, `archive/`, `runs/tasks.jsonl` + `runs/summary.md`, `diagnostics/run-loop-memory.jsonl(.1)`; runtime-store directories under `studies/<s>/.ultraplan/runtime-store/` are managed through platform/runtime helpers.
- Not authoritative here: generated report/final-report Markdown (written by runtime children; validated by this surface but never written), workspace planning artifacts, run-control journal (upstream of entrypoints).
- Related sibling authority: sprint execute `.run-state.json` belongs to the sprint surface; naming overlaps but scopes differ (study vs sprint directories).

## 5. Invariants (as implemented)

- One mutator per study: O_EXCL pidfile creation; unreadable lock fails closed; dead-PID lock removed once then retried; second conflict names the new owner; live PID blocks; release refuses on ownership tuple mismatch (PID+Study+AcquiredAt) (locks.go:34-123).
- Atomic persistence: canonical run-state changes only via fsynced temp + rename + best-effort dir sync; failed writes preserve prior bytes (test-pinned incl. injected rename failure).
- Transition persistence: `update()` bumps stateVersion and persists coalesced (≥250ms spacing, forced saves bypass); final forced save at loop end; SaveRunState validates every candidate state before writing.
- Resume safety chain: reconcile → resume-validate → history-restore, always followed by an immediate save before scheduling; completed artifacts never trusted without current validation pass; arbitrary stale files never adopted without a matching history record.
- History append-only + deduped: only terminal statuses with CompletedAt recorded; key `runID|taskID|attempts|completedAtRFC3339Nano`; whole-file rewrite via temp+fsync+rename after trimming at most one invalid trailing record; earlier malformed records hard-fail reads (run_history.go:239-272).
- Bounded concurrency: goroutine count capped by effectiveParallelism (≥1); disk-pressure admission pause; memory throttle floor 1.
- Dependency gate: synthesis scheduled only when all dependency analyses completed, or explicitly failed `synthesis.dependencies_failed` when dependencies reached terminal states without completing.
- Cancellation discipline: unscheduled tasks are not rewritten on cancel (test-pinned); claimed-but-unstarted ones become explicit cancellations; active work cancels via the runtime boundary.
- Scope isolation: filtered runs mutate only in-scope task rows; scope excludes synthesis when a source filter selects a strict subset of applicable sources (run_loop.go:650-666).
- Fail-closed uncertainty: malformed uncertainty markers abort reconciliation; markers survive no-op reconciles.
- Redaction at persistence boundaries: lock command sanitizes token/key/secret-shaped args and truncates >120 chars; task error messages pass through compactDiagnostic + config.RedactValue at render time.

## 6. Trust boundaries

- Persisted run-state (and its DB mirror) is re-read as authoritative input on every resume: statuses, attempts counters, RetryAfter timestamps, LastError text, and Task.Session checkpoints (session ID, provider/model, WorkDir, InputFingerprint) drive scheduling and runtime continuation decisions. Load-time checks are structural (schema/enum/shape), not semantic.
- Lock file contents drive liveness decisions (`kill(pid,0)`, EPERM counted alive) and cross-process SIGINT delivery (`CancelRunLoop` signals whatever PID the file names, guarded only by study-name match, self-check, and kill permission). The lock file is world-readable 0644 inside a 0755 directory tree. Release compares PID+Study+AcquiredAt only.
- cleanup-uncertain.json is written without the lock by design (shutdown-deadline contention); consumers treat malformed markers as hard errors. OwnerPID is recorded but never cross-checked.
- The history ledger feeds RestoreCompletedRunHistory; combined with artifact validators it gates which tasks are trusted complete after transient reopenings.
- Command-line material enters the durable record via sanitizeCommand (heuristic lowercase substring redaction of token/key/secret; 120-char truncation) and is displayed by status surfaces.
- Diagnostics files capture child PIDs, RSS/CPU, and cmdline-substring-derived task attribution, written world-readable; they feed web/TUI resource panels (`StudyResources`, ParallelismThrottle summaries).
- Runtime-store GC deletes directories under the study's `.ultraplan` tree based on recorded owner PID/state/age; stale-active stores (>30min dead owner) are downgraded to retained rather than deleted.
- Platform assumption: `/proc` parsing and `syscall.Kill`/`Statfs` presume Linux/unix; no build tags exist.

## 7. External effects & lifecycle semantics

- Effects confined to the workspace tree, productstate DB, run-control journal, child runtime processes (each with isolated runtime store), git publications, and signals to the lock-owner PID.
- Crash/restart story: dead-owner locks stolen conservatively on next acquire; orphaned active task states converted to explicit cancellations either by the next RunLoop's resume chain (pending again, history restored if artifacts valid) or by web-startup reconciliation (terminal cancelled evidence); partial trailing history lines tolerated exactly once.
- Cancellation lanes: (a) own-process signal → context; (b) another process's `study cancel` → SIGINT to owner; (c) run-control requested cancellation polled by the durable-operation controller; (d) `--force-unlock` bypasses the lock entirely (unconditional remove before acquisition).
- Retry semantics: within one invocation a non-retrying terminal outcome is not re-claimed (attempted-map); Retrying tasks re-enter scheduling after RetryAfter; across invocations Failed tasks with elapsed RetryAfter return to pending; attempts counter persists and feeds history dedup keys and retry summaries.
- Error surfacing: first worker/scheduling/publication error stops new admissions and short-circuits the epilogue (final save/sync/publish skipped when returning early); lock-release errors override nil results; write failures are loud per contract.
- Publication ordering: per-execution publications are serialized by a single consumer goroutine and deferred past state save; the final run-state/history/summary publish happens once at the end.

## 8. Immediate surface dependencies

- `study-task-execution` (internal/study run.go, synthesize.go, prompts.go, validation.go): prompt composition, runtime invocation, artifact validators, edit-warning snapshots, session fingerprint compatibility (`studySessionCompatible` run.go:251), DeferPublication/DeferSessionCleanup flags consumed here.
- `opencode-agent-runtime` (internal/platform/runtime): StartRun boundary, per-task isolated RuntimeStorePath, Event stream (filtered forwarding), CleanupRuntimeStores/InspectRuntimeStores (store.go:172-237: stale-active→retained, 72h expiry, 2GiB quota, aggressive mode).
- `product-state-mirror` (internal/productstate): kind `study_run` authority selection, Load/Save/Has/Ensure.
- `repo-publication` (internal/platform/gitpublish): Publisher.Publish for reports (deferred queue) and run-state/history/summary (end-of-run).
- `run-control` (internal/runcontrol via internal/app durable_operations.go): operation acceptance, fencing, heartbeat, cancellation polling upstream of the context RunLoop receives.
- Presentation consumers: TUI/web dashboards (active-run indicator, resources panel, ParallelismThrottle), `study status`/`study runs` commands, README/help contract strings.

## 9. Files / symbols

| Path | Role |
| --- | --- |
| internal/study/run_loop.go | RunLoop scheduler (:23), persist/update closures (:149-208), runTask (:264), runnableTaskIDs/tiering (:600), applyExecutionResult/retry taxonomy (:779-855), status derivation (:945-991), LockInfoForStatus (:1013) |
| internal/study/run_state.go | NewRunState/graph builder (:26,:102), ReconcileRunState (:67), ResumeValidateRunState (:277), RestoreCompletedRunHistory (:366), task-ID grammar (:447-468) |
| internal/study/state.go | Load/SaveRunState (:27,:59), atomic writer with hooks (:73), ValidateRunState (:121), syncDir (:177), legacy 64MiB GC threshold (:15) |
| internal/study/state_database.go | productstate routing for kind `study_run` (:17,:43,:70,:78) |
| internal/study/locks.go | ErrStudyLocked (:15), processAlive (:17), AcquireRunLoopLock (:34), Release (:105), RunLoopActive (:131), CancelRunLoop (:141), ForceUnlockRunLoop (:161), sanitizeCommand (:184) |
| internal/study/cleanup_uncertain.go | RecordCleanupUncertain (:31), ReconcileInterruptedRun (:66), reconcileInterruptedRunLocked (:100), marker load/remove (:156,:172) |
| internal/study/run_history.go | Record schema (:22), append/dedupe (:76-114), trailing-record trim (:116), atomic rewrite (:128), SyncRunHistory (:150), readers (:226-300), key format (:306) |
| internal/study/run_history_summary.go | summary.md render/write (non-atomic os.WriteFile, :12-23) |
| internal/study/memory_pressure.go | /proc/meminfo thresholds: stretch max(15%,1GiB), recover max(25%,1.5GiB) (:41-49) |
| internal/study/disk_pressure.go | constants 1.5GiB/768MiB/512MiB (:6-9), diskParallelismCap (:21), statfs reader (:38) |
| internal/study/run_loop_diagnostics.go | sampler lifecycle (:98), sample fields (:126), 5s interval/8MiB rotation (:19-22,:228-244), SummarizeParallelismThrottle (:401), LoadRunLoopResourceHistory (:422) |
| internal/study/domain.go, run_state_domain.go, execution_domain.go | constants, TaskStatus enum (9 statuses), RunState/TaskState/TaskSession shapes, RunLoopRequest (incl. Continue field :137)/Result/Progress types |
| internal/study/durable_metadata.go, runtime_metadata.go | AgentMetadata clone/compact, agentMetadata projection, retryAfterFromAgent (:171) |
| internal/app/study_commands.go | runStudyRunLoop (:197), flag parsing (:241), reset confirmation (:277), service wiring (:318), error classes (:354), progress rendering (:397), status lock read (:1135), help contract (:645+) |
| internal/app/durable_operations.go | beginDurableCLICommand (:49), Finish terminal classification (:71), controlOperation cancellation polling (:223) |
| internal/app/operation_runner.go | web/TUI study start/resume (:131-151) and cancel (:152-162) |
| cmd/ultraplan/main.go | signal.NotifyContext(SIGINT, SIGTERM) (:19) |
| internal/platform/runtime/store.go | InspectRuntimeStores (:172), CleanupRuntimeStores (:199) |
| Tests | locks_test.go (:10,:45,:74), run_loop_test.go (10 scenarios :57-:495), state_test.go (:13-:299), run_history_test.go (:13-:102), cleanup_uncertain_test.go (:11,:63), disk_pressure_test.go (:5), run_loop_diagnostics_test.go (:11,:44), app/study_run_loop_commands_test.go (help/flags, locked stderr, force-unlock, reset yes/no prompts, redacted api-key lock fixture :119), app/study_run_all_commands_test.go |

Baseline: review/baseline shows go test ./..., go test -race ./..., go vet green at the frozen commit.

## 10. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace projects/ultraplan-go/sprints/12-durable-run-loop/requirements.md (authoritative sprint contract for this surface):
- AC: deterministic initial state from applicable matrix excluding inapplicable pairs (:38); load+validate before scheduling with actionable diagnostics (:39); schema change must migrate-or-reject, never silently reinterpret (:40); every meaningful transition persisted atomically (:41); temp+flush+rename+dir-sync with test-proven preservation (:42); lock records PID/command/timestamp, refuses second run-loop, releases on exit (:43); conservative stale-lock handling, explicit tested force-unlock scoped to study (:44); stale-running recovery preserving attempt/error history (:45); completed tasks revalidated on resume with clear diagnostics (:46); bounded workers never exceeding configured parallelism (:49); cancellation stops scheduling, cancels active runtime runs, waits, saves, records cancelled/retryable tasks, exits with cancellation convention (:50); drained event streams and Wait on every started run (:51); retry metadata persisted with unknown usage kept unknown (:52-53); typed error classification, safe diagnostics (:54); runtime-free status (:55); concise deterministic human output without prompts/secrets/native bytes (:56); loud write failures (:57); fake-runtime offline tests + race + build (:60-62).
- Constraints: orchestration owned by internal/study with thin app wiring; no direct OpenCode invocation; metadata from agentwrap-facing results not text parsing; context propagation mandatory; bounded concurrency; state/lock paths workspace-contained; atomic durable writes; safe redacted rendering; markdown inapplicable pairs skipped not failed.

TRD (docs/TRD.md):
- Applicability respected by run-loop initial creation and resume validation (~L733-734, ~L1058-1059); inapplicable pairs skipped not failed (~L743).
- §21.2 Locks (~L2372-2379): per-study run lock for run-loop; PID/command/timestamp in lock file; conservative stale detection; explicit force unlock; Sprint 35 note that PID+timestamp alone is not sufficient authority going forward (lease+fencing direction).

ARCHITECTURE.md:
- Batch/run-loop execution capabilities: bounded concurrency, durable task state, diagnostics, terminal failure state, resumability (~L620).
- run-loop scheduling stays inside the study module reuse boundary (~L651); sprint module must not depend on study run-loop scheduling (~L278).

Sprint 35 (projects/ultraplan-go/sprints/35-durable-run-observability/requirements.md): FUTURE-INTENT/partially-implemented overlay — durable run identity, leases/fencing beyond pidfiles, conservative reconciliation ("never infer success from process absence or artifact presence"), idempotent authorized cancellation. Partially realized via internal/runcontrol wiring around entrypoints; the study-level pidfile lock itself remains PID-based in this target.

In-repo doc contracts: internal/study/doc.go package scope; studyRunLoopHelp (app/study_commands.go:645-660): persists run-state after each meaningful transition, appends runs/tasks.jsonl, refreshes summary.md, cancels through the runtime boundary on interrupt, refuses concurrent invocations unless --force-unlock.

## 11. Explicit unknowns / open questions (for later reviewers)

1. `RunLoopRequest.Continue` (execution_domain.go:137) is populated by CLI (`--continue`) and web/TUI operations but no non-test code in internal/study reads it; resume behavior occurs identically without the flag. Intended semantics undocumented.
2. PID-reuse window: processAlive trusts `kill(pid,0)` (+EPERM-as-alive) with no start-time/identity corroboration; release verifies only PID+Study+AcquiredAt. A recycled PID can make a stale lock look live (or a foreign process signal-eligible via CancelRunLoop, subject to the study-name string match and self-PID check).
3. Archive collision: reset archives use second-granularity timestamps (`run-state-20060102T150405Z.json`) with plain os.Rename onto the destination.
4. `runs/summary.md` is rewritten with plain os.WriteFile while tasks.jsonl uses temp+fsync+rename; durability asymmetry is unpinned by tests.
5. Diagnostics appends silently discard MkdirAll/open/encode errors; rotation removes `.1` unconditionally; sampling reads many /proc files per tick.
6. syncDir ignores errors; dir-entry durability after rename is assumed.
7. On non-Linux platforms /proc parsing yields zero-values interpreted as "no pressure" (silent degradation); syscall usage has no build tags.
8. When firstErr is set (including publication errors), RunLoop returns before the final forced save/SyncRunHistory/publish epilogue; whether that ordering is intended signaling or an omission is undocumented.
9. DB-authoritative mode keeps the raw file stale mid-run (checkpoint written only at Complete); external tools reading the JSON directly see lagging data — compatibility expectation unstated.
10. Within one invocation, `attempted` allows re-claim only for Retrying tasks; Failed/Pending/Waiting/Cancelled statuses blocked even though taskRunnable admits them — interplay with multi-day RetryAfter values across invocations is emergent behavior.
11. `recordHistory` deep-clones the entire run state per terminal task; scaling characteristics on large studies are uncharacterized.
12. History dedup key omits validation/error fields; two distinct terminal records differing only in those fields collapse to one ledger line (practical reachability unclear given CompletedAt granularity).
13. Cleanup-uncertain OwnerPID is persisted but never compared during reconciliation; Reason vocabulary is hard-restricted to `"server_shutdown"`.
14. Effective parallelism is mutated by both memory hysteresis and disk caps each iteration with a single schedulingMessage slot; combined-throttle presentation order is emergent.
15. Child-task attribution in diagnostics matches cmdline substrings when multiple tasks are active — heuristic, may misattribute or leave blank.

— End of context pack. Descriptive only; no defect claims made or implied.
