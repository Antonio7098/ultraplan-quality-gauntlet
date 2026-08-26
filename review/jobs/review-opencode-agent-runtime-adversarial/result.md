Context pack read. Note it was written against `8eef70f`, but the frozen target is `50d6f0d`. Let me verify what changed and then read the actual sources.Runtime package is unchanged between the two commits, so pack line references hold. Reading the sources now.UltraPlan side fully read. Now examining the agentwrap SDK in the module cache — this is where the actual subprocess handling lives.Now the SDK internals. Reading the agentwrap opencode adapter and policy engine.Key suspicion forming around `OPENCODE_DB` plumbing: `processSpec` sets it only on the run spec's env, while `queryOpenCodeDB` (SDK reconcile) and UltraPlan's `openCodeDBCommand` (deletion) build env without it. Verifying SDK wiring, errors, wrappers, and process handling.`OPENCODE_DB` mismatch confirmed at SDK level. Now checking error unwrapping semantics and the opencode models/pricing files.Now verifying that production runs actually use scoped stores (making the reconcile mismatch live), plus the deletion callers.Baseline test run plus SDK event-scanning internals.All verification complete: package tests pass (`go test -count=1`, `go vet` clean), production callers confirmed store-scoped, SDK internals traced. Final report:

---

# Findings — `opencode-agent-runtime` (frozen @ 50d6f0d)

## F1 — `OPENCODE_DB` isolation is never applied to `<exe> db` invocations; durable-state reconciliation is dead (and misclassifying) for every production run

**Claim.** Per-run DB isolation is injected only into the run subprocess env. The SDK's durable-reconcile queries and UltraPlan's deletion/checkpoint/VACUUM commands resolve a *different* database than the one the run wrote to.

**Evidence.**
- agentwrap `opencode/runtime.go:145-160`: `OPENCODE_DB` is set on the *local* `env` built inside `processSpec` and returned only in `processSpec.Env`.
- agentwrap `opencode/runtime.go:663-668` (`queryOpenCodeDB`): `cmd.Env = append(os.Environ(), r.env...)` — `r.env` never receives `OPENCODE_DB`. Wired unconditionally as `dbQuery` in `NewRuntime` (`options.go`, `if !r.dbQuerySet { r.dbQuery = r.queryOpenCodeDB }`).
- UltraPlan `opencode.go:180-184` (`openCodeDBCommand`) and `:107-108` (`session delete`): same env construction, no `OPENCODE_DB`.
- Production always isolates: study `internal/study/run.go:127`, sprint `internal/sprint/service.go:1139` (QA investigator included, via `runtimeRequest`) → `runtime.go:576-582` sets `agentwrapopencode.MetadataDatabasePath`.

**Observable bad outcomes.**
1. `finalResult`'s clean-exit recovery ladder (`opencode/runtime.go:458-469`) queries the shared DB, finds no session row, and classifies a run that committed work to the *isolated* DB as `failed`/"finished without a final structured result" — exactly the population this proof was built to rescue. PolicyRunner then retries/falls back (`policy.go:156-158, 248-261`): duplicate LLM spend and spurious failure states in study/sprint.
2. Unconditional usage/cost/assistant-text reconciliation (`opencode/runtime.go:473-480`) is a guaranteed no-op in production: DB usage enrichment, provider-reported cost provenance (`CostSourceProviderReported`), and authoritative assistant-text override silently degrade to streamed-events-only / LiteLLM-priced / absent.
3. Every terminal attempt spawns 4 doomed `<exe> db --format json` subprocesses against the shared DB.
4. Deletion-side `checkpoint/VACUUM/session delete` target the shared DB — correct today only because current callers route store-scoped results to `DeleteRuntimeStore`/`RemoveAll` (`study/service.go:54-60`, `sprint/flow.go:37-44`); the moment `DeleteSessions` is invoked with IDs from an isolated run (any new caller, or a result persisted with an empty `RuntimeStorePath`), it mutates the wrong database.

**Trigger/preconditions.** Any production `StartRun` (all are store-scoped). Outcomes 1–2 are conditional on OpenCode omitting structured finals/usage events; outcome 3 is unconditional.

**Controls/counter-evidence searched.** Grepped both repos: nothing else injects `OPENCODE_DB`; no alternate `dbQuery` wiring reachable from UltraPlan; streamed events usually carry finals/usage, which limits frequency of 1–2 but not 3–4.

**Severity:** high (reliability + cost-accounting correctness across the whole execution platform). **Confidence:** high on mechanism; medium-high on realized frequency.

**Regression test.** Fake executable capturing argv/env: assert `<exe> db ...` invocations carry `OPENCODE_DB=<metadata path>`; integration variant with two SQLite files proving `reconcileFinalState` reads the scoped DB and recovers a clean-exit run as completed. Fix belongs in `queryOpenCodeDB` (read `req.Metadata[MetadataDatabasePath]`) plus an UltraPlan-side DB-path parameter for deletion commands.

## F2 — PID reuse makes `active` stores immortal; immune even to critical disk-pressure sweeps

**Claim.** A dead owner whose PID was recycled is indistinguishable from a live owner (`processAlive` = `kill(pid,0)`, store.go:239-244), and *active* state exempts a store from every removal criterion: `expired` (:221), `overQuota` (:222), and `aggressive` (:223) all require `State != RuntimeStoreActive`; only the `staleActive` flip (:211) transitions out, and it requires `!processAlive`.

**Bad outcome.** Crashed-run stores accumulate permanently. Under disk pressure the scheduler sacrifices retained stores (`run_loop.go:378-388`, `aggressiveCleanup := disk.Critical || active == 0`) but can pause admission indefinitely ("disk pressure paused new workers") while hundreds of MB–GBs sit in un-reclaimable active stores. Quota accounting can never free those bytes anywhere in-tree.

**Trigger.** Owner crash followed by PID wraparound before a GC sweep. Plausible on long-lived hosts given this product spawns several short-lived processes per run; compounding across thousands of stores.

**Counter-evidence.** The 30-minute staleness gate limits false flips; per-store reuse probability is modest — this is a slow-burn leak, not an immediate failure.

**Severity:** medium. **Confidence:** high on mechanics (code-expressed invariant), medium on realization.

**Regression test.** Record with `PID` set to a live unrelated process (e.g., gomaxprocs sleeper) and old `UpdatedAt`, under quota breach and `aggressive=true`; pin desired behavior (e.g., Linux `/proc/<pid>` start-time comparison or staleness-based flip independent of liveness).

## F3 — Store directories without a valid `store.json` are invisible to inspection, quota, and GC forever

**Claim.** `InspectRuntimeStores` skips dirs whose record fails to load (`store.go:187-189`), and `CleanupRuntimeStores` operates solely on that listing. `prepareRuntimeStore` creates the dir before the record exists (`store.go:58-66`); a crash between `MkdirAll` and the temp-file rename — or a torn/unparsable `store.json`, or a future schema-version bump (`loadRuntimeStoreRecord` rejects `SchemaVersion != 1`, :142-144) — leaves a directory containing `opencode.db` (+ WAL) that no code path will ever list, count, or remove. `.store.*.tmp` leftovers land in the same hole.

**Bad outcome.** Permanent unmanaged growth inside the managed root; quota decisions undercount actual bytes, so `maxBytes` enforcement can be arbitrarily defeated by orphaned DBs.

**Trigger.** Crash/power-loss in a millisecond window per store creation (rare but nonzero across fleet-scale runs); version-skew on upgrade/downgrade.

**Severity:** low-medium. **Confidence:** high on mechanism, low on frequency.

**Regression test.** Seed managed root with a hash-named dir containing a large dummy `opencode.db` and corrupt/absent `store.json`; assert `CleanupRuntimeStores` accounts for and removes (or at least reports) it.

## F4 — `checkpointOpenCode` conflates every command failure with "busy": fixed 5 s stall under the global mutex, misleading empty-diagnostic error

**Claim.** `checkpointOpenCode` (opencode.go:151-167) retries 20×250 ms on *any* nonzero exit or unparseable output, then returns `database remained busy: <detail>` — with `detail` empty when the real cause is exec failure (missing executable, EACCES). It runs twice per batch while holding the process-wide `openCodeSessionCleanupMu`.

**Bad outcome.** A misconfigured `Agentwrap.Executable` turns each `DeleteSessions`/`DeleteRuntimeStore`-adjacent call into ≥5 s of serialized stall followed by a diagnostic naming lock contention instead of the exec failure, misdirecting operators. The `fields[len(fields)-3] == "0"` parse is also coupled to an unpinned external output format (no `--format json` passed, unlike reconcile queries).

**Counter-evidence.** With a healthy binary emitting whitespace-separated checkpoint columns the loop works; the 20×retry is a reasonable busy strategy. Failure mode requires environmental breakage.

**Severity:** low. **Confidence:** high on mechanism.

**Regression test.** Script executable exiting nonzero with empty stdout; assert the returned error identifies exec failure and that retry count/backoff is observable/injectable.

## F5 — Cancellation racing natural completion misreports completed runs as `cancelled`

**Claim.** Two mechanisms collapse a finished run into a cancellation when the caller's cancel lands near completion: (a) `runtime.go:378-380` force-overwrites a nil `waitErr` with `ctx.Err()`, flipping a genuinely successful `RunResult` (status, terminal output, usage already in hand) to `Status:"cancelled"` + cancellation error; (b) `policyRun.Wait` returns `RunResult{}, cancellation` *immediately* on a cancelled ctx (`policy.go:376-385`) without waiting for `done`, so the advertised 5-second grace (`runtime.go:364-377`) structurally cannot observe the real final result through the production wrapper stack — the graceful-drain design only works for fakes whose `Wait` ignores ctx.

**Bad outcome.** Work that completed (and may have written its report/artifacts) is journaled and surfaced as cancelled; consumers re-run shards/tasks (duplicate spend) or display a false terminal state. Window is the completion↔mapping interval plus the post-Cancel grace, so it is a narrow race — but the (b) half guarantees the grace period is dead weight in production.

**Counter-evidence.** Study guards continuation-fallback against exactly this (`studyContinuationNeedsFreshFallback` returns false when `ctx.Err() != nil`, run.go:183-186); `TestStartRunCancelsUnderlyingRunWhenContextCancelled` pins only the pre-cancelled fast path, not the success-during-cancel case.

**Severity:** low-medium. **Confidence:** high on mechanism, medium on impact frequency.

**Regression test.** Fake run whose `Wait` returns a successful result after `Cancel`; decide and pin the intended contract (completed-wins vs cancellation-wins) — today's code makes cancellation win even with the finished result in hand.

---

## Defended / non-issues (checked, not reported)

- **SQL-through-binary injection:** `sqliteString` quote-doubling is correct for the two fixed statement shapes; queries pass as single argv elements (no shell). Session IDs originate from the binary's own NDJSON, not model text.
- **Deletion targeting the shared DB** is parity-correct for its actual in-tree callers (store-less legacy results only) — residual risk folded into F1(4).
- **Empty-batch VACUUM:** all in-tree callers guard `len(ids)==0` before reaching the hook.
- **Sanitization bounds** (64 fields / 8 KiB strings / depth 3 / 16 slice items / 96 KiB terminal / 200-event ring / 4 KiB redacted diagnostics) hold and are pinned by tests; raw payload bytes never copied, omission reasons preserved.
- **Cache cohort key** is deterministic (`encoding/json` sorts map keys); no in-tree consumer depends on the metadata keys.
- **Backup-model fallback preserves original metadata** (hence `OPENCODE_DB`) — `overlayRunRequest` only overlays Provider/Model/etc. Note: primary-model `--variant` rides along onto backup attempts; external-CLI semantic risk only, no provable in-tree harm.
- **Retry classification:** `missingSessionPolicy` stop is conservative and unreachable via model-controlled text; runtime-exit-before-retries fallback order matches documented policy.
- **RateTableStore offline** costs ≤10 s once per process (client timeout), then unpriced — bounded degradation.
- **`RedactValue` over-redaction** (whole diagnostic replaced on marker match) is fail-closed, an availability tradeoff, not a leak.
- **Store-record `LastError` staleness** after a later successful run affects no consumer (field unread outside the package).
- **Late `OnEvent` callbacks after `StartRun` returns** (abandoned collector, ≤~2 s) reach idempotent consumer closures; no structural consumer breaks in-tree.