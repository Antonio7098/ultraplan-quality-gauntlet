Now let me read the core source files.Context pack read. Note it references commit 8eef70f but frozen HEAD is c6f01cf — I'll verify drift as I go. Now reading supporting machinery: runtime wiring, locks, prompt context, publication, flow dispatch.`prepareSharedPromptContext` resolves with `create=false` — confirming direct CLI execute never creates a worktree. Let me trace who calls with `create=true` and read flow/locks/runtime wiring.Found something suspicious: `runtimeRequest` (service.go:1166-1180) force-overrides `req.Provider/req.Model` from the stage config whenever `metadata["stage"]` is set — and execute always sets it. The per-task plan annotation may never reach the actual runtime call. Let me verify against docs and tests.My rg flags mangled output (`-r` = replace, display-only). Let me read the actual docs section cleanly.cli-reference.md documents no per-task annotation — but code+tests implement one (`executeSelectionForTask`, "plan.md task annotation" source). Critical question: does the annotated model reach `req.Provider/req.Model`? Reading the test harness to verify.No test asserts the annotated model reaches the runtime request. Let me check the remaining mechanics: section line math, config defaults for publication, runtime callback semantics, and gitpublish `All` handling.Confirmed a serious race window: `Adapter.StartRun` abandons the event-drain goroutine after only 1s (runtime.go:400) while `req.OnEvent` keeps firing — and execute.go's OnEvent handler mutates `task`/`state` under `sessionMu`, but the post-run classification and terminal save (execute.go:275-316) touch the same objects **without** that mutex. Let me check the remaining wiring (locks, flow, startSprintRuntime) before proving things empirically.`startSprintRuntime` records `req.Provider/req.Model` into metrics — so metrics would also record the wrong model for annotated tasks. Now let me set up an out-of-tree repo copy to prove the top candidates empirically.Fixture pattern understood. Now let me copy the repo at the frozen commit into `/tmp/opencode` and write falsification tests for my top candidates.Writing falsification tests against the copy now.Now writing the verification test file:All candidates confirmed empirically (the two "blocked" assertions confirm the checked-marker guard *does* work — proving the unchecked path in test 2 is the true gap). Now let me verify the OnEvent race with a `-race` test using the real `Adapter`.The race didn't trigger — my fake closed its event stream too early. The real window needs events arriving **>1s after Wait returns** so `StartRun` abandons the drain. Restructuring the fake:Need sustained overlap. Rewriting the fake to pump session events continuously past `StartRun`'s 1s drain grace, guaranteeing concurrent writers:**Data race confirmed** under `-race`, in the exact predicted window: the Adapter's abandoned event-drain goroutine (runtime.go:339 → execute.go:259) races the main flow's unlocked post-run write (execute.go:275). Now checking the startup-reconcile contrast (locks.go) which strengthens the resume-overwrite finding, then the dry-run cache-write detail:Startup reconcile **errors loudly** on malformed run-state (locks.go:64-66) while `--resume` silently discards it — a sharp internal inconsistency. Quick final checks: dry-run cache write and CLI flag parsing.Dry-run test passed (no cache written) — my filename guess may be wrong. Checking actual cache path naming:Cache lives under `root/.ultra/cache/...`, not the sprint dir. Fixing the walk target:Confirmed: `--dry-run` writes the context-pack cache (`<key>.json` under `.ultra/cache/sprint-context/`). Minor doc deviation. Final check on execute CLI arg parsing:`BuildPlanManifest` requires no code-context — confirming the no-worktree path is reachable for legacy workspaces. Verifying the target repo is untouched, then compiling findings:Target repo's new modifications are in QA/workspace files outside my surface and weren't made by me (all my work was in `/tmp/opencode/gauntlet/repo`); my evidence files (`internal/sprint/execute*.go`, `runtime.go`, `git_publication.go`) are unmodified at frozen commit `c6f01cf`. Final report:

---

# Findings: `sprint-execute-resume` fresh-context adversarial review

All findings verified against frozen commit `c6f01cf`. Falsification tests live in `/tmp/opencode/gauntlet/repo/internal/sprint/zzz_gauntlet_*_test.go` (out-of-tree copy); every claimed behavior below was reproduced by execution, three under `-race`.

## F1. Per-task plan model annotation never reaches the runtime transport — wrong model executes while state/prompt/metrics record the annotated one

**Claim.** The inline `<!-- model: provider/model -->` task annotation changes selection-layer bookkeeping only; `req.Provider/req.Model` are always overwritten by the stage config because `runtimeRequest` unconditionally applies `s.runtimeForStage(stage)` whenever `metadata["stage"]` is set — and execute always sets it.

**Observable bad outcome.** An operator pinning a specific model for a task gets the globally configured execute model instead, while `.run-state.json` records `Runtime.Model = "vendor/annotated"`, the prompt tells the agent "Model source: `plan.md task annotation`", and `.runtime-metrics.json` records `model="test/model"` — two contradictory models persisted for one run. Session-reuse gating also makes continuation decisions from the phantom value.

**Trigger/preconditions.** Any executed task carrying an annotation (with any stage/config model present). Reproduced:
```
request provider="test" model="model"  model_source="plan.md task annotation"
recorded state runtime={... Model:vendor/annotated ModelSource:plan.md task annotation ...} status=complete
```

**Evidence & path.** Feature contract: comment at `internal/sprint/execute_plan.go:24-25` ("Model optionally overrides the runtime model … for this task"). Annotation parsed into `taskSelection` (`execute.go:220`, precedence helper `execute_model` path `execute.go:739-744`) → request built at `execute.go:241-249` sets only prompt/metadata/WorkDir/SessionID → `service.go:1166-1180` overwrites `req.Provider, req.Model = splitProviderModel(override.Model)`. Contrast with correct pattern at `code_context.go:294`, which applies an override *after* `runtimeRequest`. No test covers annotated-model-at-transport (only `executeSelectionForTask` struct precedence, `execute_plan_test.go:202`).

**Counter-evidence searched.** No post-hoc Provider/Model assignment anywhere in the execute loop; no alternate transport for annotations; cli-reference.md doesn't document the annotation (so no doc conflict — but the code contract and recorded state are self-contradictory regardless).

**Severity:** high (silent misexecution + false audit/cost trail). **Confidence:** high.
**Regression test:** assert `requests[0].Provider=="vendor" && requests[0].Model=="annotated"` after executing an annotated plan (fails today); fix by applying `splitProviderModel(taskSelection.Model)` to `runtimeReq` after the `runtimeRequest` call.

## F2. Explicit `--resume` silently discards unreadable/malformed/unsupported/legacy run-state and re-executes completed work

**Claim.** `execute.go:177` ignores every `LoadExecuteRunState` error when resuming; combined with fresh-state rebuild and unconditional save at :180, a corrupt (or legacy-v0, or future-schemaVersion) file is destroyed on first persist and its completed/deferred history is lost.

**Observable bad outcomes (all reproduced).**
1. Corrupt JSON + all-unchecked markers + `Resume:true` → **no error**, both tasks re-executed, file replaced: `resume err=<nil> runtime calls 1 -> 2`.
2. Recognized legacy terminal record (`LegacyTerminalExecuteStatus` returns ok) + `--resume` → overwritten with fresh task records, history gone: `legacy file survived=false`.
3. Control proving the gap is the unchecked-marker case: checked `[x]` marker + corrupt state IS blocked ("checked tasks lack execution state") — but execute never ticks boxes itself and prompts instruct only the `[/]` protocol, so real-world interrupted sprints have unchecked markers and hit the silent path. Flow dispatch always passes `Resume:true` (`flow.go:241`), so flow-driven resumes are exposed too.

**Doctrinal contradiction.** Startup reconcile treats the identical condition as fatal: `locks.go:64-66` returns the load error unless it's `ErrExecuteRunStateMissing` or legacy-terminal; sprint-23 requires malformed schemas be "rejected with actionable diagnostics". `--resume` — the operation whose entire purpose is continuity — is the one path that neither rejects nor surfaces.

**Bad outcome concretely.** After disk corruption/manual edit, the operator asks to resume; the tool re-runs already-implemented tasks against a tree that already contains their changes (conflicts/duplicate work/cost) and permanently erases deferral rationales and attempt diagnostics that review gating depends on.

**Evidence.** `execute.go:177-182`; loader classification `execute_state.go:35-67`; legacy recognition `execute_state.go:81-103`; loud-failure contrast `locks.go:46-66`.

**Counter-evidence searched.** No doc grants resume "fresh restart" semantics (pack §7 documents that only for runs *without* `--resume`); no tolerance flag or operator confirmation exists.

**Severity:** medium-high. **Confidence:** high (behavior proven; intent undocumented).
**Regression test:** `--resume` with a malformed/unsupported-schema/legacy-v0 `.run-state.json` must fail with an actionable error (or require explicit reset), preserving the file. Fails today.

## F3. Data race: late `OnEvent` callbacks mutate task/state concurrently with unlocked post-run persistence — can corrupt the durable checkpoint after command return

**Claim.** `Adapter.StartRun` abandons its event-drain goroutine after a 1-second grace (`platform/runtime/runtime.go:385-401`, `case <-time.After(time.Second)`) while that goroutine keeps invoking `req.OnEvent`. Execute's OnEvent handler mutates `task`/`state` under `sessionMu` (`execute.go:253-268`), but everything after `startSprintRuntime` returns — Runtime summary merge (:275), status classification, `CompletedAt`, terminal `SaveExecuteRunState` (:283-316) — touches the same objects **without** that mutex.

**Proof.** `-race` test with the production adapter (`pruntime.NewAdapter`) and a fake agentwrap run whose event stream keeps emitting past `StartRun`'s grace:
```
WARNING: DATA RACE
Read at ... by goroutine 27: Service.Execute.func2() execute.go:259   <- abandoned event goroutine
Previous write at ... by goroutine 10: runtimeSummary() execute.go:641 / Execute() execute.go:275
```

**Observable bad outcome.** Beyond the memory violation: a late event's own `SaveExecuteRunState` (inside the mutex) serializes the whole `state` snapshot; if it lands after the terminal save, last-writer-wins persists a stale image — e.g. `Runtime` replaced by `{SessionID,...}` losing RunID/usage merge, or (for a crash-timed window) status regressed toward `running`, which crash recovery would then misclassify as stale-running→failed despite a successful turn. In multi-task queues, late events for task N overlap task N+1's saves.

**Trigger.** Agent runtime events lagging >1s behind run completion (large terminal output / slow stream close — exactly the condition the 1s grace exists for).

**Evidence.** `runtime.go:332-344` (goroutine calls `req.OnEvent`), `runtime.go:400` (abandon), `execute.go:253-268` vs `275-316`.

**Severity:** high (race + durable-state corruption of the exact artifact the resumability contract depends on). **Confidence:** high.
**Regression test:** the pumping-event `-race` test above; fix by extending `sessionMu` coverage over post-run mutation+saves, or guaranteeing OnEvent quiescence in `Adapter.StartRun` before returning.

## F4. Minor: `--dry-run` writes state despite documented "no state write"

`Execute` prepares the shared prefix with `persistCache=true` *before* the DryRun branch (`execute.go:150-154`), so `sprint p s execute --dry-run` writes `<root>/.ultra/cache/sprint-context/<p>/<s>/<key>.json` (reproduced). cli-reference.md:317 says dry-run runs "without a runtime, durable acceptance, or state write"; `PromptExecute` deliberately uses `persistCache=false`. Disposable content-addressed cache ⇒ negligible impact, but it's a doc-contract deviation with an easy fix. Severity low, confidence high.

## F5. Publication `All:true` has no dirty/authorship scoping — sweeps unrelated WIP when no sprint worktree exists

`resolveSprintTarget(create=false)` everywhere in the execute path (`execute.go:360`, `prompt_context.go:106`) means direct CLI execute on a pre-code-context workspace (no `.workspace.json`; `BuildPlanManifest` doesn't require code-context — `plan.go:24-44`) sets agent `WorkDir` and publication root to the **user's live checkout**. On full resolution, `publishExecuteStage` commits `All:true` (`publication.go:63`) — including unrelated pre-existing uncommitted user changes — under "ultraplan: sprint p/s complete execute", optionally pushing. `createSprintWorkspace` explicitly refuses dirty sources (`execute_target.go:145-151`), so the protection exists upstream but is bypassed whenever the worktree is absent. Default `git.stage_completion: off` (config.go:190) gates exposure. Severity medium, confidence medium (opt-in config; possibly accepted tradeoff, but undocumented).

## Defended non-issues (searched, counter-evidence found)

- **Agent-emitted "diagnostic-only" warning marks complete without artifacts** (`execute.go:307-309`): completion evidence is inherently runtime-provided; no additional trust transition is created.
- **Deferred counts as resolved for publication** and **fresh-rebuild-without-`--resume` clobbering state**: both are explicit documented semantics (cli-reference.md; pack §7).
- **PlanLine arithmetic**: `sectionStartLine` (heading line) + `i+1` matches `markdownSections` body indexing — verified correct.
- **`uniqueSorted` lexicographic ordering / "test" substring evidence heuristic**: affect IDs consistently and only via identity text already subject to change.
- **Continuing a failed task's recorded session on resume**: deliberate reuse-gate design (target+model equality only).
- **Concurrent human plan edit misclassifying a successful turn as `invalid-deferral`**: fails closed, retryable; lease doctrine excludes human editors.

**Recommended fix order:** F3 (corruption) → F1 (wrong model) → F2 (resume data loss) → F5 → F4.