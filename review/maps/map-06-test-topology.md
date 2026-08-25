# map-06-test-topology — Test-Topology Surface Map (independent pass)

Target: ultraplan-go @ f0fcd0c2107a8e8d69e1283f9e8d5e2c6da94025 (frozen). Workspace: ab12dc38059c9bf485f9aced9075bcd7d924cac5 (context only).
Method: six bounded review-worker discoveries (study, sprint, platform, app/CLI, web+tui, runcontrol/cross-cutting — the last partially re-done directly) + direct reads. Facts only; no findings. Coverage figures from `go test -cover` baseline.

## Repo/test topology overview

- 17 packages; 206 production .go files, 135 *_test.go files (~31 in internal/app alone). Baseline: all green incl. -race. CI = gofmt check + `go test ./...` + build of ./cmd/ultraplan (.github/workflows/ci.yml) — no race lane, no vet lane in CI.
- Harness styles, by package:
  - In-process fakes behind minimal consumer interfaces (`study.Runtime`, `sprint.Runtime { StartRun }`) — agent responses simulated by writing expected artifacts or emitting structured events.
  - Real subprocess harnesses: process_test.go (real /bin/sh children), runcontrol/process_integration_test.go (test binary re-execs itself as helper processes), web/packaging_test.go (builds real binary, real socket, SIGINT).
  - Real git repos in temp dirs (gitpublish), real SQLite files everywhere durable state is tested (runcontrol), chmod/directory-substitution sabotage for write-failure paths, atomic-write hook injection (`atomicWriteHooks.BeforeRename`), injectable clocks (`WithClock`, runcontrol `mutableClock`).
  - Env-gated lanes: `ULTRAPLAN_REAL_SMOKE=1` (internal/sprint/smoke_test.go:TestRealSmokeHarness); helper-mode env gates in runcontrol. `testing.Short` skips only packaging_test.
- Contract-freeze tests exist for: web API (api_compatibility_test.go ↔ docs/web-compatibility-baseline.md), browser SSE vocabulary vs shipped JS (operations_contract_test.go regex-parses static/js/sse.js), QA canonical fixture (internal/testdata/qa-canonical-v1.json shared by app/tui/web), CLI exit classes and JSON envelopes, sprint verify/smoke flag parity.
- Architecture self-policing: import-boundary AST tests in runcontrol (stdlib + modernc.org/sqlite + x/sys only) and web (stdlib + internal/app only).
- Zero-coverage areas: cmd/ultraplan (wired indirectly via web packaging test), internal/productstate (0%, no test file anywhere), internal/platform/filesystem (doc-only empty package), logging package (no external callers).

## Candidate surfaces

### Domain A — Study analysis (agent-backed research)

**A1. study-authoring-validation** — risk: normal
- Behaviour: workspace study init (clone optional), study.json config precedence/validation, prompt rendering determinism, report/final-report validation schema, rating parsing, summary.csv regeneration, validate command aggregation with secret non-leakage, publication of completed executions.
- Entrypoints: `study init|list|<s> prompt|<s> summary|<s> validate`; publication hook post-run.
- State: studies/<s>/{study-init.yml,study.json,dimensions,sources,reports/,summary.csv}; `.ultraplan/run-state.json` (read-side validation only here).
- Primary production files: internal/study/{init,init_clone,init_render,init_yaml,config,prompts,validation,validation_command,rating,summary,markdown,applicability,discovery,resolve,publication}.go
- Protecting tests: study/init_test.go (dry-run purity, clone partial failure ErrInitPartial, credential redaction, OutputDir escape rejection), config_test.go (strict rejection taxonomy, unknown fields rejected), prompts_test.go (byte-equal determinism), validation_test.go (section/rating/citation checks incl. extension allowlist), validation_command_test.go (aggregate status, run_state.parse, secret non-leakage), summary_test.go (determinism, read-only-dir write failure preserves prior summary), rating_test.go, publication_test.go (fake Publisher; publishExecution paths pinned; publishRunAllSummary/publishRunLoopState unpinned).
- Failure semantics exercised: partial clone failure, overwrite refusal, malformed frontmatter, oversized output redaction, chmod-blocked writes.

**A2. study-run-execution** — risk: normal
- Behaviour: single analysis/synthesis task execution through a runtime: request mapping (workdir, deny-by-default policy, model override precedence, timeout, metadata), session continuation gated on input fingerprint with one-shot fresh-session fallback, validation expectation mapping per check, clean-exit recovery when a validating report exists despite runtime_exit, source-edit warnings, skip for inapplicable sources, session deletion after success, synthesis preflight blockers.
- Entrypoints: `study <s> run <dim> <src>`, `study <s> synthesize <dim>`; app layer stubs `studyRuntimeFactory`.
- State: reports/source/<ref>/<source>.md, reports/final/<ref>.md, session checkpoints inside run-state.
- Primary production files: internal/study/{run,service,runtime_validation,runtime_metadata,edit_warnings,durable_metadata}.go
- Protecting tests: study/run_test.go (fakeRuntime/continuationFallbackRuntime; ~14 tests covering continuation, fallback-once, validation mapping, edit warnings, model precedence, session delete assertions), runtime_validation_test.go (repair spec shape: max 2 attempts, continue-same-session), runtime_metadata_test.go, app/study_run_commands_test.go + study_prompt_commands_test.go (CLI-level, no-runtime boundary).
- Failure semantics exercised: runtime failure taxonomy, validation failures per check name, rename-failure preserving prior state, oversized-diagnostic compaction.

**A3. study-runloop-scheduler** — risk: high
- Behaviour: durable run-loop orchestration: worker-slot refill without batch barrier, priority dimension tiers, per-dimension early synthesis, parallelism bounds + cancellation accounting, resume/revalidate/reset-with-archive semantics, cancelled-session checkpoint persistence/continuation, lock acquire/conflict/force-unlock/stale-PID replacement, disk-pressure admission cap arithmetic, cleanup-uncertain marker reconciliation (fail-closed), append-only run history ledger (trailing-record tolerance, dedupe), diagnostics sampling, run-loop publication triple.
- Entrypoints: `study <s> run-loop [--reset|--force-unlock]`, `study <s> run-all`, `study runs summary`; server-shutdown path calls RecordCleanupUncertain.
- State authorities: `.ultraplan/run-state.json` (atomic temp+fsync+rename+dir-sync), `runs/tasks.jsonl`, lock file with PID/command payload, `cleanup-uncertain.json`.
- Primary production files: internal/study/{run_loop,run_all,run_state_domain,state,locks,cleanup_uncertain,run_history,run_history_summary,disk_pressure,memory_pressure,run_loop_diagnostics,publication}.go
- Protecting tests: run_loop_test.go (channel-guarded refill test with 15s fail-fast; resume/reset/cancel/tier ordering; isolated RuntimeStorePath per task), run_all_test.go (maxActive concurrency gauge, preflight-starts-zero-runtime, exact CSV), locks_test.go (processAlive var stubbed; conflict/dead-PID/live-PID), cleanup_uncertain_test.go (reconcile consumes marker; fail-closed retains it), run_history_test.go, disk_pressure_test.go (pure cap arithmetic), state_test.go (graph construction, resume revalidation, retry/status counts).
- Protection notes (facts): memory_pressure.go has no seam and no test; disk-pressure admission branch in RunLoop untested; DB-backed run-state branch (state_database.go → productstate) never exercised by any study test; CancelRunLoop SIGINT path and retry-wait loop (`waitUntilRetry`) untested in-package; concurrent save/debounce windows untested.

### Domain B — Governed sprint delivery

**B1. sprint-planning-chain** — risk: normal
- Behaviour: requirements → code-context → sprint-index → technical-handbook → area-reasoning → reasoning → plan stage machine: per-stage content validators, byte-stable shared prompt prefix (exact artifact bytes, fail-closed source resolution, TOCTOU replacement detection, 256 KiB budget), code-context read-only sandbox policy + relative-path citation enforcement, same-session structured repair (bounded), dry-run purity, prior-review direct-input injection window, template precedence project→workspace→builtin, evidence-path containment.
- Entrypoints: `sprint <p> <s> flow --to <stage>` (+ prompt/validate/dry-run variants).
- State: sprint dir artifacts (requirements.md, code-context.md, sprint-index.md, technical-handbook.md, reasoning/*.md, reasoning.md, plan.md), flow-state.json transitions, session checkpoints scoped by provider/model/workdir.
- Primary production files: internal/sprint/{flow,service,index,validation,code_context,context_pack,prompt_context,prompt_bundle,input_contract,direct_inputs,handbook,reasoning,plan,prompts,session_state,artifacts,store_fs,discovery}.go
- Protecting tests: prompt_context_test.go (byte-exactness, escape/symlink/replacement detection, one identical prefix across previews and runtime requests), code_context_test.go (12 mutation cases incl. ../secret, inverted ranges, 64 KiB budget; failed rerun preserves valid artifact; state-persistence failure restores prior artifact; fail-closed outcomes matrix), sprint_index_test.go (canonical order, parse validation, panicRuntime proves preview runtime-free), handbook_test.go, reasoning_test.go, plan_test.go (10-case mutation table), session_state_test.go (checkpoint continue/clear/404-restart/checksum tolerance), direct_inputs_test.go (ordering, no starvation, redaction).
- Failure semantics exercised: runtime failure leaves no candidate artifacts, cancellation/interrupted/cleanup_uncertain outcome projection, repair exhaustion, mutation-conflict typed error.

**B2. sprint-execute-resume** — risk: high
- Behaviour: plan-task extraction with stable IDs and agent deferral markers; execute queue over shared runtime session with compact continuations (fresh fallback), stale-running reconcile to failed, checkpoint-loss fails task without losing queue discipline (stop-on-first-failure), deferral requires rationale, resume validation against checked tasks + complete records, execute summary writing, legacy terminal run-state treated as history not resumed.
- Entrypoints: `flow --to execute`, `execute [--resume|--defer --task --reason]`.
- State authority: `.run-state.json` (per-task terminal states, attempts, diagnostics).
- Primary production files: internal/sprint/{execute,execute_plan,execute_state,execute_target,execute_model}.go
- Protecting tests: execute_plan_test.go (extraction incl. deferral marker, resume keeps checked parent+children), execute_state_test.go (recordingExecuteRuntime, directory-instead-of-file sabotage for checkpoint failure, WithClock-triggered persist failure before runtime start, shared-session continuation metadata, fresh-fallback mode, deferred rationale), execute_target_test.go, execute_model_test.go, locks_test.go (ReconcileInterruptedMutation leaves legacy terminal state untouched, rejects unrecognized malformed state).
- Protection notes: no end-to-end Execute(Resume:true) against mixed terminal/running/pending state combining reconcile + queue reconstruction; direct `sprint execute` subcommand has thin CLI-level coverage (deferral parsing only).

**B3. sprint-review-smoke-gating** — risk: critical
- Behaviour: review fan-out with configured bound producing structured ReviewCoverageResult JSON; verdict ladder (finding w/o action ⇒ blocked; blocker severity ⇒ fail; out-of-manifest/out-of-range citations ⇒ blocked; duplicate IDs ⇒ blocked); exactly-one structured-output repair then block; artifact preservation on malformed rerun; input fingerprint rebasing of validated coverage; focused reruns merging single coverage; restart discarding coverage+sessions; smoke protocol gating on current/valid review digest, force+confirmed override with rationale for failed/blocked reviews, scope-completeness mapping rules, manifest v1 validation (paths contained, env allowlist, timeouts, safe argv), protected-write attribution, environment preservation (PATH/HOME); verify assessment precedence truth table (malformed→blocked … n/a→not_applicable) and diagnostic overrides cannot launder a failed review into success; expired-attempt reconciliation via heartbeat age + PID liveness.
- Entrypoints: `review [--resume|--focus|--restart]`, `smoke [--yes|--dry-run]`, `verify`, `flow --to smoke|verify`.
- State: review.md + verification/ private store, smoke.md, verification attempt locks.
- Primary production files: internal/sprint/{review,review_runtime_validation,smoke,smoke_protocol,smoke_author,smoke_types,verify,verification_lock,verification_phase,freshness_policy,runtime_validation}.go
- Protecting tests: review_test.go (OpenCode-shaped extraction incl. reasoning-object syntax, repair trio, fan-out bound, drift-reported-not-blocking, fingerprint ignores smoke-only roadmap noise), smoke_test.go (~45-clause authoring prompt assertion, commit-and-preserve-on-malformed e2e, real-harness env-gated lane), verify_test.go (assessment truth table, freshness merge, migration predecessor rule), verification_lock_test.go, verification_phase_test.go, efficiency_improvements_test.go (input packets exclude sibling coverage; area reasoning one-request-per-missing-template).
- Protection notes: Service.Verify composition never invoked directly in-package (only gate-level); publishReviewStage/publishSmokeStage lack dedicated publisher-injected tests; snapshot freshness compile-switches default false (documented design choice, digest checks still active).

**B4. sprint-qa-investigation** — risk: normal
- Behaviour: fingerprint-derived QA map (single ownership per changed path, boundary shards, risk tags), bounded concurrent investigator shards with read-only/default-deny prompts and fenced outputs (unknown fields/trailing JSON/oversize rejected), retry/turn budgets with stop reasons, challenger cross-examination, deterministic synthesis with contradiction handling and capped follow-ups, private 0600/0700 pointer-last store with writer-token fencing and symlink-escape rejection, QAFlowSummary pointer into flow-state, interruption reconciliation.
- Entrypoints: `qa start/resume/cancel/recover/status`.
- Primary production files: internal/sprint/{qa,qa_map,qa_state,qa_synthesis,qa_prompt,qa_types}.go
- Protecting tests: qa_test.go (concurrency gauge, publication-failure worker shutdown, panic containment, permission/target/map-drift rejection, cancellation persists interrupted shard), qa_map_test.go (byte-stable map independent of change order), qa_state_test.go (enums, IDs, modes, fences, rename-failure preserves prior), qa_synthesis_test.go (determinism under reorder, forbidden-field scan), qa_prompt_test.go (denied tool catalog, approved-check argv allowlist, post-execution drift detection).
- Protection notes: full RunQA pipeline not driven end-to-end in-package; components tested separately; app-layer error mapping covered in app/qa_errors_test.go.

**B5. sprint-state-publication** — risk: high
- Behaviour: flow-state.json strict loading (schema version gate v1→v2 migration once, DisallowUnknownFields, single JSON value, per-stage order/path-containment validation), atomic writes preserving prior bytes on rename failure, save-preserves-unspecified-records, session checkpoints, mutation lease (in-process + O_EXCL pidfile with live/dead discrimination), publication of completed stages to git with exact owned path sets, cleanup-uncertain recording/reconciliation, legacy reader compatibility (pre-code-context projections, legacy v2 published-QA classification).
- Entrypoints: every sprint operation; status refresh materializes missing state.
- Primary production files: internal/sprint/{state,state_database,store_fs,locks,verification_lock,cleanup_uncertain,publication,runtime_metrics}.go
- Protecting tests: sprint_test.go (strict load/atomic write preserves prior; status refresh/malformed rejection), verify_test.go (exactly-one-predecessor migration), code_context_test.go (pre-code-context compatibility), locks_test.go, cleanup_uncertain_test.go, publication_test.go (planning path set; target-before-workspace order for execute).
- Protection notes: state_database.go (productstate-backed authoritative branches for FlowState/ExecuteRunState) has zero test references anywhere; MigrateFlowStateToDatabase/MigrateExecuteStateToDatabase unpinned; storage migrate command also untested at app layer.

### Domain C — Durable operations core

**C1. runcontrol-durable-journal** — risk: high
- Behaviour: durable operational run identity: Accept→Claim(fenced attempts)→Append(sanitized ordered events)→Heartbeat(lease)→RequestCancellation/Acknowledge→ProposeTerminal(single immutable arbitration winner, CAS), Reconcile (exact process-birth identity via /proc, never infers success from PID presence; grace-period terminalization of never-claimed acceptances; clock-jump safety), retention (per-run event limit advancing replay boundary, tombstone compaction, soft quota rejecting acceptance with reserved headroom reporting), sanitization of hostile payloads and oversize replacement warnings, local private bounded redacting log, ID entropy/canonicality, schema migrations (version records, backup, integrity check, proven-stale lock reclaim, corrupt-DB refusal without replacing evidence), symlink database-boundary rejection, reopen persistence.
- Entrypoints: consumed exclusively via app (run_commands, run_control, durable_operations, web_usecases, run_usecases, storage_commands).
- State: .ultraplan/run-control.db (SQLite WAL, foreign keys, immediate txlock).
- Primary production files: internal/runcontrol/{sqlite,lifecycle,model,migration,retention,sanitize,id,local_log,process,process_linux,metrics,errors}.go
- Protecting tests: sqlite_test.go (private schema, correlation-field safety, monotonic sequences under concurrent writers, terminal CAS single winner, context-aware transactions), fault_test.go (PRAGMA max_page_count induced quota-full never returns uncommitted success; closed repository typed failures; query_only permission loss rejects all mutations with unchanged snapshot), lifecycle_test.go (fenced idempotent heartbeat/cancel, completion-vs-cancellation race one winner, birth-identity reconcile, clock-jump, mutableClock), process_integration_test.go (two real helper processes share one durable sequence 1..40; cross-process cancellation persisted by observer; owner-exit reconcile idempotent), migration_test.go, retention_test.go, sanitize_test.go, local_log_test.go, id_test.go, import_boundary_test.go, benchmark_test.go (append + replay-page benchmarks; no thresholds asserted).
- Harness style: file-backed SQLite in TempDirs, injected Clock and ProcessProbe, self-re-exec helper pattern keyed on ULTRAPLAN_RUNCONTROL_HELPER.

**C2. app-durable-operation-boundary** — risk: critical
- Behaviour: every runtime-backed CLI/TUI/browser operation passes through durable acceptance before child work begins (accept+claim persisted first, event journal, sanitized progress, fenced terminal finish); confirmation-digest dedup across manager instances (Existing=true); closed repository fails closed; controlledRuntime wraps child runtime so claim/commit precede event delivery, persistence failure prevents child start, runtime errors are persisted without leaking raw text; tool-observability drafts preserve tool id/args/result with secrets [REDACTED]; QA ownership token fencing from context; source-level inventory test statically pins that all runtime-backed entries use beginDurableCLICommand (6 sprint + 4 study sites).
- Entrypoints: sharedOperationRunner kinds (OperationFlow/VerifyStart/ExecuteStart/ReviewStart/SmokeStart/QAStart/QAResume/StudyStart/StudyResume/StudyCancel) used by CLI, TUI, web alike.
- Primary production files: internal/app/{operation_runner,durable_operations,run_control,operations,json_output,status_json}.go
- Protecting tests: durable_operations_test.go (accept-before-execute, dedup, fail-closed, stale fence generation rejected, message omission but safe fields retained), run_control_test.go (controlledRuntime quartet), run_control_inventory_test.go (static completeness guard), run_tool_observability_test.go, run_commands_test.go (list/show/follow journal replay/cancel/diagnostics support export ≤1 MiB mode 0600; --help does not open SQLite).
- Protection notes: controlOperation heartbeat/reconcile-tick loop and `run follow` live idle polling untested; `run list` filter flags (--sprint/--study/--lifecycle/--limit/--after) unexercised.

### Domain D — Platform infrastructure

**D1. runtime-opencode-adapter** — risk: high
- Behaviour: Adapter.StartRun maps requests into agentwrap SDK runs: metadata/database-path/cache-key injection, 200-event ring with drop accounting, terminal-output capture from nested payloads, cancellation propagating to underlying run with ≤5 s wait, error classification/redaction to [REDACTED], bounded diagnostics, usage/cost/attempt/policy metadata, validation+repair spec plumbing; opencode stack composition (ObservingRuntime{ValidatingRuntime{PolicyRunner{variant routing, missing-session stop-policy}}}), backup-model fallback, session deletion via SQL through CLI binary + WAL checkpoint retry + VACUUM; runtime stores scoped by sha256(owner) under .ultraplan/runtime/opencode/, atomic store.json, dead-owner retention ("interrupted, not disposable"), aggressive cleanup sacrificing retained not live; OpenCode log pruning (48 h / 128 MB quota, never deleting files modified within 10 min).
- Primary production files: internal/platform/runtime/{runtime,agentwrap,events,health,models,opencode,opencode_maintenance,store,policy}.go
- Protecting tests: runtime_test.go (~15 tests: event/usage/error mapping, rate-limit RetryAfter preservation, redaction, ring retention, cancellation mapping, malformed-event safety, health/capability name validation), opencode_test.go (variant argv, missingSessionPolicy, sqliteString escaping), opencode_maintenance_test.go (synthetic mtimes for expiry/quota/recent-preserve), store_test.go (lifecycle, pending expiry, unmanaged path rejection, dead-PID preservation, aggressive cleanup), cache_test.go (cohort cache-key derivation).
- Protection notes: NewOpenCode construction, checkpoint/VACUUM/session-deletion exec paths, Adapter.Health/Capabilities/ListModels/DeleteSession(s)/DeleteRuntimeStore, RequestFromConfig, sanitizeAnySlice slice branch — 0% coverage; no test fakes or spawns the OpenCode binary itself (SDK interfaces faked instead).

**D2. process-control** — risk: normal
- Behaviour: DirectRunner spawns owned process groups (Setpgid), streams events with slow-consumer-safe drain and stdout truncation bounds, context cancel SIGTERMs group then SIGKILLs after grace with leader-exits-first race documented, timeout produces TimedOut+CleanupComplete with partial output, signal capture.
- Primary production files: internal/platform/process/{process,process_unix,process_other}.go
- Protecting tests: process_test.go (real /bin/sh descendants; poll kill(pid,0)+/proc state Z for group death; 5000-event drain non-blocking; 50 ms timeout vs 5 s child).
- Protection notes: already-dead PIDs, spawn permission errors, stderr truncation, DroppedEvents counter, Result.Signal population untested; process_other.go excluded by build tags on this host.

**D3. git-publication** — risk: normal
- Behaviour: opt-in publication modes off/commit/commit-and-push; temporary index seeded from parent commits exactly owned paths while preserving user's staged index; UltraPlan-Publication trailer; update-ref CAS against expected parent ("branch changed while committing"); push with timeout mapping and GIT_TERMINAL_PROMPT=0; flock-based repo lock with ctx-cancellable retry; remote-name validation; detached-HEAD refusal; path-escape rejection.
- Primary production files: internal/platform/gitpublish/{publisher,lock_unix,lock_other}.go
- Protecting tests: publisher_test.go (real temp git repos: owned-paths-only commit preserving user index; push-failure retry pushes same commit without duplicate).
- Protection notes: detached HEAD/off-mode/escape/invalid remote/lock contention arms untested in-package (Publish 63.9%); no worktree fixture.

**D4. config-redaction** — risk: normal
- Behaviour: four-layer precedence defaults→workspace ultraplan.yml (hand parser, 5 list fields)→env (27+ generated keys)→CLI with per-field provenance; extensive Validate bounds (timeouts, quotas, git mode/remote charset, run-control floors, env names, agentwrap allow-lists); QA lower-only limits vs hard ceilings (28 fields); Sensitive-marker redaction ([REDACTED]) applied to values and messages in the logging package; Redact() projection of Effective config.
- Primary production files: internal/platform/config/{config,qa,redaction}.go
- Protecting tests: config_test.go (~20 subtests incl. 26 QA upper-bound mutations, malformed env not claiming env provenance, unknown-field ordering), logging_test.go (text+JSON redaction).
- Protection notes: internal/platform/logging has no callers outside its own tests (facts); level field stored but never consulted as a filter.

**D5. workspace-project-skills-codeextract** — risk: normal
- Behaviour: workspace discovery precedence explicit>env>parent-walk keyed on ultraplan.yml; ResolveInside containment; init plan/idempotence/validate/embedded defaults; skills embedding contract (exactly 11 skills, per-stage delegation rules pinned in prose-heavy tests, idempotent materialise, Force restore); project catalog/roadmap parsing with status-aware validation severities; roadmap delivery marking idempotent preserving 0o640; reasoning-default three-tier resolution; codeextract citation resolution (inline ranges, en-dash normalization, basename fallback, ignored dirs, escape/out-of-range diagnostics, StatusPartial aggregation).
- Primary production files: internal/workspace/*, internal/project/*, internal/codeextract/{service,domain}.go
- Protecting tests: workspace_test.go, skills_test.go (heavy delegation-rule contracts), project_test.go, roadmap_test.go, reasoning_defaults_test.go, codeextract_test.go; app-layer counterparts (skills_commands_test, project_commands_test, code_commands_test).
- Protection notes: internal/platform/filesystem is an empty doc-only package.

**D6. productstate-store** — risk: high
- Behaviour: pure-Go SQLite KV at fixed .ultraplan/run-control.db path (separate tables product_states/product_state_items, FK cascade), DSN hardening (busy_timeout, WAL, synchronous=FULL, txlock=immediate), MaxOpenConns 4, sha256-hash-guarded conditional upserts with stale-item deletion in one transaction, process-wide instance cache, Ensure/Existing split.
- Consumers (production): internal/sprint/state_database.go (kinds sprint_flow/sprint_execute), internal/study/state_database.go (kind study_run), internal/app/storage_commands.go (`storage migrate`).
- Protecting tests: none anywhere in the repo (package 0%; no test references any consumer of these branches; storage migrate has zero app-layer tests).
- Note: this surface sits beneath the authoritative state paths of B5/A3; its branches activate when the productstate DB exists/is enabled, which standard fixtures do not create — hence untested in all downstream suites too.

### Domain E — Observation surfaces

**E1. web-dashboard-api** — risk: high
- Behaviour: hand-rolled router (~45 HTML/API/static routes) with layered security middleware (request-target ≤8 KiB, framing ambiguity, Host==canonical listener authority, signed HMAC session cookie for mutations, exact-Origin checks with Sec-Fetch-Site/Referer proof path, constant-time CSRF, body ≤64 KiB restricted to operation POSTs, MaxInFlight=32 semaphore); strict identifier/opaque-ref validation; static asset allowlist; fail-closed template hierarchy (namespace ordering, cycle detection); operation hub (per-session isolation, capacity 8, replay bounds, slow-subscriber eviction, drain with deadline persisting cleanup-uncertainty before terminal projection, SSE frame format + heartbeats + max stream lifetime); API compatibility freeze (18-route method matrix, reflection-derived DTO schemas, error codes, cache policy) bound bidirectionally to docs/web-compatibility-baseline.md; browser-client contract incl. SSE vocabulary cross-checked against shipped static/js/sse.js; loopback enforcement at CLI preflight, post-listen re-validation, and per-request Host equality; packaging test builds real binary, walks 18 routes over a real socket outside source tree, asserts no asset-path leaks, SIGINT clean exit.
- Entrypoints: `serve [--listen]`, browser operations (prepare/start/cancel, no-JS form flow), `/api/v1/*`, run/QA/timeline/artifact views.
- Primary production files: internal/web/{routes,handlers,security,server,operations,operation_handlers,run_handlers,timeline_handlers,qa_handlers,artifacts,dimensions,templates,sse,markdown? (rendered via app)}.go
- Protecting tests: security_test.go (15 tests incl. 127.0.0.2 rejection, Origin-null static exception, port-stripped origin proof chain, CL/TE smuggling 400, query-secret redaction in diagnostics), operations_test.go (15 incl. full HTTP prepare→start→SSE→cancel cycle and no-JS form flow), routes_test.go (inventory, envelope, cancellation propagation to queries, 404/405 semantics), server_test.go (banner, shutdown <2 s, truthful 503 health), integration_test.go (real app.NewWebUseCases end-to-end agreement HTML↔JSON + operation lifecycle), api_compatibility_test.go, operations_contract_test.go, templates_test.go (23), run_handlers_test.go (12 incl. two handlers over two SQLite handles sharing observation; legacy-ID 410), qa_handlers_test.go, artifacts_test.go (hostile markdown never raw), dimensions_test.go (real-workspace leaderboard), timeline_handlers_test.go, sse_test.go (frame format only), sprint_create_test.go, import_boundary_test.go, server_policy_test.go, packaging_test.go.
- Protection notes: no real network-drop SSE disconnect test; port-conflict path untested; MaxInFlight rejection branch untested; browser JS asserted by substring only (never executed).

**E2. tui-console** — risk: low
- Behaviour: bubbletea model with route stack, operation menu → OperationRequest mapping, confirmation card, foreground/background run semantics (esc hides without cancelling), durable cancellation key, event buffer clamped to 100, parallelism form validation, preview scroll/follow-selection math, QA cockpit views (verdict-neutral copy), durable-run view lifecycle vocabulary incl. stalled/gap/uncertain-cancel/owner_unreachable states, tick-driven refresh scheduling.
- Entrypoints: `tui`.
- Primary production files: internal/tui/{app,model,views,viewport,keys,run_view,qa_view,verify}.go
- Protecting tests: model_test.go (17, synthesized tea.KeyMsg + closure commands, deterministic), views_test.go (10, RenderWithSize string containment), run_view_test.go (dedupe + four-state matrix), qa_view_test.go (canonical fixture, forced fail verdict), viewport_test.go, verify_test.go, test_fakes_test.go (fakeUseCases).
- Protection notes: tui.Run/tea.Program itself never started in tests (no AltScreen/resize/theme coverage); ActionQuit-during-running guard untested; tuiSprintRuntimeProgress filtering exercised only transitively; no import-boundary test for tui.

## Cross-cutting seams (producer ⇄ consumer, seam-test status)

1. Agent runtime seam: consumer interfaces `study.Runtime` (service.go:27) / `sprint.Runtime` (flow.go) / app `controlledRuntime` (run_control.go:159) → platform/runtime.Adapter → agentwrap SDK → OpenCode binary. Seam heavily faked on the consumer side; adapter-to-binary hop (exec, checkpoint, deletion SQL) untested; no fake OpenCode binary exists anywhere.
2. Process seam: sprint smoke/QA consume pprocess.Runner (WithProcessRunner; fakes smokeRecordingRunner/qaProcessRunner); DirectRunner itself tested with real children.
3. Publication seam: WithPublisher(gitpublish.Publisher) injected by app/git_publication.go stagePublisher; study/sprint fakes record calls; real git tested only in gitpublish's own suite; review/smoke stage publishers lack dedicated pins.
4. State seams: run-state.json / flow-state.json / .run-state.json / verification/ private store / runs/tasks.jsonl (file authorities, well pinned) versus productstate DB authority (unpinned, activates only when DB present).
5. Durability seam: beginDurableCLICommand / DurableOperationManager / runcontrol.Repository — inventory test statically guards completeness at CLI layer; TUI/web route through same runner.
6. Observation seams: app.WebQueries + additive capability interfaces (type-asserted, graceful 503), WebOperations/DurableOperationManager/OperationCleanupRecorder, RunUseCases over runcontrol; SSE vocabulary frozen against shipped JS; cross-surface consistency (web vs TUI fixtures vs real use cases) asserted only in integration_test/dimensions_test pockets.
7. Boundary policing: AST import-boundary tests in runcontrol and web; none for other packages.
8. Config/redaction seam: config.RedactValue applied at runtime error mapping, logging, status_json, health; redaction behaviour pinned in each consumer's tests plus config_test.

## Smoke-path inventory

Automated (in-suite):
- Full-chain-with-fake-runtime: app TestSprintFlowNonDryRunUsesConfiguredRuntime; TUI full-flow streaming test; web integration + packaging (real binary/socket/SIGINT); study run-loop durable e2e trio; sprint smoke authoring e2e; gitpublish real-git pair; runcontrol multi-process journal tests.
Env-gated: internal/sprint TestRealSmokeHarness (ULTRAPLAN_REAL_SMOKE=1 + ULTRAPLAN_REAL_SMOKE_WORKSPACE) — the only automated lane touching a real OpenCode-backed harness.
Documented-manual only (no automation in scripts/ or CI): docs/opencode-smoke.md (gated real-runtime study smoke), docs/planning-smoke.md (offline planning checks + gated chain + disposable dogfood), docs/recovery.md runbook, docs/release-checklist.md gates (incl. byte-identical double `qa --dry-run --json`). dist/smoke-evidence.md is a historical record noting the gated OpenCode smoke was not re-run for the recorded release (cites external harness run IDs).

## Protection-gap register (facts; candidates for later review prioritization, not findings)

1. productstate branches: zero coverage at every level (package, consumers sprint/state_database + study/state_database, storage migrate command).
2. Runtime→binary hop: NewOpenCode, session-deletion SQL/checkpoint/VACUUM, Health/Capabilities/ListModels, RequestFromConfig untested; nothing exercises a real or scripted OpenCode process.
3. Memory-pressure throttle loop and disk-pressure admission branch in study run-loop untested (memory has no injection seam).
4. Study/sprint DB-authoritative load/save preference branches untested wherever the DB is absent from fixtures.
5. Service.Verify composition and review/smoke stage publishers unpinned directly.
6. Execute crash-resume end-to-end (mixed-state resume) not composed in any test.
7. controlOperation heartbeat/reconcile loop; run follow live polling; run list filters.
8. TUI program-level behaviour (resize, quit-during-run guard, theme) and real SSE network disconnects; port-conflict binding.
9. Process edge arms (dead-at-spawn, permission, signal reporting, DroppedEvents) and non-unix stopAndWait.
10. gitpublish contention/off-mode/detached/escape arms; no worktree scenario.
11. Requirements-content validator lacks systematic negative table; input_contract pinned for only 2 of ~11 stages.
12. No golden-file pinning of embedded skill/prompt bodies beyond substring assertions; browser JS never executed.
13. CI runs only `go test ./...` (no -race/-vet lanes) though baseline proves both pass today.

## Domain grouping & suggested review order

- Domain "governed-delivery" (B1–B5): highest concentration of externally meaningful gating; B3 critical, B2/B5 high.
- Domain "durable-operations" (C1–C2, D6): correctness of resumability claims; C2 critical, C1/D6 high.
- Domain "agent-runtime-platform" (D1–D3, A3): where agents actually execute and get cleaned up; D1/A3 high, D2/D3 normal.
- Domain "authoring-config" (A1, A2, D4, D5): normal.
- Domain "observation" (E1 high, E2 low): E1 security-critical localhost boundary with strong existing coverage; E2 presentation.

