# Surface Map — map-01-user-operations (user-visible product operations)

Job: independent product-surface discovery. No findings reported in this phase; risk notes are review-prioritisation rationale only.

## Provenance

- Target: `/home/antonioborgerees/coding/ultraplan/ultraplan-go`, frozen commit `f73c4dc659ba1492c16382be26ffcbce1a5ea84a`.
- Working tree observed at `8eef70f4903b25580719960009a170945bdad9ad` during discovery. Sole delta vs the freeze: agentwrap dependency bump `v0.0.0-20260825123040-dec2f4498922` → `v0.0.0-20260825130518-dccd575bd101` (`go.mod`/`go.sum` only, commit message "chore: update agentwrap permission handling"). All UltraPlan `.go` sources are byte-identical to the freeze, so every file:line citation below holds at `f73c4dc`; only third-party adapter behaviour could differ.
- Planning workspace: `/home/antonioborgerees/coding/ultraplan/ultraplan-workspace` @ `ab12dc38059c9bf485f9aced9075bcd7d924cac5` (clean).
- Method: 8 parallel bounded `review-worker` discoveries (study ops; sprint planning flow; sprint execute+publication; review/smoke/verify; QA; foundation/config/platform; web+TUI; durable run control) plus mapper verification of load-bearing claims (`app.go` dispatch/help/exit classes, `prompt_context.go:20`, `freshness_policy.go:14`, `serve_commands.go:117`, `execute_target.go:29`, `run_state.go:67`, `review.go:169/1710`, git delta).
- Baseline green at freeze per `review/state.json`: `go test ./...`, `-race`, `vet`, cover all passed.
- This map replaces the earlier `maps/map-01-user-operations.md` written against the superseded freeze `f0fcd0c…`.

## Product shape in one paragraph

UltraPlan Go is a local-first CLI (`cmd/ultraplan/main.go` → `internal/app/app.go:88 Run`) with 14 top-level commands dispatched at `app.go:144-177`: `version, init-workspace, defaults, skills, config, health, storage, run, project, sprint, study, tui, serve, code`. Users scaffold workspaces and studies, run agent-backed (agentwrap/OpenCode) analyses and a governed sprint stage chain `requirements -> code-context -> sprint-index -> technical-handbook -> area-reasoning -> reasoning -> plan -> execute -> review -> smoke` (+ QA investigation), inspect/resume/cancel durable runs, and observe/drive the same operations from a TUI and a loopback web console. All agent-backed work crosses one durable acceptance boundary (`.ultraplan/run-control.db`) before any child starts. Exit classes 0-8 fixed at `app.go:15-25`; stable JSON envelope via `internal/app/json_output.go`.

## Command inventory (entrypoint summary)

| Family | Commands | Dispatcher |
|---|---|---|
| Foundation | `init-workspace`, `defaults install`, `skills materialise [stage]`, `config show [--json]`, `health [--json]`, `storage migrate [--dry-run|--json]`, `version` | `internal/app/{workspace,defaults,skills,config,health,storage}_commands.go` |
| Durable runs | `run list|show|follow|cancel|diagnostics [--support-export]` | `internal/app/run_commands.go:19` |
| Project | `project list|<p> status|<p> validate` | `internal/app/project_commands.go` |
| Sprint | `sprint <p> <s> status|metrics|validate <stage>|prompt <stage>|flow|verify|execute|review(/conformance-review)|qa|smoke` | `internal/app/sprint_commands.go:28-69` |
| Study | `study init|list|<s> list|prompt analysis/synthesis|run|synthesize|run-all|run-loop|validate|status|summary|runs summary` | `internal/app/study_commands.go:26-120` |
| Interfaces | `tui`, `serve [--listen] [--open-browser]`, `code <report>...` | `internal/app/{tui_commands,serve_commands,code_commands}.go` |

---

# Candidate surfaces

Grouping proposal — **A Studies** (S1-S3), **B Governed sprint delivery** (S4-S10), **C Durable runtime** (S11), **D Interfaces** (S12-S13), **E Foundation** (S14-S16).

## Domain A — Studies

### S1 — `study-init-applicability`
- Behaviour: scaffold a study from YAML (sources+dimensions), optional shallow `git clone --depth 1`, write normalized artifacts; establish the source→dimension applicability contract every later study operation consumes (`SourceAppliesToDimension`, `prompts.go:163`).
- Entry: `study init <yml> [--dry-run --force --no-clone --output]` → `runStudyInit` (`app/study_commands.go:1452`) → `study.Init` (`study/init.go:55-91`).
- Files: `internal/study/{init,init_yaml,init_render,init_clone,discovery,markdown}.go`.
- State: `studies/<s>/{study-init.yml, study.json, README.md, dimensions/, sources/*.ultraplan-source.yml}`; strict `study.json` v1; applicability sidecars/frontmatter are the live contract, `study-init.yml` is provenance only.
- Outputs/failure: structured init problems → exit 5; clone failures accumulate → exit 8 partial with created artifacts kept; never overwrites without `--force`; clone output redacted (`init_clone.go:54`).
- Tests: `study/init_test.go`, `study/study_test.go` (sidecar/frontmatter precedence), `app/study_init_commands_test.go` (unsafe output path, clone partial, redaction).
- Seams: feeds S2/S3/S4 the applicability predicate uniformly (run skip, task graph, synthesis inputs, validate markers, summary N/A); path safety via `workspace.ResolveInside`.
- Risk: normal. YAML schema strictness + count-must-match rules are user-facing sharp edges; external git invocation with credential redaction.

### S2 — `study-stage-execution`
- Behaviour: single-task runtime execution `study <s> run <dim> <src>` and `synthesize <dim>` through agentwrap/OpenCode: prompt assembly (workspace override first, embedded fallback), session continuity by fingerprint with single fresh-session fallback (`run.go:139-197`), runtime-side validation spec with bounded same-session repair, report validation, publication hook.
- Entry: `runStudyRun`/`runStudySynthesize` (`app/study_commands.go:802/831`) wrapped in durable CLI command; `study.Service.RunAnalysis/Synthesize`; runtime stack `platform/runtime/{runtime,opencode}.go` (PolicyRunner retries/backoff/backup-model → ValidatingRuntime → ObservingRuntime).
- Files: `internal/study/{run,synthesize,prompts,runtime_validation,service,edit_warnings,publication}.go`.
- State: scoped OpenCode store `studies/<s>/.ultraplan/runtime/opencode/<hash>/opencode.db`; reports `reports/source/<dimRef>/<src>.md`, `reports/final/<dimRef>.md`; model precedence CLI `--model` → env `ULTRAPLAN_STUDY_MODEL` → `study.json:model`.
- Failure semantics: inapplicable pair skips before runtime; runtime error whose report validates records Completed ("recovered"); `runtime_exit` degrades to validation failure; exits 6 runtime / 7 cancel / 5 validation (`classifyExecutionResult`, `study_commands.go:933-948`).
- Tests: `study/run_test.go` (session continue/fresh fallback/model precedence/recovery mapping), `runtime_validation_test.go`, `app/study_run_commands_test.go`.
- Seams: consumes S1 applicability; every invocation wrapped by S11 `controlledRuntime.StartRun` (accept-before-start fail-closed); prompts shared with S3.
- Risk: high — LLM output becomes persisted product artifact; the "recovered as completed" classification decides what users are told succeeded.

### S3 — `study-runloop-resume`
- Behaviour: ephemeral parallel `run-all` vs durable resumable `run-loop`: per-study lock (O_EXCL + PID liveness + `--force-unlock`), versioned atomic `.ultraplan/run-state.json` (temp+fsync+rename+dir-sync, `state.go:73-119`), resume = reconcile graph against current applicability → revalidate completed artifacts on disk → restore history only with matching completed ledger record AND valid artifact (`RestoreCompletedRunHistory`, `run_state.go:366-429`); `dimension_order` priority ranks (backfill allowed); adaptive parallelism under memory/disk pressure incl. runtime-store GC; retry taxonomy with `retry_after`; `--reset` archives after confirm; cancellation leaves unscheduled tasks pending.
- Entry: `runStudyRunAll` (`study_commands.go:546`), `runStudyRunLoop` (:197) → `study.Service.RunAll/RunLoop`.
- Files: `internal/study/{run_all,run_loop,run_state,state,state_database,locks,cleanup_uncertain,run_history,run_history_summary,memory_pressure,disk_pressure,run_loop_diagnostics}.go`.
- State authorities: `studies/<s>/.ultraplan/{run-state.json, archive/, run-loop.lock, runs/tasks.jsonl, runs/summary.md, cleanup-uncertain.json, diagnostics/run-loop-memory.jsonl}`; productstate DB authoritative when present, JSON becomes checkpoint-only (`state_database.go:17-76`).
- Failure semantics: lock conflict → exit 8; malformed/unsupported state → 5; cancellation → 7 with unscheduled tasks left pending; persist throttled ≥250 ms; cleanup-uncertain marker reconciled fail-closed (`cleanup_uncertain.go:100-150`); study-cancel = SIGINT to lock-owner PID (`locks.go:141-159`).
- Tests: `run_loop_test.go` (resume-vs-reset, tier order, slot refill, cancel scope), `state_test.go` (reconcile, restore-requires-valid-artifact, rename-failure), `locks_test.go`, `app/study_run_loop_commands_test.go`, `app/study_status_commands_test.go`.
- Seams: S11 durable runs (`OperationStudyStart/Resume`), S15 config/runtime factory, S12/S13 web/TUI study-run-loop ops, S16 storage migrate import of run-state.
- Risk: high — long-running parallel provider spend, resume integrity across edits/crashes, pressure-triggered GC near user data.

## Domain B — Governed sprint delivery

### S4 — `project-catalog-inspection`
- Behaviour: discover projects under `projects/`, render status (docs, roadmap, project-index catalog health, sprints, reasoning-default resolution chain project→workspace→builtin) and validate the `project-index.md` catalog (contracts/evidence/reasoning templates/review protocols/smoke harness manifest cross-references).
- Entry: `project list|<p> status|<p> validate` → `internal/app/project_commands.go` + `project_usecases.go`; `internal/project/{discovery,index,roadmap,roadmap_status,service,validation,reasoning_defaults,store_fs}.go`.
- State: read-only over `projects/<p>/**`; no writes.
- Tests: `project_test.go`, `roadmap_test.go`, `reasoning_defaults_test.go`, `app/project_commands_test.go`.
- Seams: catalog is a governing input for S5 stage prompts/validation (sprint-index subset-of-catalog rule) and for S6 target resolution (`- **Target Implementation Directory:**` in project-index).
- Risk: normal (but upstream trust: catalog content steers later agent stages).

### S5 — `sprint-planning-flow` (requirements…plan, code-context)
- Behaviour: `status [--json]` (refreshes flow-state.json unless legacy/readOnly), `validate <stage>` per-artifact contracts, `prompt <stage>` runtime-free previews, `flow --to <stage>` governed generation. Cumulative order `requirements -> code-context -> sprint-index -> technical-handbook -> area-reasoning -> reasoning -> plan` (`flowStages`, `flow.go:257-282`); valid stages skipped ("already complete") but still published; code-context runs exactly once per canonical flow (`TestFlowToPlanSchedulesCodeContextExactlyOnceInCanonicalOrder`). Code-context: reference-only ≤64 KiB markdown shape (repo-relative paths, positive line ranges, no fenced source; `ValidateCodeContextContent`, `code_context.go:34-112`), read_only sandbox runtime against resolved target, candidate temp file → validate → atomic promote → flow-state save with prior-bytes restore on failure (`promoteCodeContext`, :457-506). Downstream prompts use byte-stable prefix: exact stored requirements/code-context bytes + budgeted transient live source evidence labelled untrusted, 512 KiB cap (`renderSharedPromptContext`, `prompt_context.go:158-228`, cap :20), context-pack cache keyed by content digests (best-effort). Sprint worktree creation happens on real code-context flows (`resolveSprintTarget create=true`, `execute_target.go:136-181`).
- Entry: `runSprint` cases `status/metrics/validate/prompt/flow` (`app/sprint_commands.go:90-279`); real flow wrapped in durable CLI command with `sprintRuntimeService` (:685-711).
- Files: `internal/sprint/{service,flow,domain,state,store_fs,artifacts,discovery,index,handbook,reasoning,plan,code_context,context_pack,prompt_context,prompt_bundle,prompts,input_contract,direct_inputs,freshness_policy,execute_target}.go`; app `sprint_usecases.go` projections.
- State: `projects/<p>/sprints/<s>/flow-state.json` (strict schemaVersion 2; temp+fsync+rename+dir-sync, preserves prior Review/Smoke/QA records on save, `state.go:201-292`), all planning artifacts, `.stage-sessions.json` planning sessions, `.ultra/cache/sprint-context/**`.
- Failure semantics: dry-run never writes; invalid artifact/findings → exit 5; runtime-classified errors → 6 (note: classification inspects `"runtime"` substring of message, `sprint_commands.go:269`); flow-state write failures preserve prior file.
- Tests: `code_context_test.go`, `sprint_index_test.go`, `handbook_test.go`, `plan_test.go`, `reasoning_test.go`, `sprint_test.go`, `app/sprint_commands_test.go`, `app/sprint_usecases_test.go`.
- Seams: S4 catalog inputs; S11 durable acceptance; S12 web exposes sprint-create + read-only prompt bundle API but NOT flow/status refresh routes; S6 shares mutation lease + target resolution; git publication after each successful stage (`publishExecuteStage` pattern, `publication.go`).
- Risk: high — agent-authored artifacts become the governing inputs for execution/review/QA; promotion atomicity and prompt-prefix stability are load-bearing.

### S6 — `sprint-execute-resume`
- Behaviour: execute validated top-level `plan.md` task checkboxes through one reusable runtime session; per-task checkpoint saved BEFORE runtime invoke (running state persisted first, tested); terminal-status priority switch (checkpoint-save-failure > invalid-deferral > deferred > cancelled > runtime-failed > evidence-complete > diagnostic-only > missing-evidence); stop-on-failure queue; `--task` selection; `--defer --reason` operator deferral and agent-driven `[/] — Deferred:` protocol preserving stable task IDs (`task-` + sha256[:12]); `--resume` requires every plan `[x]`/`[/]` marker to have a matching complete/deferred record (`validateResolvedResumeTasks`, `execute.go:386-421`); stale `running` records become failed; batch session reuse falls back to fresh sessions without a reusable ID; work constrained to resolved target dir (`ResolveExecuteTarget`, `execute_target.go:29`); Git publication only when ALL tasks resolve: implementation repo `All=true` first, then workspace `execute.md`+`.run-state.json` (CAS `update-ref`, flock `<git-common-dir>/ultraplan-publish.lock`).
- Entry: `sprint <p> <s> execute [flags]` (`app/sprint_commands.go:329-368`) → `sprint.Service.Execute/PromptExecute/DeferExecuteTask` (`execute.go:129/95/29`).
- Files: `internal/sprint/{execute,execute_model,execute_plan,execute_state,execute_target,session_state,cleanup_uncertain,publication,verification_lock,locks}.go`; `app/git_publication.go`; `platform/gitpublish/publisher.go` (+lock_unix/lock_other).
- State: `projects/<p>/sprints/<s>/.run-state.json` (atomic, DB-authoritative mirror when enabled), `execute.md` (written once AFTER loop, plain WriteFile — trails atomic state), worktree record `.workspace.json`, publish lock.
- Failure semantics: failed/cancelled queue stop → exit 8; missing evidence → task failed; publication failure aborts success; no `--force-unlock` exists for execute (unlike study); dead-PID lease replaced once (`verification_lock.go:26-93`); `ReconcileInterruptedMutation` converts orphans.
- Tests: `execute_plan_test.go`, `execute_state_test.go` (persist-before-runtime, deferred rationale), `execute_target_test.go` (containment, `../` rejection), `session_state_test.go`, `publication_test.go` (target-before-workspace ordering), `locks_test.go`, `cleanup_uncertain_test.go`, `app/durable_operations_test.go`.
- Seams: S5 target/worktree; S7 consumes unresolved-plan-task rule (`review.go:1466-1490` accepts deferred records); S11 fence token + heartbeats during writes; S12/S13 expose execute start/resume; config `git.stage_completion off|commit|commit-and-push` gates publisher entirely (`app/git_publication.go:10-19`).
- Risk: high — mutates the user's implementation repository and can push; session reuse and checkpoint ordering determine correctness of what is marked done.

### S7 — `sprint-conformance-review`
- Behaviour: bounded parallel read-only reviewer workers (default 3, cap 16) produce coverage per active-contract catalog entry + independent handbook reviewer; resumable attempts retain per-coverage {status, SessionID, Result}; resume compatibility = identical input fingerprint + model + coverage-id set; validated results rebase forward across fingerprint changes; live sessions continue only when fingerprint unchanged; `--restart` discards everything; `--focus <id>` rerun promotes ONLY when previous attempt was complete AND fingerprint matches exactly AND all other coverage retained valid (`reviewCoveragePlan`, `review.go:656-702`); verdicts `pass | pass_with_findings | fail | blocked` (any validation finding ⇒ blocked); review.md written atomically only after content validation passes.
- Entry: `sprint <p> <s> review [--focus] [--restart] [--dry-run] [--model] [--parallel] [--json]` (`app/sprint_commands.go:467-512`); alias `conformance-review`.
- Files: `internal/sprint/{review,review_runtime_validation,qa_state(stage states)}.go`; fingerprint builder `review.go:1341-1395` (SHA-256 over inputs' path/id/content-hash; smoke-harness index sections and roadmap lifecycle lines deliberately excluded from frozen view).
- State: review stage state inside flow-state.json (strict validators `review.go:1139-1200`), `review.md`, retained sessions via runtime store.
- Failure semantics: preflight findings ⇒ blocked persisted; cancel/persist-failure ⇒ failed with retained checkpoints; expired attempts recovered via OwnerPID liveness + 2 h heartbeat window (`verify.go:467-509`); verdict fail still writes artifact then exits 5.
- Tests: `review_test.go` (~30 named scenarios: determinism, exclusions, resume/rebase/restart, verdict matrix, repairs exhausted ⇒ blocked, atomic-write preservation, concurrency bound, drift-without-blocking, secret-redacted diagnostics), `app/sprint_verify_commands_test.go`.
- Seams: S6 unresolved-task rule; S8 gate input + digest match; S10 hard dependency (QA map requires fresh terminal review fingerprint, `qa_map.go:63-70`); S11 durable wrap; read_only sandbox policy deny-default (`review.go:900-933`).
- Risk: high — this verdict gates smoke promotion and QA admissibility; resume/fingerprint logic is where gate integrity could silently erode.

### S8 — `smoke-harness-gate`
- Behaviour: review-fingerprint-gated deep verification against an external protocol-v1 harness: static readiness checks (`prepareSmokeStatic`: single catalog harness row, symlink-resolved containment of manifest/executable/cwd/evidence roots, manifest schema/protocol/capabilities/authoring paths), authoring run restricted to manifest-declared paths with whole-harness before/after content snapshot (bounded 20k files/64 MiB; out-of-allowlist change aborts; stricter protected-snapshot mode exists but disabled, `freshness_policy.go:14`), discovery identity/ownership/mapping completeness validation, direct bounded argv execution with env allowlist + output caps, exact executed-test-identity set equality, per-failure issue-file requirement, `smoke.md` committed atomically BEFORE flow-state (divergence detectable later), diagnostic override `--force-review --override-reason` returns BEFORE commitSmoke and cannot promote review or overall assessment (`smoke.go:180-182`, `verify.go:256-258`).
- Entry: `sprint <p> <s> smoke [--level|--suite|--test] [--timeout] [--force-review --override-reason] [--dry-run] [--yes] [--json]` (`app/sprint_commands.go:513-585`).
- Files: `internal/sprint/{smoke,smoke_author,smoke_protocol,smoke_types,verify(gate parts)}.go`.
- State: `smoke.md`, smoke stage state in flow-state.json, external harness tree (owned by the harness, not UltraPlan), `.runtime-metrics.json`.
- Failure semantics: timeout/cancel/truncation/cleanup-uncertain/out-of-scope writes never fabricate success; explicit level/suite selections are DiagnosticOnly unless they cover the complete mapping (explicit test always diagnostic); exits 5/6/7 by class (`sprint_commands.go:879-892`).
- Tests: `smoke_test.go` (~45 prompt-contract assertions + selection/verdict/env allowlist/discovery rejection/argv redaction), `verify_test.go` overlap.
- Seams: S7 gate; S9 orchestrates it; external harness process boundary (the only place UltraPlan directly execs non-agentwrap argv); roadmap delivery update on canonical completion.
- Risk: high — executes external processes and is the final quality gate before delivery; authoring writes into the harness repo under allowlist enforcement.

### S9 — `verify-transition` (execute-evidence → review gate → smoke)
- Behaviour: the single review-to-smoke transition (`verify.go:37-38`): mutation lock → requireCompleteExecute (complete/deferred tasks + execute.md; legacy fallback) → freshness/currentness rule (`currentReview = Fresh && executionStatus==completed && !Restart`) else run Review → continuation to smoke only for completed-but-failing review WITH full override confirmation → RunSmoke; expired-attempt recovery derivation is read-only; `--to review|smoke`; `--yes` required for non-dry-run verify/smoke.
- Entry: `sprint <p> <s> verify [...]` (`app/sprint_commands.go:280-328`) and `sprint flow --to review|smoke` delegation (`flow.go:99,179`).
- Files: `internal/sprint/{verify,verification_phase}.go`; assessment precedence table `verify.go:256-258`.
- Tests: `verify_test.go` (assessment matrix, migration exactly-once, dead-PID recovery, override-cannot-launder-fail, typed conflict), `verification_phase_test.go` (planning order excludes QA/repair/conformance-review), `app/sprint_verify_commands_test.go` (flag parity, override-reason required).
- Seams: composes S6+S7+S8; same sprint-owned transition used by flow.
- Risk: high despite small size — it is the promotion boundary enforcing "diagnostic cannot become canonical".

### S10 — `qa-investigation`
- Behaviour: bounded read-only adversarial QA over changed paths: `qa --dry-run` builds byte-stable map (changed paths grouped into primary shards + one boundary shard when >1 primary; unknown groups pre-blocked), `qa` starts a durable fenced run claiming state BEFORE investigator work, worker pool (default 3, max 8), command/shard/run timeouts 5m/20m/60m (hard maxima 10m/30m/90m), output caps, retryable-failure taxonomy, closed outcome enum `confirmed/refuted/invalid/inconclusive/blocked/cross_shard/not_applicable` (none auto-issue), synthesis dedupes theories and derives follow-ups only from cross_shard/inconclusive; `resume` only when retained attempt == freshly computed semantic attempt ID (model change ⇒ new ID ⇒ stale); `recover` runtime-free marks interrupted/stale and prunes; `cancel --run` cooperative through run control; QA can never alter Conformance Review verdict (read-only annotation, `withQAConformanceReview`).
- Entry: `sprint <p> <s> qa [map|run|resume|status|cancel|recover]` (`app/sprint_commands.go:369-466,585-679`).
- Files: `internal/sprint/{qa,qa_map,qa_prompt,qa_state,qa_synthesis,qa_types}.go`; config `platform/config/qa.go`.
- State: `projects/<p>/sprints/<s>/verification/state.json` + `attempts/<id>/{map,shards,synthesis}` (strict loads: mode 0600, ≤128 MiB, schema gate, digest-verified refs, symlink rejection; pointer-last publish with writer-fence heartbeat), flow-state holds bounded QAFlowSummary pointer.
- Failure semantics: usage 2 / config 3 / validation-stale-policy 5 / runtime-persistence 6 / cancel-deadline 8 (`mapQACommandError`); owner death ⇒ reconcile terminalizes run `interrupted` while state stays active until explicit recover.
- Tests: `qa_map_test.go` (byte-stability, single ownership), `qa_prompt_test.go` (investigator default-deny, challenger zero authority), `qa_state_test.go` (strict load/immutability/fence), `qa_test.go` (workers, panic containment, cancel-no-retry), `qa_synthesis_test.go`, `app/sprint_commands_test.go` qa arg matrix, `web_operations_test.go` caller-control rejection.
- Seams: S7 fingerprint prerequisite; S6 execute evidence (changed paths); S11 writer fence tokens re-heartbeat during publishes; S12 GET routes + generic operation endpoints only.
- Risk: high — heavy bounded provider fan-out with its own durability layer; staleness/fence logic guards against investigating the wrong tree.

## Domain C — Durable runtime

### S11 — `durable-run-control`
- Behaviour: every executing operation (CLI `beginDurableCLICommand`, web/TUI `AcceptOperation`, child `controlledRuntime.StartRun`) crosses Accept → Claim(fencing generation, monotone ordinal) → running event before child work; lifecycle `accepted→queued→running→cancelling` + terminal arbitration `succeeded/failed/cancelled/timed_out/interrupted/cleanup_uncertain/persistence_degraded` (single-winner CAS); append-only event journal with per-run sequence CAS + immutability trigger; SQLite WAL `_synchronous=FULL` txlock immediate at `.ultraplan/run-control.db` (0700/0600, symlink rejected); `run list/show/follow/cancel/diagnostics` observation plane (follow replays then polls 250ms-1s, interrupting follow never touches state); cancel idempotent, terminal-winner protected, owner observes via 1 s control loop ack→ctx cancel; reconciliation scans unclaimed-after-grace and expired leases with PID-birth probing (`/proc/<pid>/stat` starttime+boot_id), decisions logged sanitized; retention compaction/tombstone/expiry + quota gates (accept rejected at soft quota, events at hard quota, heartbeat fails at hard quota forcing owner stop); support export ≤1 MiB O_EXCL 0600 excluding payloads/paths/credentials.
- Entry: `app/run_commands.go:19-276`; subsystem `internal/runcontrol/*` (`sqlite.go,lifecycle.go,model.go,process*.go,retention.go,sanitize.go,migration.go,local_log.go,metrics.go`); app integration `app/{durable_operations,run_control,operations}.go`.
- State: `.ultraplan/run-control.db` (+WAL, migrate lock, backups), `.ultraplan/run-control.log` (1 MiB capped JSONL).
- Failure semantics: persistence failure ABORTS work fail-closed (start-append failure ⇒ no child starts + `persistence_degraded`; mid-run Append failure cancels owned op and provider stream); observation commands mutate nothing.
- Tests: `sqlite_test.go`, `lifecycle_test.go` (fenced idempotence, winner races, birth-probe matrix, clock-jump), `fault_test.go` (SQLITE_FULL, closed repo, permission loss), `process_integration_test.go` (multi-process sequences, owner-exit interrupted idempotent), `retention_test.go`, `migration_test.go`, `sanitize_test.go`, `app/run_commands_test.go` (no-leak export), `run_control_inventory_test.go` (source inventory pins durable-boundary usage).
- Seams: backbone under S2/S3/S5-S10/S12/S13; import boundary pinned by test (stdlib + sqlite + x/sys only).
- Risk: high — correctness of fencing/cancellation/recovery governs every other surface's operational safety.

## Domain D — Interfaces

### S12 — `web-console-operations` (`serve`)
- Behaviour: loopback-only guarded browser dashboard: full route table pages `/`, `/projects[/{p}[/{page}]], /projects/{p}/sprints/create, /projects/{p}/sprints/{s}[/{page}], .../qa[/shards|theories], /studies[...], /runs[/{id}], /artifacts/{ref}, /operations/{id}`; API v1 read twins + `POST /api/v1/operations/prepare|start`, `GET/DELETE /api/v1/operations/{id}`, SSE `/api/v1/operations/{id}/events`, `DELETE /api/v1/runs/{id}`, timeline windows; two-step prepare→confirm (TTL 2 min, cap 128, single-use, bound to session+canonical-request+fingerprint); 27 operation kinds mapped (`operation_handlers.go:610-742`) executed through `sharedOperationRunner` → same sprint/study services; startup reconciliation; shutdown drains owned ops bounded 10 s marking cleanup-uncertain rather than success.
- Security model: numeric loopback listen validation pre-listen + post-listen authority re-check (`serve_commands.go:117-139`, `server.go:167-177`), host pinning 403, HMAC-signed HttpOnly SameSite=Strict session cookie with per-process secret, CSRF token (header for API, hidden field for forms), origin proofs incl. port-stripped-Origin fallback requiring Sec-Fetch-Site + byte-exact Referer, body ≤64 KiB only on operation POSTs, framing ambiguity rejection, MaxInFlight 32, strict CSP/nosniff/DENY/no-store, build-time policy caps "cannot be weakened" (`server_policy.go`), markdown sanitizer, redaction+bounds on projected text, artifact serving restricted to markdown/json with size/truncation contract.
- SSE: hub ring buffer 256 events/256KiB, subscriber queue 32 with slow-consumer disconnect, caps 8/op + 32 streams ⇒ 429 Retry-After, 15 s heartbeat, 30 min hard lifetime, cursor-gap 409s; durable-owned runs polled via `RunEvents(…,512)`.
- Files: `internal/web/{routes,handlers,operation_handlers,operations,run_handlers,timeline_handlers,qa_handlers,server,server_policy,security,artifacts}.go`, `templates/**`; app `serve_commands.go`, `web_usecases.go`, `operation_runner.go`, `web_dimensions.go`, `web_repos.go`.
- Tests: `routes_test.go`, `security_test.go`, `server_policy_test.go`, `operations_contract_test.go`, `api_compatibility_test.go`, `sse_test.go`, `integration_test.go`, `packaging_test.go`, `templates_test.go`, `run_handlers_test.go`, `timeline_handlers_test.go`, `qa_handlers_test.go`, `sprint_create_test.go`, `app/web_operations_test.go`, `app/serve_commands_test.go`.
- Seams: S11 durable manager; readOnly flag only suppresses status-refresh writes (`usecases.go:130-132`), not operation execution; sprint-create page performs roadmap/workspace mutations (worktree creation path shared with S5).
- Risk: high — network listener with session/CSRF/origin logic guarding real mutations; the largest untrusted-input surface in the product.

### S13 — `tui-console-operations` (`tui`)
- Behaviour: Bubbletea dashboard (Projects/Studies/Runs tabs; project/study/dimensions/sprint/QA shard-theory/run views), keybindings q/esc/tab/arrows/r/enter/c/p/u/w; enter-enter confirmation begins durable operations inline: sprint flow dry-run/run per stage, execute start/resume, review run/restart, verify, smoke incl. diagnostic override, QA start/resume/recover/focused shard, study run-loop with parallelism form 1-64 default 3; `c` requests durable cancellation; quitting mid-run refuses and points to `c`; 1 s tick refresh while a run view is open reloading Dashboard+Runs(200)+RunEvents(200); event channel 128-buffer drop-on-full.
- Files: `internal/tui/{app,model,views,keys,qa_view,viewport,theme,markdown}.go`; app `tui_commands.go`.
- Tests: `tui/model_test.go` (menu exposure, confirmation bounds, escape-hides-not-cancels, parallelism entry), `qa_view_test.go`, `views_test.go`, `run_view_test.go`, `app/tui_commands_test.go`.
- Seams: identical `sharedOperationRunner`/durable manager as S12; unlike serve, TUI does not set readOnly so its status view may write flow-state refreshes; help labels itself "read-only" at `app.go:268` while exposing the full mutating catalog.
- Risk: normal-high — same mutation power as web with weaker transport guarantees assumed (local TTY), plus doc/behaviour divergence worth verification attention.

## Domain E — Foundation

### S14 — `code-extraction`
- Behaviour: parse `` `path:N[,N|-N–N…]` `` citations + `| # | Name | Path |` source tables from reports; resolve inside workspace/report-dir roots with symlink-evaluated containment; direct → `<source-name>/` prefix strip → unique-basename walk (skipping VCS/node_modules/.ultraplan); CRLF-normalized snippets with range validation; deterministic JSON `{reports,sources,references,diagnostics,unresolved,status ok|partial|validation}`; `--output` write.
- Files: `internal/codeextract/{parser,resolver,service,domain,doc}.go`; app `code_commands.go`.
- Tests: `codeextract_test.go` (ranges/basenames/escape/out-of-range/partial), `app/code_commands_test.go`.
- Seams: consumes reports produced by S2/S3; standalone otherwise.
- Risk: low — contained, read-only, well-tested; path-safety primitives shared with workspace.

### S15 — `workspace-bootstrap-and-defaults`
- Behaviour: `init-workspace` creates minimal scaffold (`ultraplan.yml`, `README.md`, `studies/`) idempotently; `defaults install` materialises 28 embedded prompt/template overrides with byte-compare → skip → interactive confirm → `--force`; `skills materialise [all|stage]` writes 11 stage skills (`.agents/skills/ultraplan-<stage>/SKILL.md` + `agents/openai.yaml`, implicit invocation disabled; code-context skill delegates to canonical flow op) with identical confirm semantics; embedded-vs-workspace override resolution happens at prompt-read time everywhere (`workspace.DefaultOverrideFile` fallback, source label `builtin:<rel>`).
- Files: `internal/workspace/{init,defaults,skills,paths,discovery,validation}.go`; app `{workspace,defaults,skills}_commands.go`.
- State: workspace root files, `prompts/`, `templates/`, `.agents/skills/**`.
- Failure semantics: dry-run never writes; customized files preserved without confirmation/force; exit 4 workspace errors.
- Tests: `app/app_test.go` (dry-run/create/skip/force matrix), `workspace/workspace_test.go`, `workspace/skills_test.go` (contract tests incl. code-context delegation), `app/skills_commands_test.go`.
- Seams: defines the override surface consumed by S2/S5 prompt rendering; skills content embeds canonical stage prompts that must stay consistent with S5/S7/S8 runtime prompts.
- Risk: normal — overwrite-confirm UX and embedded/default consistency drift are the exposures.

### S16 — `config-health-inspect-migrate`
- Behaviour: `config show [--json]` renders effective config after defaults → `ultraplan.yml` (custom line parser) → ~27 env keys → CLI logging overrides, with source tracking and marker-based redaction (secret/token/api-key/sk-/ghp_/… patterns); `health [--json]` gates workspace discovery/structure/config/filesystem/env/runtime capability checks with stubbed seam for tests; `storage migrate [--dry-run|--json]` imports study run-states, sprint flow-states, execute run-states into productstate SQLite (per-item idempotent "InDatabase?" skip; files remain as checkpoints; partial exit on item failures); `version` metadata.
- Files: `internal/platform/config/{config,qa,redaction}.go`; `internal/platform/runtime/{health,opencode_maintenance}.go` (health mapping); `internal/productstate/store.go`; `internal/app/storage_commands.go`; app `{config,health}_commands.go`.
- Failure semantics: config errors exit 3 before anything runs; health exit precedence cfgErr→3, runtime→6, structure→5; storage migrate ExitWorkspace on open/discovery, ExitPartial(8) when items fail.
- Tests: `config_test.go` (precedence/validation/redaction/QA bounds), `health_commands_test.go`, `app_test.go` config-show tests; NOTE: no dedicated test file found for `storage migrate`.
- Seams: every surface funnels through `discoverWorkspace` + `loadEffectiveConfig` (`app.go:287-319`); productstate DB presence flips S3/S5/S6 state authorities to DB-primary/JSON-checkpoint; runtime config feeds the agentwrap stack (sandbox/permission defaults).
- Risk: normal-high — misconfiguration propagates everywhere; migration idempotency protects dual-authority consistency.

---

## Seams between surfaces

1. **Entry funnel**: `discoverWorkspace` + `loadEffectiveConfig` (flag → `ULTRAPLAN_WORKSPACE` → cwd ancestry marker `ultraplan.yml`) precede every surface; config errors exit 3 (`app.go:287-319`).
2. **Durable acceptance boundary** (S11 ↔ S2/S3/S5/S6/S7/S8/S9/S10/S12/S13): three registration styles (CLI wrapper, web/TUI manager, child runtime runs) must keep kind inventory consistent (`run_control_inventory_test.go`).
3. **flow-state.json multi-writer** (S5 status-refresh, S5 flow, S7 review state, S8 smoke state, S10 pointer projection, S12 readOnly gating): strict v2 schema, preserve-prior-on-save, legacy migration paths.
4. **Fingerprint/staleness chain** (S6 evidence → S7 input fingerprint & digest → S8 review-digest gate → S10 semantic attempt ID): each downstream re-derives currency independently; deliberate exclusions (smoke-index sections, roadmap lifecycle lines) documented at `review.go:1353-1395`.
5. **Mutation locks family**: study `run-loop.lock` (PID SIGINT cancel), sprint mutation lease + verification O_EXCL lock, gitpublish flock, runcontrol migrate lock, web preparation store; different staleness/liveness rules per family.
6. **Runtime platform boundary** (shared domain): per-operation sandbox/permission policies (read_only reviewers, restricted code-context author, workdir-scoped executor, path-allowlisted smoke author) over one agentwrap adapter stack with retry/backoff/backup-model; agentwrap dependency bumped post-freeze (see provenance).
7. **Prompt/template override chain** (S15 ↔ S2/S5): workspace file beats embedded builtin; project-level reasoning overrides add a middle layer (`projectReasoningPromptTemplate`).
8. **Dual state authority switch** (S16 ↔ S3/S5/S6): productstate DB authoritative when present, JSON files become checkpoints; `storage migrate` populates it idempotently.
9. **Git mutation edge** (S5/S6/S12): worktree creation (`git worktree add -b ultraplan/<project>/<slug>`) and stage publication (CAS update-ref, optional push) are the only intended repo mutations; web sprint-create shares the worktree path.
10. **Web/TUI ↔ CLI parity**: one `sharedOperationRunner` closure + typed use cases; documented divergences: readOnly flag (serve only), preparation/confirmation mechanics, SSE vs channel streaming, TUI parallelism form.

## Cross-cutting inventory facts (descriptive; for later-phase verification prioritisation)

- Top-level help labels both dashboards "read-only" (`app.go:268-269`) while S12/S13 expose the full mutating operation catalog; `readOnly` only gates status-refresh writes. `docs/cli-reference.md:3` describes serve as having "guarded operations" — internal doc inconsistency candidates.
- Undocumented implemented commands/subcommands vs `docs/cli-reference.md`: `tui`, `storage migrate`, `study <s> runs summary`; several implemented flags absent from docs (e.g. `--model` on study run/synthesize/run-all/run-loop, flow `--stage-model/--stage-variant`); embedded `skillsMaterialiseHelp` lists 9 of 11 materialised stages.
- Three coexisting argv-parser styles in `internal/app` (hand-rolled loops, stdlib flag, ordered variants) — inventory fact for interface-consistency reviews.
- `sprint flow` exit-class mapping inspects `"runtime"` substring of error text (`sprint_commands.go:269`); `flowFailedStages` drops prior failure detail on snapshot-read failure (`service.go:1214-1218`).
- Strict freshness snapshots exist but are disabled behind policy constants (`strictSmokeAuthorProtectedSnapshots=false`, `freshness_policy.go:14`).
- No dedicated automated test discovered for `storage migrate`; `metrics.go` counters are process-local only.
- PRODUCT.md describes a web UI as "a complete product interface"; implementation matches the operation catalog above (map-level consistency check only).

## Coverage notes / uncertainties

- `process_darwin.go`/`process_other.go` and full darwin probe behaviour inferred from build-tag layout, not line-traced.
- Worktree creation call sites traced to `resolveSprintTarget(create=true)` (real code-context flows, web sprint-create) but not exhaustively enumerated across web handlers.
- Template button inventory sampled, not exhaustive (`templates/**`).
- Reconcile grace constants cited from `model.go:513-521` (tick 1 s, heartbeat 5 s, lease 15 s, reconcile 10 s, grace 45 s) — single-source, not cross-checked.
- Planning-workspace contracts were used only as context; surfaces were derived from implementation reality per doctrine.
