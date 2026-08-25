# Context Pack: `sprint-smoke-gate` — Smoke harness gate

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: governed-sprint-delivery. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

Deep verification of an executed sprint against an **external, cataloged protocol-v1 smoke harness**. The product does not own smoke tests; it owns the *gate* around them:

1. **Static readiness** (`prepareSmokeStatic`): exactly one `Smoke Harnesses` catalog row in `project-index.md`, a valid target implementation directory, a manifest-contained executable/cwd/evidence roots, and a current, non-stale, acceptable Conformance Review (fail/blocked reviews admit only a confirmed diagnostic override).
2. **Agent-driven authoring** inside the harness: one restricted-permission agent call may modify only manifest-declared authoring paths; the whole harness tree is hashed before/after and any out-of-allowlist change fails the run.
3. **Machine-readable discovery** executed as a direct bounded child process; its JSON is validated for identity, referential closure, and coverage assignment.
4. **Scope selection**: narrowest sufficient scope (explicit level/suite/test override or the authored complete sprint mapping); narrower-than-containing scopes and forced-review runs are marked `DiagnosticOnly` and never commit evidence.
5. **Direct argv execution** of the harness `run` command with env allowlist, timeouts, output caps, and process-group cleanup — the only non-agentwrap subprocess lane in this surface.
6. **Evidence validation**: run identity, scope echo, count arithmetic, exact executed-test-set equality vs discovery, evidence/issue path containment plus optional hashes, failed-test⇒open-issue completeness.
7. **Atomic publication**: `ValidateSmokeContent`-clean `smoke.md` written atomically, then full `SmokeStageState` (+ `LastComplete`) committed to flow-state, then optional roadmap delivery mark, then git publication — strictly in that order.

## 2. Entrypoints and control flow

### 2.1 CLI `sprint <p> <s> smoke` (app/sprint_commands.go:513-562)
- `parseSprintSmokeArgs` (:825-877): `--level|--suite|--test` mutually exclusive; `--timeout` positive ≤24h; `--force-review` requires `--override-reason`; `--yes/--non-interactive` sets `NonInteractive`+`OverrideConfirmed`; `--dry-run/--preview`; `--json`.
- Non-dry-run requires `--yes` (else ExitUsage). Non-dry-run wraps `beginDurableCLICommand(OperationSmokeStart)` (accept-before-execute, cancellable context) and rebuilds the service via `sprintRuntimeService` (:695-711) which adds `WithRuntime(controlled…)` + publisher + QA/stage-runtime/smoke settings. Progress events go to stderr through `config.RedactValue("smoke.progress", …)`.
- Result rendering (text or `{schema_version,operation:"sprint.smoke",status,result}` JSON); `mapSmokeError` maps typed `SmokeError`s to stable CLI codes; final verdict `fail|blocked` ⇒ classified ExitValidation (5).

### 2.2 `Service.RunSmoke` (smoke.go:21-61)
- Dry-run bypasses lease and all state writes (`return s.runSmoke(...)` directly).
- Non-dry-run: `acquireMutationContext` (flow-state lease; reused, not re-acquired, when called under `Verify`) → `saveSmokeAttempt(running)` — aborts before any harness work on persistence failure → `runSmoke` → `saveSmokeAttempt(terminal)` (error joined with the run error) → roadmap reconciliation → publication.
- Roadmap reconciliation (smoke.go:39-48): only when `Status==completed ∧ Verdict==pass ∧ !DiagnosticOnly ∧ ReviewVerdict ∈ {pass, pass_with_findings}`; resolves the sprint, then `project.MarkRoadmapSprintDelivered(<project>/roadmap.md, slug)` (atomic temp+fsync+rename status-line rewrite; idempotent when already delivered; errors surface as `roadmap_reconciliation`, do not roll back the committed smoke evidence).
- Publication (smoke.go:49-59): when completed, non-diagnostic, artifact non-empty — **re-runs `prepareSmokeStatic`** (fresh manifest/catalog/review-gate evaluation) and then `publishSmokeStage`; failures are joined onto the success result.

### 2.3 `runSmoke` phase pipeline (smoke.go:63-189)
preflight `prepareSmokeStatic` → (non-dry) `authorSmokeSuite` → discovery child process (runs in dry-run too) → `decodeOneJSON` + `validateSmokeDiscovery` → coverage mapping enriched from `requirements.md` acceptance-criteria order → `selectSmoke` → blocked/not_applicable ⇒ completed-with-verdict `commitSmoke` (dry-run: plain return) → build redacted `SafeArgv`; dry-run returns `ready`/"Confirm and run" → run child process → truncation/cleanup/identity gates → `decodeOneJSON(response)` → `validateSmokeRun` → counts/verdict/next-action synthesis → `DiagnosticOnly` ⇒ return **without committing** → `commitSmoke`.

### 2.4 Alternate entrypoints
- `Verify` (verify.go:39-95): sole review→smoke transition; holds the lease across both stages; `requireCompleteExecute` first; reuses a fresh completed review or runs one (diagnostic continuation to smoke allowed only for completed-fail review + ForceReview + confirmed rationale); then `RunSmoke`.
- `SmokeStatus` (smoke.go:252-272): readiness projection for `sprint status` (re-runs `prepareSmokeStatic`, overlays persisted `state.Smoke` fields).
- `ValidateSmoke` (smoke.go:274-302): `sprint validate smoke` — artifact presence/content findings + flow-state fingerprint cross-check.
- TUI/web: `sharedOperationRunner` `OperationSmokeStart` (operation_runner.go:79-96) and `OperationVerifyStart` (:97-113); dry-run/status variants execute `RunSmoke(DryRun:true)` inside confirmation-preview and operation dispatch (operations.go:289-297, 474-487). Wiring note recorded in §11.

## 3. Static readiness details

`prepareSmokeStatic` (smoke_protocol.go:111-210):
- Catalog: filters `SectionSmokeHarnesses` entries; exactly one required (`smoke_catalog`). Target resolution via `resolveSprintTarget` (`smoke_target`). Harness root = `canonicalDirectory(entry.Path)` (symlinks resolved; must be dir). Catalog parsing/validation lives in internal/project (index.go:81-92 requires Manifest for smoke rows; validation.go:168-196 enforces absolute root + manifest containment at validate time; domain.go:16).
- Containment primitives: `canonicalFileInside` / `canonicalDirectoryInside` (EvalSymlinks + lexical `inside()` check, artifacts.go:77) applied to manifest, executable, cwd, runs root, issues root — all must stay inside the harness root.
- Manifest grammar (`validateSmokeManifest`, :213-258): `schemaVersion==1`, `protocolMajor(protocolVersion)==1`; required harness ID/version, executable, cwd, discover+run commands, evidence roots; default timeout parseable, (0,24h]; capabilities must include `discovery`, `run`, `evidence-v1`, `scope-mapping`, `authoring-v1`; authoring paths non-empty, clean, unique, pairwise non-overlapping (`smokePathsOverlap`) and disjoint from evidence roots; environment names `validProtocolEnvName` (uppercase identifier) and unique.
- Review gate: `LoadFlowState` (errors ⇒ `smoke_review_state`); `state.Review` required; stale recomputation — `review.Stale = PrepareReview error/findings || (strictCompletedReviewSnapshotFreshness && fingerprint mismatch)`; when not stale, additionally artifact readable ∧ `ValidateReviewContent` clean (manifest fingerprint pinned to recorded value while the strict switch is false, freshness_policy.go:12) ∧ recorded `ArtifactDigest` matches current review.md bytes. Stale ⇒ `smoke_review_stale` ("stale reviews cannot be overridden").
- Verdict gate: `ReviewPass|ReviewPassWithFindings` proceed; `ReviewFail|ReviewVerdictBlocked` require `ForceReview ∧ OverrideConfirmed ∧ non-empty OverrideRationale` (else `smoke_review_blocked` / `smoke_review_override_confirmation`); anything else ⇒ `smoke_review_invalid`. Forced fail/blocked runs later set `DiagnosticOnly=true` (smoke.go:128), which suppresses artifact/state commitment entirely.
- Timeout resolution (`smokeTimeout`, :644-657): request flag > configured source (`smoke.run_timeout` from workspace/env per `Sources`) > manifest default (>0, ≤24h) > configured/default fallback, with provenance string retained in results/artifact.

## 4. Agent authoring (`authorSmokeSuite`, smoke_author.go:20-121)

- Requires `s.runtime` (nil ⇒ `smoke_author_runtime`); loads planning inputs and renders the byte-stable shared prompt prefix (`prepareSharedPromptContext(..., true)`), then the stage suffix from `prompts/smoke.md` template (workspace override else built-in default, prompts.go:241-257) + "UltraPlan Smoke Authoring Manifest" section + exhaustive writable-path list + boundary contract prose + direct-input packet (sprint-index, technical-handbook, area reasoning dir, reasoning.md, plan.md, execute.md, review.md, `.run-state.json` contents injected verbatim).
- Snapshot before/after (`smokeHarnessSnapshot`, :333-374): WalkDir over harness root skipping `.git`, `node_modules`, runs/issues evidence roots; symlink ⇒ hard error; >64 MiB regular file ⇒ error; >20000 files ⇒ error; value = per-file sha256. Diff (`changedSmokeHarnessPaths`) must satisfy `smokeAuthorPathAllowed` (exact file or descendant-of-directory match) else `smoke_author_scope` fails the whole authoring run.
- Runtime request: `WorkDir=HarnessRoot`, `Permissions="restricted"`, `RequireCaps+=permissions`, one allow `PermissionPathRule` per declared authoring path; routed model via stage runtime override (`planning.smoke_model/variant`, sprint_commands.go:940). Executed through `startPlanningStageRun` (session_state.go:105-156): resumes a compatible `.stage-sessions.json` entry (key `smoke`; provider/model/workdir match), injects continuation instruction, one fresh-session fallback on "session not found"; session deletion after success via `cleanupPlanningStageSessions`.
- `strictSmokeAuthorProtectedSnapshots` is compile-time false (freshness_policy.go:14): target/project identity capture, event-based protected-write attribution (`smokeAuthorAttributedProtectedWrite`, :139-174) and concurrent-change diagnostics are compiled out today; the harness-tree allowlist diff remains enforced unconditionally. Attribution logic and its markers remain tested (smoke_test.go:130-177).
- Unsupported permission count ⇒ `smoke_author_permissions`; empty RunID ⇒ `smoke_author_identity`; ctx cancelled ⇒ `smoke_author_cancelled`. `_ = cleanupPlanningStageSessions` is best-effort. Metrics appended to `projects/<p>/sprints/<s>/.runtime-metrics.json` (stage `smoke`, operation `author`) via `startSprintRuntime`/`recordRuntimeMetric` (runtime_metrics.go:116-171; mutex-guarded, 512-record ring, atomic write; failure becomes a result warning, not fatal).

## 5. Discovery, selection, environment, execution

- Child-process lane: `pprocess.DirectRunner` (process.go:60-152) — `exec.Command` with owned process group (Setpgid, process_unix.go:12), explicit-env-only, bounded truncating stdout/stderr captures (defaults 4 MiB / 1 MiB), timeout timer, ctx-cancel lane with SIGTERM→grace(5s default)→SIGKILL group teardown reporting `CleanupComplete`; progress events via bounded dispatcher (drops counted).
- Discovery argv: `manifest.Args + Commands.Discover + ["--target", target]`; cwd/executable/env from prepared values; `DiscoveryTimeout` (default 30s). Truncated stdout ⇒ `smoke_discovery_truncated`; non-single-JSON ⇒ `smoke_discovery_malformed`.
- `validateSmokeDiscovery` (:260-373): identity quadruple (schemaVersion 1, protocol, harnessId, evidenceSchema 1); per-kind unique non-empty IDs (prerequisites with status enum available/blocked; levels; suites; tests); closure (level→suites, suite→tests with ownership consistency, suite→prerequisites, test→suite); sprint mappings non-contradictory; complete mappings must assign every requiredCoverage ID through enumerated non-empty suites/tests (legacy unrelated mappings tolerated but can never select complete — smoke_test.go:260-285).
- Environment forwarding (`smokeEnvironment`, :623-642): name set = `settings.Environment` (config default `PATH,HOME,TMPDIR,LANG,LC_ALL`, config.go:189/501/573; env vars `ULTRAPLAN_SMOKE_*`, config.go:362-382) ∩ `manifest.Environment`; i.e. the manifest can only *confirm* allowlisted names, never add ones; values read via getenv at launch; empty values dropped; result sorted.
- Selection (`selectSmoke`, :441-540): duplicate sprint mapping ⇒ error; NotApplicable ⇒ completed `not_applicable`; explicit `--level`/`--suite`/`--test` validated against discovery, prerequisites checked (`checkPrereqs`: unknown or non-available ⇒ completed `blocked` listing descriptions/actions); `DiagnosticOnly`: test ⇒ always; suite ⇒ unless mapping is exactly that one complete suite; level ⇒ unless mapping complete and level's suites ⊇ mapping suites; default path ⇒ requires mapping `Complete ∧ suites ∧ requiredCoverage` else `blocked`. Explicit-level completeness additionally requires `containsAll(level.Suites, mapping.Suites)` (pinned by TestSmokeExplicitScopeMustCoverCompleteMapping).
- Run argv: `manifest.Args + Commands.Run + ["--project", p, "--sprint", s, "--workspace", root, "--target", target, "--scope-kind", kind, "--scope", join(ids,",")]`; `EffectiveTimeout` from §3; `SafeArgv` (smoke.go:638-657) keeps only option names (values → `[ARG]`, `--x=v` → `[REDACTED]`, base executable name) for display/persistence.

## 6. Evidence validation and verdict (`validateSmokeRun`, smoke.go:304-412)

- Identity: response schemaVersion 1, protocol/harness match manifest, non-empty RunID; scopeKind + joined scope equal the request; counts arithmetically consistent (non-negative, parts sum to Total); DurationMs ≥ 0; ≥1 evidence item.
- Test identity: every test ID non-empty, status ∈ passed/failed/skipped/error; recomputed status tallies must equal reported `Counts`; sorted expected IDs from discovery (`expectedSmokeTests`: test→itself, suite→suite.Tests, level→union of member suites' tests) must exactly equal sorted actual IDs (NUL-joined string comparison).
- Process coherence: `TimedOut | Cancelled | !CleanupComplete` ⇒ classify (`smoke_timeout`/`smoke_cancelled`/`smoke_cleanup`); `ExitCode != 0` with zero failed/error counts ⇒ `smoke_process_unexplained`.
- Evidence items: kind `issue` resolves against IssuesRoot, everything else against RunsRoot; relative paths joined to HarnessRoot; `canonicalFileInside` containment; stat + sha256 (declared SHA256 compared case-insensitively when present); non-issue evidence basename must contain RunID. Raw stat/hash errors propagate unwrapped (not SmokeError-classified).
- Issues: ID non-empty, status ∈ open/resolved; open issues require full metadata (test_id/severity/title/summary/theory/evidence/action); path contained in IssuesRoot with basename containing the ID; every failed/errored test must have a matching **open** issue (`smoke_issue_missing`).
- Verdict synthesis (`synthesizeSmokeVerdict`, :666-674): failed/errors > 0 ⇒ `fail`; any open issue ⇒ `pass_with_open_issues`; else `pass`. `nextSmokeAction` maps each of the five verdicts to fixed guidance. Blocked/not_applicable carry harness-supplied rationale/next-action from selection.

## 7. Commit path (`commitSmoke`, smoke.go:439-482) and artifact

- Order: `RenderSmoke` (deterministic markdown; wall-clock date line uses `time.Now().UTC()` directly) → `ValidateSmokeContent` must produce zero findings (required headings incl. Mutation/Safety and Verdict; verdict enum; placeholder scan; forbidden substrings `-----BEGIN`, `Authorization: Bearer`, `"stdout":`, `"stderr":`; next-action present) → `atomicWriteFile(smoke.md)` (temp+write+fsync+rename+best-effort dir sync, :714-746; prior artifact preserved on failure) → `LoadFlowState` → build replacement `SmokeStageState` + `LastComplete` → `SaveFlowState`.
- Fingerprints: `SmokeFingerprint == ArtifactDigest == sha256(smoke.md bytes)`; `InputFingerprint = refreshEvidenceFingerprint(smokeIdentityReferences)` (verify.go:293-347): sorted refs over manifest file bytes, review.md bytes, facts (review fingerprint/verdict, scope, harness, prerequisites, timeout), each evidence file, each issue file + status fact — file digests taken at commit time; hash errors are discarded (`inputFingerprint, _ :=`), and `validateSmokeStageState` rejects a `LastComplete` with empty InputFingerprint/ArtifactDigest, so such a commit surfaces as `smoke_reconciliation` after the artifact was already replaced.
- Flow-state load/save failures after artifact write set `Reconciliation=true` and return `smoke_reconciliation` ("Reconcile flow state by rerunning smoke"); prior verification evidence preservation and DB-authoritative routing are flow-state-surface behaviors (SaveFlowState backfills nil Review/Smoke/QA from prior state).
- `saveSmokeAttempt` lifecycle (smoke.go:191-232): running attempt `{ID:"smoke-<unixnano>", OwnerPID, HeartbeatAt}` recorded pre-work; terminal save converts ActiveAttempt→LastAttempt with CompletedAt and mapped status (`smokeAttemptStatus`: canceled/timed-out/blocked(review_gate,catalog)/completed/failed), sanitizing SmokeError diagnostics via `safeError`; on non-completed outcomes the displayed Verdict/RunID/EvidenceID are restored from LastComplete so later evidence stays visible. Missing flow state is tolerated by deriving a fresh stage skeleton.
- Post-commit `publishSmokeStage` (publication.go:93-121): harness-repo publish of changed authoring paths (when any), then workspace-repo publish of smoke.md + flow-state.json (+ roadmap.md iff `Verdict==pass ∧ !DiagnosticOnly`); publisher nil ⇒ no-op; visible results attached to `result.Publications`. Commit-before-publish ordering preserved; publication failure never rolls back committed state.

## 8. Inputs / outputs

Inputs: project-index.md catalog row(s), roadmap.md, governed sprint artifacts (requirements/sprint-index/handbook/reasoning*/plan/execute/review + .run-state.json), flow-state.json (Review + Smoke sub-records), harness tree (manifest JSON, executable, scripts, prior evidence), process env (allowlisted names), wall clock, os.Getpid, config smoke settings + Sources, agent runtime for authoring.
Outputs: smoke.md (atomic replace), flow-state.json Smoke record + attempt history, .stage-sessions.json smoke key (create/update/delete), .runtime-metrics.json append, roadmap.md status-line edit, git publications (harness repo + workspace repo), stdout/stderr envelopes (text/JSON), sentinel `*SmokeError{Code,Category,Message,Guidance}` values, runtime store GC before authoring (`CleanupRuntimeStores(sp.Path,72h,2GiB)`).

## 9. Authoritative state and ownership boundaries

- Product-owned: `projects/<p>/sprints/<s>/smoke.md`; `flow-state.json` field `smoke` (shape pinned by `validateSmokeStageState`, smoke_types.go:291-333: status/verdict enums, fixed Path, contained path, ≤240-char sanitized diagnostics, attempt active/last consistency, override shape, issue ID/status enum, LastComplete completeness); `.runtime-metrics.json`; `.stage-sessions.json`.
- Harness-owned (never rewritten by the product): manifest, executable, authoring paths (modified only by the authoring agent within the allowlist), `runs/`, `issues/` evidence trees.
- Workspace-owned: `roadmap.md` (status line only), git refs via publisher.
- Compile-time policy switches (freshness_policy.go:11-15): `strictCompletedReviewSnapshotFreshness=false` (used by the smoke review gate), `strictCompletedSmokeSnapshotFreshness=false` (gates evidence-refresh term in VerificationStatus and target ref in identity), `strictSmokeAuthorProtectedSnapshots=false` (gates target/project identity snapshots). Artifact existence, content validation, digest-vs-recorded comparisons, and the authoring allowlist remain enforced regardless.

## 10. Invariants (as implemented)

- No harness work starts unless: exactly-one catalog row, contained manifest/executable/cwd/roots, valid protocol-v1 manifest, and a current acceptable (or explicitly overridden) review.
- Authoring changes outside the declared path set reject the entire run; symlinks and oversized/unbounded harness trees abort snapshotting.
- Discovery and run responses must be exactly one JSON value each; truncated streams are failures, not data loss.
- Executed tests must equal discovered selected tests exactly (set + counts); every failure needs an open, fully-populated, ID-addressed issue under the issues root.
- Diagnostic-only outcomes never write smoke.md or flow-state verification evidence (but their attempts are recorded in flow state via the wrapper).
- Canonical smoke.md changes only via fsync+rename of a fully validated render; malformed/failed reruns preserve the previous artifact (test-pinned).
- Flow-state commit happens after artifact commit; roadmap mark after flow-state commit; git publication last; every later failure leaves earlier artifacts intact and reports reconciliation guidance instead of rolling back.
- Raw harness streams/secrets never enter smoke.md (validator forbidden-substring list; SafeArgv redaction; env values never persisted).
- Blocked/not-applicable are *completed* verdicts (committed), distinct from execution failure.

## 11. Trust boundaries

- Harness stdout is decoded and **executed as verification truth** (discovery graph steers selection/expected-test sets; run response decides verdict inputs) — mitigations: identity quadruples, referential closure validation, arithmetic checks, exact test-set equality, evidence containment + hashing, exit-code coherence.
- Manifest (JSON in the harness tree) selects the executable/argv prefix/cwd/evidence roots/authoring allowlist/env names — mitigations: cataloged absolute root, EvalSymlinks+containment on every path, capability/schema gates, env-name grammar, overlap rejection. The catalog row itself comes from user-authored project-index.md markdown (never executed as commands).
- Manifest-declared env names cross the allowlist boundary: effective names = config allowlist ∩ manifest list (manifest cannot expand), values sourced at spawn time.
- The authoring agent receives governed inputs verbatim (direct-input packet) and works inside the harness with restricted permissions; enforcement relies on runtime permission support (`UnsupportedCount` gate) plus the unconditional post-hoc tree diff.
- This is the product's direct non-agentwrap argv execution lane (per canonical surface map; QA's approved-check lane lives in a different surface). Child output is bounded and truncated streams are rejected rather than parsed.
- smoke.md renders selected harness-controlled strings (IDs, titles, summaries) through `printable()` (backtick flattening) and the forbidden-content validator; diagnostics persist through `safeError` sanitization.

## 12. Cancellation / retry / restart / error semantics

- Cancellation: ctx propagates into both child processes (group SIGTERM→SIGKILL with grace; CleanupComplete false if pgid lookup fails ⇒ `smoke_cleanup`) and is checked after authoring (`smoke_author_cancelled`); `smokeAttemptStatus` maps context.Canceled/SmokeCancelled ⇒ AttemptCancelled. Durable CLI acceptance provides the cancellable ctx; web/TUI cancel flows through the operation hub (other surface).
- Timeout: discovery and run have independent timeouts; timeout classification distinguishes `smoke_timeout` from generic process failure; request/config/manifest provenance recorded.
- Retry/rerun: rerunning smoke repeats the full pipeline; successful reruns atomically replace smoke.md and the Smoke record; failed reruns preserve the prior artifact AND restore Verdict/RunID/EvidenceID from LastComplete in flow state. Authoring resumes prior sessions when provider/model/workdir match, with one fresh-session fallback.
- Restart/crash: a crash between artifact write and flow-state save leaves smoke.md updated with `Reconciliation=true` guidance on next observation; a crash mid-attempt leaves a running attempt record that flow-state-surface reconcile (`attemptExpired`: dead PID immediate, else 2h heartbeat window — heartbeated once at attempt start here) converts to timed_out/interrupted. No cleanup-uncertain marker is produced by this surface.
- Error envelope: every failure path funnels through `smokeFailedResult` + typed `SmokeError` (code/category/message/guidance/wrapped cause); CLI maps categories to exit classes; JSON consumers get the full typed result.

## 13. Immediate surface dependencies

- `sprint-conformance-review`: `PrepareReview` (gate freshness inputs), review.md digest/content validation, ReviewVerdict enums; smoke's InputFingerprint embeds review fingerprint/bytes.
- `sprint-flow-state`: lease acquisition/reuse, LoadFlowState/SaveFlowState (evidence preservation, DB routing), attempt validators, expiry reconcile of smoke attempts.
- `process-execution`: DirectRunner request/result semantics, truncation, teardown ladder; smoke translates the manifest into two Requests.
- `repo-publication`: gitpublish.Publish for harness + workspace path sets, ordered last.
- `opencode-agent-runtime`: authoring StartRun (session stores, permission summary), runtime-store GC before authoring.
- Foundation: project catalog/target resolution, config smoke settings + redaction, workspace prompt-template override chain, durable-operation spine (accept/finish for CLI/TUI/web invocations).

## 14. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace `projects/ultraplan-go/docs/TRD.md`:
- §18.9.2 (~L2052-2068): catalog + versioned manifest; "UltraPlan must never execute a shell command parsed from Markdown or README prose"; author-model invocation before discovery on every non-dry run; discovery structure requirements; "A narrow passing rerun does not replace required evidence from its containing suite"; execution bullet list (current passing/non-blocking review default; author run/model and changed paths recorded; explicit argv, contained cwd, bounded env forwarding, timeout, cancellation, descendant cleanup; validate run ID/counts/evidence/issue refs; raw evidence stays in harness; atomic smoke.md after validating authoring scope and enumerated identities; unavailable required env ⇒ blocked; irrelevant scope ⇒ not applicable).
- ~L2070: product review/smoke workflows must not edit product source/tests/governed inputs/Git; smoke authoring limited to manifest-declared authoring paths; "Any other harness change ... fails smoke".
- §18.9.3 (~L2072-2080): blocking/high review findings stop smoke unless confirmed diagnostic override; flow state records execution status vs verdict distinction, input fingerprints; stale review makes smoke stale; CLI/TUI agreement.
- ~L1953-1959: smoke.md validator requirements (exists post-completion, no placeholders, identifies project/sprint/review verdict+fingerprint/harness/scope/rationale/environment/model/date, five-verdict enum).
- ~L50-55: non-goals — no auto-fixes, no Git mutation during smoke, harness owns detailed run/issue persistence.
- ARCHITECTURE.md: L32/146 smoke.go responsibility split; L146 lists a `smoke_validation.go` filename that does not exist in-tree (validation lives in smoke.go/smoke_types.go); L432-444 process package is the generic volatile-boundary adapter and "must not understand ... smoke levels"; L280 keep only current review.md/smoke.md in sprint root.
- In-repo doc.go: sprint package contract sentence covering review-gated smoke.

HISTORY/context only: workspace sprint dirs contain legacy manual `deep-smoke.md` files predating Phase 3; the real cataloged harness (`ultraplan-go-smoke`, absolute path outside the planning workspace) notes its versioned manifest as planned work — the in-repo fake-harness tests define de-facto protocol behavior exercised today.

## 15. Tests (evidence map)

Package-internal (internal/sprint/smoke_test.go):
- TestSmokeSelectionAndVerdicts — default complete-mapping selection sorted; not_applicable mapping; explicit test always diagnostic; verdict synthesis table.
- TestRenderSmokeIncludesDetailedIssueFinding — rendered finding/open-issue sections.
- TestDefaultSmokeEnvironmentPreservesInterpreterPath — PATH/HOME forwarded; undeclared secret dropped.
- TestSmokeAuthorProtectedWriteAttribution / TestSmokeConcurrentChangeDiagnosticIsBounded — attribution markers and diagnostic bounding (logic currently compiled out by the strict switch).
- TestSmokeExplicitScopeMustCoverCompleteMapping — partial level/suite stay diagnostic; containing level sufficient.
- TestSmokeDiscoveryRejectsBrokenRelationships / TestSmokeDiscoveryAllowsIdentityReuseAcrossKinds — closure errors; cross-kind ID reuse legal, same-kind duplicates rejected.
- TestPopulateSmokeCoverageRequirementsUsesGovernedAcceptanceOrder — AC-NN ordering from requirements.md checkboxes.
- TestLegacyMappingsDoNotBlockAuthoredSprintButCannotPass — unrelated legacy mapping tolerated; cannot establish completeness.
- TestSmokeAuthoringPathAllowlist — exact-file/descendant matching; sibling-prefix escapes rejected.
- TestSmokeRunCommitsValidatedArtifactAndPreservesItOnMalformedRun — end-to-end with fake author runtime + recording runner: full happy path (roadmap marked delivered, assessment pass, coverage mapping persisted, prompt-boundary assertions, author model routing), unsupported-author-permissions aborts before any harness call, malformed discovery preserves prior smoke.md byte-for-byte and records failed attempt while retaining LastComplete.
- TestSmokeManifestRejectsUnsafeValues — protocol/dup-env/timeout-bounds/overlapping-paths/root-authoring rejections; SafeArgv redaction.
- TestRealSmokeHarness — opt-in (`ULTRAPLAN_REAL_SMOKE=1`) integration against the real cataloged harness; skipped by default.
Fixtures: `reviewFixture` (review_test.go:678) supplies the full governed-input chain; smoke harness fixtures create real temp executables/manifests and a `Smoke Harnesses` catalog row.
App-level: sprint_commands_test.go:612 (flow --to smoke requires --yes), :739 parse/help assertions (mutual exclusion, forced-review rationale, help text); sprint_verify_commands_test.go:11 (verify/flow smoke-flag parity); sprint_error_test.go:11 (mapSmokeError preserves typed cause); run_control_inventory_test.go:26 (OperationSmokeStart inventory).
Baseline: `go test ./...`, `-race`, `-cover`, `go vet` green at the frozen commit (review/baseline).

## 16. Explicit unknowns / open questions (for later reviewers)

1. `sharedOperationRunner.OperationSmokeStart` (operation_runner.go:79-80) constructs the sprint service **without** `WithRuntime`, unlike every other runtime-backed branch (and unlike `OperationVerifyStart`, which uses `sprintRuntimeService`). On the TUI/web smoke-start path `authorSmokeSuite` therefore hits the nil-runtime guard (`smoke_author_runtime`) before any harness work. Whether this door is intended to be authoring-capable is undocumented.
2. All three strict freshness/protected-snapshot switches are compile-time false; target/project identity drift during authoring is currently undetectable by design (comment attributes brittleness). Digest-vs-recorded checks carry the remaining enforcement; the smoke gate's practical strength depends on the review-digest equality path.
3. `commitSmoke` discards `refreshEvidenceFingerprint` errors; a transient hash failure yields an empty InputFingerprint which `validateSmokeStageState` then rejects at SaveFlowState, converting a completed run into a `smoke_reconciliation` outcome after smoke.md was already replaced.
4. `prepareSmokeStatic` executes twice on the success path (preflight and pre-publication), each re-reading manifest and re-validating the review gate; behavior when the gate flips between the two evaluations (error joined onto an otherwise-successful result, after roadmap marking) is defined only by code order.
5. Roadmap-mark condition (Verdict pass ∧ !DiagnosticOnly ∧ ReviewVerdict ∈ {pass, pass_with_findings}) differs from the roadmap-in-publication condition (Verdict pass ∧ !DiagnosticOnly); whether the review-verdict asymmetry between marking and publishing is intended is unstated.
6. Dry-run executes the harness `discover` subprocess (a real external effect) while skipping authoring/run and all writes; operator expectations for "preview" purity are documented only in help text.
7. Smoke attempts heartbeat once at start (`HeartbeatAt=StartedAt`); long runs approach the flow-state 2h expiry window with no renewal from this surface (expiry reconcile belongs to sprint-flow-state).
8. Evidence items route to RunsRoot for any kind except `"issue"`; the accepted kind vocabulary beyond issue/run/summary is unpinned. Evidence `os.Stat`/hash failures escape as raw errors rather than typed SmokeErrors (inconsistent error envelope on that branch).
9. `RenderSmoke` uses `time.Now().UTC()` rather than `s.now()`; artifact Date line is not clock-injectable (test determinism impact unstated).
10. `smokeInputFingerprint` (verify.go:287-291) has no callers; commitSmoke inlines equivalent logic — divergence risk if one copy evolves.
11. Empty-string env values are dropped silently, so the harness cannot distinguish "unset" from "set to empty" for allowlisted names; manifest-declared-but-unconfigured names are likewise silent omissions rather than `blocked` classifications (TRD's "classify unavailable required environment as blocked" has no dedicated implementation visible in this surface).
12. Platform split in teardown guarantees: linux/darwin get owned process groups with SIGTERM→SIGKILL group ladders (process_unix.go); the `!linux && !darwin` build (process_other.go:11-24) skips Setpgid entirely and Kill()s only the direct child, returning CleanupComplete=false after the grace window — descendant-process cleanup guarantees are therefore platform-dependent, and smoke's `!CleanupComplete ⇒ smoke_cleanup` failure treats that degradation as an error.

— End of context pack. Descriptive only; no defect claims made or implied.
