This is a surface-map pass (`map-06-test-topology`) against `ultraplan-go @ f73c4dc`. I'll gather topology facts directly, then delegate bounded behaviour-coverage questions to `review-worker` subagents.Baseline topology facts gathered. Now delegating bounded behaviour-coverage discovery to five `review-worker` subagents in parallel.All five workers returned. Verifying the most load-bearing coverage-gap claims directly before synthesizing the map.All load-bearing gap claims verified directly (`productstate` zero test refs, `memory_pressure` zero refs, `storage` command zero refs, fixture consumers, doc-only filesystem package, gated smoke lane, HEAD = frozen commit). Synthesizing the map.

# map-06-test-topology — Test-Topology Surface Map (independent pass)

Target: ultraplan-go @ `f73c4dc659ba1492c16382be26ffcbce1a5ea84a` (frozen; verified `git rev-parse HEAD`). Workspace context only. Method: five bounded review-worker discoveries (study; sprint; runcontrol/platform/productstate/project/workspace/codeextract; app/cmd/QA-fixture; web/tui) + direct verification of every load-bearing claim (greps re-run personally; coverage collected fresh via `go test -cover ./...`). Facts only; no findings.

## Repo/test topology overview

- 17 packages; 206 production `.go`, 135 `_test.go`. Baseline (`review/baseline/`, state.json): `go test ./...` and `go test -race ./...` both passed.
- CI (`.github/workflows/ci.yml`): gofmt check + `go test ./...` + build `./cmd/ultraplan`. **No `-race` lane, no vet lane in CI** (race runs only in gauntlet baseline).
- Fresh coverage (this pass): app 57.9% · codeextract 78.9% · platform/config 71.3% · gitpublish 70.9% · logging 82.1% · process 88.9% · runtime 58.7% · productstate **0.0%** · project 81.9% · runcontrol 68.7% · sprint 70.9% · study 75.7% · tui 68.3% · web 77.4% · workspace 79.5% · cmd/ultraplan **0%** · platform/filesystem no test files (doc-only, sole file `doc.go`).
- Harness styles:
  - In-process fakes behind two consumer-side runtime seams: `study.Runtime` and `sprint.Runtime{StartRun(ctx, Request) (Result, error)}` (33 fake implementations in sprint alone). Agent responses simulated by writing expected artifacts (`writePlanRuntime`) or emitting structured events (`reviewRuntime`, `checkpointRuntime` emitting session IDs then `context.Canceled`).
  - Real subprocesses: platform/process (real `/bin/sh` children, `/proc/<pid>/stat` zombie checks); runcontrol helper-process pattern (test binary re-execs `os.Args[0]` under `ULTRAPLAN_RUNCONTROL_*` env gates); web/packaging_test.go (real `go build`, real listener, SIGINT); sprint real-smoke lane (`pprocess.DirectRunner`).
  - Real SQLite (modernc.org/sqlite v1.57.0) throughout runcontrol tests: WAL+synchronous=2+FK pinned by schema introspection (`sqlite_test.go:42-92`), reopen persistence, concurrent monotonic sequences, terminal CAS, crash-consistent backups.
  - Fault injection: `atomicWriteHooks.BeforeRename` rename failure (study `state_test.go:198`), clock-hook replaces state file with a directory (sprint `execute_state_test.go:233`), symlink swap (runcontrol `sqlite_test.go:197`), `PRAGMA query_only` read-only loss (`fault_test.go:96`), `stubProcessAlive`, package var `readSchedulerDiskPressure`, injectable clocks (`mutableClock` in runcontrol, `WithClock` in sprint service, `timeNow` var in app).
  - Mutation tables: code-context 12 cases incl. `../secret`, `/etc/passwd`, inverted ranges (`code_context_test.go:55-86`); plan validation 10 cases; smoke manifest protocol/timeouts/secret-redacting argv; execute-target workdir escapes; QA prompt arg/symlink escapes.
  - Contract freezes: API route/method matrix + reflected DTO field-order schemas (`api_compatibility_test.go`; baseline doc is prose-linked only, never parsed); browser SSE vocabulary extracted from shipped `static/js/sse.js` via regex on `Object.freeze([...])` (`operations_contract_test.go:142-174`); 27 event-kind strings; 8 lifecycle states incl. `cleanup_uncertain`; 13 error-code/status pairs; QA canonical fixture `internal/testdata/qa-canonical-v1.json` rendered through all three adapters (app JSON envelope `sprint_usecases_test.go:18`, web handlers with hostile `<script>` injection `qa_handlers_test.go:65`, TUI view text `qa_view_test.go:13`). Import-boundary AST self-policing in runcontrol (stdlib + sqlite + x/sys only) and web (stdlib + internal/app only).
- Gates/skips: `ULTRAPLAN_REAL_SMOKE=1` (+WORKSPACE/PROJECT/SPRINT) for exactly 1 of 14 smoke tests; `ULTRAPLAN_RUNCONTROL_HELPER/ACTION/ROOT/FENCE` helper-process gates; `testing.Short` skips only packaging_test.go:16; POSIX-only skips (platform/process ×4, app study_init fake-git shims ×2); platform identity Skip at runcontrol/lifecycle_test.go:322-325 when birth identity unavailable. No other env gates; runcontrol/platform tests are sleep-free (clock injection), sleeps confined to platform/process (60 ms cancel, 10 ms polls) and readiness polls (packaging 5 s/20 ms; server_test 2 s stdout poll).

## Candidate surfaces

### Domain A — Study analysis (agent-backed research)

**A1. study-authoring-validation** — risk: normal
- Behaviour: workspace study init (clone optional), study.json strict validation/precedence, prompt rendering determinism, report/final-report schema + rating parsing, summary.csv regeneration, validate aggregation with secret non-leakage, publication of completed executions.
- Entrypoints: `study init|list|<s> prompt|summary|validate|runs summary`.
- State: `studies/<s>/{study-init.yml,study.json,dimensions,sources,reports/,summary.csv}`.
- Files: internal/study/{init,init_clone,init_render,init_yaml,config,prompts,validation,validation_command,rating,summary,markdown,discovery,resolve,publication}.go.
- Tests: init_test.go (dry-run purity, partial-clone `ErrInitPartial`, credential redaction `[redacted]@example.com`, OutputDir escape), config_test.go (unknown-field rejection), prompts_test.go (byte-equal determinism), validation_test.go, validation_command_test.go (aggregate status, secret non-leakage), summary_test.go (read-only-dir write failure preserves prior summary), publication_test.go (only `publishExecution` pinned; `publishRunLoopState`/`publishRunAllSummary` unpinned).
- App layer: study_init/commands tests incl. POSIX-gated fake-git shim lanes (exit Partial=8 mapping).

**A2. study-run-execution** — risk: normal
- Behaviour: single analysis/synthesis task through `study.Runtime`: request policy (deny-by-default), model precedence, timeout, continuation gated on input fingerprint with one-shot fresh-session fallback, validation-expectation mapping, clean-exit recovery when validating report exists despite `runtime_exit`, edit warnings, session deletion after success.
- Entrypoints: `study <s> run <dim> <src>`, `study <s> synthesize <dim>`; app stub seam `studyRuntimeFactory` (package var, study_commands.go:22).
- Tests: run_test.go (13 tests; fakeRuntime/continuationFallbackRuntime), runtime_validation_test.go (repair spec ≤2 attempts same-session), app study_run/prompt/status/validate commands tests (status+validate proven runtime-free via stub assertion).

**A3. study-runloop-scheduler** — risk: high
- Behaviour: durable multi-task run loop: worker-slot refill without batch barrier, priority tiers, per-dimension early synthesis, parallelism caps + cancel accounting, resume/revalidate/reset-with-archive (`.ultraplan/archive`), lock acquire/conflict/force-unlock/stale-dead-PID replacement/live-PID block, disk-pressure admission cap arithmetic, cleanup-uncertain fail-closed reconciliation, append-only history ledger (trailing-line tolerance, dedupe).
- State authorities: `.ultraplan/run-state.json` (temp+fsync+rename+dir-sync via `saveRunStateWithHooks`, injectable `atomicWriteHooks`), lock file with PID payload, `cleanup-uncertain.json`, `runs/tasks.jsonl`.
- Tests: run_loop_test.go (15 s fail-fast channel guard proves no batch barrier; isolated per-task runtime stores; reset-archive bytes), locks_test.go (processAlive stubbed; dead-PID replaced :45, live-PID blocks :74), cleanup_uncertain_test.go (:11 consume, :63 fail-closed), run_history_test.go (:13/:30 malformed trailing tolerated, :41 dedupe), disk_pressure_test.go (pure cap math only).
- Untested facts (grep-verified this pass): `memory_pressure.go` zero test references (sole call site run_loop.go:348); DB-backed run-state branch (`state_database.go` → productstate) never executed — fixtures never create `.ultraplan/run-control.db` so `Existing()` always returns not-enabled; `waitUntilRetry` retry loop (run_loop.go:481) unreferenced; `CancelRunLoop` (locks.go:141) exercised by no study test and no app test (production caller app/operation_runner.go:158 `OperationStudyCancel`); admission-pause branch run_loop.go:376-400 (store cleanup, `<-done`, 30 s timer) unasserted.

### Domain B — Governed sprint delivery

**B4. sprint-planning-chain** — risk: normal
- Behaviour: stage machine requirements→…→plan with byte-stable shared prompt prefix (LF/CRLF/no-trailing-newline exactness, fail-closed source resolution, budget 256 KiB), code-context read-only sandbox + relative-citation enforcement, structured repair (bounded), dry-run purity proven by `panicRuntime`, handbook/reasoning template precedence, evidence-path containment.
- Entrypoints: `sprint <p> <s> flow --to <stage>`; session checkpoints scoped provider/model/workdir.
- Tests: prompt_context_test.go (11, byte-exactness + one-shared-prefix across previews/runtime), code_context_test.go (12 mutators; failed rerun preserves valid artifact :496), sprint_index_test.go, handbook/reasoning/plan tests, session_state_test.go (6: continue/delete-on-complete/prompt-change tolerance/interrupted-retention :172), direct_inputs_test.go.
- Thin spots (facts): store_fs.go has no dedicated read-error-path test; `runtime_validation.go` and `state_database.go` have zero filename test hits (transitive only).

**B5. sprint-execute-resume** — risk: high
- Behaviour: plan-task extraction with stable IDs + agent-deferral markers; execute queue over shared runtime session; stale-running reconcile to failed; checkpoint-loss fails task without queue-discipline loss; deferral requires rationale; resume validation vs checked tasks; legacy terminal run-state treated as history not resumed.
- Entrypoints: `flow --to execute`, `execute [--resume|--defer]`; `.run-state.json` authority.
- Tests: execute_plan_test.go (10), execute_state_test.go (8 incl. persistence-sabotage → failed :233, checkpoint-loss diagnostic :264, escape-table :91-160), execute_target_test.go (workdir containment :52).
- Fact: no test drives a task into `ExecuteTaskCancelled`; no mid-run ctx-cancel analogues to review_test.go:657/qa_test.go:263 exist for Service.Execute.

**B6. sprint-qa-review** — risk: normal
- Behaviour: QA gate (35 tests across qa_/qa_state/qa_map/qa_prompt/qa_synthesis) and review gate (22 tests): citation containment (`../../etc/passwd` rejected), shard options, synthesis; stable error classes `qa.runtime_unavailable`/`qa.invalid_state` pinned end-to-end at app level (exit 5/6/partial mapping, sprint_commands_test.go:190).
- Cross-layer: shares qa-canonical-v1.json fixture with web+tui (verified consumer set: exactly those three test files).

**B7. smoke-harness** — risk: normal
- Behaviour: cataloged external harness execution through manifest `discover`/`run` child processes; verdict taxonomy {pass, pass-with-open-issues, fail}.
- Harness duality: 13/14 tests default-on using `smokeRecordingRunner` (fake `pprocess.Runner` marshalling canned JSON to stdout) + `smokeAuthorRuntime`; only `TestRealSmokeHarness` (smoke_test.go:522) requires `ULTRAPLAN_REAL_SMOKE=1` plus an index-registered executable harness on disk (plain `NewService` leaves runtime nil → authoring failure swallowed by `t.Skipf` :540 unless existing-coverage fast path).
- Mutation table: protocol version, duplicate env keys, `0s/-1s/25h` timeouts, overlapping/root authoring paths, argv secrets.

### Domain C — Durability substrate

**C8. runcontrol-durability** — risk: high
- Behaviour: fenced run/attempt/event ledger in SQLite: schema hardening (dir 0700/db 0600, WAL, synchronous=2, FK), monotonic sequences under concurrency (in-process :287 and cross-process :64-80), stale-fence rejection, terminal CAS single-winner, idempotent heartbeats, cancellation-vs-terminal race arbitration, cleanup-uncertain on birth-identity mismatch, reconcile idempotence with real `NativeProcessProbe`, migration backups/integrity + corrupt-DB evidence preservation, typed persistence faults (`PRAGMA query_only`), retention/quota with 49 MiB fixture.
- Helper-process pattern: parent tests re-exec `os.Args[0] -test.run=^TestProcessWriterHelper$`; helpers perform accept (owner exits → reconciled Interrupted after grace without PID probe, using `mutableClock`), accept-claim (claimed-owner exit reconciled via real probe), cancel (cross-process `RequestCancellation` observed independently), and 20 fenced appends proving two-process gapless sequence.
- Import-boundary AST test forbids all imports except modernc.org/sqlite + golang.org/x/sys/unix.
- Fact: platform-dependent Skip at lifecycle_test.go:322-325 silences exact-birth-identity reconcile where the OS cannot expose it.

**C9. productstate-mirror** — risk: high
- Behaviour: SQLite key/state mirror at `<root>/.ultraplan/run-control.db` (WAL/FULL/immediate DSN, hash-guarded upserts, per-root singleton).
- Coverage facts (verified): 0.0% measured; zero `_test.go` references anywhere; importers are study/state_database.go:10, sprint/state_database.go:9, app/storage_commands.go:9; study/sprint fixtures never enable the DB branch (early-return not-enabled path only). The `storage` command (`runStorage`) has no test anywhere in the repo; ops script scripts/migrate-product-state.sh execs `$ULTRAPLAN_BIN storage migrate` against whatever binary is on PATH.

### Domain D — Delivery interfaces

**D10. cli-dispatch-envelopes** — risk: normal
- Behaviour: verb dispatch (`app.Run`), exit-class constants ExitOK..ExitPartial (app.go:15-25) with mapping errorCode() (:67-86), single-document JSON envelopes (schema_version 1, stable codes/categories, ANSI-free, redaction).
- Pinning tests: exit constants asserted 117×(OK)/27×(Validation)/11×(Workspace)/9×(Usage)/7×(Runtime)/7×(Partial)/1×(Cancel); **ExitConfig asserted 0×**. Envelope shape tests: sprint_commands_test.go:120/:149 (single JSON doc, no trailing output), health_commands_test.go:75 (redaction, no ANSI), study_status_commands_test.go:65.
- Isolation facts: no `t.Setenv("HOME"/"XDG_*")` anywhere; isolation relies solely on explicit `--workspace` flags; `Env: nil` falls back to real `os.Getenv` (app.go:300-307), so host env can reach command logic in tests.
- Zero-reference prod files: storage_commands.go, git_publication.go (stagePublisher; only fake-git partial failure), status_json.go, version.go default value.

**D11. web-api-sse** — risk: high
- Behaviour: 18-route HTTP API, DTO schemas, error/cache policy (`no-store`, unknown-query 400, wrong-method 405), operation sessions (cookie + `X-CSRF-Token` bootstrap, CSRF 403 `csrf_failed`, confirmation-token expired/mismatch/replayed/stale codes, origin/CORS rules, cross-session isolation), SSE hub (snapshot+terminal presence, slow-subscriber eviction after `SubscriberQueueSize+4`, `?after=`/`Last-Event-ID` replay with `cursor_ahead`/`replay_gap` tombstones), HTML sanitization against hostile `<script>` titles.
- Contract freezes: hardcoded route/method matrix failing on rename/removal/method change (DeepEqual on allowed-method sets); reflected DTO field-order schemas; SSE vocabulary regex-extracted from shipped static/js/sse.js `Object.freeze([...])` (8 events; brittle to formatting, hard-fails if absent); 27 kind strings; lifecycle states; error pairs.
- Facts: everything in-process httptest except packaging; SSEHeartbeat=15 s defined operations.go:30, validated only as a startup-policy invariant (server_policy_test.go), never exercised live; cookie attribute values (Secure/HttpOnly/SameSite), token entropy/TTL, logout/revocation untested; import-boundary AST test restricts web imports to stdlib+internal/app.
- Integration: integration_test.go drives real `app.NewWebUseCases` on a TempDir workspace.

**D12. packaging-binary-lifecycle** — risk: normal
- Behaviour: single test owns the entire cmd/ultraplan surface (0% direct): `go build -buildvcs=false` real binary, `init-workspace`, serve on reserved port (grab-release-reuse ⇒ TOCTOU window on the port), poll `/api/v1/health` ≤5 s, assert 18 pages/assets 200 outside source tree with no `internal/web/static` leakage in output, SIGINT → clean exit. Does not assert in-flight-stream drain or shutdown latency. Skipped under `-short`; the only `testing.Short` skip in the repo.
- Signal wiring exists solely in main.go:19 (`signal.NotifyContext(os.Interrupt, SIGTERM)`); openBrowser wired but never invoked by any test.

**D13. tui-console** — risk: normal
- Behaviour: bubbletea model updates driven directly with synthetic `KeyMsg`s and operation/validation messages; views asserted via substring checks on rendered builders (incl. verdict-neutral QA wording at narrow width via shared fixture); fakeUseCases captures ctx cancellation.
- Facts: no teatest, no golden files; `tea.Program` AltScreen loop (app.go:30) has no test; keys.go/theme.go covered only transitively.

### Domain E — Platform services

**E14. platform-services** — risk: normal
- process (88.9%): real `/bin/sh` children; owned-descendant kill verified via `kill(pid,0)` + `/proc/<pid>/stat` zombie check; truncation at 128-byte callback bound; Unix-only skips.
- runtime (58.7%, lowest tested above zero besides productstate): SDK error classification/redaction/bounding, policy fallback, cancellation mapping, event retention; store path-escape rejection; opencode log pruning. Integration harnesses absent.
- config (71.3%): precedence chains incl. env sources, redaction, agentwrap list-vs-scalar, QA/run-control/git bounds, unknown-field rejection.
- gitpublish (70.9%): real temp repos (`git init -b main`, bare remote); owned-paths-only commits preserving user's staged index; push retry without duplicate commit.
- logging (82.1%): text+JSON secret redaction. project (81.9%) / workspace (79.5%) / codeextract (78.9%): discovery precedence, resolve-escape rejection, roadmap dependency/phase ordering, range extraction incl. ignored dirs and partial/unresolved specs.
- filesystem: doc-only placeholder, no symbols.

## Seams between surfaces

1. **Agent runtime seam** — `study.Runtime` / `sprint.Runtime{StartRun}` faked at every consumer test; the real adapter (platform/runtime, 58.7%) is tested only in isolation. No test exercises a real adapter driving sprint/study state machines.
2. **run-state file ↔ SQLite mirror** — `saveRunStateWithHooks` (file authority) vs `state_database.go`→productstate (DB mirror); DB branch unreachable from all current tests; `MigrateRunStateToDatabase` unreferenced by tests; only ops script reaches it at runtime.
3. **Process ownership/probe seam** — `NativeProcessProbe{}` birth-identity contract between runcontrol lifecycle and platform/process; identity-unavailable degrades to CleanupUncertain and is skipped on incapable platforms.
4. **CLI factory seams** — `studyRuntimeFactory`, `stubSprintRuntimeFactory`, `TUIRunner`/`WebRunner` globals swapped by tests; `Env: nil` → real getenv fallback couples tests to host environment.
5. **web ↔ app use-case seam** — web restricted by AST test to internal/app; integration via real `NewWebUseCases`; handler files themselves symbol-covered only through `dispatch`.
6. **Go↔browser SSE vocabulary seam** — regex extraction from shipped JS is the only link between server event names and client handling.
7. **QA canonical fixture seam** — one JSON fixture projected through app/web/tui adapters; the freeze point is the shared `QAResult` struct fields.
8. **Smoke process-runner seam** — `pprocess.Runner` interface splits default-lane fakes from real `DirectRunner` children (env-gated).

## Likely domain grouping & risk rationale

- **Critical-path durability**: A3, C8, C9 — durable state authorities where the untested branches concentrate (DB mirror, memory-pressure call site, cancel paths). Highest reviewer priority.
- **Governed workflows**: B4-B7 — heavy mutation-table and fail-closed coverage; residual gaps are cancellation-state cells and store_fs read errors.
- **Interfaces**: D10-D13 — strong contract freezes; residual gaps are timing (heartbeat, drain) and cookie/token attributes.
- **Platform**: E14 — isolated unit coverage; the runtime adapter's low coverage matters mainly through Seam 1.

Reviewable-unit sizing conforms to contract: each surface ≈ 5-20 primary files, one workflow, few state authorities, named protecting tests.