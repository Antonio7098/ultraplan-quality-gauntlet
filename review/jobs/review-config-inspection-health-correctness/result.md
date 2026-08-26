Context pack read. Now examining the runtime stack that consumes this config (sandbox/permission mapping), plus version and workspace discovery.Now checking the authoritative contracts in the planning workspace.Contracts confirmed. Now verifying candidate defects experimentally — building the CLI and testing the redaction/parser paths.Both redaction gaps confirmed live. Now testing the YAML parser edge cases.Now an end-to-end proof of the `required_health: []` inversion using a stub `opencode` binary.The inversion is empirically proven. Running the surface's test suites to baseline, and checking recent history for context.Final consolidated reproduction covering every redaction-bypass instance in one workspace.# Review report — `config-inspection-health` (correctness lens)

Target: `ultraplan-go @ 50d6f0d`. All findings reproduced against the built binary or verified at source level. Existing tests (`go test ./internal/platform/config/... ./internal/app/ -run 'Config|Health|Version'`) pass and would not catch any finding below.

---

## F1 — Redaction bypass: secrets in `agentwrap.extra_args`, four planning variants, and `qa.variant` print verbatim through `config show`

**Severity:** Medium **Confidence:** High

**Claim.** `Redact()` routes only some free-form user-set strings through `RedactValue`. `agentwrap.extra_args` is never inspected (`redaction.go:72-78` handles only `Executable` + `Env`); `planning.requirements_variant`, `sprint_index_variant`, `technical_handbook_variant`, `area_reasoning_variant` are skipped while their sibling variants are wrapped (`redaction.go:54-70`); `qa.variant` is skipped while `qa.model` is wrapped (`redaction.go:22-25`). Any value matching `Sensitive()`'s own markers (`--key`, `-key`, `sk-`, …) in those fields is emitted as plaintext.

**Observable bad outcome (reproduced):** workspace with
`extra_args: ["--api-key=sk-extraargs-secret-7"]`, `requirements_variant: sk-variant-secret-1`, … `qa.variant: sk-qa-variant-secret-6`:

```
code_context_variant: [REDACTED]              ← routed through RedactValue
requirements_variant: sk-variant-secret-1     ← leaked (text AND --json)
extra_args: ['--api-key=sk-extraargs-secret-7'] ← leaked (--json)
qa.variant: sk-qa-variant-secret-6            ← leaked
```
The same secret pasted into `agentwrap.env` or `models.default` *is* redacted — the inconsistency is the proof of intent. `config show --json` is a declared stable surface (`docs/cli-reference.md:478`) and "Sensitive values are redacted" is its documented contract (`cli-reference.md:93`, TRD §6.3, sprint-02 AC-04). The `--key` marker in `Sensitive()` exists precisely to catch arg-style secrets, yet args never reach it; a regression test already asserts `--api-key=` redaction for lock commands (`config_test.go:97`), showing this class of value is meant to be caught everywhere.

**Trigger/preconditions:** user places a credential-shaped string in one of the uncovered fields (misuse, but exactly the misuse redaction exists to absorb), then runs `config show` / pastes output into an issue or support bundle.

**Path:** `ultraplan.yml` → `Load/setField` (admitted unvalidated) → `Redact` → stdout.

**Fix/verification:** wrap all five field families in `RedactValue`; extend `TestRedactSensitiveValues` with an `extra_args` entry and every variant asserting `[REDACTED]`.

---

## F2 — Emptying `agentwrap.required_health` makes `health` stricter (runs and enforces all 8 checks) while runs skip preflight entirely

**Severity:** Low-Medium **Confidence:** High (empirically reproduced)

**Claim.** `Validate` accepts an emptied list (`config.go:517-521` loop no-ops); `RequestFromConfig` copies it verbatim (`runtime.go:551`). The nil/empty list then collides with two agentwrap defaults: `CheckHealth` substitutes the full 8-check default set when `Checks` is empty, and `RequiredHealthFailure` treats a non-explicit required set as "everything that ran must be ready/skipped" (agentwrap `health.go:127-131`, `opencode/health.go:63-70`). Meanwhile run-time `requiredPreflight` returns nil on empty (`opencode/runtime.go:98-100`). Same input, opposite semantics, both diverging from "no required checks".

**Observable bad outcome (reproduced with stub `opencode`):** identical workspace, identical runtime:
- default config (3 checks) → `health` exits 0;
- after appending bare `required_health:` (empty) → health additionally probes `provider`/`authentication`/`model`/`config`/`runtime_paths`, fails (`runtime.provider: fail … exit 6`) because default `models.primary` is `provider/model`. Removing requirements caused failure. On real opencode builds lacking `debug paths`/`debug config`, even provider-less workspaces fail.

**Counter-evidence checked:** no admission guard rejects the empty list; `mapHealthIDs(nil)` yields an indistinguishable empty slice (`agentwrap.go:10`), so the "explicitly none vs defaulted" information is destroyed before agentwrap re-interprets it.

**Fix/verification:** treat explicitly-empty `required_health` as "run nothing, require nothing" (or reject it at admission with guidance). Regression: load a workspace with emptied list and assert the produced `HealthRequest.Checks` stays empty end-to-end through `Adapter.Health` with a fake health impl.

---

## F3 — YAML parser silently drops stray `- ` lines instead of failing closed

**Severity:** Low **Confidence:** High (reproduced)

`loadFile` dispatches `- item` lines through a switch on the active list field with **no default case** (`config.go:225-244`); when no list header is active the line vanishes. Reproduced: under `agentwrap:` with only `executable:` set, the line `- --this-line-is-misplaced` is discarded — `config show` exits 0, `extra_args` unchanged, source claims `default`. Elsewhere the parser is strict (unknown fields and non-list garbage error out, `config.go:247-248,420`), so a structural typo that happens to start with `- ` gets a free pass. Bad outcome: configured runtime arguments silently absent from effective config while the command reports success. Fix: return `"unsupported line"` for unmatched list items; regression test with ws-style YAML expecting `Load` error.

## F4 — Scalar value ending in `:` is parsed as a section header; assignment silently discarded

**Severity:** Low **Confidence:** High (reproduced)

Section detection is purely suffix-based (`config.go:211`), so `git.remote: backup-origin:` becomes `section = "git.remote: backup-origin"` and the key/value disappears. When it is the last content line, nothing downstream errors: reproduced — exit 0, effective `git.remote` stays `origin`, JSON sources say `default`. With `stage_completion: commit-and-push` this publishes to the wrong remote instead of failing. Mid-file occurrences do fail later with a confusing unknown-field error (fail-closed but misleading). Fix: reject section keys containing whitespace/`.` or validate that a section name is a known top-level key; regression: trailing-colon value must either parse as value or fail `Load`.

---

## F5 — 30 working environment override variables are absent from the documented override list

**Severity:** Low **Confidence:** High (verified live)

`docs/configuration.md` ("Supported environment overrides", closed list) omits `ULTRAPLAN_CODE_CONTEXT_MODEL`, `ULTRAPLAN_CODE_CONTEXT_VARIANT`, and all 28 generated `ULTRAPLAN_QA_*` keys (`qa.go:94-105`). Verified live: `ULTRAPLAN_QA_PRIMARY_SHARDS=7 ultraplan config show` reports `qa.primary_shards: 7`. These are untrusted admission channels that reshape QA policy and planning models, invisible to anyone auditing from the docs (the surface's own doctrine lists env vars as a trust boundary). Fix: document them or generate the doc section from `EnvOverrides()`.

## F6 — Version command can never report build metadata; release-checklist gate unsatisfiable

**Severity:** Low **Confidence:** High

`version.go:5-9` declares the values as **consts** and `cmd/ultraplan/main.go:26` hardcodes `app.DefaultVersion()`. Go `-ldflags -X` works only on package-level vars, so there is no mechanism — Makefile, CI, or code — by which a release binary could ever print anything but `Version: 0.0.0-local / Commit: local / BuildDate: local`. `docs/release-checklist.md:151` requires confirming "`ultraplan version` reports intended build metadata," which cannot pass without a code change; triage of user reports from released binaries is misled. Fix: convert to vars plus ldflags injection in the release build; verify via `go build -ldflags "-X ...defaultCommit=abc"`.

## F7 — `config.Redact` mutates its input's `Agentwrap.Env` backing array (latent)

**Severity:** Low (no current victim) **Confidence:** High

`redactAgentwrap` receives a struct copy whose `Env` slice shares storage with the caller's `Effective.Config.Agentwrap.Env`; the in-place writes at `redaction.go:74-76` overwrite the original entries with `[REDACTED]`. Today the sole caller (`config_commands.go:41`) discards `effective` afterwards, so no live misbehavior — but any future ordering that redacts before executing/logging (the exact pattern health uses: redact diagnostics *and* keep using config) silently launches runtimes with literal `[REDACTED]` environment values. An exported "projection" function violating projection semantics is a correctness landmine in this surface. Fix: build a fresh slice (`make`+copy) inside `redactAgentwrap`; regression: assert deep equality of input `Effective` before/after `Redact`.

---

## Defended non-issues (investigated, counter-evidence found)

- **Capability gate skip on `Capabilities` error** (`health.go:74` `err == nil` guard): looks fail-open, but the opencode adapter's `Capabilities` returns a static table and never errors (agentwrap `opencode/options.go:132`), and `Adapter.runtime` is never nil via `NewOpenCode` (`opencode.go:82`). Unreachable defensive branch, not a live gate bypass.
- **Free-form `agentwrap.sandbox` / `permission_mode`:** in this agentwrap version both are opaque metadata/observability strings (`runtime.go:67-68` "open placeholder"; enforcement flows exclusively through `PermissionPolicy`, whose `permission_default`/`permission_unsupported_behavior` inputs *are* enum-validated at `config.go:527-536` and re-validated by `agentwrap.ValidatePermissionPolicy`). A hostile value cannot weaken enforcement today.
- **`filesystem.read: ok` unconditional** (`health_commands.go:65`): discovery has already stat'ed the marker file (`discovery.go:64-66`) and marker == `ultraplan.yml`, so genuine read failures surface later via `config.validation: fail`. Mislabeled check name, not misleading outcome.
- **Missing `ultraplan.yml` hard-failing `Load`:** unreachable post-discovery (marker file *is* the config file).
- **Skipped checks rendered `ok`** (`health.go:113-121`): matches agentwrap semantics where `RequiredHealthFailure` counts skipped required checks as satisfied.
- **Exit-code precedence** config(3) > runtime(6) > structure(5) in `runHealth:100-108`: internally consistent and matches the designed precedence; JSON `status` correctly reflects aggregate failure.
- **QA lower-only bounds**: enforced identically across YAML and env channels (`validateQA`), including zero/negative rejection — tests cover every limit.
- **Precedence order, CLI > env > workspace > defaults**, and source attribution: correct, including failed overrides not claiming `env` provenance (tested).

The stale package comment in `config/doc.go` ("deferred to a later sprint") and text-mode `config show` omitting the `git.*` section (JSON shows it) are noted as cosmetic/completeness observations only.