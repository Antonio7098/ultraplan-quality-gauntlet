Context pack read; commits verified at frozen state. Now inspecting primary sources and tests directly.Primary sources read. Now the supporting helpers and callers that guard these entrypoints.A significant lead on `DirectRunner` env handling — verifying semantics and checking the rest of the wiring before concluding.Now proving the env-inheritance lead with a reproduction outside the target repo (copying the runner sources into a scratch module).**Defect #1 proven by execution**: `DirectRunner` leaks the full parent environment into children whenever `Request.Env` is empty/nil — violating the explicit-env-only contract. Both the QA path (`make([]string, 0, …)` with empty catalog env) and smoke path (nil return from `smokeEnvironment`) are affected.

Continuing with the remaining verification targets.Tests confirm the gap: the env tests only assert *presence* of allowed vars, never absence of parent-only vars. Final due-diligence sweep on remaining candidates.Review complete. All evidence gathered directly from source, tests, and an executed reproduction; baseline suites are green at the frozen commit.

# Findings — `process-execution`

## F1. HIGH / HIGH — `DirectRunner` leaks the entire parent environment into children when `Request.Env` is empty or nil

**Claim.** internal/platform/process/process.go:86 — `cmd.Env = append([]string(nil), req.Env...)` produces a **nil** slice whenever `len(req.Env) == 0`. Per `os/exec` semantics, `Cmd.Env == nil` means *inherit the parent's full environment*. The explicit-env-only invariant holds only when the caller passes ≥1 variable; it inverts exactly when the caller asks for the strictest possible env (empty).

**Observable bad outcome.**
- **QA approved checks — deterministic, every execution.** qa_prompt.go:266-271 builds `env := make([]string, 0, len(descriptor.Environment))`; catalog descriptors carry no `Environment`, so `env` is always empty-non-nil, then converted to nil at process.go:86. Every `gofmt -d` child receives the complete parent environment. The project's own deployment guidance (docs/configuration.md, Redaction section) says provider tokens live in the *runtime environment*, so the gofmt child — and any future approved-check executable — is handed exactly the secrets the "child env omits PATH" contract says it must not see.
- **Smoke harness — allowlist bypass.** `smokeEnvironment` (smoke_protocol.go:623-642) declares `var env []string` and appends nothing when every allowlisted name resolves empty/unset — precisely the configuration an operator sets following docs' "add a name only when the harness genuinely needs it." Result: the external harness inherits every parent secret, defeating the allowlist that exists to prevent that.

**Trigger.** Any `Run` call with `Env` nil or length 0. QA path triggers unconditionally; smoke path triggers whenever the resolved allowlist yields zero non-empty values.

**Evidence / reproduction.** Copied `process.go` + `process_unix.go` byte-identical into `/tmp/opencode/envrepro` and ran two tests asserting a parent sentinel (`ULTRAPLAN_SECRET_CANARY=leaked`) is absent from child stdout with `Env: []string{}` and with `Env: nil`. Both **FAIL**: child printed `"leaked-UNSET"`.

**Existing controls / counter-evidence searched.** No caller guards emptiness (grep: process.go:86 is the sole `Env` assignment in the package). Tests cannot catch it: `TestDirectRunnerExactEnvironmentCwdAndCapture` (process_test.go:15) asserts only *presence* of an explicitly passed var and shell-set `PWD`; `TestDefaultSmokeEnvironmentPreservesInterpreterPath` (smoke_test.go:108) tests `smokeEnvironment` in isolation, never the spawn. Nothing disproves the claim.

**Fix + regression.** Preserve emptiness: `env := make([]string, len(req.Env)); copy(env, req.Env); cmd.Env = env` (a zero-length `make` result is non-nil → empty child env). Regression tests: the two repro tests above, plus an end-to-end `RunApprovedQACheck` assertion that a parent sentinel does not reach the child.

---

## F2. LOW-MEDIUM / HIGH — Shipped default forwards `PATH`/`HOME` to harness children, contradicting the written contract

CURRENT-CONTRACT violation. docs/configuration.md:230 states: *"The built-in platform set is limited to TMPDIR, LANG, and LC_ALL; PATH and HOME are not forwarded by default."* Reality: config.go:189 and smoke_types.go:55 both default to `[PATH HOME TMPDIR LANG LC_ALL]`, and `TestDefaultSmokeEnvironmentPreservesInterpreterPath` deliberately pins forwarding of PATH and HOME. A harness author or operator relying on the documented guarantee gets `HOME` (→ `.netrc`, `.gitcookies`, cloud-CLI configs) and `PATH` injected into external harness processes. Also widens F1's smoke-side blast radius relative to what docs promise. Fix: pick one truth — align defaults to the doc set, or rewrite the doc sentence — and update the pinning test to match the decision.

## F3. LOW / HIGH — Dispatcher goroutine leak when spawn fails while `Progress` is set

process.go:97 creates the dispatcher (starting its sink goroutine) before `cmd.Start()`; the Start-error return at process.go:101-103 never calls `dispatch.close()`. With `Progress != nil` — which the smoke run invocation always sets (smoke.go:150) — each failed spawn (executable vanished between prepare and spawn, ENOEXEC, etc.) strands one goroutine blocked forever on `range d.ch` plus its buffered channel. In serve/operation-runner mode with repeated retries this accumulates. (No fd leak: Go ≥1.20 closes parent pipes on Start failure.) Fix: `defer`-style close on the early-return paths, or create the dispatcher after a successful Start.

## F4. LOW / MEDIUM-HIGH — Unbounded resource consumption when hashing external evidence

validateSmokeRun hashes every declared evidence file via `hashFile` (smoke.go:363, 707-713) with **no size bound and no non-regular-file guard**, while this codebase's own identity hashing bounds reads at 64 MiB/file (verify.go:433-435). A multi-GiB sparse file inside the runs/issues root → `os.ReadFile` loads it fully → OOM of the ultraplan process mid-validation; a FIFO inside a declared root → `os.ReadFile` blocks forever while the sprint mutation lease is held. Framing honestly: the review-gated harness is semi-trusted and could squander resources directly, so this is robustness rather than privilege escalation — but it converts harness bugs/misbehavior into parent-process failure during a phase that should fail closed gracefully. Fix: stat-and-bound before read (mirror verify.go's 64 MiB rule) and reject non-regular files.

## F5. LOW / HIGH — Web/durable-operation door drops smoke-timeout parse errors and the 24 h cap

The CLI enforces `--timeout` positivity and ≤24 h with a hard error (sprint_commands.go, `parseSprintSmokeArgs`). The operation-runner door does neither: operation_runner.go:82-84 executes `timeout, _ = time.ParseDuration(req.Timeout)` — a malformed value silently becomes 0 and falls through `smokeTimeout` precedence (request > setting > manifest ≤24h > default, smoke_protocol.go:644-657), so the operator's intended cap is silently discarded; a valid-but-huge value (`"999999h"`) is accepted uncapped because the `request` branch of `smokeTimeout` applies no ceiling. Either way the mutation lease is held for far longer than the user believed. Fix: propagate parse errors and apply the same 24 h ceiling as the CLI.

---

## Defended / non-issues (checked, not reporting as defects)

- **Post-SIGKILL indefinite wait (D-state child)** — unix `stopAndWait` blocks on `<-waited` after group-KILL (process_unix.go:31-34); a child stuck in uninterruptible I/O delays return despite elapsed timeout. Inherent to any Wait-after-KILL ladder; the alternative (abandoning Wait) loses exit status and zombie reaping. Accepted tradeoff, correctly surfaced via `CleanupAttempted`.
- **Dead intersection logic in `smokeEnvironment`** — manifest-declared names can never add anything (they're appended only if already in `settings.Environment`, which seeds the slice). Fails closed; matches the seam contract "admitted only if already allowlisted." Redundant, not harmful.
- **`Getpgid`-failure cleanup-incomplete path** — near-unreachable on unix (a zombie retains its pgid until reaped), and its failure direction (`CleanupComplete=false`) is fail-closed.
- **QA approved-check authorization chain** — investigator-supplied refs must match map-owned `ID`+SHA-256 canonical-JSON fingerprint (qa_prompt.go:244-258), descriptor is revalidated under frozen budgets, drift detection wraps execution, raw streams are discarded post-digest. Deterministic encoder (struct-order JSON, HTML escaping off) keeps fingerprints stable. No injection path from LLM output into argv/env/executable.
- **Arg policy ordering** — metachar rejection precedes the flag skip (qa_prompt.go:104-113), so `-d$(...)`-style arguments are rejected despite starting with `-`; write-mode denylist covers gofmt's only mutating flags.
- **Protocol hygiene** — single-JSON-object decoding rejects trailing values (smoke.go:625-637); discovery/run stdout truncation fails closed; executed-test-set equality, count arithmetic, evidence containment/hash/run-ID addressing, and unexplained-exit rejection all hold as coded.
- **Non-unix lane** (`process_other.go`) — `(nil, false)` cleanup-incomplete return is unpinned by tests, but it maps to the fail-closed `cleanup` error category; platform targets linux/darwin.

F1 is the load-bearing result: it invalidates the surface's two headline guarantees (explicit-env-only, allowlist-bounded child capability) and is proven by execution, not inference.