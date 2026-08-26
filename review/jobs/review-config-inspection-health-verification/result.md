The context pack is only a summary. Inspecting source directly now.Now the runtime package to trace how config feeds sandbox/permissions/health.Now checking the docs contract, main entrypoint/version wiring, and workspace validation.Baseline: build and run the surface tests, then check downstream `DefaultParallel` consumers.Now empirical verification. I'll clone the repo into `/tmp/opencode` (leaving the target untouched) and add scratch tests there.Three candidates confirmed empirically. Now verifying the version-wiring candidate and checking for any build-time stamping mechanism.One exploratory scratch test of the completely-uncovered `Adapter.Health` path (fake `HealthChecker`) to hunt for bugs the current verification allows:The third scratch test failed due to my own harness (embedded fake always implements `CheckHealth`) — the first two confirm the mapping logic. One last mechanical check for the empty-`required_health` path:Review complete. Target repo was left untouched (note: an external actor advanced HEAD `50d6f0d`→`deaf158` and edited `internal/sprint/*` mid-review; none of my findings' files were affected). All reproductions ran against a scratch clone in `/tmp/opencode/ultraplan-go-clone`.

# Findings — `config-inspection-health`

## F1. Workspace YAML parser silently discards unmatched list items (fail-open admission)
- **Claim**: In `loadFile`, a `- item` line is routed through `switch listField` (internal/platform/config/config.go:225-244) which has **no default case**; whenever no list header is active (`listField == ""`) or the field isn't one of the five known lists, the line is silently dropped.
- **Bad outcome**: A workspace config line vanishes with zero diagnostics and loading succeeds. Reproduced: `models:\n  - workspace/default` → `err=nil`, `models.default="provider/model"` (placeholder), `source="default"`. Runs then execute against a placeholder provider/model and fail far from the misconfiguration site. Same silent swallow for an item appended after a scalar inside a section (`smoke:\n  run_timeout: 10m\n  - oops`).
- **Trigger**: Any mistyped list under a scalar key, item before its header, or column-0 list header (config.go:213 requires leading whitespace > 0 to recognize a nested list header; otherwise the header itself becomes a bogus *section* and its items are dropped).
- **Evidence**: config.go:225-245 (switch without default → `continue`); contrast scalar unknowns which fail closed ("unknown config field", config.go:420), as promised by docs/configuration.md ("Unsupported fields are rejected"). No test covers stray items.
- **Counter-evidence searched**: downstream `Validate` does not catch it because defaults fill every required field — the run proceeds "healthy".
- **Severity**: Medium. **Confidence**: High (reproduced).
- **Regression test**: `Load` with `models:\n  - x` must return an error naming the offending line; item-under-scalar and column-0-header cases included.

## F2. Clearing a list via empty header leaves provenance claiming `"default"`
- **Claim**: An empty list header (`required_health:` with no items) calls `clearListField` (config.go:216, 255-258) which resets the default-populated slice to nil, but never sets `Sources[field] = "workspace"` (only per-item appends do, lines 230-241).
- **Bad outcome**: Reproduced: `agentwrap.required_health:` (empty) → value `[]` (vs. 3-item default), `Sources["agentwrap.required_health"]=="default"`. Same for `smoke.environment`. `config show --json` — a stable, script-consumed surface whose stated purpose is source metadata (docs/configuration.md:14) — reports the wrong origin for a value that differs from default.
- **Trigger**: Any empty list header in `ultraplan.yml`.
- **Evidence**: config.go:211-223 (clear path sets no source), 227-243 (source set only on items).
- **Severity**: Low-Medium (misleads exactly the debugging flow provenance exists for). **Confidence**: High (reproduced).
- **Regression test**: after loading a config with an emptied list, assert `Sources[field] == "workspace"`.

## F3. Emptying `agentwrap.required_health` silently flips health to the strictest possible gate
- **Claim**: `Validate` accepts an empty list (config.go:517-521 iterates nothing). `RequestFromConfig` copies nil (internal/platform/runtime/runtime.go:551); `Adapter.Health` sends empty `Checks`/`RequiredChecks` (internal/platform/runtime/health.go:47,51,55-62); agentwrap's OpenCode adapter then expands empty `Checks` to **all eight** default checks including `provider`/`authentication`/`model` probes (agentwrap/opencode/health.go:47-59), and `RequiredHealthFailure` with empty `required` treats *every result* as required (`explicitRequired=false` path, agentwrap/health.go:121-131).
- **Bad outcome**: An operator who empties the list to relax runtime gates gets the opposite: probes like `opencode providers list provider` run against the placeholder split of `models.primary="provider/model"` (runtime.go:542, `splitModel`) and fail on any real machine → `health` exits 6. Provenance simultaneously claims "default" (F2), so the change looks self-inflicted-by-defaults.
- **Trigger**: `required_health:` emptied in workspace YAML; docs document no empty-list semantics (docs/configuration.md only shows populated lists; Validation section doesn't reject it).
- **Counter-evidence searched**: scaffold always populates the list (internal/workspace/init.go:171-175); no consumer treats empty as "skip runtime checks"; behavior is stricter, not weaker, so no security hole — but it is undocumented, unvalidated semantics inversion.
- **Severity**: Medium-Low. **Confidence**: High on mechanics (code-traced end-to-end), Medium on intent.
- **Regression test**: `Validate` should reject an explicitly emptied `required_health` (or the empty form must be defined and documented); assert either way.

## F4. Redaction skips `agentwrap.extra_args`
- **Claim**: `redactAgentwrap` redacts only `Executable` and `Env` (internal/platform/config/redaction.go:72-78); `ExtraArgs` passes through raw into `Redacted` and is printed verbatim by the stable `config show --json` surface (internal/app/config_commands.go:41-43).
- **Bad outcome**: Reproduced: `ExtraArgs=["--api-key=sk-live-abc123", "--provider-header=Bearer ghp_xyz"]` survive `Redact` unchanged while the equivalent `Env` item becomes `[REDACTED]`. `Sensitive()` already carries arg-form markers precisely shaped for this (`"--key"`, `" key="`, redaction.go:30) — they are just never applied to the one field whose contents are CLI arguments passed to the runtime subprocess (opencode.go:34-39).
- **Trigger**: Secret-bearing extra arg in `ultraplan.yml`; visible to anyone reading terminal scrollback/CI logs of `config show --json`.
- **Counter-evidence**: docs advise against tokens in YAML (configuration.md:234) and text-mode `config show` omits extra_args entirely — leak is `--json`-only. Still contradicts "redacts sensitive-looking values in config … output" for a defense the same file implements for sibling free-form fields.
- **Severity**: Low-Medium. **Confidence**: High (reproduced).
- **Regression test**: `Redact` must apply `RedactValue("agentwrap.extra_args", ...)` per item; assert `--api-key=…` → `[REDACTED]`.

## F5. Version metadata cannot be stamped; `version` always reports placeholders
- **Claim**: `Version` values come from package consts (internal/app/version.go:5-9) wired only via `app.DefaultVersion()` in main (cmd/ultraplan/main.go:20). There are no package-level `var`s for `-ldflags -X`, CI builds plain `go build` (.github/workflows/ci.yml:27), and the release checklist's cross-compile commands have no ldflags either (docs/release-checklist.md:53-56).
- **Bad outcome**: Verified by building and running: `Version: 0.0.0-local / Commit: local / BuildDate: local` for any build, including releases. The checklist acceptance item "`ultraplan version` reports intended build metadata" (release-checklist.md:151) is unmeasurable as wired, and support/diagnostics cannot identify a deployed binary — an operability gap on one of this surface's three entrypoints.
- **Severity**: Low. **Confidence**: High.
- **Regression/fix verification**: convert consts to vars, stamp via ldflags in CI/release scripts, add a test asserting non-placeholder output when `Config.Version` is injected.

## F6. Health probes have no deadline anywhere (`Timeout` is a dead parameter)
- **Claim**: `HealthRequest.Timeout` (internal/platform/runtime/health.go:14) is never read; `Adapter.Health` never sets `agentwrap.HealthCheckRequest.Timeout` (health.go:55-62; agentwrap applies it only if >0, opencode/health.go:18-22); `runRuntimeHealth` passes the caller's context, which in production is the bare signal context (cmd/ultraplan/main.go:15-17).
- **Bad outcome**: Every probe shells out to `opencode` sequentially (up to 8 checks given F3's expansion). One wedged subcommand hangs `ultraplan health` indefinitely — a command docs position for scripted use (cli-reference.md:74 "script-friendly"), where no human Ctrl-C exists.
- **Counter-evidence searched**: probes get no stdin and bounded output capture, but nothing bounds wall-clock; no timeout is imposed by `runHealth` either.
- **Severity**: Low-Medium (operability). **Confidence**: High on dead parameter/facts, Medium on real-world hang frequency.
- **Regression test**: fake `HealthChecker`/process runner that blocks; assert health returns within a configured deadline with a classified failure.

## F7. Verification gap: the health aggregation layer has zero direct coverage
- **Claim**: All four app-level health tests stub `runtimeHealthChecks` (internal/app/health_commands_test.go:104-111), and `fakeRuntime` in the runtime package implements only `StartRun`/`Capabilities`, so `NewAdapter` leaves `health=nil` and `Adapter.Health` is never executed by any test; `runRuntimeHealth`'s report→check mapping is equally unexercised.
- **Bad outcome**: The code that decides capability gates and status mapping (runtime/health.go:43-103) — logic that feeds exit codes and the stable `health --json` envelope — can regress silently. I exercised it myself with a fake `HealthChecker`: capability-downgrade (`sessions` unsupported → error + status `unsupported`) and degraded-required-failure both behave correctly, so this is unprotected rather than currently-broken; F1-F3 each also lack a failing-test witness, which is how they survived.
- **Severity**: Low-Medium (per review lens: fake-only confidence). **Confidence**: High (verified by exhaustive grep plus filling the gap with scratch tests).

## Defended non-issues (counter-evidence found)
- **Capability-probe swallow branch** (`if caps, err := a.Capabilities(ctx); err == nil`, runtime/health.go:74) looks fail-open but is unreachable for OpenCode: `Capabilities()` returns a static table and never an error (agentwrap/opencode/options.go:132-157).
- **Warn-status check with exit 0** is impossible: `Checks == RequiredChecks` and `RequiredHealthFailure` fails required checks on degraded/unknown/unsupported/transient (agentwrap/health.go:138-156).
- **Exit precedence** config(3) > runtime(6) > structure(5) matches implementation; `validation.Issues[0]` is safe (issues non-empty iff invalid, workspace/validation.go:28).
- **Unbounded upper side of `execution.default_parallel`/`default_retries`** matches the current documented validation contract (configuration.md "Validation" enumerates exactly what is rejected); consumers enforce ≥1 (study_commands.go:327). Watch item, not a defect.
- **Parser edges fail closed**: CRLF, BOM, single quotes, inline comments, oversized lines (>64KiB scanner limit), unreadable `ultraplan.yml`, empty scalars for required fields — all produce load/validation errors. (`filesystem.read: ok` asserts existence, not readability, but a subsequent unreadable file correctly fails `config.validation`.)
- **QA lower-only bounds, malformed-env attribution, precedence, double redaction, env summary counting**: implemented as specified and genuinely tested (config_test.go covers max/zero/negative per field and failed overrides don't claim env source).