# map-02-state-authorities — State-Authority Surface Map (independent pass)

Target: ultraplan-go @ f0fcd0c2107a8e8d69e1283f9e8d5e2c6da94025 (frozen). Working tree sits one commit ahead (`ad0be98`); sole delta is `internal/sprint/prompt_context.go` shared-prompt budget constant 256 KiB→512 KiB — all other evidence read at HEAD is valid at the frozen SHA. Workspace context: ultraplan-workspace @ ab12dc38059c.
Method: five bounded review-worker discoveries (runcontrol/productstate DB; cross-state seams; study durable state; sprint durable state; ephemeral/in-process state) + direct spot-checks of load-bearing claims. Facts only; no findings. Lens: **durable and ephemeral state authorities, behaviours grouped by the state they read/mutate.**

Repo frame: 17 packages, 206 production files. Two packages own the same SQLite file; three JSON state families (study `.ultraplan/run-state.json`, sprint `flow-state.json`, sprint `.run-state.json`) each have a file and a DB representation selected at runtime.

---

## Part 1 — State-authority inventory (the spine)

### Durable authorities

**SA-1 · Run-control SQLite** — `<workspace>/.ultraplan/run-control.db`, owned by `internal/runcontrol`.
Tables (`initialSchema` sqlite.go:229-342): `app_schema`, `runs`, `attempts`, `events` (append-only via trigger `trg_events_immutable` :327-331), `operation_aliases`, `reconciliation_log`; indexes :333-341. Versioning: `app_schema('run_control',1)` + `PRAGMA user_version=1` (sqlite.go:356-359; migration.go:20). Pragmas: busy_timeout=5000, foreign_keys=on, WAL, synchronous=FULL, txlock=immediate, `_defensive=1` re-verified post-open (`verifyPragmas` sqlite.go:207-227). Pool default 4 / max 16 conns. Migration takes `<db>.migrate.lock` O_EXCL with exact birth-token stale reclaim (migration.go:92-165); backup-before-create only for version==0 with pre-existing objects: WAL truncate checkpoint → `.bak.<UTC>` copy ≤512 MiB mode 0600, keep newest 3 (migration.go:175-291); `RestoreBackup` integrity-checked temp+rename (:296-352).

**SA-2 · Product-state SQLite (same file, separate handle)** — `internal/productstate` declares the identical `DatabaseRelativePath` const (store.go:19 vs runcontrol/sqlite.go:22). Own tables `product_states` (PK kind,scope; header_json + sha256 header_hash) and `product_state_items` (FK cascade, ordinal order, payload_hash) (store.go:92-116). Same four pragmas, no `_defensive`, no version tracking, fixed pool of 4, process-lifetime handles cached in `stores sync.Map` keyed by abs root (store.go:39,55-90); **no exported Close()**. Kinds in use: `study_run` scope=study name (study/state_database.go:13,22,67), `sprint_flow` / `sprint_execute` scope=project/slug (sprint/state_database.go:13-14,96,126). Populated only by `ultraplan storage migrate` (app/storage_commands.go:60-106) or first `Ensure()` save.

**SA-3 · Study run-state file** — `studies/<n>/.ultraplan/run-state.json` (domain.go:6; run_state.go:188-190). Schema `RunState`/`TaskState` (run_state_domain.go:19-68): schema_version gate, unique task IDs, status/kind whitelists, session bounds len≤512/no CR-LF-NUL (`ValidateRunState` state.go:121-166), 64 MiB load threshold. Write: same-dir temp + `Sync()` + rename + best-effort dir fsync (state.go:73-119; dir-sync errors ignored :177-183). Reset archives prior state to `.ultraplan/archive/run-state-<ts>.json` (run_loop.go:559-573).

**SA-4 · Study run-history ledger** — `studies/<n>/runs/tasks.jsonl` (run_history.go:16-20): schema_version=1 records; dedupe key `runID|taskID|attempts|completedAtRFC3339Nano` (:306-312); trailing-partial record tolerated on read, dropped before rewrite; earlier malformed lines hard-fail; 4 MiB scanner cap; writes are full-file rewrite temp+sync+rename (:128-148); **no rotation/size cap**. Sibling `runs/summary.md` written plain non-atomic `os.WriteFile` (run_history_summary.go:12-23).

**SA-5 · Study auxiliary durables** — `.ultraplan/run-state/cleanup-uncertain.json` marker (cleanup_uncertain.go:152-154; reason must be `"server_shutdown"`); `.ultraplan/run-loop.lock` O_EXCL PID/command/acquiredAt payload, dead-PID steal, force-unlock, SIGINT cancel refusing self (locks.go:17-167,141-158); `.ultraplan/diagnostics/run-loop-memory.jsonl` append-only with one-file rotation (run_loop_diagnostics.go:93,228-245); per-task agent stores `.ultraplan/runtime/opencode/<sha256(owner)[:16]>/opencode.db` + `store.json` PID record (platform/runtime/store.go:48-62), GC'd 72h/2GiB by callers (study/run_loop.go:60,381; sprint/runtime_metrics.go:119).

**SA-6 · Sprint flow-state** — `projects/<p>/sprints/<slug>/flow-state.json`. Schema v2 (`FlowStateSchemaVersion=2`, domain.go:10-11,135-144): fixed stage order, per-stage `StageState{Status,Path,LastRunAt,Error,LatestOutcome}`, review/smoke digest blocks, `QAFlowSummary` pointer validated to end `verification/state.json` with valid digest+fingerprint (state.go:369-384). Strict load: header probe, v1/v2 only, DisallowUnknownFields, single JSON value (state.go:20-81); v1→v2 in-memory migration inserting code-context stage, marking review/smoke stale, backfilling `"legacy-unverifiable"` digests (state.go:150-192). Atomic save temp+fsync+hook+rename+dir-sync, prior bytes preserved on failure (state.go:242-292; pinned sprint_test.go:100); nil Review/Smoke/QA backfilled from prior loaded state (state.go:201-218).

**SA-7 · Sprint execute state** — `projects/<p>/sprints/<slug>/.run-state.json` (artifacts.go:44): tasks with status/attempts/diagnostics/evidence (domain.go:103-133); deferred requires completedAt+rationale diagnostic, terminal requires completedAt (execute_state.go:184-269). Loads with plain `json.Unmarshal` — **no unknown-field/trailing-value rejection here** (execute_state.go:59-61), unlike flow-state. schemaVersion 0 terminal = immutable history, never resumed/reconciled (execute_state.go:73-103; locks.go:64). Atomic temp+rename writes; persist failure before runtime start aborts task launch ("persist running…" execute.go:216-218; triggered via third `WithClock` call replacing file with directory, execute_state_test.go:233-262).

**SA-8 · Sprint verification/QA privates** — composite pidfile lock `.ultraplan/locks/sprint/<project>--<slug>.lock` (verification_lock.go:14-101) layered over an in-process `sync.Map` lease (service.go:89-110); verification attempts carry OwnerPID+HeartbeatAt, expired = dead PID or >2h silence (domain.go:160-170,313-340; verify.go:467-510); QA private tree `verification/{state.json,attempts/<id>/…}` dirs 0700 / files 0600 temp+rename+fsync, reader enforces modes + symlink-component rejection, 128 MiB hard budget, writer-token fencing `(RunID, OperationalAttemptID, FencingGeneration)` (qa_state.go:138-152,197-339,453-464,16); pointer-last publish order map→shards→synthesis→state→flow-summary (qa_state.go:351-423). Snapshot inputs written 0400 (review.go:1627-1631). Strict-snapshot-freshness switches default **false** (freshness_policy.go:11-15); digest equality gates remain active (verify.go:142,188,226,237).

**SA-9 · Config/layout/git authorities** — env-first config table ~30 `ULTRAPLAN_*` vars incl. RUN_CONTROL_FULL_HISTORY/TOMBSTONE_HISTORY/WORKSPACE_QUOTA_BYTES/AGENTWRAP_* (platform/config/config.go:122-148; generated ULTRAPLAN_QA_* qa.go:100); `study.json` strict parse missing=defaults (config.go:40-63); smoke child-env allowlist PATH/HOME/TMPDIR/LANG/LC_ALL (smoke_types.go:55, smoke.go:89-93); project roadmap/index stores (project/store_fs.go, index.go); git publication through one `ultraplan-publish.lock` with explicit owned path sets per stage (gitpublish/publisher.go:31-32,112; study/publication.go:10-63; sprint/publication.go:11-121).

### Ephemeral authorities (process lifetime only)

**EA-1 · Web operation hub** — `operationHub{records, dedup}` under one mutex (web/operations.go:105-118): bounded event slices (256/op, slow-subscriber drop), MaxActive=8 preparations TTL 2m, TerminalRetention 10m reap (operations.go:18-32,565-575); SSE replay/gap logic (:376-426); stable event-name projection (:470-477; pinned against shipped JS by operations_contract_test). Shutdown: drain cancels ops reason `server_shutdown`, waits ≤10s (server.go:21,131); **only on drain deadline does it persist cleanup-uncertain markers** (operations.go:496-531 → app/web_usecases.go:540-560). Counters all memory-only atomics (:120-131). Auth: anonymous HMAC-signed cookie + CSRF token + Origin/Host checks + loopback-only bind, policy declared env-immune (security.go:110-251,149-177; server_policy.go:8-52).

**EA-2 · App orchestration caches** — per-process `runControlState` repos/loggers keyed by workspace root, policy-change rejected, Close() discipline at shutdown (app/app.go:108-120; run_control.go:23-95); `controlledRuntimeFor` wraps any StartRun into fenced DB runs (run_control.go:140-343): Accept→Claim→Fence, 250ms event-coalescing window, 1s ticker / 5s heartbeat / 10s reconcile goroutine (runcontrol/model.go:513-521); retry helpers 5s (run_control.go:345-368); `durableOperationManager.owned` map + per-op control goroutine (durable_operations.go:14-300). TUI holds model state, 1s dashboard tick while run view open, cancel prefers durable CancelRun over local ctx (tui/app.go:357-366,222-273,108-149).

**EA-3 · Runtime/session ephemera** — platform/runtime Adapter: 200-event ring (runtime.go:144,450-470), 5s cancel grace (:362-381), package-mutex session GC with raw SQL delete + VACUUM/checkpoint ×20 (opencode.go:19,93-167), log prune 48h/128MiB skipping <10m-old files (opencode_maintenance.go:25-77); study edit-warning snapshots are in-memory sha256 maps excluding `.ultraplan`/`.git` (edit_warnings.go:24); sprint stage sessions live in `.stage-sessions.json` (temp+rename **without fsync**, empty map deletes file, "session not found" clears key and restarts fresh — session_state.go:62-85,146-171,212-227).

Globals inventory: `productstate.stores sync.Map`; sprint `mutations *sync.Map` + `metricsMu` (service.go:38-39); `openCodeSessionCleanupMu`; static lookup tables (web/routes.go:23; timeline_handlers.go:42; project/roadmap.go:59-81; codeextract/resolver.go:11; runcontrol/sanitize.go:10; sprint/index.go:27; workspace/init.go:102-107; app/sprint_commands.go:968); error vars (web/security.go:453-458; operations.go:34-39). **No `func init()` anywhere.**

---

## Part 2 — Candidate surfaces (grouped by state authority)

### Domain R — Run-control & product-state authority (SQLite)

**R1. run-lifecycle-fencing** — risk: critical
- Behaviour: accept (alias mint) → claim (ordinal/fencing_generation = MAX+1, host_digest/boot_id/pid/birth_token identity, CAS `WHERE current_attempt_id IS NULL AND terminal_outcome IS NULL`) → lease (15s) / heartbeat (5s) → fenced event append (sequence = last_sequence+1, stale-fence rejection) → single-winner terminal CAS; reconciliation decisions from process-birth probes (not-found→interrupted, uncertain→cleanup_uncertain, expired→stalled) logged to reconciliation_log with class-label evidence.
- Entrypoints: `ultraplan run …`, serve/TUI durable operations, `RequestCancellation` acknowledge-then-cancel.
- Primary files: internal/runcontrol/{sqlite,model,lifecycle,id,interfaces}.go
- Symbols: Claim sqlite.go:486-592; AppendEvent :666-685; ProposeTerminal :769-786; reconcileProcessDecision lifecycle.go:481-509; Health :856-914.
- Tests: sqlite_test (schema/pragmas :17, reopen :157, concurrent sequences :287, terminal winner :344, immediate-tx locking :398); lifecycle_test (fenced heartbeat/cancel idempotency :26, exact-birth reconcile :176, clock-jump safety :233, never-claimed grace :261); fault_test (disk-full quota :12, closed handle :52, query_only :87).
- Risk rationale: this authority arbitrates every concurrent runner; fencing/CAS bugs corrupt all downstream dashboards and recovery.

**R2. run-recovery-retention-migration** — risk: high
- Behaviour: schema migration with lock + birth-token stale reclaim + pre-create backup/restore (keep 3); per-run compaction (4096 events/16 MiB) advancing oldest_retained_sequence; full→compacted→tombstone aging (defaults 7d/30d, env-overridable) then hard delete + passive wal_checkpoint + incremental_vacuum; alias resolution; health stall/backlog arithmetic using julianday unless clock injected.
- Entrypoints: `ultraplan storage migrate`; startup Reconcile; retention sweeps.
- Primary files: internal/runcontrol/{migration,retention,sanitize,metrics,local_log,process,process_linux,process_darwin}.go
- Tests: migration_test (:15 backup+versions, :67 newer-schema/contention, :94 stale-lock reclaim, :154 corrupt-db evidence, restore :124-146); retention_test (:12 replay boundary, :38 compact/tombstone order, :100 soft quota/headroom); import_boundary_test (:12-39 stdlib+sqlite+x/sys only).
- Risk rationale: destructive deletes + backup pruning + multi-process locking on one file; recovery tooling users must trust.

**R3. product-state-store-seam** — risk: high
- Behaviour: generic kind/scope/item KV inside the run-control DB; Save = single immediate tx, change-gated upserts + stale-item deletion; Load ordered by ordinal; used as the *authoritative* home for study run-state, sprint flow-state, sprint execute state once present; created by `storage migrate` (skips already-stored and legacy-terminal execute states; source files kept as checkpoints).
- Entrypoints: `ultraplan storage migrate` (app/storage_commands.go:60-206); every study/sprint state load/save.
- Primary files: internal/productstate/store.go; internal/study/state_database.go; internal/sprint/state_database.go
- Tests: **none direct** — productstate has no test file anywhere (baseline cover 0%); study/sprint package tests never exercise the DB branch; only indirect via storage_commands tests if any.
- Risk rationale: sole persistence for three state families, zero dedicated coverage, silent existence-based activation (below).

### Domain S — Study run-state authority

**S1. study-runloop-scheduler-state** — risk: critical
- Behaviour: worker-slot refill under disk/memory pressure caps (diskParallelismCap reserves 512 MiB/worker above 1536/768 MiB thresholds via Statfs; meminfo stretch/recover watermarks), priority tiers, early synthesis, throttled persisted saves (saveMu + version check + 250ms min interval), resume reconcile (running/validating/waiting/cancelled→pending; completed outputs revalidated, demoted to failed on artifact mismatch), `--reset` archive-then-fresh, cancelled-session checkpoint persistence, retry accounting (10m rate-limit delay / 2m otherwise), diagnostics sampling, final save→history sync→publication triple ordering.
- Entrypoints: `study <s> run-loop [--reset|--force-unlock]`; `CancelRunLoop` SIGINT path.
- Primary files: internal/study/{run_loop,run_state,state,run_state_domain,locks,disk_pressure,memory_pressure,run_loop_diagnostics,cleanup_uncertain}.go
- Tests: run_loop_test (channel-guard refill, isolated RuntimeStorePath per request, cancelled-session resume); state_test (save/load error categories :56, oversized-diagnostic compaction :101, rename-failure preserves prior :198, resume revalidation :224/:247, restore-history validity :277); run_history_test (trailing tolerance :13, trim :30, dedupe :41, loop integration :102); locks_test (conflict/force/release :10, dead-PID steal :45, live block :74); cleanup_uncertain_test (durability+consume :11, fail-closed :63); disk_pressure_test (:5 cap arithmetic only).
- Protection gaps (facts for later phases): Statfs/meminfo readers unpinned; dir-fsync failures ignored; debounce timing untested; memory_pressure.go has no injection seam; retry-wait loop and CancelRunLoop SIGINT lane untested.
- Risk rationale: longest-lived mutating process in the product; many interleaved writers to one JSON file plus a rewrite-the-whole-ledger history format.

**S2. study-dual-home-seam** — risk: high
- Behaviour: `RunStateInDatabase` = mere existence of `<workspace>/.ultraplan/run-control.db` (productstate.Existing, state_database.go:70-80); DB row wins on load (state.go:28-35); on save with DB authoritative, DB is always written and the JSON file is rewritten **only when `state.Complete`** (state.go:59-71) — deliberate partial parity; conversion one-way via `storage migrate`; no UI/CLI indicator distinguishes homes.
- Primary files: internal/study/{state,state_database}.go; internal/productstate/store.go
- Tests: none exercise the DB branch in-package.
- Risk rationale: two representations of the same logical state with asymmetric write rules and an implicit file-presence trigger.

**S3. study-session-checkpointing** — risk: normal
- Behaviour: per-task `TaskState.Session` checkpoints written via OnEvent callback; continuation allowed only when ContinueFailures==0 and provider/model/workDir/input-fingerprint match; fresh fallback increments ContinueFailures except transient categories; diagnostics truncated to 4096 B before persist; per-task opencode.db stores GC'd 72h/2GiB; summary.csv deterministic atomic writes preserving prior file on failure.
- Entrypoints: `study <s> run|synthesize|summary`.
- Primary files: internal/study/{run,runtime_metadata,edit_warnings,durable_metadata,summary}.go
- Tests: run_test (:56-132 checkpoint/continue/fallback), run_loop_test (:132), summary_test (:9,:45,:70,:96).
- Risk rationale: moderate — wrong continuation silently mixes agent context across changed inputs.

### Domain F — Sprint flow-state authority

**F1. flow-stage-machine-publication** — risk: high
- Behaviour: skeleton creation, per-stage validators, skip-if-complete via ExecuteComplete reading execute state, strict v2 load with v1 in-memory upgrade, atomic saves preserving unspecified records, publication path-set ownership per stage (planning: flow-state+artifact+templates+workspace; execute: target repo All:true + execute.md/.run-state.json; review; smoke harness+roadmap on pass) behind one publish lock; byte-stable shared prompt prefix (frozen budget 256 KiB) with TOCTOU replacement detection.
- Entrypoints: `sprint <p> <s> flow --to <stage>`.
- Primary files: internal/sprint/{flow,state,artifacts,prompts,prompt_context,code_context,index,handbook,reasoning,plan,publication,store_fs}.go
- Tests: sprint_test (:38 rel-path, :100 atomic prior-bytes, :210 malformed); state validation matrix; code_context_test (12 mutation cases, state-persist failure restores valid artifact); plan_test (10-case table); publication_test (:45 owned set).
- Risk rationale: central record other stages gate on; strictness asymmetries vs execute-state matter here.

**F2. mutation-lease-reconciliation** — risk: high
- Behaviour: two-layer lease (in-process sync.Map + cross-process pidfile with kill(0) liveness, EPERM counts alive); composite workflows reuse lease via context marker; `ReconcileInterruptedMutation`: running execute tasks→failed(`recovery-interrupted`), expired verification attempts→timed_out, active QA→interrupted; legacy v0 terminal execute-state and v0/v1 flow-state deliberately untouched; malformed non-legacy state is a hard error; consumes `.cleanup-uncertain.json` only after recovery mutated something, else fails closed.
- Entrypoints: every mutating sprint command; server startup ReconcileOperations.
- Primary files: internal/sprint/{locks,verification_lock,cleanup_uncertain,service}.go
- Tests: locks_test (:11-44 lease sharing/nesting, :46-79 legacy untouched, :116-137 malformed rejected); verification_lock_test (live/dead replace :11-40).
- Risk rationale: decides what survives a crash mid-write; fail-open/fail-closed choices here define data loss.

**F3. execute-resume-state** — risk: high
- Behaviour: plan extraction → queue execution over shared runtime session; per-transition `.run-state.json` persists before runtime starts; resume rebases onto plan records, stale running→failed(`stale-running`); checkpoint-loss marks task failed without breaking stop-on-first-failure queue discipline; defer requires rationale; execute.md summary plain os.WriteFile; schemaVersion 0 terminal treated as immutable history.
- Entrypoints: `flow --to execute`; `execute [--resume|--defer]`.
- Primary files: internal/sprint/{execute,execute_plan,execute_state,execute_target,execute_model}.go
- Tests: execute_state_test (:44-88 prior-bytes sabotage, :91-160 validation matrix, :174-185 legacy, :233-262 persist-before-start, :264-282 checkpoint-failure); execute_plan_test (resume keeps checked parent+children).
- Protection gap (fact): no end-to-end Resume against mixed terminal/running/pending state combining reconcile + reconstruction.
- Risk rationale: per-task durability gating runtime launches; lossy reconcile paths decide rework volume.

### Domain V — Verification & QA private authority

**V1. verify-digest-gating** — risk: critical
- Behaviour: review freshness = recorded ArtifactDigest vs current bytes + governed-input manifest match; smoke input fingerprint = sha256 over manifest/review.md/verdict/scope/harness/prereqs/evidence; expired-attempt reconciliation (dead PID or >2h) converts active→timed_out and fails the stage; `--yes` override requires ForceReview + rationale, stale review not overridable; strict-snapshot-freshness switches compile-time default false so disabled branches sit dormant next to active ones.
- Entrypoints: `verify [--level|--suite|--force-review|--override-reason]`; smoke gating.
- Primary files: internal/sprint/{verify,freshness_policy,review_runtime_validation,smoke_protocol}.go
- Tests: verify_test (assessment truth table, freshness merge, predecessor rule); smoke_protocol_test (override ladder :195-205).
- Risk rationale: this is the quality gate everything else trusts; digest arithmetic mistakes launder bad reviews.

**V2. qa-private-store** — risk: high
- Behaviour: fingerprint-derived immutable map → bounded shard fan-out → challenger → deterministic synthesis; private 0700/0600 pointer-last persistence with writer-token fencing tied to operational attempt generation; symlink-component escape rejection; 128 MiB hard budget; prune keeps newest N + protected attempt; recovered states refuse active phases; interruption reconciliation flips shards to interrupted and republishes terminal state.
- Entrypoints: `sprint qa start|resume|cancel|recover|status`.
- Primary files: internal/sprint/{qa,qa_map,qa_state,qa_synthesis,qa_prompt,qa_types}.go
- Tests: qa_test (concurrency gauge, publication-failure shutdown, panic containment, cancellation persists); qa_state_test (:119-165 pointer-last, :207-215 symlink root refused, fence cases); qa_map_test (order-independence); qa_synthesis_test (determinism).
- Risk rationale: newest subsystem; permission/fencing mechanics hand-rolled.

**V3. review-smoke-artifacts** — risk: normal
- Behaviour: review.md atomic write with hooks; smoke.md atomic; area-reasoning candidates written plain then atomically renamed; snapshot inputs 0400; stage-session checkpoints (no fsync) with continue/clear/restart semantics ignoring prompt checksum by design; restart discards coverage+sessions.
- Primary files: internal/sprint/{review,smoke,smoke_author,session_state,verification_phase}.go
- Tests: review_test (extraction trio, repair, fan-out bound); smoke_test (~45-clause authoring assertion, malformed-commit preserve); session_state_test (:50-170).
- Risk rationale: mostly covered; residual risk concentrated in non-fsync session writes during long planning chains.

### Domain W — Serving & orchestration ephemeral authority

**W1. web-operation-hub-shutdown** — risk: high
- Behaviour: bounded in-memory operation registry projecting onto durable runcontrol runs (op ID replaced by run ID on Accept); SSE replay with gap accounting and stable event-name contract regex-pinned against shipped JS; graceful drain cancels non-terminal ops reason `server_shutdown`, waits ≤10s, and only past the deadline persists per-record cleanup-uncertain markers (sprint and study flavours) **intentionally without leases**; startup ReconcileOperations replays them fail-closed; anonymous HMAC cookie + CSRF + Origin + loopback-only policy declared env-immune.
- Entrypoints: `ultraplan serve`; web routes.
- Primary files: internal/web/{server,operations,handlers,routes,security,server_policy}.go; internal/app/{web_usecases,serve_commands}.go
- Tests: operations_contract_test (SSE vocabulary vs static/js/sse.js); packaging_test (real binary/socket/SIGINT); api_compatibility_test.
- Risk rationale: the only place where crash semantics get *written down* after the fact; marker-less-lease design plus 10s bound defines what counts as uncertain.

**W2. controlled-runtime-fencing-wrapper** — risk: high
- Behavior: wraps CLI/TUI/web runtimes into fenced DB-backed runs: Accept→Claim→Fence, 250ms coalesced OnEvent, 1s/5s/10s control goroutine, 5s retry helpers, FinishOperation terminal propose; per-workspace repo cache with policy-drift rejection; TUI shares use-cases and prefers durable cancel.
- Primary files: internal/app/{run_control,durable_operations,operation_runner,usecases}.go; internal/tui/app.go
- Tests: app-level run_usecases/study_runs tests; runcontrol integration helper-process suite.
- Risk rationale: every runtime's correctness depends on this wrapper's lease/heartbeat discipline matching runcontrol expectations.

**W3. runtime-store-hygiene** — risk: normal
- Behaviour: per-owner hashed opencode.db stores with store.json PID records; dead-PID >30min retained-store cleanup; retained expiry/quota eviction (72h/2GiB callers); log prune 48h/128MiB; session GC under package mutex with VACUUM/checkpoint ×20.
- Primary files: internal/platform/runtime/{store,runtime,opencode,opencode_maintenance}.go
- Tests: runtime package unit tests; isolated RuntimeStorePath assertions in study run_loop_test.
- Risk rationale: background deletion near live agent state; GC misfires destroy resumable sessions.

### Domain C — Configuration & layout authorities (context tier)

**C1. config-precedence** — risk: normal — env table overrides config file (config.go:122-148); study.json strict/missing-defaults; sprint template precedence project→workspace→builtin; smoke child-env allowlist; web policy explicitly env-immune. Files: platform/config/{config,qa,redaction}.go; study/config.go. Tests: config_test taxonomy; smoke flag-parity tests.
**C2. project-workspace-layout** — risk: normal — workspace init/validation/skills; project discovery, roadmap/status parsing, index canonicalization; store_fs persistence for roadmap artifacts. Files: workspace/{init,paths,discovery,validation,skills}.go; project/{service,store_fs,roadmap,roadmap_status,index,discovery}.go. Tests: project package suites; workspace init tests.

---

## Part 3 — Seams between surfaces

**X1. One file, two packages** — runcontrol and productstate open independent sql.DB handles on `.ultraplan/run-control.db` with identical pragmas (runcontrol adds `_defensive`); concurrent open expected ("multi-process repository" comment sqlite.go:41-42; storage migrate opens both in one command). Immediate-tx locking is the arbitration mechanism; runcontrol pins it in tests (:398), productstate relies on the driver.
**X2. File↔DB dual-home activation** — presence of the DB file silently switches three state families to DB-authoritative (study/state_database.go:70-80; sprint/state.go:21-32, execute_state.go:36-47); conversion only via `storage migrate`; partial parity rules differ per family (study: file rewritten only on Complete; sprint flow: checkpoint gate flowStateCheckpoint state.go:233; sprint execute: file written when all tasks terminal execute_state.go:105-130).
**X3. Shutdown ordering chain** — signal ctx (cmd/ultraplan/main.go:19) → web drain (≤10s) → deadline → per-record cleanup-uncertain markers written **leaseless** (study/cleanup_uncertain.go:28-61; sprint/cleanup_uncertain.go:31-57) → hub in-memory `cleanup_uncertain` doc state → process exit; next startup reconciles markers fail-closed (study cleanup_uncertain_test:63; sprint locks.go:101-108).
**X4. Publish-after-commit seams** — study run_loop performs final save+history sync then publishes run-state.json+tasks.jsonl+summary.md, returning publication failure only after state is committed (run_loop.go:459-473; publication.go:45-58); sprint stage runners save flow-state then publish owned path sets; one shared `ultraplan-publish.lock` serializes all publications.
**X5. Read-side mutation asymmetry** — `sprint.Status` may **write** flow-state when `statusWrites` (service.go:229,291-295) while study status only reconciles in memory (study_commands.go:1129-1135); web timeline reads runcontrol only (timeline_handlers.go:82); TUI reads via app use-cases; web import boundary enforced by AST test (import_boundary_test.go:13-33); tui imports sprint solely for result types (tui/views.go:357-368).
**X6. Clock pluralism** — runcontrol injectable `Clock` drives leases/grace/retention/julianday predicates; sprint `WithClock` feeds attempt-expiry and triggers persist-failure tests; study uses raw `time.Now().UTC()` everywhere including retry delays; sprint verification expiry (2h heartbeat) and runcontrol lease (15s) never consult each other despite gating overlapping operations.
**X7. Lease layering** — sprint stacks three mechanisms (in-process sync.Map → pidfile → verification-attempt heartbeats); study uses one O_EXCL lock with kill(0); runcontrol uses DB leases with birth tokens. Cross-family operations (server shutdown markers) deliberately bypass all of them (X3).
**X8. Legacy strata** — flow v1→v2 in-memory migration with `"legacy-unverifiable"` digests; schemaVersion-0 terminal execute-state as immutable history excluded from both resume and reconciliation; runcontrol backup/restore as the DB-level analogue. Each stratum changes which writer may touch a file (locks.go:64,98).

---

## Part 4 — Cross-cutting facts for downstream phases

- Zero-coverage state code: `internal/productstate` (0%, no test file); study/sprint `state_database.go` branches never exercised in-package; memory_pressure readers have no injection seam.
- Non-atomic durable writes classified: runs/summary.md (os.WriteFile), execute.md (os.WriteFile), .stage-sessions.json (rename, no fsync) vs atomic-with-dir-sync flow-state/run-state/review.md/smoke.md/QA records.
- Env knobs touching state sizing/aging: ULTRAPLAN_RUN_CONTROL_FULL_HISTORY/_TOMBSTONE_HISTORY/_WORKSPACE_QUOTA_BYTES; study/sprint GC constants are call-site literals (72h/2GiB).
- Baseline: go test ./..., -race, vet, cover all green (state.json baseline section); CI = gofmt + go test ./... + cmd build only.
