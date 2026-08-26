Context pack read. Now inspecting the actual source files.Initial observations on `process.go`: the concurrent `cmd.Wait()` + `StdoutPipe()` drain pattern looks suspicious (documented-incorrect usage), and the drain phase after normal exit looks unbounded. Let me verify against the sprint layer and tests.Now the smoke invocation sites, QA integration, and supporting helpers.Now the QA integration points and supporting helpers.Supporting helpers, wiring, and config next.Web API accepts arbitrary `timeout` strings that reach smoke uncapped and unvalidated. Now the sprint-side tests.Tests read. Now I'll empirically probe the primary suspect — the concurrent `cmd.Wait()` vs `StdoutPipe()` drains race in `DirectRunner.Run` — using a standalone replica of the exact production code under `/tmp/opencode`.Race confirmed: 6/300 runs lost up to 40KB of stdout tail with no truncation flag and exit 0. Let me size the effect for realistic discovery-JSON payloads and verify the setsid hang.The setsid case returned in 4ms — suspicious. Let me check whether the escaped descendant actually outlives `Run` (i.e., Wait's pipe-close unblocks the drains rather than the descendant dying).Both probes conclusive: escaped descendants survive with `CleanupComplete=true`, and the Wait/drain race loses up to 40KB of stdout silently. Now checking remaining counter-evidence: `smokeIdentityReferences`, CI platforms, and the freshness flag.CLI path honors its validated `--timeout` in-process; only the web/TUI durable door swallows parse errors and skips the cap. Baseline test check before write-up:All evidence gathered. Final report:

---

# Findings: `process-execution` surface review

## F1 — HIGH severity, HIGH confidence: `DirectRunner` races `cmd.Wait()` against the stdout/stderr drains; child output tail is silently lost without any truncation flag

**Claim.** `Run` creates pipes via `StdoutPipe()`/`StderrPipe()` (internal/platform/process/process.go:88-95), starts two `copyStream` goroutines, then calls `cmd.Wait()` concurrently in a separate goroutine (process.go:104-109). `os/exec` documents that `Wait` closes the parent pipe ends after seeing the command exit and that calling `Wait` before reads complete is incorrect. When `Wait` wins the race, unread bytes still buffered in the kernel pipe are discarded and blocked `Read`s return errors that `copyStream` swallows (process.go:185-197). `limitedCapture` flags `truncated` only on over-limit writes (process.go:161-177), so the loss is invisible: `ExitCode=0`, `StdoutTruncated=false`.

**Empirical proof** (verbatim replica of process.go/process_unix.go, go1.26.6, `/tmp/opencode/procrace`):
- 200 KB payload (`sh -c 'dd … | tr'`), 300 runs: **6 runs lost tail bytes, up to 40,960 bytes missing**, all with exit 0 and no truncation flag.
- 16 KB payload, 500 runs: 0 losses (below the 64 KiB pipe buffer, drains usually win).
- Repo's own tests pass because they exercise ≤5 KB outputs (process_test.go:21,75,89); nothing pins large-payload capture integrity.

**Observable bad outcomes.**
- Smoke discovery/run stdout >64 KiB intermittently decodes as corrupt JSON → spurious `smoke_discovery_malformed` / `smoke_run_malformed` failures (internal/sprint/smoke.go:105,158-163) that misattribute a parent-side bug to the external harness — an error-truth defect in the gate's verdict.
- QA approved checks persist `StdoutDigest`/`OutputBytes` computed over a partial stream into durable attempt state with `Truncated=false` (internal/sprint/qa_prompt.go:277), so recorded digests describe output the command never produced as observed.

**Fix/regression test:** assign `cmd.Stdout`/`cmd.Stderr` writers (exec-managed copy goroutines that `Wait` joins), or join drains before reaping; add a stress test asserting exact capture of e.g. 256 KB over ≥200 iterations.

## F2 — MEDIUM-LOW severity, HIGH confidence: unbounded whole-file reads while hashing harness-controlled evidence

`validateSmokeRun` hashes every declared evidence file via `hashFile` → `os.ReadFile` (internal/sprint/smoke.go:363, 707-713), then `commitSmoke` re-hashes every evidence and issue file again through `refreshEvidenceFingerprint` (smoke.go:462-463 → verify.go:330-347). File size is `Stat`-checked but never bounded (smoke.go:359-373). Contrast `targetIdentity`, which deliberately refuses files >64 MiB (verify.go:433-435). A buggy or hostile cataloged harness writing a multi-gigabyte file into its declared runs root (harness-writable by design) causes a multi-GB allocation in the orchestrator twice per run — OOM kill of the sprint operation. Regression test: declare an evidence item backed by a >capture-limit file and assert a typed size-bound error instead of a full read.

## F3 — LOW-MEDIUM severity, HIGH confidence: web/durable-operation door silently ignores malformed timeouts and bypasses the documented 24 h cap

The web API accepts a free-form `timeout` string for smoke operations (internal/web/operation_handlers.go:35, 619). `operation_runner.go:81-84` (and :102-104 for verify) does `timeout, _ = time.ParseDuration(req.Timeout)` — parse errors become 0 and silently fall back to defaults; valid values get **no ceiling**. The request source outranks everything in `smokeTimeout` (internal/sprint/smoke_protocol.go:644-657), so `"timeout": "999999999h"` from the API yields an effectively unlimited harness run holding the mutation lease, while `"timeout": "2 hours"` silently degrades to the 30 m default with zero diagnostics. Both diverge from the CLI door, which validates `>0 && ≤24h` (internal/app/sprint_commands.go:927-931, 1237-1241) and from the documented rejection policy (docs/configuration.md:193). Fix: validate identically to the CLI and fail the operation on malformed input.

## F4 — LOW-MEDIUM severity, HIGH confidence: documentation contradicts code/tests on env forwarding, and manifest `environment` declarations are functionally inert

docs/configuration.md:230 states "The built-in platform set is limited to `TMPDIR`, `LANG`, and `LC_ALL`; `PATH` and `HOME` are not forwarded by default" — contradicted by the doc's own example (same file, lines 67-72), both defaults (config/config.go:189, sprint/smoke_types.go:55), and a test that pins PATH+HOME forwarding **against an empty manifest** (smoke_test.go:108-128). Additionally, `smokeEnvironment` seeds the name set entirely from settings (smoke_protocol.go:628-634), so `manifest.environment` can neither add nor remove anything: the field the protocol makes harnesses declare and validate is decorative. Security-relevant: operators reason about env disclosure to external harnesses from that paragraph, and harness authors may believe declaring nothing minimizes exposure. Fix docs or make the intersection real; regression: pin forwarding set == configured allowlist regardless of manifest.

## F5 — LOW severity (high mechanical confidence, low trigger likelihood): process-group escape yields surviving orphans reported as `CleanupComplete=true`

Empirically pinned (pack listed it as unknown): a `setsid` descendant survives `Run`'s TERM→KILL ladder (verified `sleep 6` alive seconds after return; run finished in 3 ms with `CleanupComplete=true`). The same `Wait` pipe-close that causes F1 converts what would be a drain hang into early EOF, so the survivor's later pipe writes are also silently dropped. A daemonizing harness leaks processes past its timeout with clean-cleanup reporting, defeating the "cleanup must complete before verdicts" invariant for this escape class (smoke.go:165-167 can never see it). Honest preconditions: requires a harness that daemonizes; no in-package fix short of tracking descendants, so at minimum document the guarantee's boundary next to process_unix.go:14.

## Defended non-issues

- `OutputBytes > OutputLimit*2` (qa_prompt.go:281) is unreachable — per-stream caps bound the sum to exactly 2×limit — but the budget is still enforced via `Truncated`; cosmetic dead guard, not scored.
- `stopAndWait`'s Getpgid-failure `<-waited` block (process_unix.go:20): ESRCH implies reap, which delivers the buffered wait error immediately; not a practical hang.
- Non-unix `(nil,false)` path followed by `drains.Wait()` (process_other.go:18-23) could deadlock, but CI is ubuntu-only and all tests skip Windows; latent platform-lane issue only.
- Settings-dominant env semantics match test-pinned intent (only docs diverge, F4).
- Investigator check requests cannot inject descriptors — execution rebuilds the map-owned catalog deterministically (qa.go:826-843); denylist/metachar/write-mode rules are sound defense-in-depth.
- Symlink TOCTOU between canonicalization and spawn crosses no privilege boundary (the harness already owns its root); `safeArgv` redaction is deliberate and test-pinned (smoke_test.go:530-533).