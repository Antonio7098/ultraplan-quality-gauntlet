# Surface Map — map-01-user-operations (user-visible product operations)

Job: independent product-surface discovery. No findings reported in this phase; risk notes are prioritisation rationale only.

## Provenance

- Target: `/home/antonioborgerees/coding/ultraplan/ultraplan-go`, frozen commit `f0fcd0c2107a8e8d69e1283f9e8d5e2c6da94025`.
- Working tree moved to `ad0be98e0bac587eccb2cc2064699a6bdd905b09` ("Increase shared prompt context budget") during discovery. Sole delta vs the frozen commit: `maxSharedPromptPrefixBytes` 256 KiB → 512 KiB (`internal/sprint/prompt_context.go:20`). All workers verified their reads are valid for both commits except that one constant. Surfaces below are structurally identical at `f0fcd0c`.
- Planning workspace: `/home/antonioborgerees/coding/ultraplan/ultraplan-workspace` @ `ab12dc38059c9bf485f9aced9075bcd7d924cac5` (verified == HEAD).
- Method: 8 parallel bounded `review-worker` discoveries (CLI grammar, study workflow, sprint chain, run control, web, TUI, cross-cutting ops, planning-workspace contract) + mapper verification of load-bearing claims.
- Baseline: `go test ./...`, `-race`, `vet`, cover all passed at freeze (state.json).

## Product shape in one paragraph

UltraPlan Go is a local-first CLI (`cmd/ultraplan/main.go` → `internal/app/app.go:88 Run`) with 14 top-level commands: `version, init-workspace, defaults, skills, config, health, storage, run, project, sprint, study, tui, serve, code`. Users scaffold workspaces and studies, run agent-backed (agentwrap/OpenCode) analyses and a governed sprint stage chain, inspect/resume/cancel durable runs, and observe via TUI and loopback web console. All agent-backed work crosses one durable acceptance boundary (`.ultraplan/run-control.db`) before any child starts.

## Command inventory (entrypoint summary)

| Family | Commands | Dispatcher |
|---|---|---|
| Foundation | `init-workspace`, `defaults install`, `skills materialise [stage]`, `config show [--json]`, `health [--json]`, `storage migrate [--dry-run|--json]`, `version` | `internal/app/{workspace,defaults,skills,config,health,storage}_commands.go` |
| Durable runs | `run list|show|follow|cancel|diagnostics` | `internal/app/run_commands.go:19` |
| Project | `project list|<p> status|<p> validate` | `internal/app/project_commands.go` |
| Sprint | `sprint <p> <s> status|metrics|validate <stage>|prompt <stage>|flow|verify|execute|review(/conformance-review)|qa|smoke` | `internal/app/sprint_commands.go:28` |
| Study | `study init|list|<s> list|summary|validate|status|runs summary|run-loop|run-all|run|synthesize|prompt analysis|synthesis` | `internal/app/study_commands.go:26` |
| Interfaces | `tui`, `serve --listen --open-browser`, `code <report>` | `internal/app/{tui_commands,serve_commands,code_commands}.go` |

Exit classes 0–8 fixed at `internal/app/app.go:15–25`; JSON envelope `{schema_version,command,workspace,status,result}` via `internal/app/json_output.go`. Three coexisting arg parsers (hand loops, stdlib flag, `orderRunArgs`) — inventory fact.

Doc cross-check (`docs/cli-reference.md`): 0 doc-only commands; **undocumented implemented commands**: `tui`, `storage migrate`, `study <s> runs summary`; ~7 flag-level omissions (e.g. `--model` on study run/synthesize/run-all/run-loop; flow override flags); embedded `skillsMaterialiseHelp` lists 9 of the 11 stages actually materialised.

---

## Candidate surfaces

Grouping proposal: **A Studies** (S1–S4), **B Governed sprint delivery** (S5–S8, S15), **C Durable runtime** (S9), **D Interfaces** (S10–S12), **E Foundation** (S13–S14).

### S1 — `study-init-applicability` (domain A)
- Behaviour: scaffold a study from YAML (sources+dimensions), optional shallow git clones, write normalized artifacts; source→dimension applicability contract consumed by every later study operation.
- Entrypoints: `study init [--dry-run --force --no-clone --output]`; sidecar/frontmatter applicability files.
- Files: `internal/study/{init,init_yaml,init_render,init_clone,discovery,markdown}.go`; app dispatch `study_commands.go:1452`.
- State authorities: `studies/<s>/{study-init.yml, study.json, README.md, dimensions/*.md, sources/*.ultraplan-source.yml}`, cloned sources. `study.json` strict v1 (unknown fields rejected).
- Outputs/failure: structured `InitValidationProblem`s → exit 5; clone failures accumulate → exit 8 partial, artifacts kept; never overwrites without `--force`.
- Key tests: `init_test.go` (dry-run determinism, clone args/redaction, path safety), `study_test.go` (sidecar/local metadata precedence, frontmatter), `app/study_init_commands_test.go`.
- Seams: feeds S2/S3/S4 the applicability predicate (`SourceAppliesToDimension`, `prompts.go:163` — enforced uniformly in run skip, task graph, synthesis inputs, validate markers, summary N/A). Path safety via `workspace.ResolveInside`.
- Risk: normal. YAML schema strictness + count-must-match-items rules are user-facing sharp edges; clone runs external git with credential redaction.

### S2 — `study-stage-execution` (domain A)
- Behaviour: single `run <dim> <src>` and `synthesize <dim>` through agentwrap/OpenCode: prompt assembly (workspace overrides + embedded defaults), session continuity by fingerprint, runtime-side validation spec with ≤2 same-session repairs, report validation, publication hook.
- Files: `internal/study/{run,synthesize,prompts,runtime_validation,service,edit_warnings,publication}.go`; platform adapter stack `internal/platform/runtime/{runtime,opencode}.go` (PolicyRunner retries/backoff/backup-model → ValidatingRuntime → ObservingRuntime; cancel-on-ctx with 5 s grace).
- State: scoped OpenCode store `studies/<s>/.ultraplan/runtime/opencode/<hash>/opencode.db`; reports land `reports/source/<dimRef>/<src>.md`, `reports/final/<dimRef>.md`; model precedence CLI `--model` → env `ULTRAPLAN_STUDY_MODEL` → `study.json:model`.
- Failure semantics: inapplicable pair skips before runtime; runtime error whose report validates still records Completed; `runtime_exit` degrades to ValidationFailed; success deletes sessions + VACUUMs.
- Key tests: `run_test.go` (session continue/fresh fallback/recovery mapping/model precedence), `runtime_validation_test.go`, `app/study_run_commands_test.go`.
- Seams: consumes S1 applicability; every invocation wrapped by S9 controlledRuntime (accept-before-start, fail-closed); prompts shared with S3 orchestration.
- Risk: high-value review target — LLM output becomes persisted product artifact; validation gates and "recovered" classifications determine what users are told succeeded.

### S3 — `study-runloop-resume` (domain A)
- Behaviour: ephemeral parallel `run-all` vs durable resumable `run-loop`: per-study lock (O_EXCL + PID liveness + `--force-unlock`), atomic versioned `.ultraplan/run-state.json`, resume = reconcile graph → revalidate completed artifacts on disk → restore history only with completed ledger record + valid artifact; adaptive parallelism under memory/disk pressure; retry taxonomy with `retry_after`; `--reset` archives state after interactive confirm; cancellation leaves unscheduled tasks pending.
- Files: `internal/study/{run_all,run_loop,run_state,state,locks,cleanup_uncertain,run_history,run_history_summary,memory_pressure,disk_pressure,run_loop_diagnostics}.go`; app `study_commands.go:197,546`.
- State authorities: `.ultraplan/{run-state.json, archive/, run-loop.lock, runs/tasks.jsonl, runs/summary.md, cleanup-uncertain.json, diagnostics/}`; productstate DB mirror authoritative when present (`state_database.go`).
- Key tests: `run_loop_test.go` (resume-with-revalidation, reset archive, priority tiers, cancellation), `state_test.go` (atomic save, rename-failure preservation), `locks_test.go`, `cleanup_uncertain_test.go` (fail-closed), `run_history_test.go` (torn-line tolerance).
- Seams: claims S2 execution units; writes S4-inspectable status; lock file is also signalled by `serve` cancellation path; cleanup-uncertain marker reconciled by both study owner and web shutdown.
- Risk: highest concurrency/state-machine density on study side — resume correctness ("resumable not exactly-once", stale-artifact flipping to failed) is the product's core durability promise.

### S4 — `study-reporting-inspection` (domain A)
- Behaviour: read-only assurance: `validate` (structure + per-report validation + run-state parse class), `status` (persisted-state projection incl. lock info, JSON schema), `summary` (CSV rating roll-up, deterministic, never clobbers on failure), `prompt previews` (manifest + exact prompt, zero-runtime promise), plus `ultraplan code` citation extraction (source tables, line specs, basename fallback walk, containment checks).
- Files: `internal/study/{validation_command,validation,summary,status_json? (app/status_json.go)}.go`, `internal/app/code_commands.go`, `internal/codeextract/**`.
- Key tests: `validation_command_test.go` (no-secret-leak assertion), `summary_test.go`, `app/study_{validate,status,summary,prompt}_commands_test.go`, `codeextract_test.go`.
- Seams: consumes outputs of S1–S3; `code` resolves paths against workspace roots with symlink-aware containment (stronger than lexical-only `ResolveInside` used by writers — asymmetry is an inventory fact).
- Risk: normal-high; these commands define what users are told about their own state (exit 5 semantics, JSON contracts).

### S5 — `sprint-planning-flow` (domain B)
- Behaviour: governed artifact chain requirements → code-context → sprint-index → technical-handbook → area-reasoning → reasoning → plan via cumulative `flow --to <stage>` (+dry-run, model/variant overrides incl. per-stage), backed by `flow-state.json` strict machine, byte-stable shared prompt prefix (exact requirements+context bytes + budgeted transient evidence, fail-closed budget), context-pack cache, candidate-promotion for code-context, opt-in publication after each stage.
- Files: `internal/sprint/{state,domain,index,code_context,handbook,reasoning,plan,prompts,prompt_context,context_pack,direct_inputs,artifacts,store_fs,service}.go`; app `sprint_commands.go` (flow/validate/prompt/status/metrics branches).
- State authorities: `projects/<p>/sprints/<s>/flow-state.json` (+ SQLite mirror rows), stage artifacts, `.ultra/cache/sprint-context/...`, `.workspace.json` (worktree pointer created at code-context).
- Failure semantics: completed stages skipped with monotonic runtime call counts; code-context scheduled exactly once per canonical chain; persistence-layer loss ⇒ exit 6 with zero extra model calls; failed generation preserves prior artifact.
- Key tests: `prompt_context_test.go` (byte-exact prefix, budget fail-closed), `efficiency_improvements_test.go` (frozen evidence pack, no selection limit), `code_context_test.go` matrix, `sprint_index_test.go:175` (exactly-once), `app/sprint_commands_test.go:464` (call-count + fail-closed persistence).
- Seams: hands execute-readiness to S6; publishes via S15; prompt bundle surfaced read-only by web/TUI (`PromptBundle`).
- Inventory facts for reviewers: committed budget constant is 256 KiB at `f0fcd0c` (512 KiB at HEAD); tests do not hardcode the number.

### S6 — `sprint-execute-resume` (domain B)
- Behaviour: drive plan.md checkboxes to completion in an approved git worktree: target/worktree resolution & validation (dirty source refused), per-task durable records (`.run-state.json`, strict schema, persist-before-runtime), deferral protocol, execute summary, session checkpoints (`.stage-sessions.json`), interrupted-running ⇒ forced failed.
- Files: `internal/sprint/{execute,execute_plan,execute_model,execute_target,execute_state,session_state}.go`; app `execute` branch `sprint_commands.go:329`.
- Key tests: `execute_state_test.go:264` (fails before runtime when checkpoint cannot persist), `execute_plan_test.go` (stable IDs, deferral ID-stability), `execute_target_test.go` (containment/safety instructions), `sprint_workspace_test.go` (dirty refusal).
- Seams: mutates the real target repo via worktree — strongest mutation surface in the product; fenced into S9 durable operations; publishes target-repo-first via S15.
- Risk: critical — this is where agents modify user code; checkpoint ordering, resume-after-crash semantics, and worktree isolation deserve deep review.

### S7 — `sprint-review-verify-smoke` (domain B)
- Behaviour: multi-worker conformance review over a frozen snapshot with fingerprinted inputs, structured verdict+citation validation, resumable/focused reruns (`--focus`, `--restart`); `verify --to review|smoke` derives a deterministic assessment; smoke runs an external protocol-v1 harness gated on fresh non-failing review, with explicit diagnostic override (`--force-review --override-reason --yes`) that cannot launder a canonical fail.
- Files: `internal/sprint/{review,review_runtime_validation,verify,verification_lock,verification_phase,smoke,smoke_protocol,smoke_author,freshness_policy}.go`.
- State: review/smoke records inside flow-state (+ digests), review snapshot cache `.ultra/cache/review/...`, QA fencing from durable operation token.
- Inventory fact (risk rationale, not a finding): `freshness_policy.go:12–14` sets all three strict snapshot-freshness switches to `false` ("temporarily disabled"); staleness currently falls back to format+digest checks. Enabling them changes gate semantics with no signature changes elsewhere.
- Key tests: `review_test.go` (frozen paths, drift reported w/o blocking, atomic preserve), `verify_test.go:209` (override cannot promote canonical smoke), `smoke_test.go` (malformed run preserves artifact, author allowlist), `app/sprint_verify_commands_test.go` (flag parity).
- Risk: high — these are the product's quality gates; the disabled freshness switches and the override path are exactly where silent gate-weakening would hide.

### S8 — `sprint-qa-investigation` (domain B)
- Behaviour: bounded QA investigation sub-system: byte-stable QA map, shard fan-out with approved argv catalog (read-only investigators/challengers; policy rejects shell/git/writes/env), falsifiable theories, bounded synthesis with hydration-from-retained-shard rules, private QA store with pointer-last publish and symlink/stale-writer rejection; CLI actions `qa run|resume|status|cancel --run|recover [--shard] [--dry-run]`.
- Files: `internal/sprint/{qa,qa_map,qa_prompt,qa_state,qa_synthesis,qa_types}.go`; app `qa` branch + `qa_errors.go` stable public codes.
- Exit nuance (inventory fact): QA cancel/deadline maps to exit 8 unlike sprint's 7.
- Key tests: `qa_*_test.go` suites (policy bounds, drift rejection, panic containment, terminal bound), `app/web_operations_test.go:148` (preparation rejects caller-owned controls).
- Seams: fences into owning S9 operation via QA ownership token; projected read-only by web QA handlers/pages and TUI qa_view.
- Risk: normal-high; policy enforcement around investigator capabilities is security-relevant even though everything stays local.

### S15 — `git-publication-optin` (domain B, small)
- Behaviour: opt-in git publication of owned paths after completed stages; modes off/commit/commit-and-push; nil publisher ⇒ silently skipped everywhere; execute publishes target repo before workspace files.
- Files: `internal/platform/gitpublish/publisher.go`, `internal/sprint/publication.go`, `internal/app/git_publication.go`; config `git.*` (validated modes/bounds, redacted remote charset).
- Key tests: `publication_test.go:26,55`.
- Risk: high consequence despite tiny size — it writes to the user's git history/remote; trigger-point enumeration and path ownership are the review targets.

### S9 — `durable-run-control` (domain C)
- Behaviour: workspace-private SQLite authority for every agent-backed/durable operation: accept (quota-gated) → claim (fencing generation + process birth identity) → sanitized immutable event journal → heartbeat/lease liveness → idempotent cancellation → exactly-one terminal arbitration → reconciliation that never infers success from absence (birth-identity probe; `interrupted` / `cleanup_uncertain` / `stalled`). Retention compaction/tombstone/expiry; schema migration with birth-proof locks + integrity check; support export ≤1 MiB O_EXCL.
- Entrypoints: `run list|show|follow|cancel|diagnostics`; implicit accept for every durable CLI op (`beginDurableCLICommand`, inventory-pinned call sites), web/TUI share `repositoryRunUseCases`.
- Files: `internal/runcontrol/**` (model, sqlite, lifecycle, id, migration, retention, sanitize, process*, local_log, metrics, context, errors), `internal/productstate/store.go`, app glue `run_commands.go`, `run_control.go`, `durable_operations.go`, `run_usecases.go`.
- State: `.ultraplan/run-control.db` (+ WAL, backups, migrate lock), `.ultraplan/run-control.log` (1 MiB drop-cap), productstate tables `product_states`/`product_state_items`.
- Sanitization (described): key allowlist + sensitive-fragment rejection, path/credential/NUL value omission, omission accounting, oversize→warning replacement; app-layer recursive `[REDACTED]` for tool payloads.
- Key tests: `sqlite_test.go` (schema/private-mode/concurrency/CAS), `lifecycle_test.go` (races, clock jumps, reconcile matrix), `process_integration_test.go` (true multiprocess), `fault_test.go` (disk-full/closed/read-only typed failures), `migration_test.go`, `sanitize_test.go`, `app/run_control_inventory_test.go`.
- Seams: the trust spine — every surface's durability claim routes through here; storage migrate imports legacy file states into productstate DB.
- Risk: critical; suggested split if too large: (a) repository/lifecycle/retention internals, (b) observation/cancellation/diagnostics UX.

### S10 — `cli-grammar-exit-contract` (domain D)
- Behaviour: dispatch, global `--workspace`, help/version, exit classes 0–8 with stable code strings, JSON envelope conventions (envelope vs inline vs raw-struct inconsistency is inventory fact), classified errors preserving causes.
- Files: `cmd/ultraplan/main.go`, `internal/app/{app,json_output,qa_errors,version}.go`; contract docs `docs/cli-reference.md`, `docs/phase3-json-schemas.md`.
- Key tests: `app_test.go` (classified cause/code), per-command help/usage tests.
- Risk: normal; it is the automation contract — exit-code and JSON-shape regressions break users silently.

### S11 — `web-console` (domain D)
- Behaviour: `serve` loopback-only console: HTML pages (dashboard/projects/sprints/QA/studies/runs/artifacts/operations), versioned JSON API `/api/v1/*`, SSE streams with stable event names and replay-gap honesty, two-phase prepare/start confirmation with session-bound TTL tokens, CSRF/HMAC sessions, host pinning, origin tiering, body/framing discipline, semaphore, no CORS; graceful drain persists cleanup-uncertain markers.
- Entrypoints: `serve --listen (127.0.0.1:8080) --open-browser`; ~18 API resources + pages; mutating endpoints: operations start/cancel, run cancel, sprint-create.
- Files: `internal/web/**` (server, server_policy, security, routes, handlers, operation/run/qa/timeline handlers, operations hub, templates/static embedded), app `serve_commands.go`, `web_usecases.go`.
- Inventory facts (not verdicts): `POST /projects/{p}/sprints/create` bypasses the confirmation-token flow (session+CSRF only); `validCommandOrigin` docstring contradicts its strict-equality body; terminal-operation SSE triggers full page reload (self-flagged P0 in `docs/ui-audit.md`); compatibility frozen by executable fixtures (`api_compatibility_test.go`, `operations_contract_test.go`, baseline doc).
- Key tests: security_test suite, operations hub tests, integration test driving real use cases end-to-end.
- Risk: high — largest untrusted-input surface (browser origin), though bound-restricted; confirmation-flow completeness and redaction-before-projection are review priorities.

### S12 — `tui-dashboard` (domain D)
- Behaviour: bubbletea alt-screen dashboard (Projects/Studies/Runs tabs, route stack, 14 route kinds), ~24 operation kinds behind mandatory CONFIRM dialogs (prepare→sha256 dedup→durable accept), foreground progress stream (100-event bound), 1 Hz run-view polling, `c` cancels durable or local work, quit refused while active work runs; no direct filesystem/git writes.
- Files: `internal/tui/**` (app, model, views, keys, viewport, qa_view, theme, markdown) + `internal/app/tui_commands.go`.
- Inventory facts: non-TTY falls back to `/dev/tty` then exit 1 (bubbletea behaviour, derived from vendored source); enum kinds `sprint-stage`/`sprint-stage-dry-run` exist but no TUI navigation item constructs them.
- Key tests: `model_test.go` (operation inventory, confirmation, parallelism form), `views_test.go` (verdict-neutral QA), `run_view_test.go`, `app/tui_commands_test.go:52` (end-to-end flow streaming).
- Risk: normal; shares the S10/S9 seams; parity with web kind vocabulary is pinned by tests.

### S13 — `workspace-config-skills-defaults` (domain E)
- Behaviour: bootstrap and customization: `init-workspace` (never overwrites; minimal tree `ultraplan.yml`,`README.md`,`studies/`), config system (single-file search, 26+ env overrides incl. generated QA keys, precedence default→workspace→env→CLI with per-field provenance, strict validation, deterministic redaction), `defaults install` (embedded 27 assets, customize-detect, stdin confirm/`--force`), `skills materialise` (11 manual-only stage skills rendered to `.agents/skills/<stage>/{SKILL.md,agents/openai.yaml}` with distinct state-ownership contracts: delegate=review+code-context, prompt-only=execute, repair=reconcile), `health` (workspace/config/env/runtime checks; note: no runcontrol DB check participates).
- Files: `internal/workspace/**` (discovery, paths, init, defaults, skills), `internal/platform/config/**`, app `{workspace,config,defaults,skills,health}_commands.go`.
- Key tests: `workspace_test.go` (precedence, ResolveInside escape rejection), `skills_test.go` (ownership boundaries pinned), `config_test.go` (precedence/redaction), `app_test.go` (confirm matrix).
- Seams: everything depends on discovery+config; skills invoke S5/S6/S7 operations (the `$ultraplan-code-context` canonical delegation seam).
- Risk: normal-high; env override handling and redaction are trust-adjacent; skills encode human/agent division-of-labour promises.

### S14 — `project-catalog-roadmap` (domain E)
- Behaviour: project discovery (safe-name charset), reference resolution (unique-prefix, ambiguity errors), status composition (docs/roadmap/index/sprints + reasoning-default source labels), catalog validation (roadmap↔filesystem cross-check: active-without-dir error, dir-unclaimed error, delivered-without-dir warn; smoke-harness absolute-path + symlink-evaluated manifest containment; cross-project template rejection; deprecated scope-field rejection), three-layer reasoning-default resolution (project→workspace→builtin, fail-closed on invalid project override).
- Files: `internal/project/**` (service, discovery, store_fs, validation, roadmap, index, reasoning_defaults), app `project_commands.go`, `project_usecases.go`.
- Key tests: `roadmap_test.go`, `project_test.go`, `reasoning_defaults_test.go`, `app/project_commands_test.go` (exit 5 + stderr findings).
- Seams: S5 validators consume the same catalog rules (sprint-index must be strict subset of project-index); dashboard/web reuse `ProjectSummaries`.
- Risk: normal; governs what counts as a legal sprint target.

---

## Seam register (cross-surface contracts)

1. **Durable acceptance seam** — every agent-backed/mutating operation (CLI `beginDurableCLICommand`, TUI/web `AcceptOperation`) accepts a run before any child starts; acceptance failure prevents execution (fail-closed). Owners: S9 ↔ S2/S3/S5–S8.
2. **Operation-kind vocabulary seam** — closed enums (`internal/app/operations.go`) shared by TUI menus, browser JS, and CLI; pinned by contract tests on both sides.
3. **Confirmation seam (web)** — prepare(side-effect-free, fingerprinted, TTL 2 min, cap 128) → start(atomically consumed, session-bound); one known non-confirming mutation: sprint-create.
4. **Applicability seam (studies)** — single predicate consumed identically by run-skip, task graph, synthesis preflight, validate markers, summary N/A.
5. **Byte-stable prompt prefix seam (sprint)** — exact requirements+context bytes reused across preview and runtime requests; budget enforced fail-closed; constant differs between frozen commit (256 KiB) and HEAD (512 KiB).
6. **Publication seam** — stage completion → optional git commit/push of owned paths only; nil-off mode silently disables everywhere.
7. **Gate seam** — review verdict freshness gates smoke; diagnostic override recorded but cannot launder canonical fail; freshness switches currently compile-time false (inventory fact).
8. **Cleanup-uncertainty seam** — shutdown/interrupt writers persist `.cleanup-uncertain.json`; owners reconcile to explicit cancelled/interrupted evidence or fail closed.
9. **Redaction seam** — config redaction, runcontrol sanitize allowlist, app-layer tool-payload redaction, health message redaction — four layers with different marker sets (inventory fact worth a focused look).
10. **State-migration seam** — legacy file-based states remain checkpoints; `storage migrate` imports into SQLite; DB becomes authoritative only when present (`state_database.go`).
11. **Path-containment seam** — two strengths coexist: lexical `workspace.ResolveInside` (writers/planners) vs symlink-evaluated containment (codeextract, smoke harness, web artifacts).
12. **Skills delegation seam** — materialised skills either own the work manually (default), delegate canonically (code-context→flow), or restrict CLI to status/prompt/validation (execute, review).

## Reviewer prioritisation (risk rationale only)

1. S6 sprint-execute-resume — agents mutate user code in worktrees; checkpoint/resume ordering.
2. S9 durable-run-control — durability spine; arbitration, reconciliation, sanitization.
3. S3 study-runloop-resume — densest state machine; resume-vs-reset semantics.
4. S7 review/verify/smoke gates — freshness switches disabled; override path.
5. S11 web-console — largest untrusted-input surface; confirmation completeness.
6. S2 study-stage-execution — LLM output→artifact promotion and recovery classification.
7. S15 git-publication — small but writes user git history.
8. S5 sprint-planning-flow — exactly-once clauses, budget fail-closed behavior.
9. Remaining surfaces: normal priority.

## Neutral inventory facts carried forward (not findings)

- Undocumented commands/flags and stale embedded help (see command inventory section).
- Three arg-parser styles; three JSON output styles (envelope/inline/raw struct).
- QA cancel/deadline exits 8 vs sprint's 7.
- Freshness constants false with explanatory comment.
- Health has no runcontrol DB check; `filesystem.read` health check is static-ok.
- Local log drops records at 1 MiB cap rather than rotating.
- Web `sprint-create` outside confirmation flow; `validCommandOrigin` docstring/body mismatch; SSE terminal reload (self-reported audit item).
- TUI non-TTY `/dev/tty` fallback; unused operation-kind enum entries.
- Two path-containment strengths; four redaction layers with differing marker sets.
- `docs/ui-audit.md` partially historical; `.ultra/cli` migration mentioned in README docs list but no implementation found (closest: `storage migrate`).
