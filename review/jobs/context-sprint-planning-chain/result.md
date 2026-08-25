# Context Pack: `sprint-planning-chain` — Sprint planning stage chain

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: governed-sprint-delivery. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

Governed, ordered generation of seven planning artifacts through agent-runtime calls:

```
requirements.md -> code-context.md -> sprint-index.md -> technical-handbook.md
-> reasoning/*.md (per selected area) -> reasoning.md -> plan.md
```

Mechanisms implemented on top of the chain:
- **Byte-stable shared prompt prefix** (`maxSharedPromptPrefixBytes` = 512 KiB, prompt_context.go:20): governance text + sprint identity + exact requirements.md bytes + exact code-context.md bytes + transiently resolved source evidence, terminated by one `<<< ULTRAPLAN STAGE-SPECIFIC INSTRUCTIONS BEGIN >>>` boundary (prompts.go:22-33). Every downstream agent call composes `prefix + stage suffix` (prompt_context.go:133-156).
- **TOCTOU replacement detection** on every source read: per-component `Lstat` symlink rejection, `EvalSymlinks` containment, open-handle `SameFile` identity checks before/after read, size+mtime equality, canonical-location stability (readSharedSource prompt_context.go:346-420, verifySharedSourceUnchanged :422).
- **Per-stage content validators** (pure functions of content + manifests/catalog) used for gating, skip decisions, and repair prompts.
- **Bounded repair**: one product-side in-session repair attempt (`generatedArtifactRepairAttempts = 1`, runtime_validation.go:12) after the run; independently, artifact-writing stages attach an agentwrap `ValidationSpec` whose `RepairConfig.MaxAttempts = 2` with fresh-session fallback at attempt ≥2 (runtime_validation.go:127-143).
- **Candidate-temp promotion** for code-context: write `.code-context.*.candidate.md`, validate, `os.Rename` over the artifact, restore prior bytes if flow-state persist fails (promoteCodeContext code_context.go:457-506).
- **Worktree creation**: first code-context resolution creates a Git worktree `ultraplan/<project>/<slug>` branch at `<parent-of-target>/.<target>-ultraplan-worktrees/<project>/<slug>`, recorded in `<sprint>/.workspace.json` (execute_target.go:136-181).
- **Stage-session checkpoints** in `<sprint>/.stage-sessions.json` enabling interrupted-session continuation (session_state.go).

## 2. Entrypoints and control flow

### CLI (`internal/app/sprint_commands.go`)
- Dispatch `runSprint` (:28). Subcommands over `sprint.NewService(root)` (:88, publisher/stage-runtime/smoke settings wired; no runtime attached for read-only paths):
  - `status [--json]` (:90) → `Service.Status`.
  - `validate <stage>` (:142) → `Validate<Stage>`; exit `ExitValidation` when invalid.
  - `prompt <stage> [--explain]` (:180) → `Prompt<Stage>`; prints prompt or JSON `PromptExplanation` + `InputContract`.
  - `flow [--to <stage>] [--dry-run] [--model] [--variant] [--stage-model=<stage>=<v>] [--stage-variant=...]` plus review/smoke flags (:223, parser :1003). Non-dry-run requires `--yes` only for smoke (:233); wraps in `beginDurableCLICommand` (accept-before-execute run-control row, cancellation-aware ctx, `finishDurableCLICommand`) and rebuilds the service with a controlled runtime via `sprintRuntimeService` (:685). Error mapping `mapSprintError` (:944): canceled→ExitCancel, conflict→ExitPartial, malformed state→ExitValidation, ref errors→ExitValidation, default→ExitWorkspace; findings-bearing failures→ExitValidation; "runtime"-string errors→ExitRuntime (:269).
- Single-stage durable operations (web/TUI) call `Service.FlowStage` (operation_runner.go:27, operations.go:435 dry-run).

### Service state machine (`internal/sprint/flow.go`)
- `Flow` (:92): `flowStages(target)` (:257) yields the canonical prefix 1..8 (7 planning stages + execute; review/smoke targets also run all 8 then `Verify`). DryRun: single target stage only; review/smoke dry-run delegates to `Verify(DryRun:true)`. Non-dry: `resolveSprintForRequirements(create=true)` materializes the sprint skeleton **before** lease acquisition (:108-115), then `acquireMutationContext` (:117).
- Per stage: emit progress → if already valid (`flowStageAlreadyValid` :284, validator-based; code-context uses `codeContextPrerequisite`; execute uses `ExecuteComplete`) → clear that stage's saved session checkpoint (:142-146), publish, emit "skipped", continue; else `runFlowStage` (:226) → on success publish (`publishPlanningStage`) → next stage. First failure returns immediately with persisted failed-state.
- `FlowStage` (:189): exactly one planning stage, prerequisites preserved, no earlier scheduling; publishes after success.
- `runFlowStage` dispatch (:226-246): FlowRequirements, FlowCodeContext, FlowSprintIndex, FlowTechnicalHandbook, FlowReasoning (area or final), FlowPlan, Execute(Resume).

### Common non-dry stage shape
1. `resolveSprintInputsForFlow` (service.go:956): DiscoverProjects→ResolveProject→DiscoverSprints→ResolveSprint (exact then unique-prefix; skeleton creation only for unambiguous RefError) → containment check `inside(p.Path, sp.Path)` → `ReadPlanningInputs` (store_fs.go:95: requirements required; code-context/sprint-index optional-missing-ok; project-index.md required; sorted docs list) → `ParseProjectIndex` must produce zero findings.
2. Prerequisite validation per stage (requirements content; code-context prerequisite = artifact valid **and** flow-state shows code-context complete, code_context.go:237; sprint-index/handbook/reasoning/plan manifests built from catalog + prior artifacts).
3. Render stage prompt (prompts.go): base body from workspace override → builtin default (`renderPromptFromDefault` :219; reasoning defaults resolve project→workspace→builtin, :259); injected template files; "Hard constraints" block; Runtime Manifest; then `appendDirectInputPacket` (direct_inputs.go:187) copying governed inputs **in full**, wrapped in `<<< BEGIN/END ULTRAPLAN DIRECT INPUT >>>` blocks with ID/Kind/Path/Mode/Original-Bytes headers; missing inputs listed with reasons redacted to `[workspace]` (:217).
4. `composeSharedRuntimePrompt` (persistCache=true): `prepareSharedPromptContext` (prompt_context.go:93) loads the cached pack (identity-keyed) or renders live; suffix appended after the boundary.
5. `runtimeRequest` (service.go:1122): base config request; WorkDir=workspace root (code-context later overrides WorkDir to the target worktree, code_context.go:306); metadata (project/sprint/stage/task/coverage/area/output_path/candidate_path/target_source/model_source/variant_source/trace_id/prompt checksums/input-contract list); scoped `RuntimeStoreOwner/Path`; `CacheDirective{Mode:"stable-prefix"}` when explanation says cacheable; per-stage model/variant from `planningStageRuntime(config)` (app :894) merged with `withStageOverrides` (service.go:195).
6. `startPlanningStageRun` (session_state.go:105): checkpoint lookup; compatible record (same provider+model+workDir; prompt checksum diagnostic-only, :87) → continue session with continuation instruction inserted right after the boundary (:173); `OnEvent` persists new session IDs best-effort (mutex-deduped); on "session not found" string match across err/output/attempts (:158) → clear key, restart fresh with original prompt.
7. `startSprintRuntime` (runtime_metrics.go:116): `CleanupRuntimeStores(sp.Path, 72h, 2GiB)`; `StartRun`; append record to `.runtime-metrics.json` (atomic, cap 512).
8. Post-run: status must be ""|success|complete|completed; code-context additionally requires `Permissions.UnsupportedCount == 0`; read artifact back from disk; validate; if findings and SessionID ≠ "" → one `repairGeneratedArtifact` (continue session, findings-derived repair prompt, service.go:1197) then re-validate; remaining findings → failed state persisted. Success: compute full success `StageStates` (flow.go:324-383; encodes next-stage Ready and Skipped propagation when `NoTemplates`), `SaveFlowState(NewFlowState(...))`, `cleanupPlanningStageSessions` (delete runtime store/session + remove checkpoint; errors discarded), return.

### Code-context specifics (`code_context.go`)
- `FlowCodeContext` (:257): validated requirements + resolvable target mandatory; candidate temp created in sprint dir (:282, removed via defer); runtime sandbox `"read_only"`, permissions `"restricted"` with allow-list {read,glob,search,list} and `RequireCaps:["permissions"]` (:307-310); output content = TerminalOutput else last event content field (:401); validated twice (content + `validateResolvedCodeContext` dry-render of the shared prefix to prove every reference resolves, :391); candidate re-read and re-validated after write; prefix rendered and cached (best-effort) **before** promotion; `promoteCodeContext` renames and rolls back on persist failure (:457); success marks sprint-index Ready.
- Target/worktree: `resolveSprintTarget(create)` (execute_target.go:56): `**Target Implementation Directory:**` extracted from user project-index (:229); existing `.workspace.json` validated (source-root match, dir exists, git-common-dir identity, current branch match, :99); else `createSprintWorkspace` (:136) requires clean source worktree root, records baseline HEAD, creates branch + worktree, atomically writes record; record-write failure removes worktree and branch (:176-178). `Flow`'s already-valid code-context path re-checks workspace creation findings (flow.go:133-141).

### Shared prefix rendering (`prompt_context.go:158`)
Order: `sharedPromptInstructions` → `Project:/Sprint:` identity → framed exact requirements → framed exact code-context → framed source evidence → boundary. References parsed from the validated "Selected Source References" section (:283); ranges parsed (positive N or N-M, :307), merged/canonicalized per file (:230). Per selection: remaining-budget computation; containment + symlink-free + regular-file + handle-identity + UTF-8 + range-bounds + unchanged checks; framed as `UNTRUSTED TRANSIENT SOURCE EVIDENCE`; running total checked against 512 KiB including closer+boundary (:196, :213, :221). Any failure is a typed `promptContextError` (kinds: invalid_path, containment, file_kind, missing_source, invalid_range, changed_during_read, invalid_encoding, budget_exceeded, :24-35) with diagnostics that avoid absolute implementation-root leakage (:70).

### Context pack cache (`context_pack.go`)
Key = sha256("sprint-context-v1\x00"+reqDigest+"\x00"+ccDigest+"\x00"+targetDigest) (:34); stored at `<root>/.ultra/cache/sprint-context/<project>/<slug>/<key>.json` (:47). Load verifies schema/project/sprint/key/all digests, prefix digest, boundary suffix, and size cap (:61-66); mismatch → treated as miss. Save refuses invalid payloads, writes atomically, prunes to 8 newest by mtime (:91). Both directions are non-authoritative: read failure falls back to live render; write failure is ignored (prompt_context.go:110-122).

### State authorities (`state.go`, `state_database.go`)
- `LoadFlowState` (:20): SQLite mirror first (productstate kind `sprint_flow`, scope `p/s`); else JSON `flow-state.json` schemaVersion 2 (v1 migrated; pre-code-context 6-stage layouts interpreted with an inserted code-context stage, :74/:96). Strict decode (DisallowUnknownFields, trailing-value rejection), `ValidateFlowState` (:294: exact stage list/order, valid statuses, contained paths, sanitized error text, outcome enum).
- `SaveFlowState` (:201): preserves prior Review/Smoke/QA records; DB-authoritative when a mirror row exists (JSON checkpoint then written only for all-terminal stage states, :233); otherwise atomic fsync+rename JSON write (:242-292).
- `DeriveStages` (service.go:1484): artifact-presence derivation with code-context requiring prior complete status; area-reasoning derived from `reasoning/` contents or explicit no-selection; first Missing stage becomes Ready; prior Failed preserved as blocker. `Status()` (:229) derives and (unless `WithoutStatusWrites`) saves refreshed state.

## 3. Inputs / outputs

Inputs: argv; env/config (models, variants, timeouts via `RequestFromConfig`); filesystem — workspace project docs/catalog (`project-index.md`, `roadmap.md`, `docs/*.md`), prior sprint `review.md`s (lexicographically earlier slugs only, direct_inputs.go:107), prompt/template overrides, prior artifacts, live target-repository source; stdin none. Outputs: stdout human/JSON results; stderr `[sprint]`/`[runtime]` progress lines (redacted via `config.RedactValue`, app :791); exit codes (0 ok, 2 usage, validation/runtime/workspace/partial/cancel classes); side-effect files listed in §4/§7; agent subprocess runs via the OpenCode adapter.

## 4. Authoritative state

- Artifacts under `projects/<p>/sprints/<s>/`: requirements.md, code-context.md, sprint-index.md, technical-handbook.md, `reasoning/*.md`, reasoning.md, plan.md (paths fixed by ArtifactRelPath, artifacts.go:11).
- `flow-state.json` / SQLite mirror row (authoritative stage statuses; see §2).
- `.stage-sessions.json` (schemaVersion 1; strict decode; temp+chmod0600+rename writes **without fsync**; file removed when empty; session_state.go:62-85, :212).
- `.workspace.json` (worktree record: sourceRoot/path/branch/baseline/createdAt).
- `.ultra/cache/sprint-context/**` (derived, disposable).
- `.runtime-metrics.json` (derived telemetry, cap 512).
- `.ultraplan/locks/sprint/<p>--<s>.lock` (O_EXCL lease file with PID liveness reclaim, verification_lock.go:26-60) plus in-process `sync.Map` lease (service.go:89); lease passed to nested calls via context marker (locks.go:112).
- Run-control rows/events (`.ultraplan/run-control.db`) owned by the durable-operation-spine dependency.

## 5. Invariants (as implemented)

- Stage order is fixed and cumulative; flow stops at first failing stage; the failing stage is persisted Failed with redacted, newline/NUL-stripped, 180-char-clamped error (`safeError`, flow.go:415).
- Dry-run performs no writes and acquires no lease (review/smoke dry-run routes through Verify dry-run instead).
- The shared prefix is a pure function of (requirements bytes, code-context bytes, canonical target path); contains exactly one stage boundary; ≤512 KiB; embeds no timestamps/run IDs/output paths; identical across all downstream stages (tests enforce byte equality).
- Resolved source evidence is transient per composition and never persisted into code-context.md (fenced content is a validation finding, code_context.go:69).
- Every source reference resolves or the whole stage fails closed (validateResolvedCodeContext pre-checks resolvability before promotion).
- Code-context artifact replacement is atomic rename with prior-bytes restoration if state persistence fails; runtime failure leaves no artifact.
- Requirements/code-context presence alone does not imply Complete in derived state; code-context completion requires persisted flow-state evidence.
- Checkpoint compatibility is provider+model+workDir equality; prompt checksum is informational; continuation instructs the agent to reread current state.
- Repair is bounded (one product-side continuation; agentwrap spec max 2 attempts with fresh fallback at attempt ≥2).
- Catalog steering: sprint-index selections must exist in project-index sections with exact path matches (validation.go:20-48); reasoning outputs must stay inside `projects/<p>/sprints/<s>/reasoning/` with .md extension (reasoning.go:222).
- Direct-input packets copy governed inputs verbatim, never truncate, and keep canonical dependency order.
- Publication happens after every successful non-execute stage: flow-state.json + stage artifact (+ `.workspace.json` for code-context; all selected reasoning outputs for area-reasoning) via gitpublish; nil publisher = no-op (publication.go:11-55).

## 6. Trust boundaries

- User-authored workspace content (catalog, roadmap, docs, prior reviews, prompt/template overrides, evidence reports, reasoning templates) enters prompts verbatim and steers validators; override read failures degrade to embedded "# Prompt Load Error"/"# Missing Prompt Default" text (prompts.go:241-257).
- Agent-authored artifacts become prerequisites governing later stages only after structural validation; sprint-index selections decide which files are copied into later prompts (validated against the user catalog).
- code-context Path/Lines fields select which live repository bytes enter every downstream prompt; controls enforced: relative clean path, lexical + EvalSymlinks containment, no symlink components, regular file, UTF-8, in-bounds ranges, budget, TOCTOU stability, all failing closed.
- Repository bytes are labelled `UNTRUSTED TRANSIENT SOURCE EVIDENCE`; governance text declares them non-executable and non-exclusive.
- The implementation root originates from user project-index and is handed to `git worktree add`; worktree identity is re-checked via git-common-dir and branch on reuse.
- Runtime/session identifiers (SessionID from adapter events) are persisted and later reused as continuation handles; provider/model/workDir must still match.
- Persisted error/detail strings are redacted and clamped before entering flow-state; prompt-context diagnostics suppress absolute root paths.

## 7. External effects & lifecycle semantics

Effects: create/replace artifacts; write flow-state.json (+SQLite mirror), .stage-sessions.json, .workspace.json, cache packs, metrics, candidate temps (defer-removed); create/delete git worktree + branch beside the target repo; gitpublish commit/push per configured mode; run-control accept/claim/event/finish rows; spawn OpenCode subprocesses with per-stage sandbox/permissions; runtime-store directories under the sprint path cleaned by 72h/2GiB policy.

Cancellation: caller ctx (durable command ctx for CLI flow) propagates into StartRun, per-line source reads, render loops, and the pre-rename check in promotion; canceled/interrupted runs leave the session checkpoint intact for continuation (`LatestOutcome` cancelled/interrupted/cleanup_uncertain recorded, code_context.go:443). `ReconcileInterruptedMutation` (locks.go:25) converts dead-owner residue to explicit interrupted evidence under the same lease, gated by `.cleanup-uncertain.json`.

Retry/restart: per-stage skip-if-valid makes rerun idempotent; interrupted sessions resume via checkpoint with automatic fresh restart when the session vanished; failed runtime stores retained 72h; stale lease reclaimed when holder PID is dead; conflicting live lease → `ErrVerificationConflict` → ExitPartial.

Error taxonomy: typed prompt-context errors; validation findings (Section/EntryName/Path/Problem/Cause/Suggestion) both rendered and fed to repair prompts; classified CLI exits per §2; state-persistence failures during code-context promotion roll back the artifact.

## 8. Immediate surface dependencies

Upstream/consumed: `sprint-flow-state` (flow-state authority + DB mirror semantics), `opencode-agent-runtime` (platform/runtime adapter, RequestFromConfig, ValidationSpec/RepairConfig, runtime stores), `durable-operation-spine` (CLI accept/claim/finish, cancellation ctx), `repo-publication` (gitpublish modes), `product-state-mirror` (productstate Existing/Ensure/Save/Load), `internal/project` (discovery, IsSafeName, ParseProjectIndex, reasoning-default resolution), `internal/workspace` (ResolveInside containment, DefaultOverrideFile). Consumers downstream: execute/review/smoke/QA surfaces (shared prefix, plan/reasoning artifacts), TUI/web via app use cases, `sprint status/metrics`.

## 9. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace projects/ultraplan-go/docs:
- TRD §7.6 (~L410-450): stage order with code-context after requirements; exactly one authoritative code-context.md, no parallel JSON manifest; target resolution through project index + existing worktree mechanisms; repository reads with stage writes limited to code-context.md in the sprint root; validated-prerequisites gating; required content categories; rejection of unsafe paths/placeholders/malformed ranges; atomic replace on rerun; pre-code-context compatibility; per-stage model/variant config; shared-renderer block order and byte-for-byte stability with no dynamic run data; all downstream agent-backed stages receive the prefix; additional live inspection remains allowed; first implementation must not add RAG/embeddings/repository index/provider cache coupling/auto-staleness detector.
- TRD L125: exact requirements + code-context bytes preserved in one shared prefix before stage instructions.
- TRD §18 (~L1640-1700): `internal/sprint` owns planning-stage model, artifact path rules, flow-state.json, prompt rendering, validation, flow execution; dependency direction sprint→project/workspace/platform.
- TRD §18.8 (~L2008-2028): required commands `sprint <p> <s> status|validate|prompt|flow --to <stage>`; flow options list includes `--from`, `--force`, `--no-skip`, dry-run, model/variant and stage-specific overrides, `--json` for review/smoke/verify/status; `issues` invalid as target.
- ARCHITECTURE.md (~L228-263): sprint-owned artifact inventory and ownership bullets including "Stable shared requirements/code-context prompt-prefix rendering".
- In-repo: `internal/sprint/doc.go` package contract (runtime-free status, sprint-owned order/state/validation); help texts (sprint_commands.go:1713+) document flow flags and smoke `--yes` requirement.

## 10. Tests (evidence map)

- prompt_context_test.go: exact-byte preservation (:14); duplicate/overlap canonicalization (:55); fail-closed resolution per error kind (:78); diagnostics without absolute paths (:114); cancellation/encoding/budget identities (:129); TOCTOU replacement detection (:153); exact 512 KiB boundary acceptance/rejection (:186); no recursion/source mutation (:209); boundary/suffix composition (:242); one exact shared prefix across previews (:258) and runtime requests (:304).
- code_context_test.go: content-validation matrix (:51); no-skip without successful outcome (:162,:175); dry-run/execute/rerun preservation (:201); repair within runtime boundary (:246); event-output fallback (:277); resolved repository untouched (:289); mutation-conflict boundary (:325); pre-code-context compat + serialization (:382); runtime failure leaves no artifact (:422); missing output/unsupported permissions/cancellation fail closed (:440); persistence failure restores prior artifact (:496).
- session_state_test.go: continuation from checkpoint (:50); completed stages delete session+checkpoint (:81); prompt-change tolerance (:103); dimension-scoped keys (:121); fresh restart on missing session (:147); interrupted-execute session retention (:172).
- handbook_test.go (:14,:46,:85), reasoning_test.go (:14,:73,:118,:135,:174), plan_test.go (:13,:68,:101,:113), sprint_index_test.go (parse vs catalog :15, runtime-free previews :36, state updates :73, runtime validation wiring :97, skeleton rules :113/:138/:161, code-context scheduled once in cumulative flow :175), sprint_test.go (domain :17, safeError redaction :43, derivation :50, strict state load + atomic write :100, legacy classification :152, status refresh :179), direct_inputs_test.go (order/explanation :10, full-copy of oversized inputs :32, path redaction :50, prior-review ordering :62).

Baseline: full `go test ./...` green at frozen commit (review/baseline/go-test.txt, -race variant present).

## 11. Explicit unknowns / open questions (for later reviewers)

1. `.stage-sessions.json` format and checkpoint semantics have no external contract; TRD/ARCHITECTURE never mention stage-session checkpoints — implementation-defined only (including rename-without-fsync durability).
2. TRD §18.8 lists flow options `--from`, `--force`, `--no-skip`; `parseSprintFlowArgs` (sprint_commands.go:1003) implements none of them. Deferral intent is not recorded in-repo.
3. Cache packs live under `<root>/.ultra/cache/...` while operational state uses `.ultraplan/...`; no documentation found explaining the `.ultra` vs `.ultraplan` split (normalizeWorkspacePath strips prototype-era `.ultra/` prefixes, handbook.go:133).
4. `planningSessionNotFound` detects lost sessions by substring "session not found" across free-form adapter output; the adapter-side error taxonomy contract was not verified in this pack (opencode-agent-runtime surface).
5. `stageSessionCompatible` ignores variant changes (provider+model+workDir only); continuation across variant switches is accepted by design comment (session_state.go:87-92); interaction with per-stage model overrides is otherwise undocumented.
6. Context-pack pruning keeps the 8 newest by mtime; behavior under clock skew or concurrent same-key writers is unspecified.
7. Concurrent flows over different sprints sharing one target repository: worktree parent dirs are shared; collision behavior is an error (execute_target.go:159); supported concurrency level undocumented.
8. `FlowStage` skips the already-valid/publish/clear-checkpoint loop that `Flow` applies per stage; intended equivalence for single-stage durable operations is not documented.
9. PID liveness via `syscall.Kill` (verification_lock.go:95) is POSIX-only; Windows behavior of lease reclaim is not addressed in files reviewed.
10. Area-reasoning entries run strictly sequentially; no documented rationale or ceiling for large template selections (budget impact lands on the 512 KiB prefix only via shared evidence, while direct-input copies are uncapped by design — direct_inputs.go:184-186 states the provider owns context limits).