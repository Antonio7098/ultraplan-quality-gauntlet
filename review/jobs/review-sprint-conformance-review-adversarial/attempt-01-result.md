Context pack read. Now examining the supporting machinery: runtime startup, flow-state persistence, lease, and the agentwrap contract.Baseline green. Now I'll empirically confirm the strongest candidate defect (coverage-ID slug collision wedging the review) by reproducing it against a throwaway copy of the frozen tree.Reproduced — and the failure mode is even harder than predicted (state-validation wedge, not just overwrite). Let me pin down one remaining loose end on session deletion targeting, then finalize.The cleanup-path finding is confirmed by code. One quick empirical check that `--restart` cannot escape the slug-collision wedge, then I'll finalize.My second repro had a fixture bug (dropped the target-path substitution). Re-running with the exact test-1 setup plus `Restart: true`:Both repros confirm the wedge. Cleaning up the scratch copy and finalizing.# Sprint Conformance Review — Fresh-Context Adversarial Review

Target: ultraplan-go @ `5d54525` (untouched, detached HEAD only). Evidence: source read in full (`review.go`, `review_runtime_validation.go`, plus state/flow/locks/verify/runtime/store/session-state/publication layers and the agentwrap dependency), tests read, two defects reproduced empirically against a throwaway copy.

## Finding 1 — Duplicate coverage identities from punctuation-variant catalog names permanently wedge the review stage (reproduced)

**Severity: medium · Confidence: high (empirically reproduced)**

- **Claim:** Two distinct Active Contract Pool entries whose names differ beyond case-fold but slug identically (e.g. `Rate Limiting` vs `Rate-Limiting`) produce two coverage items sharing one ID; the review then fails on every run — including `--restart` — with a misleading durable-state-corruption diagnostic, and reviewers never execute.
- **Bad outcome:** Governed inputs that pass every existing preflight check block the review gate forever. The recorded diagnostic is `resume-state: flow state malformed … invalid review resume checkpoint`, pointing the operator at corrupted state instead of the real cause (duplicate selection identity). `--focus <id>` cannot escape either ("focused review requires a previous complete review").
- **Trigger/preconditions:** project-index lists both names in Active Contract Pool; sprint-index selects both. All current controls pass: `ParseSprintIndex` dedupe is keyed on name+path (index.go:101–105), contract selections skip path validation entirely (validation.go:33), and each name still resolves uniquely through `catalogEntry` (review.go:1312–1325).
- **Path:** `PrepareReview` builds IDs via `prefix+"-"+slugReviewID(name)` (review.go:314) → `m.Coverage` holds two entries with ID `contract-rate-limiting` → initial running save succeeds (Resume nil) → `initializeReviewResume` creates one checkpoint per item (review.go:727–735) → `SaveFlowState` → `validateReviewStageState` rejects duplicate checkpoint IDs (review.go:1174–1179) → resume-state failure; terminal save records failed/blocked. Reproduced for default and `Restart` runs with zero runtime calls; second coverage slot would also be silently unfillable in the collector (review.go:565–570) if resume were bypassed.
- **Counter-evidence searched:** exact-duplicate rows rejected upstream; EqualFold-identical names rejected as ambiguous at resolution; DB/file flow-state paths both funnel through the same validator. None catch this shape.
- **Regression test:** fixture selecting `Rate Limiting` + `Rate-Limiting`; assert review either completes or blocks with an explicit preflight "duplicate review coverage identity" finding naming both catalog entries — never the resume-state corruption error. Fix options: preflight finding on duplicate slugged coverage IDs, or make IDs collision-free (path-hash suffix).

## Finding 2 — Post-success reviewer session deletion targets the wrong database; cleanup is a silent no-op (code-verified)

**Severity: low · Confidence: high (mechanism verified in both codebases; production adapter not executed)**

- **Claim:** After a passing review, `deleteCompletedSession` (review.go:643–645 → flow.go:26–35 → runtime.go:269–280) deletes by session ID against the *default* OpenCode database, but reviewer sessions live in per-coverage scoped stores selected per-run via `OPENCODE_DB`.
- **Evidence:** `runtimeRequest` always sets `RuntimeStorePath` (service.go:1138–1139); scoped path becomes request metadata (runtime.go:576–582) translated to `OPENCODE_DB` inside agentwrap's process spec (agentwrap opencode/runtime.go:154–159). The `deleteSessions` closure (opencode.go:92–124) runs `<opencode> db "DELETE FROM event_sequence…"` and `<opencode> session delete <id>` with only static config env (openCodeDBCommand, opencode.go:180–184) — it cannot know the store path.
- **Bad outcome:** cleanup no-ops (0-row DELETE; `session delete` errors "session not found", aborting the loop before checkpoint/VACUUM); every successful review leaks its per-coverage SQLite stores until a later sprint-stage run's 72h/2GiB sweep reclaims them (runtime_metrics.go:119). All evidence swallowed by `_ =`. Contrast: execute cleans correctly via `DeleteRuntimeStore(result.RuntimeStorePath)` (flow.go:37–42).
- **Regression test:** fake adapter capturing the delete target; assert post-success review removes the scoped store directory (route deletion by `RuntimeStorePath`, as execute does).

## Finding 3 — Model provenance mislabeled when execute-model wins the fallback chain

**Severity: low · Confidence: high**

- **Claim:** With `planning.review_model` unset but both execute and plan models configured, `reviewModelSelection` returns the **execute** model yet relabels its source `planning.plan_model` whenever StagePlan has any model (review.go:1293–1297; `executeModelSelection` checks `StageExecute` first, execute.go:710–728).
- **Bad outcome:** `Model source: planning.plan_model` rendered into every reviewer prompt header, into the canonical review.md, and into metrics/JSON — wrong provenance for the model that decided the verdict.
- **Regression test:** configure both stage models, run review, assert artifact's model source equals the actual selection source.

## Defended hypotheses (investigated, not defects)

- **Continuation of swept/deleted sessions:** OpenCode continuation is explicitly best-effort/unverified (agentwrap sessionMetadata), but within-run repair falls back to fresh sessions on any non-cancel/timeout/permission error (validation.go:382–388, freshRepairFallbackAllowed), so worst case is degraded context with bounded extra calls; decision tables still gate all output. Fail-safe, not fail-wrong.
- **Injection via last-object-wins extraction (`extractReviewJSON`):** injected objects must still satisfy the full schema/citation decision table against frozen manifest contents; sources are actor-governed inputs embedded verbatim by design. No trust transition crossed.
- **Focus-mode retained results skip re-validation:** fingerprint equality pins manifest contents byte-for-byte (normalization is deterministic), so retained citations remain valid.
- **Snapshot `.complete` trusted on presence / rebuild races:** same-sprint concurrency is excluded by the cross-process mutation lease (locks.go); forged markers are workspace-trust, equivalent to editing review.md directly.
- **Blocked-verdict unreachable render path, never-set `ProvisionalVerdict`, `_ = now` dead store:** latent/dead code, no observable failure.
- Minor observations folded out of findings: panic-path `out.Error` skips `safeReviewText` (review.go:888) though no attacker-controlled source reaches panics; invalid-result slots also emit `missing-coverage` (duplicate diagnostics, blocked either way); severity sort is lexicographic (deterministic, odd display order).

Baseline `go test ./internal/sprint/ -run 'TestReview|…'` green before and after investigation; target repo left clean at the frozen commit.