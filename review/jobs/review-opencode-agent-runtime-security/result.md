Context pack read. Now reading the remaining package files.SDK runtime read. Key observation forming around DB targeting. Now reading consumers and remaining SDK files.All primary evidence gathered. Let me do final verification passes on two remaining candidates before writing up.# Review: `opencode-agent-runtime` — security/misuse lens

Target: ultraplan-go @ `5d54525` (clean tree). All line references verified against frozen source; SDK refs are agentwrap@v0.0.0-20260825130518. The `opencode` binary (v1.18.23) is installed here, so several hypotheses were tested empirically, not just read.

---

## F1 — Session-deletion hooks run against the wrong database (no `OPENCODE_DB`), performing destructive maintenance on an unrelated DB

**Claim.** `deleteSessions`/`deleteSession` (internal/platform/runtime/opencode.go:93-125) execute `<exe> db <sql>` and `<exe> session delete <id>` without setting `OPENCODE_DB`, so they always target OpenCode's *global default* database — while every production run isolates its sessions into a per-owner store via `MetadataDatabasePath` → `OPENCODE_DB` (runtime.go:576-582 → SDK opencode/runtime.go:155-160).

**Observable bad outcomes.**
1. Cleanup of isolated sessions is a silent no-op: the actual session/event rows live in `<scope>/.ultraplan/runtime/opencode/<hash>/opencode.db` and are never touched; they survive until the 72h/2GiB GC.
2. Every deletion batch runs `DELETE FROM event_sequence…`, `PRAGMA wal_checkpoint(TRUNCATE)`×20-retry, `VACUUM`, second checkpoint against whatever DB the binary resolves by default — empirically `$HOME/.local/share/opencode/opencode.db` (the user's personal interactive session store). This is unrequested destructive maintenance (full DB rewrite, WAL reset, exclusive locks) on data this flow does not own.
3. Ambient-inheritance hazard: `cmd.Env = append(os.Environ(), c.Agentwrap.Env...)` (opencode.go:108,182) passes any ambient `OPENCODE_DB` through. Run launches are protected — SDK `setEnvValue` strips pre-existing `OPENCODE_DB` before appending the isolated path (SDK opencode/runtime.go:191-199) — but the deletion hook has no equivalent strip, so a process launched inside an OpenCode-managed environment (agent-driven shells do export it; verified in this very harness) deletes/vacuums the *parent's live* database.

**Trigger/preconditions.** Production trigger is real and frequent: sprint review deletes every completed coverage session per-ID via `s.deleteCompletedSession` (internal/sprint/review.go:643-645). Those requests are store-scoped (`runtimeRequest`, internal/sprint/service.go:1137-1139). All other callers prefer `DeleteRuntimeStore`, so review is effectively the only production user of the SQL hook — and it always hits the wrong DB.

**Evidence / execution path.**
- opencode.go:103 `query := "DELETE FROM event_sequence WHERE aggregate_id = " + sqliteString(sessionID)`; :104 `openCodeDBCommand(ctx, c, query)`; :107 `exec.CommandContext(ctx, exe, "session", "delete", sessionID)`; :113/:116/:121 checkpoint/VACUUM/checkpoint. None set `OPENCODE_DB`.
- Empirical (binary v1.18.23): `env -u OPENCODE_DB opencode db path` → `/home/.../.local/share/opencode/opencode.db`; with `OPENCODE_DB=<iso>` → the isolated file. So the env var governs the `db` subcommand, and its absence selects the global store.
- Sessions land only in the isolated DB: study/run.go:127, sprint/service.go:1139 set `RuntimeStorePath`; runtime.go:580 maps it to `MetadataDatabasePath`.

**Controls/counter-evidence searched.** The comment at opencode.go:101-103 shows schema awareness but no DB targeting; no workspace contract governs this sequence (pack Unknown 1); no test drives the hook end-to-end (pack §18 gap); `sqliteString` correctness is not the issue here — targeting is. The whole-store RemoveAll path is correct and explains why study/sprint-execute don't surface this; review.go does.

**Severity:** high (wrong-database writes from product code + defeated cleanup intent). **Confidence:** high (binary behavior demonstrated; code path fully traced).

**Regression proof:** integration test that starts a fake-isolated run (store dir with an `opencode.db` containing a seeded `event_sequence` row for session `ses_x`), invokes `Adapter.DeleteSession(ctx,"ses_x")` with a stub executable that asserts `OPENCODE_DB` equals the store path in its child env; currently the executable receives no such variable. A CLI-level variant: seed both global and isolated DBs, delete, assert only the isolated DB changed.

---

## F2 — `missingSessionPolicy` matches attacker/binary-influenced error text, suppressing retry/fallback for unrelated failures

**Claim.** `openCodeSessionNotFound` (opencode.go:140-148) substring-matches `"session not found"` over `UserDetail`, `DebugDetail`, `ResponseBody`, and the cause chain, and short-circuits to `PolicyDecisionStop` before `BasicPolicy`. Two of those fields embed subprocess-controlled content: runtime-exit errors carry the full stderr tail (`WithDebugDetail("exit_code=%d stderr=%s", …)`, SDK opencode/runtime.go:1255-1260) and decode errors embed the raw malformed stdout line (`classifyDecodeError`, SDK :1247-1253).

**Bad outcome.** A transient, retryable failure becomes terminal because incidental text matched. Concrete path: an agent task whose tool output or stderr happens to contain `Error: Session not found` (e.g., the agent debugging its own app), followed by an unrelated nonzero exit → category `runtime_exit` (retryable, fallback-eligible per policy.go:248-276) → UltraPlan policy sees the phrase in DebugDetail → stop, no retry, no backup model. Same for a malformed NDJSON line quoting the phrase. The matched text originates downstream of the LLM/tool trust boundary, not from OpenCode's own continuation logic.

**Controls/counter-evidence.** The pinned test (opencode_test.go:20-28) only proves the intended case fires; nothing bounds over-matching. Category checks elsewhere are exact (`defaultRetryable` switches on `err.Category`). Counter-argument considered: in practice "session not found" from stderr usually *is* a continuation failure — but the policy cannot distinguish speaker, and `malformed_event`/`runtime_exit` payloads are precisely the channels where model-influenced text lands.

**Severity:** low-medium (lost resilience, misclassified terminal failures visible in study/sprint status). **Confidence:** medium-high mechanism, medium occurrence.

**Regression proof:** unit test feeding `BasicPolicy`-wrapped `missingSessionPolicy` an `SDKError{Category: runtime_exit, DebugDetail: "exit_code=1 stderr=Error: Session not found (from user's app)"}` asserting a retry decision (fails today).

---

## F3 — Event collector outlives `StartRun`; late `OnEvent` calls race durable bookkeeping and can mislabel cancellations as persistence loss

**Claim.** `Adapter.StartRun` returns while its collector goroutine may still invoke `req.OnEvent`: the post-wait drain has a 1s cap (runtime.go:385-401) and the 5s-grace branch abandons the collector entirely (:364-377). `controlledRuntime.StartRun` wraps `OnEvent` with fenced durable appends bound to `runCtx` (internal/app/run_control.go:204-249) and inspects `persistenceErr` only after the base returns (:319-322).

**Bad outcome.** A late event arriving after `cancel()` (run_control.go:317) fails `Append` with context cancellation, which is non-retryable (`retryableRunControlError` = ErrUnavailable/ErrBusy only, :370-372) → `setPersistenceErr` → the run is durably proposed as `TerminalPersistenceLost`/"durable event persistence failed" (:322-331) instead of cancelled/completed. Secondary effects: the synthesized-cancelled result carries no `Events`, `EventStats`, or `SessionIDs` (resume checkpoints lose the session ID even though it exists durably in the isolated store), and consumer callbacks (session checkpointing, progress UI) fire after their owner returned.

**Trigger.** Cancellation where the waiter misses the 5s window, or normal completion where draining >1s (large final event burst through sanitize+consumer callbacks), plus ≥1 further event delivered after return.

**Controls/counter-evidence.** Only the fast pre-cancelled path is pinned (runtime_test.go:481-497); neither grace branch is tested (pack §18). The wrapper's mutex prevents data races but not the ordering hazard; late events between base-return and `cancel()` append harmlessly — the defect needs delivery after `cancel()`, which the adapter makes possible by design.

**Severity:** medium-low (wrong durable outcome class for user cancels; lost resume metadata). **Confidence:** medium-high mechanism, medium occurrence.

**Regression proof:** fake runtime whose `Events()` stays open past `Wait`; cancel caller ctx; assert (a) no `OnEvent` after `StartRun` returns, or (b) controlledRuntime proposes `TerminalCancelled`, not persistence-lost.

---

## F4 — No policy wall-clock bound; provider-controlled `RetryAfter` can stall a logical run arbitrarily

`NewOpenCode` sets `MaxAttemptsPerTarget`, backoff capped at 30s, `RetryRateLimits: true` — but no `MaxElapsed` (opencode.go:52-56). `BasicPolicy.delay` prefers provider-signalled `RetryAfter`/`ResetAt` over backoff with no ceiling (SDK policy.go:186-201); values are parsed straight from provider headers/metadata (`retry-after-ms`, HTTP-date years out) without an upper bound (SDK opencode/rate_limit.go:202-223, 297-333), and `PolicyRunner` sleeps the full delay bounded only by caller ctx (policy.go:477-483). Per-attempt `req.Timeout` does not cover inter-attempt sleeps. Bad outcome: one crafted/misbehaving 429 with `retry-after: 100000000` parks a sprint stage indefinitely with only manual cancellation as escape. Severity low, confidence high (mechanism verified end-to-end; needs unusual provider header). Regression: unit test pinning a sane max delay in `NewOpenCode`'s policy configuration.

---

## F5 — Log pruning resolves `XDG_DATA_HOME` differently than the subprocess it prunes for

`pruneOpenCodeLogs` reads `XDG_DATA_HOME` **only** from configured `Agentwrap.Env` (opencode_maintenance.go:26-35), while the subprocess sees `os.Environ()+env` (SDK process.go:22-24) and OpenCode itself resolves XDG-first. On hosts exporting `XDG_DATA_HOME` ambiently but not configuring it in `Agentwrap.Env` (common on some distros), logs land under `$XDG_DATA_HOME/opencode/log` while pruning scans `$HOME/.local/share/opencode/log` → the 48h/128MiB caps silently never apply; log growth is unbounded. Note the asymmetry is internal inconsistency, not just external: the SDK's own log scanner reads the parent process env (`os.Getenv`, SDK opencode/runtime.go:943). Severity low, confidence high. Regression: test asserting `pruneOpenCodeLogs` derives its root identically to the subprocess env composition.

---

## F6 — PID-reuse pins stores as permanently active, immune to all GC including aggressive sweeps

`processAlive` = `syscall.Kill(pid,0)` (store.go:239-244); an active store whose recorded PID was recycled by any long-lived process never satisfies `staleActive` (needs dead PID, store.go:211), is spared by quota removal (`State != RuntimeStoreActive`, :222) and by aggressive sweeps (:223). Dead-owner retention then leaks the store until manual intervention; PID recycling on long-running hosts with ~4M pid wraparound makes this reachable, and `store.json` is writable by any workspace-local writer (an agent could also deliberately pin `PID: 1`). The 30-min staleness window compensates only for genuinely dead PIDs. Severity low, confidence high mechanism / low frequency. Regression: test simulating a recycled PID owned by a live process and asserting eventual reclamation or an ownership-proof field (e.g., process start-time comparison).

---

## Defended / non-issues (checked, not reported)

- **SQL escaping**: `sqliteString` quote-doubling is sufficient for the single string-literal context used; both SQL statements have fixed shapes; argv paths are shell-free; NUL/newlines harmless in literals. The defect is targeting (F1), not injection.
- **Checkpoint parsing**: verified against the installed binary — default `tsv` output of `PRAGMA wal_checkpoint(TRUNCATE)` is `busy\tlog\tcheckpointed\n0\t0\t0`, so `fields[len(fields)-3]=="0"` (opencode.go:157-159) parses correctly, including the busy case.
- **Payload/diagnostic bounds hold**: mapped events are size-bounded by their input (sanitize never expands beyond the ≤16MiB scanner-bounded line); diagnostics ≤4096B + `config.RedactValue`; terminal output ≤96KiB from *both* sources — the SDK bounds DB-reconciled text via `boundTerminalOutput`, closing the apparent bypass at runtime.go:626.
- **Raw-payload non-persistence**: `cloneAnyMap` sanitizes; `RawOmitted=presence` is faithful; `ObservingRuntime` enforces `PersistUnsafeRawPayloads:false` at the boundary.
- **Backup-model fallback preserves isolation**: `fallbackRequest` starts from the original request, so `opencode.database_path` survives the provider/model overlay (SDK policy.go:776-831).
- **Store containment guard**: `validatedRuntimeStoreDir` marker+basename+absolute checks hold; GC `RemoveAll` cannot escape the managed shape, symlinked hash dirs are invisible to inspection (and `RemoveAll` unlinks rather than follows).
- **Health bypass of the policy stack** is intentional; `requiredPreflight` re-enforces `RequireHealth` inside every launch.