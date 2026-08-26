Let me quickly verify the boundary-marker injection mechanics with a test in the scratch copy:All verification work is done. Baseline suite is green at the frozen commit (the two commits since `c6f01cf` touch only `qa.go`; the working-tree edit is `internal/workspace/skills.go`, both outside this surface). I reproduced the headline defect with executed tests in a scratch copy at `/tmp/opencode/repro/ultraplan-go` (target repo untouched) and falsified one strong initial hypothesis (recursion — see non-issues).

# Findings: `sprint-planning-chain` (verification/operability lens)

## F1 — P2 · Confirmed · `--dry-run` writes durable flow-state and fabricates stage statuses on two prerequisite-failure paths

- **Claim:** Two failure paths call `SaveFlowState` unconditionally, violating the documented dry-run no-write invariant, and use the package-level `flowFailedStages`, which marks every stage before the target `Complete` without evidence.
- **Bad outcome:** `sprint flow --to technical-handbook --dry-run` (or `--to reasoning/area-reasoning --dry-run`) against a sprint with a prerequisite defect rewrites `projects/<p>/<s>/flow-state.json`: target stage persisted `Failed`, all prior stages persisted `Complete` — including `requirements: complete` while its content is invalid (visible verbatim in my repro output). Durable governed state is mutated by an operation whose help says "Dry-run prints planned inputs without mutation" (sprint_commands.go:1812) and whose invariant has a test precedent only for code-context (code_context_test.go:217–222).
- **Trigger/preconditions:** Dry-run where (a) selected evidence fails `BuildHandbookManifest` (e.g., missing evidence file), or (b) `requirements.md` is empty/contains a placeholder substring ("todo"/"tbd").
- **Evidence:** service.go:838–842 (`FlowTechnicalHandbook`) and service.go:916–920 (`FlowReasoning`) lack the `if !req.DryRun` guard present in every sibling path (service.go:656–658, 749–751, 1266–1268, 1402–1404); fabricated statuses come from flow.go:385–396. Repro tests `TestDryRunHandbookPrereqFailureMustNotWriteState` / `TestDryRunReasoningRequirementsFailureMustNotWriteState` both fail on the frozen commit; full suite otherwise green.
- **Controls/counter-evidence:** Sibling guards show the intended pattern; `failCodeContext` correctly gates on `req.DryRun` (code_context.go:435). No test covers these two paths — that is why the defect survived.
- **Fix direction + regression test:** Gate both saves on `!req.DryRun` (and prefer `s.flowFailedStages` for truthful derivation). Regression: the two repro tests above (assert `flow-state.json` absent after dry-run prerequisite failure).

## F2 — P3 · High · `--model`/`--variant` are silently ignored for every flow target except code-context

- **Claim:** `parseSprintFlowArgs` accepts `--model/--variant` for any target (sprint_commands.go:1110–1121), but `FlowRequest.ModelOverride/VariantOverride` are consumed only by `FlowCodeContext` (code_context.go:293–305); other stages read only config defaults plus `StageOverrides` (service.go:1166–1181).
- **Bad outcome:** `sprint flow --to plan --model vendor/expensive` runs plan on the configured default model with no error, warning, or metadata indication; automation believing the override applied gets different cost/behavior than requested. `--stage-model plan=…` works, proving the wiring exists and `--model` is simply unwired. The TUI compensates by setting both fields (operation_runner.go:176–185), confirming the intended mechanism.
- **Counter-evidence:** Help text documents `--model` only on the code-context usage line (sprint_commands.go:1802) — mitigating, not excusing: accepted-but-inert flags on other targets are a trap.
- **Fix/test:** Map `--model/--variant` into `StageOverrides[req.To]` (matching TUI), or reject them for targets where they have no effect. Regression: capture runtime request for `flow --to plan --model X` and assert provider/model.

## F3 — P3 · Mechanism confirmed · Agent-authored artifacts can break the exactly-one-stage-boundary prompt contract

- **Claim:** No validator rejects the literal product markers in agent-authored `requirements.md`/`code-context.md`. A code-context embedding `<<< ULTRAPLAN STAGE-SPECIFIC INSTRUCTIONS BEGIN >>>` passes `ValidateCodeContextContent`, and the rendered prefix then contains two boundaries.
- **Bad outcome (demonstrated):** With such content, `explainComposedPrompt` splits at the *first* (injected) marker (prompt_bundle.go:97) → `SharedPrefixBytes`/`CacheBreakpointBytes`/digests fed into `pruntime.CacheDirective` and `.runtime-metrics.json` describe the wrong stable/volatile split; `insertStageContinuation` (session_state.go:174) inserts the resume instruction *inside* the untrusted artifact frame instead of after the governance boundary. Probe test showed instruction at offset 881 landing before/inside injected text, with 2 boundary occurrences in the prefix. The "exactly one boundary" property is asserted only by tests using clean fixtures (prompt_context_test.go:28–33).
- **Trust boundary:** agent-authored artifact → product prompt structure (§6 of context pack). Low likelihood (authoring sessions never see the literal), zero-detection when it happens.
- **Fix/test:** Reject the boundary and direct-input markers in `ValidateRequirementsContent`/`ValidateCodeContextContent` (like the existing fence check, code_context.go:69); regression: artifact containing the marker must produce a validation finding.

## F4 — P3 · High · Context-pack cache identity omits renderer/governance version → stale prefixes survive upgrades

- **Claim:** Cache key = digest(requirements)+digest(code-context)+clean(target) with a hardcoded `"sprint-context-v1"` salt (context_pack.go:34–40); the shared instructions text and renderer logic are not part of identity, and `contextPackSchemaVersion` (line 16) is decoupled from the key. Packs have no TTL; pruning runs only on save (context_pack.go:88).
- **Bad outcome:** After upgrading ultraplan-go with revised `sharedPromptInstructions`/framing, every existing sprint keeps serving the old cached prefix to all downstream stages (prepareSharedPromptContext prefers cache, prompt_context.go:110–112). Governance text drifts silently; digests "verify" nothing because they hash the stale payload. Byte-stability tests can't catch this (in-process, empty cache).
- **Fix/test:** Mix a renderer-version constant into `contextPackIdentity` (bump with any framing change). Regression: render → mutate instruction constant identity → assert cache miss.

## F5 — P3 · High mechanism · One torn `.stage-sessions.json` permanently and silently disables checkpointing

- **Claim:** Checkpoint writes are temp+rename **without fsync** (session_state.go:62–85; contrast the fsynced `atomicWriteFile` used for flow-adjacent state, smoke.go:714–731). Every writer is read-modify-write that aborts on decode error: `persist` returns without saving (session_state.go:131–133), continuation lookup fails open (loadErr ignored, :109), clears return errors that all callers discard (`_ =`). Nothing ever repairs or removes a corrupt file.
- **Bad outcome:** After a power-loss-torn write, session IDs are never again persisted or resumed for that sprint: interrupted long stages restart from scratch with no warning, no diagnostic, no self-heal — indefinitely, until manual deletion. No test covers corrupt-file behavior (session_state_test.go covers only valid states).
- **Existing controls:** Strict decode prevents acting on garbage (good); fresh-run fallback keeps stages functional (fail-open).
- **Fix/test:** fsync before rename; treat decode failure as "recreate empty state" (or warn via `result.Warnings`). Regression: write garbage checkpoint → run stage → assert checkpoint persistence works afterwards.

## F6 — P3 · Medium · CLI `sprint status` performs unleased `SaveFlowState` refreshes that can race away failure records

- **Claim:** `Status()` derives stages from a snapshot loaded earlier and saves refreshed state whenever `statusWrites` (service.go:263–295); the CLI status command uses a writing service (sprint_commands.go:88, 95) and takes no mutation lease, unlike flow/execute/review/smoke.
- **Bad outcome:** Interleaving: status loads pre-failure state (T0) → flow persists `Failed`+error+`LatestOutcome=cancelled/cleanup_uncertain` (T1) → status saves derived state (T2) → the only durable record of the failure is erased; surfaces show Ready/Missing for a stage that just failed. Web/TUI explicitly opt out via `WithoutStatusWrites` (usecases.go:131, sprint_usecases.go:1012) — CLI deliberately doesn't, inheriting the race.
- **Counter-evidence checked:** `DeriveStages` faithfully preserves prior Failed blockers (state preserved when loaded *after* the write), so steady-state refreshes are safe; only the concurrent window loses data. Consequence limited to diagnostics/state truth, not gating.
- **Fix/test:** Make CLI status read-only (join web/TUI) or take the lease around the refresh. Regression: concurrent status-during-failure interleaving test asserting Failed survives.

## F7 — P3 · High · TRD-listed flow options `--from`, `--force`, `--no-skip` are rejected

- **Contract:** ultraplan-workspace TRD §18.8 (~L2008–2028) lists `--from`, `--force`, `--no-skip` among flow options (CURRENT-CONTRACT per pack §9); `parseSprintFlowArgs` returns "unsupported argument" for them (sprint_commands.go:1144–1146). In-repo help omits them, and no deferral is recorded in the repo — the mismatch surfaces only against workspace docs.
- **Outcome:** Scripts/operators following the authoritative spec get ExitUsage. Fix direction: implement or record the deferral next to the parser.

## F8 — P3 · Medium (intent) · `FlowStage` lacks the already-valid skip/publish/cleanup applied by `Flow`

- **Claim:** `Flow` skips validator-valid stages, clears their checkpoints, and republishes (flow.go:128–158); `FlowStage` (flow.go:189–224) always invokes the stage runtime. Single-stage TUI/web operations therefore regenerate and overwrite validated artifacts (new plan.md/reasoning content) where the CLI cumulative flow would no-op.
- **Evidence:** Only test coverage of `FlowStage` is the conflict boundary (code_context_test.go:325); the doc comment addresses only "not scheduling earlier stages," not skip semantics. If regeneration is intended for the UI button, it is undocumented and untested; if not, it's an expensive surprise.
- **Fix/test:** Either document/pin rerun semantics or apply the same validity gate; regression: `FlowStage` on a complete stage asserts expected call count.

---

## Defended non-issues (hypotheses raised and disproven)

- **Infinite recursion in `s.flowFailedStages`** — initially read as self-recursion on `ReadArtifacts` error; actual code falls back to the package-level function (service.go:1214–1218). Verified empirically with a permission-broken `reasoning/` dir: returns promptly. (The fallback's status fabrication is subsumed by F1's path analysis; on its own it marks only stages that already passed prerequisite gates.)
- **Session deletion is fake-confidence** — `Adapter.DeleteSession` is capability-gated and the OpenCode adapter doesn't support it, so cleanup is a graceful no-op remotely; local runtime stores are still pruned (72h/2GiB, startSprintRuntime). Documented in adapter comments; bounded consequence.
- **Frozen source evidence going stale after repository/execute changes** — intentional and contract-aligned (context_pack.go:18–20 comment; TRD byte-stable-prefix requirement). Cache freezing is the design, not a bug (renderer-version omission in F4 is the actual gap).
- **Residual TOCTOU window (same-size/same-mtime swap)** — inherent limitation; the layered Lstat/EvalSymlinks/SameFile/size+mtime checks detect the realistic replacement cases and are test-pinned.
- **Budget framing under-reservation** (reader budget excludes frame text, prompt_context.go:196 vs 213) — fails closed with `budget_exceeded`; conservative direction, cosmetic edge only.
- **Variant-ignoring checkpoint compatibility and rerun-after-failure continuing the old session** — deliberate, commented (session_state.go:87–92), test-pinned; provider/model/workDir still gate.
- **`stageSessionKey` collision risk / area-entry checkpoint scoping** — dimension keys correct; prefix-based clear covers `area-reasoning:::<name>` keys.

Strongest result: **F1 (confirmed, reproduced)** directly contradicts the surface's core "dry-run performs no writes" invariant; F2/F5 are cheap-to-fix operator traps; F3/F4 are silent-integrity gaps in the prompt-governance machinery this surface exists to guarantee.