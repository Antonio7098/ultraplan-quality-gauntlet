# Surface review: `cli-dispatch-exit-contract` — dispatch, exit classes, JSON envelopes

Reviewer: independent deep review (tests-first where useful). Target: ultraplan-go @ `50d6f0d` (working tree == frozen commit; verified `git log`). Baseline `go test ./internal/app/` green at review time. All live probes ran a binary built from the frozen tree (`go build -o /tmp/opencode/ultraplan ./cmd/ultraplan`) against throwaway fixtures under `/tmp/opencode/probe`; target repo and workspace untouched.

Method note: every candidate below was first searched for counter-evidence (callers, guards, mappers, tests) before being kept; defended non-issues are listed at the end.

---

## F1. Substring-sniffed error classification misroutes typed/reference failures to exit 6, and adding `--json` silently changes the exit class

**Severity: high · Confidence: high (live-reproduced)**

Claim: three sprint CLI ladders classify by `strings.Contains(err.Error(), …)` instead of by the typed cause that is still available on the error; the sniffed text embeds operator-controlled names, and the `flow` arm applies the substring rule only in text mode.

Concrete claim & bad outcome:
1. A plain reference failure exits **6 (`provider.runtime`)** instead of 5 because the error message lists sibling directory names and one contains "runtime". A script keying 5 = "fix inputs, do not retry" vs 6 = "transient provider problem, retry" will retry a permanent input error indefinitely.
2. The same invocation with and without `--json` produces different exit classes (reproduced: 6 vs 5). Automation cannot state a single contract for `sprint flow`.

Trigger/preconditions (all reproduced in one fixture):
```
ws/projects/runtime-tools/        # any project/sprint dir whose name contains "runtime"
ultraplan --workspace ws sprint nosuchproj 01 status                            -> exit 5   (correct)
ultraplan --workspace ws sprint nosuchproj 01 flow --to requirements --dry-run  -> exit 6   (wrong)
ultraplan --workspace ws sprint nosuchproj 01 flow --to requirements --dry-run --json -> exit 5
ultraplan --workspace ws sprint nosuchproj 01 review --json                     -> exit 6   (wrong)
```
All four fail with the identical cause: `project reference "nosuchproj" not found; available: runtime-tools`.

Exact source evidence:
- Ladders: `internal/app/sprint_commands.go:269-272` (flow, text-mode only), `:360-365` (execute: `"failed tasks"` then `"runtime"`), `:507-509` (review).
- The flow `--json` branch (`sprint_commands.go:258-264`) omits the substring check entirely and falls to `mapSprintError`, hence the mode-dependent class.
- False-positive source: `internal/project/discovery.go:19-27` and `internal/sprint/discovery.go` render `RefError` with candidate lists built from user-chosen directory names (`IsSafeName` permits `runtime-*`); `internal/sprint/execute.go:315` interpolates plan-derived task IDs into `persist terminal execute task %q: %w`.
- Intended true positives also exist ("runtime is required for X flow", `internal/sprint/service.go:597` etc.; "code-context runtime ended with status %q", `code_context.go:322`), which is why the branch fires at all — but nothing constrains which strings reach it.
- `mapSprintError` (`sprint_commands.go:944-963`) already type-matches `project.RefError`/`sprint.RefError` → 5 correctly; the substring check pre-empts it.

Execution path: `service.Flow/Review/Execute` returns typed error → handler ladder sniffs rendered text before/instead of `errors.As` → wraps in a new `classedError` → `fail()` (`app.go:229-241`) takes the outermost class.

Existing controls / counter-evidence searched:
- Typed mappers exist and are correct for `status`/`validate`/`metrics`; they are simply bypassed by the ladders.
- `sprint_error_test.go:29-41` pins that the *TUI/web* path does NOT classify on free-form text — evidence the team considers text-sniffing wrong, yet the CLI branches are separate untested code.
- No test drives any substring branch directly (confirmed by reading `sprint_commands_test.go`; the only nearby pin, `TestSprintFlowNonDryRunUsesConfiguredRuntime:464-595`, exercises an accept-time failure returned early — that path never reaches the ladder). Suite green, so nothing fails for this defect.

Regression test: table-driven test over one fixture workspace containing `projects/runtime-tools/`: assert `status`, `flow --to requirements --dry-run {,"--json"}`, and `review --json` all exit `ExitValidation` on a missing-ref error; plus a unit test feeding a `RefError` with a "runtime-x" candidate through the flow/review handlers asserting class 5.

---

## F2. Durable accept/finish failures demote from designed exit 6 to exit 4 / code `validation.workspace` — QA contradicts its own documented mapping

**Severity: medium-high · Confidence: high (live-reproduced)**

Claim: `beginDurableCLICommand` and `durableCLICommand.Finish` deliberately wrap persistence failures as `ExitRuntime` (`internal/app/durable_operations.go:57`, `:81`), but most call sites re-map the joined error through fallback mappers that match neither the type nor the text, so the process exits **4 (`validation.workspace`)** — the wrong subsystem signal — and for QA this directly contradicts `docs/cli-reference.md:363`.

Live repro (broken run-control store simulated by `.ultraplan` being a regular file):
```
ultraplan --workspace ws2 run list                        -> exit 6   (mapRunControlError default) ✓ docs
ultraplan --workspace ws2 sprint p 01 qa cancel --run abc --json
                                                          -> exit 4, error.code "validation.workspace"
ultraplan --workspace ws2 sprint p 01 qa resume --json    -> exit 4, error.code "validation.workspace"
```
stderr in both QA cases: `sprint.qa: run-control.open: validate: run_control_directory must be a real directory…`. cli-reference.md:363 promises "runtime or persistence failures 6" for exactly this surface, and the envelope's stable `error.code` is part of the declared-stable QA JSON (cli-reference.md:485).

Source path:
- QA arm: accept failure → `runErr = durableErr` (`sprint_commands.go:419-423`); finish/status failures join into `runErr` (`:442-447`) → `mapQACommandError` (`:662-679`): not ctx-cancelled, not `sprint.QAError` → falls to `mapSprintError` default → `classedError{class:4}`.
- Finish-only demotion generalizes: after successful work, `finishDurableCLICommand` returns `errors.Join(nil, classifiedCause(ExitRuntime,…, "run-control.finish"))`. "run-control.finish" contains no `"runtime"` substring and matches no sentinel, so flow (`:272`), execute (`:366`), review (`:510`), qa (above), and study (`study_commands.go:354-363` → `mapStudyExecutionError` default) all re-wrap as ExitWorkspace(4). The ExitRuntime classification built at `durable_operations.go:81` never survives to the exit code anywhere.
- Counter-evidence checked and rejected: `fail()` uses `errors.As`, and `errors.Join` trees do contain the inner classedError{6} — but each mapper wraps the join in a fresh outer `classedError{4}` before `fail()` sees it, and `errors.As` stops at the outermost match. Verified empirically (exit=4).

Bad outcome: operators see "workspace or filesystem error" for a durability/persistence incident; monitoring and rerun policy keyed on class 6 never fires; the durable row says failed/persistence-lost while the shell reports a workspace problem. Also internally inconsistent: the identical fault is 6 via `run list` and 4 via `qa cancel`.

Why current verification allows it: the inventory test (`run_control_inventory_test.go`) pins only call-site counts/kinds by grepping source; no test asserts exit classes for accept-failure outside the single flow case (`TestSprintFlowNonDryRunUsesConfiguredRuntime`), none covers finish-failure exit codes at all, and no storage/run-control-fault tests exist for the qa/study arms.

Regression test: with `.ultraplan` replaced by a file, assert `qa cancel --run x`, `qa resume` exit `ExitRuntime` with `error.code` starting `provider.runtime`; add a finish-failure seam test per durable verb asserting class 6 when work succeeded but terminal persistence failed.

---

## F3. SIGINT during `sprint execute` exits 8 (partial), not 7 (cancellation); undocumented divergence inside one verb family

**Severity: low-medium · Confidence: high on mechanism (code-path proven; not live-driven)**

Claim: cancellation of a non-dry-run execute is reported as partial completion.

Path: on ctx cancellation `Service.Execute` marks the running task `Cancelled` and discards the runtime's `context.Canceled` (`internal/sprint/execute.go:296-298` — the `ctx.Err() != nil` case precedes `runErr != nil`), breaks the loop (`:320-322`), then returns `fmt.Errorf("execute completed with failed tasks")` because `hasFailedExecuteTask` counts cancelled tasks (`:750-757`, `:335-336`). The CLI ladder maps that text to `ExitPartial` (`sprint_commands.go:360-362`) before `mapSprintError`'s cancellation→7 branch can ever see the cause.

Contrast: `flow` under SIGINT propagates `context.Canceled` → exit 7 (`sprint_commands.go:948-949`); study run-loop cancellation is pinned to 7 by `study_run_loop_commands_test.go:199`. Docs define `7: cancellation`, `8: partial completion` globally (`cli-reference.md:29-30`); only QA's cancellation→8 is documented (`:363`). No doc or test states execute's behavior either way.

Bad outcome: wrappers that treat 7 as "user aborted, clean stop" and 8 as "work products exist, inspect them" misclassify an abort as having produced authoritative partial results.

Counter-evidence searched: no compensating translation downstream (`finishDurableCLICommand` joins but does not re-classify; `FinishOperation` records TerminalCancelled internally while the process code stays 8). No test pins execute-cancellation exit.

Regression test: drive `execute` with a cancellable fake runtime, cancel mid-run, assert exit 7 (or, if 8 is intended, pin it and document it next to the QA paragraph).

---

## F4. `serve` usage errors print full help to stdout and duplicate the flag error on stderr — unique among the 15 verbs

**Severity: low · Confidence: high (live-reproduced)**

Claim: `ultraplan serve --bogus` exits 2 but writes the entire help text to **stdout** and two stderr lines (`flag provided but not defined: -bogus` from the flag package via `fs.SetOutput(deps.stderr)`, then `serve: …` from `fail()`), because `flag.ContinueOnError` calls `fs.Usage` on parse failure too (`serve_commands.go:19-28`; custom `Usage` writes to stdout at `:23`).

Repro: stdout 640 bytes of help + stderr 2 lines, exit 2. Contrast probe `health --bogus`: stdout empty, one stderr line. Every other verb emits nothing on stdout for usage errors (pinned e.g. `app_test.go:72-83` for the top level).

Bad outcome: `out=$(ultraplan serve …)` indistinguishable from success-help; stderr-line-count-based alerting trips twice.

Why verification allows it: `serve_commands_test.go` passes `Stdout: nil` (→ io.Discard) in every error-path case, so the suite structurally cannot observe stdout pollution; only the happy `-h` path asserts stdout content.

Regression test: run serve with an invalid flag and buffers on both streams; assert stdout empty and exactly one stderr line.

---

## F5. Declared-stable `sprint verify --json` failure envelope emits `"status": ""` — an out-of-vocabulary value

**Severity: low · Confidence: high (live-reproduced)**

Claim: the verify handler builds the envelope label from `result.Verification.Assessment` even when `Verify` failed before populating `Verification` (`sprint_commands.go:313-318`), emitting `{"operation":"sprint.verify","status":"",…,"error":{…}}`. Reproduced: missing project ref → exit 5, `status: ""`. phase3-json-schemas.md defines assessment values (pass/fail/blocked/incomplete…) — "" is not one of them, and `sprint verify --json` is in the stable-surfaces list (`cli-reference.md:487`). Same pattern can emit stale labels elsewhere (probe: `review --json` reported `"status":"ready"` while failing with exit ≠ 0).

Bad outcome: consumers keying on the documented `status` enum get an undefined value on the exact surfaces the docs promise to keep compatible.

Regression test: force a typed verify failure (bad ref) with `--json`; assert `status` is a defined failure label consistent with the emitted `error`.

---

## Verification-gap observations (fold into fixes above, not standalone defects)

- `storage migrate` has zero test coverage (no storage test file; no references in app tests) despite owning file+SQLite mutation and the ExitPartial contract.
- No test anywhere asserts text-vs-JSON exit-class parity, finish-persistence exit classes, or stderr line counts; several "pinning" tests pass nil streams (io.Discard), hiding real stream behavior (F4).
- `TestEveryRuntimeBackedCLIEntryUsesDurableAcceptanceInventory` greps source text for call-site counts — it verifies wiring exists, not that acceptance/classification behaves.

## Defended non-issues (investigated, counter-evidence found)

- **QA cancellation/deadline → 8 vs generic → 7**: explicitly documented for QA (`cli-reference.md:363`); intentional, though cross-family reconciliation guidance remains absent (context-pack unknown #2).
- **Empty alias digest making dedup unreachable**: `AcceptOperation(..., "")` at the only CLI entry (`durable_operations.go:55`) is coherent with per-invocation CLI semantics; web/TUI pass digests separately.
- **PascalCase fields in `sprint flow/metrics --json` results** (`FlowResult` lacks json tags): ugly but both verbs are deliberately excluded from the stable-surfaces list (`cli-reference.md:474-491`); `status --json` (stable) uses tagged snake_case DTOs.
- **TTY-less confirmations** (defaults/skills/run-loop reset): conservative on EOF, gated flags documented (`--force`, `--reset --yes`); piped-stdin behavior pinned by `app_test.go:194` and run-loop tests.
- **`orderRunArgs` reorder table**: unknown dash-tokens become positionals but then fail `fs.Parse` → usage; `--flag=value` handled via SplitN. No reachable misparse found.
- **`writeStatus`/`fail` write-failure branches → ExitError(1)**: matches docs' "internal or write error"; heterogeneous other sources of 1 are a docs-nuance, not a defect.
- **Health precedence cfgErr → runtimeFailed → structure**: matches documented precedence; JSON emitted before failing is intended dual-channel behavior shared with qa/verify.
- **Best-effort `_ =` encode on failure envelopes**: broken-pipe drops the body but preserves the exit code; acceptable given stderr carries the truth.
