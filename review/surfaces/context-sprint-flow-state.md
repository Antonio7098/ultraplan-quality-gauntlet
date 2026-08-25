# Context Pack: `sprint-flow-state` — Sprint flow-state authority and mutation locks

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: governed-sprint-delivery. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

`flow-state.json` is the central multi-writer record every sprint stage gates on. This surface owns:

1. **Strict loading** of flow state: DB record first, else file; schemaVersion restricted to 2 (current) and 1 (predecessor); unknown-field rejection; single-JSON-value enforcement; v1→v2 migration performed at read time only (never persisted back as v1).
2. **Atomic saves** that preserve prior verification evidence: temp+fsync+rename+dir-fsync writes, and nil `Review`/`Smoke`/`QA` backfilled from the prior state so planning-stage refreshes cannot erase completed verification records.
3. **Status-refresh write gating** (`statusWrites`): `Service.Status` derives stages from artifacts and by default persists the refreshed state; read-only presentation surfaces can disable that write.
4. **Layered mutation lease**: per-Service in-process `sync.Map` guard plus a cross-process O_EXCL pidfile at `.ultraplan/locks/sprint/<p>--<s>.lock` with `kill(pid,0)`/EPERM liveness and ownership-checked release.
5. **Interrupted-mutation reconcile**: converts durable running records (execute tasks, review/smoke attempts, QA phases) left by a dead owner into explicit interrupted evidence under the lease.
6. **Cleanup-uncertain consumption**: a product-owned marker written without the lease at shutdown-deadline exhaustion; consumed (or refused) by reconcile.

Consumers re-read flow-state bytes as trusted gate input: code-context prerequisite (persisted complete stage), smoke's review gate, QA map's terminal-review requirement, Verify's assessment, and status rendering.

## 2. Entrypoints and control flow

### 2.1 LoadFlowState (`internal/sprint/state.go:20`)
- Calls `loadFlowStateDatabase` (`state_database.go:19`): `productstate.Existing(root)` (DB `.ultraplan/run-control.db`, kind `"sprint_flow"`, scope `"<project>/<slug>"`); if a record exists, header JSON unmarshals into `FlowState`, each item payload appends a `StageState`; then `ValidateFlowState` runs on the assembled state and it is returned. No v1 migration or pre-code-context interpretation is applied to DB-loaded records.
- Otherwise reads `projects/<p>/sprints/<s>/flow-state.json` (`FlowStatePath`, artifacts.go:51 via `resolveSprintContained`): not-exist → `ErrFlowStateMissing`; header probe of `schemaVersion`; 0 → malformed "missing schemaVersion"; anything other than `FlowStateSchemaVersion`(2) / `PreviousFlowStateSchemaVersion`(1) → `ErrFlowStateUnsupported` ("restore version %d or regenerate state"); decode with `DisallowUnknownFields` → malformed on failure; second decode must hit EOF (multiple/trailing JSON values → malformed).
- If v1: `migrateFlowStateV1` (state.go:150) sets SchemaVersion=2, applies pre-code-context stage insertion if the shape matches, marks `Review`/`Smoke` stale, and for completed review/smoke backfills digests from artifact files (missing file → digest `"legacy-unverifiable"`), synthesizes `LastComplete` with completedAt from LastRunAt or UpdatedAt, smoke InputFingerprint `"legacy-unverifiable"`. Then validated; nothing is written back (pinned by verify_test.go:62-93).
- Independent of version: a v2 state whose stages are exactly the six legacy stages (requirements..plan, no code-context) is interpreted in memory by `interpretPreCodeContextStages` (state.go:96): inserts code-context after requirements — Ready if requirements complete and all later stages missing, otherwise Skipped inheriting requirements' LastRunAt.
- Final `ValidateFlowState` (state.go:294): exact PlanningStages list/order/count, no duplicates, valid statuses, required non-empty contained relative `Path` per stage (abs/`../` rejected; `resolveSprintContained` double-checks), error text free of NUL/CR/LF, `LatestOutcome ∈ {"",failed,cancelled,interrupted,cleanup_uncertain}`, Project/Sprint must equal the addressed sprint, UpdatedAt non-zero, plus sub-record validators `validateReviewStageState` (review.go:1139), `validateSmokeStageState` (smoke_types.go:291), `validateQAFlowSummary` (state.go:369).

### 2.2 SaveFlowState (`state.go:201`)
- Evidence preservation: if incoming `Review`/`Smoke`/`QA` any nil, loads prior state; nil fields are backfilled from prior. Prior-load errors other than `ErrFlowStateMissing` abort the save. Comment: "Planning-stage refreshes must not erase the last complete verification evidence."
- Authority routing: `FlowStateInDatabase` true → `saveFlowStateDatabase` (`state_database.go:69`: stamps UpdatedAt, validates, header = FlowState minus Stages, one item per stage keyed by stage name with ordinal, transactional upsert via `productstate.Store.Save`), then additionally writes the file via `saveFlowStateWithHooks` only when `flowStateCheckpoint(state)` — all stages terminal (complete/failed/skipped) and non-empty. Otherwise plain file save.
- `saveFlowStateWithHooks` (`state.go:242`): stamps `UpdatedAt=time.Now().UTC()` (not the service clock), `ValidateFlowState`, `json.MarshalIndent` + `\n`, MkdirAll sprint dir 0755, `os.CreateTemp(dir, ".flow-state.*.tmp")`, Write+Sync+Close, optional `BeforeRename(path)` hook (test seam), `os.Rename` onto the canonical path, deferred temp cleanup on every failure path, best-effort dir fsync (`syncDir`, state.go:399 — errors ignored).

### 2.3 Service.Status (`service.go:229`) — the statusWrites writer
- Discovers project+sprint, enforces `inside(p.Path, sp.Path)`.
- Probes raw bytes: `preCodeContextFlowState` (state.go:115) reports whether persisted v2 has the legacy six-stage shape (suppresses the refresh write for such files).
- `LoadFlowState`; missing tolerated, other errors returned. Reads `ArtifactSnapshot` (store_fs.go:35 — presence + content validation of requirements/code-context, area-reasoning dir listing, sprint-index "no reasoning templates" marker).
- `DeriveStages(sp, snap, prior)` (service.go:1484): artifact-presence-derived statuses in fixed order; prior failed records preserved while artifact still absent; first non-complete stage becomes Ready; later ones blocked/missing; code-context requires prior Complete to stay Complete (or Skipped carries over); area-reasoning Complete when files exist, Skipped when index says none selected.
- Builds `refreshed := NewFlowState(...)`, copies prior `Review`/`Smoke`/`QA` pointers, recomputes `Review.Stale` (PrepareReview findings + fingerprint compare + content validation + digest equality; snapshot-fingerprint terms gated by `strictCompletedReviewSnapshotFreshness`, currently false, freshness_policy.go:12-14) and `Smoke.Stale`/`Reconciliation` (artifact presence/content/digest, fingerprint mismatch vs stored SmokeFingerprint, review-currency linkage).
- Write gate: `if s.statusWrites && !legacyCodeContextState { SaveFlowState(refreshed) }`. `WithoutStatusWrites` (service.go:82) flips the gate off; used by `dashboardUseCases.sprintService()` when readOnly (app/usecases.go:130-132) and web sprint-status use case (app/sprint_usecases.go:748). Default CLI/TUI/web dashboard Status calls keep writes enabled (app/sprint_commands.go:95, project rollups app/project_usecases.go:39, operations.go:415/451/469).
- Then loads execute run state (legacy terminal summaries handled separately), computes `VerificationStatus`, returns `StatusSummary`.

### 2.4 Mutation lease
- `acquireMutation` (`service.go:89`): resolve sprint → key = cleaned sprint path; `s.mutations.LoadOrStore` (per-Service `*sync.Map`, created in NewService; loaded ⇒ `ErrVerificationConflict`) → `acquireVerificationFileLock`; file-lock failure deletes the in-process key before returning.
- `acquireVerificationFileLock` (`verification_lock.go:26`): path `<root>/.ultraplan/locks/sprint/<p>--<s>.lock`, MkdirAll 0755, loop of 2 attempts: `O_WRONLY|O_CREATE|O_EXCL` 0644; on success marshal+write `verificationLockInfo{Project,Sprint,PID=os.Getpid(),AcquiredAt}` (write/close failure removes the file). On EEXIST: read existing lock — unreadable/invalid JSON/negative PID/zero timestamp ⇒ fail closed with `ErrVerificationConflict`; `verificationProcessAlive(existing.PID)` (`kill(pid,0)`; alive = nil OR EPERM; pid≤0 dead) ⇒ conflict naming PID and AcquiredAt; dead owner ⇒ remove once and retry; second EEXIST-with-live-owner exits the loop with conflict.
- `release` (`verification_lock.go:78`): re-reads the lock; not-exist ⇒ already released (nil); unreadable ⇒ error; mismatch of PID/Project/Sprint/AcquiredAt ⇒ "ownership changed; refusing release"; else remove.
- `acquireMutationContext` (`locks.go:112`): context value `mutationLeaseContext{path}` lets composite workflows reuse an existing lease for the same sprint path without re-acquiring (nested acquire returns a no-op release). Non-dry-run users: Flow (flow.go:117), FlowStage (flow.go:201), Verify (verify.go:41), Review (review.go:411), RunSmoke (smoke.go:25), Execute (execute.go:37,131), QA start/resume (qa.go:135,249), RecoverQA (qa.go:135), ReconcileInterruptedMutation (locks.go:26).

### 2.5 ReconcileInterruptedMutation (`locks.go:25`)
- Acquires the full lease; `ErrVerificationConflict` ⇒ `(false, nil)` (a live owner is never rewritten).
- Probes `<sprint>/.cleanup-uncertain.json` (`loadCleanupUncertain`); a present-but-invalid marker aborts reconcile with its parse/validation error (fail closed).
- Execute run state: running tasks → Failed with diagnostic code `recovery-interrupted` ("running task belonged to a stopped process…"), CompletedAt=now, persisted via SaveExecuteRunState. Missing execute state tolerated only when `legacyTerminalExecuteRunState`; other load errors abort.
- Flow state: `reconcileExpiredAttempts(&flow, now)` (verify.go:481) — for Review/Smoke ActiveAttempt expired per `attemptExpired` (verify.go:467: running ∧ (dead OwnerPID immediately ∨ now−max(StartedAt,HeartbeatAt) > 2h)) → attempt copied to LastAttempt as `timed_out` category `interrupted` with bounded diagnostics, ActiveAttempt cleared, stage Status set Failed, LastRunAt=now; persisted via SaveFlowState when changed. Missing flow state tolerated only when `legacyFlowState` (v0+version1 map form, state.go:131); other load errors abort.
- QA: if `verification/state.json` exists and phase ∈ {queued, running, synthesizing} → phase Interrupted, run lifecycle terminal/interrupted, blocker recorded, republished through `qaStore.SaveRecoveredState` (qa_state.go:429 — refuses active phases, writes only state.json + refreshed `flow.QA` pointer via SaveFlowState).
- Cleanup-uncertain consumption: marker present ∧ changed ⇒ remove marker; marker present ∧ ¬changed ⇒ return `ErrCleanupUncertain` naming the marker path (marker retained, test-pinned). Returns `changed`.
- Called from web startup reconciliation over every sprint summary (`ReconcileOperations`, app/web_usecases.go:563-576, ctx checked between sprints).

### 2.6 Cleanup-uncertain markers (`cleanup_uncertain.go`)
- `RecordCleanupUncertain` (line 31): honors ctx.Err(); deliberately does NOT acquire the mutation lease ("at deadline exhaustion the original owner may still hold that lease"); forces SchemaVersion=1, OwnerPID=os.Getpid(), normalizes timestamps to UTC, requires OperationID/Kind non-empty, Reason exactly `"server_shutdown"`, no NUL/CR/LF in OperationID/Kind; atomic write (temp+fsync+rename, smoke.go:714 atomicWriteFile) of `<sprint>/.cleanup-uncertain.json`.
- App entrypoint: `RecordOperationCleanupUncertain` (app/web_usecases.go:540) routes sprint/study markers; reason comes from server-shutdown operation finalization.
- `removeCleanupUncertain` idempotent (not-exist ⇒ nil).

### 2.7 VerificationStatus (read-side derivation, verify.go:142)
- Loads flow state; applies `reconcileExpiredAttempts` to the in-memory copy ONLY (comment: "Status derives expired-attempt truth without mutating durable state. The next explicit review/smoke operation owns any persisted transition." Pinned by verify_test.go:123).
- Recomputes freshness reasons from current artifacts (existence, digest equality vs recorded ArtifactDigest, content validators, PrepareReview governed-input findings, fingerprint comparisons where the strict switches enable them) and derives OverallAssessment + NextAction via `deriveAssessment` (verify.go:252: malformed ⇒ blocked; fresh fail verdicts ⇒ fail; incomplete chain ⇒ incomplete naming earliest stage; smoke verdict mapping incl. pass_with_findings/not_applicable).

## 3. Inputs / outputs

Inputs: workspace tree (planning artifacts, review.md/smoke.md bytes, execute.md, .run-state.json), product-state DB row (kind sprint_flow), lock file contents, cleanup-uncertain marker, wall clock (`s.now()` for reconcile/attempts; `time.Now().UTC()` inside save/database writers), os.Getpid().
Outputs: replaced `flow-state.json` (or DB row + checkpoint file), created/removed lock file, created/removed cleanup-uncertain marker, recovered `verification/state.json` + flow pointer, sentinel-class errors (`ErrFlowStateMissing/Malformed/Unsupported`, `ErrVerificationConflict`, `ErrCleanupUncertain`), `StatusSummary`/`VerificationStatus` structures for presentation.

## 4. Authoritative state

- File authority: `projects/<p>/sprints/<s>/flow-state.json` (FlowStateRelPath, artifacts.go:39).
- DB mirror: `.ultraplan/run-control.db` tables `product_states` (header JSON + sha256 + updated_at) and `product_state_items` (payload per stage, ordinal order, FK cascade). SQLite WAL, synchronous FULL, immediate txlock, busy_timeout 5000ms, MaxOpenConns 4, process-wide store cache (internal/productstate/store.go:55-90). Runtime selection: Load prefers an existing DB record; Save routes to DB only when a record already exists (`FlowStateInDatabase`), adding a file checkpoint only when all stages are terminal (`flowStateCheckpoint`, state.go:233).
- Migration command: `storage.migrate` imports file→DB when DB lacks the scope and the file loads cleanly; skips when DB already has it (app/storage_commands.go:142-162; `MigrateFlowStateToDatabase`, state_database.go:143).
- Ancillary state owned here: `.ultraplan/locks/sprint/<p>--<s>.lock`; `<sprint>/.cleanup-uncertain.json`.
- Legacy strata deliberately excluded from mutation: schemaVersion 0 + `"version": 1` map-based flow state (`legacyFlowState`) and pre-code-context-shaped v2 files (`preCodeContextFlowState`) are read/interpreted but never rewritten by reconcile/status; v1 is upgraded in memory only.
- Related but distinct authorities referenced by this surface: `.run-state.json` (SA-7, execute tasks) and `verification/state.json` + QA privates (SA-8).

## 5. Invariants (as implemented)

- Strict load grammar: version ∈ {1,2}; DisallowUnknownFields; exactly one JSON value; fixed stage sequence/count; unique valid stages/statuses; safe workspace-relative stage paths (lexical + `resolveSprintContained`); sanitized error detail; LatestOutcome enum; Project/Sprint identity match; UpdatedAt required.
- Sub-record validation: review path fixed to `projects/<p>/<s>/review.md`; status/verdict/provisional enums; attempts satisfy active⇒running∧CompletedAt==nil and last⇒terminal∧CompletedAt set, diagnostics ≤240 chars without NUL/CR/LF; resume checkpoints consistent with active attempt ID while running, completed checkpoints carry results and incomplete ones never do; LastComplete fully populated. Smoke analog plus override confirmation shape and issue status enum. QA pointer must be a contained `.../verification/state.json` with 64-hex digest, non-empty next action, valid optional attempt ID.
- Atomicity: canonical flow-state.json changes only by rename of a fully written+fsynced temp; failures leave prior bytes intact (test-pinned including a BeforeRename hook failure injection).
- Verification-evidence preservation across saves: nil Review/Smoke/QA backfilled from prior state.
- Read-only derivations never persist: VerificationStatus expiry interpretation; v1 upgrade; pre-code-context insertion.
- Status write gate: default writes; `WithoutStatusWrites` suppresses creation/update; pre-code-context-shaped files exempt from the refresh write.
- Lease layering: same Service instance ⇒ sync.Map short-circuit; different instance/process ⇒ O_EXCL pidfile; liveness check treats EPERM as alive (conservative); unreadable lock fails closed; stale lock removed then retried once; release verifies ownership tuple before unlink; nested composite workflows reuse via context marker keyed on cleaned sprint path.
- Reconcile: never rewrites a live lease; converts durable running records into explicit terminal evidence; consumes uncertainty only on actual change; refuses (error, marker retained) when uncertain cleanup had nothing to reconcile; legacy strata untouched (test-pinned).
- Attempt expiry rule: running only; dead OwnerPID expires immediately; else heartbeat/started older than 2h.

## 6. Trust boundaries

- flow-state bytes (and the DB row) are trusted gate input for every later stage: code-context prerequisite demands a persisted complete stage (code_context.go:245-255); smoke's review gate trusts flow.Review verdict/fingerprint/digest then re-validates artifact content and digest at decision time (smoke_protocol.go:177-209); QA map requires a terminal, non-stale review record with fingerprint (qa_map.go:59-70); Verify chains off these plus complete execute evidence. Anyone who can write flow-state.json (or the DB row) can flip gate inputs; mitigations are load-time strict validation plus decision-time digest/content recomputation and stale recomputation in Status/VerificationStatus.
- Legacy strata excluded from mutation: reconcile/status refuse to rewrite v0/version-1 and pre-code-context-shaped states; v1 upgrade is memory-only.
- The lock file is workspace-local and world-readable (0644) in a 0755 directory; liveness uses signals against arbitrary PIDs; EPERM (foreign-user process) counts as alive so locks are not stolen across users.
- The cleanup-uncertain marker is written WITHOUT the lease by design (shutdown deadline contention); consumers treat malformed markers as hard errors rather than ignoring them.
- Stage `Error` strings and attempt diagnostics are sanitized/redacted at write time (`safeError` truncates to ~180-240 chars, strips NUL/CR/LF, redacts secret-shaped values per config.RedactValue; test-pinned TestSafeErrorRedactsSecretsBeforePersistence) and re-validated at load.

## 7. External effects & lifecycle semantics

- Effects limited to the workspace tree + DB: replace-on-success flow-state.json; transactional DB upserts (hash-guarded no-op when unchanged); lock create/remove; marker create/remove; QA recovery writes.
- Crash/restart story: dead-owner pidfiles stolen on next acquisition; orphaned running attempts/tasks/QA phases converted to explicit timed_out/failed/interrupted by ReconcileInterruptedMutation (web invokes it over all sprints at startup); temp files never become canonical due to rename-based publication.
- Cancellation: RecordCleanupUncertain checks ctx.Err() first; web reconciliation checks ctx between sprints; runtime-stage cancellation is upstream of this surface but its residue (running attempts) is what reconcile consumes.
- Retry semantics: re-running a conflicting operation returns ErrVerificationConflict with owner PID/timestamp until the owner exits; rerunning reconcile is safe/idempotent once evidence is terminal.
- Error surfacing: malformed/unsupported flow state propagates as errors out of Status (CLI exit class 5 pinned by app tests) and gates (smoke review gate, code-context prerequisite, QA map). Many planning-flow failure paths intentionally ignore SaveFlowState errors (`_ = SaveFlowState(...)` after building failed-stage results) — the FlowResult/error still reports the original failure; the state write is best-effort on those paths. Successful-path saves propagate errors to callers (review/smoke wrap persistence failures into result diagnostics like `smoke_reconciliation`).
- Platform assumption: `syscall.Kill` in verification_lock.go has no build tags; the lease mechanism presumes a unix signal API (repo targets linux; no windows variant exists in-tree).
- Durability classification (consistent with map-02 X-series facts): fsync+dir-sync for flow-state file writes; syncDir errors ignored; DB relies on SQLite synchronous FULL/WAL.

## 8. Immediate surface dependencies

- `product-state-mirror` (internal/productstate): DB authority selection, storage.migrate import; kind/scope grammar `sprint_flow`@`<project>/<slug>`.
- Writers into flow state (all route through SaveFlowState): planning flows in service.go (requirements/code-context/sprint-index/handbook/reasoning/plan success + failed-stage records), code_context.go:436,475, review.go (saveReviewState:1202-1263 including attempt lifecycle and heartbeats via updateReviewResume:839-864; initializeReviewResume:748), smoke.go (saveSmokeAttempt:191-232 running/terminal transitions; terminal commit:476-480 writing full SmokeStageState + LastComplete), qa_state.go (publish flow pointer LAST of all artifacts:419; SaveRecoveredState:447), locks.go reconcile:71.
- Readers/gates outside this pack: qa_map.go:59, qa.go:153/285, smoke_protocol.go:177, verify.go:150, execute_state surface (.run-state.json sibling authority), app presentation layers (sprint_commands.go, project_usecases.go rollups, web dashboards, operations.go kinds sprint-status etc.), study package mirrors the marker pattern separately (study/cleanup_uncertain.go).
- Upstream inputs this surface consumes but does not own: PrepareReview manifests/fingerprints (review preparation), ValidateReviewContent/ValidateSmokeContent, artifact validators, gitpublish publication (separate seam), config.RedactValue.

## 9. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace docs:
- TRD §18.1 (~L1671): `internal/sprint` owns `flow-state.json`.
- TRD §18.2 (~L1798): flow-state.json among reused Phase-1 artifacts.
- TRD §18.5 (~L1815): "`flow-state.json` must be versioned and stored in the sprint root." Required fields: schema version, project name, sprint slug, updated timestamp, per-stage status/artifact path/last run timestamp/error-if-any; allowed statuses enumerated (list begins `missing`).
- TRD ~L1991: a planning stage is complete only when runtime succeeds, artifact exists and passes its validator, AND "`flow-state.json` is updated atomically."
- TRD ~L2180: required tests include ordered-stage transitions, OLD FLOW-STATE COMPATIBILITY, path/range validation, atomic rerun, cancellation, and recovery.
- ARCHITECTURE.md L249 lists flow-state.json in the sprint artifact chain; L708: "Detailed QA attempt state belongs outside `flow-state.json`; flow state keeps canonical summaries, freshness, verdicts, and pointers."
- doc.go (in-repo): package contract — strictly loads flow-state.json, derives status from artifact presence, writes refreshed state atomically; app parses/renderers only; stage order/schema validation sprint-owned.

HISTORY/context only: map-02 SA-6/SA-8 record the same structural facts at a prior target SHA; freshness_policy.go comments document why snapshot-based strictness is currently disabled (brittleness attribution), i.e., the strict switches are a deliberate temporary posture, not an oversight claim either way.

## 10. Tests (evidence map)

Package-internal:
- sprint_test.go:100 TestFlowStateStrictLoadingAndAtomicWritePreservesPrior — round-trip; unsupported stage ⇒ ErrFlowStateMalformed; unsafe stage path rejected by ValidateFlowState; BeforeRename hook failure leaves prior bytes byte-identical.
- sprint_test.go:152 TestLegacyV2ReaderClassifiesPublishedQASummaryAsMalformed — a v2 file carrying `qa` fails DisallowUnknownFields decode and is classified malformed (not unsupported).
- sprint_test.go:179 TestServiceStatusRefreshesMissingStateAndRejectsInvalidState — Status creates flow-state.json from artifacts; manual artifact observed and synchronized on next Status; `{not json` state ⇒ ErrFlowStateMalformed surfaced.
- verify_test.go:62 TestFlowStateMigratesExactlyOnePredecessor — v1 file loads as v2 with stale flags and synthesized LastComplete while the FILE still contains schemaVersion 1 afterwards (pins read-time-only migration); version 0 ⇒ malformed; version 99 ⇒ unsupported.
- verify_test.go:95 TestPreCodeContextFlowStateCompatibilityPreservesKnownOutcomes — six-stage v2 gains inserted code-context (Skipped) in memory; persisted bytes unchanged.
- verify_test.go:123 TestVerificationStatusDerivesExpiredReviewAttemptWithoutWriting — expired running attempt reported without mutating durable state.
- locks_test.go:26 TestSprintMutationLeaseIsSharedAndCompositeSafe — nested same-service acquire via ctx marker; second Service conflicts (ErrVerificationConflict); reacquire after release.
- locks_test.go:46/81/116 — reconcile leaves legacy terminal .run-state.json and legacy v0/version-1 flow-state byte-untouched; unrecognized malformed run state aborts with ErrExecuteRunStateMalformed.
- verification_lock_test.go:11 TestVerificationFileLockRejectsLiveOwnerAndReplacesDeadOwner — live PID (own process) blocks; dead-PID lock file replaced and cleanly released.
- cleanup_uncertain_test.go:12 TestRecordCleanupUncertainIsDurableAndReconciliationConsumesIt — marker durable/valid; reconcile with NO canonical running evidence returns ErrCleanupUncertain and RETAINS the marker.
- code_context_test.go:180-195 — WithoutStatusWrites Status does not create flow-state.json.
App-level: sprint_commands_test.go:75 TestSprintStatusRefreshesStateAndRendersDeterministically; :214 TestSprintStatusErrorsAndInvalidFlowStateExitFive; app/sprint_commands_test.go:1039 and web_usecases_test.go:71-83 seed/load/save flow state directly.
Baseline: full `go test ./...` green at frozen commit (review/baseline).

Coverage gaps noted factually (from map-02, consistent with this reading): no dedicated test for malformed flow-state during reconcile; no cross-process race test between lease holders; no test exercising the DB-authoritative save/checkpoint branch of SaveFlowState (storage.migrate covers import only).

## 11. Explicit unknowns / open questions (for later reviewers)

1. DB-authoritative records skip the v1→v2 migration and pre-code-context interpretation entirely (state_database decodes straight into FlowState; ValidateFlowState would reject v1). Whether DB-resident legacy/v1 records can exist in practice (import path loads via LoadFlowState first, which would migrate in memory and then SAVE v2 — so imported rows should always be v2) is inferred, not pinned by a test.
2. In SaveFlowState's authoritative-DB branch, a successful DB write followed by a failed checkpoint file write returns the file error even though the DB row is authoritative — intended signaling semantics undocumented.
3. Status with statusWrites=true does not hold the mutation lease; two concurrent Status invocations (or Status racing a stage writer) rely solely on atomic rename last-writer-wins. Whether concurrent status reads/writes are considered supported is undocumented.
4. PID-reuse window: lock liveness and attempt OwnerPID expiry trust `kill(pid,0)`; a recycled PID belonging to an unrelated live process makes a stale lock look live (and vice versa for expiry). No start-time/identity check beyond PID+timestamp tuple at release.
5. The 2h attempt-expiry constant and the 2-attempt lock retry bound are code constants with no configuration or documented rationale.
6. Many planning failure paths discard SaveFlowState errors (`_ =`); whether losing the failed-stage record itself matters downstream is unstated.
7. cleanup-uncertain Reason being hard-restricted to `"server_shutdown"`: stable contract vs incidental validation; study package mirrors the pattern independently.
8. Status's DeriveStages refresh rebuilds ALL stage records while preserving Review/Smoke/QA pointers; interplay between an in-flight verification attempt (recorded in Review/Smoke sub-records) and stage-level rewrites during that window is unspecified behavior territory.
9. syncDir ignores errors; durability of the renamed directory entry on filesystems requiring explicit dir fsync is assumed rather than verified.
10. Windows/portability: syscall.Kill usage has no build tags or alternative; platform support matrix is implicit (linux-only in practice).

— End of context pack. Descriptive only; no defect claims made or implied.
