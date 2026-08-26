The context pack is a stub (its detailed body didn't survive), so I'll rely on direct inspection. Reading the primary sources now.Context pack is thin, so direct inspection it is. Now tracing the security-critical downstream consumers: how `agentwrap.sandbox` / `permission_mode` flow into the runtime, plus the workspace helpers health depends on.`Sandbox`/`PermissionMode` pass through unvalidated into `agentwrap.SandboxMode`. Now I need the agentwrap library semantics — what an unknown mode does. Also checking `mapHealthIDs`/`mapPermissionPolicy`.Sandbox/permission_mode turn out to be metadata-only downstream. Now let me check the contracts in the planning workspace, plus tests and remaining app files.Now checking main.go version wiring and docs/configuration.md promises, then I'll build and run adversarial experiments.Docs claim `PATH`/`HOME` are not forwarded to smoke by default, but the code defaults include them. Checking consumers of `Smoke.Environment` before concluding.Doc/behavior divergence on smoke env defaults confirmed via history. Now verifying remaining candidates: `CLIOverrides.JSON` usage, and building the binary for adversarial runs.Binary builds. Running adversarial experiments now.All verification complete. Here is my independent review.

---

# Review: `config-inspection-health` (security/misuse lens)

Target: `ultraplan-go@50d6f0d`, workspace `@ab12dc38`. All findings below were reproduced against a locally built binary from the frozen commit (`go build ./cmd/ultraplan`). The provided context pack was a stub, so every claim is grounded in direct source/test/runtime inspection plus the authoritative workspace records.

## Findings

### F1 — Hand-rolled YAML parser silently discards mis-indented list blocks instead of rejecting them (Medium, High confidence)

**Claim.** In `loadFile`, entering list mode requires the header line to be indented (`internal/platform/config/config.go:213`: `leadingWhitespace(raw) > 0 && section != ""`), and `- ` items are dropped without error when `listField == ""` (`config.go:225-244`, switch with no default). A flat-style block therefore parses to exit 0 with the **defaults silently restored**.

**Demonstrated bad outcomes** (both reproduced):
- Workspace YAML `smoke:\nenvironment:\n  - PATH` (header not indented) → `config show` reports `smoke.environment: PATH, HOME, TMPDIR, LANG, LC_ALL` (source `default`), exit 0. The operator's forwarding restriction is widened to the full default allowlist; extra variables flow into smoke subprocesses.
- `agentwrap:\nrequired_health:\n- runtime_available\n- authentication` → reported as `runtime_available, structured_output, workdir`, exit 0. The operator's required **authentication** gate is silently absent from every subsequent runtime admission check (`RequestFromConfig` → agentwrap preflight).

**Trigger/preconditions.** Any `ultraplan.yml` where a list header or items lack indentation — a single editor slip. No warning anywhere; `Sources` claims `default`.

**Contract.** CFG-TYPE-001 (configuration contract, `system/contracts/core/configuration.md:85-88`) forbids "silently defaulting invalid values" and requires rejecting ambiguous parsing. The channel is explicitly untrusted admission (workspace YAML).

**Counter-evidence searched.** Unknown sections *with scalar children* do fail closed (`unknown config field`), so the parser is not uniformly lenient; the silent path exists only for list headers/items and value-less keys. Tests (`config_test.go`) cover only correctly indented input. A regression test loading flat-indented `required_health`/`smoke.environment` and asserting either an error or the configured values would prove a fix.

### F2 — Sprint-29 security tightening reverted in code while docs still assert it: `PATH`/`HOME` forwarded to smoke again by default (Medium, High confidence)

**Claim.** Sprint 29 (`ultraplan-workspace/.../29-phase-3-documentation-hardening-release/execute.md:22`: "Removed `PATH` and `HOME` from the default smoke environment") was implemented in commit `f8446a6`, which changed both defaults to `TMPDIR, LANG, LC_ALL` **and** updated `docs/configuration.md:230` ("`PATH` and `HOME` are not forwarded by default"). Commit `5e7d309` (Sprint 36 QA foundation) silently reverted the code — `config.go:189` and `internal/sprint/smoke_types.go:55` now ship `["PATH","HOME","TMPDIR","LANG","LC_ALL"]` — leaving the documentation claim in place.

**Demonstrated.** Fresh `init-workspace` scaffold + `config show` prints `smoke.environment: PATH, HOME, TMPDIR, LANG, LC_ALL`; `smokeEnvironment` (`internal/sprint/smoke_protocol.go:623-642`) forwards all five into smoke subprocesses.

**Bad outcome.** Operators reason from the documented allowlist ("only named environment variables… PATH and HOME are not forwarded") and under-count what smoke harnesses receive — `HOME` especially exposes credential-bearing paths (`.netrc`, `.git-credentials`) to whatever the harness executes. This is a regression of a recorded hardening decision, i.e., HISTORY shows intent, REALITY contradicts CURRENT-CONTRACT docs.

**Fix/verification.** Re-apply the sprint-29 default or update the doc; add a test asserting `DefaultSmokeSettings().Environment` matches the documented set (none exists today).

### F3 — `config show --json` leaks secrets via `extra_args` (never redacted) and `_KEY=`-shaped env entries (marker miss) (Medium, High confidence)

**Demonstrated** with one workspace file:

```yaml
agentwrap:
  extra_args: ["--header", "Authorization: Bearer abc123def"]
  env:        ["AUTH=Bearer abc123def"]
```

`config show --json` output: `"extra_args": ["--header", "Authorization: Bearer abc123def"]` verbatim, while the identical secret in `env` becomes `"[REDACTED]"`. Separately, `env: ["MISTRAL_KEY=4f8a2b7c9d"]` passes through unredacted because `Sensitive()` (`redaction.go:28-41`) matches `-key` / ` key=` / `api_key` but not `_key=`, and the value contains no dictionary marker.

**Evidence path.** `redactAgentwrap` (`redaction.go:72-78`) covers only `Executable` + `Env`; `ExtraArgs` is emitted raw in JSON (`config_commands.go:43` → `Redacted.Agentwrap`). Text mode omits both lists, so exposure is via the stable JSON envelope (terminals, CI logs).

**Contract.** CFG-SECRET-001 is Blocker-severity ("redact secrets in errors, diagnostics"; "public config export is allowlisted"), and sprint 29's plan (`plan.md:32`) explicitly intended to "redact sensitive configuration/**extra-argument** values in effective diagnostics" — never implemented. Docs' "don't put tokens in ultraplan.yml" guidance mitigates but does not close it: `env`/`extra_args` exist precisely to inject values into the runtime command line/environment. Regression test: `config show --json` must not contain a planted token placed in either list.

### F4 — `agentwrap.sandbox` / `agentwrap.permission_mode` accept arbitrary values and are enforcement-inert downstream (Medium-low, High confidence)

**Demonstrated.** `sandbox: totally_bogus`, `permission_mode: danger_allow_all` (and env vars `ULTRAPLAN_AGENTWRAP_SANDBOX=disabled`, `ULTRAPLAN_AGENTWRAP_PERMISSION_MODE=none`) load with exit 0 and render in `config show`. `Validate()` (`config.go:425-538`) enum-checks `permission_default` and `unsupported_behavior` but has no checks for these two fields, despite the same admission channel carrying them.

**Downstream trace.** `RequestFromConfig` (`runtime.go:553-554`) → `toAgentwrapRequest` (`runtime.go:609-610`) → agentwrap, where `SandboxMode` is documented as "an open placeholder for future sandbox requirements" (agentwrap `runtime.go:67-68`; used only in health-config metadata) and `Permissions` is recorded solely as `PermissionMetadata.Mode` (`opencode/permissions.go:42`) — actual enforcement derives exclusively from the `permission_default` policy expansion into OpenCode native tool permissions. Even product code's own `req.Sandbox = "read_only"` markers (`sprint/qa_evaluator.go:43`, `review.go:903`) enforce nothing.

**Bad outcome.** False assurance: an operator who sets `permission_mode: restricted` or any sandbox value gets zero validation feedback and zero behavioral effect, while docs (`docs/configuration.md:216`) say these "map to agentwrap sandbox and permission policy fields." Violates CFG-TYPE-001 (validate enums; reject ambiguous). Fix: validate both fields against their real vocabulary (or remove/mark them inert) with a bounds-table test.

### F5 — `version` metadata can never be real: shipped release binaries print `0.0.0-local/local/local` (Low-medium, High confidence)

`cmd/ultraplan/main.go` hardwires `Version: app.DefaultVersion()`; there are no package-level vars to target with `-ldflags -X`, and neither `scripts/install-ultraplan.sh` nor the release checklist build lines (`docs/release-checklist.md:53-56`) pass any. Verified by executing the committed release artifact: `dist/ultraplan-linux-amd64 version` → `Version: 0.0.0-local / Commit: local`. This makes release-checklist item "`ultraplan version` reports intended build metadata" (`release-checklist.md:151`) unsatisfiable by any build, degrading incident triage ("which binary am I running?"). Regression check: build with the checklist commands and assert commit != `local`.

### F6 — `health`'s `filesystem.read` check is hardcoded `ok` and can never fail (Low, High confidence)

`health_commands.go:65` appends `{ID: "filesystem.read", Status: "ok", Message: workspace.MarkerFile}` unconditionally after discovery (which only `os.Stat`s the marker — `discovery.go:64-67`, no readability probe). Demonstrated with `chmod 000 ultraplan.yml`:

```
config.validation: fail - config.load: read workspace config: open ...: permission denied
filesystem.read: ok - ultraplan.yml
```

A permanently green check in both text and stable JSON is a misleading diagnostic signal exactly when filesystem diagnostics matter. Fix: derive status from an actual read (or drop the check).

## Defended non-issues

- **Exit-code precedence** config(3) > runtime(6) > structure(5) in `runHealth` — matches documented/tested behavior; verified experimentally (exit 3 with broken config + structure, exit 5 structure-only).
- **`validation.Issues[0]` indexing** — cannot panic: `Valid == false` ⇔ `len(Issues) > 0` (`workspace/validation.go:28`).
- **Health requiring OpenCode** — supersedes sprint-02's "no OpenCode dependency" criterion; sprint 09 explicitly extends `health` with runtime checks.
- **`bufio.Scanner` 64 KiB line cap** on huge YAML lines — fails closed with a read error.
- **`CLIOverrides.JSON` unread by `Load`** — dead field; rendering happens in the command layer, no behavioral gap.
- **`execution.default_parallel` has no upper bound** unlike other numerics — docs promise only positivity rejection; consumers use it as concurrency hints; not a contract violation (worth capping someday, not a defect today).
- **User `OPENCODE_CONFIG_CONTENT` vs permission policy collision** in agentwrap `mergeEnv` — policy-derived per-tool values override user entries; no privilege-widening injection found at this seam.
- **Marker-redaction false positives** (model IDs containing "token" → `[REDACTED]`) — cosmetic annoyance, opposite direction of risk.

The decisive theme across F1–F4: the admission layer projects strictness (validated enums for some fields, fail-closed errors for some malformations) while silently relaxing exactly the controls an operator is most likely to tighten — env-forwarding allowlists, required health gates, secret redaction coverage, and sandbox posture.