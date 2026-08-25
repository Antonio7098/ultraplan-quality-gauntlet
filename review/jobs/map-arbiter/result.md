The result.md files are summaries; the full maps live in `review/maps/`. Let me read them.All six maps are read. Now I'll independently verify the load-bearing structural facts against the target repo before composing the canonical map.Structure verified. Now spot-checking the remaining load-bearing claims that anchor surface boundaries (readOnly divergence, prompt budget, freshness policy, SIGINT cancel lane, status-refresh writes):All six mapper maps have been read and independently reconciled against the repositories. Key structural facts were re-verified directly at the target (`8eef70f`, byte-identical sources to frozen `f73c4dc` except go.mod/go.sum): dual-package pools on `.ultraplan/run-control.db` (internal/runcontrol/sqlite.go:22, internal/productstate/store.go:19), 15-verb dispatch and exit classes 0–8 (internal/app/app.go), flow stage order requirements→plan+execute/review/smoke (internal/sprint/flow.go:250), study state-file family (internal/study/domain.go, locks.go:155 SIGINT lane), empty CLI acceptance digest (internal/app/durable_operations.go:57), two-tier ParentRun fencing (internal/app/run_control.go:162), lease constants (internal/runcontrol/model.go:514–516), web `readOnly:true` vs TUI default status-writes (web_usecases.go:430, usecases.go:130), 512 KiB prompt budget and disabled strict freshness switches (prompt_context.go:20, freshness_policy.go:17–20). Where mappers disagreed (surface granularity for TUI, validation, subprocess policies), I followed code structure over any single proposal.

```json
{
  "surfaces": [
    {
      "id": "run-journal-fencing",
      "name": "Run-control SQLite journal and fencing",
      "domain": "durability-core",
      "risk": "critical",
      "purpose": "Durable identity and arbitration for every executing operation: accept, fenced claim, append-only event journal, heartbeat leases, cancellation request/acknowledge split, single-winner terminal CAS, and birth-identity reconciliation.",
      "entrypoints": ["internal/runcontrol/sqlite.go:Repository.Claim", "internal/runcontrol/lifecycle.go:Heartbeat", "internal/runcontrol/lifecycle.go:ProposeTerminal", "command run show|cancel"],
      "paths": ["internal/runcontrol/sqlite.go", "internal/runcontrol/lifecycle.go", "internal/runcontrol/model.go", "internal/runcontrol/process_linux.go", "internal/runcontrol/id.go"],
      "state": [".ultraplan/run-control.db (runs/attempts/events/operation_aliases/reconciliation_log)", ".ultraplan/run-control.log"],
      "trust_boundaries": ["durable journal rows re-read as input", "agent-derived event payloads admitted only after sanitizer allowlist"],
      "dependencies": []
    },
    {
      "id": "run-recovery-retention",
      "name": "Run-control recovery, migration, retention",
      "domain": "durability-core",
      "risk": "high",
      "purpose": "Schema migration with backup/restore and proven-stale lock reclaim, crash reconciliation decisions, event compaction, aging to tombstones, quota gates at accept/append/heartbeat, and support diagnostics export.",
      "entrypoints": ["internal/runcontrol/migration.go:Open/Migrate", "internal/runcontrol/retention.go:Enforce", "internal/runcontrol/lifecycle.go:Reconcile", "internal/app/run_commands.go:support export"],
      "paths": ["internal/runcontrol/migration.go", "internal/runcontrol/retention.go", "internal/runcontrol/local_log.go", "internal/runcontrol/sanitize.go"],
      "state": [".ultraplan/run-control.db backups (.bak.*, keep newest 3)", ".ultraplan/run-control.db.migrate.lock", "reconciliation_log evidence rows"],
      "trust_boundaries": ["corrupt/hostile pre-existing DB files must not be silently replaced", "lock file contents parsed as identity proof"],
      "dependencies": ["run-journal-fencing"]
    },
    {
      "id": "durable-operation-spine",
      "name": "App durable operation spine",
      "domain": "durability-core",
      "risk": "critical",
      "purpose": "Single accept-before-execute chokepoint shared by CLI/TUI/web: operation accept with alias dedup, claim, persistence-gated event delivery, 1s/5s control cadence, terminal finish mapping, and QA writer-token handoff to sprint.",
      "entrypoints": ["internal/app/durable_operations.go:beginDurableCLICommand", "internal/app/durable_operations.go:durableOperationManager.AcceptOperation", "internal/app/run_control.go:controlledRuntime.StartRun", "internal/app/operation_runner.go:sharedOperationRunner"],
      "paths": ["internal/app/durable_operations.go", "internal/app/run_control.go", "internal/app/operation_runner.go"],
      "state": ["outer 'operation' runs and inner runtime runs in .ultraplan/run-control.db correlated via ParentRun"],
      "trust_boundaries": ["confirmation digests/fingerprints must be derived server-side, never accepted from transport"],
      "dependencies": ["run-journal-fencing"]
    },
    {
      "id": "product-state-mirror",
      "name": "Product-state SQLite mirror and storage migrate",
      "domain": "durability-core",
      "risk": "high",
      "purpose": "Second package/pool on the same run-control DB holding kind/scope/item KV for study run-state, sprint flow-state, and execute state; mere existence of DB file plus row flips authority away from JSON checkpoints; `storage migrate` imports files one-way and idempotently.",
      "entrypoints": ["internal/productstate/store.go:Existing/Ensure/Save/Load", "internal/app/storage_commands.go:runStorage (storage migrate --dry-run|--json)"],
      "paths": ["internal/productstate/store.go", "internal/study/state_database.go", "internal/sprint/state_database.go", "internal/app/storage_commands.go"],
      "state": ["product_states / product_state_items tables in .ultraplan/run-control.db", "source JSON files retained as checkpoints"],
      "trust_boundaries": ["stored rows re-enter as authoritative state with unconditional row-wins on read"],
      "dependencies": ["run-journal-fencing"]
    },
    {
      "id": "study-authoring",
      "name": "Study scaffolding, validation, summary",
      "domain": "study-analysis",
      "risk": "normal",
      "purpose": "Create studies from YAML with optional shallow git clone, normalize study.json/source sidecars establishing source-dimension applicability, render prompts deterministically, validate reports/ratings, regenerate summary.csv, publish completed executions.",
      "entrypoints": ["commands study init|list|<s> prompt|summary|validate|runs summary", "internal/study/init.go:Init", "internal/study/validation_command.go"],
      "paths": ["internal/study/init.go", "internal/study/init_clone.go", "internal/study/init_yaml.go", "internal/study/config.go", "internal/study/prompts.go", "internal/study/validation.go", "internal/study/rating.go", "internal/study/summary.go", "internal/study/publication.go"],
      "state": ["studies/<s>/{study-init.yml, study.json, dimensions/, sources/, reports/, summary.csv}"],
      "trust_boundaries": ["user-supplied study-init.yml and cloned external repositories enter as data", "clone child process stdout redacted before display"],
      "dependencies": []
    },
    {
      "id": "study-task-execution",
      "name": "Study single-task execution",
      "domain": "study-analysis",
      "risk": "high",
      "purpose": "Run one analysis or synthesis task through the agent runtime: fingerprint-gated session continuation with one-shot fresh fallback, bounded same-session repair, clean-exit recovery when a validating report exists despite runtime_exit, edit warnings, post-success session deletion.",
      "entrypoints": ["commands study <s> run|synthesize", "internal/study/run.go:RunAnalysis/Synthesize", "internal/app/study_commands.go:runStudyRun"],
      "paths": ["internal/study/run.go", "internal/study/runtime_validation.go", "internal/study/edit_warnings.go", "internal/study/runtime_metadata.go"],
      "state": ["reports/source/** and reports/final/**", "per-task session checkpoints inside run-state", "scoped stores under studies/<s>/.ultraplan/runtime/opencode/<hash>/"],
      "trust_boundaries": ["LLM markdown output becomes persisted product artifact and success classification", "opaque agent-issued SessionIDs used as resumable capability handles"],
      "dependencies": ["opencode-agent-runtime", "product-state-mirror"]
    },
    {
      "id": "study-runloop-scheduler",
      "name": "Study durable run-loop scheduler",
      "domain": "study-analysis",
      "risk": "critical",
      "purpose": "Long-lived resumable parallel execution across a task graph: worker-slot refill without batch barrier, priority tiers, memory/disk-pressure admission and GC, retry taxonomy, atomic run-state saves, resume revalidation of completed artifacts, PID-based lock with SIGINT cancel lane, cleanup-uncertain fail-closed reconciliation, append-only history ledger.",
      "entrypoints": ["commands study <s> run-loop|run-all|--reset|--force-unlock", "internal/study/run_loop.go:RunLoop", "internal/study/locks.go:CancelRunLoop", "internal/study/cleanup_uncertain.go:ReconcileInterruptedRun"],
      "paths": ["internal/study/run_loop.go", "internal/study/run_state.go", "internal/study/state.go", "internal/study/state_database.go", "internal/study/locks.go", "internal/study/cleanup_uncertain.go", "internal/study/run_history.go", "internal/study/memory_pressure.go", "internal/study/disk_pressure.go", "internal/study/run_loop_diagnostics.go"],
      "state": ["studies/<s>/.ultraplan/{run-state.json, run-loop.lock, cleanup-uncertain.json, archive/, runs/tasks.jsonl, runs/summary.md, diagnostics/run-loop-memory.jsonl}", "DB-authoritative mirror when productstate enabled"],
      "trust_boundaries": ["lock/pidfile contents drive liveness decisions and SIGINT delivery to other processes", "persisted run-state files re-read as input on resume"],
      "dependencies": ["study-task-execution", "opencode-agent-runtime", "product-state-mirror", "repo-publication"]
    },
    {
      "id": "sprint-planning-chain",
      "name": "Sprint planning stage chain",
      "domain": "governed-sprint-delivery",
      "risk": "high",
      "purpose": "Governed generation of requirements, code-context, sprint-index, technical-handbook, area-reasoning, reasoning, plan artifacts through agent calls with byte-stable shared prompt prefix (512 KiB cap), TOCTOU replacement detection, per-stage content validators, bounded repair, candidate-temp promotion, worktree creation, and stage-session checkpoints.",
      "entrypoints": ["commands sprint <p> <s> flow|status|validate|prompt", "internal/sprint/flow.go:Flow", "internal/sprint/code_context.go:promoteCodeContext", "internal/sprint/prompt_context.go:renderSharedPromptContext"],
      "paths": ["internal/sprint/flow.go", "internal/sprint/service.go", "internal/sprint/code_context.go", "internal/sprint/context_pack.go", "internal/sprint/prompt_context.go", "internal/sprint/session_state.go", "internal/sprint/handbook.go", "internal/sprint/reasoning.go", "internal/sprint/plan.go", "internal/sprint/input_contract.go", "internal/sprint/direct_inputs.go"],
      "state": ["projects/<p>/sprints/<s>/{requirements.md, code-context.md, sprint-index.md, technical-handbook.md, reasoning*, plan.md}", ".stage-sessions.json (rename-only writes, no fsync)", "flow-stage records in flow-state.json"],
      "trust_boundaries": ["agent-authored artifacts become governing inputs for later stages", "live repository source embedded into prompts labelled untrusted", "catalog/index content steers validators"],
      "dependencies": ["sprint-flow-state", "opencode-agent-runtime", "durable-operation-spine", "repo-publication", "product-state-mirror"]
    },
    {
      "id": "sprint-execute-resume",
      "name": "Sprint execute queue and resume",
      "domain": "governed-sprint-delivery",
      "risk": "high",
      "purpose": "Execute validated plan tasks through a reusable agent session: persist-before-launch checkpointing, stop-on-failure queue discipline, stale-running reconcile, deferral protocol requiring rationale, resume validation against plan markers, containment to resolved target dir, then git publication of implementation changes and workspace summaries.",
      "entrypoints": ["commands sprint <p> <s> execute [--resume|--defer]", "internal/sprint/execute.go:Execute", "internal/sprint/execute_target.go:ResolveExecuteTarget"],
      "paths": ["internal/sprint/execute.go", "internal/sprint/execute_plan.go", "internal/sprint/execute_state.go", "internal/sprint/execute_target.go", "internal/sprint/execute_model.go", "internal/app/git_publication.go"],
      "state": ["projects/<p>/sprints/<s>/.run-state.json (atomic, DB-mirror when enabled)", "execute.md (plain WriteFile)", ".workspace.json worktree record"],
      "trust_boundaries": ["mutates the user's implementation repository and can push", "plan checkbox markers and agent deferral text parsed as control input"],
      "dependencies": ["sprint-planning-chain", "sprint-flow-state", "opencode-agent-runtime", "repo-publication", "product-state-mirror"]
    },
    {
      "id": "sprint-conformance-review",
      "name": "Sprint conformance review fan-out",
      "domain": "governed-sprint-delivery",
      "risk": "high",
      "purpose": "Bounded parallel read-only reviewers produce coverage verdicts over the active contract catalog with an independent handbook reviewer; input fingerprints gate resume/rebase/restart semantics; verdict ladder pass/pass_with_findings/fail/blocked decides downstream admissibility.",
      "entrypoints": ["commands sprint <p> <s> review [--focus|--restart]", "internal/sprint/review.go:Review/reviewCoveragePlan", "internal/sprint/review_runtime_validation.go"],
      "paths": ["internal/sprint/review.go", "internal/sprint/review_runtime_validation.go"],
      "state": ["review.md (atomic write after content validation)", "review stage state + coverage checkpoints in flow-state.json", "retained review sessions in runtime stores"],
      "trust_boundaries": ["structured reviewer JSON decoded tolerantly then judged by decision tables", "citation paths checked against frozen manifest contents"],
      "dependencies": ["sprint-flow-state", "sprint-execute-resume", "opencode-agent-runtime"]
    },
    {
      "id": "sprint-smoke-gate",
      "name": "Smoke harness gate",
      "domain": "governed-sprint-delivery",
      "risk": "high",
      "purpose": "Review-digest-gated deep verification against an external protocol-v1 harness: static readiness checks, authoring restricted to manifest-declared paths with content snapshots, discovery identity validation, direct argv execution with env allowlist and caps, exact test-identity set equality, atomic smoke.md commit before flow-state.",
      "entrypoints": ["commands sprint <p> <s> smoke [--level|--suite|--force-review|--override-reason|--yes]", "internal/sprint/smoke.go:Smoke", "internal/sprint/smoke_protocol.go:decodeOneJSON"],
      "paths": ["internal/sprint/smoke.go", "internal/sprint/smoke_author.go", "internal/sprint/smoke_protocol.go", "internal/sprint/smoke_types.go"],
      "state": ["smoke.md", "smoke stage state in flow-state.json", "external harness tree (owned by harness)", ".runtime-metrics.json"],
      "trust_boundaries": ["external harness stdout/stderr JSON decoded and executed as verification truth", "manifest-declared paths/env names cross an allowlist boundary", "the only direct non-agentwrap argv execution"],
      "dependencies": ["sprint-conformance-review", "process-execution", "sprint-flow-state", "repo-publication"]
    },
    {
      "id": "sprint-verify-transition",
      "name": "Verify review-to-smoke transition",
      "domain": "governed-sprint-delivery",
      "risk": "high",
      "purpose": "The single promotion boundary composing execute-completeness checks, review freshness/currentness, diagnostic override ladder, and smoke continuation; enforces that diagnostic results can never become canonical assessment.",
      "entrypoints": ["commands sprint <p> <s> verify [--to review|smoke|--yes]", "internal/sprint/verify.go:Verify", "flow --to review|smoke delegation internal/sprint/flow.go"],
      "paths": ["internal/sprint/verify.go", "internal/sprint/verification_phase.go", "internal/sprint/freshness_policy.go"],
      "state": ["assessment blocks in flow-state.json (review/smoke digest gating)"],
      "trust_boundaries": ["override rationale and confirmation flags gate irreversible quality promotion"],
      "dependencies": ["sprint-conformance-review", "sprint-smoke-gate", "sprint-flow-state"]
    },
    {
      "id": "sprint-qa-investigation",
      "name": "QA adversarial investigation",
      "domain": "governed-sprint-delivery",
      "risk": "high",
      "purpose": "Bounded read-only adversarial QA over changed paths: byte-stable shard map, fenced investigator fan-out with timeouts/budgets, closed outcome enum, deterministic synthesis, resume keyed on semantic attempt ID, recover/prune, private pointer-last store with writer-token fencing tied to the durable run lease.",
      "entrypoints": ["commands sprint <p> <s> qa start|map|run|resume|cancel|recover|status", "internal/sprint/qa.go:RunQA", "internal/sprint/qa_map.go:BuildQAMap", "internal/sprint/qa_state.go:Publish"],
      "paths": ["internal/sprint/qa.go", "internal/sprint/qa_map.go", "internal/sprint/qa_state.go", "internal/sprint/qa_synthesis.go", "internal/sprint/qa_prompt.go", "internal/sprint/qa_types.go"],
      "state": ["verification/state.json + attempts/<id>/{map,shards,synthesis}.json (0700/0600, symlink rejection, 128 MiB budget)", "QA pointer record in flow-state.json written last"],
      "trust_boundaries": ["investigator structured output gated by budgets/self-approval rejection/catalog-owned check refs", "writer tokens minted from run-control fences checked fail-closed"],
      "dependencies": ["sprint-conformance-review", "sprint-execute-resume", "process-execution", "durable-operation-spine", "sprint-flow-state"]
    },
    {
      "id": "sprint-flow-state",
      "name": "Sprint flow-state authority and mutation locks",
      "domain": "governed-sprint-delivery",
      "risk": "high",
      "purpose": "Central multi-writer record all sprint stages gate on: strict v2 loading with read-time-only v1 upgrade, atomic saves preserving prior verification evidence, status-refresh write gating, layered mutation lease (in-process plus O_EXCL pidfile with EPERM liveness), interrupted-mutation reconcile, cleanup-uncertain consumption.",
      "entrypoints": ["internal/sprint/state.go:LoadFlowState/SaveFlowState", "internal/sprint/service.go:Status (statusWrites)", "internal/sprint/locks.go:withMutationLock/ReconcileInterruptedMutation", "internal/sprint/verification_lock.go"],
      "paths": ["internal/sprint/state.go", "internal/sprint/state_database.go", "internal/sprint/locks.go", "internal/sprint/verification_lock.go", "internal/sprint/cleanup_uncertain.go", "internal/sprint/artifacts.go"],
      "state": ["projects/<p>/sprints/<s>/flow-state.json", ".ultraplan/locks/sprint/<p>--<s>.lock", "sprint cleanup-uncertain markers"],
      "trust_boundaries": ["flow-state bytes re-read as trusted gate input by every later stage", "legacy v0/v1 strata deliberately excluded from mutation"],
      "dependencies": ["product-state-mirror"]
    },
    {
      "id": "repo-publication",
      "name": "Git stage publication",
      "domain": "governed-sprint-delivery",
      "risk": "normal",
      "purpose": "Opt-in commit/commit-and-push of exactly owned path sets per stage: temp index preserving user's staged index, update-ref CAS against expected parent, flock publish lock, bounded push with disabled prompts, roadmap delivery marking; always ordered after durable state commits.",
      "entrypoints": ["internal/platform/gitpublish/publisher.go:Publish", "internal/app/git_publication.go:stagePublisher", "internal/sprint/publication.go", "internal/study/publication.go"],
      "paths": ["internal/platform/gitpublish/publisher.go", "internal/platform/gitpublish/lock_unix.go", "internal/platform/gitpublish/lock_other.go", "internal/sprint/publication.go", "internal/study/publication.go"],
      "state": ["git refs in implementation/workspace repos", "<git-common-dir>/ultraplan-publish.lock", "roadmap.md delivery marks"],
      "trust_boundaries": ["pushes to configured remotes are externally visible and effectively irreversible", "remote name/URL validated before shell-free git invocation"],
      "dependencies": []
    },
    {
      "id": "cli-dispatch-exit-contract",
      "name": "CLI dispatch and exit-code envelope contract",
      "domain": "operator-interfaces",
      "risk": "high",
      "purpose": "Argv grammar for 15 verbs consumed by scripts and agents: exit classes 0-8, stable JSON envelopes with stable codes, stdout/stderr stream discipline, interactive confirmations without TTY detection, and classification of errors including substring-sniffed families.",
      "entrypoints": ["internal/app/app.go:Run (dispatch app.go:144-177)", "internal/app/json_output.go", "internal/app/status_json.go", "cmd/ultraplan/main.go:main"],
      "paths": ["internal/app/app.go", "internal/app/json_output.go", "internal/app/status_json.go", "internal/app/*_commands.go", "docs/cli-reference.md"],
      "state": ["stdout/stderr contracts only; no durable state"],
      "trust_boundaries": ["all CLI flags/args/env are untrusted admission input", "error text inspected by classifiers"],
      "dependencies": ["config-inspection-health", "durable-operation-spine"]
    },
    {
      "id": "run-cli-observation",
      "name": "Run observation and cancellation CLI",
      "domain": "operator-interfaces",
      "risk": "normal",
      "purpose": "Operator window onto the durable spine: filtered run listing with cursor pagination, snapshot rendering, follow with omission expansion and replay-until-terminal, reason-whitelisted cancellation, health diagnostics and bounded no-leak support export.",
      "entrypoints": ["commands run list|show|follow|cancel|diagnostics", "internal/app/run_commands.go:runCommands", "internal/app/run_usecases.go"],
      "paths": ["internal/app/run_commands.go", "internal/app/run_usecases.go", "internal/app/run_control.go"],
      "state": ["reads .ultraplan/run-control.db (opens pool and startup Reconcile even for reads)", "support export file (O_EXCL 0600, <=1 MiB)"],
      "trust_boundaries": ["journal payload projection passes sanitizer/redaction before display"],
      "dependencies": ["run-journal-fencing"]
    },
    {
      "id": "web-routing-projection",
      "name": "Web routing, pages, and API projection",
      "domain": "operator-interfaces",
      "risk": "high",
      "purpose": "~47 hand-routed HTML/API/static endpoints projecting dashboard/project/sprint/study/QA/run state with {data,meta} envelopes, query allowlists, identifier regexes, safe markdown rendering, HMAC artifact preview refs, and a frozen route/method/DTO compatibility baseline.",
      "entrypoints": ["internal/web/routes.go:handler chain", "internal/web/handlers.go", "internal/web/artifacts.go", "GET /api/v1/*"],
      "paths": ["internal/web/routes.go", "internal/web/handlers.go", "internal/web/artifacts.go", "internal/web/timeline_handlers.go", "internal/web/templates/", "internal/web/static/", "internal/app/web_usecases.go"],
      "state": ["read-only projections; artifact ref mint map is process-lifetime (refs die on restart)"],
      "trust_boundaries": ["every request path/query/header is untrusted input", "agent-produced markdown rendered through sanitizing pipeline only"],
      "dependencies": ["shared-usecase-vocabulary"]
    },
    {
      "id": "web-operation-hub-sse",
      "name": "Web operation hub, SSE, and shutdown drain",
      "domain": "operator-interfaces",
      "risk": "high",
      "purpose": "Two-phase prepare/start mutations with TTL'd single-use confirmation tokens, hub capacity/dedup ordering, SSE streams with replay-gap accounting and slow-subscriber eviction, browser cancels, and graceful 10s drain persisting leaseless cleanup-uncertain markers before in-memory terminal projection.",
      "entrypoints": ["POST /api/v1/operations/prepare|start", "GET /api/v1/operations/{id}/events", "internal/web/operation_handlers.go:handleOperationStart", "internal/web/operations.go:operationHub/drainAndWait", "internal/app/serve_commands.go"],
      "paths": ["internal/web/operations.go", "internal/web/operation_handlers.go", "internal/web/server.go", "internal/app/serve_commands.go", "internal/app/web_usecases.go"],
      "state": ["in-memory hub records/preparations (TTL 2m, cap 128)", "cleanup-uncertain markers persisted to study/sprint state after drain deadline"],
      "trust_boundaries": ["browser-supplied operation requests re-canonicalized and fingerprint-checked server-side", "session cookie + CSRF token gate every mutation"],
      "dependencies": ["durable-operation-spine", "shared-usecase-vocabulary"]
    },
    {
      "id": "web-security-boundary",
      "name": "Web security, session, and origin controls",
      "domain": "operator-interfaces",
      "risk": "high",
      "purpose": "Loopback trust model enforcement: triple loopback-bind validation, Host pinning, signed anonymous session cookies with derived CSRF, origin proof tiering with fetch-metadata fallback, body/framing limits, in-flight semaphore, CSP headers, build-time policy coherence gate, and out-of-token-flow mutations such as sprint-create.",
      "entrypoints": ["internal/web/security.go:middleware chain", "internal/web/security.go:validCommandRequestOrigin", "internal/web/server_policy.go", "serve --listen"],
      "paths": ["internal/web/security.go", "internal/web/server_policy.go", "internal/app/serve_commands.go", "cmd/ultraplan/main.go:main"],
      "state": ["in-memory HMAC session secret (rotates per process)", "preparation store bound to session+fingerprint"],
      "trust_boundaries": ["the sole network boundary of the product; browser origin vs loopback listener"],
      "dependencies": []
    },
    {
      "id": "tui-console",
      "name": "TUI console dashboard and operations",
      "domain": "operator-interfaces",
      "risk": "normal",
      "purpose": "Bubbletea dashboard over the same use cases: navigation/projection with 1 Hz refresh while run views open, mandatory confirmation pipeline for operations, esc-detach-without-cancel foreground runs, overloaded cancel key including study-cancel issued directly without durable acceptance, quit refusal during running ops.",
      "entrypoints": ["command tui", "internal/tui/app.go:Run", "internal/tui/model.go", "internal/app/tui_commands.go"],
      "paths": ["internal/tui/app.go", "internal/tui/model.go", "internal/tui/views.go", "internal/tui/keys.go", "internal/tui/qa_view.go", "internal/app/tui_commands.go"],
      "state": ["in-memory model only; durability delegated to operation gateway"],
      "trust_boundaries": ["keystrokes construct OperationRequests; local TTY assumed instead of transport controls"],
      "dependencies": ["durable-operation-spine", "shared-usecase-vocabulary"]
    },
    {
      "id": "shared-usecase-vocabulary",
      "name": "Cross-interface operation vocabulary and parity",
      "domain": "operator-interfaces",
      "risk": "high",
      "purpose": "The convergence fabric behind all three frontends: 27-kind OperationKind enum, three structurally different acceptance digest regimes landing in one alias column, divergent cancellation reasons and target checks, readOnly composition split between serve and TUI, and triplicated replay polling logic.",
      "entrypoints": ["internal/app/operations.go:OperationKind enum", "internal/app/usecases.go:dashboardUseCases", "internal/app/surfaces.go:TUIRunner/WebRunner"],
      "paths": ["internal/app/operations.go", "internal/app/usecases.go", "internal/app/sprint_usecases.go", "internal/app/study_usecases.go", "internal/app/web_usecases.go", "internal/app/run_usecases.go", "internal/web/operations_contract_test.go"],
      "state": ["Target.Operation kind strings persisted in run-control rows; dedup keys in OperationAlias column"],
      "trust_boundaries": ["same logical action arriving via different doors must produce equivalent durable semantics"],
      "dependencies": ["durable-operation-spine"]
    },
    {
      "id": "opencode-agent-runtime",
      "name": "OpenCode agent runtime adapter and session stores",
      "domain": "agent-execution-platform",
      "risk": "high",
      "purpose": "Everything below the consumer fakes: request/policy mapping into agentwrap SDK, binary invocation with DB isolation, event ring with sanitize bounds, retry/backoff/backup-model policy, synthesized-cancelled return after 5s grace abandoning teardown observation, session deletion via SQL-through-binary with WAL checkpoint/VACUUM, hashed per-owner runtime stores with dead-owner retention and GC, log pruning.",
      "entrypoints": ["internal/platform/runtime/runtime.go:Adapter.StartRun", "internal/platform/runtime/opencode.go", "internal/platform/runtime/store.go", "internal/platform/runtime/opencode_maintenance.go"],
      "paths": ["internal/platform/runtime/runtime.go", "internal/platform/runtime/opencode.go", "internal/platform/runtime/agentwrap.go", "internal/platform/runtime/store.go", "internal/platform/runtime/opencode_maintenance.go", "internal/platform/runtime/policy.go"],
      "state": [".ultraplan/runtime/opencode/<sha256(owner)[:16]>/opencode.db + store.json", "XDG data-dir OpenCode logs"],
      "trust_boundaries": ["real LLM subprocess output enters as events/results", "deletion SQL built by interpolation behind escaping helper", "GC RemoveAll near live agent state"],
      "dependencies": []
    },
    {
      "id": "process-execution",
      "name": "Subprocess control and spawn policies",
      "domain": "agent-execution-platform",
      "risk": "high",
      "purpose": "Capability grants to child processes: owned process groups with explicit-env-only DirectRunner, SIGTERM-then-SIGKILL ladder with cleanup reporting, truncating stream drains, smoke manifest-to-runner translation, and QA approved-check interpreter/argv/env policy with target drift detection.",
      "entrypoints": ["internal/platform/process/process.go:DirectRunner.Run", "internal/sprint/smoke_protocol.go manifest validation", "internal/sprint/qa_prompt.go approved-check policy"],
      "paths": ["internal/platform/process/process.go", "internal/platform/process/process_unix.go", "internal/platform/process/process_other.go", "internal/sprint/smoke_protocol.go", "internal/sprint/qa_prompt.go"],
      "state": ["evidence roots, harness dirs, temp outputs"],
      "trust_boundaries": ["external manifests and catalog entries become executable argv/env", "child stdout/stderr re-enters as verification evidence"],
      "dependencies": []
    },
    {
      "id": "workspace-scaffold-defaults",
      "name": "Workspace bootstrap, defaults, skills",
      "domain": "foundation",
      "risk": "normal",
      "purpose": "init-workspace scaffolding, materialisation of embedded prompt/template overrides and stage skills with overwrite-confirm semantics, and the workspace-marker discovery plus override-resolution rules consumed at prompt-render time everywhere.",
      "entrypoints": ["commands init-workspace|defaults install|skills materialise", "internal/workspace/discovery.go:Discover", "internal/workspace/paths.go:ResolveInside", "internal/workspace/skills.go"],
      "paths": ["internal/workspace/init.go", "internal/workspace/defaults.go", "internal/workspace/skills.go", "internal/workspace/paths.go", "internal/workspace/discovery.go", "internal/app/workspace_commands.go", "internal/app/defaults_commands.go", "internal/app/skills_commands.go"],
      "state": ["ultraplan.yml marker, README.md, studies/, prompts/, templates/, .agents/skills/**"],
      "trust_boundaries": ["embedded defaults vs user-editable workspace overrides determine prompt content"],
      "dependencies": []
    },
    {
      "id": "config-inspection-health",
      "name": "Config admission, inspection, and health",
      "domain": "foundation",
      "risk": "normal",
      "purpose": "Layered configuration (defaults -> ultraplan.yml -> ~27 env vars -> CLI) with bounds, provenance, and secret redaction; config show rendering; health capability gates; version metadata; feeds sandbox/permission grants to the runtime stack.",
      "entrypoints": ["commands config show [--json]|health [--json]|version", "internal/app/app.go:discoverWorkspace/loadEffectiveConfig (app.go:287-319)", "internal/platform/config/config.go"],
      "paths": ["internal/platform/config/config.go", "internal/platform/config/qa.go", "internal/platform/config/redaction.go", "internal/platform/runtime/health.go", "internal/app/config_commands.go", "internal/app/health_commands.go"],
      "state": ["none durable beyond reads; effective config drives all other surfaces"],
      "trust_boundaries": ["environment variables and workspace YAML are untrusted admission channels", "redaction markers applied before any human-visible output"],
      "dependencies": ["workspace-scaffold-defaults"]
    },
    {
      "id": "project-catalog",
      "name": "Project discovery, catalog, roadmap",
      "domain": "foundation",
      "risk": "normal",
      "purpose": "Read-only discovery and inspection of projects: project-index catalog validation, roadmap parsing/ordering and status, reasoning-default resolution chain; the catalog content steers later planning and review stages.",
      "entrypoints": ["commands project list|<p> status|<p> validate", "internal/project/discovery.go", "internal/project/validation.go", "internal/project/roadmap_status.go"],
      "paths": ["internal/project/discovery.go", "internal/project/index.go", "internal/project/roadmap.go", "internal/project/roadmap_status.go", "internal/project/validation.go", "internal/project/reasoning_defaults.go", "internal/app/project_commands.go"],
      "state": ["reads projects/<p>/** (project-index.md, roadmap.md); roadmap delivery marking delegated to publication"],
      "trust_boundaries": ["repo/user-authored markdown catalogs parsed as governing input for agent stages"],
      "dependencies": []
    },
    {
      "id": "code-extraction",
      "name": "Code citation extraction",
      "domain": "foundation",
      "risk": "low",
      "purpose": "Standalone extraction of cited source snippets from reports: inline-range and table parsing, symlink-aware containment within workspace roots, deterministic JSON output with partial-status aggregation.",
      "entrypoints": ["command code <report>", "internal/codeextract/service.go", "internal/codeextract/resolver.go", "internal/app/code_commands.go"],
      "paths": ["internal/codeextract/parser.go", "internal/codeextract/resolver.go", "internal/codeextract/service.go", "internal/app/code_commands.go"],
      "state": ["optional --output file"],
      "trust_boundaries": ["report-controlled citation paths resolved against real filesystem with escape rejection"],
      "dependencies": ["study-task-execution"]
    }
  ],
  "seams": [
    {
      "id": "cli-acceptance-empty-digest",
      "from": "cli-dispatch-exit-contract",
      "to": "durable-operation-spine",
      "contract": "Every runtime-backed CLI verb must cross beginDurableCLICommand accept-before-execute (inventory-pinned); CLI passes an empty digest so alias dedup is unreachable and each invocation is a new run - callers relying on idempotent CLI retries get none.",
      "risk": "high"
    },
    {
      "id": "three-acceptance-regimes-one-alias",
      "from": "shared-usecase-vocabulary",
      "to": "run-journal-fencing",
      "contract": "Three dedup-key derivations (CLI empty, TUI sha256(canonical+NUL+fingerprint), web sha256(session+NUL+token)) land in one OperationAlias uniqueness column; no test pins any formula, so cross-door double-click protection is asymmetric.",
      "risk": "high"
    },
    {
      "id": "cancellation-reason-parity",
      "from": "shared-usecase-vocabulary",
      "to": "durable-operation-spine",
      "contract": "Cancellation reasons differ per door (CLI whitelist of four vs hard-coded user_requested in web/TUI); QA-cancel ownership precheck exists only in one use-case path; study-cancel is a SIGINT lock signal that bypasses durable acceptance entirely.",
      "risk": "high"
    },
    {
      "id": "outer-inner-double-fencing",
      "from": "durable-operation-spine",
      "to": "run-journal-fencing",
      "contract": "One user operation yields one outer operation run plus N inner runtime runs correlated via ParentRun; two independent 15s leases with 5s heartbeats must stay alive for a clean terminal; QA publishes heartbeat the outer lease from inside sprint under WithoutCancel with 30s timeout.",
      "risk": "critical"
    },
    {
      "id": "persistence-gated-delivery",
      "from": "run-journal-fencing",
      "to": "web-operation-hub-sse",
      "contract": "Observers receive progress events only after successful Append; first Append failure cancels the run context and suppresses display callbacks; hub SSE falls back to durable RunEvents paging (512 @250ms-1s) past hub eviction windows.",
      "risk": "high"
    },
    {
      "id": "productstate-study-mirror",
      "from": "product-state-mirror",
      "to": "study-runloop-scheduler",
      "contract": "When the DB file and study_run row exist, DB wins unconditionally on read while JSON is rewritten only on Complete - mid-run crashes leave a stale file next to fresher DB rows; reset archives only the file copy; zero tests exercise this branch.",
      "risk": "high"
    },
    {
      "id": "productstate-sprint-mirror",
      "from": "product-state-mirror",
      "to": "sprint-flow-state",
      "contract": "flow/execute states flip to DB-authoritative only when all stages/tasks are terminal; loaders are DB-first with row-wins; storage migrate is one-way, holds no file locks, opens both packages' pools simultaneously (up to 8 conns on one WAL database).",
      "risk": "high"
    },
    {
      "id": "flow-state-multi-writer",
      "from": "sprint-planning-chain",
      "to": "sprint-flow-state",
      "contract": "Planning, execute, review, smoke, QA pointers, and status-refresh all write flow-state.json under strict v2 schema; saves must preserve prior Review/Smoke/QA records; readOnly composition only disables status-refresh writes, not operation-driven ones.",
      "risk": "high"
    },
    {
      "id": "execute-evidence-review-input",
      "from": "sprint-execute-resume",
      "to": "sprint-conformance-review",
      "contract": "Review input fingerprints are derived from execute evidence and changed paths; deferred tasks count as complete for promotion while unresolved tasks pre-block; deliberate exclusions (smoke-only index sections, roadmap lifecycle lines) must stay aligned on both sides.",
      "risk": "high"
    },
    {
      "id": "review-digest-gates-smoke",
      "from": "sprint-conformance-review",
      "to": "sprint-smoke-gate",
      "contract": "Smoke admits work only when recorded ArtifactDigest equals current review bytes and governed-input manifest matches; force-review override requires confirmed rationale and cannot promote a failed/blocked review; strict snapshot freshness switches are compile-time false so digest checks carry all enforcement.",
      "risk": "critical"
    },
    {
      "id": "qa-review-freshness",
      "from": "sprint-qa-investigation",
      "to": "sprint-conformance-review",
      "contract": "QA maps derive from fresh terminal review fingerprints; semantic attempt ID changes (e.g. model change) make retained attempts stale; QA can annotate but never alter the review verdict.",
      "risk": "high"
    },
    {
      "id": "verify-promotion-chain",
      "from": "sprint-verify-transition",
      "to": "sprint-smoke-gate",
      "contract": "The verify transition composes requireCompleteExecute, freshness/currentness, and assessment precedence; continuation to smoke occurs only for completed-but-failing reviews with full override confirmation; diagnostic selections must never become canonical.",
      "risk": "critical"
    },
    {
      "id": "qa-writer-fence-handoff",
      "from": "durable-operation-spine",
      "to": "sprint-qa-investigation",
      "contract": "app mints QAWriterToken{RunID,AttemptID,FencingGeneration} from the outer run-control fence; sprint checks it fail-closed in every Publish and the check callback heartbeats the outer lease; sprint never imports runcontrol so token shape/semantics are the entire contract.",
      "risk": "critical"
    },
    {
      "id": "lease-layering-divergent-liveness",
      "from": "sprint-flow-state",
      "to": "study-runloop-scheduler",
      "contract": "Lock families disagree on liveness predicates (EPERM-counts-alive in study/sprint locks vs strict kill(0)==nil in runtime-store cleanup) and on staleness windows (2h verification heartbeats vs 15s run-control leases); cleanup markers are written deliberately leaseless and consumers must re-acquire locks before consuming them fail-closed.",
      "risk": "high"
    },
    {
      "id": "commit-then-publish",
      "from": "sprint-execute-resume",
      "to": "repo-publication",
      "contract": "Durable state always commits first and git publication second; publication failure never rolls back committed state and surfaces as partial success; owned-path sets and CAS parent expectations must match what each stage wrote.",
      "risk": "high"
    },
    {
      "id": "drain-marker-study-reconcile",
      "from": "web-operation-hub-sse",
      "to": "study-runloop-scheduler",
      "contract": "After the 10s drain deadline the hub persists leaseless cleanup-uncertain markers (reason whitelist exactly server_shutdown) BEFORE projecting terminal state; the next boot's reconcile must consume them fail-closed before serving; marker location/format is an unwritten cross-package contract.",
      "risk": "high"
    },
    {
      "id": "drain-marker-sprint-reconcile",
      "from": "web-operation-hub-sse",
      "to": "sprint-flow-state",
      "contract": "Same drain path dispatches sprint-shaped markers consumed by ReconcileInterruptedMutation at serve startup; ANY reconcile error aborts boot before listen, coupling hub shutdown honesty to sprint state-machine tolerance of malformed concurrent states.",
      "risk": "high"
    },
    {
      "id": "agent-runtime-seam-fakes",
      "from": "opencode-agent-runtime",
      "to": "sprint-planning-chain",
      "contract": "Consumers test exclusively against minimal fake runtimes (study.Runtime, sprint.Runtime{StartRun}) while the real adapter stack and everything below Adapter.StartRun (argv construction, stdin prompts, teardown after cancel grace) has no integration coverage; behavioural equivalence across the seam is unpinned.",
      "risk": "high"
    },
    {
      "id": "session-continuity-policy",
      "from": "opencode-agent-runtime",
      "to": "study-task-execution",
      "contract": "SDK treats SessionID/SessionAction as opaque pass-through; ALL continuation policy lives product-side and is split three ways (study fingerprint + ContinueFailures ladder, .stage-sessions.json ignoring prompt checksums, execute batch Resume seeding); restart trigger everywhere is the literal substring 'session not found'.",
      "risk": "high"
    },
    {
      "id": "smoke-manifest-runner",
      "from": "sprint-smoke-gate",
      "to": "process-execution",
      "contract": "Validated protocol-v1 manifests become DirectRunner invocations: exec-not-shell argv, env intersection of PATH/HOME/TMPDIR/LANG/LC_ALL with manifest requests admitted only if already allowlisted, timeouts capped 24h, truncation fails closed, group SIGTERM->SIGKILL cleanup must complete before verdicts.",
      "risk": "high"
    },
    {
      "id": "qa-approved-check-policy",
      "from": "sprint-qa-investigation",
      "to": "process-execution",
      "contract": "Investigator-approved check commands cross an interpreter denylist/metachar ban with cwd==target, LANG/LC_ALL/TZ-only env, and post-execution target-identity drift detection; child env omits PATH while lookup happens parent-side.",
      "risk": "normal"
    },
    {
      "id": "atomicity-tier-divergence",
      "from": "sprint-planning-chain",
      "to": "sprint-execute-resume",
      "contract": "Durability tiers differ within one workflow: flow-state/.run-state.json use temp+fsync+rename+dir-sync, .stage-sessions.json is rename-only without fsync, execute.md and summary.md are plain WriteFile trailing atomic state - consumers must not assume equal crash guarantees across these siblings.",
      "risk": "normal"
    },
    {
      "id": "containment-helper-divergence",
      "from": "workspace-scaffold-defaults",
      "to": "code-extraction",
      "contract": "Four coexisting path-containment implementations (lexical ResolveInside, sprint per-component Lstat walkers, EvalSymlinks+Rel in codeextract/web, runcontrol Lstat boundary) must agree on rejecting escapes under symlinks; a caller choosing the wrong helper for hostile-path contexts weakens the boundary invisibly.",
      "risk": "normal"
    },
    {
      "id": "prompt-override-chain",
      "from": "workspace-scaffold-defaults",
      "to": "sprint-planning-chain",
      "contract": "Workspace override files beat embedded builtins at prompt-read time with project-level reasoning templates as middle layer; materialised skills embed copies of stage prompts that must stay semantically consistent with runtime prompts despite no golden-file pinning.",
      "risk": "normal"
    },
    {
      "id": "shutdown-ordering-chain",
      "from": "web-operation-hub-sse",
      "to": "run-journal-fencing",
      "contract": "Single NotifyContext(INT,TERM) in main drives web drainAndWait(10s) -> leaseless markers persisted before in-memory projection -> server.Shutdown -> pools closed LAST via deferred runControl.Close; all terminal proposals ride detached 30s contexts so shutdown cannot prevent terminal commit; the TUI program is never ctx-torn-down (key-driven exit only).",
      "risk": "high"
    },
    {
      "id": "sse-vocabulary-freeze",
      "from": "web-operation-hub-sse",
      "to": "web-routing-projection",
      "contract": "Server-emitted SSE event names are linked to browser handling only via regex extraction of Object.freeze([...]) from shipped static/js/sse.js; renaming a Go event constant or reformatting the JS silently breaks live updates unless the contract test catches it.",
      "risk": "normal"
    },
    {
      "id": "observation-replay-parity",
      "from": "run-cli-observation",
      "to": "web-operation-hub-sse",
      "contract": "CLI follow, web run-SSE, and web durable-op SSE fallback copy-paste the same 512-batch/1s/250ms replay algorithm with no parity test; cursor semantics (cursor_ahead/replay_gap 409s, terminal+cursor==LastSequence termination) must stay consistent across doors.",
      "risk": "normal"
    },
    {
      "id": "sandbox-policy-grant",
      "from": "config-inspection-health",
      "to": "opencode-agent-runtime",
      "contract": "Effective config becomes permission/sandbox policy per operation role (read_only reviewers, restricted code-context author, workdir-scoped executor, path-allowlisted smoke author); unsupported permission counts must fail closed; the agentwrap dependency was bumped post-freeze, so adapter-level grant behaviour may shift beneath unchanged sources.",
      "risk": "high"
    }
  ],
  "domains": [
    {
      "id": "durability-core",
      "name": "Durability core",
      "surface_ids": ["run-journal-fencing", "run-recovery-retention", "durable-operation-spine", "product-state-mirror"]
    },
    {
      "id": "study-analysis",
      "name": "Study analysis",
      "surface_ids": ["study-authoring", "study-task-execution", "study-runloop-scheduler"]
    },
    {
      "id": "governed-sprint-delivery",
      "name": "Governed sprint delivery",
      "surface_ids": ["sprint-planning-chain", "sprint-execute-resume", "sprint-conformance-review", "sprint-smoke-gate", "sprint-verify-transition", "sprint-qa-investigation", "sprint-flow-state", "repo-publication"]
    },
    {
      "id": "operator-interfaces",
      "name": "Operator interfaces",
      "surface_ids": ["cli-dispatch-exit-contract", "run-cli-observation", "web-routing-projection", "web-operation-hub-sse", "web-security-boundary", "tui-console", "shared-usecase-vocabulary"]
    },
    {
      "id": "agent-execution-platform",
      "name": "Agent execution platform",
      "surface_ids": ["opencode-agent-runtime", "process-execution"]
    },
    {
      "id": "foundation",
      "name": "Foundation",
      "surface_ids": ["workspace-scaffold-defaults", "config-inspection-health", "project-catalog", "code-extraction"]
    }
  ]
}
```