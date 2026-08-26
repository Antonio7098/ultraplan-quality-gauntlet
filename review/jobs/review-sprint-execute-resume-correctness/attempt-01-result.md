Now let me build an empirical probe outside the target tree. I'll copy the repo to `/tmp/opencode` and add scratch tests there (target stays untouched).Finding #1 empirically confirmed. Now the late-OnEvent race probe under `-race`:Race confirmed at execute.go:264 vs execute.go:327. Now checking whether `PlanFingerprint` is ever actually compared anywhere (staleness semantics):Baseline suite + resume-path probes (the pack notes resume internals have no direct tests):R2 failure is my fixture's fault (I seeded a mismatched target path, which correctly triggers the target-changed guard). Fixing the probe to use the resolved target:Final counter-evidence checks for F1 (help text wording) and cleanup verification that the target tree is untouched:The target working tree just became dirty with unrelated in-progress changes (from parallel activity — not mine; all my work went to `/tmp/opencode`). Verifying my reviewed surface files still match the frozen commit, both in the target and in my scratch copy:Review complete. Target and workspace untouched (verified); all experiments ran against a byte-identical copy of frozen `5d54525` in `/tmp/opencode/ugo-scratch`. Concurrent edits appearing mid-review in the target tree touch only review/qa/smoke surfaces — none overlap this job's files (verified byte-identical to frozen commit).

# Findings — `sprint-execute-resume`

## F1 — Per-task model annotations and direct-execute `--model` never reach the runtime request; state records a phantom selection

**Claim.** `Service.Execute` resolves an effective model per task (`executeSelectionForTask`, internal/sprint/execute.go:220) but never applies it to `runtimeReq.Provider/Model`. `runtimeRequest` sets Provider/Model solely from base runtime config and the stage override (internal/sprint/service.go:1123, 1172-1175); production wiring maps `StageExecute → planning.execute_model` (internal/app/sprint_commands.go:1019-1022, 779). Nothing else mutates the request before launch (execute.go:241-249).

**Observable bad outcome.** A task annotated `- [ ] Task 2: … <!-- model: vendor/cheap -->`, or `sprint p s execute --model X` run directly, executes on `planning.execute_model`/config-default while `.run-state.json`, prompts (`Model source:`), metadata (`model_source`), metrics, and session-reuse logic all record and act on the annotated/overridden model. Wrong model runs billed work; audit trail misattributes it; resume's reuse gate (`batchSessionModel == taskSelection.Model`, execute.go:222, 569) makes continuation decisions against phantom models (spurious fresh-fallback full prompts).

**Trigger/preconditions.** Any sprint using the annotation syntax (feature exists solely for this; comment at execute_plan.go:24-26 says "optionally overrides the runtime model"), or any direct CLI/web/TUI execute with `--model`.

**Evidence & path.**
- Static: execute.go:220→241-249 (selection never copied to request); service.go:1166-1181 (only stage override); request → wire mapping at internal/platform/runtime/runtime.go:603-604.
- Empirical probe (`zz_scratch_probe_test.go` in scratch, passing): annotated task produced `request provider="test" model="model"` while `metadata[model_source]="plan.md task annotation"`, `state.Runtime.Model="vendor/annotated"`; `--model override/model` likewise ran on `test/model` while state claimed `command override`.
- Counter-evidence checked and dismissed: no metadata consumer selects models (`toAgentwrapRequest` ignores it); QA/review/code-context DO apply their selections to requests (code_context.go:293-295, review.go:902, qa_prompt.go:170), and the *flow* path applies execute `--model` via `StageOverrides → runtimeRequest` (operation_runner.go:175-187) — so behavior differs by entrypoint, confirming the direct path is a gap, not a design.

**Severity:** Medium-High (wrong outcome + misleading durable records on a governed surface). **Confidence:** High.
**Regression test:** extend the batch-execution fixture: annotate plan, assert `requests[0].Provider/Model` equal the annotated split; same for `ModelOverride`.

## F2 — Data race between late runtime events and the post-run persist/summary path

**Claim.** The OnEvent checkpoint closure mutates shared state under `sessionMu` (execute.go:257-267), but the main loop's writes/reads are unsynchronized: `task.Runtime = mergeRuntimeSummary(...)` (execute.go:275), `task.UpdatedAt/CompletedAt` (283-284), terminal persist (314), and `WriteExecuteSummary` iteration (327). The platform adapter legitimately delivers `OnEvent` after `StartRun` returns: it waits at most 1s for the event pump (runtime.go:400) and returns immediately on the cancellation fast-path (runtime.go:362-377), leaving the pump live.

**Observable bad outcome.** Undefined behavior per the Go memory model during every run whose event stream drains past the 1s window (long turns; run-control's per-event OnEvent wrapper adds per-event work, making slow drains realistic) or any cancelled turn. Reproduced under `-race`: `WARNING: DATA RACE — Write execute.go:264 (goroutine 26) vs Read execute.go:327 (main)`. Practically: torn `task.Runtime` pointer/fields, `.run-state.json` written from partially-mutated snapshots, extra DB-mirror writes in DB mode, `-race` CI failures.

**Trigger/preconditions.** Events arriving after `startSprintRuntime` returns — cancellation, or backlog drain exceeding 1s.

**Evidence.** Race report above from probe `TestScratchLateOnEventRacesTerminalPersist` (fake runtime fires OnEvent post-return, mirroring adapter-permitted timing).

**Severity:** Medium. **Confidence:** High (race proven; real-world frequency moderate).
**Regression:** keep the probe test; fix by taking `sessionMu` around lines 275-284/314 or draining/joining events before classification.

## F3 — Wholesale publication sweeps unrelated working-tree content into implementation commits/pushes

**Claim.** When all tasks resolve, publication issues `{Root: target.Path, All: true}` (publication.go:63) which stages `git add -A -- .` over the entire implementation repo (publisher.go:169-171, 228) and, in `commit-and-push`, pushes it. There is no cleanliness check or scope guard at publish time. The only clean-tree control is at worktree creation (execute_target.go:145-151) — absent entirely in raw-source mode (no `.workspace.json`) and stale once a user touches the worktree during a long sprint.

**Observable bad outcome.** With `git.stage_completion ∈ {commit, commit-and-push}` (default `off`, config.go:190), the operator's unrelated modified/untracked-not-ignored files (potentially secrets/local config) are committed under `ultraplan: sprint p/s complete execute` and pushed to origin.

**Counter-evidence weighed.** Opt-in config gate and documented "wholesale-commit root" intent reduce surprise; agent prompt bans git mutation but not user edits; `.gitignore` respected. Consequence remains concrete and irreversible once pushed.

**Severity:** Low-Medium. **Confidence:** High (mechanics code-certain; harm conditional).
**Regression:** publisher/product test: All-request with pre-existing dirty/untracked file must refuse, warn, or exclude.

---

## Defended non-issues (verified, not reported as defects)

- **Resume internals** (untested upstream): probes confirm stale-running→failed reconcile then retry (with `stale-running … on resume` diagnostic), checked-marker validation blocking before any runtime call, and session reuse correctly gated on exact target-path equality + model equality (mismatched seeded target correctly fell back to `initial` full prompt).
- **Unchecked plan + missing/corrupt state under `--resume` fails open to a fresh run** (execute.go:177): consistent with documented operator guidance ("leave tasks unchecked for a new execution"); atomic rename makes corruption unlikely.
- **`PlanFingerprint` captured once, never compared** (only charset-validated): dead diagnostic field; no contract claims drift detection (workspace unknown #4).
- **Containment via direct `WorkDir = target.Path`**: trivially contained; unused `ValidateExecuteWorkdir` is latent-only.
- **Legacy v0 state overwritten by an explicit new execute**: narrow operator-triggered historical-evidence loss; legacy shape is documented non-resumable.
- **`invalid-deferral` label when plan corruption breaks read-back despite successful work**: fail-closed, same exit class; recovery via resume re-run.
- **Execute ignores `run.Status`** (unlike code-context's defensive check): current agentwrap contract pairs failed status with non-nil error (status derived from Err when empty), so no reachable wrong-complete path today.
- **Dry-run persists the disposable context-pack cache** while `PromptExecute` stays read-only: cache-only side effect, outside governed publication paths.