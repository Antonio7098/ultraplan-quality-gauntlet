Now let me read the test files to check what's actually pinned.The `opencode` binary is available locally. Let me safely probe its CLI contract (read-only queries + scratch DB only) to verify the deletion/checkpoint assumptions.Let me do a final counter-evidence check on the SDK side: does any SDK test pin the reconcile env behavior intentionally?All verification complete. Here is the review.

---

# Findings — `opencode-agent-runtime` (failure/concurrency lens)

Baseline: frozen target `5d54525` — worktree `internal/platform/runtime/` is byte-identical (`git diff --stat 5d54525 -- internal/platform/runtime/` empty); agentwrap pin `v0.0.0-20260825130518-dccd575bd101` read from module cache. Package tests pass. External CLI claims below were verified empirically against `opencode` v1.18.23 using a scratch `OPENCODE_DB`; the reviewer's own session DB was never touched.

## F1 · Session deletion runs against the wrong database — review-stage cleanup can never succeed
**Severity: High · Confidence: High**

- **Claim:** The `deleteSessions` hook executes SQL-through-binary without `OPENCODE_DB`, so it targets the ambient global OpenCode database, while every production run writes sessions into a per-owner scoped store.
- **Evidence:** Runs get isolation via metadata `opencode.database_path` → `OPENCODE_DB` in the *run* subprocess env only (`agentwrap/opencode/runtime.go:155-160`). The deletion commands build their own env without it: `opencode.go:104` (`openCodeDBCommand`, `cmd.Env = append(os.Environ(), c.Agentwrap.Env...)`, `opencode.go:180-184`) and `opencode.go:107-111` (`session delete`). All production requests set `RuntimeStorePath` unconditionally (`study/run.go:126-127`, `sprint/service.go:1137-1139`).
- **Empirical proof:** `opencode db path` honors `OPENCODE_DB` (printed scratch path under override); without it resolves to `~/.local/share/opencode/opencode.db`. `opencode session delete ses_<missing>` exits 1 with `Session not found`. Schema check: `event_sequence`, `session`, `message`, `part` exist as assumed — only the target DB is wrong.
- **Bad outcome / trigger:** Any completed sprint Review deletes per-session via `Adapter.DeleteSession` (`review.go:643-645`) — each call is a guaranteed failure (swallowed by `_ =`): two wasted subprocesses per session under the process-global `openCodeSessionCleanupMu`, a no-op `DELETE FROM event_sequence` against the user's personal global DB, and reviewer session data retained in `.ultraplan/runtime/opencode/<hash>/` until the 72 h GC backstop. The batch aborts at the first failing `session delete`, so the checkpoint/VACUUM tail never runs either.
- **Counter-evidence searched:** Study/planning/execute paths prefer whole-store `DeleteRuntimeStore` (works — `RemoveAll`); GC bounds the retention damage; `sqliteString` escaping is sound; no contract document pins this sequence (context-pack unknown #1). Nothing rescues the wrong-DB targeting.
- **Regression test:** Prepare a scoped store, insert fixture rows via `<exe> db` with `OPENCODE_DB` set, call `Adapter.DeleteSessions`, assert the *scoped* file's rows are gone and that child processes received `OPENCODE_DB=<scoped path>`. Fails today.

## F2 · Durable-DB reconcile reads the global DB — terminal-proof recovery, usage/cost enrichment are dead code under standard configuration
**Severity: High · Confidence: High**

- **Claim:** `queryOpenCodeDB` (`agentwrap/opencode/runtime.go:648-688`) omits `OPENCODE_DB` (`cmd.Env = append(os.Environ(), r.env...)`, :666-668), so `reconcileFinalState` queries the default/global database even though the run wrote to the isolated one.
- **Execution path:** `finalResult` calls reconcile unconditionally (:473, including cancellation/timeout outcomes) and in the recovery ladder (:458-470). For isolated runs the queried session cannot exist there → `proof.completed` is always false, so a clean exit without final/idle/output markers always falls through to `runtime_exit` failure (:467-470), triggering `BasicPolicy` retries/fallback against already-finished work; OpenCode-reported cost/usage enrichment (:471-509) and the authoritative assistant-text override (:475-480) never apply — cost provenance silently degrades from `provider_reported` to rate-table estimates. Worst-case teardown latency grows by ~5 s (shared 5 s ctx over four subprocess queries; doubled in the deep-ladder branch), which feeds F3's grace race.
- **Counter-evidence searched:** SDK tests pin the run-subprocess env (:161-178) but exercise reconcile **only with an injected fake `dbQuery`** (`runtime_test.go:1326-1352`) — the real wiring is unpinned; nothing propagates `MetadataDatabasePath` into the query path. This is exactly the class TRD §11.4's DB-isolation design intends to serve.
- **Regression test:** Start a run whose fixture stdout lacks terminal markers with `MetadataDatabasePath` pointing at a fixture SQLite containing a finished assistant message; assert completed status + DB-recovered usage. Fails today (query hits default DB).

## F3 · StartRun abandons its event collector; >5 s cancellations return an observation-stripped result and discard the true outcome
**Severity: Medium · Confidence: High (mechanism) / Medium (frequency)**

- **Claim:** `Adapter.StartRun` never joins its collector goroutine (`runtime.go:332-344`). In the grace branch (:368-377) it synthesizes `Result{Status:"cancelled"}` carrying **no Events, EventStats, Memory, SessionIDs, Usage, or Cost**, marks the store retained with only the ctx error, and returns — while the waiter may still deliver a fully-mapped real result seconds later, which is dropped on the floor. The surviving collector keeps invoking `req.OnEvent` after return, unaccounted.
- **Trigger:** Cancellation whose SDK teardown exceeds the 5 s window — realistically reachable because `Cancel` cleanup alone budgets 2 s (`process.go:83-96`) and `finalResult`'s reconcile adds up to ~5–10 s more (F2) before `done` closes.
- **Bad outcome:** A run that actually finished during teardown is recorded as bare "cancelled": lost usage/cost, lost session IDs (so consumer-side `CleanupSessionIDs` is empty), duplicate re-execution on resume, and indistinguishability from pre-output cancellation. Late `OnEvent` delivery is harmless in production only accidentally — the `controlledRuntime` gate drops it post-cancel (`run_control.go:204-249`, `cancel()` at :317); unwrapped consumers show the hazard class concretely (review's worker callback sends into a channel closed after `wg.Wait`, `review.go:524-529` vs :534-542 — a send-on-closed-channel panic pattern for any non-gated caller).
- **Controls/counter-evidence:** Only the pre-cancelled fast path is pinned (`runtime_test.go:481-497`); the grace branch has zero coverage (confirms pack §18). Normal-completion drain has a separate silent 1 s cap (`runtime.go:400`) that can likewise return before the collector finishes, losing EventStats/SessionIDs with no warning.
- **Regression test:** Fake `Run` whose `Wait` blocks past 5 s while events keep flowing; assert either eventual real-result adoption or an explicit warning/marker distinguishing abandoned teardown, plus that `OnEvent` is quiesced before `StartRun` returns (or documented otherwise).

## F4 · PID reuse permanently exempts dead owners' stores from all GC paths
**Severity: Low-Medium · Confidence: High (mechanism) / Medium (occurrence)**

- `store.go:211` flips stale active stores to retained only when `!processAlive(store.PID)`; `processAlive` is `syscall.Kill(pid,0)==nil` (:239-244). If the recorded PID is recycled by any unrelated process, the store stays `active` forever, and expired (:221), quota (:222), and aggressive (:223) removal all exclude active stores — a multi-MB `opencode.db`(+-wal) leaks indefinitely per owner hash. The 30-minute staleness window never applies because liveness is misreported. No timestamp-based forced flip exists.
- **Counter-evidence searched:** Same-user GC processes make false-dead unlikely; EPERM edge irrelevant in-process; sprint/study sweeps run regularly but respect the active exemption.
- **Regression test:** Store record with `PID` bound to a live unrelated process and month-old `UpdatedAt`; assert an aggressive sweep retires or flags it. Kept today.

## F5 · Log-pruning root diverges from the subprocess's effective environment
**Severity: Low · Confidence: High (mechanism)**

- `pruneOpenCodeLogs` resolves `XDG_DATA_HOME` solely from configured `Agentwrap.Env` (`opencode_maintenance.go:26`, `envValue`:79-87), while the run subprocess effectively sees `os.Environ()+env` (`agentwrap/opencode/process.go:22-24`) — an ambient `XDG_DATA_HOME` wins for the binary when config lacks the key. Pruning then caps/deletes `$HOME/.local/share/opencode/log` while the real logs grow elsewhere: silent reclamation failure plus deletion activity against a directory this deployment doesn't own. (The SDK's own log classification uses a third resolution — ambient `os.Getenv`, `sdk opencode/runtime.go:943`.) Config-supplied env present → consistent; divergence needs only ambient-env users.

---

## Defended non-issues (investigated, counter-evidence found)

- **Checkpoint busy-loop parsing** (`opencode.go:151-167`): `fields[len(fields)-3]=="0"` is correct for `opencode db` v1.18.23 — default TSV emits header `busy log checkpointed` plus row `0 N M` (verified empirically; JSON mode not used here).
- **`sqliteString` quote-doubling**: sufficient for SQLite string literals in these fixed statement shapes; identifiers are static. Session IDs originate from the local harness's own schema, not an adversarial source.
- **Grace-path `OnEvent` leakage reaching product callbacks**: blocked in-tree by the `controlledRuntime` persistence gate (post-cancel appends fail fast, short-circuiting `configuredOnEvent`) — the review closed-channel panic is therefore latent-contract, not reachable as written; folded into F3 rather than reported standalone.
- **Aggressive disk-pressure cleanup sacrificing retained stores** (`study/run_loop.go:376-381`): deliberate, commented policy; live-active stores spared, matching `store_test.go:103-121`.
- **Sanitization/redaction/ring bounds**: behavior matches tests and doctrine (raw bytes never copied; diagnostics ≤4096 redacted; terminal output separately bounded).
- **`retainRuntimeStore` preserving a previous run's `LastError` on subsequent success** (`store.go:85-87`): misleading label only; cosmetic, not reported as a finding.