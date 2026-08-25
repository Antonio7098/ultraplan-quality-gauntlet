All six maps read. Map-06's full content is inline in its result.md; the others have canonical maps under `review/maps/`. Now I'll independently verify the repo structure against these claims before reconciling.Core structure verified (17 packages, 15 verbs, dual `DatabaseRelativePath` declarations, empty CLI acceptance digest, 27-kind enum, single `signal.NotifyContext`, TUI without ctx teardown, readOnly composition split, divergent containment resolvers). Verifying last few cited paths before composing the canonical map.```json
{
  "surfaces": [
    {
      "id": "runcontrol-fencing-ledger",
      "name": "Run-Control Fenced Ledger",
      "domain": "durability-run-control",
      "risk": "critical",
      "purpose": "Single durable arbiter for concurrent operations across all frontends: acceptance admission, fenced claims, append-only sanitized event journal, heartbeat leases, single-winner terminal outcomes, quota gates.",
      "entrypoints": ["internal/runcontrol/sqlite.go:SQLiteRepository.Accept/Claim/Append/ProposeTerminal", "internal/runcontrol/lifecycle.go:Heartbeat/RequestCancellation/AcknowledgeCancellation", "internal/runcontrol/sanitize.go"],
      "paths": ["internal/runcontrol/sqlite.go", "internal/runcontrol/model.go", "internal/runcontrol/lifecycle.go", "internal/runcontrol/sanitize.go", "internal/runcontrol/id.go", "internal/runcontrol/interfaces.go"],
      "state": [".ultraplan/run-control.db (WAL, synchronous=FULL, events immutability trigger, fencing generations)", ".ultraplan/run-control.log (capped local JSONL)"],
      "trust_boundaries": ["agent-derived event payloads admitted via Append under two-layer sanitizer", "multi-process writers arbitrated by BEGIN IMMEDIATE + busy_timeout on one file"],
      "dependencies": []
    },
    {
      "id": "runcontrol-recovery-migration-retention",
      "name": "Run-Control Recovery, Migration & Retention",
      "domain": "durability-run-control",
      "risk": "high",
      "purpose": "Crash-recovery arbitration of unowned/expired runs into interrupted/stalled/cleanup_uncertain via process-birth probes; schema migration with backups and proven-stale lock reclaim; retention aging, tombstoning, quota enforcement.",
      "entrypoints": ["internal/runcontrol/lifecycle.go:Reconcile", "internal/runcontrol/migration.go", "internal/runcontrol/retention.go"],
      "paths": ["internal/runcontrol/migration.go", "internal/runcontrol/retention.go", "internal/runcontrol/process_linux.go", "internal/runcontrol/process_darwin.go", "internal/runcontrol/process_other.go", "internal/runcontrol/local_log.go", "internal/runcontrol/metrics.go"],
      "state": ["app_schema/user_version version records", ".ultraplan/run-control.db.bak.* backups (keep 3)", "reconciliation_log evidence", "record_state full/compacted/tombstone"],
      "trust_boundaries": ["migrate-lock reclaim requires exact birth-token mismatch proof", "PID/birth-identity probing of foreign processes (/proc/<pid>/stat starttime + boot_id)"],
      "dependencies": ["runcontrol-fencing-ledger"]
    },
    {
      "id": "durable-operation-spine",
      "name": "Durable Operation Spine (two-tier runs)",
      "domain": "durability-run-control",
      "risk": "critical",
      "purpose": "Every mutating CLI/TUI/web operation crosses accept-before-execute: an outer operation run plus N inner runtime runs correlated via ParentRun; persistence failure prevents child start; terminal outcome mapping and finish retries.",
      "entrypoints": ["internal/app/durable_operations.go:beginDurableCLICommand/newDurableOperationManager/AcceptOperation", "internal/app/run_control.go:controlledRuntime.StartRun/controlOperation", "internal/app/operation_runner.go:sharedOperationRunner", "internal/app/operations.go:RunOperation/FinishOperation"],
      "paths": ["internal/app/durable_operations.go", "internal/app/run_control.go", "internal/app/operation_runner.go", "internal/app/operations.go"],
      "state": ["operation aliases/digests in run-control.db", "in-memory per-op control goroutines and owned map", "250ms progress coalescing windows"],
      "trust_boundaries": ["transport-supplied fingerprints zeroed and re-derived server-side", "three dedup-key derivations (CLI empty, web session+token, TUI canonical+fingerprint) converge on one alias column"],
      "dependencies": ["runcontrol-fencing-ledger"]
    },
    {
      "id": "run-observation-cli",
      "name": "Run Observation & Control CLI",
      "domain": "durability-run-control",
      "risk": "normal",
      "purpose": "`run list/show/follow/cancel/diagnostics` observation plane over the journal: filtered listing with opaque cursors, replay-then-poll follow, reason-whitelisted cancellation, bounded support export.",
      "entrypoints": ["internal/app/run_commands.go", "internal/app/status_json.go"],
      "paths": ["internal/app/run_commands.go", "internal/app/run_usecases.go", "internal/app/status_json.go", "internal/runcontrol/local_log.go"],
      "state": ["reads snapshots/events from run-control.db", "support export ≤1MiB O_EXCL 0600 excluding payloads/secrets"],
      "trust_boundaries": ["cursor tampering at list boundaries", "redaction of lock.command/error messages before display"],
      "dependencies": ["runcontrol-fencing-ledger"]
    },
    {
      "id": "productstate-dual-home-mirror",
      "name": "Product-State Dual-Home Mirror",
      "domain": "durability-run-control",
      "risk": "high",
      "purpose": "SQLite key/state mirror that becomes authoritative for study/sprint state families when its tables exist, with asymmetric checkpoint mirroring back to JSON files; `storage migrate` performs the one-way file→DB import.",
      "entrypoints": ["internal/productstate/store.go:Existing/Load/Save", "internal/app/storage_commands.go:runStorage", "internal/study/state_database.go", "internal/sprint/state_database.go"],
      "paths": ["internal/productstate/store.go", "internal/study/state_database.go", "internal/sprint/state_database.go", "internal/app/storage_commands.go"],
      "state": ["product_states/product_state_items tables inside .ultraplan/run-control.db", "source JSON files remain as checkpoints"],
      "trust_boundaries": ["mere existence of DB file + row switches authority for three state families", "hash-guarded upserts; hashes stored but not re-verified on load"],
      "dependencies": ["runcontrol-fencing-ledger", "study-runloop-scheduler", "sprint-flow-state-leases", "sprint-execute-resume"]
    },
    {
      "id": "opencode-agent-execution",
      "name": "OpenCode Agent Execution Adapter",
      "domain": "agent-execution-platform",
      "risk": "high",
      "purpose": "Maps product requests onto agentwrap/OpenCode binary invocations: retry/backoff/backup-model policies, permission policies, event ring capture with sanitize bounds, cancellation grace, error taxonomy and redaction.",
      "entrypoints": ["internal/platform/runtime/runtime.go:Adapter.StartRun", "internal/platform/runtime/opencode.go", "internal/platform/runtime/agentwrap.go"],
      "paths": ["internal/platform/runtime/runtime.go", "internal/platform/runtime/agentwrap.go", "internal/platform/runtime/events.go", "internal/platform/runtime/policy.go", "internal/platform/runtime/models.go", "internal/platform/runtime/health.go"],
      "state": ["200-event in-memory ring buffer with dropped accounting", "ephemeral request/response state"],
      "trust_boundaries": ["model output enters as events and terminal output under field/string/depth caps", "OpenCode child inherits full parent environment; prompt delivered on stdin"],
      "dependencies": []
    },
    {
      "id": "session-continuity-checkpoints",
      "name": "Agent Session Continuity & Checkpoints",
      "domain": "agent-execution-platform",
      "risk": "high",
      "purpose": "Decides when agent work resumes an existing session versus restarts fresh: study task fingerprint gates with one-shot fallback, sprint stage sessions in .stage-sessions.json, execute batch-session reuse, shared missing-session stop policy.",
      "entrypoints": ["internal/study/run.go:continuation block", "internal/sprint/session_state.go", "internal/sprint/execute_model.go"],
      "paths": ["internal/sprint/session_state.go", "internal/study/run.go", "internal/sprint/execute_model.go"],
      "state": ["projects/<p>/sprints/<s>/.stage-sessions.json (rename-only write)", "SessionID checkpoints inside flow-state.json and study run-state.json"],
      "trust_boundaries": ["agent-issued opaque SessionIDs consumed without syntactic validation", "literal "session not found" error text triggers fresh-session restart"],
      "dependencies": ["opencode-agent-execution"]
    },
    {
      "id": "runtime-store-gc-hygiene",
      "name": "Runtime Store GC & Log Hygiene",
      "domain": "agent-execution-platform",
      "risk": "normal",
      "purpose": "Lifecycle of per-owner scoped OpenCode stores: store.json PID records, session deletion pipeline (SQL delete + CLI delete + WAL checkpoint/VACUUM), 72h/2GiB store GC invoked by study/sprint, log pruning.",
      "entrypoints": ["internal/platform/runtime/store.go", "internal/platform/runtime/opencode.go:deleteOpenCodeSessions", "internal/platform/runtime/opencode_maintenance.go"],
      "paths": ["internal/platform/runtime/store.go", "internal/platform/runtime/opencode_maintenance.go", "internal/platform/runtime/opencode.go"],
      "state": ["studies/<s>/.ultraplan/runtime/opencode/<sha256(owner)[:16]>/opencode.db + store.json", "XDG data-dir OpenCode logs"],
      "trust_boundaries": ["stores of kill(0)-declared-dead owners become removable (RemoveAll behind managed-root validator)", "SQL-through-binary deletion with escaping"],
      "dependencies": ["opencode-agent-execution"]
    },
    {
      "id": "subprocess-execution-capability",
      "name": "Subprocess Execution Capability",
      "domain": "agent-execution-platform",
      "risk": "high",
      "purpose": "Grants capabilities to child processes: DirectRunner owned process groups with explicit-env semantics and SIGTERM→SIGKILL ladder, smoke harness spawns, QA approved-check interpreter/env policy, hardened-env git subprocesses.",
      "entrypoints": ["internal/platform/process/process.go:DirectRunner.Run", "internal/sprint/smoke_protocol.go", "internal/sprint/qa_prompt.go:approved checks", "internal/platform/gitpublish/publisher.go:git env"],
      "paths": ["internal/platform/process/process_unix.go", "internal/platform/process/process_other.go", "internal/sprint/smoke_protocol.go", "internal/sprint/qa_prompt.go", "internal/platform/gitpublish/publisher.go"],
      "state": ["harness evidence roots and authoring trees (externally owned)", "worktree records"],
      "trust_boundaries": ["external harness manifest steers executable/cwd/evidence roots", "child stdout parsed as protocol data (single-value JSON decode)", "env allowlist intersections and timeout caps"],
      "dependencies": []
    },
    {
      "id": "git-publication-commit-push",
      "name": "Git Stage Publication",
      "domain": "agent-execution-platform",
      "risk": "normal",
      "purpose": "Opt-in commit / commit-and-push of owned-path sets after stage completion: flock serialization, temp-index tree preserving user's index, update-ref CAS against expected parent, bounded push with prompt-disabled env, roadmap delivery marking.",
      "entrypoints": ["internal/platform/gitpublish/publisher.go:Publish", "internal/app/git_publication.go:stagePublisher", "internal/sprint/publication.go", "internal/study/publication.go"],
      "paths": ["internal/platform/gitpublish/publisher.go", "internal/platform/gitpublish/lock_unix.go", "internal/platform/gitpublish/lock_other.go", "internal/app/git_publication.go", "internal/project/roadmap_status.go"],
      "state": ["implementation repo refs and worktrees", "<git-common-dir>/ultraplan-publish.lock", "roadmap.md delivery marks"],
      "trust_boundaries": ["configured remote URL charset validated; push authorization rests on config validation + GIT_TERMINAL_PROMPT=0", "non-linux lock fallback has no exclusion"],
      "dependencies": ["sprint-planning-flow", "sprint-execute-resume", "smoke-harness-gate", "study-runloop-scheduler"]
    },
    {
      "id": "config-precedence-redaction",
      "name": "Effective Configuration & Redaction",
      "domain": "foundation-config",
      "risk": "normal",
      "purpose": "Layered configuration defaults→ultraplan.yml→~30 ULTRAPLAN_* env vars→CLI flags with bounds validation, source tracking, secret-marker redaction applied across outputs; `config show`.",
      "entrypoints": ["internal/platform/config/config.go:Load/Validate", "internal/app/config_commands.go"],
      "paths": ["internal/platform/config/config.go", "internal/platform/config/qa.go", "internal/platform/config/redaction.go", "internal/app/config_commands.go"],
      "state": ["none durable (pure admission layer)"],
      "trust_boundaries": ["workspace ultraplan.yml and environment variables are untrusted inputs", "redaction taxonomy (bearer/sk-/ghp_ markers) gates downstream display"],
      "dependencies": []
    },
    {
      "id": "study-init-applicability",
      "name": "Study Init & Applicability Contract",
      "domain": "study-analysis",
      "risk": "normal",
      "purpose": "Scaffold studies from YAML sources/dimensions with optional shallow git clone; establish the source→dimension applicability predicate every later study operation consumes; strict study.json normalization.",
      "entrypoints": ["internal/study/init.go:Init", "command: study init [--dry-run|--force|--no-clone]", "internal/study/discovery.go"],
      "paths": ["internal/study/init.go", "internal/study/init_yaml.go", "internal/study/init_render.go", "internal/study/init_clone.go", "internal/study/discovery.go", "internal/study/config.go", "internal/study/domain.go"],
      "state": ["studies/<s>/{study-init.yml,study.json,dimensions/,sources/*.ultraplan-source.yml}"],
      "trust_boundaries": ["user YAML (non-strict init yaml vs strict study.json)", "cloned external repository content", "credential redaction in clone output"],
      "dependencies": ["workspace-bootstrap-defaults-skills"]
    },
    {
      "id": "study-task-execution",
      "name": "Study Task Execution (run/synthesize)",
      "domain": "study-analysis",
      "risk": "high",
      "purpose": "Single analysis/synthesis task through the runtime: prompt assembly with workspace overrides, model precedence, fingerprint-gated continuation, validation spec with bounded same-session repair, report/rating parsing, exit classification including clean-exit recovery.",
      "entrypoints": ["internal/study/run.go:RunAnalysis", "internal/study/synthesize.go", "commands: study <s> run|synthesize|prompt"],
      "paths": ["internal/study/run.go", "internal/study/synthesize.go", "internal/study/prompts.go", "internal/study/runtime_validation.go", "internal/study/validation.go", "internal/study/rating.go", "internal/study/edit_warnings.go", "internal/study/reports.go", "internal/study/markdown.go"],
      "state": ["reports/source/<dim>/<src>.md", "reports/final/<dim>.md", "per-task Session checkpoints in run-state"],
      "trust_boundaries": ["LLM output becomes persisted product artifact gated by validators and rating ambiguity detection", "clean-exit recovery classifies Completed when a report validates despite runtime_exit"],
      "dependencies": ["study-init-applicability", "opencode-agent-execution", "durable-operation-spine"]
    },
    {
      "id": "study-runloop-scheduler",
      "name": "Study Run-Loop Scheduler & Resume",
      "domain": "study-analysis",
      "risk": "high",
      "purpose": "Durable resumable multi-task run loop and ephemeral run-all: worker-slot scheduling without batch barrier, priority tiers, memory/disk-pressure adaptation, PID lock ownership with SIGINT cancel lane, resume revalidation, reset-with-archive, append-only history ledger.",
      "entrypoints": ["internal/study/run_loop.go:RunLoop", "internal/study/run_all.go", "internal/study/locks.go:CancelRunLoop", "commands: study <s> run-all|run-loop|--reset|--force-unlock|cancel"],
      "paths": ["internal/study/run_loop.go", "internal/study/run_state.go", "internal/study/state.go", "internal/study/locks.go", "internal/study/cleanup_uncertain.go", "internal/study/run_history.go", "internal/study/memory_pressure.go", "internal/study/disk_pressure.go", "internal/study/run_loop_diagnostics.go"],
      "state": ["studies/<s>/.ultraplan/run-state.json (temp+fsync+rename+dir-sync)", ".ultraplan/run-loop.lock (JSON PID payload)", ".ultraplan/runs/tasks.jsonl + summary.md", ".ultraplan/cleanup-uncertain.json", ".ultraplan/archive/", "diagnostics/run-loop-memory.jsonl"],
      "trust_boundaries": ["host /proc/meminfo and Statfs feed admission and parallelism control", "SIGINT sent to lock-owner PID recorded in the lock file", "cleanup-uncertain marker written leaseless at shutdown, consumed fail-closed at boot"],
      "dependencies": ["study-init-applicability", "study-task-execution", "durable-operation-spine", "git-publication-commit-push", "productstate-dual-home-mirror"]
    },
    {
      "id": "study-validation-summary",
      "name": "Study Validate & Summary Aggregation",
      "domain": "study-analysis",
      "risk": "normal",
      "purpose": "Offline aggregation over completed study artifacts: validate dimension/source coverage aggregation without leaking discovered secrets, deterministic summary.csv regeneration, runs-summary ledger rewrite without execution.",
      "entrypoints": ["internal/study/validation_command.go", "internal/study/summary.go", "commands: study <s> validate|summary|runs summary"],
      "paths": ["internal/study/validation_command.go", "internal/study/summary.go", "internal/study/run_history_summary.go"],
      "state": ["summary.csv (atomic, prior preserved on failure)", "runs/summary.md"],
      "trust_boundaries": ["report content scanned for credentials which must not leak into aggregates"],
      "dependencies": ["study-task-execution", "study-runloop-scheduler"]
    },
    {
      "id": "project-catalog-inspection",
      "name": "Project Catalog & Roadmap Inspection",
      "domain": "sprint-delivery",
      "risk": "normal",
      "purpose": "Discover projects, render status, validate project-index catalogs (contracts/evidence/templates/harness cross-references), resolve reasoning-default precedence; supplies governing inputs and target directories to later stages.",
      "entrypoints": ["internal/project/service.go", "commands: project list|<p> status|<p> validate"],
      "paths": ["internal/project/discovery.go", "internal/project/index.go", "internal/project/roadmap.go", "internal/project/validation.go", "internal/project/reasoning_defaults.go", "internal/project/store_fs.go", "internal/app/project_commands.go"],
      "state": ["read-only over projects/<p>/** (project-index.md, roadmap.md, docs)"],
      "trust_boundaries": ["repo/user-authored catalog content steers agent stages and target-directory resolution (`Target Implementation Directory`)"],
      "dependencies": ["workspace-bootstrap-defaults-skills"]
    },
    {
      "id": "sprint-planning-flow",
      "name": "Sprint Planning Flow (requirements→plan)",
      "domain": "sprint-delivery",
      "risk": "high",
      "purpose": "Governed generation of planning artifacts through ordered stages with validated agent output, byte-stable shared prompt prefix with live-evidence budget, code-context sandbox extraction with atomic promote, structured repair, status/prompt/validate projections.",
      "entrypoints": ["internal/sprint/flow.go:Flow", "internal/sprint/service.go", "commands: sprint <p> <s> flow --to <stage>|status|metrics|validate <stage>|prompt <stage>"],
      "paths": ["internal/sprint/service.go", "internal/sprint/flow.go", "internal/sprint/prompts.go", "internal/sprint/prompt_context.go", "internal/sprint/context_pack.go", "internal/sprint/code_context.go", "internal/sprint/index.go", "internal/sprint/handbook.go", "internal/sprint/reasoning.go", "internal/sprint/plan.go", "internal/sprint/validation.go", "internal/sprint/execute_target.go", "internal/app/sprint_usecases.go"],
      "state": ["planning artifacts (requirements/code-context/index/handbook/reasoning/plan)", "512KiB-budgeted shared prompt prefix", "worktree creation on real code-context flows"],
      "trust_boundaries": ["agent-produced markdown becomes the governing input for execution/review/QA after stage validators", "live source excerpts labelled untrusted inside prompts", "TOCTOU replacement detection on governed files"],
      "dependencies": ["project-catalog-inspection", "durable-operation-spine", "opencode-agent-execution", "session-continuity-checkpoints", "git-publication-commit-push"]
    },
    {
      "id": "sprint-flow-state-leases",
      "name": "Flow-State Authority & Mutation Leases",
      "domain": "sprint-delivery",
      "risk": "high",
      "purpose": "Strict load/save and crash semantics of flow-state.json (v1→v2 read-time migration, preserve-prior-on-save, verification-evidence backfill) plus layered mutation leases, verification-attempt expiry derivation, interrupted-mutation reconciliation, cleanup-uncertain consumption.",
      "entrypoints": ["internal/sprint/state.go:LoadFlowState/SaveFlowState", "internal/sprint/locks.go", "internal/sprint/verification_lock.go", "internal/sprint/service.go:ReconcileInterruptedMutation"],
      "paths": ["internal/sprint/state.go", "internal/sprint/state_database.go", "internal/sprint/locks.go", "internal/sprint/verification_lock.go", "internal/sprint/cleanup_uncertain.go", "internal/sprint/artifacts.go"],
      "state": ["projects/<p>/sprints/<slug>/flow-state.json (strict v2)", ".ultraplan/locks/sprint/<p>--<s>.lock pidfile", "verification attempts inline in flow-state", "cleanup-uncertain markers"],
      "trust_boundaries": ["on-disk state re-read as input under strict loaders", "lock files carry PID-liveness claims (kill(0)/EPERM-as-alive)", "server-shutdown markers written deliberately leaseless"],
      "dependencies": ["productstate-dual-home-mirror"]
    },
    {
      "id": "sprint-execute-resume",
      "name": "Sprint Execute Queue & Resume",
      "domain": "sprint-delivery",
      "risk": "high",
      "purpose": "Executes plan.md task checkboxes through a shared runtime session with stable task IDs and persist-before-launch discipline; operator and agent deferral protocol; resume validation against plan markers; stale-running reconcile; worktree-scoped mutation.",
      "entrypoints": ["internal/sprint/execute.go:Execute/DeferExecuteTask", "commands: sprint <p> <s> execute [--resume|--defer --task --reason]"],
      "paths": ["internal/sprint/execute.go", "internal/sprint/execute_plan.go", "internal/sprint/execute_state.go", "internal/sprint/execute_model.go", "internal/sprint/execute_target.go"],
      "state": ["projects/<p>/sprints/<slug>/.run-state.json (atomic, DB-authoritative mirror when enabled)", "execute.md written once after loop", ".workspace.json worktree record"],
      "trust_boundaries": ["plan checkboxes and `[/] — Deferred:` markers are agent-editable queue inputs", "mutations land in the user's implementation worktree"],
      "dependencies": ["sprint-planning-flow", "sprint-flow-state-leases", "durable-operation-spine", "opencode-agent-execution", "session-continuity-checkpoints", "git-publication-commit-push", "productstate-dual-home-mirror"]
    },
    {
      "id": "sprint-conformance-review",
      "name": "Sprint Conformance Review",
      "domain": "sprint-delivery",
      "risk": "high",
      "purpose": "Bounded parallel read-only reviewer fan-out producing coverage verdicts: resumable attempts with input-fingerprint rebasing, focused-rerun promotion rules, verdict ladder (any diagnostic ⇒ blocked), atomic review.md publication after validation.",
      "entrypoints": ["internal/sprint/review.go:Review", "command: sprint <p> <s> review [--focus|--restart|--parallel]"],
      "paths": ["internal/sprint/review.go", "internal/sprint/review_runtime_validation.go"],
      "state": ["review stage records in flow-state.json", "review.md (atomic write)", "review input fingerprint (SHA-256 over governed inputs)", "retained reviewer sessions", "0400 snapshot cache"],
      "trust_boundaries": ["model coverage verdicts decoded tolerantly then judged against decision tables", "citation containment checked against frozen manifest contents"],
      "dependencies": ["sprint-planning-flow", "sprint-flow-state-leases", "opencode-agent-execution", "session-continuity-checkpoints"]
    },
    {
      "id": "verify-transition",
      "name": "Verify Promotion Transition",
      "domain": "sprint-delivery",
      "risk": "high",
      "purpose": "The single review→smoke promotion boundary: require complete execute evidence, enforce review freshness by digest equality, derive expired-attempt recovery read-only, run diagnostic override ladder that cannot launder failures, continue into smoke.",
      "entrypoints": ["internal/sprint/verify.go", "commands: sprint <p> <s> verify [--to review|smoke] [--force-review --override-reason]", "internal/sprint/flow.go delegation for --to review|smoke"],
      "paths": ["internal/sprint/verify.go", "internal/sprint/verification_phase.go", "internal/sprint/freshness_policy.go"],
      "state": ["assessment precedence over flow-state Review/Smoke records", "expired verification-attempt derivation"],
      "trust_boundaries": ["ArtifactDigest equality decides currency; overrides require ForceReview + confirmed non-empty rationale"],
      "dependencies": ["sprint-conformance-review", "sprint-execute-resume", "smoke-harness-gate", "sprint-flow-state-leases"]
    },
    {
      "id": "smoke-harness-gate",
      "name": "Smoke Harness Gate",
      "domain": "sprint-delivery",
      "risk": "high",
      "purpose": "Deep verification against an external protocol-v1 harness: static readiness checks with symlink-resolved containment, allowlisted authoring writes under whole-tree snapshots, discovery identity validation, direct bounded argv execution, exact executed-test identity equality, verdict taxonomy.",
      "entrypoints": ["internal/sprint/smoke.go", "commands: sprint <p> <s> smoke [--yes|--dry-run|--level|--suite|--test|--timeout]"],
      "paths": ["internal/sprint/smoke.go", "internal/sprint/smoke_author.go", "internal/sprint/smoke_protocol.go", "internal/sprint/smoke_types.go"],
      "state": ["smoke.md committed atomically before flow-state", "smoke stage digest blocks in flow-state", "external harness tree (externally owned)", ".runtime-metrics.json"],
      "trust_boundaries": ["external harness manifest/executables/stdout are outside UltraPlan's trust", "only place UltraPlan directly execs non-agentwrap argv", "explicit level/suite selections remain DiagnosticOnly unless covering complete mapping"],
      "dependencies": ["verify-transition", "subprocess-execution-capability", "git-publication-commit-push"]
    },
    {
      "id": "qa-investigation",
      "name": "QA Adversarial Investigation",
      "domain": "sprint-delivery",
      "risk": "high",
      "purpose": "Bounded read-only adversarial QA over changed paths: byte-stable shard mapping, worker fan-out with wall-clock/turn budgets, fenced private-store publishes with pointer-last ordering, deterministic synthesis, resume/recover/cancel lifecycle.",
      "entrypoints": ["internal/sprint/qa.go:RunQA", "commands: sprint <p> <s> qa start|resume|cancel --run|recover|status|map"],
      "paths": ["internal/sprint/qa.go", "internal/sprint/qa_map.go", "internal/sprint/qa_state.go", "internal/sprint/qa_synthesis.go", "internal/sprint/qa_prompt.go", "internal/sprint/qa_types.go", "internal/platform/config/qa.go"],
      "state": ["verification/state.json + attempts/<id>/{map,shards,synthesis}.json (0700/0600, digest-guarded, symlink-rejecting)", "QAFlowSummary pointer in flow-state (written last)", "QAWriterToken{RunID,AttemptID,FencingGeneration}"],
      "trust_boundaries": ["investigator JSON output gated by decision tables (self-approval rejected, catalog-owned refs)", "writer-token fencing fails closed when no fence configured", "QA can annotate but never alter the conformance-review verdict"],
      "dependencies": ["verify-transition", "sprint-execute-resume", "durable-operation-spine", "opencode-agent-execution"]
    },
    {
      "id": "cli-dispatch-contract",
      "name": "CLI Dispatch & Exit Contract",
      "domain": "interfaces",
      "risk": "high",
      "purpose": "argv dispatch of top-level verbs, global --workspace scan, exit classes 0–8 with stable string codes, single-document JSON envelopes and stdout/stderr stream discipline consumed by scripts and agents, interactive confirmations.",
      "entrypoints": ["internal/app/app.go:Run", "cmd/ultraplan/main.go", "internal/app/json_output.go", "internal/app/qa_errors.go"],
      "paths": ["internal/app/app.go", "internal/app/json_output.go", "internal/app/status_json.go", "internal/app/sprint_commands.go", "internal/app/study_commands.go", "internal/app/run_commands.go"],
      "state": ["none beyond the surfaces each verb invokes"],
      "trust_boundaries": ["argv/env/stdin arrive from users and automation", "error-text classification maps causes to exit codes"],
      "dependencies": ["config-precedence-redaction", "durable-operation-spine"]
    },
    {
      "id": "web-console-projection",
      "name": "Web Console Routes & Projection",
      "domain": "interfaces",
      "risk": "high",
      "purpose": "Loopback dashboard pages and read API: route matching with query allowlists, {data,meta} envelopes, safe markdown rendering, contained artifact previews via HMAC refs, timeline sampling, embedded template/static serving with API compatibility freezes.",
      "entrypoints": ["internal/web/routes.go", "internal/web/handlers.go", "command: serve"],
      "paths": ["internal/web/routes.go", "internal/web/handlers.go", "internal/web/timeline_handlers.go", "internal/web/qa_handlers.go", "internal/web/run_handlers.go", "internal/web/artifacts.go", "internal/web/templates", "internal/web/static", "internal/app/markdown.go", "internal/app/web_usecases.go"],
      "state": ["in-process artifact-ref mint map (restart-invalidated)", "embedded FS assets"],
      "trust_boundaries": ["HTTP requests on loopback", "agent-produced markdown rendered to HTML through goldmark-without-unsafe", "artifact paths resolved via HMAC ref + mint map + symlink evaluation + lexical containment"],
      "dependencies": ["web-security-boundary", "run-observation-cli"]
    },
    {
      "id": "web-operation-hub-sse",
      "name": "Web Operation Hub & SSE",
      "domain": "interfaces",
      "risk": "high",
      "purpose": "Two-phase prepare→confirm mutations with single-use bound tokens, hub caps and dedup, SSE streams with Last-Event-ID replay-gap accounting, graceful drain cancelling non-terminal ops and persisting cleanup-uncertain markers past the 10s deadline.",
      "entrypoints": ["internal/web/operation_handlers.go", "internal/web/operations.go", "internal/web/server.go:drainAndWait", "internal/app/serve_commands.go"],
      "paths": ["internal/web/operations.go", "internal/web/operation_handlers.go", "internal/web/server.go", "internal/app/serve_commands.go"],
      "state": ["preparation store TTL 2m cap 128", "in-memory hub records/events/subscribers with lazy reaping", "cleanup-uncertain markers persisted via study/sprint services"],
      "trust_boundaries": ["confirmation tokens bound to session + canonical request + input fingerprint", "browser SSE clients consume frozen event vocabulary; slow subscribers disconnected"],
      "dependencies": ["durable-operation-spine", "web-security-boundary", "sprint-flow-state-leases", "study-runloop-scheduler"]
    },
    {
      "id": "web-security-boundary",
      "name": "Web Security Boundary",
      "domain": "interfaces",
      "risk": "high",
      "purpose": "Loopback-only admission and request hygiene: triple bind enforcement, Host pinning, signed anonymous session cookies, CSRF comparison, origin proof tiers with fetch-metadata fallbacks, framing/body-size caps, security headers, pre-display redaction, fail-closed policy coherence gate.",
      "entrypoints": ["internal/web/security.go", "internal/web/server_policy.go", "internal/app/serve_commands.go listen validation"],
      "paths": ["internal/web/security.go", "internal/web/server_policy.go", "internal/app/serve_commands.go"],
      "state": ["per-process HMAC session secret (memory-only, rotates on restart)", "MaxInFlight semaphore"],
      "trust_boundaries": ["everything browser-reachable crosses here", "Origin/Sec-Fetch-Site/Referer proofs gate mutations", "HTML form CSRF compared alongside constant-time API CSRF"],
      "dependencies": []
    },
    {
      "id": "tui-console-operations",
      "name": "TUI Console Operations",
      "domain": "interfaces",
      "risk": "normal",
      "purpose": "Terminal dashboard observing projects/studies/runs and driving the same durable operations with Enter-confirmations, priority-ordered cancel key, esc-detach-without-cancel semantics, parallelism form, 1Hz refresh while run views are open.",
      "entrypoints": ["internal/tui/app.go", "internal/app/tui_commands.go", "command: tui"],
      "paths": ["internal/tui/app.go", "internal/tui/model.go", "internal/tui/views.go", "internal/tui/keys.go", "internal/tui/viewport.go", "internal/tui/qa_view.go", "internal/tui/markdown.go", "internal/app/tui_commands.go"],
      "state": ["in-memory clamped event buffers; no durable state of its own"],
      "trust_boundaries": ["keystroke-driven operation construction shares acceptance gateway", "study-cancel issues SIGINT to the run-loop lock owner rather than a durable cancellation record"],
      "dependencies": ["durable-operation-spine", "run-observation-cli", "study-runloop-scheduler"]
    },
    {
      "id": "code-extraction",
      "name": "Code Citation Extraction",
      "domain": "interfaces",
      "risk": "low",
      "purpose": "Standalone extraction of cited code snippets from reports into deterministic JSON with range validation and contained path resolution; optional --output write.",
      "entrypoints": ["internal/codeextract/service.go", "command: code <report>..."],
      "paths": ["internal/codeextract/parser.go", "internal/codeextract/resolver.go", "internal/codeextract/service.go", "internal/app/code_commands.go"],
      "state": ["optional --output file"],
      "trust_boundaries": ["report-controlled citations resolved with symlink-evaluated containment and unique-basename walks"],
      "dependencies": []
    },
    {
      "id": "workspace-bootstrap-defaults-skills",
      "name": "Workspace Bootstrap, Defaults & Skills",
      "domain": "foundation-config",
      "risk": "normal",
      "purpose": "init-workspace scaffolding, embedded defaults/skills materialisation with confirm/force semantics, and the workspace-override-beats-builtin resolution chain consumed by every prompt rendering; ResolveInside containment primitive.",
      "entrypoints": ["internal/workspace/init.go", "commands: init-workspace|defaults install|skills materialise", "internal/workspace/paths.go:ResolveInside"],
      "paths": ["internal/workspace/init.go", "internal/workspace/defaults.go", "internal/workspace/skills.go", "internal/workspace/paths.go", "internal/workspace/discovery.go", "internal/workspace/validation.go", "internal/app/workspace_commands.go", "internal/app/defaults_commands.go", "internal/app/skills_commands.go"],
      "state": ["workspace root ultraplan.yml marker", "prompts/, templates/, .agents/skills/**"],
      "trust_boundaries": ["workspace files override embedded builtins at prompt-read time", "overwrite confirmations read stdin without TTY detection (EOF = decline)"],
      "dependencies": []
    },
    {
      "id": "workspace-health-inspection",
      "name": "Workspace Health Inspection",
      "domain": "foundation-config",
      "risk": "normal",
      "purpose": "`health` readiness gates over workspace discovery/structure, config validity, filesystem permissions, environment, and runtime capability with typed exit precedence and test stub seam.",
      "entrypoints": ["internal/app/health_commands.go", "internal/platform/runtime/health.go"],
      "paths": ["internal/app/health_commands.go", "internal/platform/runtime/health.go"],
      "state": ["read-only diagnostics"],
      "trust_boundaries": ["environment and runtime introspection reported to operators"],
      "dependencies": ["config-precedence-redaction", "opencode-agent-execution"]
    },
    {
      "id": "binary-process-lifecycle",
      "name": "Binary Process Lifecycle",
      "domain": "foundation-config",
      "risk": "normal",
      "purpose": "Process shell of the single binary: signal.NotifyContext(INT/TERM) wiring, TUI/Web runner injection into app.Run, open-browser launch, orderly shutdown propagation as process return value.",
      "entrypoints": ["cmd/ultraplan/main.go", "internal/app/surfaces.go", "internal/app/app.go:Run"],
      "paths": ["cmd/ultraplan/main.go", "internal/app/surfaces.go", "internal/app/app.go"],
      "state": ["none durable"],
      "trust_boundaries": ["OS signal disposition: sole NotifyContext site; library consumers without it take default signals"],
      "dependencies": []
    }
  ],
  "seams": [
    {
      "id": "accept-before-execute-fence",
      "from": "durable-operation-spine",
      "to": "runcontrol-fencing-ledger",
      "contract": "Accept+Claim+Running event must persist before any child work starts; alias uniqueness arbitrates dedup across three frontend keying regimes landing in one OperationAlias column; every append and terminal proposal rides a verified fence with single-winner CAS; persistence failure must prevent child start and map to TerminalPersistenceLost.",
      "risk": "critical"
    },
    {
      "id": "outer-inner-fencing-correlation",
      "from": "durable-operation-spine",
      "to": "opencode-agent-execution",
      "contract": "Inner runtime runs correlate to outer operation runs via Correlation.ProductRunID=ParentRun(ctx); two independent leases/heartbeats must stay alive for a clean terminal; QA writer tokens minted from the outer fence are heart-beat from inside sprint publishes under WithoutCancel.",
      "risk": "high"
    },
    {
      "id": "frontends-acceptance-parity",
      "from": "web-operation-hub-sse",
      "to": "tui-console-operations",
      "contract": "Web/TUI/CLI doors must agree on OperationKind vocabulary values, cancellation reasons and target validation, confirmation gating, and write-on-refresh composition (web readOnly:true vs TUI default-false) even though dedup keys, preparation mechanics, and streaming transports differ.",
      "risk": "high"
    },
    {
      "id": "flow-state-writer-contract",
      "from": "sprint-planning-flow",
      "to": "sprint-flow-state-leases",
      "contract": "All writers (status refresh, stage completion, review/smoke records, QA pointer projection) agree on strict v2 schema, preserve-prior-on-save with nil-record backfill protecting verification evidence, atomic temp+fsync+rename writes, and read-time-only legacy migration.",
      "risk": "high"
    },
    {
      "id": "review-smoke-currency-gate",
      "from": "sprint-conformance-review",
      "to": "verify-transition",
      "contract": "Recorded ArtifactDigest must equal current review.md bytes and the governed-input fingerprint must match for review freshness; deliberate fingerprint exclusions (smoke-index sections, roadmap lifecycle lines) stay aligned between producer and gate; override ladder cannot promote stale results.",
      "risk": "high"
    },
    {
      "id": "qa-admissibility-fingerprint",
      "from": "verify-transition",
      "to": "qa-investigation",
      "contract": "QA admissibility requires a fresh terminal conformance-review fingerprint; semantic attempt IDs derived from model/settings mean resume matches only identical inputs, and recovered state refuses active phases.",
      "risk": "normal"
    },
    {
      "id": "dual-home-authority-switch",
      "from": "productstate-dual-home-mirror",
      "to": "study-runloop-scheduler",
      "contract": "DB file+row presence silently flips state families to DB-authoritative; loaders prefer DB rows unconditionally; mirror predicates differ per family (study mirrors JSON only when Complete; sprint families only when all stages/tasks terminal); archive/reset operates on the file copy only.",
      "risk": "high"
    },
    {
      "id": "sqlite-two-pools-one-file",
      "from": "runcontrol-fencing-ledger",
      "to": "productstate-dual-home-mirror",
      "contract": "Two packages hold independent pools on the same .ultraplan/run-control.db agreeing only via BEGIN IMMEDIATE + shared busy_timeout; pragma hardening differs between them; storage migrate holds both pools simultaneously.",
      "risk": "high"
    },
    {
      "id": "shutdown-drain-marker-chain",
      "from": "binary-process-lifecycle",
      "to": "web-operation-hub-sse",
      "contract": "SIGINT/SIGTERM→ctx→10s bounded drain cancels non-terminal ops reason server_shutdown bypassing session ownership, persists leaseless cleanup-uncertain markers before in-memory terminal projection; next boot reconciles markers fail-closed before serving; TUI program is not ctx-torn-down.",
      "risk": "high"
    },
    {
      "id": "commit-then-publish-ordering",
      "from": "sprint-execute-resume",
      "to": "git-publication-commit-push",
      "contract": "Durable state commits first and git publication second; publication failure never rolls back committed state and is surfaced as partial/error; callers hand exact owned-path sets and rely on publish-lock serialization whose exclusion guarantees differ per platform.",
      "risk": "high"
    },
    {
      "id": "session-continuation-contract",
      "from": "opencode-agent-execution",
      "to": "session-continuity-checkpoints",
      "contract": "SDK treats SessionID/SessionAction as opaque pass-through; all continuation policy is product-side keyed on provider/model/workdir/fingerprint compatibility; the literal "session not found" substring is the shared fresh-restart trigger; stage-session checkpoint writes are rename-only without fsync.",
      "risk": "high"
    },
    {
      "id": "runtime-binary-hop",
      "from": "opencode-agent-execution",
      "to": "subprocess-execution-capability",
      "contract": "Binary invocation contract: argv shape (--format json/--dir/--model/--session/--variant), OPENCODE_DB isolation env, prompt-on-stdin delivery; parent-cancel waits ≤5s grace then returns a synthesized cancelled Result while child teardown proceeds unobserved.",
      "risk": "high"
    },
    {
      "id": "containment-resolver-divergence",
      "from": "sprint-planning-flow",
      "to": "code-extraction",
      "contract": "Agent-authored repo-relative paths and citations are resolved against workspace roots with symlink-aware containment; several package-local resolver implementations (lexical ResolveInside, per-component Lstat walkers, EvalSymlinks+Rel pairs) must agree on escape-rejection semantics.",
      "risk": "high"
    },
    {
      "id": "mutation-lock-family-divergence",
      "from": "study-runloop-scheduler",
      "to": "sprint-flow-state-leases",
      "contract": "Study uses one O_EXCL PID lock with EPERM-counts-alive liveness and SIGINT cancel/force-unlock; sprint stacks in-process sync.Map lease, pidfile, and verification heartbeats with different expiry rules; cleanup-uncertain markers bypass both by design and consumers re-establish leases before consuming fail-closed.",
      "risk": "high"
    },
    {
      "id": "sse-vocabulary-freeze",
      "from": "web-console-projection",
      "to": "web-operation-hub-sse",
      "contract": "Browser JS handles a frozen set of SSE event names and operation-kind aliases; Go emitters and shipped static/js/sse.js must keep identical vocabulary, with regex extraction from the shipped JS as the only automated link.",
      "risk": "normal"
    },
    {
      "id": "config-entry-funnel",
      "from": "config-precedence-redaction",
      "to": "cli-dispatch-contract",
      "contract": "Workspace discovery (global flag → ULTRAPLAN_WORKSPACE → cwd-ancestry ultraplan.yml marker) and effective-config resolution precede every surface; config errors exit 3 before behaviour selection; serve composes env-immune policy and freezes workspace identity at boot.",
      "risk": "normal"
    },
    {
      "id": "prompt-override-consistency",
      "from": "workspace-bootstrap-defaults-skills",
      "to": "sprint-planning-flow",
      "contract": "Workspace prompt/template overrides beat embedded builtins at read time (source labels builtin:<rel>); materialised skills content stays consistent with runtime stage prompts and the 512KiB shared-prefix budget.",
      "risk": "normal"
    },
    {
      "id": "agent-output-trust-transition",
      "from": "opencode-agent-execution",
      "to": "sprint-planning-flow",
      "contract": "Model output becomes persisted artifacts only after tolerant extraction, strict per-stage content validation, bounded same-session repair, and atomic promote-or-restore; retained event payloads may serve as fallback extraction sources alongside terminal output.",
      "risk": "high"
    },
    {
      "id": "smoke-manifest-runner-contract",
      "from": "smoke-harness-gate",
      "to": "subprocess-execution-capability",
      "contract": "Validated manifest becomes DirectRunner argv/env/cwd/evidence-root spawn with env-name intersection allowlist, timeout caps ≤24h, truncation failing closed; whole-tree authoring snapshots bound writable blast radius; post-execution targetIdentity drift detection closes the loop.",
      "risk": "high"
    },
    {
      "id": "journal-observation-parity",
      "from": "run-observation-cli",
      "to": "web-console-projection",
      "contract": "CLI runs commands, web timeline/run pages, and TUI polling read the same cached repository handle; replay batching (512 events), idle/catch-up cadences (1s/250ms triplicated), omission expansion, and record_state visibility must stay consistent across projections.",
      "risk": "normal"
    }
  ],
  "domains": [
    {
      "id": "durability-run-control",
      "name": "Durability & Run Control",
      "surface_ids": ["runcontrol-fencing-ledger", "runcontrol-recovery-migration-retention", "durable-operation-spine", "run-observation-cli", "productstate-dual-home-mirror"]
    },
    {
      "id": "agent-execution-platform",
      "name": "Agent Execution Platform",
      "surface_ids": ["opencode-agent-execution", "session-continuity-checkpoints", "runtime-store-gc-hygiene", "subprocess-execution-capability", "git-publication-commit-push"]
    },
    {
      "id": "study-analysis",
      "name": "Study Analysis",
      "surface_ids": ["study-init-applicability", "study-task-execution", "study-runloop-scheduler", "study-validation-summary"]
    },
    {
      "id": "sprint-delivery",
      "name": "Governed Sprint Delivery",
      "surface_ids": ["project-catalog-inspection", "sprint-planning-flow", "sprint-flow-state-leases", "sprint-execute-resume", "sprint-conformance-review", "verify-transition", "smoke-harness-gate", "qa-investigation"]
    },
    {
      "id": "interfaces",
      "name": "User Interfaces",
      "surface_ids": ["cli-dispatch-contract", "web-console-projection", "web-operation-hub-sse", "web-security-boundary", "tui-console-operations", "code-extraction"]
    },
    {
      "id": "foundation-config",
      "name": "Foundation & Configuration",
      "surface_ids": ["config-precedence-redaction", "workspace-bootstrap-defaults-skills", "workspace-health-inspection", "binary-process-lifecycle"]
    }
  ]
}
```