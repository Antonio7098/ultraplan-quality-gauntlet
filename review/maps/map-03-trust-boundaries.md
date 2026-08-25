# Surface Map — map-03-trust-boundaries (untrusted inputs & capability transitions)

Job: independent product-surface discovery. No findings reported in this phase; risk notes are prioritisation rationale only.

## Provenance

- Target: `/home/antonioborgerees/coding/ultraplan/ultraplan-go`, frozen commit `f73c4dc659ba1492c16382be26ffcbce1a5ea84a`.
- Working tree at `8eef70f4903b25580719960009a170945bdad9ad`; verified `f73c4dc` is its direct parent and the sole delta is go.mod/go.sum (agentwrap SDK bump). All source line references below are valid for the frozen commit.
- Planning workspace `ultraplan-workspace @ ab12dc38059c` treated as context only.
- Method: 6 parallel bounded `review-worker` discoveries (CLI/config input channels, agent-output trust transition, filesystem containment, web/TUI input boundary, subprocess capability boundary, durable-state capability gates) + mapper verification of load-bearing claims (code_commands.go output confinement; productstate hardening absence; run SSE lifetime).
- Baseline: all green incl. -race/vet/cover (state.json).

## Lens statement

Surfaces below are grouped by where an untrusted input crosses into trusted state or where the system's capability changes. Two inventories first; surfaces follow.

## Inventory A — untrusted/external input channels

| # | Channel | Enters via | First validation point |
|---|---|---|---|
| IN-1 | CLI flags/args | cmd/ultraplan → internal/app/*_commands.go parsers | per-command parse loops + config.Validate bounds (app.go:198-220, config.go:425-538) |
| IN-2 | Environment variables | ~30 `ULTRAPLAN_*` vars through config pipeline + `ULTRAPLAN_WORKSPACE`, `ULTRAPLAN_STUDY_MODEL`, smoke/QA env name intersections | setField coercion + Validate (config.go:275-288); two direct-read bypasses noted in seam S1 |
| IN-3 | Workspace config file `ultraplan.yml` | hand parser loadFile | strict unknown-field error; list items under unknown context silently ignored (config.go:197-269) |
| IN-4 | Study inputs (`study-init.yml`, `study.json`, frontmatter, `.ultraplan-source.yml`) | study init/discovery/load | study.json: DisallowUnknownFields+version pin (study/config.go:49-63); study-init.yml: non-strict yaml.Unmarshal (init_yaml.go:108-112) |
| IN-5 | Repo/user content as data (project catalog, roadmap, sprint artifacts on disk, cloned sources) | project/sprint/study readers | per-reader validators (project/validation.go, code_context.go:163-204) |
| IN-6 | Agent/model output — markdown artifacts | runtime adapter TerminalOutput/events → stage validators | per-stage content validators + bounded repair (runtime_validation.go:87-144 etc.) |
| IN-7 | Agent/model output — structured JSON | review coverage result, QA investigator output, plan task lines | decision tables (review_runtime_validation.go:192-238; qa.go:685-712) with tolerant extraction upstream (review.go:1637-1693) |
| IN-8 | Agent session identifiers | Result.SessionID + every event | opaque; no syntactic validation anywhere; behavioural guards only (session_state.go:158-171; opencode.go:129-149) |
| IN-9 | External process stdout/stderr | smoke harness JSON, QA approved-check output, git CombinedOutput | decodeOneJSON single-value rule (smoke.go:625-637); credential redaction (study/init_clone.go:52-65); safeArgv projection (smoke.go:638-657) |
| IN-10 | HTTP requests | web server loopback listener | single middleware chain security.go:102-178 (target size, framing, Host equality, session, origin proofs, CSRF, body cap) |
| IN-11 | TUI keystrokes/forms | bubbletea model | static OperationRequest construction (model.go:408-571); parallelism form bounds 1–64 (app.go:79-83) |
| IN-12 | Durable files re-read as input (flow-state.json, .run-state.json, run-state.json, tasks.jsonl, QA store, cleanup markers, lock/pidfiles) | strict loaders | DisallowUnknownFields + schema-version gates + trailing-record tolerance rules (sprint/state.go, qa_state.go:309-340, run_history.go:116-126) |
| IN-13 | Durable journal event payloads (runcontrol Append) | producers incl. agent-derived events | two-layer sanitizer allowlist/denylist + oversize replacement (sanitize.go:10-117; sqlite.go:1005-1029) |

## Inventory B — capability transitions

| # | Transition | Guard chain | Anchor |
|---|---|---|---|
| CT-1 | dry-run preview → real execution | `--yes` required for non-dry-run smoke/verify-to-smoke (sprint_commands.go:232-235, 285-287, 518-520); web/TUI confirmation cards + single-use preparation records | operation_handlers.go prepare/start |
| CT-2 | operator intent → durable accepted run (accept-before-execute) | Accept(+Claim+lifecycle event) persisted before child start; failure ⇒ no child + persistence_degraded | app/run_control.go:159-343; inventory-pinned 6 sprint + 4 study sites (run_control_inventory_test.go) |
| CT-3 | completion → git publication | mode off/commit/commit-and-push from config; temp index, update-ref CAS vs expected parent, remote charset validation, GIT_TERMINAL_PROMPT=0 | gitpublish/publisher.go:132-165, 213-260, 284-315 |
| CT-4 | locked → forced takeover / destructive reset | `--force-unlock` = bare flag deletes live lock (study/locks.go:161-167); `--reset` = typed yes or `--yes` then rename-archive (study_commands.go:290-316; run_loop.go:559-573); migration lock reclaim requires proven-stale identity (migration.go:140-165) |
| CT-5 | file authority ↔ productstate DB authority | DB authoritative iff `.ultraplan/run-control.db` exists (productstate/store.go:41-51); reads prefer DB unconditionally; writes dual-path with checkpoint predicate (sprint/state.go:219-240; study/state.go:59-71); `storage migrate` guarded importer |
| CT-6 | sandbox grant to agent | read_only/restricted policies, tool deny-by-default catalogs, writable-path allowlists; unsupported permission counts fail closed (qa_prompt.go:175-190; code_context.go:417-424; smoke_author.go:56-111) |
| CT-7 | spawn external process | DirectRunner exact-env children (process.go:84-86) vs opencode/git full-parent-env children ([SDK] process.go:22-24; publisher.go:291-302); QA check argv/env allowlists + target-identity drift detection (qa_prompt.go:89-134, 262-284) |
| CT-8 | fenced ownership of a run | Claim allocates fencing_generation MAX+1; verifyFence quadruple on every mutation; terminal CAS single winner | runcontrol/sqlite.go:486-592, 985-1003, 722-807 |

---

## Candidate surfaces

Grouping proposal: **A Admission** (S1–S2), **B Agent trust transitions** (S3–S5), **C External process & repo mutation** (S6–S7), **D Persistence trust** (S8–S10), **E Observation** (S11–S12).

### S1 — `cli-config-admission` (domain A) — risk: normal
- Behaviour: every externally supplied flag/env/file value is parsed, layered (defaults→workspace→env→CLI), validated, and mapped to exit classes before any behaviour is selected; destructive-command guard layering lives here.
- Entrypoints: global dispatcher app.go:144-177; per-command parsers; `config show`.
- Trust role: IN-1/2/3 admission; CT-1/CT-4 flag guards.
- State: none durable (pure admission) except written configs during init.
- Primary files/symbols: internal/app/app.go (Run, parseGlobalFlags, classedError), internal/platform/config/{config,qa,redaction}.go, internal/workspace/{discovery,paths}.go, per-command parse functions, json_output.go.
- Tests: config_test precedence/bounds tables; serve_commands_test listen-before-workspace ordering; study_run_loop_commands_test reset-confirmation; TestRunCommandsListShowFollowCancelDiagnosticsAndSupport.
- Seam observations (facts): `ULTRAPLAN_STUDY_MODEL` and `code --output` reach execution/write paths without the confinement/validation their siblings pass through (study_commands.go:187 vs config.Validate; code_commands.go:61-64 vs study_commands.go:1409 ResolveInside gate); `CLIOverrides.LogFormat/LogLevel` escalation path has no production caller; ultraplan.yml parser silently ignores list items under unrecognized context.

### S2 — `durable-operation-gateway` (domain A) — risk: critical
- Behaviour: the accept-before-execute capability transition shared by CLI/TUI/web: confirmation digest dedup across managers, fingerprint re-derivation refusing transport-supplied authority, controlledRuntime claim/event-commit-before-delivery, persistence-failure-prevents-child-start, closed-repository fail-closed, QA writer-token fencing, finish mapping to terminal outcomes.
- Entrypoints: beginDurableCLICommand sites (10, inventory-pinned); AcceptOperation/RunOperation/FinishOperation; hub-backed web operations.
- Trust role: CT-2, CT-8; consumes IN-13 sanitization downstream.
- State: .ultraplan/run-control.db rows + aliases/digests.
- Primary files/symbols: internal/app/{operation_runner,durable_operations,run_control,operations}.go; runcontrol interfaces.
- Tests: durable_operations_test (dedup/fail-closed/stale fence), run_control_test quartet, run_control_inventory_test static completeness, run_tool_observability_test redaction pins.
- Seam observations: controlOperation heartbeat/reconcile tick loop untested; dedup key composition differs between TUI (sha256 canonical+fingerprint) and web (client-supplied dedupKey, session-scoped).

### S3 — `agent-artifact-validation` (domain B) — risk: high
- Behaviour: markdown-ish model output becomes persisted state only after per-stage validators: requirements/index/code-context/handbook/reasoning/plan content rules, byte-stable prompt prefix + TOCTOU replacement detection feeding validators, exactly-bounded same-session repair then fail-without-promotion, plan task extraction with deferral markers and forbidden-stage-content scan, study report/rating/citation validation plus source-edit warnings and clean-exit recovery when a report validates despite runtime_exit.
- Entrypoints: sprint flow stages; study run/synthesize.
- Trust role: IN-5+IN-6 transition; CT-6 read-only policy verification.
- State: candidate temp artifacts promoted by rename-with-restore (code_context.go:457-506); flow-state stage records.
- Primary files/symbols: internal/sprint/{validation,index,code_context,prompt_context,handbook,reasoning,plan,execute_plan,runtime_validation,input_contract,direct_inputs}.go; internal/study/{validation,rating,markdown,runtime_validation,edit_warnings}.go.
- Tests: code_context_test 12-mutation matrix; plan_test table; reasoning/handbook unsafe-path tests; run_test clean-exit recovery + edit warnings; session_state checksum tolerance.
- Seam observations: extraction accepts retained-event payloads as fallback output sources (review/code-context "captured event" paths); requirements validator lacks systematic negative table (fact from test-topology map, consistent here).

### S4 — `structured-untrusted-decoding` (domain B) — risk: high
- Behaviour: machine-readable verdict-bearing outputs from agents and child processes are extracted tolerantly, decoded strictly, judged against decision tables, and gated: review coverage verdict ladder (blocked on any diagnostic; severity ladder; citation containment against frozen manifest contents), QA fenced-output gates (budgets, self-approval rejection, catalog-owned check refs), smoke harness JSON single-value decode plus forbidden-content scan of smoke.md, rating ambiguity detection.
- Entrypoints: review/smoke/qa stages; study summary.
- Trust role: IN-7/IN-9 transition; verdicts feed CT-1 gating of later stages.
- State: Review.Resume.Coverage checkpoints in flow-state; QA private store; review snapshot cache (0400 files).
- Primary files/symbols: internal/sprint/{review,review_runtime_validation,qa,qa_synthesis,smoke,smoke_types}.go; internal/study/rating.go.
- Tests: review_test verdict/citation/extraction-tolerance quartet; qa_test strict-decoding and turn-gate tests; smoke_test malformed-rerun preservation.
- Seam observations: extractReviewJSON keeps the last decodable coverageId object in mixed prose; smoke-stage parsing consumes external-process (not model) output — adjacent but distinct provenance inside one surface.

### S5 — `session-identity-continuation` (domain B) — risk: high
- Behaviour: opaque agent-issued session IDs become resumable capability handles: checkpoint scoping by provider/model/workdir (+prompt checksum diagnostic-only), continuation fingerprints over mutable inputs, one-shot fresh-session fallbacks, missing-session stop policy, post-success deletion via SQL-through-binary with quote escaping + WAL checkpoint/VACUUM, scoped runtime stores keyed sha256(owner) with dead-owner retention.
- Entrypoints: sprint planning stages, review coverages, study tasks; platform adapter stack.
- Trust role: IN-8 admission (no syntactic validation anywhere — behavioural guards only); CT-7 deletion capability.
- State: .stage-sessions.json, flow-state coverage sessions, study run-state checkpoints, .ultraplan/runtime/opencode/<hash>/ stores.
- Primary files/symbols: internal/platform/runtime/{opencode,opencode_maintenance,store,policy,runtime}.go; internal/sprint/session_state.go; internal/study/run.go continuation block.
- Tests: session_state_test compatibility matrix; run_test continuation trio; opencode_test SQL escaping; store_test managed-root/dead-owner pins.
- Seam observations: three divergent PID-liveness predicates exist (EPERM-as-alive in study/sprint locks vs strict kill(0)==nil in runtime store cleanup) — fact recorded for reviewers; deletion SQL built by string interpolation behind sqliteString escaping.

### S6 — `subprocess-execution-capability` (domain C) — risk: high
- Behaviour: everything that grants a child process capabilities: DirectRunner owned process groups with explicit-env-only semantics and SIGTERM→SIGKILL group ladder; smoke manifest v1 validation (schema/capabilities/timeouts ≤24h, symlink-evaluated containment of manifest/executable/cwd/evidence roots, exec-not-shell argv, env intersection allowlist PATH/HOME/TMPDIR/LANG/LC_ALL); QA approved-check policy (interpreter denylist, metachar/write-flag bans, cwd==target, LANG/LC_ALL/TZ-only env, targetIdentity drift detection); authoring-time writable-set snapshots; git subprocess families (publisher hardened-env vs identity/worktree/clone helpers inheriting full env).
- Entrypoints: pprocess.Runner consumers (service.go:36; smoke.go:97,150; qa_prompt.go:272); gitpublish; SDK spawn path.
- Trust role: CT-7; IN-9 production.
- State: evidence roots, worktree records, harness dirs.
- Primary files/symbols: internal/platform/process/*.go; internal/sprint/{smoke_protocol,smoke_author,smoke_types,qa_prompt,verify(targetIdentity)}.go; internal/platform/gitpublish/publisher.go.
- Tests: process_test real-children group-kill; smoke manifest/env/authoring tests; qa_prompt_test policy table + drift; publisher_test real-git pair.
- Seam observations: env-inheritance asymmetry is structural (explicit-env DirectRunner vs full-parent-env opencode/git); QA child env omits PATH while executable lookup happens parent-side; non-unix stopAndWait degrade path excluded by build tags on this host.

### S7 — `publication-capability` (domain C) — risk: normal
- Behaviour: opt-in repo mutation: owned-path computation and escape rejection, temporary index seeded from parent preserving user index, commit-tree + update-ref CAS ("branch changed while committing"), push with timeout mapping and prompt-disabled env, flock repo lock, detached-HEAD refusal, roadmap.md delivery marking.
- Entrypoints: stage publishers wired from app/git_publication.go when cfg.Git.StageCompletion ≠ off.
- Trust role: CT-3; consumes IN-3 config value `git.remote`.
- State: git refs, publish lock file.
- Primary files/symbols: internal/platform/gitpublish/{publisher,lock_unix}.go; internal/sprint/publication.go; internal/study/publication.go; project/roadmap_status.go.
- Tests: publisher_test owned-paths + push-retry-no-duplicate; publication_test path sets and ordering.
- Seam observations: contention/off-mode/detached arms untested in-package; push authorization rests entirely on config charset validation + GIT_TERMINAL_PROMPT=0.

### S8 — `filesystem-containment-persistence` (domain D) — risk: high
- Behaviour: lexical and symlink-aware containment helpers applied per package (ResolveInside family; per-component Lstat walkers; EvalSymlinks+inside pairs), atomic write machinery (temp+fsync+rename+dir-sync variants, hook injection points, restore-on-failure compensation), private-mode enforcement (0700/0600/0400 inventories), and every deletion-capable path (runtime store RemoveAll behind managed-root validator; review snapshot teardown; log pruning recency guard; QA attempt pruning; migration backup pruning).
- Entrypoints: cross-cutting; consumed by all other surfaces.
- Trust role: IN-5/IN-12 integrity boundary; constrains CT-7 blast radius.
- State: all durable files listed in map-02; this surface owns the write/read mechanics only.
- Primary files/symbols: workspace/paths.go; sprint/artifacts.resolveSprintContained; sprint/{prompt_context,qa_state,verification_lock}.go readers; runcontrol/sqlite.preparePrivateDatabase; local_log.go; migration.RestoreBackup; platform/runtime/store.go; app/usecases.rejectPreviewSymlink; web_usecases.containedArtifactPath; codeextract/resolver.go.
- Tests: symlink-escape matrices (qa_state_test, usecases_test, qa_prompt_test targetIdentity); preserve-prior-on-rename-failure trio; store_test unmanaged-root rejection; local_log_test privacy/redaction.
- Seam observations: atomic-write strength varies (full fsync+dir-sync for state authorities vs sync-less rename for .stage-sessions.json/runtime store records/run history dir-sync omission); productstate package performs no Lstat/chmod of its own, relying on runcontrol having created the shared path first — ordering between packages untraced.

### S9 — `durable-journal-fencing` (domain D) — risk: critical
- Behaviour: the run-control authority: acceptance admission under soft-quota, fenced claims, sanitized append (allowlisted payload keys, sensitive-field drop, credential-marker/absolute-path rejection, oversize warning replacement), heartbeat lease windows with SQLite-clock authority, cancellation request/acknowledge split (request unfenced CAS; ack fenced), ProposeTerminal immutable single-winner arbitration, reconciliation via exact process birth identity (boot_id/starttime/host digest) that never infers success from liveness, retention advancing replay boundaries, migrations with proven-stale-lock reclaim and corrupt-DB refusal without replacing evidence.
- Entrypoints: repository interface consumed exclusively via app layer.
- Trust role: CT-8; IN-13 admission control for hostile payloads.
- State: .ultraplan/run-control.db (WAL, defensive pragmas, immutability triggers).
- Primary files/symbols: internal/runcontrol/{sqlite,lifecycle,sanitize,retention,migration,id,local_log,process_linux,model}.go.
- Tests: lifecycle_test fencing/CAS/birth-identity/clock-jump; fault_test quota-full never returns uncommitted success + permission-loss fail-closed; process_integration_test two real helper processes; sanitize_test hostile payloads; migration_test corrupt-refusal.
- Seam observations: RequestCancellation takes no fence by design (CAS-guarded, idempotent); reconciler grace decisions distinguish interrupted vs cleanup_uncertain on incomplete identity.

### S10 — `state-authority-handoff` (domain D) — risk: high
- Behaviour: which store wins when both a JSON file and the productstate DB could hold flow/execute/run state: activation predicate = mere existence of `.ultraplan/run-control.db`; unconditional DB preference on read; conditional-upsert sha256 guards with stale-item deletion; checkpoint-to-file predicates; `storage migrate` skip/legacy-terminal guards with dry-run preview and ExitPartial aggregation.
- Entrypoints: every sprint/study load/save; `storage migrate [--dry-run|--json]`.
- Trust role: CT-5; IN-12 interpretation depends on this selection.
- State: product_states/product_state_items tables vs flow-state.json/.run-state.json/run-state.json.
- Primary files/symbols: internal/productstate/store.go; internal/sprint/{state,state_database,execute_state}.go; internal/study/{state,state_database}.go; internal/app/storage_commands.go.
- Tests: none reference productstate/storage-migrate/DB-authoritative branches anywhere in the repo (verified by worker grep twice; recorded as fact).
- Seam observations: both-exist semantics = stale checkpoint files remain on disk and readable by older tooling; help text documents "existing files remain as checkpoints".

### S11 — `web-http-boundary` (domain E) — risk: high
- Behaviour: sole network listener: loopback-only admission enforced three times (CLI preflight, post-listen canonical authority re-validation, per-request Host equality), ordered single middleware (target ≤8 KiB, CL/TE framing, signed session cookie requirement for mutations, exact-Origin proof chain with Sec-Fetch-Site/Referer port-stripped fallback, constant-time CSRF, body ≤64 KiB restricted to operation POSTs, MaxInFlight=32), identifier/opaque-ref regexes at dispatch, query-key allowlists, embedded-FS-only static/template serving with namespace hierarchy validation, goldmark-without-unsafe markdown rendering of agent-produced artifacts, SSE streams with replay bounds/slow-subscriber eviction/session-scoped operations and shutdown drain persisting cleanup uncertainty.
- Entrypoints: `serve --listen`; ~45 routes; API compatibility freeze bound to docs baseline.
- Trust role: IN-10 admission; CT-1 confirmations; observation of all other surfaces.
- State: in-memory hub/preparation stores; session HMAC keys process-lifetime only.
- Primary files/symbols: internal/web/{security,server,server_policy,routes,handlers,operation_handlers,operations,run_handlers,timeline_handlers,artifacts}.go; internal/app/markdown.go; app/web_usecases.go preview containment.
- Tests: security_test 15-test chain; operations_test lifecycle/drain/cleanup-uncertain; packaging_test real binary+socket+SIGINT; api_compatibility freeze; artifacts hostile-markdown tests.
- Seam observations: followRunSSE (durable-run stream variant) has no max-lifetime timer unlike operation SSE; session validity = valid HMAC alone (no server-side revocation); cookie minted even for subsequently rejected requests.

### S12 — `tui-console-entry` (domain E) — risk: low
- Behaviour: keystroke-driven construction of the same OperationRequests and durable cancellations: statically-built nav items, Enter-confirmation card (single keystroke, nothing typed), parallelism form bounds, esc-hides-without-cancelling foreground runs, quit-during-active-run refusal, cancel reason fixed to user_requested.
- Entrypoints: `tui`.
- Trust role: IN-11; CT-1 via same gateway.
- State: none beyond gateway.
- Primary files/symbols: internal/tui/{keys,model,app,views,verify,markdown}.go.
- Tests: model_test synthesized key matrices; views_test render containment; parallelism form pins.
- Seam observations: ActionConfirm defined-but-unreachable by keyboard (confirmation flows through ActionOpen with identical guard); glamour fallback returns raw content unchanged on renderer error (terminal, not HTML).

---

## Seams between surfaces (producer ⇄ consumer)

1. Interface→gateway seam (S11/S12/S1 → S2): all mutating intents converge on AcceptOperation/beginDurableCLICommand; three different dedup-key derivations meet one alias table.
2. Service→validator seam (S3/S4): runtime adapter output → stage validators → promotion-or-fail; extraction tolerance precedes strict decoding by design.
3. Adapter→binary seam (S5 → S6): product-side policy stack composes SDK requests; session deletion crosses back into subprocess capability with interpolated-but-escaped SQL.
4. Manifest→runner seam (S4/S6): validated manifests become DirectRunner argv/env; drift detection closes the loop post-execution.
5. Save/load→authority seam (S8 → S10): atomic file writes vs conditional-upsert DB writes selected by file-existence predicate.
6. Journal seam (S2 → S9): controlledRuntime events pass the two-layer sanitizer before durable append; fencing quadruple binds events to claim generations.
7. Completion→publication seam (S3/S7): validated stage artifacts determine owned-path sets handed to gitpublish.
8. Containment substrate seam (S8 under all): every path-producing surface delegates to a package-local helper rather than one shared resolver — four distinct containment implementations coexist (workspace.ResolveInside lexical; sprint per-component Lstat; codeextract/web EvalSymlinks+Rel; runcontrol Lstat boundary).

## Domain grouping & suggested review order

- **A admission** (S1, S2): where intent becomes capability; S2 critical because it is the single chokepoint invariant (accept-before-execute) protecting every runtime-backed operation.
- **B agent trust transitions** (S3, S4, S5): highest volume of attacker-influenced bytes (model output, repo content) becoming durable state and verdicts; S5 additionally owns a deletion-capable SQL channel.
- **C external process & repo mutation** (S6, S7): capability grants to children and to git remotes; strong existing controls, asymmetric env handling worth one pass.
- **D persistence trust** (S8, S9, S10): corruptible/hostile disk state re-entering the program; S10 flagged high primarily due to zero test evidence across its branches, not observed misbehaviour.
- **E observation** (S11, S12): E1 is the only network boundary with layered defenses already heavily pinned; residual review focus on stream lifetimes and session-model assumptions.

