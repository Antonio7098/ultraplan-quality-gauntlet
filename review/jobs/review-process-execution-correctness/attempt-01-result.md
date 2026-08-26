Context pack read. Now let me inspect the actual source, starting with the primary files.Initial notes taken on the process package. Now the sprint-side files.Both key files read. I've spotted several candidates already (notably around env handling). Now let me read the invocation sites, validators, and tests.Tests confirmed to only pin non-empty env. Let me verify the empty-env mechanics with a quick experiment and read the QA resolution path plus helpers.Empty-env inheritance confirmed empirically (73 env lines inherited vs 3 with a truly empty env). Now the QA descriptor resolution:Descriptor confirmed to come from the freshly-built catalog (qa.go:826-839), so the tautological cwd validation is latent-only. Now the shared helpers:All smoke tests use fake runners — no end-to-end coverage of DirectRunner env semantics. Now the config/wiring layer and QA types:There's a `pprocess.SortedEnvironment` used elsewhere — let me check that helper and find the config file:A whole `isolation.go` exists in the process package that the context pack didn't cover. Let me inspect it plus the smoke config parsing:Docs divergence confirmed. Now baseline: build the target and run the process package tests, then write adversarial repros against the real `DirectRunner`.Build and baseline tests pass. To avoid touching the target repo, I'll copy the process package into /tmp/opencode and run adversarial repros against it:Repro 1 (env inheritance) and repro 3 (goroutine leak) confirmed. Repro 2 used a flawed fixture — I redirected the descendant's stdout myself. Retrying with an inheriting descendant:It returned promptly — Go's `poll.FD` unblocks readers on close, killing the hang theory. But that mechanism implies a different defect: `cmd.Wait()` closes the pipe at reap, so undrained tail data is **silently lost** without the truncation flag. Testing that:No tail loss — contradicts my model of `cmd.Wait()`. Let me check the actual Go stdlib behavior in this toolchain:The `yes|head` fixture self-synchronizes (writer blocks until drained), masking the race. Need a writer that exits with data still buffered — small overflow over 64 KiB with a slow consumer:Still no loss — my model of close-vs-read semantics must be wrong somewhere. I have the package source locally; instrumenting to see what actually happens:The `Progress` sink is decoupled via the dispatcher channel — `copyStream` itself never blocks, so it outraces `Wait()`'s close in every attempt. Let me verify the loss mode deterministically by simulating a descheduled drain (what happens under CPU starvation):Self-blocked again — the child couldn't exit with a full pipe. Fixing: keep payload under the 64 KiB buffer so the child exits unaided:**Tail-loss confirmed deterministically**: 4096 of 60000 bytes captured, rest silently lost with `file already closed`, no truncation flag, nil error. Now closing out the request-timeout question — checking whether web operation requests actually accept a Timeout field for smoke:The uncapped request-timeout on the durable path is confirmed. One last wiring check (`reasoning_effort` = Variant looks suspicious), then sprint package baseline tests:Evidence complete. Sprint baseline tests pass; all candidates adversarially checked. Final report:

---

# Findings: `process-execution` — Subprocess control and spawn policies

Target: ultraplan-go @ `5d54525b`. All findings verified against source; repros executed against a byte-identical copy of the process package in `/tmp/opencode/processrepro` (target repo untouched).

---

## F1 — `DirectRunner` cannot spawn an empty child environment; nil/empty `Env` silently inherits the orchestrator's full environment

**Severity: HIGH · Confidence: HIGH (reproduced)**

**Claim.** `cmd.Env = append([]string(nil), req.Env...)` (process.go:86) converts an *empty* env slice into `nil`, and `os/exec` treats nil `Env` as "inherit parent". The API therefore has **no way to express "no environment"** — the harder a caller tries to lock down the child env, the more leaks.

**Observable bad outcome.**
- *QA path (unconditional):* `RunApprovedQACheck` builds `env := make([]string, 0, len(descriptor.Environment))` (qa_prompt.go:266) from a catalog whose `Environment` is always empty by construction (qa_prompt.go:75). Every `gofmt` check therefore runs with the **entire UltraPlan process environment**, including model-provider credentials held for agent runs — directly contradicting the coded invariant "child env omits everything else (including PATH)" and the `qa-approved-check-policy` seam contract.
- *Smoke path (conditional but inverted):* `smokeEnvironment` returns `var env []string` → nil whenever the configured-allowlist ∩ manifest-names intersection computes empty or all values are unset (smoke_protocol.go:635-641). An operator who hardens `smoke.environment` to e.g. `[TMPDIR]` on a headless unit where TMPDIR is unset gets **full credential-bearing inheritance instead of minimal forwarding** — the control knob fails toward maximum exposure. The external harness can read it all via `/proc/self/environ`.
- Same defect reaches the isolation workspace lane (`SortedEnvironment` returns empty non-nil slice; isolation.go:422-437 → qa_investigation.go:85).

**Trigger/preconditions.** QA: none (always). Smoke: empty intersection or all allowlisted values unset in parent.

**Evidence.** process.go:84-87; qa_prompt.go:75, 266-272; smoke_protocol.go:623-642. Repro against the real `DirectRunner`: `Env: []string{}` with canary `REPRO_CANARY_SECRET=leaked` → child printed the full parent environment including the canary (73 entries).

**Counter-evidence searched.** `TestDirectRunnerExactEnvironmentCwdAndCapture` pins only a *non-empty* env; all sprint-level tests use fake runners, so nothing refutes or catches this. `TestDefaultSmokeEnvironmentPreservesInterpreterPath` uses a fake getenv with PATH/HOME set, never hitting the nil branch. No caller relies on inheritance by intent; every contract states the opposite.

**Fix/regression test.** In `Run`: `if req.Env == nil { req.Env = []string{} }` (before copying) so nil and empty both mean explicit-empty. Regression: `Run(/usr/bin/env, Env: []string{})` asserts empty child env output; currently fails.

---

## F2 — Silent stdout/stderr tail loss: `cmd.Wait()` closes the pipe at reap, racing the drain goroutines; lost bytes produce no truncation flag

**Severity: MEDIUM · Confidence: HIGH on mechanism (deterministic repro), moderate on production frequency**

**Claim.** Drains run concurrently with `cmd.Wait()` (process.go:106-109). With `StdoutPipe()`, stdlib closes the parent read ends immediately after reaping (go1.26 `exec.go:954`; documented at exec.go:1102-1104: *"It is thus incorrect to call Wait before all reads from the pipe have completed"*). Any bytes still in the kernel buffer when close wins the race are discarded; subsequent reads return `file already closed`, which `copyStream` swallows (process.go:193-195). Result: short capture, `StdoutTruncated=false`, `err=nil`.

**Observable bad outcome.**
- Smoke: a correct harness whose response JSON suffers mid/tail loss yields misleading `smoke_discovery_malformed` / `smoke_run_malformed` failures (smoke.go:105, 158-163) — flaky, wrongly blames the harness, and the fail-closed truncation gate at smoke.go:101/:154 never fires because the flag isn't set.
- QA: `StdoutDigest`/`OutputBytes` recorded over silently incomplete `gofmt` output (qa_prompt.go:277) — the persisted evidence digest no longer corresponds to real command output, and the `OutputBytes > 2×limit` budget check can be bypassed by loss.

**Trigger/preconditions.** The drain goroutine must be descheduled between reads while the child exits — GC stop-the-world, CPU contention (budgets permit up to 8 concurrent QA investigators plus smoke runs on one host). Loss magnitude ≤ 64 KiB per stream (pipe buffer).

**Evidence.** Deterministic repro replicating the exact structure with the drain parked after chunk 1: child wrote 60000 bytes, exited; `cmd.Wait` closed the fd; final read returned `read |0: file already closed`, n=0 → **captured 4096 of 60000 bytes, err=nil, truncated=false**. Without artificial parking, the tight drain loop wins every observed race (10+ attempts, incl. 1 MiB payloads) — hence "narrow window, real under load".

**Counter-evidence searched.** `limitedCapture.Write` always accepts (no backpressure) — rules out writer-side stalls as the cause; the dispatcher decouples `Progress` slowness from drains (so `TestDirectRunnerSlowProgressDoesNotBlockDrain` cannot catch this); no buffering/retry of read errors exists.

**Fix/regression test.** Assign `cmd.Stdout = outCapture; cmd.Stderr = errCapture` (exec-owned copying; `Wait` then awaits its own goroutines) or add `WaitDelay`. Regression: package-internal test injecting a stall between drain reads (the repro above), asserting full capture.

---

## F3 — Dispatcher sink goroutine leaks on every failed `cmd.Start`

**Severity: LOW · Confidence: HIGH (reproduced)**

`newDispatcher` spawns the sink goroutine (process.go:97, 211-216) *before* `cmd.Start()`; all early-return paths (pipe creation :89-94, Start failure :101-103) skip `dispatch.close()`, leaking one goroutine + buffered channel per failed spawn. Measured: 50 failing `Run`s → goroutines 2→52. Realistic accumulation: repeated QA checks when `gofmt` is absent (`LookPath` fails at Start; each attempt ends in `QAErrorRuntimeUnavailable`, retried per shard/attempt). Fix: `defer dispatch.close()`-style cleanup on the pre-spawn error paths. Regression: goroutine-count assertion around failing Starts (repro 3).

---

## F4 — Never-spawned process classified as "cleanup incomplete" with wrong remediation guidance

**Severity: LOW-MEDIUM · Confidence: HIGH**

Pre-spawn failures return the zero `Result` (process.go:62-103) — `CleanupComplete=false`, `CleanupAttempted=false`. `classifyProcessSmokeError` checks `!result.CleanupComplete` third (smoke.go:609-610) and emits category `"cleanup"` with guidance *"Terminate owned descendants before retrying"* — for a process that **never existed** (e.g., harness binary deleted during the authoring window, `EACCES`, fork exhaustion, or `Timeout<=0` from a mangled settings parse). Users are sent hunting phantom process groups while the wrapped cause is buried. Attempt status stays `AttemptFailed`, so flow-state is not corrupted — this is diagnostic-truth damage, not verdict corruption. Fix: distinguish `!CleanupAttempted` → category `process` (or a dedicated `spawn` category). Regression: unit asserting `classifyProcessSmokeError(Result{}, err)` ≠ cleanup category.

---

## F5 — `Run` can still block indefinitely past its own deadline: unbounded final `<-waited` after group SIGKILL

**Severity: LOW-MEDIUM · Confidence: MEDIUM (mechanism certain; trigger environmental)**

`stopAndWait`'s timeout branch sends group SIGKILL then blocks unconditionally on `<-waited>` (process_unix.go:31-33); the TERM-phase grace does not bound the KILL phase. A leader stuck in uninterruptible sleep (hung FUSE/NFS I/O — plausible for browser-driving smoke harnesses) ignores SIGKILL, so `Run` never returns despite `Timeout`, cancellation already having been consumed by the select, and `CleanupComplete=true` asserted only after an unbounded wait. Consequence: smoke mutation lease pinned forever; flow-state stuck `running`. Related liveness gap: `drains.Wait()` (process.go:124) is likewise unbounded and ctx-blind once the select exits. Fix: second grace-bounded select after SIGKILL; on expiry report `(nil, false)` with an orphan-reaper goroutine. Verification: assert `Run` returns within `Timeout + 2×grace + slack` for a stopped/descheduled leader (D-state itself is not portably simulable; document the residual).

---

## F6 — Request-sourced run timeout is uncapped on the durable-operation door

**Severity: LOW · Confidence: HIGH**

Every other timeout source is bounded: config ≤ 24 h (config.go:453-468), manifest ≤ 24 h (smoke_protocol.go:222), CLI ≤ 24 h (sprint_commands.go:928-931). But `smokeTimeout` returns `req.Timeout` verbatim (smoke_protocol.go:644-647), and the web/TUI durable path neither caps nor validates: `normalizeOperationRequest` trims only (operations.go:566), and `operation_runner.go:81-84` does `timeout, _ = time.ParseDuration(req.Timeout)` — parse errors silently become 0 (precedence fallback), while `"8760h"` parses and pins the sprint mutation lease for a year with a hung harness. A submitted smoke-start operation therefore becomes an unbounded lease/resource occupation unavailable through the CLI. Fix: cap request timeouts to 24 h inside `smokeTimeout` (surface-owned, fixes all doors at once) and log/ignore parse failures explicitly.

---

## F7 — Documentation/default contract conflict: PATH/HOME forwarding

**Severity: LOW · Confidence: HIGH (factual divergence)**

docs/configuration.md:230: *"The built-in platform set is limited to TMPDIR, LANG, and LC_ALL; PATH and HOME are not forwarded by default."* Reality: default `smoke.environment` includes PATH and HOME (config.go:189), `DefaultSmokeSettings` includes both, and `TestDefaultSmokeEnvironmentPreservesInterpreterPath` pins forwarding (smoke_test.go:108-128). Either the doc or the default is wrong; operators hardening per the doc get the opposite of the documented behavior — compounded materially by F1, where their next defensive step (emptying/trimming the allowlist) produces total inheritance. Reconcile doc, defaults, and seam contract.

---

## Defended non-issues (investigated, not reported as defects)

- **Tautological cwd validation** — `validateQACheckDescriptor(descriptor.WorkingDirectory, …)` (qa_prompt.go:259) passes the descriptor's own cwd as "target", so the cwd==target rule can't fail there. Latent only: the executed descriptor always comes from the freshly built catalog against the real target (qa.go:826-839), the investigator contributes solely ID+fingerprint (qa_prompt.go:244), and the fingerprint canonically binds `WorkingDirectory` (qa_map.go:380-387). Defense-in-depth nit, no reachable failure.
- **Drift fence ignores gitignored files** (`ls-files -co --exclude-standard`, verify.go:370) — mutations to ignored paths are invisible to `targetIdentity`. Today's single catalog check (`gofmt -d`) cannot write, so no concrete bypass; flagged for the future-checks case only.
- **Getpgid-failure path in `stopAndWait`** — near-unreachable (a reaped-but-unwaited zombie still has a pgid); fail-closed direction regardless.
- **Duplicate suite IDs in `mapping.Suites`** pass discovery validation and later guarantee an expected-vs-executed mismatch — fail-closed with confusing diagnostics only; requires a malformed external discovery document.
- **Truncation-checked before timeout classification** (smoke.go:154 precedes :165) — a timed-out run with oversized output reports the truncation error; both facts are true, only the category/guidance is suboptimal.
- **`reasoning_effort` = `Variant`** metadata duplication (qa_prompt.go:171-172, 232-233) — operative variant routing consumes `Metadata["variant"]` (opencode.go:198-203); belongs to the runtime surface, no concrete failure established here.
- **Windows lane `(nil, false)` cleanup path** — untested but fail-closed; unix is the primary deployment and tests intentionally skip windows.