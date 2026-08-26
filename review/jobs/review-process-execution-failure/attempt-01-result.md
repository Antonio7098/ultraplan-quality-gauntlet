Context pack read. Now inspecting the actual source.Now the sprint-side files.Now the invocation sites in smoke.go and the process tests.Source read. Now verifying key hypotheses: callers/wiring, the drain/wait interplay, and what tests pin down.No `WaitDelay`/`Cancel` on the exec path — and notably the repo's own agent-harness-study documents this exact hazard class ("WaitDelay pipe guard") as required practice elsewhere. Now checking the settings/config paths that feed `Request`, then I'll empirically reproduce the drain race.A `cleanup_uncertain.go` file exists and config parse errors are ignored at sprint_commands.go:899-900. Reading both plus the app-side guards.The context pack missed `internal/platform/process/isolation.go` — a workspace wrapper used by QA evidence runs (`qa_investigation.go:85`). Reading it now, plus config validation of smoke durations.`isolation.go` is a missed surface component (QA evidence commands run through it). Checking remaining package files and config validation of smoke durations.Composition reviewed. Now the decisive step: empirically reproduce the suspected happy-path drain race using the exact `DirectRunner` source (copied unmodified into a scratch module — target repo untouched).Unexpected — passed. Before theorizing, let me instrument to see what's actually happening (longer stalls, hotter loops, logged captures).Full capture despite 100 ms stalls — my hypothesis needs checking against the actual Go runtime implementation of `Cmd.Wait`. Inspecting:`Wait` provably closes the read end before returning, yet nothing is lost — my mental model of post-close pipe reads must be wrong. Micro-experiment:Post-close reads fail and lose buffered data — mechanism confirmed at OS level. Yet DirectRunner captures fully. Something else guarantees ordering; inspecting this toolchain's `StdoutPipe`/`Start` internals more closely:`StdoutPipe` confirmed user-owned. Instrumenting the scratch copy to see read timing vs Wait return:The Progress sink sleeps on the dispatcher goroutine, not the drain loop — so my first repro didn't stall the reader at all. The real race is reader-descheduling vs Wait-close. Trying scheduler-pressure reproduction:Zero losses across 40 adversarial runs — the drain loop consistently wins the wakeup race on this platform. Recalibrating that finding's confidence downward (mechanism real per os/exec docs; field frequency unproven). Moving to remaining candidates: `targetIdentity` bounds, mutation-lease ctx honoring, timeout caps at doors, and `qaPathApproved`.Checking where web doors source `req.Timeout`, the `smokeError` unwrap chain, and counter-evidence in tests:F5 confirmed: no validation/cap in the durable-operation door. Baseline check that the frozen tree builds and its own tests pass, so findings rest on a green base:One more empirical data point — happy path with an outliving grandchild holding the pipe (does Go's close-wake save us, or hang?):All evidence gathered. Target repo untouched (scratch reproduction lived in `/tmp/opencode`). Final report:

---

# Review: `process-execution` — subprocess control & spawn policies

Target: ultraplan-go @ `5d54525`. Baseline green (`go test ./internal/platform/process/...` ok, build ok). Context pack was accurate on primary paths but missed `internal/platform/process/isolation{,_linux,_unix,_other}.go`, which I reviewed as in-package composition (QA evidence commands execute through `workspace.Run` → `DirectRunner`, qa_investigation.go:85).

## F1 · Happy-path stdout/stderr tail-loss race (concurrent `cmd.Wait()` closes pipes mid-drain)

**Claim.** `Run` starts `cmd.Wait()` concurrently with the drain goroutines (process.go:108-109) and takes its result immediately on normal completion (process.go:114). `Cmd.Wait` closes the parent-side pipe read ends *before* returning (verified against this toolchain's `exec.go:954`), and a `Read` issued after that close fails with `file already closed`, discarding whatever remains buffered — up to 64 KiB of protocol payload. This is precisely the pattern os/exec documents as incorrect: "it is incorrect to call Wait before all reads from the pipe have completed."

**Bad outcome.** A silently short capture with `StdoutTruncated == false` (the flag only trips on *over-limit* writes, limitedCapture process.go:161-177). Smoke then misdiagnoses its own data loss as a harness defect: `decodeOneJSON` fails → `smoke_discovery_malformed` / `smoke_run_malformed`, "Fix the harness discovery command" (smoke.go:105-107, 157-164) — blocking verdicts that blame the external party. QA side: digests/durations recorded over partial streams (qa_prompt.go:277).

**Trigger.** Nondeterministic scheduling: loss requires Wait's reap+close to land in the µs-scale window after the child's final write enters the pipe buffer but before the drain loop consumes it. Pipe backpressure keeps writer and reader in lockstep, which shrinks the window — I could **not** trigger it in ~40 adversarial runs (burst outputs, GOMAXPROCS=1 + CPU-spinner starvation, reader stalls up to 100 ms/chunk). The loss primitive itself is proven (standalone experiment: 200 KB buffered, close, subsequent read yields 0 bytes, `file already closed`). So: severity **high** (silent evidence corruption inside a delivery gate), probability **low/unquantified**, confidence **high** that the documented contract is violated, **medium-low** that it manifests in the field.

**Counter-evidence found.** Go's poller wakes blocked readers on close (`ErrClosed`) rather than hanging — so the grandchild-outliving-child variant returns cleanly in ~4 ms (empirically shown); only the tail-race remains. Existing tests never exercise large-output-plus-exit timing.

**Fix / regression.** Reorder to EOF-before-Wait with the existing ladder as watchdog, or switch to internal-copy form plus `cmd.WaitDelay`; either preserves the kill semantics. Regression test needs stall injection at the capture boundary or a large-output soak under load — the current suite structurally cannot catch it.

## F2 · Unix cleanup ladder can block forever after SIGKILL

**Claim.** `stopAndWait` bounds only the SIGTERM phase; after the group SIGKILL it does `<-waited, true` unconditionally (process_unix.go:31-33). If the group leader is in uninterruptible sleep (NFS/FUSE/hung driver, frozen cgroup), SIGKILL stays pending, `cmd.Wait` never returns, and `Run` never returns — despite Timeout and CleanupGrace having expired. Context cancellation cannot help: the select already exited (process.go:119-122). The non-unix variant handles exactly this case by bounding the post-KILL wait and reporting cleanup incomplete (process_other.go:18-23) — unix is strictly less robust than the stub. Same unbounded shape in the Getpgid-failure branch (process_unix.go:20), benign today since it only fires post-reap.

**Bad outcome.** Smoke/QA invocation hangs indefinitely past its deadline while holding the sprint mutation lease; flow-state shows a running attempt until external reconciliation; the CLI/web caller's own context cancel is ignored.

**Trigger/preconditions.** Leader in D state or cgroup-frozen. Low likelihood on typical dev hosts, real on network-FS CI. Severity **medium-high** (liveness/resource ownership), confidence **high** on mechanics, **low-medium** on occurrence.

**Counter-evidence.** None — no watchdog exists downstream; `CleanupGrace` applies only pre-KILL.

**Fix / regression.** Bound the final wait (e.g. grace/2) and return `CleanupComplete=false` like `process_other.go`; verifiable via cgroup freezer in a Linux-gated test.

## F3 · Pre-spawn failures misclassified as "cleanup uncertain"

**Claim.** Early returns leave `Result` zero-valued except `StartedAt` — including `CleanupComplete=false` (process.go:90, 94, 102: pipe-open failure, stderr-pipe failure, Start failure). This contradicts the runner's own invariant handling one line later (`!CleanupAttempted ⇒ CleanupComplete=true`, process.go:135-137, which early returns skip). `classifyProcessSmokeError` then falls into `!result.CleanupComplete` (smoke.go:609-610 via smoke.go:99, 161, 165-167) and emits category `cleanup`: "*cleanup is uncertain — Terminate owned descendants before retrying*" for ENOENT/EACCES/ENOEXEC/EMFILE spawn failures where nothing was ever spawned. Misleading operator guidance in smoke.md diagnostics and flow-state attempts.

**Reachability.** Moderate-low: prepare validates executable/cwd just before, but minutes of agent authoring run between validation and spawn, and fd exhaustion hits the pipe-open calls directly. Logic is certain by inspection; no test pins this branch. Severity **low-medium**, confidence **high**.

**Fix / regression.** Set `CleanupComplete=true` whenever no process started, or gate the cleanup category on `CleanupAttempted`. Unit test: feed a start-error Result through `classifyProcessSmokeError`, expect category `process`.

## F4 · Unbounded memory when hashing externally declared evidence

**Claim.** `validateSmokeRun` hashes every evidence item with `hashFile` → `os.ReadFile` (smoke.go:363, 707-713), loading the entire file into parent memory with **no size ceiling**, for paths chosen entirely by the external run response (location/naming are checked; size never). Every other external-input read in this surface's orbit is bounded: stream captures (4 MiB/1 MiB), timeouts (≤24 h), `targetIdentity` (64 MiB/file, verify.go:433), isolation trees (budgeted). The missing sibling control sits on the trust boundary the pack itself flags ("child stdout/stderr re-enters as verification evidence").

**Bad outcome.** A buggy or hostile harness declares one multi-GiB file inside its runs root → parent allocation spikes → OOM kill of UltraPlan mid-gate while holding the mutation lease.

**Severity/confidence.** Medium-low / high (plain code path; trigger requires misbehaving harness, which is exactly the adversary class every neighboring control assumes).

**Fix / regression.** Stream-hash with `io.LimitReader` plus an explicit per-file ceiling returning a typed `smoke_evidence_*` error; regression: declare an oversized file, expect typed rejection, bounded RSS.

## F5 · Timeout input control inconsistent across doors feeding `smokeTimeout`

**Claim.** Request-sourced timeouts win precedence outright (smoke_protocol.go:645-647). The CLI validates both shape and cap ("--timeout must be positive and no more than 24h", sprint_commands.go:927-930, 1237-1240). The durable-operation door used by web/TUI does neither: `timeout, _ = time.ParseDuration(req.Timeout)` (operation_runner.go:81-84, 102-105) — parse failures silently become 0 (falling back to lower-precedence sources while the operator believes their value applies), and positive values are uncapped, so e.g. `"99999h"` flows straight into `DirectRunner.Timeout`, pinning the mutation lease for an effectively unbounded owned run with flow-state showing `running`.

**Severity/confidence.** Low-medium / high. Pure door asymmetry, visible by inspection.

**Fix.** Route the durable door through the same parse-and-cap helper and surface the error.

## F6 · Documented env-containment promise contradicts pinned default

**Claim.** docs/configuration.md:230 states PATH and HOME are "not forwarded by default"; the shipped default forwards both (`config.go:189`, `DefaultSmokeSettings` smoke_types.go:55), and `TestDefaultSmokeEnvironmentPreservesInterpreterPath` (smoke_test.go:108-128) pins forwarding as intended. One of contract/code/tests is wrong. Security-relevant because operators reason about exposure to external harnesses from the doc. Severity **low**, confidence **high** on divergence, intent unknowable from code.

---

## Defended non-issues (checked, with disproof)

- **Normal-exit hang when a descendant inherits the pipe:** disproven empirically — poller-close wakes drains, `Run` returned in ~4 ms with `exit=0, CleanupComplete=true`.
- **Investigator-forged checks:** impossible without breaking SHA-256 fingerprint ownership; descriptor revalidated under current budgets at execution (qa_prompt.go:244-261); fingerprint covers budget-derived fields too.
- **E2BIG on `gofmt -d` argv:** bounded by `Budgets.ChangedPaths` (default 512, qa_types.go:128).
- **Env leakage to harness:** config-validated name allowlist ∩ manifest-declared names only; exclusion pinned by test (UNDECLARED_SECRET).
- **Dispatcher races:** `close(d.ch)` strictly ordered after `drains.Wait()`; drop counting mutex-guarded.
- **Cancellation status mapping:** `SmokeError.Unwrap` (smoke_types.go:266) makes `errors.Is(err, context.Canceled)` work → `AttemptCancelled` reachable correctly.
- **bwrap isolation lane:** fail-closed admission when native protected-root denial can't be proven (qa_investigation.go:64-69); PID-namespace + die-with-parent contains descendants.
- **Group escape via `setsid`:** inherent OS limitation of pgid ownership; ladder still reaps the leader and `CleanupComplete` degrades honestly — residual risk, not a defect against the coded contract.

**Watch-items (not defects):** `targetIdentity` full-worktree content hashing before/after every approved check is O(tree) with only a per-file cap — degradation, absorbed by shard budgets today; `ExitCode=-1` conflates "signaled" with "never reaped" (`Signal` disambiguates); `Event.Data` can split multibyte runes across 32 KiB chunks (progress-only consumers).

Priority order if fixing: F1 (contract violation, silent evidence corruption), F2 (liveness), F4 (resource bound), F3/F5 (cheap correctness/operability), F6 (docs decision).