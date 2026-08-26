Context pack notes it reviewed `8eef70f` but my assignment freezes `50d6f0d` (= current HEAD). Let me verify the working tree matches and then dig into sources.Working tree is clean and the `8eef70f→50d6f0d` delta touches only `internal/sprint/smoke_author.go` — so app-package sources match what the pack described. Now reading core sources.Now the supporting infrastructure files.Now the authoritative contract doc and tests.The TUI-side test explicitly rejects text-based classification while CLI ladders use it — let me trace how real provider failures surface as error strings.Time for empirical probes. Setting up a scratch clone of the frozen commit (target repo untouched).Both probes confirmed. Let me check what real provider-failure messages look like (agentwrap SDKError) to size the blast radius of the substring sniffing.`SDKError.Error()` prints categories like `provider_unavailable`/`authentication` — most contain no "runtime" substring. Let me verify what a realistic provider-outage error does in plain mode, and how sprint wraps StartRun errors.Exit-code misclassification fully confirmed against realistic provider-error shapes. Now probing the Ctrl-C behavior during interactive confirmation.SIGINT hang confirmed empirically. Now checking the remaining candidates: diagnostics health-status values, study_runs door, and the serve usage-output quirk.All evidence gathered. Cleaning up probe artifacts and writing the final report.# Review Report: `cli-dispatch-exit-contract`

Target: ultraplan-go @ `50d6f0d` (frozen). All findings verified against source at that commit; probes run in a scratch export (`/tmp/opencode/gauntlet`); target repo and workspace untouched.

---

## Findings

### F1 — Provider/runtime failures are misclassified as exit 4 (workspace) or 8 (partial) instead of the documented 6 (runtime/provider)

**Claim.** The sprint dispatch ladders classify runtime-backed failures by substring-sniffing `err.Error()` for `"runtime"` (`sprint_commands.go:269`, `:363`, `:507`) instead of typed classification. Real provider failures surface as agentwrap `SDKError` strings formatted `"operation: category: detail"` (agentwrap@`dccd575/errors.go:51-62`) whose categories — `provider_unavailable`, `model_unavailable`, `authentication`, `rate_limit`, `timeout`, `configuration`, `permission` — contain no `"runtime"` substring. Sprint planning flows propagate those errors raw (`service.go:604-608, 688-692, …`; no wrapping), so the substring branch misses them and `mapSprintError`'s default arm fires → **ExitWorkspace(4)**.

**Observable bad outcome.** A provider outage/auth failure during `sprint flow --to requirements|sprint-index|plan|…` or `sprint review` exits **4 "workspace or filesystem error"**, contradicting cli-reference.md:28 ("6: runtime/provider error"). Operators and scripts are directed to diagnose workspace files instead of the provider, and monitoring keyed on class 6 never fires.

**Execute is worse:** `Service.Execute` never returns the runtime error — it collapses per-task failures into `fmt.Errorf("execute completed with failed tasks")` (`execute.go:336`). The CLI ladder checks `"failed tasks"` before `"runtime"` (`sprint_commands.go:360-365`), so a single-task execute killed by an auth failure exits **8 "partial completion"**. Class 6 is effectively unreachable for execute's dominant failure mode. Study run-all/run distinguish `RuntimeFailed→6` from `Partial→8` (`study_commands.go:732-745`), so the same outcome shape gets different classes across families.

**Trigger/preconditions.** Any runtime-backed sprint verb with a failing provider/executable whose error text lacks `"runtime"`.

**Evidence (executed probe, scratch clone).** Stub runtime returning `errors.New("run: provider_unavailable: upstream returned 503")`; fixture workspace as in `TestSprintFlowNonDryRunUsesConfiguredRuntime`:
```
plain:  status=4  stderr="…\nsprint.flow: run: provider_unavailable: upstream returned 503\n"
json:   status=4  stdout={…"status":"failed"…}   (no "error" object)
```
With message `"runtime backend refused connection"`: plain exits **6** (substring hits), confirming the branch is purely lexical.

**Counter-evidence searched.** Accept-time persistence failures do exit 6 (`beginDurableCLICommand` → `classifiedCause(ExitRuntime)` at `durable_operations.go:57`, pinned by `TestSprintFlowNonDryRunUsesConfiguredRuntime`) — so 6 *is* produced for start-up DB problems but not for actual provider failures, inverting the documented meanings. The TUI/web path explicitly rejects this technique (`TestFailedOperationDoesNotClassifyFromErrorText` feeds `"runtime validation lock provider missing"` and asserts it does **not** classify from text), demonstrating the project's own invariant that the CLI ladders violate.

**Severity:** high (operator-contract correctness on the highest-risk verbs). **Confidence:** high (code path + executed probe).

**Regression test:** table-driven dispatch test over stub sprint runtimes returning SDKError-shaped messages (`provider_unavailable`, `authentication`, `timeout`) asserting exit 6 for `flow` (plain and `--json`), `review`, and `execute`; plus assert `flow --json` and plain agree for identical causes.

---

### F2 — Identical failure exits 6 without `--json` but 4 with `--json` (flow-only divergence)

**Claim.** In the flow ladder (`sprint_commands.go:256-273`), the `"runtime"` substring branch exists only on the non-JSON path; the JSON path returns `mapSprintError(...)` directly. An error containing `"runtime"` therefore classifies differently by output flag alone.

**Bad outcome.** Scripts consuming machine output get exit 4 + code `validation.workspace` for provider/runtime failures; humans running the identical command get 6. Also, the flow `--json` failure envelope carries no `error` object (probe output keys: `schema_version/operation/status/result` only), unlike verify/qa, so JSON consumers get neither a stable code nor the right class.

**Evidence.** Executed probe (same fixtures):
```
plain: status=6 ; json: status=4  (message containing "runtime")
```
**Counter-evidence.** flow/metrics envelopes are outside the declared stable list (cli-reference.md:474-491), but the exit-class table (:18-30) is global and unconditional.

**Severity:** medium. **Confidence:** high (executed probe).

**Regression test:** one command run twice (± `--json`) against a failing stub runtime; assert equal classes.

---

### F3 — Durable-finish persistence failure downgrades successful commands to exit 4 and reports them "failed"

**Claim.** `finishDurableCLICommand` wraps finish errors as `classedError{ExitRuntime, "run-control.finish"}` and joins them into the run error (`durable_operations.go:86-91`). Every durable call-site ladder then re-wraps the joined error from scratch, discarding that class: the joined text (`"run-control.finish: …"`) contains no `"runtime"` substring, so flow/review fall to `mapSprintError` default → **ExitWorkspace(4)**; execute/study ladders likewise (`mapStudyExecutionError` default → 4). Only accept-time failures keep class 6 — the same DB fault yields 6 at start and 4 at completion.

**Bad outcome.** A flow/verify/review/execute/study operation whose work fully succeeded and mutated the workspace exits 4 ("workspace/filesystem error") when terminal persistence fails (quota-reached DB, disk full, >30 s lock). With `--json`, flow emits `"status":"failed"` for work that actually completed (`sprint_commands.go:259` runs because the joined err ≠ nil), inviting destructive reruns. `Finish`'s own intent (class 6) is dead at every one of the 10 inventory-pinned call sites.

**Trigger/preconditions.** `ProposeTerminal` fails after the operation context is still live (e.g., hard-quota reached per `sqlite.go:907`, I/O error); reachable in production, not simulated by any test (inventory test pins call sites only).

**Evidence (executed probe).** Stub repository where `Accept/Claim/Append` succeed and `ProposeTerminal` fails:
```
joined text: "run-control.finish: attempt to write a readonly database" (contains 'runtime': false)
finish wrapper class: 6  →  dispatch ladder class observed: 4
```
**Counter-evidence searched.** No doc defines finish-failure semantics (grep of docs/*.md negative); nothing rescues the inner class — `fail()`'s `errors.As` sees the outer re-wrapped `classedError` first.

**Severity:** medium (conditional on storage fault, but misleading-failure on successful mutations). **Confidence:** high mechanism, medium-high reachability.

**Regression test:** stub `ProposeTerminal` to fail; run non-dry-run flow; assert exit 6 and (for `--json`) that status/error identify `run-control.finish`, not workspace validation.

---

### F4 — SIGINT/SIGTERM is ignored while confirmation prompts block on stdin; process hangs until input arrives

**Claim.** `confirmOverwriteDefaults` (`defaults_commands.go:131`), `confirmOverwriteSkills` (`skills_commands.go:154`), and `confirmRunLoopReplacement` (`study_commands.go:304`) block in `bufio.Reader.ReadString('\n')` and never consult `deps.ctx`. main.go converts SIGINT/SIGTERM into context cancellation only (`main.go:19`), so during a prompt the signal is swallowed and the read keeps waiting.

**Bad outcome (reproduced).** Built binary; customized `prompts/base.md`; ran `defaults install --path W` with piped stdin:
```
after SIGINT rc = None (still running)
final rc after EOF = 0    # only exits once stdin closes
```
An operator pressing ^C at "Type yes to overwrite:" sees nothing happen; wrappers that feed stdin lazily hang indefinitely. Same structure in all three prompts (run-loop reset prompt also prints to stdout and can end ExitPartial).

**Counter-evidence.** EOF/non-affirmative answers take the conservative branch, matching docs ("A negative or empty answer keeps customized files", cli-reference.md:84) — the defect is the ignored cancellation, not the conservative default.

**Severity:** medium-low (interactive availability; automation stalls). **Confidence:** high (executed repro).

**Regression test:** inject a pre-cancelled ctx and a blocking stdin reader; assert the prompt path returns promptly (ExitCancel) instead of blocking.

---

### F5 — Global `--workspace` is silently ignored by `init-workspace`; scaffold lands in the current directory

**Claim.** `parseGlobalFlags` extracts `--workspace` from anywhere in argv (`app.go:198-220`) and `discoverWorkspace` honors it, but `runInitWorkspace` seeds `path := deps.workDir` and never reads `deps.workspaceFlag` (`workspace_commands.go:11-32`), unlike `defaults install`/`skills materialise` which do (`defaults_commands.go:27-29`, `skills_commands.go:27-29`).

**Bad outcome (reproduced).** `ultraplan --workspace /tmp/opencode/ws init-workspace --dry-run` prints `Workspace: /tmp/opencode/cwdtest` — plans writes into cwd while the user explicitly selected another path. Non-dry-run creates files in the wrong directory, exit 0. The defaults doc explicitly promises workspace-flag fallback (cli-reference.md:76); init-workspace's silent divergence has no documented rationale.

**Severity:** low-medium (wrong-place mutation with explicit user intent). **Confidence:** high on behavior; intent ambiguity acknowledged (init "creates new" vs `--workspace` "selects existing") — if intentional, it should be a flagged arg error, not silence.

**Regression test:** `--workspace A init-workspace --dry-run` must plan into A or fail with usage; must not report B.

---

### F6 — `run diagnostics --json` switches the shared-envelope `status` vocabulary and exits 0 on repository failure

**Claim.** `runDiagnostics` sets the envelope's top-level `status` to `string(health.Status)` (`run_commands.go:264`) instead of the family convention (`"ok"/"fail"` = command outcome, cf. `config show` example cli-reference.md:96-107, health, study validate/status). Repository health can be `"degraded"`/"failed"` (hard quota reached, `sqlite.go:906-910`) with a nil error, so the command exits **0** while emitting `"status":"failed"`.

**Bad outcome.** Within one envelope family, `status` means two things: consumers validating `.status == "ok"` treat a healthy diagnostic run as failed; consumers trusting the exit code miss quota-breach entirely. Text mode equally prints `Status: failed` and exits 0.

**Severity:** low (verb not in the stable list, but it is part of the documented envelope family and the field meaning silently flips). **Confidence:** high on behavior; medium-high that it is a defect rather than intent.

**Regression test:** pin diagnostics envelope status values + exit-code pairing (or exit nonzero when health is failed).

---

### F7 — Per-verb admission grammar/stream discipline diverges from the documented global flags

**Claim.** Three concrete divergences from cli-reference.md:14 ("`-h`, `--help`: show help") and the empty-stdout-on-failure norm pinned for other verbs:
1. `serve` installs `fs.Usage` writing full help to **stdout** (`serve_commands.go:20-23`), so a flag error produces help on stdout + one stderr line + exit 2 (reproduced: `serve --listen`), whereas sprint/project usage failures keep stdout empty (`TestSprintMalformedArgumentsUseUsageExit`).
2. `sprint p s qa status --help` → `unknown QA argument "--help"`, exit 2 (reproduced): `parseSprintQAArgs` pre-checks only `args[0]`.
3. `study <s> run <dim> <src> --help` → `unknown argument "--help"`, exit 2 (reproduced): help accepted only as the sole argument.

**Bad outcome.** Scripts capturing stdout on failure receive a help page where every other verb emits nothing; users requesting help on valid subcommands get usage errors.

**Severity:** low. **Confidence:** high on behavior (all reproduced); medium-high defect vs. intent.

**Regression test:** matrix test asserting `--help/-h` acceptance positions and stdout-empty-on-usage-error across all 15 verbs.

---

## Defended / non-issues (searched counter-evidence)

- **QA cancellation/deadline → 8 while generic sprint cancellation → 7:** explicitly documented (cli-reference.md:363); test-pinned (`TestQACommandErrorClassesAndStableCodes`). Not a defect.
- **`sprint status --json` label "partial" + exit 0 on catalog/review_gate readiness:** consistent with phase3 "Cancellation and unavailable prerequisites remain distinct from pass" (label ≠ ok/pass); inspection succeeded.
- **EOF at confirmation treated as refusal:** matches cli-reference.md:84; conservative and deterministic for piped automation (distinct from F4's ignored signal).
- **Multi-line stderr via `errors.Join`** (qa run/resume double failure; finish joins): docs promise stderr errors, never "exactly one line"; not charged.
- **Empty alias digest makes CLI dedup unreachable** (`durable_operations.go:55`): deliberate single-host regime; conflicts surface as classed accept failures; ownership lives in durable-operation-spine.
- **`orderRunArgs` reordering:** probed edge shapes produce correct usage exits; `--limit` bounds enforced downstream (1..200 → ErrInvalidArgument → exit 2), matching docs.
- **health precedence cfgErr(3) > runtime(6) > structure(5):** all checks reported in-envelope; no doc contradicts.
- **Redaction/displaySafe coverage** on JSON-embedded messages and progress streams: present where required (review progress callback omits redaction, but its content is app-generated coverage IDs — no attacker-controlled source identified).
- **`run follow` exit 0 on interrupt:** explicitly documented observation-only semantics.
- **`code --output` not containment-checked:** local-trust tool; docs promise no containment (unlike study prompt, which checks via `ResolveInside`).
- **Coverage gaps noted neutrally by the context pack confirmed real** (no direct tests for substring branches, flow `--json` success fields, storage migrate, writeStatus/fail write-failure arms) — recorded here as absence-of-tests evidence supporting F1-F3, not independent defects.