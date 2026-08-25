# Surface Map — map-03-trust-boundaries

Job: `map-03-trust-boundaries` (surface-map, lens: untrusted inputs & capability transitions)
Target: `/home/antonioborgerees/coding/ultraplan/ultraplan-go` @ `f0fcd0c2107a8e8d69e1283f9e8d5e2c6da94025`
Workspace context: `/home/antonioborgerees/coding/ultraplan/ultraplan-workspace` @ `ab12dc38059c9bf485f9aced9075bcd7d924cac5`
Evidence class: REALITY (source + tests at frozen commit). No findings reported in this phase, per doctrine.

Method: seven bounded `review-worker` discoveries (CLI ingest; web HTTP; on-disk documents/config; agent runtime; OS/exec+git capabilities; rendering/TUI; durable stores), followed by mapper spot-checks of four load-bearing claims (all confirmed). Load-bearing claims below carry file:line refs; worker-reported refs were sampled, not exhaustively re-verified (uncertainties listed at the end).

## 1. Trust-boundary inventory

External input sources (data crosses into UltraPlan):
- T1 CLI: `os.Args[1:]`, global `--workspace`, per-command flags/positionals (`cmd/ultraplan/main.go:45-62`, `internal/app/app.go:88-177`); env `ULTRAPLAN_*` (~27 keys via `config.EnvOverrides()`, config.go:120-149); stdin confirmation lines (study_commands.go:304, skills_commands.go:154, defaults_commands.go:131).
- T2 HTTP (loopback-only server): path params, query params, JSON bodies (<=64KiB strict decode), form bodies with CSRF; identifier gate `validIdentifier` `^[A-Za-z0-9][A-Za-z0-9._-]*$` <=128B and `validOpaqueRef` `^[A-Za-z0-9_-]+$` (`internal/web/handlers.go:1035-1042`); exact Host match, session cookie HMAC, Origin checks, method allowlists (`web/security.go:144-170,253-260`; `server_policy.go`).
- T3 TUI keystrokes: quit/nav/refresh/cancel/open/preview/confirm actions mapping to the same operation pipeline as web (`internal/tui/app.go:62-207`, keys.go:24-55).
- T4 On-disk user-editable documents: `ultraplan.yml` (custom line scanner, unknown-field rejection, config.go:197-269,419-421); study init YAML (yaml.v3, NO unknown-field rejection, init_yaml.go:100-206); `study.json` (strict DisallowUnknownFields, study/config.go:50); roadmap.md / project-index.md line parsers (project/roadmap.go:93-202, index.go:19-58); dimension front matter (study/markdown.go:27); prompt/template override files read verbatim as prompt content (project/reasoning_defaults.go:52-114).
- T5 Agent output (opencode via agentwrap) — primary untrusted stream: events (deep-cloned, bounded 64 fields/8KiB/depth3, runtime/agentwrap.go:130-195), terminal-output heuristics up to 96KiB (runtime.go:472-501), QA investigator JSON (strict schema, sprint/qa.go:685-712), study report markdown (regex/rating validation, study/validation.go:33-89). Agents write files directly inside WorkDir; UltraPlan validates after the fact (sprint/runtime_validation.go:87-161).
- T6 Smoke harness manifest (JSON read from project-index catalog): supplies Executable/Args/CWD/evidence roots that become argv/cwd of a spawned process (sprint/smoke_protocol.go:142-175; smoke.go:94-150). Manifest-declared "harness authoring paths" are approved for agent mutation (smoke.go:573).
- T7 Git URLs from study-init YAML -> `git clone --depth 1` (study/init_clone.go:45).

Capability transitions (UltraPlan gains power on behalf of data):
- C1 Browser/TUI keystroke -> durable run that mutates target repos (operation prepare/start/cancel; app/operations.go:376+, tui app.go:153-156).
- C2 Spawn processes: smoke harness, QA check descriptors (`gofmt -d` built-in, qa_prompt.go:75,272), opencode maintenance exec (`agentwrap.Executable session delete <sessionID>`, SQL literal via single-quote escaping, runtime/opencode.go:103-125,181-188), generic runner with process-group SIGTERM/SIGKILL cleanup (platform/process/process_unix.go:14-35).
- C3 Git mutation/egress: worktree add with branch `ultraplan/<safeGitComponent(project)>/<safeGitComponent(slug)>` (sprint/execute_target.go:156-165); CAS-style commit-tree/update-ref publish + push (gitpublish/publisher.go:224-257, no force-push found); clone (T7).
- C4 Filesystem writes: flow-state.json/run-state.json/.stage-sessions.json/run-history JSONL/runtime stores/artifacts; support-export direct O_EXCL write at user-supplied path (app/run_commands.go:355).
- C5 Persistence round-trips that later re-enter execution: persisted SessionID -> continuation prompt + req.SessionID (sprint/session_state.go:111-143); persisted stage.Path revalidated on load (sprint/state.go:331-336); slugs -> branch names/worktree paths/publish targets; study OutputPath derived from workspace names -> publication target (study/run_state.go:135,164; reports.go:8-21).
- C6 Rendering/exposure: goldmark safe markdown -> template.HTML into web pages (app/markdown.go:15-22; handlers.go:801-814); glamour ANSI in TUI with raw fallback (tui/markdown.go:9-31); SSE payloads redacted by safeWebText/sanitize allowlists (app/operations.go:634-651; runcontrol/sanitize.go); render-time-only redaction — persisted payloads stored unredacted (config/redaction.go applied at sinks only).
- C7 Shared SQLite file: run-control (versioned, fenced, WAL/immediate/CAS, runcontrol/sqlite.go:73-79,985-1003) and productstate (CREATE TABLE IF NOT EXISTS every open, no version gate, productstate/store.go:63-90) both open `<ws>/.ultraplan/run-control.db`.
- C8 Path containment primitives: `workspace.ResolveInside` is lexical only (Abs/Clean + Rel prefix check; NO symlink evaluation — workspace/paths.go:9-41, spot-checked); symlink evaluation exists only in smoke-harness validation (project/validation.go:168-196) and web artifact serving re-checks EvalSymlinks (app/web_usecases.go:1525-1543).

## 2. Candidate surfaces

Size guide respected (~1 workflow, few state authorities, bounded files each). Risk = review-priority rationale, not a finding.

### S1 cli-input-dispatch — risk: normal
Behaviour: parse/validate all CLI input; dispatch to use cases; exit-code contract (RefError -> 5 etc.).
Entrypoints: main.go Run. State: none (pass-through). Outputs: use-case calls, JSON envelopes.
Files: cmd/ultraplan/main.go, internal/app/app.go, *_commands.go (sprint/study/run/serve/skills/defaults/workspace/code/storage/health/config/tui/project), surfaces.go, json_output.go.
Tests: sprint_commands_test.go parsers (:247,:619,:637,:668,:739,:1170), study init/run-loop flag tests, serve_commands_test.go:51.
Trust rationale: first validator for T1; free-form model/variant/task/shard/focus strings pass with only non-empty checks (sprint_commands.go:979-1001); stdin confirmations gate destructive ops.

### S2 web-http-security-gate — risk: high
Behaviour: loopback bind enforcement, host/origin/session/CSRF/body-size/framing rejection chain, route matching + central identifier validation, static assets from embedded FS set.
Entrypoints: securityMiddleware wrap of all routes (routes.go:76-77); ValidateLoopbackListen (serve_commands.go:119-139) + post-listen revalidation (server.go:72-74).
State: HMAC session cookie; ServerPolicy immutable bounds. Outputs: validated handler invocations; 400/403 projections.
Files: internal/web/{security,routes,server,server_policy,handlers(dispatch)}.go.
Tests: security_test.go (host/origin/CSP/IPv6/port-stripped-origin/framing/diagnostics-redaction), routes_test.go TestAPIValidationAndIdentifierBoundaries:167, server_policy_test.go:8.
Trust rationale: sole filter for T2; documented local-trust model (docs/local-web.md:86-115) means any non-loopback leak or origin bypass is high impact.

### S3 operation-control-plane — risk: critical
Behaviour: prepare/start/cancel operations from three frontends (web forms, web JSON, TUI keystrokes); spec normalization (Kind/Scope/Options incl. Stage,ToStage,Task,Shard,Model,DryRun,Resume,OverrideRationale,Parallelism); durable-run creation; session-scoped SSE hub vs workspace-visible run events.
Entrypoints: POST /operations/prepare|start|{id}/cancel, form variants, TUI PrepareOperation/AcceptOperation/CancelRun (tui/app.go:102-156,174-184).
State: operation records + aliases (runcontrol DB); hub subscriber caps (8/op, 32 streams, 30min).
Files: internal/web/{operation_handlers,run_handlers}.go, internal/app/operations.go (+web_usecases.go op parts), internal/app/durable_operations.go, internal/tui/app.go (actions).
Tests: operations_test.go, operations_contract_test.go:142, sse_test.go:22, run_handlers_test.go:295.
Trust rationale: this is C1 — the seam where an authenticated-as-anyone local browser session or a keystroke grants repo-mutating execution; DryRun/yes-gating semantics concentrate here.

### S4 agent-runtime-adapter — risk: critical
Behaviour: translate runtime.Request -> agentwrap runs; drain/bound/redact events; map results/usage/errors; permission policy translation+validation; session resume/delete (exec + escaped-SQL literal); retry w/ backup-model fallback; health/models; runtime store lifecycle cleanup.
Entrypoints: platform/runtime Adapter StartRun/DeleteSession/Health/ListModels.
State: ring buffer last 200 events; scoped OpenCode DBs under managed root (sha256 owner dirs); PersistUnsafeRawPayloads=false.
Files: internal/platform/runtime/*.go (runtime,opencode,agentwrap,events,policy,health,models,store,opencode_maintenance).
Tests: runtime_test.go (mapping/bounds/redaction/cancel/policy :15-481), opencode_test.go:20,:79, store_test.go:78, cache_test.go:5.
Trust rationale: ingestion point for T5; everything downstream trusts its mapped outputs; also holds C2 (session delete argv/SQL) and store deletion paths.

### S5 prompt-context-assembly — risk: high
Behaviour: build prompts sent to agents: shared context, requirements text, code-context artifacts under byte budget, source-line ranges framed as UNTRUSTED TRANSIENT SOURCE EVIDENCE, direct-input packets, prompt checksum identity; disk cache keyed by content digests; codeextract parses Go code from arbitrary target repos.
Entrypoints: sprint Render*Prompt/RenderExecutePrompt (prompts.go:36-178, execute.go:452-497); prompt_context.go:158-228; context_pack.go:34-70; study startRuntime continuation prefixing (run.go:108-181,255-257); internal/codeextract/*.
State: cache dir keyed by digests. Outputs: Prompt strings into runtime requests.
Files: internal/sprint/{prompts,prompt_context,context_pack,direct_inputs,handbook,code_context,plan,input_contract}.go, internal/codeextract/{parser,resolver,service,domain}.go.
Tests: prompt_context_test.go, execute_model_test.go, codeextract_test.go.
Trust rationale: egress boundary toward the model — decides which repo bytes leave; digest-keyed caching makes content-addressing part of the trust argument.

### S6 sprint-execution-state — risk: critical
Behaviour: sprint flow/execute/verify/review lifecycles; dual-homed state (productstate DB authoritative, flow-state.json/.run-state.json checkpoints); stage-session resume records; verification lock (O_EXCL PID liveness takeover); worktree targeting + rollback; git publication wiring; reconcile-interrupted-mutation.
Entrypoints: sprint Service Flow/Execute/Verify/Review/CreateWorkspace (service.go:1038-1050,1122-1212).
State authorities: flow-state.json v1->v2 migration (state.go:53-73,150), .stage-sessions.json (unlocked load-modify-rename writes, session_state.go:131-137), .ultraplan/locks/sprint/*.lock (verification_lock.go:26-61).
Files: internal/sprint/{service,state,state_database,store_fs,flow,execute,execute_state,execute_target,execute_plan,verify,review,locks,verification_lock,publication,artifacts,session_state,cleanup_uncertain}.go, internal/app/git_publication.go, internal/platform/gitpublish/*.
Tests: state/session/locks/verification/publication/execute_target test sets (incl. TestVerificationFileLockRejectsLiveOwnerAndReplacesDeadOwner, TestSprintMutationLeaseIsSharedAndCompositeSafe, TestCreateSprintWorkspaceFreezesBaselineAndReusesWorktree).
Trust rationale: C3+C4+C5 converge here; persisted stage.Path/sessionIDs/slugs become filesystem/git/session inputs; multi-process locking uses heterogeneous primitives (flock vs O_EXCL+PID vs sqlite fences).

### S7 qa-pipeline — risk: high
Behaviour: QA shards/theories/map/synthesis; investigator agent output strictly parsed (JSON, schema-version pinned); QA check descriptors spawn executables (built-ins like gofmt -d; descriptor Executable/Args/WorkingDirectory); post-run assertion that permissions were enforced (restricted/deny) plus identity drift fingerprints.
Entrypoints: sprint Service QA* (status/resume/cancel/recover), qa_prompt.go spawn site :272.
Files: internal/sprint/{qa,qa_map,qa_prompt,qa_state,qa_synthesis,qa_types,runtime_validation}.go.
Tests: qa_*_test.go, qa_errors_test.go (app layer), TestParseSprintQAArgsUsesOnlyPublicBoundedControls:637.
Trust rationale: consumes T5 verdicts and turns them into pass/fail gates; spawns helper executables from descriptors; enforcement assertions backstop the sandbox story.

### S8 smoke-harness-execution — risk: critical
Behaviour: resolve manifest from project-index catalog; validate executable/cwd inside EvalSymlinks-canonicalized harness root; intersect env allowlist; append sprint identity args; spawn discovery+run commands; classify cleanup-uncertain outcomes; restricted-mode smoke author with protected-write detection; smoke.md structural validation incl. secret-marker rejection.
Entrypoints: sprint Service RunSmoke; smoke.go:97/:150 spawn sites; smoke_protocol.go:142-175 manifest handling.
State: external evidence roots declared by manifest.
Files: internal/sprint/{smoke,smoke_protocol,smoke_author,smoke_types}.go, project/validation.go:168-196 (manifest path canonicalization).
Tests: smoke_test.go (TestSmokeManifestRejectsUnsupportedAndUnsafeValues, TestRealSmokeHarness, TestDefaultSmokeEnvironmentPreservesInterpreterPath), sprint_verify_commands_test.go parity test.
Trust rationale sharpest transition in the product: manifest (T6, partially agent-authored content) becomes argv/cwd/env of an executed process (C2), while agent authoring is allowed exactly on manifest-declared paths (smoke.go:573).

### S9 study-init-source-ingestion — risk: high
Behaviour: parse study-init YAML (normalizeInit; safeNamePattern + '..' rejection; NO unknown-field rejection); scaffold studies/<name>/ with study.json; git shallow-clone user URLs with credential-redacted output; plan-path containment validation; --no-clone/--force/--output safety.
Entrypoints: study Init (study/init.go:115-169,250-276); init_clone.go:45-65.
Files: internal/study/{init,init_yaml,init_clone,init_render,config,discovery,resolve}.go, study markdown/front matter readers.
Tests: init_test.go (TestInitValidationFailuresAreActionable, TestInitOutputPathSafetyOverwriteAndForce, TestInitCloneRunnerArgsAndPartialFailure), config_test.go strict-parse tests.
Trust rationale: T4+T7 combined; clones bring third-party repo content into the workspace where it later feeds prompts (S5) and rendering (S14).

### S10 study-run-loop — risk: high
Behaviour: dimension/source task graph; parallel agent execution with --parallel bound; run-loop lock (O_EXCL PID, force-unlock); DB-authoritative run state with JSON checkpoint/archive; history ledger (JSONL, malformed-trailing-record trim); disk/memory pressure guards; synthesize/rate/validate reports; CleanupRuntimeStores triggers.
Entrypoints: study run-loop/run-all/run/synthesize commands; run_loop.go:60,381 maintenance hooks.
State: .ultraplan/run-state.json + archive; tasks.jsonl; run-loop.lock.
Files: internal/study/{run_loop,run,run_all,run_state,run_history,run_history_summary,state,state_database,durable_metadata,locks,disk_pressure,memory_pressure,synthesize,rating,reports,publication,execution_domain}.go.
Tests: run_loop_test.go, run_history_test.go, state_test.go, locks_test.go, disk/memory pressure tests.
Trust rationale: longest-lived orchestration over T5 outputs; provider/model/cost fields copied verbatim from agent metadata into the ledger (run_history.go:163-224) then surfaced by S14.

### S11 durable-run-store — risk: high
Behaviour: run-control SQLite authority: versioned migration w/ backup+integrity, fenced ownership, CAS terminal transitions, immutable event trigger, quota compaction + retention tombstones, canonical ID validation on ingest; local 1MiB log w/ sensitive-field filtering; productstate cohabitation without version gate.
Entrypoints: runcontrol Repository used by run/serve/web/tui; storage migrate command.
State: <ws>/.ultraplan/run-control.db (WAL, immediate txlock, synchronous FULL), .db.migrate.lock, run-control.log.
Files: internal/runcontrol/{sqlite,migration,retention,id,model,lifecycle,metrics,sanitize,local_log,context,interfaces}.go, internal/productstate/store.go.
Tests: sqlite/migration/retention/lifecycle/id test sets (concurrent writers, CAS winner, stale lock reclaim, corrupt-DB evidence preservation, symlink DB rejection).
Trust rationale: observability + cancellation correctness depend on it; C5 round-trips (correlation_json, payload_json bounded but external); C7 seam — two owners, one file, divergent migration discipline (spot-checked: productstate createSchema on open, store.go:63-90).

### S12 config-env-redaction — risk: normal
Behaviour: load ultraplan.yml custom parser + ~27 ULTRAPLAN_* env overrides + defaults; validate enums/bounds/charsets (remote names reject leading '-'); QA lower-only caps; RedactValue applied at log/status/progress/error sinks; logging setup.
Entrypoints: config.Load/Validate/Redact; injected Env closure wired at app.go:300-306.
Files: internal/platform/config/*.go, internal/platform/logging/*, call sites in status_json.go, health_commands.go, sprint/study progress stderr paths.
Tests: config_test.go precedence/validation/redaction/QA-bounds sets, logging_test.go.
Trust rationale: environment is attacker-relevant when inherited (CI, wrappers); redaction is render-time only — persistence keeps secrets unredacted (relevant to S11/S14 exposure chains).

### S13 planning-document-parsing — risk: normal
Behaviour: roadmap.md/project-index.md parsing + issue-collecting validators (status/subsection/metadata whitelists, duplicate/order checks); project discovery filtered by IsSafeName + Clean-equality assertion; reasoning-default prompt overrides read from prompts/templates dirs; skills materialise/defaults install/init-workspace write paths.
Entrypoints: project Service Status/Validate; workspace Init/MaterialiseSkills/PlanDefaults.
Files: internal/project/{roadmap,index,roadmap_status,discovery,validation,store_fs,reasoning_defaults,service}.go, internal/workspace/{init,defaults,skills,validation,paths,discovery}.go.
Tests: project_test.go, roadmap_test.go, workspace_test.go, skills_test.go.
Trust rationale: these parsed values steer execution decisions and appear inside prompts; IsSafeName gating is what keeps directory names out of path tricks downstream.

### S14 artifact-rendering-preview — risk: high
Behaviour: preview/read artifacts behind HMAC opaque refs; containment + EvalSymlinks re-check + byte caps; goldmark safe-markdown -> template.HTML pages; glamour ANSI TUI previews with raw-text fallback; JSON envelopes SetEscapeHTML(true); SSE projection redaction (token=/secret=/authorization:/cookie:/home-path scrubbing, size caps w/ warning replacement); error-cause suppression.
Entrypoints: web Artifact/pages + api artifacts; dashboardUseCases.PreviewArtifact (usecases.go:159-278); web_dimensions.go:71-91.
Files: internal/app/{markdown,usecases,web_usecases,web_dimensions,json_output,status_json}.go, internal/web/{handlers(render parts),artifacts,timeline_handlers}.go, internal/tui/{markdown,views,viewport}.go.
Tests: artifacts_test.go (hostile-source escaping), templates_test.go (hostile names/empty/truncation), run_handlers_test.go:295 (script-tag escape assertion), usecases_test.go symlink/preview tests, views_test.go markdown renderer tests.
Trust rationale: exposure boundary for T4+T5 content (agent-authored markdown, cloned third-party repos, verbatim agent metadata) into browser/terminal/JSON consumers; bluemonday present as indirect dep but zero call sites — sanitization rests entirely on goldmark defaults + html/template autoescaping.

## 3. Seams between surfaces (assumption-mismatch candidates)
- SEAM-A S1/S3 -> S11: three frontends normalize the same logical operation differently before it becomes a durable run (CLI flags vs web operationSpecRequest/form vs TUI digit form 1-64); divergence surface for capability gating (--yes/--dry-run/--force-* exist only at CLI layer).
- SEAM-B S3 -> S6/S10: operation dispatch assumes sprint/study services validate scope refs; RefError taxonomy must survive the web error-projection layer (safeOperationCause).
- SEAM-C S4 <-> S6/S10: session resume trusts .stage-sessions.json SessionIDs with compatibility checked only against Provider/Model/WorkDir (session_state.go:87-91); repair loops continue the same session after failed validation.
- SEAM-D S8 -> S4/S6: manifest-declared paths become both process argv (smoke exec) and agent-writable authoring allowlist; two consumers of one untrusted document.
- SEAM-E S6/S10 -> S2/S14: state files and agent-produced artifacts rendered back through web/TUI; safeWebText/sanitize allowlists differ between run-event SSE, operation SSE, and artifact pages (three redaction regimes over similar data).
- SEAM-F S11 <-> productstate (C7): same SQLite file, independent DSNs, migration gate on one side only.
- SEAM-G S12 -> S4/C2: config env values and Agentwrap.Executable flow into child-process environments; ambient os.Environ inherited at opencode.go:181 and smoke env intersection at smoke_protocol.go:623-642.
- SEAM-H S13/S9 -> S5: parsed workspace documents and cloned third-party content become prompt payload; byte budgets decide what leaves the machine.
- SEAM-I S6 -> C3 gitpublish: slug-derived branch names, CAS update-ref, upstream selection; remote names validated but credentials ambient.
- SEAM-J S14 -> S11: render-time-only redaction vs unredacted persistence; support-export path writes DB-derived diagnostics to a user-chosen file outside containment checks (run_commands.go:355).

## 4. Domain grouping
- Ingestion & Interface: S1, S2, S3 (plus TUI action half of S3), S14.
- Orchestration & Agent Trust: S4, S5, S6, S7, S8, S9, S10.
- Durable State & Environment: S11, S12, S13.

## 5. Mapper uncertainties (for reviewers/arbiters)
- agentwrap module internals (event/stdout parsing inside github.com/Antonio7098/agentwrap) were not audited — treated as trusted component boundary; its validation entry points (ValidatePermissionPolicy) are invoked by S4.
- review/index.md shows target commit 31c55aa… while state.json pins f0fcd0c…; index appears stale — mapping assumed state.json binding.
- Worker-reported line numbers were spot-checked only where marked; bulk accepted as orientation anchors for reviewers, not as verified facts.
- TUI event-payload formatting paths (views.go run views printing detail/message strings) were sampled, not exhaustively traced.
- No dedicated tests found for internal/productstate (single file, no test file) despite shared-DB cohabitation with runcontrol.
