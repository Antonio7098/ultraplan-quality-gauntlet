# Context Pack: `sprint-verify-transition` — Verify review-to-smoke transition

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: governed-sprint-delivery. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

`Service.Verify` is the single promotion boundary between execute evidence and quality assessment. It composes four policies and delegates their effects:

1. **Execute completeness** (`requireCompleteExecute`): every execute task terminal (complete/deferred) plus a non-blank `execute.md`, before any verification work.
2. **Review freshness/currentness**: recompute review currentness from current bytes/governed inputs; reuse canonical review evidence only when fresh+completed; otherwise run the full conformance review.
3. **Diagnostic override ladder**: after a completed-but-failing review, continuation into smoke requires the explicit flag trio (`--force-review` + confirmation + non-empty rationale); stale/blocked reviews are never overridable.
4. **Smoke continuation** through the smoke gate (`RunSmoke`), which independently re-validates the review gate at decision time.

The stated enforcement goal: diagnostic results can never become canonical assessment — implemented by `deriveAssessment` ignoring overrides entirely and by diagnostic-only smoke never committing artifacts/state.

## 2. Entrypoints and control flow

### 2.1 CLI `sprint <p> <s> verify [...]` (`internal/app/sprint_commands.go:280-328`)
- `parseSprintVerifyArgs` (:1119-1191): default `To=smoke`; `--to` restricted to review|smoke (:1172); `--focus-review` accumulates; `--restart-review` conflicts with focus (:1175); at most one of `--level/--suite/--test` (:1178-1186); `--timeout` parsed, positive, ≤24h; `--force-review` requires `--override-reason` (:1187); `--yes/--non-interactive` sets both `Smoke.NonInteractive` and `Smoke.OverrideConfirmed`; `--dry-run` propagates into Review and Smoke sub-requests.
- Pre-flight CLI gate (:285-287): `To==smoke && !DryRun && !Smoke.NonInteractive` → ExitUsage "— --yes is required for smoke execution".
- Non-dry-run wraps the command in a durable run-control operation (`beginDurableCLICommand`, OperationVerifyStart; durable_operations.go:49-60 — repository Accept → Claim with owner lease + fencing generation → lifecycle running event → cancellation-aware ctx watched by `controlOperation` heartbeat/cancel polling :223-264), rebuilds a runtime-backed service via `sprintRuntimeService` (:685-711), and wires progress rendering.
- Calls `verifyService.Verify` (:303); error mapping distinguishes `AsSmokeError` → `mapSmokeError` else `mapSprintError("sprint.verify", …)` (:305-312). JSON envelope `{schema_version:1, operation:"sprint.verify", status:<assessment>, result, error?}` (:313-318); text output via `renderSprintVerification` (:1282-1302). Exit classes (app.go:16-24): usage=2, validation=5, runtime=6, partial=8; success-path fail/blocked assessments exit 5 (:325-327).

### 2.2 `Service.Verify` (`internal/sprint/verify.go:39-95`)
- Lease once up front for non-dry-run via `acquireMutationContext` (:40-47); nested `Review`/`RunSmoke` reuse it through the context marker (locks.go).
- Default `To=smoke`; reject anything but review|smoke (:48-53).
- `requireCompleteExecute` (:55; impl :97-132): `LoadExecuteRunState`; on load failure falls back to reading raw `.run-state.json` as `{status:"complete"|"completed", files:[…]}` plus readable non-blank `execute.md` (legacy summary shape :104-116). Loaded state must have ≥1 task, all tasks `complete` or `deferred`, and non-blank `execute.md`.
- `VerificationStatus` (:58-64; impl :142-250). `ErrFlowStateMissing` tolerated here (result.Verification stays zero-valued). Read-only in-memory `reconcileExpiredAttempts` (:154-156; expiry rule `attemptExpired` :467-479 — running attempts expire immediately when OwnerPID is dead, else when now−max(started,heartbeat) > 2h).
  - Review stage (:158-212): status/verdict/digests from persisted `ReviewStageState`; FreshnessReasons from `PrepareReview` findings ("governed review inputs…"), fingerprint mismatch (only under `strictCompletedReviewSnapshotFreshness`, currently false), missing `review.md`, digest mismatch vs recorded `ArtifactDigest`, and `ValidateReviewContent` against the recorded fingerprint (validation manifest pinned to recorded fingerprint while the strict switch is off, :191-196). `Fresh = no reasons ∧ LastComplete != nil`. Resume counters/resumability computed from `Resume.Coverage`; resumability invalidated by prepare failures or manifest fingerprint/model drift.
  - Smoke stage (:213-247): inherits "review evidence is not current" when review not fresh; checks `smoke.md` existence, digest equality vs `ArtifactDigest`, `ValidateSmokeContent`, and input-fingerprint presence; snapshot refresh of external evidence only under `strictCompletedSmokeSnapshotFreshness` (off).
  - `deriveAssessment` (:252-285) precedence: malformed ⇒ blocked; fresh review fail ⇒ fail ("diagnostic smoke cannot change this result"); fresh review blocked ⇒ blocked; incomplete chain ⇒ incomplete naming earliest non-current stage (review before smoke); then smoke verdict mapping fail/blocked/not_applicable/pass_with_findings/pass; review pass_with_findings downgrades a clean pass; unsupported smoke verdict ⇒ blocked.
- Currentness decision (:65): `statusErr == nil ∧ status.Review.Fresh ∧ ExecutionStatus=="completed" ∧ !req.Review.Restart`.
- Not current ⇒ runs `s.Review` (:66-77) with the request's review sub-request (dry-run forced to match). Continuation exception `allowDiagnosticContinuation = To==smoke ∧ review.Status==completed ∧ Verdict==fail ∧ Smoke.ForceReview ∧ Smoke.OverrideConfirmed ∧ rationale non-empty` (:74); any other review error aborts. (Review returns error `"review completed with failing verdict"` exactly for completed-fail, review.go:640-642; blocked/prerequisite/restart-focus errors do not qualify.)
- `To==review || DryRun` ⇒ refresh VerificationStatus (non-dry-run only; its error is discarded, :83) and return.
- Else smoke continuation (:87-94): `RunSmoke(req.Smoke)`; final VerificationStatus refresh (error discarded, :93); returns the smoke error.

### 2.3 Flow delegation (`internal/sprint/flow.go`)
- Dry-run `flow --to review|smoke` delegates to `Verify{DryRun:true}` with message from the review prompt preview (:97-105).
- Non-dry-run `flow --to review|smoke` materializes planning stages 1..execute (`flowStages` end=8 for review/smoke targets, :278-280), then calls `Verify{To:req.To}` carrying Review/Smoke sub-requests (:178-182); result message `verification assessment=%s next=%s`.

### 2.4 Web/TUI
- Operation kinds `verify-start`/`verify-dry-run` (operations.go:92-93); dry-run executes read-only via `ss.Verify(..., DryRun:true)` (operations.go:507-512); start goes through the durable operation runner with progress events (operation_runner.go:97-113), constructing the same VerifyRequest including `OverrideConfirmed: req.ForceReview`. Web start requires the two-phase prepare→confirmation-token handshake (web/security.go preparation store; operation_handlers.go prepare/start handlers).

### 2.5 `internal/sprint/verification_phase.go`
Small adapter layer: `VerificationPhase` enum {conformance-review, qa, repair}; `ParseVerificationPhase` accepts "review" as an alias for conformance-review; `CompatibilityStage` maps only conformance-review → StageReview so QA/repair can never enter planning order.

### 2.6 `internal/sprint/freshness_policy.go`
Three compile-time switches (`strictCompletedReviewSnapshotFreshness`, `strictCompletedSmokeSnapshotFreshness`, `strictSmokeAuthorProtectedSnapshots`) all currently `false`, with an in-file rationale: snapshot-based invalidation was too brittle (unrelated/concurrent/tiny edits); artifact existence, format, recorded-digest checks, and authoring allowlists remain enforced. This is the documented meaning of "fresh" today.

## 3. Inputs / outputs

Inputs: project/sprint refs; workspace tree (`.run-state.json`, `execute.md`, `review.md`, `smoke.md`, `flow-state.json`, `project-index.md` harness catalog, governed protocol docs consumed by PrepareReview); wall clock `s.now()`; `os.Getpid()`; request flags (To, DryRun, Restart, Focus, level/suite/test selection, timeout, ForceReview, OverrideConfirmed, OverrideRationale, NonInteractive).
Outputs: `VerifyResult{project, sprint, to, dry_run, review_result?, smoke_result?, verification}` where VerificationStatus carries per-stage execution/verdict/fresh/freshness-reasons/digests/fingerprints/attempts/override plus overall assessment+next action (domain.go:187-228). Side effects are entirely delegated: review writes (atomic `review.md` replace, flow-state Review record incl. attempt lifecycle/resume/heartbeats, publications), smoke writes (harness authoring inside declared paths, discovery+run subprocesses, atomic `smoke.md` replace, flow-state Smoke record + LastComplete + DiagnosticOverride, roadmap delivery mark, smoke-stage publication). CLI adds structured JSON/text rendering, exit codes, and durable run-control records/events.

## 4. Authoritative state

- Canonical gate state: `projects/<p>/sprints/<s>/flow-state.json` `Review`/`Smoke` blocks — Status, Verdict, Fingerprint (governed-input), ArtifactDigest (content sha256), InputFingerprint, LastComplete completions, ActiveAttempt/LastAttempt, Stale, SmokeFingerprint, Override block (Requested/Confirmed/Rationale/RequestedAt/ReviewFingerprint/ReviewVerdict, domain.go:178-185). Ownership and write grammar belong to `sprint-flow-state`; this surface is the policy reader that decides which writes happen (via delegated Review/RunSmoke).
- Execute authority checked pre-transition: `.run-state.json` (+ legacy raw-summary fallback) and `execute.md` — owned by the execute surface.
- Run-control DB (CLI/web non-dry-run): durable operation acceptance/claim/fence/events/terminal outcome for the verify invocation itself.
- No third verification artifact exists or is created by this surface (contract requirement).

## 5. Invariants (as implemented)

- Execute completeness precedes everything; incomplete evidence short-circuits before any review/smoke work.
- Transition target ∈ {review, smoke}, default smoke; validated at parse and service layers.
- Canonical review evidence reused only when Fresh∧completed∧not-restarted; otherwise review reruns (with resume/focus semantics owned by the review surface).
- Both ends re-verify at decision time: Verify recomputes freshness from current bytes; the smoke gate independently reloads flow state, recomputes staleness, compares digests, validates content, then applies the verdict ladder (smoke_protocol.go:177-209).
- Override ladder: stale reviews cannot be overridden at all (:195-196); fail/blocked verdicts require ForceReview ∧ OverrideConfirmed ∧ non-empty rationale at BOTH Verify's continuation check and the smoke gate; CLI additionally requires `--force-review`⇒`--override-reason` at parse time.
- Diagnostics cannot become canonical: force-review marks the smoke result DiagnosticOnly (:128); DiagnosticOnly results return before `commitSmoke` (:180-182) so no smoke.md/state/roadmap/publication occurs; narrow selections are diagnostic (test always, smoke_protocol.go:520; suite/level unless discovery proves complete coverage, :496/:508); `deriveAssessment` never reads Override (pinned by TestDiagnosticOverrideCannotPromoteCanonicalSmoke).
- Overall assessment is a pure deterministic function of the two stage states + malformed flag; malformed canonical evidence forces blocked regardless of verdicts.
- Expired running attempts are reconciled read-only inside VerificationStatus; persisted transitions are owned by explicit operations (pinned by test).
- Single mutation lease spans the whole transition (review + smoke share it via context marker); concurrent mutators get typed `ErrVerificationConflict`.

## 6. Trust boundaries

- flow-state.json Review/Smoke blocks are trusted gate input for promotion decisions; controls are load-time strict validation (flow-state surface) plus decision-time recomputation here (digest equality, content validators, governed-input manifests). Anyone able to rewrite flow state or review.md/smoke.md can alter gate inputs; digest/content binding narrows what passes.
- Override flags originate from CLI args / web request fields; they authorize running smoke despite a failing review and persist an attributable audit record (rationale + review fingerprint/verdict + timestamp into smoke.md and the flow-state Override block). Confirmation is enforced per-surface: CLI `--yes`, web two-phase confirmation token.
- Harness identity/executable/CWD/evidence paths come from `project-index.md` + manifest and are containment-checked (canonicalDirectory* ) by the smoke surface before any process runs; Verify itself launches no processes.
- Fingerprinting helpers hash files and (under disabled switches / review snapshot path) walk the target repo (`targetIdentity` bounded at 64 MiB/file, symlink-escape rejected; `targetRevisionIdentity` used by review.go:288 deliberately excludes unrelated dirty files).
- Error/diagnostic text crossing into durable state is sanitized downstream (`safeError` redaction/truncation) per the flow-state pack.

## 7. External effects & lifecycle semantics

- Effects: reviewer runtime sessions (delegated Review), external harness subprocesses with bounded timeouts (delegated smoke), atomic artifact replaces, flow-state saves, git publications for review/smoke stages, roadmap.md delivery mark on canonical pass only (smoke.go:39-48), post-review session deletion (review.go:643-645), run-control operation records/events.
- Cancellation: non-dry-run CLI/web run under the durable operation context; `controlOperation` acknowledges requested cancellations and heartbeats ownership; review persists explicit cancelled state via `persistReviewFailure`; smoke classifies timeout/cancelled/cleanup-incomplete harness outcomes into typed smokeErrors and terminal attempt statuses (`AttemptCancelled/TimedOut/Blocked/Failed`, smoke.go:234-250).
- Interruption residue: running attempts left behind are expired by dead-PID-or-2h rule in read-side reconcile; the persisted transition belongs to ReconcileInterruptedMutation / the next writer (flow-state surface).
- Retry/restart: rerunning verify after a failed review reruns review (resume coverage retained unless `--restart-review`; restart also bypasses currentness reuse at :65); smoke rerun reconciles its own attempt records and re-gates.
- Error propagation: first blocking error wins; VerificationStatus refresh errors are intentionally discarded on the two return legs (:83, :93); ErrFlowStateMissing tolerated at entry; CLI always emits a single JSON document on failure with a structured `error` field (pinned by TestSprintFailureJSONIsOneStructuredDocument) and exits 5 on fail/blocked assessment even without a run error.
- Long-held lease: the single lease covers potentially hours of reviewer/harness runtime; cross-process contention waits or fails fast with conflict diagnostics naming the owner PID/AcquiredAt.

## 8. Immediate surface dependencies

- `sprint-conformance-review`: `PrepareReview` (manifests/findings/fingerprints feeding freshness), `Service.Review` (execution + saveReviewState completion records), resume/focus plan.
- `sprint-smoke-gate`: `RunSmoke`/`runSmoke`, `prepareSmokeStatic` review gate, `selectSmoke` diagnostic classification, `commitSmoke` canonical-vs-diagnostic commit boundary, publication rules.
- `sprint-flow-state`: LoadFlowState/SaveFlowState grammar, attempt validators, reconcile helpers (`attemptExpired`/`reconcileExpiredAttempts` physically live in verify.go:467-510 but are documented as flow-state machinery shared with locks.go).
- Execute surface: `.run-state.json`/`execute.md` authority behind requireCompleteExecute; `ExecuteComplete` also serves flow's execute-validity check (flow.go:308).
- Run-control/durable operations: CLI/web acceptance boundary, fencing, events, cancellation.
- Publication seam (`publishReviewStage`/`publishSmokeStage`), roadmap project domain (`MarkRoadmapSprintDelivered`).

## 9. Contracts (CURRENT-CONTRACT evidence)

- PRD.md:585-586: "`ultraplan sprint <project> <sprint> verify [--to review|smoke]` — Convenience orchestration over the same review and smoke use cases; it does not introduce a third verification artifact."
- TRD.md:2016 command list; :2020-2030 flow options include "explicit smoke level/suite/test selection, smoke timeout, and review-failure diagnostic override" and "stable --json output for review, smoke, verify, and status"; Phase 3 adds review then smoke as valid `--to` stages.
- roadmap.md:1002: verify as convenience command over the same use cases; :1010 acceptance: "`flow --to smoke` and `verify` always apply review before smoke unless the user explicitly selects a diagnostic override"; :1011 stale-marking on governed-input/implementation change; :1012 narrow-test pass must not hide containing-suite failure; :1014 "overall assessment is deterministic and cannot contradict the stage verdicts."
- In-repo help text (sprint_commands.go:1647-1655, 1723-1728): gate order, override semantics ("requires --yes and a rationale, remains diagnostic, and cannot improve the overall assessment"), and flow/verify parity are user-facing contract statements pinned by TestVerifyHelpExplainsGateAndRecovery.
- Workspace skills (skills.go:187/226/371-386, mirrored in workspace skill docs): instruct invoking agents NOT to use verify/flow/smoke commands to perform stage work — usage-policy context, not code behavior.
- HISTORY: freshness_policy.go comment documents why snapshot strictness is off (brittleness attribution) — deliberate temporary posture.

## 10. Tests (evidence map)

Package-internal (internal/sprint/verify_test.go):
- TestDeriveAssessmentPrecedence (:30-60): 12-case table pinning the full assessment precedence incl. override-carrying pass still reduced by fresh review fail? (override case asserts fail dominates).
- TestDiagnosticOverrideCannotPromoteCanonicalSmoke (:209-222): deriveAssessment ignores a confirmed override.
- TestVerificationStatusDerivesExpiredReviewAttemptWithoutWriting (:123-153): 25h-old running attempt reported failed/timed_out in-memory; durable bytes untouched.
- TestVerificationStatusImmediatelyRecoversDeadAttemptOwner (:155-178): dead OwnerPID expires instantly.
- TestReviewFreshnessArtifactEditAndFocusedMerge (:180-207): appended external edit flips Fresh=false and assessment incomplete.
- TestVerificationMutationConflictIsTyped (:224-241): concurrent review ⇒ ErrVerificationConflict.
- Migration/pre-code-context tests (:62-121) cover flow-state loading strata this surface reads.
Related: smoke_test.go selection diagnostic pins (:70, :200-208 — test always diagnostic; suite/level diagnostic unless complete); reviewFixture (review_test.go:678-697) builds complete-execute fixtures including the legacy `.run-state.json` summary shape.
App-level: sprint_verify_commands_test.go (:11-37 parse/validation matrix incl. flow parity, :39-46 help contract); sprint_commands_test.go:120-147 verify --json failure emits one structured doc with `error` present.
Baseline: full `go test ./...` green at frozen commit (review/baseline).
Coverage shape (factual): no test calls `Service.Verify` directly, and no test drives non-dry-run `Flow --to review|smoke`; the composition decisions inside Verify (:65,:74,:81) are exercised only indirectly via deriveAssessment/VerificationStatus unit tables, smoke-gate tests, and CLI parse/help/envelope tests.

## 11. Explicit unknowns / open questions (for later reviewers)

1. Ladder asymmetry: Verify's diagnostic continuation admits only verdict==fail (:74), while the smoke gate admits fail OR blocked verdicts behind the same flag trio (smoke_protocol.go:198-206). Whether blocked-verdict continuation is reachable through Verify depends on blocked reviews always returning non-continuing errors — inferred from review.go paths, not pinned by a test.
2. Service.Verify has no direct test coverage of its own control flow (currentReview reuse branch, allowDiagnosticContinuation branch, To==review early return, discarded VerificationStatus refresh errors at :83/:93).
3. `Smoke.NonInteractive` is set by CLI parsing but appears unconsumed inside internal/sprint (only defined, smoke_types.go:65); library/web callers construct VerifyRequest directly (operation_runner.go:106) without setting it — the effective confirmation enforcement points are the CLI gate (:285-287), parse-time flag coupling, and the web confirmation-token handshake.
4. ErrFlowStateMissing tolerated at Verify entry yields a zero-valued Verification in results; interplay with saveReviewState/saveSmokeAttempt synthesizing fresh flow state mid-transition is undescribed.
5. The lease held across the entire review+smoke runtime has no liveness metadata updates during the run (lock file stamped once at acquisition); contention behavior over multi-hour transitions is untested.
6. 2h attempt-expiry constant and immediate dead-PID expiry trust `kill(pid,0)`/EPERM-alive (unix-only, no build tags) and are subject to PID reuse; constants have no configuration.
7. `targetIdentity`'s 64 MiB/file bound and git-based enumeration run only under disabled strict switches from this surface's paths; reachability today is limited to smoke InputFingerprint file hashing and review snapshot identity — exact live call graph under current switches is worth confirming.
8. deriveAssessment treats a fresh completed review with empty/unset verdict as neither fail nor blocked nor pass (falls to the unsupported-smoke-verdict default only when smoke is fresh; otherwise incomplete) — behavior with hand-edited flow states is validator-dependent.
9. Exit-code contract: success-path fail/blocked assessments exit 5 even when no error occurred (:325-327); whether automation treats assessment-fail-without-error as distinct from validation failure is undocumented.
10. Flow delegation means `flow --to smoke` without `--yes` fails only at the CLI gate (:234) — parity holds at CLI level; service-level Flow callers have no equivalent NonInteractive gate (mirrors unknown #3).

— End of context pack. Descriptive only; no defect claims made or implied.
