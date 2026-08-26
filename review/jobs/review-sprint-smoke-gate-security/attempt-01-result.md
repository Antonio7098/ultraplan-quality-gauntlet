Context pack read. Now inspecting the actual source.Primary files read. Now the supporting wiring: process runner, verify fingerprint logic, publication, and CLI.Now checking service construction, config defaults, gitpublish, and the runtime permission model.`commitPaths` uses git plumbing (`commit-tree`/`update-ref`), not `git commit` — so hooks don't fire during publication. Now checking the tests and remaining wiring.Tests read. Checking the remaining wiring pieces: freshness policy flags, lease acquisition, and session-run plumbing.Wiring confirmed. Running the targeted smoke tests and vet to anchor the baseline before finalizing findings.Baseline green (`go vet` clean, smoke tests pass). Verification complete — here are the results.

---

## Findings

### F1 — Forced-review diagnostic runs commit canonical evidence when selection resolves `blocked`/`not_applicable` (medium, high confidence)

**Claim.** `runSmoke` computes `result.DiagnosticOnly` at internal/sprint/smoke.go:128 specifically to mark forced fail/blocked-review runs as diagnostic-only, but the branch at smoke.go:129-135 commits unconditionally for those verdicts without checking the flag:

```go
result.DiagnosticOnly = selection.DiagnosticOnly || (req.ForceReview && (prepared.Review.Verdict == ReviewFail || prepared.Review.Verdict == ReviewVerdictBlocked))
if selection.Verdict == SmokeBlockedVerdict || selection.Verdict == SmokeNotApplicable {
    result.Status, result.Verdict, result.NextAction = SmokeCompleted, selection.Verdict, selection.NextAction
    if req.DryRun { return result, nil }
    return s.commitSmoke(prepared, result)   // ignores result.DiagnosticOnly
}
```

Every other DiagnosticOnly path honors it (executed scope returns before commit at smoke.go:180-182; publication and roadmap gates check `!result.DiagnosticOnly` at smoke.go:39,49) — this branch is the lone exception.

**Bad outcome.** A diagnostic run performed over a **failing or blocked conformance review** writes canonical `smoke.md` and a flow-state `Smoke` record with `LastComplete` (completed status, override recorded), i.e., durable verification evidence derived from an overridden failed review. This violates the surface's own invariant ("Diagnostic-only outcomes never write smoke.md or flow-state verification evidence") and TRD §18.9.3's treatment of overrides as non-delivery evidence. The triad is internally inconsistent: artifact+state commit, but publication/roadmap skip because they *do* honor the flag.

**Trigger.** Review verdict fail/blocked + `--force-review --override-reason X --yes`; harness discovery then reports a not-applicable mapping (selectSmoke:461-463 returns before any prereq/scope work) or blocked prerequisites (:493,505,523,537). Also reachable through `Verify`'s fail-review diagnostic continuation (verify.go:74).

**Counter-evidence searched.** Top-level assessment does not flip to pass — `deriveAssessment` reports Fail/Blocked from the fresh failing review first (verify.go:256-261); roadmap is never marked; no test covers forced-review × blocked selection (smoke_test.go has none). Harm is confined to the evidence chain, hence medium severity.

**Regression test.** Fixture with completed-fail review + not-applicable mapping; run `RunSmoke` with ForceReview+confirmed rationale; assert `smoke.md` absent and `state.Smoke.LastComplete == nil` while the attempt records the diagnostic outcome.

### F2 — TUI/web `OperationSmokeStart` constructs the service without a runtime; every non-dry start fails (medium, high confidence)

**Claim.** operation_runner.go:80 builds the smoke service as `sprint.NewService(root.Path).WithPublisher(...).WithSmokeSettings(...)` — no `WithRuntime`, unlike every sibling runtime-backed branch including `OperationVerifyStart` (:98 uses `sprintRuntimeService`). Non-dry smoke requires authoring, which hard-fails on nil runtime (smoke_author.go:21-23, `smoke_author_runtime`).

**Bad outcome.** Any confirmed smoke-start dispatched through the shared runner (TUI action / web API — operations.go:310 advertises `Mutates=true`, runtime identity "configured smoke author and harness" at :657-658) fails after durable acceptance and after `saveSmokeAttempt(running)` recorded a running attempt, leaving a failed attempt plus failed durable op each time. The identical CLI invocation works (sprint_commands.go:571 rebuilds via `sprintRuntimeService`). No comment or guard indicates CLI-only intent; the branch otherwise mirrors VerifyStart field-for-field.

**Regression test.** Dispatch `OperationSmokeStart` in an app-level fixture and assert the authoring request is constructed / run completes instead of failing with `smoke_author_runtime`.

### F3 — Authoring-scope diff cannot detect out-of-allowlist changes under `.git`, `node_modules`, or the evidence roots; non-regular file swaps are invisible (low-medium security, high confidence on behavior)

**Claim.** The unconditional backstop enforcing "Any other harness change ... fails smoke" (TRD ~L2070) — `smokeHarnessSnapshot`/`changedSmokeHarnessPaths`/`smokeAuthorPathAllowed` — never sees writes there: smoke_author.go:353 does `SkipDir` for `.git`, `node_modules`, and everything inside RunsRoot/IssuesRoot, and :368-370 silently skips non-regular entries. A changed path that appears in neither snapshot cannot trip `smokeAuthorPathAllowed`, so out-of-allowlist modifications in exactly those trees pass the gate and are omitted from `AuthorChangedPaths`/smoke.md audit trail.

**Exploitability (honest framing).** Requires the restricted-permission runtime to miss a write — the precise scenario the post-hoc diff exists to catch. I chased the sharpest sink and it is closed: `publishSmokeStage` → gitpublish commits via plumbing only (`read-tree`/`add`/`write-tree`/`commit-tree`/`update-ref`, publisher.go:200-261), so planted `.git/hooks` do **not** execute at publication. Residual concrete outcomes: tampered `.git/config`/refs steer all later harness-repo git operations (branch resolution, parent selection at publisher.go:87,208); planted `node_modules` lifecycle content executes later when the manifest executable itself invokes tooling under DirectRunner — outside every authoring-time boundary; evidence-tree writes corrupt the audit story. Primary enforcement remains the runtime policy, so defense-in-depth severity.

**Regression test.** Between two `smokeHarnessSnapshot` calls, create `.git/hooks/x`, write into `runs/`, and swap a regular file for a FIFO; assert the diff surfaces them (or snapshot errors) instead of passing.

### F4 — `commitSmoke` discards the input-fingerprint error; transient hash failure converts a successful run into reconciliation after `smoke.md` was already replaced (low reliability, high confidence on mechanism)

**Claim.** smoke.go:463 `inputFingerprint, _ := refreshEvidenceFingerprint(identityRefs)`. On error the empty fingerprint flows into both `SmokeStageState.InputFingerprint` and `LastComplete.InputFingerprint`; `validateSmokeStageState` (invoked by SaveFlowState, state.go:356-359) rejects an empty `LastComplete.InputFingerprint` (smoke_types.go:328), so Save fails and the run returns `smoke_reconciliation` — after `atomicWriteFile` already replaced the artifact (:448). Result: valid pass evidence on disk, no completion record, digest-mismatch findings from `ValidateSmoke`, "rerun smoke" guidance for a run that succeeded. Trigger needs a read/hash failure in the post-validation window (evidence file deleted/replaced concurrently, review.md transiently unreadable) — low likelihood, but the failure mode is self-inflicted state divergence. Regression: force a hash failure between validate and commit; assert either retryable typed classification or pre-artifact abort.

### F5 — Evidence hashing reads files of unbounded size (low resource safety, high confidence on code)

**Claim.** `hashFile` (smoke.go:707-712) does `os.ReadFile` with no cap when hashing run response evidence and issue files (smoke.go:363, and again during fingerprint refresh). Every other tree read on this surface is bounded at 64 MiB (snapshot :371; `targetIdentity` verify.go:433), and stdout is bounded at 4 MiB before decode — this lane forgot the discipline. A degenerate/hostile harness can declare a multi-GiB sparse regular file under its runs root; validation OOMs the ultraplan process mid-gate. Regression: declare a large sparse evidence file; assert bounded rejection.

---

## Defended / non-issues

- **Env forwarding cannot expand the allowlist**: `smokeEnvironment` unions settings.Environment with `manifest ∩ settings` — the loop is a tautology; effective env ≡ config allowlist minus empty values (test-pinned). Silent omission of manifest-declared-but-unset names vs TRD's "blocked required env" is a protocol-v1 schema gap (no `required` concept), FUTURE-INTENT flavored.
- **Git hooks at publish**: probed as escalation for F3; closed — plumbing-only commits execute no hooks.
- **Response size/parse safety**: truncation gate precedes `decodeOneJSON`; single-value enforcement rejects concatenated/trailing garbage; discovery/run JSON bounded by capture limits.
- **Selective reporting**: sorted NUL-joined exact set equality + recomputed tallies must equal reported counts; duplicate IDs in `response.Tests` break equality; every failed/errored test needs a fully populated open issue with ID-addressed file under IssuesRoot.
- **Containment**: EvalSymlinks+`inside()` on manifest/executable/cwd/evidence/issue paths; symlinks hard-error in snapshots; catalog row itself is inert markdown.
- **Review gate**: stale reviews refuse override entirely; recorded-digest equality enforced even with strict switches off; forced runs marked diagnostic (modulo F1).
- **Blocked/non-applicable committing in ordinary (non-forced) runs** is intended semantics (completed verdicts distinct from execution failure) — only the forced-review interaction (F1) is defective.
- **RenderSmoke newline injection** from harness strings: `printable()` flattens backticks but not newlines, so multi-line issue fields can inject markdown prose; machine consumers read flow-state JSON and `fieldBacktick` is first-match-wins against the genuine header, so impact is limited to human-facing artifact confusion — noted, not raised to a finding.
- **Dry-run executing discovery** is bounded, writes no product state, and matches help text; lease bypass for dry-run is consistent with zero product writes.