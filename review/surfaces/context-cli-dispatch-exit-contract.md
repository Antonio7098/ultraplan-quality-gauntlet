# Context Pack: `cli-dispatch-exit-contract` — CLI dispatch and exit-code envelope contract

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: operator-interfaces. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

`cmd/ultraplan/main.go:18-43` wires OS argv/stdout/stderr, a signal context (`signal.NotifyContext` on SIGINT/SIGTERM, main.go:19), and the TUI/web runner adapters into `app.Run`, then exits with its integer return (`os.Exit`). `app.Run` (internal/app/app.go:88-178) is the single admission point for every invocation: it normalizes nil streams to `io.Discard` (stdout/stderr) or `os.Stdin` (stdin), defaults version and workdir, constructs the process-wide `runControlState` closed via defer (app.go:108-109), parses global flags, and dispatches one of 15 arms — `--help/-h`, `version`, and 13 command words (`init-workspace`, `defaults`, `skills`, `config`, `health`, `storage`, `run`, `project`, `sprint`, `study`, `tui`, `serve`, `code`) — at app.go:144-177; anything else is ExitUsage. The surface owns the numeric exit-class vocabulary (0-8, app.go:15-25), the stable-code strings derived from classes (`errorCode`, app.go:67-86), the classified-error wrapper that carries both a display string and an exit class (`classedError`, app.go:41-65), the stderr one-liner printer (`fail`, app.go:229-241), the shared JSON envelope writer (`json_output.go`), and the per-command renderers/flag grammars in `*_commands.go`. Scripts and agents consume three things: process exit codes, stdout bytes, and stderr bytes; docs/cli-reference.md declares those contracts.

## 2. Entrypoints and control flow

### 2.1 Global admission
`parseGlobalFlags` (app.go:198-220) extracts only `--workspace <path>` / `--workspace=<path>` (rejecting missing or dash-prefixed values) and passes all other tokens through in order; no other global flags exist. Empty argv, `--help`, `-h` print `renderHelp()` to stdout and exit 0 (app.go:140-146); unknown first token exits 2 with a pointer to `--help` (app.go:175-176). Every handler returns a single `error`; `failOrOK`/`fail` (app.go:222-241) print `err.Error()` as one stderr line and return the wrapped `classedError.class`, falling back to ExitError(1) for unclassed errors (including raw I/O write errors returned by handlers). `writeStatus` (app.go:243-248) returns ExitError if the stdout write itself fails.

### 2.2 Verb families
- **Pure text inspectors** (no workspace mutation): `version` (app.go:147-148 → `renderVersion`), `config show [--json]` (config_commands.go:10-119; ~60 fixed `key: value` lines plus `(source: …)` provenance annotations; redaction applied before rendering), `project list|<p> status|<p> validate` (project_commands.go:10-75; validate findings are printed to **stderr**, project_commands.go:118-129, and any findings force ExitValidation).
- **Workspace writers**: `init-workspace [--path --dry-run]` (workspace_commands.go), `defaults install [--path --dry-run --force]` (defaults_commands.go), `skills materialise [stage] [--path --dry-run --force]` accepting the `materialize` alias (skills_commands.go:17-23). All three print operation plans (`created/would create/overwrite/skip file …`) and use ExitWorkspace for failures.
- **Durable-store door**: `run list|show|follow|cancel|diagnostics` (run_commands.go:19-40) — full behavior is the run-cli-observation pack's subject; this surface owns its arg parsing conventions (`std flag.FlagSet` with output discarded, `helpRequested` pre-check, `orderRunArgs` hand-rolled flag/positional reordering for show/follow/cancel/diagnostics, run_commands.go:463-481) and its error→class mapping `mapRunControlError` (403-414): ErrInvalidArgument→2, ErrNotFound→5, ErrConflict/ErrTerminal→8, everything else→6.
- **Study/sprint product verbs** (§2.3-2.4): large hand-rolled positional+flag grammars over `study.Service` / `sprint.Service`.
- **Frontends**: `tui` (tui_commands.go:21-61) and `serve` (serve_commands.go:18-99) build `dashboardUseCases`/`NewWebUseCases`, open the run-control repository, and delegate to injected runners; `serve` validates `--listen` as a numeric loopback IP:port *before* workspace discovery (ValidateLoopbackListen, serve_commands.go:119-139; test-enforced ordering serve_commands_test.go:51) and maps shutdown-time context cancellation to exit 0 (serve_commands.go:62-64, 92-97).
- **Utility**: `storage migrate [--dry-run] [--json]` (storage_commands.go:33-140) imports study/sprint state files into SQLite and exits ExitPartial when any item failed (136-138); `code <report>... [--json] [--output]` (code_commands.go:19-78) maps extraction status Validation→5 / Partial→8.

### 2.3 Sprint verb grammar (sprint_commands.go)
`runSprint` (28-566) requires `<project> <sprint> <verb>`; `conformance-review` is aliased to `review` in-place (77-79). Help checks are position-sensitive (args[3]/args[4]). Per verb:
- `status [--json]` (90-121): renders status; smoke-readiness failure categories `catalog`/`review_gate` downgrade the JSON `status` label to `"partial"` but still exit 0.
- `metrics [--json]` (122-141): bare service DTO encoded directly (no envelope fields beyond the struct).
- `validate <stage>` (142-179): ten stages incl. execute/review/smoke; invalid stage → usage; failed validation → ExitValidation after printing findings.
- `prompt <stage> [--explain]` (180-222): prints the rendered prompt verbatim to stdout; `--explain` emits explanation JSON instead.
- `flow --to <stage> […]` (223-279): requires `--yes` for non-dry-run smoke targets (234-235); non-dry-run wraps work in `beginDurableCLICommand`; error classification ladder at 256-273: findings present → ExitValidation regardless of cause; else **`strings.Contains(err.Error(), "runtime")` → ExitRuntime** (269-272); else `mapSprintError`.
- `verify […]` (280-328): same durable wrap; typed smoke errors go through `mapSmokeError`, others `mapSprintError`; assessment fail/blocked forces ExitValidation (325-327). The verify payload is emitted even on failure, with `status` taken from `result.Verification.Assessment` and an added `error` object from `stableCommandError` (313-318).
- `execute […]` (329-368): durable wrap skipped for dry-run and for `--defer` (defer runs outside the durable boundary, 349-350); classification ladder 356-366: findings → 5; **`Contains(err.Error(), "failed tasks")` → ExitPartial**; **`Contains(err.Error(), "runtime")` → ExitRuntime**; else mapSprintError.
- `qa [map|status|recover|cancel|run|resume]` (369-466): parser enforces flag/action compatibility (588-640, e.g., cancel requires `--run`, forbids `--shard`); run/resume take the durable path with QA writer-token handoff; progress callbacks print `[qa] …` to **stderr** with redacted messages (435-441); the JSON envelope `{schema_version, operation:"sprint.qa", status, result, error?}` is written to **stdout even on failure** (454-459), with `error` built by `stableQACommandError` (category, retryable, correlation_id, recovery, timestamp).
- `review […]` (467-512): durable wrap unless dry-run; verdict fail or status blocked → ExitValidation; **`Contains(err.Error(), "runtime")` → ExitRuntime** (507-509); else mapSprintError.
- `smoke […]` (513-562): `--yes` mandatory for non-dry-run (518-520); typed `mapSmokeError` decides everything (879-892): category cancellation→7, process/timeout/cleanup→6, all other typed categories→5; untyped falls back to mapSprintError; verdict fail/blocked → ExitValidation.

Shared mappers: `mapSprintError` (944-963) orders `context.Canceled`→7, `DeadlineExceeded`→6, `ErrVerificationConflict`→8, flow-state and execute-state sentinels→5, project/sprint RefError→5, default→ExitWorkspace(4). `mapQACommandError` (662-679) maps canceled/deadline→**ExitPartial(8)** with codes `qa.cancelled`/`qa.deadline_exceeded`, QA-typed runtime/persistence→6 with codes `qa.<category>`, other QA categories→5, fallback→mapSprintError. `stableCommandError` (1463-1470) reduces any classed error to `{code, message, recovery}` where message passes through `displaySafe` (usecases.go:251-278: config redaction + ANSI/control-char stripping).

### 2.4 Study verb grammar (study_commands.go, study_runs_commands.go)
`runStudy` (26-120) routes `init | list | <s> list | <s> {summary, validate, status, runs, run-loop, run-all, run, synthesize, prompt}`; sub-verbs are matched positionally (`args[1]=="run-loop"` etc.). Runtime-backed verbs (`run-loop`, `run-all`, `run`, `synthesize`) wrap execution in `beginDurableCLICommand` and classify results afterwards: `classifyRunAllResult` (732-745) completed→0, cancelled→7, partial→8, runtime-failed→6, otherwise→5; `classifyExecutionResult` (933-948) completed/skipped→0, runtime-failed→6, cancelled→7, validation-failed/preflight-blocked→5, unknown status→ExitError(1). Read-only doors: `status [--json]` uses the shared envelope with `statusJSONResult` (status_json.go:67-145 — counts, redacted lock command, per-task usage/cost as `{known,value}` pairs, workspace-relative paths); `validate [--json]` uses the shared envelope with redacted checks and returns ExitValidation when failed (1012-1044); `summary`/`runs summary` regenerate artifacts and return 0 unless resolution/history sync fails. Error mappers: `mapStudyError`/`mapStudyPromptError`/`mapStudySummaryError` (RefError/config sentinels→5, default→4), `mapStudyRunLoopError` (locked→8, malformed/unsupported→5, else execution mapper), `mapStudyStatusError` (1162-1178 — missing/malformed→5, else→4, with a `displayError` wrapper whose display string has absolute paths rewritten relative via `strings.ReplaceAll`).

### 2.5 Durable acceptance coverage (dispatch-level contract)
`beginDurableCLICommand` (durable_operations.go:49-60) opens the repository, accepts an outer operation row with an **empty alias digest** (so alias dedup is unreachable from CLI, durable_operations.go:55), claims it, appends a lifecycle-running event, and hands back a cancellation-aware context. CLI call sites: sprint flow/verify/execute/review/smoke/qa-run/qa-resume (6 textual call sites) and study run-loop/run-all/run/synthesize (4). Dry-run, prompt, validate, status, metrics, qa map/status/recover/cancel, storage, run-observation, and all foundation verbs skip acceptance. `finishDurableCLICommand` (86-91) joins the run error with `Finish`, which classifies the terminal outcome (ctx-done→cancelled, deadline→timed_out, err→failed, else succeeded) under a fresh 30 s `context.Background()` window so terminal persistence survives parent cancellation, and surfaces finish failure as a classed ExitRuntime error joined into the result. `TestEveryRuntimeBackedCLIEntryUsesDurableAcceptanceInventory` (run_control_inventory_test.go:11-55) pins these call-site counts and kind strings by reading the source files.

## 3. Inputs / outputs

Inputs: `os.Args[1:]`; env vars read via `envLookup` (`ULTRAPLAN_WORKSPACE`, `ULTRAPLAN_STUDY_MODEL` studyModelOverride study_commands.go:183-188, QA/smoke settings via effective config, smoke `Getenv` injection sprint_commands.go:814-823); stdin for the three confirmation prompts (§5); filesystem state behind discovery/validation; wall clock (`timeNow` var, study_commands.go:1160, used in envelope timestamps and status reconciliation).

Outputs, by stream:
- **stdout**: help/version text; all human result renderers; all JSON envelopes and NDJSON (`run follow --json`); interactive prompts and their echoes for defaults/skills/run-loop-reset confirmations; `[run-loop]` task-progress lines (the documented exception to stderr-progress discipline, cli-reference.md:32); `serve` startup messages.
- **stderr**: exactly one line per failed command via `fail()`; sanitized progress streams for sprint flow/verify/execute/review/smoke/qa and study run-all/run/synthesize (`[sprint]`, `[runtime]`, `[smoke]`, `[qa]` prefixes; redacted via `config.RedactValue`); project-validate findings; non-completed-task and warning summaries after study runs; serve diagnostics.
- **exit code**: one of {0..8}; ExitError(1) is reachable only through write failures, unclassified handler errors, `serve`/`tui` runner failures, code-render failures, and the unknown-status arm of `classifyExecutionResult`.

Envelope families (all schema_version 1):
1. Shared `writeJSON` envelope `{schema_version, command, workspace, status, generated_at, result}` (json_output.go:20-31): `config show`, `health`, `study validate`, `study status`, `run list/show/diagnostics` (diagnostics sets `status` to `string(health.Status)` rather than constant "ok").
2. Ad-hoc command envelopes `{schema_version, operation, status, result, error?}` encoded inline: `sprint status/flow/verify/qa/review/smoke` (and `metrics` emits the bare DTO without wrapper fields).
3. Deterministic domain JSON: `code --json` (`codeextract.RenderJSON`), `storage migrate --json` (`storageMigrationResult` struct).
Failure-path emission varies: health writes its envelope with status "fail" before returning the error (health_commands.go:46-50); sprint flow/verify/qa write failure envelopes best-effort (`_ =` discard of encode errors, e.g., sprint_commands.go:259, 318, 459); most other verbs emit nothing on stdout when failing.

## 4. Authoritative state

None durable is owned here. This surface is projection/admission only: it creates no tables, holds no locks beyond what delegated layers acquire, and persists nothing except through delegated effects (files written by init/defaults/skills/study-init/code-output/prompt-output/storage-migrate; rows written via beginDurableCLICommand and runRepository; listener socket in serve). Process-local state: `runControlState` singleton (per-workspace repository+logger, closed at Run exit), the `durableOperationManager` created per accepted command (owned-operation map, control goroutine), and the injectable `timeNow`. The authoritative stores it touches belong to other surfaces: `.ultraplan/run-control.db` (run-journal-fencing), flow-state/execute-state/QA files (sprint-flow-state, sprint-qa-investigation), run-state.json (study-runloop-scheduler), product-state DB (product-state-mirror).

## 5. Invariants (as implemented)

- One admission function; every exit path flows through `fail`/`writeStatus` so each command produces at most one stderr line and a single class from {0..8}.
- Classified errors preserve their cause chain (`Unwrap`, app.go:48) so `errors.Is/As` ladders operate on original sentinels/typed errors while the class rides on the wrapper.
- Stable code strings per class (`errorCode`); QA adds a finer `qa.<category>` code vocabulary carried inside JSON payloads (phase3-json-schemas.md:35 closed list).
- Interactive confirmations have no TTY detection anywhere: `confirmOverwriteDefaults` (defaults_commands.go:125-144), `confirmOverwriteSkills` (skills_commands.go:148-167), and `confirmRunLoopReplacement` (study_commands.go:277-316) read one line from configured stdin; EOF is tolerated (treated as a non-affirmative answer); non-"yes/y" answers keep existing files or refuse replacement (replacement refusal exits ExitPartial "replacement not confirmed", study_commands.go:314). Destructive non-interactive paths require explicit flags instead: `--force` (defaults/skills), `--reset --yes` (run-loop), `--yes` (smoke/flow-to-smoke/verify-to-smoke), `--force-review --override-reason` pairs enforced at parse time (sprint_commands.go:873-876, 1070-1072, 1187-1189).
- Stream discipline as documented: runtime-backed sprint progress → stderr; final results → stdout; study run-loop progress → stdout by documented exception; human errors → stderr.
- Durable acceptance precedes runtime work for exactly the inventory-pinned entry set; acceptance happens after parse/discover/config so malformed invocations never create runs.
- Terminal proposal after cancellation uses a detached context, so a cancelled command still records its durable outcome when persistence is reachable.
- Redaction before display on every renderer that embeds potentially sensitive values (`config.RedactValue` calls across config/health/study/sprint/QA renderers); `displaySafe` additionally strips ANSI escapes/control bytes for JSON-embedded messages.

## 6. Trust boundaries

- All argv tokens, env vars, and stdin answers are untrusted admission input. They reach: workspace/config loading (validated downstream by config-inspection-health), file paths for init/defaults/skills/code-output/prompt-output (`--output` for study prompt is containment-checked via `workspace.ResolveInside`, study_commands.go:1409; `code --output` is not contained, only made absolute against workDir, code_commands.go:56-65), model refs (`--model`, passed through to runtime request construction), listen address (strictly validated loopback-only), and identifiers echoed into error strings (e.g., `%q` of unknown commands/stages).
- Error text is inspected by classifiers: the sprint flow/execute/review ladders branch on substrings of `err.Error()` ("runtime", "failed tasks") produced by lower packages; everything else classifies via typed sentinel/`errors.As` matches. A parallel guard exists for the TUI/web operation path asserting free-form text does not affect classification there (sprint_error_test.go:29-41); the CLI substring branches are separate code.
- Error text also reaches consumers: `fail()` prints `err.Error()` verbatim to stderr (causes are app-constructed; several embed user-supplied tokens quoted); JSON envelopes embed only `displaySafe`-processed messages; `stableCommandError`'s fixed `recovery` string is constant.
- Confirmation prompts accept any stdin bytes; only exact `yes`/`y` (case-insensitive, trimmed) affirm.
- The `serve` door inherits web-security-boundary controls; dispatch contributes the loopback-listen validation and never forwards raw argv further.

## 7. External effects & lifecycle semantics

External effects per invocation can include: workspace scaffold/default/skill file creation or overwrite (with confirm/force gates); git clone subprocesses during `study init` (partial failures → ExitPartial with redacted clone output, study_init_commands_test.go:113); SQLite DB/schema creation (`storage migrate` non-dry-run opens runcontrol + productstate); durable run rows/events/terminals in run-control.db for accepted verbs; report/prompt/output file writes; a listening loopback socket and optional platform browser launch (`openBrowser`, main.go:45-62, exec of `open`/`rundll32`/`xdg-open` with 5 s timeout); TUI takeover of stdout.

Cancellation semantics: the process-level context derives from SIGINT/SIGTERM. For durable-wrapped verbs, cancellation propagates through the accepted operation's child context; `Finish` records cancelled/timed_out terminals; `mapSprintError` yields exit 7 for plain cancellation while the QA command path deliberately yields exit 8 with code `qa.cancelled` (both behaviors test-pinned). `serve` treats shutdown cancellation as success (exit 0) after drain. `run follow` ends observation-only on ctx-done (exit 0). Non-durable verbs simply unwind; partial side effects (e.g., half-written scaffolds) are not compensated anywhere in this layer.

Retry semantics: none at this layer — no handler retries on failure; retryability is expressed only as data (`retryable` booleans in QA error payloads; typed store-error flags consumed elsewhere).

Restart semantics: stateless between invocations; every invocation re-runs discovery/config and (for repository-opening verbs) startup reconcile owned by deeper surfaces.

Error semantics summary matrix (reachable classes per family):
- Foundation/help/version: 0, 1 (write failure), 2 (usage), 3 (config), 4 (workspace).
- health: 0, 2, 3 (config wins precedence), 4 (discovery), 5 (structure), 6 (runtime checks) — precedence order cfgErr → runtimeFailed → structure validation (health_commands.go:100-108).
- code: 0, 1, 2, 4 (extract/output), 5 (validation status), 8 (unresolved references).
- run *: 0, 2, 5, 6, 8 via mapRunControlError.
- project: 0, 2, 4, 5.
- sprint: 0, 2, 3, 4, 5, 6, 7, 8 via the ladders above.
- study: 0, 1 (unknown status), 2, 3, 4, 5, 6, 7, 8 via the classifiers above.
- storage: 0, 2, 4, 8.
- tui/serve: 0, 1 (runner failure), 2, 3, 4, 6.

## 8. Immediate surface dependencies

- **config-inspection-health** (foundation): supplies `discoverWorkspace`/`loadEffectiveConfig` (app.go:287-319), the redaction used by every renderer, `EnvOverrides` listing for the health environment summary, and the config-error exit class; health additionally consumes runtime health checks (`runtimeHealthChecks` var seam, health_commands.go:28).
- **durable-operation-spine** (durability-core): `beginDurableCLICommand`, `finishDurableCLICommand`, `controlledRuntimeFor`, `runRepository`, QA writer-token mint/fence — the acceptance spine this surface calls into for its 10 pinned call sites.
- **run-cli-observation**: implements the `run` arm under the dispatch/exit/envelope conventions defined here (its `mapRunControlError` and envelope choices are part of the same operator contract).
- **shared-usecase-vocabulary**: defines the OperationKind enum and request shapes the CLI passes into acceptance; the empty-CLI-digest regime originates at durable_operations.go:55.
- **workspace-scaffold-defaults**: backs init-workspace/defaults/skills plan/install functions whose outputs this surface renders.
- Sibling frontends (web-routing-projection, web-operation-hub-sse, tui-console, web-security-boundary) reuse the same use cases; parity expectations live in shared-usecase-vocabulary, not here.
- Foundation libraries: workspace.Discover/Rel/ResolveInside/Init/PlanDefaults/MaterialiseSkills, project/sprint/study services, codeextract, runcontrol, productstate.

## 9. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-go docs (in-tree):
- cli-reference.md:18-30 — the numeric exit-class table 0-8 with prose meanings ("7: cancellation", "8: partial completion").
- cli-reference.md:32 — stream discipline sentence: standalone/run-all/sprint progress uses stderr so stdout stays machine-readable; the durable study run-loop retains task-progress on stdout; message bodies and raw provider payloads are not printed.
- cli-reference.md:96-107 — `config show --json` envelope example fixing field names/order semantics; :117 health envelope; :220/:230-239 study validate/status envelopes ("Debug/runtime raw payloads are not a stable public JSON surface").
- cli-reference.md:363 — QA exit mapping prose: "Usage errors use exit 2, configuration errors 3, validation/stale/policy errors 5, runtime or persistence failures 6, and cancellation or deadline partial results 8" (matches `mapQACommandError` including cancellation→8).
- cli-reference.md:474-491 — the compatibility-sensitive stable JSON surface list: config show, health, study validate/status, code, sprint status/review/qa/smoke/verify (flow/metrics/run-* envelopes are outside the declared stable set).
- phase3-json-schemas.md:3, 29, 35, 81 — schema_version 1 additivity rule; QA CLI envelope shape; closed `qa.*` error-code vocabulary; "Cancellation and unavailable prerequisites remain distinct from pass".
- user-guide.md:186, 267, 301 — nonzero exits on runtime/validation failure; code extraction partial vs validation classes; stderr progress/stdout result split.
- In-code contract comments: ExpectedFingerprint is server-issued authority never accepted from transport (operations.go:129-133); OperationalUseCases carries no transport types (operations.go:23-28); sprint help texts document per-verb flags and the conformance-review alias preserving "command name, review.md, verdicts, JSON operation name, and exits" (sprintReviewHelp, sprint_commands.go:1582-1590).

From ultraplan-workspace (authoritative planning context): the operator-interface commitments are distributed across TRD sections covering governed CLI verbs and durable acceptance; no dedicated CLI-exit-code section was located in the workspace during this pass (recorded as an unknown below).

## 10. Tests (evidence map)

Baseline green (review/baseline/go-test-cover.txt): internal/app covered alongside the suite; dispatch-level tests below are the pinning evidence.
- app_test.go:13-102 — help/no-args/-h exit 0 with clean stderr; version fields; unknown command → ExitUsage with empty stdout; classified error preserves cause+code (`validation.workspace`).
- app_test.go:104-210 — init-workspace dry-run/create; defaults install idempotence, customized-file confirmation via piped stdin ("yes"), `--force` overwrite.
- app_test.go:212-255 — config show --json parses as envelope, redacts "secret", pins sources provenance; text form pins agentwrap keys.
- sprint_commands_test.go:19 TestSprintHelpIsRegistered; :120 TestSprintFailureJSONIsOneStructuredDocument (failure JSON must be one document, no trailing output, never reports success, verify must include structured error); :149 TestSprintQAJSONFailureUsesStableEnvelopeAndCategory (pins `qa.invalid_state`, category/severity/operation/component fields, ExitValidation); :190 TestQACommandErrorClassesAndStableCodes (typed runtime→6, stale→5, `context.Canceled`→8 `qa.cancelled`); :214 TestSprintStatusErrorsAndInvalidFlowStateExitFive (ambiguous/missing refs and malformed flow-state → 5, stdout empty, file untouched); :247 TestSprintMalformedArgumentsUseUsageExit; :464-596 TestSprintFlowNonDryRunUsesConfiguredRuntime (durable start ordering; runtime failure → ExitRuntime with call-count pinned); :597 validate-failure exits; :619-737 parser unit tests incl. QA public-control allowlist and smoke args.
- sprint_error_test.go:11-27 smoke typed-cause preservation; :29-47 TUI/web-side guards that free-form error text does not classify and typed cancellation does.
- study_*_test.go — usage exits (:163 study run usage; study_run_all_commands_test.go usage), ExitValidation on validation/preflight (study_run_commands_test.go:124), runtime failure → ExitRuntime, run-loop lock conflict → ExitPartial + `--force-unlock` + reset confirmation flow incl. stdin-driven yes/no (study_run_loop_commands_test.go:78, :136-198), cancellation → ExitCancel (study_run_loop_commands_test.go:199), clone-partial → ExitPartial with redacted git output (study_init_commands_test.go:88-135), status/validate envelopes and redaction (study_status/validate_commands_test.go).
- code_commands_test.go:88 output-write failure; :104 unresolved→ExitPartial / missing table→ExitValidation mapping; :125 unreadable report fails fast.
- health_commands_test.go:14 valid/invalid workspace; :61 runtime failure → ExitRuntime; :75 JSON envelope + redaction.
- project_commands_test.go:64 validate failure → ExitFive with findings on stderr; :86 ambiguous/missing refs → ExitValidation.
- serve_commands_test.go:12 lazy help; :51 listen validation before workspace/runner; :73 startup failure class; :97 cancellation is clean (exit 0).
- skills_commands_test.go:34 customized-file preservation without confirmation.
- run_control_inventory_test.go:11 source-inventory pinning of beginDurableCLICommand call sites/kinds.
- run_commands_test.go (sibling surface) pins run-door help-without-repository, list/show/follow/cancel/diagnostics happy paths and export privacy.

Factual coverage notes (absence of tests, not defects): no test drives the substring branches of the sprint flow/execute/review ladders directly (they are exercised only incidentally via integration-shaped tests such as TestSprintFlowNonDryRunUsesConfiguredRuntime); no test pins `sprint metrics --json` shape or the flow `--json` success envelope fields; no test covers `storage migrate` at all (no storage test file exists); no test asserts the shared-envelope vs ad-hoc-envelope field differences; no test exercises `writeStatus`/`fail` stdout-write-failure branches; help-position edge cases beyond the tested forms are unpinned; no test verifies exit classes for `run` door error paths end-to-end (mapping is unit-visible only through sibling-surface tests).

## 11. Explicit unknowns / open questions (for later reviewers)

1. Classification duality: four CLI branches classify by `strings.Contains(err.Error(), …)` ("runtime" ×3, "failed tasks" ×1) while sibling mappers are typed; whether lower-package message wording is contractually frozen for these branches is unstated anywhere in-tree.
2. Cancellation exit asymmetry: generic sprint cancellation → 7 (`workflow.cancelled`) but QA-command cancellation/deadline → 8 (`qa.cancelled`/`qa.deadline_exceeded`); cli-reference.md:363 documents the QA behavior explicitly but no doc reconciles the two regimes for scripts.
3. Two coexisting JSON envelope families differ in identity fields (`command`/`workspace`/`generated_at` vs `operation`) and only one family is named in the stable-surfaces list; consumer guidance for the unnamed ones (flow/metrics/run-*/storage) is absent.
4. Failure-path stdout variance: health/sprint-flow/verify/qa emit JSON bodies on stdout *and* an error line on stderr with a nonzero class; other verbs emit stderr only; best-effort encode (`_ =`) means a broken pipe during failure emission silently drops the JSON body while the exit code survives.
5. `finishDurableCLICommand` joins a `run-control.finish` ExitRuntime error into successful runs — an operation whose work completed can still exit 6 if terminal persistence fails; whether operators should treat that as rerunnable is undocumented.
6. Confirmations without TTY probing: piped/empty stdin deterministically takes the conservative branch (keep/refuse), but no doc states the automation contract (docs tell humans to use `--force`/`--yes` only).
7. `--help` grammar is per-verb ad hoc (position-sensitive index checks; `code --help` only with exactly one arg; `health` accepts `--help` anywhere among args; `run` door pre-scans all args via `helpRequested` before FlagSet parsing) — equivalence across verbs is unpinned.
8. `orderRunArgs` reorders flags ahead of positionals with a hard-coded recognized-flag table; interaction with `--flag=value` forms and repeated positionals is unit-tested only indirectly.
9. ExitError(1) semantics are heterogeneous (write failures, unclassed errors, runner failures, unknown study status) while docs describe 1 only as "internal or write error".
10. `sprint status --json` downgrades its label to `"partial"` for catalog/review-gate smoke-readiness failures yet exits 0; the label's meaning for consumers is not defined in phase3 docs.
11. `deps.workDir` falls back silently to "" when `os.Getwd` fails (app.go:128-132); downstream behavior of relative-path resolution in that state is undefined in-tree.
12. No workspace-side planning document was found that specifies the 0-8 exit table or the envelope families; the in-tree docs are currently the only stated authority (possible documentation-of-record gap, recorded neutrally).

— End of context pack. Descriptive only; no defect claims made or implied.
