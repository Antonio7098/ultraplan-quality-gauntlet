Now the core service file and remaining chain files:Let me verify the suspected unguarded dry-run writes precisely:Both repros confirm the defect. Cleaning up and checking gitpublish tolerance for missing/stale paths:Baseline is green; all repro tests were removed after verification. Here is the review report.

---

# Findings — `sprint-planning-chain` (fresh-context adversarial review)

## F1. Dry-run persists fabricated failed/completed flow-state (contract violation; reproduced)

**Claim.** Two prerequisite-failure branches call `SaveFlowState` without the `!req.DryRun` guard their sibling branches have, and they build state with the package-level `flowFailedStages` helper, which fabricates `complete` statuses for every stage preceding the target regardless of what actually ran.

**Instances**
- `internal/sprint/service.go:838-843` (`FlowTechnicalHandbook`, "selected evidence validation failed") — unguarded; compare the guarded placeholder branch at :828-834.
- `internal/sprint/service.go:916-921` (`FlowReasoning`, requirements empty/placeholder) — unguarded; same function dispatches for both `--to reasoning` and `--to area-reasoning`.

Every other pre-runtime failure site guards the write (`service.go:656, :665, :673, :750, :759, :938, :1267, :1275, :1403, :1411, :1419`) or sits after the dry-run return; `failCodeContext` guards internally (`code_context.go:435`).

**Observable bad outcome (reproduced on frozen commit).** `sprint flow p s --to technical-handbook --dry-run` on a sprint whose sprint-index selects a missing evidence report writes `flow-state.json` recording `requirements/code-context/sprint-index = "complete"` (artifacts do not exist) and `technical-handbook = "failed"`. Same for `--to reasoning --dry-run` with a placeholder `requirements.md`: five never-run stages recorded `complete`. The CLI help promises "Dry-run prints planned inputs **without mutation**" (`sprint_commands.go:1812`), matching the enforced invariant elsewhere (`sprint_index_test.go:67-70`, `code_context_test.go:217-222`). On DB-authoritative sprints the fabrication lands in the mirror while the all-terminal JSON checkpoint is withheld (`state.go:225-228, :233-240`), so `LoadFlowState`'s preferred authority diverges from the JSON file until a real stage saves again.

**Aggravator (same root).** The plain helper `flowFailedStages` (`flow.go:385-396`) marks all pre-target stages `StatusComplete` unconditionally, unlike the artifact-derived method `Service.flowFailedStages` (`service.go:1214-1229`). It is used at ~40 non-dry failure sites, so e.g. a reasoning-stage failure in a no-templates sprint rewrites persisted `area-reasoning: skipped` → `complete`; the method's own fallback (`service.go:1216-1218`) drops to this helper on `ReadArtifacts` error.

**Trigger/preconditions.** Any of: dry-run flow to handbook with invalid selected evidence; dry-run flow to reasoning/area-reasoning with placeholder/empty requirements; any later-stage failure in a sprint with skipped stages; `ReadArtifacts` error during a failure path.

**Controls searched (counter-evidence).** No downstream gate consumes raw fabricated stage statuses unsafely: `codeContextPrerequisite` (`code_context.go:237-255`) additionally requires a valid artifact, and `DeriveStages` re-derives display from artifacts — which is why the defect survives the existing test suite. That mitigation limits, but does not remove, the damage: the authoritative record is falsified and the documented dry-run contract broken.

**Severity:** medium-high. **Confidence:** high (empirically reproduced; both instances).

**Regression test.** Mirror `TestPromptPreviewAndFlowDryRunAreRuntimeFree`: (a) handbook dry-run with a missing selected evidence report must leave `flow-state.json` absent/byte-identical; (b) reasoning dry-run with placeholder requirements likewise; (c) assert `flowFailedStages` preserves a persisted `skipped` status instead of writing `complete`.

## F2. Unlocked concurrent `Status()` write can revert a just-completed stage's durable outcome

**Claim.** `Service.Status` (`service.go:229-295`) loads state, derives stages, then blindly re-saves (`statusWrites` defaults true, `service.go:76`; CLI status uses it) — with no mutation lease and no compare-and-swap, while flows hold only the sprint-scoped lease among themselves. Between its reads and the atomic rename there is a multi-step window (artifact snapshot, project-index parse, docs listing).

**Observable bad outcome.** `flow --to plan` completes code-context (artifact renamed, `SaveFlowState` writes `code-context: complete`); a concurrently running `sprint status` that snapshotted before the rename then renames its stale derivation over it. Persisted completion evidence vanishes, so `codeContextPrerequisite` (`code_context.go:245-254`) fails on the next stage: downstream planning blocks or code-context re-runs a full agent generation. On DB-authoritative sprints the stale write goes to the mirror (mid-flow states are not all-terminal, so no JSON checkpoint softens it).

**Controls searched.** `acquireMutationContext` markers cover flow/execute/review/smoke/verify only; `WithoutStatusWrites` exists for read-only surfaces but CLI/TUI status intentionally writes (`service.go:79-85`). No fsync-guarded CAS anywhere in `saveFlowStateWithHooks`.

**Severity:** medium-low. **Confidence:** medium (deterministic mechanism; requires process interleave).

**Regression test.** Race harness: loop `Status` in a goroutine while `FlowCodeContext` completes; assert final `LoadFlowState` retains `code-context: complete` (currently flaky/fails under `-race` stress). Fix direction: take the sprint lease for status writes or skip the write when derived state equals loaded state.

## F3. Boundary-exact code-context artifact deterministically fails after candidate persistence (off-by-one; reproduced)

**Claim.** `ValidateCodeContextContent` caps at `len(content) > maxCodeContextBytes` (65536, `code_context.go:32, :40`). `FlowCodeContext` validates the trimmed runtime content, then persists `content+"\n"` (`code_context.go:355-359`) and mandatorily re-validates the disk bytes (`:360-372`). A document of exactly 65536 bytes passes pre-write and fails the round-trip (`65537 > 65536`), failing the stage with "exceeds the output budget" despite a compliant model response.

**Evidence.** Pure-function repro: 65536-byte valid document → 0 findings; identical bytes + `\n` (exactly what is written) → budget finding.

**Counter-evidence searched.** No trim occurs between write and re-read; the repair loop cannot fix it because the regenerated output gets the same `+"\n"` append. Only outputs of length exactly 65536 (post-trim) are affected — rare but deterministic.

**Severity:** low. **Confidence:** high (reproduced).

**Regression test.** Feed `FlowCodeContext` a runtime returning a exactly-`maxCodeContextBytes` valid document; expect promotion success (currently fails). Validate the candidate bytes, not `content`, before choosing to append.

## F4. Sprint-context cache serves superseded governance bytes across binary upgrades

**Claim.** Cache identity is `sha256("sprint-context-v1\x00"+reqDigest+ccDigest+targetDigest)` (`context_pack.go:34-39`) — it excludes any renderer/prompt version. `prepareSharedPromptContext` prefers a cache hit over live rendering unconditionally (`prompt_context.go:110-112`); pruning keeps the 8 newest by mtime (`context_pack.go:88`), so packs persist indefinitely while artifacts stay unchanged.

**Observable bad outcome.** After upgrading ultraplan-go with modified `sharedPromptInstructions`, framing text, or templates, every downstream agent-backed stage silently receives the previous binary's governance prefix (only boundary-constant changes self-invalidate via the `HasSuffix` check, `context_pack.go:64`). Staleness is invisible: digests all verify because they hash current inputs and the stored payload self-consistently.

**Counter-evidence.** The cache is commented "disposable acceleration layer" and failures fall back to live render — but nothing disposes it on upgrade, and the served bytes are the governance text agents obey.

**Severity:** low. **Confidence:** high (mechanism certain; consequence materializes on upgrade).

**Regression test.** Render/save a pack, change `sharedPromptInstructions`, render again with same artifacts; assert the new instructions reach the prompt (fails today). Mix a renderer version into `contextPackIdentity`.

## F5. Session-loss detection keyed on free-text "session not found" lets model-authored output force a full stage restart

**Claim.** `planningSessionNotFound` (`session_state.go:158-171`) substring-matches `"session not found"` across `result.TerminalOutput` and error details. It is evaluated whenever a checkpointed continuation ran (`session_state.go:146`), including fully successful runs.

**Observable bad outcome.** A continued run whose transcript echoes the phrase (e.g., narrating the earlier interruption) clears the checkpoint and re-runs the entire stage from scratch with the original prompt — duplicate provider cost, and the final artifact is whichever run validates last. Trigger surface includes agent-controlled terminal text. The adapter-side twin (`opencode.go:140-148`) matches only SDK error details, which is fine; the product-side scan of successful output is the loose half. Bounded to one extra run.

**Severity:** low. **Confidence:** high on mechanism, low-medium on occurrence.

**Regression test.** Continuation returning `Status:"success"` with the phrase inside `TerminalOutput` must not trigger the fresh-restart branch (restarts today).

---

# Defended non-issues (investigated, counter-evidence found)

- **Shared-prefix budget/TOCTOU/containment** (`prompt_context.go`): available-byte math includes closer+boundary at both check points; component-wise Lstat symlink rejection, EvalSymlinks containment, open-handle `SameFile` before/after, size+mtime equality verified sound; exact 512 KiB acceptance/rejection is test-enforced. Residual post-final-stat rename race is inherent to the design and fails closed on next composition.
- **`promoteCodeContext` rollback** (`code_context.go:457-506`): pre-rename ctx check, prior-bytes restore via fsynced temp, malformed-prior-state preservation — correct (test :496).
- **`SaveFlowState` record preservation and DB-authoritative checkpoint gating** (`state.go:201-240`): Review/Smoke/QA carried forward; withholding non-terminal JSON checkpoints is coherent given DB preference in `LoadFlowState`. Intermediate gitpublish commits can carry stale `flow-state.json` bytes, but no gate consumes the published copy.
- **Session checkpoints**: dimension-scoped keys correctly isolate area entries (`stageSessionKey`); variant-blind compatibility is a documented, bounded design choice; corrupt `.stage-sessions.json` degrades to fresh runs rather than blocking.
- **Worktree lifecycle** (`execute_target.go`): clean-source-tree requirement, baseline pinning, git-common-dir + branch identity on reuse, record-write failure removes worktree and branch — sound.
- **`FlowStage` regenerating an already-complete stage**: deliberate rerun-with-atomic-replace semantics, prerequisites still validated per stage.
- **Uncapped direct-input packets** (`direct_inputs.go:184-186`): explicit, documented delegation of context limits to the provider; full-copy ordering is canonical and tested.
- **`explicitlyNoReasoningTemplates` whole-file substring vs section-scoped `ParseSprintIndex.NoTemplates`**: divergence only skews derived display in degenerate documents; gating re-validates manifests, so no concrete failure — dropped as cosmetic.