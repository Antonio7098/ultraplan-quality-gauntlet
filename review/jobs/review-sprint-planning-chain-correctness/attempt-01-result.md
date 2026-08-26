The working tree matches the frozen commit for all sprint-chain files (post-freeze changes touch only QA files; the skills.go edit is uncommitted and out of scope). Reading the primary sources now.The two `flowFailedStages` variants confirm mixed semantics. Now let me prove the suspected defects with probe tests, using a Go overlay so the frozen repo stays untouched.Probe 2 confirmed — dry-run wrote fabricated state. Probe 3 needs a requirements.md; let me fix and see probe 1's output fully.All three probes reproduce. Continuing the review — checking remaining test coverage and a few more candidates before concluding.Two probes confirmed. Let me also prove the amplified impact: the package-level `flowFailedStages` fabricates priors-Complete and erases existing failure records in non-dry runs too.Probe 4 passed for the wrong reason — the Failed record was likely downgraded to Missing rather than fabricated Complete. Tightening the probe to see exact outcomes:Probe 4 confirmed precisely: the package-level `flowFailedStages` resets downstream stages to `missing`, destroying a persisted Failed record. Let me verify the untouched baseline is green, then check one last counter-evidence item (whether any test intentionally documents this rewrite).Repo left untouched (only the pre-existing unrelated `skills.go` edit remains). Baseline `go test ./internal/sprint/` green. Final report:

---

# Findings — `sprint-planning-chain` (correctness lens)

All three defects below were reproduced with throwaway Go tests executed against the frozen tree via `go test -overlay` (probe files kept outside the repo, deleted afterward; target commit `c6f01cf` content verified identical to working tree for this surface).

## F1 — Dry-run planning flows persist fabricated failed-state (invariant violation)

**Severity:** medium · **Confidence:** high (proven)

**Claim.** Two early-failure paths call `SaveFlowState` without the `if !req.DryRun` guard every sibling path in the same functions has:
- `internal/sprint/service.go:841` — `FlowTechnicalHandbook`, "selected evidence validation failed" branch
- `internal/sprint/service.go:919` — `FlowReasoning`, requirements empty/placeholder branch

**Observable bad outcome (proven).**
- `sprint p s flow --to technical-handbook --dry-run` where sprint-index selects an unreadable evidence report **rewrites `flow-state.json`**: `technical-handbook: failed` with error text persisted, `sprint-index` demoted `ready→complete`, all `lastRunAt` timestamps dropped (verified before/after byte diff).
- `sprint p s flow --to reasoning --dry-run` with placeholder markers (`TODO`) in requirements.md **creates `flow-state.json` from nothing**, fabricating `requirements`…`area-reasoning` all `complete` and `reasoning: failed`.

**Trigger/preconditions.** CLI dry-run (no `--yes` needed, no lease acquired — documented invariant "Dry-run performs no writes and acquires no lease"); handbook: unresolvable selected evidence or unparseable sprint-index; reasoning: requirements.md containing `todo`/`tbd`/`{{var}}`/`<placeholder`. Both reachable through `Service.Flow` (CLI) and durable web/TUI stage operations.

**Path.** `Flow`(DryRun) → `runFlowStage` → `FlowTechnicalHandbook`/`FlowReasoning` → unguarded `_ = SaveFlowState(...)` → `saveFlowStateWithHooks` (or DB mirror row when productstate exists — so the fabrication also lands in the authoritative DB).

**Controls/counter-evidence searched.** Sibling guards at service.go:656, 664, 672, 749, 758, 830, 847, 937, 1266, 1274, 1402, 1410, 1418 show intent; `failCodeContext` guards at code_context.go:435. Existing tests enforce the invariant only on happy paths (`TestPromptPreviewAndFlowDryRunAreRuntimeFree`, app test lines 352–371), so these branches were never exercised. No documentation permits dry-run state writes.

**Aggravator.** The write happens with no mutation lease held, so a concurrent real flow can race it; and the package-level `flowFailedStages` (see F2) makes the written record *fabricated*, not derived.

**Regression test.** Mirror of probe 1/2: fixture with completed code-context + valid sprint-index referencing a missing evidence file; assert `flow-state.json` bytes unchanged after `FlowTechnicalHandbook(DryRun:true)`; fixture with `TODO` in requirements.md asserting no state file after `FlowReasoning(DryRun:true)`; plus an app-level variant extending the existing dry-run sequence. Fix: add the standard `if !req.DryRun` guard at both sites.

## F2 — Same-named `flowFailedStages` pair erases persisted failure records

**Severity:** low-medium · **Confidence:** high (proven)

**Claim.** `flow.go:385` (package func) and `service.go:1214` (method) share a name with different semantics and are mixed inside single stage functions (e.g., `FlowSprintIndex` uses the method at :655/:671 but the package version at :663/:682/:690/:696/:713/:720). The package version rebuilds from `emptyPlanningStageStates`: priors forced `complete`, everything after the target reset to `missing` — discarding persisted `Failed` records that `DeriveStages`/the method variant explicitly preserve as blockers.

**Observable bad outcome (proven).** With flow-state recording `plan: failed, error:"plan generation failed previously"`, a subsequent `FlowSprintIndex` run whose runtime errors at `StartRun` persists `plan: missing, error:""` (full stage dump captured). The recorded blocker reason, timestamp, and Failed marker are silently destroyed on any later-stage failure via FlowRequirements/FlowSprintIndex/FlowPlan/FlowTechnicalHandbook/FlowReasoning failure paths.

**Bad outcome consequence.** `sprint status` loses the diagnostic error and blocker evidence; the "prior Failed preserved as blocker" behavior implemented in `DeriveStages` (and used by the method variant, code_context.go:428) is defeated precisely on the failure paths that write state most often.

**Counter-evidence searched.** No test documents the reset as intended; no comment claims stale-record pruning; `ValidateFlowState` accepts the rewritten record so nothing detects it.

**Regression test.** Probe 4 as written (assert `plan` stays `StatusFailed` with original error after a failing sprint-index run). Fix: route all failure paths through the deriving method (or make the package function take prior state into account).

## F3 — Derived status masks failed/partial area-reasoning as complete

**Severity:** low-medium · **Confidence:** high (proven)

**Claim.** `DeriveStages` (service.go:1513-1518) sets `StageAreaReasoning = complete` whenever ≥1 non-empty `.md` exists under `reasoning/`, ignoring both a prior `Failed` record and the manifest's selected-template count — unlike the `requirements`/`code-context` branches immediately above, which explicitly respect prior `Failed`.

**Observable bad outcome (proven).** Prior state `area-reasoning: failed` + one of two selected area files present ⇒ `sprint status` reports `complete`; because `Status()` saves the refreshed state by default, the failure evidence is permanently overwritten. A partially generated multi-area stage reads as done to status consumers (CLI/TUI/web `status --json`). Stage gating itself stays safe (`flowStageAlreadyValid` uses content validation, which still fails closed), so impact is misleading success plus state-authority corruption, not skipped work.

**Trigger.** Any mid-loop area-reasoning failure after ≥1 entry succeeded (real chain path, service.go:1316-1326), or manual deletion of one area artifact after completion.

**Regression test.** Probe 3: seed prior Failed state, create one of two area files, assert `Status().Stages[area-reasoning].Status == failed` (currently returns `complete`). Fix: require the selected template set to be present (manifest count) and honor prior `Failed`, mirroring the requirements/code-context branches.

---

# Defended / non-issues (hypotheses raised and disproved)

- **Shared-prefix budget near boundary**: frame overhead above `available` can fail a selection that "fits" — but the resulting `budget_exceeded` is accurate and fail-closed; exact-boundary acceptance/rejection is test-enforced.
- **`readSharedLineRanges` accounting**: per-fragment budget check bounds `out ≤ budget`; line counting correct incl. CRLF and missing final newline; range-bounds checked post-read.
- **TOCTOU controls**: inode (`SameFile`) + size + mtime + canonical `EvalSymlinks` re-checks are coherent; residual same-inode/same-mtime rewrite window requires deliberate forgery and yields no privilege.
- **Context-pack cache trusts its own payload** (identity digests don't authenticate prefix provenance): exploitation requires arbitrary workspace write access — the same capability already suffices to edit `requirements.md`/`flow-state.json` directly, and non-code-context planning agents are deliberately not sandboxed; no new primitive introduced. Heuristic post-hoc validation is the accepted trust model.
- **`containsPlaceholder` substring heuristics** (`todo`, `tbd`) can false-positive on legitimate prose — fail-closed annoyance with bounded repair, consistent with the product's conservative-validator posture; no wrong-success path.
- **Session checkpoints**: compatibility deliberately ignores variant and prompt checksum (documented in-code); lost-session restart keyed on "session not found" can at worst redundantly rerun an idempotent stage; continuation insertion sits after the cache boundary so the stable-prefix contract holds.
- **`promoteCodeContext`**: rename+rollback verified correct including restore-failure reporting and temp cleanup; post-crash empty-artifact residue self-heals via `codeContextPrerequisite`.
- **`insertStageContinuation`, `stageSessionKey` dimension keys, area-entry skip/resume loop, repair bounding (product 1 + agentwrap 2 with fresh fallback)**: traced, no defect found.
- **`.stage-sessions.json` rename-without-fsync**: implementation-defined format (context-pack unknown #1); worst case loses resume convenience, never correctness.

The two dry-run sites in F1 are one-line guards; F2 is a consolidation fix; F3 is a branch-local derivation correction — each with a ready-made regression test from the probes described above.