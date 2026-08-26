Context pack is just a summary. Inspecting the actual source directly.Now checking the JSON envelope, main wiring, scaffold defaults, and docs.Now let me verify my parser/redaction hypotheses empirically. I'll copy the config package into a scratch dir (target stays untouched) and probe it.Confirmed four hypotheses. Now checking how agentwrap treats unvalidated `agentwrap.sandbox`/`permission_mode` values, since config `Validate` skips them.Now checking the authoritative planning workspace contracts for this surface.Baseline test run for the surface packages:Final verification pass — checking documented exit-code semantics and confirming no counter-evidence remains for the cancellation finding.# Review Report: `config-inspection-health` (failure/concurrency lens)

Target: `ultraplan-go@50d6f0d`. All hypotheses below were probed empirically against a scratch copy of the package (`/tmp/opencode/configprobe`); target tree untouched. Baseline tests pass (`go test ./internal/platform/config/... ./internal/platform/runtime/... ./internal/app -run 'TestHealth|TestConfig'`).

---

## Findings

### F1 — `agentwrap.extra_args` bypasses secret redaction in `config show --json` (Medium / High confidence)

**Claim.** `redactAgentwrap` redacts `Executable` and each `Env` entry but never touches `ExtraArgs`, so secret-bearing runtime args pass verbatim into the redacted projection that `config show --json` marshals (internal/platform/config/redaction.go:72-78; internal/app/config_commands.go:41-44).

**Bad outcome.** A user who passes a credential via extra args (the same class of free-form injection channel as `env`, which *is* redacted element-wise) gets it printed in full by the command whose documented purpose is safe inspection; output is routinely pasted into issues/CI logs. Proven:

```
[redact-extra-args] [--header=Authorization: Bearer sk-test-secret]   <- verbatim
[redact-env]        [[REDACTED]]                                      <- sibling field
```

**Trigger/preconditions.** Secret-looking string anywhere in `agentwrap.extra_args`; run `ultraplan config show --json` (text mode omits the field entirely, so only JSON exposes it).

**Evidence & path.** `Redact` → `redactAgentwrap` (redaction.go:25,72) leaves `a.ExtraArgs` unfiltered; `writeJSON(..., redacted)` serializes `Agentwrap.ExtraArgs` (json tag `extra_args`). `config.Sensitive()` already matches `"bearer "`/`"sk-"`/`"--key"` (redaction.go:30-39) — the detection exists, it just is not wired for this field.

**Counter-evidence searched.** Docs warn "Do not place provider tokens in ultraplan.yml" (docs/configuration.md:234) — but the same sentence promises "UltraPlan redacts sensitive-looking values in config … output", and the identical warning applies to `env`, which is defensively redacted anyway. No test covers ExtraArgs redaction (`TestRedactSensitiveValues` covers Models/Env only). Contract anchor: workspace `system/contracts/core/configuration.md` CFG-SECRET-001 (Blocker tier): "redact secrets in errors, diagnostics"; forbidden: "dumping all settings to diagnostics without redaction."

**Regression test.** Extend `TestRedactSensitiveValues`: `ExtraArgs = []string{"--header=Authorization: Bearer sk-x"}` must render `[REDACTED]` after wiring `RedactValue("agentwrap.extra_args", v)` into `redactAgentwrap`.

---

### F2 — Unknown/mistyped list-field headers silently swallow their items (Medium / High confidence)

**Claim.** In `loadFile`, an indented `key:` line whose `section.key` is not a recognized list field falls through to `section = key` (config.go:211-223); subsequent `- item` lines hit the `switch listField` with no default case and are silently discarded (config.go:225-244). Mistyped scalar fields error loudly ("unknown config field"), mistyped list fields do not.

**Bad outcome.** Proven: with `required_capabilites:` (typo) listing `- structured_events` / `- cancellation`, `Load` succeeds, keeps defaults, no error or warning:

```
[typo-list-field] OK ... reqhealth=[runtime_available] reqcaps=[structured_events cancellation]  <- defaults, silently
[toplevel-list-items] OK ...  <- top-level list items also silently dropped (listField == "")
```

User believes they customized gating fields (`required_capabilities`, `required_health`, `smoke.environment`, `agentwrap.env/extra_args`); effective config silently runs on defaults.

**Trigger.** Any misspelled or mis-indented (column-0) list header followed by items.

**Contract anchor.** docs/configuration.md:106: "**Unsupported fields are rejected with `unknown config field`**" — false for list-shaped unknown fields. Workspace CFG-TYPE-001 forbids "silently defaulting invalid values."

**Counter-evidence.** `Sources[field]="default"` is technically accurate and visible in JSON mode, but text mode prints sources for only a few fields, and nothing flags the unparsed block. No warning channel exists in `loadFile`.

**Regression test.** YAML with `agentwrap:\n  required_capabilites:\n    - x` must fail `Load` with "unknown config field" (or at minimum not silently ignore items); same for top-level `required_health:` + items.

---

### F3 — Inline `#` comments silently corrupt free-form values (Medium-Low / High behavioral confidence)

**Claim.** Only full-line comments are stripped (config.go:208); scalar values keep inline comments verbatim because the parser splits on the first colon and never trims trailing comments (config.go:246-250).

**Bad outcome.** Proven: `default: provider/model # primary model` yields `models.default="provider/model # primary model"` — accepted, sourced "workspace", shown by `config show`, and later used verbatim as the provider model ID so failures surface far from the cause. Validated fields fail loudly but confusingly (`discovery_timeout: 45s # note` → "must be a positive duration"). Quoted values are safe (`"provider/model#tag"` survives correctly), so the corruption specifically hits the common unquoted-with-comment idiom.

**Counter-evidence.** Shipped examples (scaffold init.go:137-175, docs/configuration.md) contain no inline comments — but `ultraplan.yml` is presented as YAML and users importing existing YAML conventions will use them; the parser accepts full-line comments, signaling intended comment support.

**Regression test.** `models.default: provider/model # comment` must load as `provider/model`.

---

### F4 — Dedent not tracked: valid YAML layouts rejected with misleading diagnostics (Low-Medium / High confidence)

**Claim.** `loadFile` never resets `section` on dedent (config.go:203-224), and any indented non-list `key:` line overwrites `section` with just `key` (line 221).

**Bad outcomes.** Proven, two shapes:
1. Top-level scalar after any section: `runtime:\n  default: opencode\nversion: 1` → `unknown config field "runtime.version"`. This is valid YAML; the file is rejected wholesale (fail-closed, so availability/diagnosability rather than integrity).
2. Indented empty non-list header: `agentwrap:\n  sandbox:\n  permission_mode: restricted` → `unknown config field "sandbox.permission_mode"` — error names a field that exists nowhere in the user's file.

The scaffold layout (init.go:137) happens to avoid both shapes, which is why tests don't catch it.

**Counter-evidence.** All misparses fail closed (never corrupt admission); canonical files load fine.

**Regression test.** `version: 1` placed last in the file must load (or produce a diagnostic naming the actual layout problem).

---

### F5 — SIGINT during `ultraplan health` exits 6 (runtime failure), not 7 (cancellation) (Low / High confidence)

**Claim.** On context cancellation mid-probe, agentwrap's `CheckHealth` returns a report with `transient_failure` results and a **nil** error (`opencode/health.go:59-66`), so `Adapter.Health`'s failure surfaces only via `RequiredHealthFailure` (runtime/health.go:94-95) wrapping `context.Canceled`. `runHealth` never inspects for cancellation and classifies any runtime-check error as `ExitRuntime` (health_commands.go:103-105).

**Bad outcome.** Ctrl-C during health prints `runtime.health: one or more runtime checks failed` and exits 6 — operator cancel is indistinguishable from genuine runtime breakage for scripts/retry wrappers. docs/cli-reference.md:29 documents "`7`: cancellation", and the repo consistently maps `context.Canceled` → `ExitCancel` elsewhere (study_commands.go:737, sprint_commands.go:948-949, serve_commands.go:62). The information needed for correct classification is present in the error chain and simply unused.

**Regression test.** Stub `runtimeHealthChecks` returning an error wrapping `context.Canceled`; assert exit 7 (after adding an `errors.Is(err, context.Canceled)` check before the `ExitRuntime` return).

---

### Observation (no fix required, liveness): health probes have no time bound

`HealthRequest.Timeout` is never set (runtime/health.go:55-62), so agentwrap's optional per-call timeout (`opencode/health.go:18-21`) is inert; probes run only under the process signal context. A hung `opencode` invocation (e.g., stalled provider enumeration) hangs `ultraplan health` indefinitely until SIGINT. Acceptable for an interactive CLI; worth a bound if health is ever wrapped by automation/TUI polling.

---

## Defended non-issues (hypothesized, then disproved)

- **Capability-gate fail-open in `Adapter.Health`:** `if caps, err := a.Capabilities(ctx); err == nil` skips required-capability checks on error (runtime/health.go:74). Counter-evidence: the OpenCode adapter's `Capabilities` returns a static map and never errors (`agentwrap@…dccd575/opencode/options.go:132-152`); every wrapper merely delegates. Unreachable for the only configured runtime.
- **Unvalidated `agentwrap.sandbox` / `permission_mode`:** `Validate` skips both enums (config.go:411-414). Counter-evidence: in the pinned agentwrap they are metadata-only placeholders (`PermissionMode`/`SandboxMode` "open placeholder"); enforcement flows solely from validated `permission_default` (`opencode/permissions.go:41-127`). Typo and correct value behave identically today — harden when enforcement lands (FUTURE-INTENT).
- **URL-ish `git.remote` colon mangling:** first-colon split would truncate `https://…`, but remote-name validation rejects it loudly (probed: "must use letters, digits …"). Fail-closed.
- **CRLF configs:** `bufio.Scanner.ScanLines` strips `\r`; CRLF files parse.
- **Empty list headers clearing defaults** (e.g., bare `required_health:`): explicit override semantics, consistent with `clearListField`; distinct from F2's typo path.
- **Double redaction of health messages** (`runRuntimeHealth` then `sanitizeHealthChecks`): redundant, idempotent, harmless.
- **`CLIOverrides.JSON` unread by `Load`:** callers own envelope rendering; no behavioral gap.
- **`version` always `0.0.0-local`/`local`:** main.go:26 explicitly passes `DefaultVersion()`; CLI contract says only "Print build metadata." No wiring exists to contradict; release-injection is future intent, not current defect.
- **Races/resource ownership:** `Load` is pure; `runControlState` is lazy and untouched by config/health; `runtimeHealthChecks` global seam is test-only and used sequentially; probe subprocesses are ctx-bound and killed on cancel.

**Summary:** 5 findings (F1–F5), 1 low observation, 9 defended non-issues. Highest priority: F1 (secret-redaction gap vs. explicit contract) and F2 (silent admission divergence from documented rejection promise).