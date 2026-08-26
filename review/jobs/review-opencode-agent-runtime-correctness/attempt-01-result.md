Context pack read. Note: the pack was written against commit `8eef70f4` while the frozen target is `5d54525b` — I'll verify line numbers against actual source. Let me explore the package.All package sources and tests read. Now I need to verify SDK-side claims in the module cache — several findings hinge on agentwrap internals (ModelLister forwarding, fallback merge, TerminalOutput bounding, stderr-limit semantics).ModelLister forwarding confirmed through the whole stack — that candidate is dead. Now the policy fallback merge and OpenCode runtime internals:SDK fallback merge confirmed non-issue (original request base, overlay only Provider/Model). Now the OpenCode adapter internals — TerminalOutput bounding, stderr limit, DB handling, final-result ladder:Key SDK facts established: TerminalOutput is bounded SDK-side (96KiB, tail-kept) — that candidate dies. `reconcileFinalState` runs twice per finish (SDK-side latency quirk). Now process/options/errors/health/ids:`WithStderrLimit(0)` falls back to 16KiB default — fine. But note `NewOpenCode` passes `c.Agentwrap.Executable` raw into deletion `exec.CommandContext` while runs get a PATH-default fallback. Checking config defaults plus remaining SDK pieces:Deletion-path callers confirmed: both study and sprint prefer whole-store delete when `RuntimeStorePath` is set. Now checking whether StartRun enforces RequireCaps independently (bears on the Health gating candidate), plus run_control event/cancel handling:run_control read — notable interaction: every mapped event synchronously persists through `appendRunEventWithRetry` (5s budget each) inside the adapter's collector goroutine, which feeds the 1s-drain-window math. Continuing verification:SDK layers understood. Now the consumers' request construction and store-owner derivation, plus running the package tests:`runtimeRequest` gives every sprint run a store path — but note the QA investigator/executor `role` is excluded from `storeOwner`. Checking QA concurrency and the GC call site:Concurrent investigators confirmed in qa.go. Reading the concurrency block and how each shard builds its request:QA investigators get distinct shard-ID owners, but the failed-shard evaluator loop shares one owner (`sprint:p:s:qa:::` — no evidence/call/task in owner). Checking its callers for concurrency, plus consumers of `Result.Events`, health callers, and the ListModels clamp:`smoke_author.go` checks `run.Events` for protected-path writes — if that comes from the result's event ring rather than OnEvent, the 1s-drain loss becomes a policy-detection miss. Reading it:The smoke-author backstop uses `run.Events` but its primary detector is the OnEvent stream — noting the interplay. Now pinning down the `wal_checkpoint(TRUNCATE)` output-format assumption against anything known about the binary's `db` output:`opencode` v1.18.23 is installed — I can empirically verify the checkpoint-parsing assumption against the real binary using a scratch DB in /tmp:The real global DB is 18GB + 16.9GB WAL — striking context. Building a minimal schema'd scratch DB to test the checkpoint output format:Decisive: real binary emits TSV with header (`busy\tlog\tcheckpointed\n0\t0\t0`) — `fields[len-3]=="0"` parses correctly. Checkpoint candidate dead, empirically. Testing `session delete` CLI shape:Targeting empirically confirmed: without `OPENCODE_DB` in env, deletion commands hit the global `~/.local/share/opencode/opencode.db`. Now writing temporary probe tests inside the package (removed afterwards) to mechanically prove the surviving candidates:Now writing temporary probes to mechanically verify the candidate findings:Rewriting the probe file cleanly (the first draft had tangled stand-ins):Probe 1 already proved the core loss (`events=0 stats={Total:0...} warnings=[] sessionIDs=[]`) before panicking on my own double-close. Fixing the scaffold and rerunning:All probes pass. Probe 2 surfaced something sharper than expected: the collector only hands its whole accumulated collection to the result **after** the SDK event channel *closes* — so any >1s gap between Wait returning and channel closure silently discards every event already consumed. And the SDK closes events only after `finalResult` completes, which includes `priceFinalUsage` → possible network fetch. Verifying that:Pricing fetch confirmed: `Ensure()` does an HTTP GET (10s timeout) inside `finalResult`, after process exit. Now checking whether `DeleteSessions` is user-reachable via web/API, the health command context, and the Go version:Cleaning up loose ends: empirically confirming `VACUUM`/`DELETE` behavior through the binary, then removing my probe file:All empirical checks done. Removing my probe file and confirming the tree is pristine:Probe file removed, tree pristine, package tests green. Review complete.

---

# Findings — `opencode-agent-runtime` @ `5d54525b`

All line references verified against the frozen commit. Probes were temporary in-package tests (now deleted); every "PROVEN" claim below was mechanically reproduced and the tree left untouched (`git status` clean, package tests pass).

---

## F1 — StartRun silently discards ALL collected events/stats/session IDs when the collector misses a fixed 1-second handoff window

**Claim.** The event handoff from the collector goroutine to the result is all-or-nothing behind `select { case events := <-eventsCh: … case <-time.After(time.Second): }` (runtime.go:385-401). The collector sends its *entire accumulated collection* only after `run.Events()` closes (runtime.go:334-343); if consumption lags more than 1s at Wait-return, every already-consumed event plus `EventStats`, `Memory`, `SessionIDs`, and the overflow warning are dropped with **no warning and no error**.

**Bad outcome.** Durable study state records nothing about runtime events (`study/runtime_metadata.go:75-81` skips `meta.Events` entirely when all stats are zero); CLI progress reports `RuntimeEvents=0` (`app/operation_runner.go:141`); smoke-authoring's result-ring backstop for protected-path write attribution loses its evidence (`sprint/smoke_author.go:93,:101`).

**Trigger.** Tail-of-run event burst × slow `OnEvent`. Production always wraps OnEvent with synchronous durable SQLite persistence including a 5s retry budget *per event* (`app/run_control.go:204-249,:345-356`), so a backlog can plausibly exceed 1s exactly when Wait returns.

**Evidence / path.** PROVEN by probe: held one event back 1.5s while `Wait` returned instantly → result had `events=0 stats={Total:0 Retained:0 Dropped:0 Limit:0} warnings=[] sessionIDs=[]`.

**Counter-evidence searched.** The ring warning (:397-399) covers only >200 overflow, not this loss; tests pin ring semantics but none drive the drain timeout (coverage gap, matches pack §18); happy-path SDK closes events promptly, so default flows usually drain in time.

**Severity** medium (silent observability corruption, weakened write-attribution backstop; no work loss). **Confidence** high.
**Regression test:** fake run whose events channel stays open >1s after `Wait`; assert either partial delivery with a warning or bounded-grace semantics — currently fails with total silent loss.

## F2 — `OnEvent` keeps firing after StartRun has returned

**Claim.** The collector goroutine outlives `StartRun` whenever the 1s handoff expires (F1) or the ≤5s grace branch synthesizes the cancelled result (runtime.go:362-377); mapped events continue invoking `req.OnEvent` with no terminal fence. PROVEN: probe observed an OnEvent delivery after return.

**Bad outcomes.** (a) `controlledRuntime` persists durable run-control events after `ProposeTerminal` ordering point (`run_control.go:302-342`) — post-terminal journal entries or append errors nobody observes; (b) smoke author's protected-write atomic flags (smoke_author.go:63-73) may flip only *after* the checks at :89-105 executed, converting an LLM write-capable tool call against the product target into a `"concurrent_target_change"` diagnostic instead of hard failure; (c) study `checkpoint()` writes TaskSession records post-return.

**Trigger.** Same lag as F1; plus cancellation where SDK teardown delays channel closure past grace — `finalResult` runs `reconcileFinalState()` **twice** (opencode/runtime.go:458,:473; ≤5s subprocess rounds each) and possibly a rate-table HTTP fetch (pricing.go:44,:300; 10s timeout), and `observedRun.Wait` blocks until all upstream event channels close (observability.go:257-267).

**Severity** low-medium. **Confidence** high on mechanism, medium on practical frequency.
**Regression test:** cancel mid-run with a slow-closing events channel; assert no OnEvent after StartRun returns (currently fails).

## F3 — Log pruning targets the wrong root whenever `XDG_DATA_HOME` is set ambiently but not in config env; pruner silently no-ops forever

**Claim.** `pruneOpenCodeLogs` resolves the data root from config `agentwrap.env` only, falling back to `$HOME/.local/share` (opencode_maintenance.go:25-35). But the OpenCode subprocess environment is `os.Environ()+config env` (SDK process.go:22-24; UltraPlan's own command env wiring opencode.go:108,:182), so an operator-exported ambient `XDG_DATA_HOME` (common on servers/containers) makes OpenCode write logs under `$XDG_DATA_HOME/opencode/log` while the pruner scans `$HOME/.local/share/opencode/log` → `ReadDir` returns `ErrNotExist` → returns nil success forever.

**Empirical.** Real binary v1.18.23 honors ambient XDG: `env -u OPENCODE_DB XDG_DATA_HOME=/tmp/... opencode db path` → `/tmp/.../opencode/opencode.db`. Probe confirmed a 72h-stale log under the ambient-XDG log dir survives `pruneOpenCodeLogs` with empty config env. The SDK's own log scanner resolves ambient-first (`os.Getenv("XDG_DATA_HOME")`, opencode/runtime.go:941-948) — UltraPlan's pruner is the odd one out.

**Context.** This machine's live global OpenCode data dir holds an 18GB DB + 16.9GB WAL — unbounded growth is real-world material, and logs are exactly what this routine exists to bound.

**Severity** low-medium (silent resource leak). **Confidence** high.
**Regression test:** set ambient `XDG_DATA_HOME` via `t.Setenv`, leave config env empty, assert pruning still reaches `$XDG_DATA_HOME/opencode/log` (currently fails).

## F4 — Session-deletion verbs target the GLOBAL default database, not the database that produced the sessions (latent seam defect)

**Claim.** `deleteSessions`/`DeleteSession` build `<exe> db <sql>` and `<exe> session delete <id>` with only `os.Environ()+config env` — no `OPENCODE_DB` targeting, unlike runs which get it via request metadata (opencode.go:93-125,:180-184 vs SDK opencode/runtime.go:155-160). Empirically verified: absent `OPENCODE_DB`, `opencode db path` resolves to `~/.local/share/opencode/opencode.db` (18GB here). If this path fires, per-session deletes hit whatever sessions share those IDs globally, then checkpoint/VACUUM compact the user's entire global DB synchronously under the process-global mutex (VACUUM measured ~2s on a toy DB; hours-scale + ~2× disk on an 18GB DB).

**Why it's latent.** Both current callers prefer whole-store deletion when `RuntimeStorePath != ""` and production study/sprint requests always set one (`study/service.go:54-61`, `sprint/flow.go:37-42`, `study/run.go:127`, `sprint/service.go:1139`). It fires only for store-less results (custom/fake runtimes, future callers) — at which point the verb silently mutates the wrong database.

**Related minor:** an explicitly empty `Agentwrap.Executable` would break deletion commands entirely while runs fall back to the PATH default `opencode` (options.go:20-26); config default prevents this today (config.go:193).

**Severity** medium-if-triggered / low likelihood today. **Confidence** high on mechanics (empirical).
**Regression test:** end-to-end deletion test asserting the commands execute against a scoped DB (currently impossible — no test drives this hook at all).

## F5 — Orphaned store directories are permanently invisible to inspection and GC

**Claim.** `InspectRuntimeStores` skips any hash dir whose `store.json` is missing/corrupt/schema-mismatched (store.go:187-189), `CleanupRuntimeStores` builds exclusively on that view, and nothing else removes such dirs. A crash between `MkdirAll` and the record rename (store.go:58-66) leaks `opencode.db`(+-wal) indefinitely beside live stores — and since `directoryBytes` totals only visible stores, quota GC also undercounts. PROVEN: orphan dir with a 4KiB DB survived aggressive GC (`inspect=0 removed=0`).

**Severity** low (slow leak, needs crash/corruption timing). **Confidence** high.
**Regression test:** create hash dir with DB but no store.json; aggressive cleanup should reclaim or at least surface it.

## F6 — Dead-owner detection treats EPERM as dead (`kill(pid,0)`)

**Claim.** `processAlive` returns false on any non-nil `syscall.Kill(pid,0)` (store.go:239-244); for a live process owned by another uid the kernel answers EPERM. A cross-user-shared scope flips active→retained after 30min (store.go:211-220) and becomes removable under retention/quota/aggressive sweeps while its owner runs. PID-reuse has the staleness compensator; EPERM does not.

**Severity** low (multi-user shared scopes only). **Confidence** high mechanism, low exposure.
**Verification:** check `errors.Is(syscall.EPERM)` → treat as alive-or-unknown rather than dead.

## F7 — QA failed-shard evaluator collapses distinct work onto one shared scoped store

**Claim.** `runtimeRequest` derives `storeOwner` without `role`/`evidence`/`call` (sprint/service.go:1127-1139), so every failed-shard evaluator invocation across all evidence records maps to `"sprint:<p>:<s>:qa:::"` → one hash dir. PROVEN identical paths. Today's harm is bounded (sequential calls; fresh sessions per run; 72h retention): mostly a lying `store.json` Owner identity and lumped retention/quota accounting. But the state machine is concurrency-unsafe for this shape: if evaluation ever parallelizes, interleaved prepare/retain marks the store `retained` while a sibling run still uses it, and quota/aggressive removal keys on `state != Active`, bypassing live-PID protection (store.go:222-226).

**Severity** low. **Confidence** high on collision, speculative on escalation.

## Minor

`retainRuntimeStore` leaves a previous run's `LastError` in place on later success (store.go:85-88) — PROVEN (`state=retained last_error="context deadline exceeded"` after a clean retain). Misleads triage reading `store.json`.

---

# Defended non-issues (hypotheses falsified)

1. **Checkpoint parse `fields[len-3]=="0"`** — empirically verified against real `opencode` 1.18.23: `PRAGMA wal_checkpoint(TRUNCATE)` prints TSV with header `busy log checkpointed` then `0\t0\t0`; third-from-last field is exactly `"0"`. Correct.
2. **`sqliteString` injection** — quote-doubling is sufficient for SQLite string literals in the two fixed statement shapes; identifiers are hardcoded; NUL bytes are rejected by `os/exec`. Empirically: quoted-ID DELETE exits 0.
3. **Backup fallback losing prompt/metadata (incl. `OPENCODE_DB`)** — `fallbackRequest` starts from the original request; the backup stub overlays only Provider/Model (policy.go:776-831).
4. **Model listing broken through the wrapper stack** — Observing/Validating/PolicyRunner all forward `ModelLister` (observability.go:190-196; validation.go:196-202; policy.go:335-341); `requestVariantRuntime` forwards too (opencode.go:212-217).
5. **Unbounded `Result.TerminalOutput`** — SDK bounds it at 96KiB tail-kept before UltraPlan ever sees it (opencode/runtime.go:556-573, applied to streamed and DB-reconciled text alike); the event-derived copy is independently 96KiB head-bounded (agentwrap.go:124; pinned by test).
6. **Health skipping capability gating when Capabilities errors** (health.go:74) — real code smell, unreachable in the shipped composition (static capability map, options.go:132-157; nil-runtime guarded at :527). Robustness nit only.
7. **Cancel-wins over a concurrently successful result** (runtime.go:378-380) — deliberate semantics; window is microscopic; result payload still carried.
8. **`missingSessionPolicy` substring matching** — operates on error-detail strings only; worst cases are a bounded wasted retry or an early stop of a doomed session.
9. **`WithStderrLimit(0)` disabling capture** — falls back to the 16KiB SDK default (options.go:52-58).
10. **Deletion batch aborting mid-way** (partial deletion, no rollback) — inherent to the verb sequence; error surfaces combined output; acceptable for a best-effort reclaimer.

**Test-verification summary:** package tests pass (`go test -count=1 ./internal/platform/runtime/`), `go vet` clean; probes F1/F2/F3/F5(minor)/F7/F6-minor mechanically reproduced then removed; deletion-path claims verified against the installed `opencode` binary v1.18.23 in `/tmp/opencode` scratch databases only.