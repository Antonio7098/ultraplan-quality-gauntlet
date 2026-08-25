# Context Pack: `study-task-execution` — Study single-task execution

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: study-analysis. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

Run exactly one study task through the agent runtime and decide product success:

1. **Analysis** (`RunAnalysis`): one source × one dimension → per-source report at
   `studies/<s>/reports/source/<dim-ref>/<source>.md`. Inapplicable pairs skip without runtime.
2. **Synthesis** (`Synthesize`): all applicable source reports of one dimension → final report at
   `studies/<s>/reports/final/<dim-ref>.md`. Preflight blocks the runtime while any applicable
   source report is missing/invalid.

Both share one execution core (`startRuntime`): fingerprint-gated session continuation with a
one-shot fresh-session fallback, an agentwrap validation spec that performs structural output
validation plus bounded same-session repair inside the run, clean-exit recovery when a validating
report exists despite a runtime error, advisory edit monitoring around the run, and post-success
deletion of the scoped runtime store / session IDs. Runtime success alone is never product
success; conversely a runtime error with a validating artifact is recovered as completed.

## 2. Entrypoints and control flow

### 2.1 CLI `study <s> run <dimension> <source> [--model <provider/model>]` (app/study_commands.go:802-829)
- `parseStudyModelArgs` (:961-988): `--model` / `--model=`; unknown flags rejected; exactly 2 positional args required (else ExitUsage).
- Wraps everything in `beginDurableCLICommand(OperationStudyStart{Stage:"analysis", Task:"<dim>/<src>", Parallelism:1})` (durable_operations.go:49-60): accept+claim+fence in the run-control DB, cancellable ctx, control goroutine heartbeating/polling cancellation; `finishDurableCLICommand` maps the outcome to a terminal state (:71-91).
- Service built by `executionService` (:860-878): effective config → `runtimepkg.RequestFromConfig` (timeout from `Execution.DefaultTimeout`; provider/model from `Models.Primary` else `Models.Default`, split on first slash, runtime.go:537-560) → `studyRuntimeFactory` = `runtimepkg.NewOpenCode` (snapshots disabled; retry policy `DefaultRetries+1`, exponential backoff 1s→30s, rate-limit retries, optional backup-model fallback, opencode.go:31-61) → wrapped in `controlledRuntimeFor` (durable run-control acceptance/claim/fenced event persistence/terminal proposal around every StartRun, run_control.go:140-151,159+) → `study.NewService(root, WithRuntime(controlled, req), WithPublisher(stagePublisher))`.
- Calls `service.RunAnalysis(durable.Context(), …)` with `OnEvent: renderStudyRuntimeProgress` (stderr progress lines, payload keys redacted via `config.RedactValue`, :429-451,510-521). No `ResumeSession`/`OnSession` are passed by the CLI path.
- Result rendering (:880-931) and exit classification (:933-948): completed|skipped→0; runtime_failed→ExitRuntime; cancelled→ExitCancel; validation_failed|preflight_blocked→ExitValidation; ref/config errors→ExitValidation, others→ExitWorkspace (:950-959).

### 2.2 CLI `study <s> synthesize <dimension> [--model ...]` (:831-858)
Same shape; Stage "synthesis"; 1 positional arg; calls `service.Synthesize`.

### 2.3 Internal callers (only other invokers in-tree)
- Run-loop (run_loop.go:316,323): passes `ResumeSession: task.Session` (from run-state.json), `OnSession: checkpointSession` (persists TaskSession into TaskState.Session atomically), `DeferPublication: true`; results folded into task state by `applyExecutionResult` (:779-826).
- Run-all (run_all.go:74,109): no session arguments, no deferral flags.
- No web/TUI caller invokes these two methods directly today.

### 2.4 `Service.RunAnalysis` (run.go:19-106)
ListStudy → ResolveDimension/ResolveSource (typed RefError) → default result Status=completed,
OutputPath=SourceReportPath → applicability gate (`SourceAppliesToDimension`, prompts.go:163-173;
empty `ApplicableDimensions` = applies to all) ⇒ skipped result, exit 0, no runtime (:40-44) →
`BuildAnalysisPrompt` → `beforeFiles = snapshotFiles(study.Path)` → model override resolution
(request > study config > empty, run.go:270-277) → `startRuntime(...)` → edit-warning diffing
(:53-59) → populate RuntimeRunID/RuntimeStatus/CleanupSessionIDs/Agent (:60-63) → error branch or
validation/publication branch (§7). `Synthesize` (synthesize.go:8-103) mirrors this with a
preflight loop over applicable sources' `ValidateSourceReport` (blockers ⇒ preflight_blocked
before prompt build/runtime), workDir = study dir, zero Source, `ValidateFinalReport`.

## 3. Runtime request construction and session continuation (`startRuntime`, run.go:108-181)

- Guards: nil runtime ⇒ error; `MkdirAll(output dir, 0o755)` before the run.
- Request copy from `s.runtimeConfig`: WorkDir set (source dir for directory analysis, study dir otherwise); model override splits on FIRST slash preserving nested ids (`splitModelReference`, :281-287).
- Isolation: `withStudyRuntimeIsolation` sets `Policy.Tools["external_directory"]="deny"` (:259-265); asserted by test.
- Metadata map (:289-308): `task.kind`, `study`, `dimension.number/slug/ref`, `output.path`, `runtime.provider/model`, optional `source.name/kind`, `runtime.permissions`.
- Scoped store: owner string `"study:<name>:<kind>:<dimRef>[:<sourceKind>:<sourceName>]"`;
  `ScopedRuntimeStorePath(study.Path, owner)` = `<study>/.ultraplan/runtime/opencode/<sha256(owner)[:16]>/opencode.db`
  (platform/runtime/store.go:48-51). The path is passed to agentwrap as the OpenCode SQLite DB location (`MetadataDatabasePath`, runtime.go:576-582); `prepareRuntimeStore` writes a `store.json` record (state active + PID), and every terminal path marks it retained (:53-107).
- Validation spec attached (§4).
- **Fingerprint** (lazy, `sync.Once`): sha256 over NUL-joined `[prompt.Text, inputDigest, provider, model, workDir, kind, study.Name, dimRef, source.Name, source.Kind, outputPath]` (:199-204). Input digest (:206-249): directory analysis → rel-path→sha256 map of files under `source.Path` (escapes rejected); synthesis → digests of each `manifest.InputReportPaths` resolved inside workspaceRoot (missing from snapshot ⇒ "input-unavailable" error); markdown analysis → empty entry set (document bytes are embedded in prompt.Text).
- **Continuation gate**: `resume != nil && fingerprint ok && studySessionCompatible(resume, req, fingerprint)` where compatible = non-empty SessionID ∧ `ContinueFailures == 0` ∧ equal provider/model/workdir/fingerprint (:251-253). Continuing sets `SessionID=resume.SessionID`, `SessionAction="continue"`, and prefixes a continuation instruction to the prompt (:255-257). Gate failure is silent — fresh start.
- **Checkpointing**: `req.OnEvent` wraps the caller callback; every event carrying a session id and the post-StartRun `result.SessionID` emit `TaskSession{SessionID, Provider(req.Provider incl. override), Model, WorkDir, InputFingerprint, UpdatedAt}` via `onSession` (:149-166). Fingerprint computation errors silently skip checkpoint emission.
- **One-shot fresh fallback** (:167-179): after a failed continued StartRun, if ctx is still alive and the error category is NOT one of `{cancellation, timeout, permission, authentication, rate_limit, provider_unavailable, runtime_unavailable, model_unavailable, cleanup}` (:183-197), the resume checkpoint is poisoned (`ContinueFailures++`, new UpdatedAt, persisted via onSession — which permanently disqualifies that stored session because compatibility requires ContinueFailures==0), then SessionID cleared, SessionAction="fresh", original prompt restored, and StartRun re-invoked exactly once. The poisoned record carries the resume pointer's SessionID value.

## 4. Output validation spec and repair (`runtime_validation.go`)

`studyReportValidationSpec` builds the agentwrap `ValidationSpec` handed to StartRun:
- Expectation: required file at outputPath ("Create or repair only the expected report.").
- Custom validator `report_schema`: ctx-cancel-aware; runs the UltraPlan validator (per-source or final); failure text lists up to 12 failed checks (`studyValidationFailureText`); repair hint pins repairs to the single report path.
- Repair config: `MaxAttempts: 2`, `SessionAction: continue` (same-session), fresh fallback allowed and on error; prompt = "Repair only `<path>` … Preserve supported evidence… After editing the report, stop. Do not modify sources, workspace configuration, Git state, or unrelated reports." + observed failure lines (:43-52,76-86).
All validation/repair execution happens inside agentwrap's StartRun; this surface observes it afterwards through `result.Repair` metadata.

## 5. Report validators (`validation.go`) — lexical, structural

- Per-source (`validatePerSourceReport`, :33-92): readable; non-empty; case-insensitive substring section checks — top-level `# `, "source info(formation)", "summary", "rating", "question"/"answer"; rating parsed from a dedicated `# Rating`/`# Rating Summary` section when present else whole content (fraction/label patterns, ambiguity ⇒ fail, validation.go:166-211); `citation.shape` regex `\b[\w.\-/]+\.[A-Za-z0-9]+:\d+(-\d+)?\b` required only for directory sources when code citations not disabled (disabled via dimension flag or substring sniff of dimension content, prompts.go:28-30,201-206).
- Final (`validateFinalReport`, :94-129): readable; non-empty; six substring sections — study parameters/context, sources studied table, executive summary, rating summary, pattern/synthesis, open questions/notable absences.
No semantic correctness verification of prose content exists anywhere in this surface.

## 6. Edit monitoring (`edit_warnings.go`) — advisory only

- Before: `snapshotFiles(study.Path)` — WalkDir hashing every file, skipping `.ultraplan` and `.git` dirs (:16-39).
- After: `snapshotFilesSettled` re-snapshots up to 4×250ms until two consecutive snapshots match (~≤1s added latency; returns the last snapshot even if never settled) (:41-62).
- `unexpectedEditWarnings(root, before, after, allowed=[outputPath])`: created/modified/deleted diff minus allowed paths ⇒ warnings `"unexpected edit outside allowed paths: <verb> <rel>"` sorted (:76-116).
- Snapshot errors degrade to `"edit monitoring skipped …"` warnings. Warnings never change status. Prior reports/source/** and reports/final/** files are inside the snapshot scope.

## 7. Outcome assembly (run.go:64-105 / synthesize.go:61-102)

Error branch (`runErr != nil`): record RuntimeError/RuntimeErr/RuntimeCategory/RuntimeDetail (DebugDetail preferred over UserDetail); validate the expected report:
- validation **passed** ⇒ status completed despite the runtime error (clean-exit recovery, e.g. `runtime_exit` after the agent finished writing); delete sessions/store unless deferred; publish unless deferred.
- category == `runtime_exit` with invalid artifact ⇒ `validation_failed` (not runtime_failed).
- otherwise `statusForRuntimeFailure` (:310-315): cancelled iff result.Status=="cancelled" or category cancellation; else runtime_failed.
Success branch: validate; failed ⇒ validation_failed; passed ⇒ cleanup + publish unless deferred.
`publishExecution` (publication.go:10-28): no-op when publisher nil or status ≠ completed; git-publishes the single output path with message `ultraplan: study <name> complete <kind> <subject>`.

## 8. Inputs / outputs

Inputs: study tree (dimensions/, sources/ with frontmatter `applicable_dimensions`, config, prior reports), workspace prompts/base.md, prompts/synthesize.md, templates/repo-analysis.md, templates/report.md (workspace override else built-in defaults, prompts.go:175-191), markdown source document embedded verbatim for document analysis, effective runtime config (provider/model/timeout/permissions/policy), optional `--model`, optional ResumeSession checkpoint (internal callers only), wall clock.
Outputs: report markdown under studies/<s>/reports/{source,final}/ (written by the LLM, validated structurally), scoped runtime store dir + store.json state transitions, run-control operation records/events (via controlled runtime), git publication of the report, stdout/stderr rendering, ExecutionResult{Status, Runtime*, Agent metadata, Warnings, Validation, PreflightResults, Blockers, CleanupSessionIDs, Publications}, TaskSession checkpoints (internal callers).

## 9. Authoritative state and ownership boundaries

- Product-owned artifacts: the two report trees (LLM-authored content, structurally validated).
- Runtime-owned: scoped OpenCode SQLite stores under `<study>/.ultraplan/runtime/opencode/<hash>/` with product-written `store.json` lifecycle records (active/retained/cleanup_pending + PID; deletion path validated to sit under `.ultraplan/runtime/opencode/` with base name `opencode.db`, store.go:109-131).
- Run-state.json `TaskState.Session` (TaskSession: SessionID/Provider/Model/WorkDir/InputFingerprint/UpdatedAt/ContinueFailures) — written by the run-loop surface through OnSession, fed back as ResumeSession; owned by the runloop-scheduler surface, consumed here as an opaque capability handle.
- Durable operation/run records live in the run-control repository (separate surface).
- The plain CLI path persists no study-owned durable state beyond the report itself.

## 10. Invariants (as implemented)

1. Success requires the structural validator to pass regardless of runtime outcome; a runtime error with a validating artifact is recovered to completed (clean-exit recovery).
2. Session reuse requires exact fingerprint equality plus ContinueFailures==0 and identical provider/model/workdir; any mismatch silently starts fresh.
3. A failed continuation gets exactly one fresh fallback, never more; the poisoned checkpoint is persisted before the fallback so the old session can never be resumed again.
4. Fallback is suppressed for cancellation/timeout/permission/authentication/rate_limit/provider_unavailable/runtime_unavailable/model_unavailable/cleanup categories and whenever ctx is already dead.
5. Study runs deny the `external_directory` tool unconditionally.
6. Only the declared output path is exempt from edit-warning diffs; warnings are advisory and never alter status.
7. Post-success cleanup prefers deleting the entire scoped runtime store, falling back to per-session deletion (event-observed ∪ result session IDs); cleanup failures become warnings, not failures.
8. Publication happens only for completed results and only when a publisher is configured.
9. Synthesis never reaches prompt-build/runtime while any applicable source report fails validation (preflight_blocked).
10. Inapplicable source/dimension pairs short-circuit before prompt building and runtime invocation.
11. Runtime store mutation/deletion refuses paths outside managed `.ultraplan/runtime/opencode/.../opencode.db` shape.
12. Diagnostics persisted into durable state are compacted to 4096 bytes per field (durable_metadata.go:84-88) and redacted at CLI render time.

## 11. Trust boundaries

- **LLM markdown becomes the persisted product artifact AND the success signal**: completion is classified by lexical validators (substring sections, rating parse, citation-shape regex) after bounded agentwrap repair (2 same-session attempts + fresh fallback). No semantic verification of analysis claims, citations' targets, or ratings exists in this surface.
- **Opaque agent-issued SessionIDs are resumable capability handles**: session ids observed in runtime events/result are persisted (run-loop) and later replayed verbatim as `req.SessionID` to the same adapter. Controls binding reuse: full input/prompt/model/workdir/task fingerprint, ContinueFailures poisoning, one-shot fresh fallback. The id is never interpreted locally.
- **Agent filesystem effects**: the runtime works in the source directory (directory analysis) or study directory (markdown analysis, synthesis); post-hoc hash-diff monitoring detects out-of-allowlist edits but only warns. `external_directory` tool denial is the preventive control; enforcement strength depends on adapter permission support (`UnsupportedCount` surfaced in metadata).
- **Workspace-authored prompt inputs** (prompts/templates/dimension files) are embedded verbatim into prompts — trusted-by-position user content; the analyzed source document is likewise embedded, not executed.
- **Diagnostics**: runtime error strings flow into stderr (redacted via `config.RedactValue`) and into durable state (compacted, DebugDetail preferred).

## 12. Cancellation / retry / restart / error semantics

- Cancellation: durable-operation ctx propagates into Adapter.StartRun, which cancels the agentwrap run, waits ≤5s for the waiter, then classifies cancellation (runtime.go:358-381); `statusForRuntimeFailure` ⇒ cancelled ⇒ ExitCancel. Fallback suppressed once ctx.Err() != nil.
- Timeout: config-global (`Execution.DefaultTimeout`); deadline ⇒ timeout category; excluded from continuation fallback.
- In-run retries/repair are agentwrap policy territory: attempt retries with backoff/rate-limit handling and backup-model fallback at factory level, plus the ≤2-attempt validation repair loop. This surface sees summaries (`result.Attempts/Policy/Repair`).
- Cross-process resume exists only through internal callers (run-loop passing TaskSession checkpoints); plain CLI always starts fresh and deletes sessions on success.
- Restart/crash mid-task leaves the retained runtime store (state active with stale PID → converted to retained by later store GC) and whatever partial report bytes exist; recovery/reconciliation belongs to the runloop-scheduler surface.
- Error envelope: ExecutionResult preserves cause chain (RuntimeErr), category, redacted detail; CLI maps statuses to exit classes; typed RefError/config sentinels map to ExitValidation.

## 13. Immediate surface dependencies

- **opencode-agent-runtime** (platform/runtime + agentwrap v0.0.0-20260825130518): StartRun execution, validation-spec evaluation and repair, event mapping/coalescing, error taxonomy/categories, session & store deletion (`DeleteSession(s)`, `DeleteRuntimeStore`, wired at opencode.go:83,93), scoped store lifecycle.
- **product-state-mirror**: gitpublish publication of completed reports; run-state.json TaskSession checkpoints (provided/consumed with runloop-scheduler surface).
- **run-control / durable operations**: operation acceptance, fenced events, cancellation polling, terminal outcomes wrapping every CLI invocation; controlledRuntime wraps each StartRun.
- **config/workspace foundation**: effective config, model reference parsing, redaction, `workspace.ResolveInside` containment for synthesis digest inputs and template overrides.
- In-package: discovery/resolution, applicability, prompt builders, report validators, rating parser.

## 14. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace `projects/ultraplan-go/sprints/10-single-analysis-synthesis/requirements.md` (governing `study run`/`study synthesize`):
- Thin CLI handlers (parse/delegate/render/map); study logic stays in internal/study; platform/runtime stays generic (no study semantics).
- Deterministic prompt builders reused, not duplicated; directory analysis uses the source dir as workdir with isolation + file-line citation rules; markdown analysis embeds stripped document and forbids external exploration.
- Inapplicable Markdown pair ⇒ successful exit with clear skip message, no runtime invocation.
- "Runtime success alone is insufficient": expected report must exist, be non-empty, pass the study validator, for both kinds; validation failures return validation exits with check names + path; runtime failures preserve the cause chain.
- Synthesis excludes inapplicable pairs and fails before runtime when any applicable source report is missing/invalid, naming blockers.
- Metadata contents enumerated (task kind, study, dimension number/slug, source name/kind, output path, runtime/provider/model).
From `docs/TRD.md`: L14/L114 — must use agentwrap + opencode adapter and its validation wrapping/retry/policy machinery rather than reimplementing runtime behavior. From sprint 12 requirements: completed outputs revalidated on resume (context for how results are consumed); `applyExecutionResult` maps completed/skipped⇒completed(+Session=nil), validation-failed⇒failed, and retries only rate_limit/timeout/provider_unavailable/runtime_unavailable categories (run_loop.go:841-848).
ARCHITECTURE.md L81/L98: study-owned behavior placement.

## 15. Tests (evidence map)

internal/study/run_test.go (fake runtimes):
- TestRunAnalysisContinuesCompatibleInterruptedSession — checkpoint emitted with fingerprint; resume issues `continue` action + prefixed prompt.
- TestRunAnalysisStartsFreshWhenStudyInputChanged — changed dimension file breaks fingerprint ⇒ fresh request.
- TestRunAnalysisFallsBackOnceWhenContinuationFails — runtime_exit on continue ⇒ poisoned checkpoint + exactly one fresh follow-up that completes.
- TestRunAnalysisSuccessMapsRuntimeRequestValidatesAndDeletesSession — workdir, external_directory deny, provider/model/timeout mapping, metadata keys, expectation path, prompt content, post-success DeleteSession call.
- TestRunAnalysisRuntimeFailureAndValidationFailures — runtime_failed w/ category + cause; missing output and invalid report ⇒ validation_failed with named checks.
- TestRunAnalysisRecoversCleanRuntimeExitWhenReportValidates / TestSynthesizeRecoversCleanRuntimeExitWhenFinalReportValidates — runtime_exit + valid artifact ⇒ completed.
- TestRunAnalysisWarnsWhenRuntimeEditsSourceFiles — modified source file yields exactly one warning mentioning the modified path.
- TestRunAnalysisSkipsInapplicableMarkdownWithoutRuntime — zero runtime calls.
- TestSynthesizeSuccessPreflightBlockAndFinalValidation / TestSynthesizePreservesRuntimeFailureCause.
- TestRunAnalysisAppliesModelOverridePrecedence / TestResolveStudyModelOverrideAndSplit — precedence and first-slash split semantics.
internal/study/runtime_validation_test.go — repair spec shape (MaxAttempts 2, continue, no request override, fresh-fallback flags) and repair-prompt contents.
internal/study/runtime_metadata_test.go — observability projection incl. omissions; executionTaskError attempt-detail preservation.
Indirect: run_loop_test.go exercises ResumeSession/OnSession wiring end-to-end through RunLoop; app-level study_run_commands_test.go covers command parsing/exits with injected fake runtime factory.
Gap note (descriptive): `snapshotFiles`/`unexpectedEditWarnings` have no dedicated unit tests; coverage is via the single edit-warning integration test above (run_loop_diagnostics_test.go reuses snapshotFiles for unrelated diagnostics).
Baseline: go test ./..., -race, vet, -cover green at frozen commit (review/baseline).

## 16. Explicit unknowns / open questions (for later reviewers)

1. `agentMetadata(runtimeResult, s.runtimeConfig)` receives the base config, not the override-applied request copy built inside startRuntime — Agent.Provider/Model/Runtime fields reflect base configuration even under `--model` overrides; per-attempt provider/model remain accurate via result.Attempts (run.go:63 vs :115-119).
2. Edit monitoring settles within ~1s; later-arriving writes are unobserved. Monitoring also runs (and adds settle latency) on failed/cancelled runs since the diff block executes unconditionally after startRuntime (run.go:50-59).
3. Markdown-analysis fingerprints cover document content only via prompt.Text (empty mutable-entry set, run.go:212-215); directory-analysis digests cover only files under source.Path — template/prompt/dimension changes surface solely through prompt.Text.
4. Checkpoint emission silently drops fingerprint errors; synthesis continuation degrades to fresh whenever any input report is absent from the snapshot (run.go:232-237).
5. `recoverableRuntimeOutputFailure` routes runtime_exit + invalid artifact to validation_failed rather than runtime_failed, which interacts with the run-loop's retry-category list (executionShouldRetry does not include it).
6. CleanupSessionIDs is populated even when store-level deletion was used, deferred, or failed; nothing in-tree consumes the field today.
7. `containsSection` matches case-insensitive substrings anywhere in the document (a prose mention of "rating" satisfies section.rating), and the citation-shape regex scans unrestricted text — validator strength is intentionally lexical.
8. Timeout is config-global; ExecutionRequest/SynthesisRequest expose no per-task timeout knob.
9. Two first-slash model-reference splitters exist (study.splitModelReference, platform/runtime.splitModel) with identical semantics.
10. The one-shot fallback poisons the resume pointer's SessionID even if events during the failed continue attempt checkpointed a different/newer session id.
11. MkdirAll creates the report's parent directory before the run; a failed run can leave an empty created directory (visible to later snapshot diffs as benign).
12. Plain CLI invocations cannot opt into session resume (no flag surfaces ResumeSession); resumability is reachable only through run-loop/run-all orchestration layers.

— End of context pack. Descriptive only; no defect claims made or implied.
