Relevant surface files are identical to the frozen commit; I can read from the working tree safely. Reading the primary paths now.Now examining supporting surfaces: gitpublish, config defaults, project-index validation, and runtime warning/artifact origins.Now checking the model annotation wiring hypothesis against tests.Now reading the gitpublish publisher to verify All=true semantics and containment.Checking help text claims for `--model`, and verifying test coverage for resume internals.Found a promising lead: the `"unresolved"` model sentinel defeating the empty-model guard. Verifying the chain.All hypotheses traced to ground truth. Final verification pass complete. Here is my report.

---

# Review Report: `sprint-execute-resume` (security/misuse lens)

Target: ultraplan-go @ `5d54525` (surface files verified identical; unrelated dirty files untouched). Baseline `go test ./internal/sprint ./internal/platform/gitpublish` green.

## Finding 1 — Execute model selection (`--model` flag and plan `<!-- model: ... -->` annotations) never reaches the runtime request

**Claim.** `Service.Execute` computes a per-task model selection (CLI override > planning.execute_model > plan_model > runtime config, plus inline per-task annotations), renders it into prompts/metadata/state, uses it as the session-reuse cohort key — but never assigns it to `runtimeReq.Provider`/`runtimeReq.Model`. The executed model always comes from `runtimeForStage(StageExecute)` (`planning.execute_model`) or the runtime-config default.

**Observable bad outcomes.**
- Operator runs `sprint p s execute --model vendor/premium` (advertised at internal/app/sprint_commands.go:1614 and docs/cli-reference.md:311): execution silently proceeds on `planning.execute_model`/default; prompts print `Model source: command override` (execute.go:455) while a different model runs → cost/capability mismatch invisible to the operator.
- A task annotated `- [ ] Task 1: ... <!-- model: vendor/x -->` executes on the batch model; `.run-state.json`, runtime metrics, and published artifacts record `Model: vendor/x` (execute.go:262, 275, 641) → published governance record misattributes the model.
- Session-reuse gating compares phantom values (`continueSession := batchSessionID != "" && batchSessionModel == taskSelection.Model`, execute.go:222; `reusableExecuteSession`, execute.go:563-574). An annotated first task yields spurious fresh-fallback full-prompt turns (cost/context loss); cross-process resume can pair a session with a declared model it never ran under.

**Trigger.** Any non-dry-run execute with `--model`, or any plan carrying a model annotation.

**Evidence / path.**
- internal/sprint/execute.go:241-249 — request built via `s.runtimeRequest(...)`; only `WorkDir`, `SessionID`, `SessionAction` are set afterward.
- internal/sprint/service.go:1166-1181 — `Provider`/`Model` assigned *only* from `s.runtimeForStage(stage)` (config); never from the selection or annotation.
- internal/sprint/execute.go:739-744 (`executeSelectionForTask`), 220/222/275-282 — selection used solely for gating/prompt text/summaries.
- Contrast: review applies its selection to the request — internal/sprint/review.go:916 `req.Model = strings.TrimPrefix(m.Model, req.Provider+"/")`. No analogous line exists in execute.
- Code comment states intent: internal/sprint/execute_plan.go:24-26 — "Model optionally overrides **the runtime model** … for this task".
- Tests would not catch it: fake runtimes record requests but no execute test asserts `req.Model`; annotation test asserts only selection-struct precedence (internal/sprint/execute_plan_test.go:196-205).

**Counter-evidence searched.** No alternate wiring via run-control wrapper (internal/app/run_control.go:159+ passes request through), stage overrides come only from config (internal/app/sprint_commands.go:754-779, 978-998).

**Severity:** High (silent misexecution + corrupt published audit data). **Confidence:** High.

**Regression test:** two-task plan, task 1 annotated with a distinct model, recording fake runtime; assert `requests[0].Model` equals the annotation (fails today) and that state/metrics/request agree.

## Finding 2 — Missing-execute-model prerequisite guard is dead code (`"unresolved"` sentinel)

**Claim.** `prepareExecute` intends to abort with an actionable finding when no model is configured (`if selection.Model == ""`, execute.go:379-381), but `executeModelSelection` returns the sentinel `{Model: "unresolved", Source: "unresolved"}` instead of empty (execute.go:723), so the condition can never fire.

**Observable bad outcome.** In a workspace with all model fields blanked, execute passes prerequisite validation, persists a `running` checkpoint, and launches agentwrap with empty Provider/Model; agentwrap then omits `--model` entirely (agentwrap `opencode/runtime.go:132-140`: the flag is appended only `if req.Provider != "" || req.Model != ""`), so OpenCode's own server-default model executes governed sprint work. State records `model_source: unresolved`. With default placeholder config (`models.primary: "provider/model"`, internal/platform/config/config.go:185), the guard still can't fire and misconfiguration surfaces as an opaque runtime failure instead of the designed finding ("Set planning.execute_model, …").

**Evidence.** internal/sprint/execute.go:378-381 vs 710-724; contrast review, which explicitly handles the sentinel: internal/sprint/review.go:344 `if strings.TrimSpace(manifest.Model) == "" || manifest.Model == "unresolved"`.

**Counter-evidence searched.** No upstream layer rejects blank models before Execute (`RequestFromConfig` accepts empty provider/model without error, internal/platform/runtime/runtime.go:537-559; CLI/web paths add none).

**Severity:** Medium. **Confidence:** High.

**Regression test:** service with empty stageRuntime/runtimeConfig; assert `Execute` returns the missing-model finding before any state write (currently proceeds to launch).

## Finding 3 — `All=true` wholesale publication can commit and push unrelated dirty content of the user's source checkout

**Claim.** When no valid `.workspace.json` exists (legacy sprints, deleted record, standalone execute), `resolveSprintTarget(create=false)` returns the raw project-index directory as target (execute_target.go:74-75). On full resolution, `publishExecuteStage` issues `Publish{Root: target.Path, All: true}` (publication.go:63) which stages `git add -A -- .` of the entire worktree (gitpublish publisher.go:169-171, 228) onto **the repo's currently checked-out branch** (publisher.go:87-94, update-ref at 249-255) and pushes it in `commit-and-push` mode.

**Observable bad outcome.** The user's own uncommitted working-tree content in their real checkout — WIP edits, non-gitignored local files (notes, configs, credentials not covered by .gitignore) — is committed under `ultraplan: sprint <p>/<s> complete execute` and pushed to the configured remote, without any diff scoping to what the sprint produced. Aggravating context: the directory itself is selected by the agent-generated `Target Implementation Directory` line in project-index.md, validated only for existence/is-dir (execute_target.go:29-54; `ValidateSprintIndexContent` checks catalog tables only, internal/sprint/validation.go:9-25).

**Trigger / preconditions.** `git.stage_completion` = `commit`|`commit-and-push` (default off — main mitigation), execute reaching all-resolved with absent workspace record, dirty/non-clean source tree. Flow-driven sprints are protected because flow creates the clean worktree at code-context (flow.go:133-140) and `validateSprintWorkspace` rejects source drift; the gap is exactly the fallback path.

**Existing controls / counter-evidence.** Default mode off; commit skipped when tree identical to parent (publisher.go:241-243); `add -A` respects .gitignore; detached-HEAD sources rejected; CAS ref update. None of these scope the commit to sprint-owned changes.

**Severity:** Medium-High consequence (unintended publication/push of user data), conditional trigger. **Confidence:** High on mechanics; medium that this is unintended rather than accepted behavior — no doc states whole-checkout commits are intended for the no-worktree case.

**Regression test / fix direction:** refuse implementation publication (or restrict to worktree-sourced targets) when `target.Source == "project-index.md"`, or publish explicit paths; test: dirty non-worktree target + enabled publisher must error instead of committing.

## Finding 4 — Resume retains stale state header (Target/PlanPath/PlanFingerprint) without revalidation

**Claim.** On resume, `reconcileExecuteState` replaces only `Tasks` (execute.go:620-638); the header carried from the prior attempt — including `Target{Path,Source}`, `PlanPath`, and the once-captured `PlanFingerprint` (execute.go:176-179) — is kept and re-published even when this run executes against a different resolved target.

**Observable bad outcome.** If the project-index target line changed between attempts while no workspace record exists (the only path where differing targets survive validation), tasks execute in the new directory while `.run-state.json`/published artifacts continue to declare the old target for those executions, permanently misattributing the implementation repository in the governance record. Related: the fingerprint is never refreshed or checked mid-run, so plan drift between attempts is undetectable in state.

**Trigger.** `--resume`, absent/removed `.workspace.json`, edited `Target Implementation Directory` since prior attempt. Worktree-recorded sprints abort earlier via `validateSprintWorkspace` (source-root equality, execute_target.go:100-102), which is the counter-evidence bounding the impact.

**Severity:** Low-Medium. **Confidence:** High on mechanics.

**Verification:** resume fixture with changed index target and no workspace record; assert state.Target equals current resolution (fails today).

---

## Defended / non-issues (searched, with counter-evidence)

- **Persist-before-launch & terminal checkpointing:** sound; running-persist failure aborts pre-launch (tested), terminal persist failure aborts command; mid-stream session checkpoints mutex-guarded and surfaced post-run (execute.go:250-272, 287-318).
- **Workdir containment:** `ValidateExecuteWorkdir` being caller-less is not exploitable here — `runtimeReq.WorkDir = target.Path` is a direct assignment of the resolved, stat-verified directory; no derived/agent-influenced path reaches it.
- **`hasDiagnosticOnlyCompletion` substring gate (execute.go:671-678):** loose match, but I found no producer of warnings containing "diagnostic-only" in this repo or in agentwrap (warnings originate from platform `EventWarning` records, not arbitrary agent output); currently inert, worth tightening if a producer ever appears.
- **gitpublish command construction:** no shell; variable data passed as argv elements; remote names validated (no leading dash, restricted charset); commit message/identity delivered via stdin; env sanitized (`GIT_TERMINAL_PROMPT=0`, `LC_ALL=C`); push timeout bounded; branch CAS prevents clobbering concurrent movement.
- **Deferral protocol:** rationale enforced at parse, CLI, state-validation, and read-back layers; deferred tasks excluded from queue and loop; complete/deferred cannot be re-deferred; deferral does not bypass resume marker validation.
- **Stop-on-failure / stale-running reconcile:** three-layer recovery consistent (startup locks.go:46-66, resume execute.go:623-627, in-loop execute.go:203-207); queue halt tested.
- **State file durability:** temp+fsync+rename+dir-sync with strict schema validation; malformed/unsupported versions classified, legacy v0 treated as historical-only.
- **Durable wrapping:** every non-dry-run CLI execute accepted/claimed/fenced with heartbeat+cancellation watch; defer intentionally unwrapped (pure state edit under lease).