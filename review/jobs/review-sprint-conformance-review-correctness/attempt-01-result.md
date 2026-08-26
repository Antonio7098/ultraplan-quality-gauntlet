The context pack points to a surface doc. Target repo HEAD (`c1eb26a`) is ahead of the frozen commit (`5d54525`) — let me verify what differs and read the surface pack.Tree is clean; the extra HEAD commit only touches study artifacts, so `internal/sprint` matches the frozen commit. Now reading the primary sources.Primary sources read. Now verifying helper implementations that several hypotheses hinge on (`inside`, `resolveSprintTarget`, `targetRevisionIdentity`, `ResolveInside` symlink behavior, stale recomputation).`inside` is purely lexical; `targetRevisionIdentity` hashes root+HEAD (so `.identity` collision would need a real changed file). Next: check `normalize` (symlink handling?), `resolveSprintTarget`, and how the execute stage treats the same changed paths (precedent for symlink containment).No symlink resolution anywhere in path helpers. Now checking whether any other sprint surface resolves symlinks for changed paths, how `.run-state.json` changed paths originate, and the flow-state save/load machinery.The codebase has a clear symlink-aware idiom (`verify.go`, `qa_state.go`, `prompt_context.go`) that review's changed-path loop skips. Checking how execute validates evidence paths and what verify.go's pattern looks like.Execute validates evidence paths only lexically too, while verify's `targetIdentity` explicitly rejects escaping symlinks — good precedent evidence. Now checking `runtimeRequest` (cache-key collisions across coverage items?), the stale-recompute block in service.go, and flow-state save/load.Status-side stale recompute covers the LastComplete-restore concern. Now reading the full review test file plus the remaining wiring (`locks.go`, `flow.go`, `publication.go`, `runtime_metrics.go`).Tests confirm the pack's evidence map; notably `reviewCoveragePlan` (focus) has zero test coverage. Now reading the remaining wiring: locks, flow-state persistence, publication, runtime sweep, and the Verify→review skip path.CLI maps both `--dry-run` and `--prompt` to `DryRun`; checking who sets `PromptOnly`, and reading flow-state persistence + mutation lease internals.`PromptOnly` is never set by any caller — the asymmetry is latent, not live. Now reading state persistence and lease internals.`validateSubset` doesn't reject duplicate names/rows — need to see whether `ParseSprintIndex` or the project-index parser dedupes. Reading both parsers.Duplicate-name contract pairs are reachable end-to-end (no parser dedupes; `validateSubset` explicitly skips path checks for contracts). Before finalizing, checking the workspace contract wording for the verdict ladder, severity ordering, and changed-path containment to calibrate findings against CURRENT-CONTRACT.Protocol confirms verdict-ladder wording (missing reviewer ⇒ `fail` in protocol vs blocked+failed in impl — documented divergence) and requires "evidence containment". Now confirming `ValidateFlowState` wires the review grammar, checking project-side uniqueness guards, and reading `executeModelSelection`.Contract wording captured. Final verification greps for Finding 1 (no O_NOFOLLOW / no Lstat anywhere in review's changed-path path) and a last look at the app-level durable op wiring for review focus.# Sprint Conformance Review — deep independent correctness review

Target `ultraplan-go` @ `5d54525` verified (working tree clean; HEAD's extra commit touches only study artifacts). All primary paths, helpers, parsers, persistence, lease/publication wiring, contract texts, and the full review test suite were read directly. Four substantive findings, one minor, plus defended non-issues.

---

## F1. Symlinked changed path escapes approved-target containment and enters reviewer prompts/snapshot (Medium, confidence: high)

**Claim.** `PrepareReview`'s changed-path ingestion checks containment **lexically only**, then reads contents with `os.ReadFile`, which follows symlinks. A changed path that is a symlink pointing outside the approved target passes `inside()` and its target file's bytes enter the manifest.

**Observable bad outcome.** Files far outside the approved implementation scope (e.g. `/home/<user>/.ssh/*`, sibling repos, workspace internals) are (a) embedded verbatim into **every** reviewer prompt sent to the model provider, (b) written into the frozen snapshot under `.ultra/cache/review/...`, and (c) become line-citable "evidence" that can be quoted into finding text and published to git via `review.md`.

**Trigger/preconditions.** `.run-state.json` is actor-editable execute evidence (parsed raw at review.go:280-286, not via the validating loader). The executing agent — the exact party review exists to check — lists a changed/evidence path that is, or resolves through, a symlink escaping the target root. Committed symlinks are ordinary git worktree content; worktree creation imposes no prohibition (`createSprintWorkspace`, execute_target.go:136-159).

**Evidence.**
- review.go:350-367: `full := filepath.Join(manifest.Target, changed)` → `if !inside(manifest.Target, full)` → `os.ReadFile(full)` → `manifest.Contents["target/"+rel]`.
- `inside` (artifacts.go:77-83) and `normalize` (workspace/discovery.go:69-78, used by `ResolveInside`) are pure `Abs`+`Clean`+`Rel`. Repo-wide grep: **no `Lstat`/`EvalSymlinks`/`O_NOFOLLOW` anywhere on this path**.
- Content flow into prompts/snapshot: renderReviewerPrompt → `directContentInput` (review.go:1544-1556); prepareReviewSnapshot writes `m.Contents` (review.go:1610-1630).

**Execution path.** run-state → `reviewChangedPaths` → `excludeGovernedReviewPaths` → containment loop → `Contents["target/<rel>"]` → reviewer prompt packet + snapshot file + citation-eligible content.

**Controls and counter-evidence searched.** `../`-style and absolute-outside paths *are* rejected (lexical `Rel` prefix check → preflight finding). Governed artifacts are excluded by exact path. Empty/unreadable targets block earlier. But none of these catch a contained-path symlink. Decisive counter-evidence against "not a concern here": the **same package already treats exactly this hazard as fail-closed** — `targetIdentity` (verify.go:405-427) does `Lstat` + `EvalSymlinks` and errors `"target symlink escapes or is unreadable"`; `qa_state.go:63,108` and `prompt_context.go:357-369` use the same idiom. The protocol mandates the control review bypasses: freeze scope over "changed paths", deterministic check of "**evidence containment**" (workspace `system/protocols/review-sprint-protocol.md` §2/§3), and TRD §18.9.1 fingerprints "explicit changed-path scope" with "citation containment".

**Severity/confidence.** Medium (scope-containment bypass enabling data exposure to an external provider and evidence poisoning; requires a planted symlink). High confidence in mechanism; impact framing standard for this threat model.

**Regression test.** Fixture whose `.run-state.json` names `docs/notes.md`, where that file is a symlink to a temp file outside the target containing a marker string. Assert: Review returns blocked with a preflight finding, and the marker appears in **no** reviewer request prompt and no snapshot file. At the frozen commit this run passes and embeds the marker.

---

## F2. Validator-clean duplicate contract names produce duplicate coverage IDs; resume grammar then bricks every retry with an unrelated diagnostic (Low, confidence: high)

**Claim.** Two `Active Contract Pool` rows sharing a Name with different Paths, both selected in sprint-index, yield two `m.Coverage` entries with the identical ID `"contract-<slug(name)>"`. Nothing upstream dedupes; downstream assumes uniqueness.

**Observable bad outcome.** After both reviewers actually run, the run fails deterministically on every retry. With resume initialized, `initializeReviewResume` persists checkpoints containing the duplicated ID and `SaveFlowState` → `validateReviewStageState` rejects it (`seen[checkpoint.CoverageID]`, review.go:1174-1177), surfacing a cryptic resume-grammar error instead of naming the duplicate catalog entry. Even without that, collection fills slots first-match-only (review.go:564-570 `break`), leaving the twin slot zero-valued and producing a misleading `"coverageId must be ... (got \"\")"` invalid-result diagnostic.

**Trigger/preconditions.** project-index.md with `| auth | contracts/v1/auth.md |` and `| auth | contracts/v2/auth.md |`; sprint-index selects both. Passes every current validator.

**Evidence.**
- `ParseProjectIndex` (internal/project/index.go:44-56): no name/path dedupe, no finding.
- `validateSubset` (internal/sprint/validation.go:20-48): for contracts, path matching is explicitly skipped (line 33) and catalog-side name uniqueness never checked.
- `ParseSprintIndex` (index.go:98-106) rejects only exact name+path duplicates of the selection itself.
- ID construction review.go:314/318; checkpoint build review.go:727-735; slot fill review.go:564-570; grammar rejection enforced via `SaveFlowState` → `ValidateFlowState` (state.go:248).
- Focus mode shares the defect (`wanted`/`retained` maps keyed by ID, review.go:661-701). Notably `reviewCoveragePlan` has zero direct test coverage.

**Counter-evidence searched.** The outcome is fail-closed everywhere — blocked verdict or failed status, never a wrong pass; citations/verdict math unaffected. Requires an index shape no current validator produces warnings for, so it is reachable by a conforming actor.

**Severity/confidence.** Low severity (availability + diagnostic quality, no integrity breach), high confidence.

**Regression test.** Build the index pair above; assert either a preflight finding naming the duplicate selection or distinct coverage IDs. Currently: first `review` runs 2 reviewers, then fails persisting resume state; every later attempt fails identically.

---

## F3. `saveReviewResumeSession` refuses legitimate session updates on checkpoints failed by a *prior* attempt (Low, confidence: high mechanism / medium impact)

**Claim.** The guard `checkpoint.Status == AttemptFailed → return` (review.go:814) was written for same-run late events racing a terminal result (per its comment), but it also matches checkpoints left `failed` by a **previous** attempt. On a resumed rerun, new session IDs emitted before the terminal result are silently dropped.

**Observable bad outcome.** Resume → runtime falls back to a fresh session S2 → crash before the result lands → next rerun resumes the stale S1 (possibly swept store, long poisoned context) instead of S2 — wasted bounded-repair cycles or an immediate reviewer failure forcing a full restart.

**Evidence.** Guard review.go:808-821; terminal-only SessionID persistence in `saveReviewResumeResult` (review.go:823-837); `reviewResumePlan` re-arms sessions for any non-completed status including `failed` (review.go:790). A blocked prior run (some coverages exhausted repairs) is the normal way failed+SessionID checkpoints persist.

**Severity/confidence.** Low (continuity/resilience only; correctness preserved). High confidence in behavior.

**Regression test.** Seed a failed checkpoint with SessionID S1; resumed run whose fake runtime reports session S2 then gets cancelled pre-result; assert persisted checkpoint carries S2 (currently S1).

---

## F4. Review model-source label falsified; effective chain diverges from documented order (Low, confidence: high behavior / medium contract)

**Claim.** `reviewModelSelection` (review.go:1293-1297) takes `executeModelSelection("")` and then unconditionally relabels `Source = "planning.plan_model"` whenever the plan-stage key is configured — even when the model value came from the **execute** stage key or the global runtime config. The effective chain is override → review key → **execute key** → plan key → global, whereas the documented chain (context pack §3; TRD §18.7: stage-specific key for the requested stage, then global) is override → review → plan → global.

**Observable bad outcome.** With `planning.plan_model=A`, `planning.execute_model=B`, no review key: reviewers run under B while review.md, dry-run preview, and metrics record `Model source: planning.plan_model` — provenance is wrong twice over (value provenance and label).

**Evidence.** review.go:1286-1298; execute.go:710-724 (execute key checked before plan; `runtime.config` fallback also gets mislabeled when plan key set).

**Severity/confidence.** Low (misleading recorded metadata; model-selection deviation). High confidence on behavior; medium on the chain-order contract reading.

---

## Minor: finding `id` is the only unbounded attacker-controlled string in the result schema

`reviewResultProblems` bounds summary (4 KiB) and title/detail/action (8 KiB) but checks `id` only for non-empty+unique (review_runtime_validation.go:213). A hostile reviewer output with a multi-megabyte ID renders verbatim into `review.md` Findings/Deviations; if it also triggers a `duplicate-finding-id` diagnostic, the >240-char `attempt.Diagnostics` limit (domain.go:334-338) makes the terminal flow-state save fail, converting a clean blocked outcome into state-write-failure churn. Low severity; add a length bound mirroring the other fields.

---

## Defended non-issues (checked, not reported as defects)

- **Missing reviewer ⇒ blocked verdict vs protocol's `fail`**: documented divergence (pack §9); smoke gate refuses stale/fail/blocked and QA treats blocked as terminal, so gating strength is preserved.
- **Lexicographic severity sort** (blocker<high<info<low<medium): deterministic, no rank-order contract found; verdict computation independent of order.
- **`PromptOnly` findings-save asymmetry** (review.go:410/423): real asymmetry in code, but no production caller ever sets `PromptOnly` (CLI `--prompt` maps to `DryRun`); latent only.
- **Fingerprint stability despite unstable `sort.Slice`**: pre-sort input construction is fully deterministic (ReadDir sorted, map-derived lists sorted before append), and Go's pdqsort is deterministic for identical input sequences — reproducible in practice (fragile, not defective).
- **`LastComplete` restore on failed saves** masking drift: Status-side freshness recomputation (service.go:269-279, verify.go:175-211) independently flips `Stale`/freshness, so downstream gates don't trust the preserved verdict.
- **Snapshot `.complete` trusted on presence**: content identity is keyed by the fingerprint itself; marker content redundant.
- **`ProvisionalVerdict` dead field, `_ = now`**: validated/persisted but never produced; cosmetic.

No changes were made to the target repository or workspace; verification was by source trace, contract texts, and the existing 24-test evidence map (which notably contains no coverage for focus mode, symlinked changed paths, or cross-attempt failed-checkpoint resume).