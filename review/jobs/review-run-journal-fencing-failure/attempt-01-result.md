runcontrol/app are byte-identical to the frozen commit, so I can review the working tree directly. Setting up a plan, then reading source.Source and primary tests read. Now the remaining package files (migration, sanitize, metrics, local log) and a couple of cross-layer callers before I start verifying candidate defects.Package fully read. Now verifying cross-cutting hypotheses: who sets `current_attempt_id`, who injects Clock in production, who calls compactRunJournal, whether any writer emits `cancellation_state='uncertain'`, and the TRD contract lines for quota/journal bounds.All hypotheses verified against callers, guards, and tests. Finalizing the report.# Review: `run-journal-fencing` — findings

Scope reviewed: all of `internal/runcontrol` (sqlite.go, lifecycle.go, retention.go, migration.go, sanitize.go, local_log.go, metrics.go, process*.go, id.go, model.go, errors.go), production callers in `internal/app` (run_control.go, durable_operations.go, run_commands.go), and the package test suite. `internal/runcontrol` and `internal/app` are byte-identical to frozen commit `5d54525`. Every candidate below was actively falsified against callers, CAS guards, and tests before being reported.

---

## F1 — Per-run journal saturation with non-compactable event types permanently wedges `Append` and force-terminalizes healthy live work as `persistence_degraded`

**Severity:** Medium. **Confidence:** high on mechanism, medium on reachability.

**Claim.** Once a single active run's journal exceeds 4096 events or 16 MiB retained payload while containing no deletable event types, every subsequent `Append` for that run fails forever with a non-retryable `CodeQuota`, and both production writers respond by killing the live operation and mislabeling it as a persistence failure — even though persistence is healthy.

**Observable bad outcome.** A long-running runtime-backed operation (agent emitting thousands of warnings/findings/artifacts — e.g., a failing tool loop) is aborted mid-work; its terminal is recorded `persistence_degraded` ("durable event persistence failed"), which is false: SQLite is fine, the run's own bounded-journal policy killed it. Interactive durable operations die as `cancelled` via the same wedge (`RecordOperationEvent` error → `owned.cancel()`). No automatic or documented recovery exists; every future append keeps failing.

**Trigger/preconditions.**
1. One run accumulates > `MaxRetainedEventsPerRun` (4096) events or > `MaxRetainedBytesPerRun` (16 MiB) of payload.
2. The journal's removable classes (`progress`, `message`, `omission`) are absent or exhausted — i.e., mass is concentrated in protected types: `warning`, `finding`, `artifact`, `lifecycle`, `cancellation` (retention.go:75-77 only ever deletes `'progress','message','omission'` in full mode).

**Exact source evidence / execution path.**
- retention.go:58-95 — `compactRunJournal`: if over limit and the DELETE affects 0 rows → `runError(CodeQuota, "event_limit", …, false /* non-retryable */)` at :86; invoked inside **every** Append transaction at sqlite.go:687; failure rolls back the just-inserted event too.
- sqlite.go:620-622 — the reserved-type bypass covers only the *global* storage quota; it does **not** exempt reserved types from the per-run cap, so even recovery-class appends to that run fail.
- No other path deletes events of active runs: `Compact` scans only `terminal_outcome IS NOT NULL` runs (retention.go:115, 130).
- Consumers: internal/app/run_control.go:237-241 — any append error sets `persistenceErr` and cancels the run context; :322-331 — proposes `TerminalPersistenceLost`; retry helper :370-372 retries only `ErrUnavailable|ErrBusy`, so `CodeQuota` is terminal immediately. durable_operations.go:213-215 cancels on record error.
- Runtime mapping feeds protected types: app/run_control.go:421-439 maps runtime `warning|warn|error` → `EventWarning`, `artifact|file` → `EventArtifact`, `finding` → `EventFinding` — none compactable.

**Existing controls / counter-evidence searched.**
- Boundedness itself is deliberate (constants model.go:427-428; error text "reached its bounded capacity"). TRD §18C (L2202) requires only that the store "*may* compact detail" while retaining a replay boundary — no contract mandates unbounded retention, but nothing sanctions terminating live work either; the policy has no defined behavior when the removable mass is gone mid-run.
- Tests pin only the happy compaction shape: retention_test.go:12-36 saturates with `progress` (removable). No test exercises saturation by protected types — the gap the defect lives in.
- Disproof attempts failed: no batch accumulation across appends helps (threshold re-checked each call; zero removable ⇒ first over-limit append fails); terminal/cancellation writes still succeed (they don't call `compactRunJournal`), so the run ends up terminalized rather than hung — confirming the kill path, not refuting it.

**Regression test / fix verification.** Seed a claimed run with 4096 `warning` events (same recursive-insert technique as retention_test.go:16-24), then `Append` any event. Today: `ErrQuota`, permanently, on every subsequent append. Desired pin: either oldest protected detail is dropped past a retained floor (with `history_complete=0` + omission accounting) or the per-run bound degrades gracefully for active runs; assert append succeeds and the replay boundary advances.

---

## F2 — Cold-start creation race makes simultaneous first opens fail hard instead of converging

**Severity:** Low. **Confidence:** high.

**Claim.** `preparePrivateDatabase` uses check-then-create without tolerating `EEXIST`, so two processes opening a fresh workspace concurrently produce a spurious permanent-looking open failure in the loser.

**Bad outcome.** Loser gets `ExitRuntime` "create private run-control directory/database failed"; e.g., `ultraplan serve` boot or a concurrent CLI command fails and must be manually retried.

**Evidence/path.** sqlite.go:158-161 — `os.Mkdir` result unchecked for `fs.ErrExist`; sqlite.go:172-177 — `Lstat`(NotExist) → `OpenFile(O_CREATE|O_EXCL)` window; racer B's `OpenFile` returns `EEXIST` → `classifyStoreError` → `OpenSQLite` fails. No retry upstream: `runControlState.repository` (app/run_control.go:46-56) caches per-process but never retries a failed open; command dispatch calls it once per invocation.

**Counter-evidence.** Window is narrow (µs–ms); after the winner creates the file, any later attempt succeeds; crash-between-steps leaves a valid empty file that subsequent opens accept. Hence low severity, but the failure is concrete and self-inflicted (the loser already did the work of detecting exactly the benign outcome).

**Fix/verification.** Treat `errors.Is(err, fs.ErrExist)` from `Mkdir`/`OpenFile` as success-after-revalidate (re-Lstat for symlink/regular checks). Test: spawn two goroutines/processes calling `OpenSQLite` on the same empty temp dir; both must succeed.

---

## F3 — Zombie owner probes as exact-live match → permanent `stalled` limbo, never arbitrated

**Severity:** Low. **Confidence:** medium-high mechanism; low-medium reachability.

**Claim.** A zombie owner process (exited, unreaped — typical under containers/PID-1s that don't reap) still has `/proc/<pid>/stat` with an unchanged starttime, so reconciliation classifies it "exact live match" and only marks `stalled`. The proof of death (state field `Z`, field index 0 of the same buffer already parsed) is read and ignored, so the run never reaches `interrupted`/`cleanup_uncertain` and stays unresolved indefinitely.

**Bad outcome.** Run remains `running/stalled` forever; `Health.StalledRuns` permanently >0; repeated reconciles re-mark stall idempotently; no terminal ever wins; manual intervention required. Retention can't help (terminal-only).

**Evidence/path.** process_linux.go:30-44 — parses stat after last `)`, takes `fields[19]` (starttime), never inspects `fields[0]` (state); lifecycle.go:481-495 — exact identity match returns `("", "")` → markStalled-only branch (:389-397); nothing else terminalizes (claim-once means no new owner possible; heartbeat impossible from a dead-but-reaped-pending PID).

**Counter-evidence.** Conservatism here is contractual direction ("cannot prove completion") — but a zombie *is* provably dead, so this is unused available proof, not deliberate conservatism. Normal parents reap promptly, keeping reachability low; sticky zombies are realistic in container deployments.

**Fix/verification.** In `probeNativeProcess`, return `ErrProcessNotFound` when the stat state char is `Z`. Verify via Linux integration test mirroring process_integration_test.go:177-233 using a forked child left unreaped, asserting `interrupted` after lease+grace.

---

## F4 — Injected-clock expiry predicate compares RFC3339Nano timestamps as raw strings (latent)

**Severity:** Low (latent). **Confidence:** high on flaw, production currently unaffected.

**Claim.** With an injected Clock, `expiredTimestampPredicate` builds `column <= ?` comparing stored text to `formatTime(now-grace)` lexicographically (sqlite.go:1139-1144). RFC3339Nano omits trailing fractional zeros, so `"…T10:00:00Z"` sorts *after* `"…T10:00:00.05Z"` (`'Z'` = 0x5A > `'.'` = 0x2E): a whole-second timestamp compares as later than an earlier fractional one, corrupting grace/expiry comparisons at sub-second boundaries.

**Why latent, not a live defect.** Grep confirms `Clock:` is injected only in `_test.go` files — production always takes the `julianday('now')` branch (sqlite.go:1143), which parses correctly. Existing tests use whole-second mutable-clock values, masking the trap. Any future embedding (libraries, tools, tests with sub-second clocks) inherits silent misordering.

**Verification.** Unit test: injected clock at `T+0.5s` vs a row stamped `T` whole-second must not expire early under grace; today's string comparison can be made to fail either direction depending on fraction presence.

---

## Defended / non-issues (checked and cleared)

- **Cancellation × append/ack/terminal races:** all mutations are short immediate transactions; losers are idempotent (`changed=false`) or typed-retryable (`CodeConflict`); sequence PK guards duplicate inserts and app-side 5 s retry converges. Pinned by lifecycle_test.go:26-78, 80-100, 119-174 and sqlite_test.go:287-342, 344-396. No lost update found.
- **Reconcile cannot false-interrupt a merely paused owner:** interrupt requires probe `NotFound` or exact birth-field mismatch; a live-but-frozen owner (sleep/SIGSTOP/NFS stall past lease+grace) probes exact-live → stall-only, self-healing on next heartbeat (lifecycle.go:481-495, Heartbeat clears stall at :48-49). Reconciler's reuse of the dead attempt's fence is safe strictly because claim-once holds — verified `current_attempt_id`'s only setter is Claim (sqlite.go:563) and nothing ever nulls it (grep across repo).
- **Single-winner terminal:** loser path commits a read and returns the incumbent (sqlite.go:753-758); attempts-row outcome stamping shares the winner's tx, so post-terminal heartbeats deterministically get `StaleFence` via the `outcome IS NULL` guard (lifecycle.go:40).
- **storageBytes counting backups/fixtures toward quota:** deliberate and test-pinned (retention_test.go:108-118 creates a synthetic `run-control.db.*` fixture to trip soft quota). Residual operational note: up to 3×512 MiB migration backups are unreclaimable by the inline `Compact` response, so large legacy DBs could hold acceptance at soft-quota lockout until an operator deletes `.bak.` files — worth one doc sentence, not a defect.
- **Sequence PK collision classified `Unavailable`(retryable) instead of `Conflict`:** masked by bounded app-side retry; no incorrect outcome possible since the insert fails before the CAS.
- **`_txlock=immediate` unverified at open (pack SA-1):** correctness doesn't depend on it — MAX+1 collisions hit the PK guard and the `last_sequence` CAS prevents lost updates even under deferred transactions; worst case is extra retryable failures.
- **Hard-quota heartbeat refusal killing owners:** explicit policy ("hard quota requires the owner to stop active work", lifecycle.go:24-28); terminal proposals remain writable via reserved types, so runs resolve to `persistence_degraded` rather than hanging. Conservative direction, intentional.
- **`cancellation_state='uncertain'` unreachable:** dead enum surface (only schema CHECK + Health counter); vestigial, no writer, no reader depends on it being set — cosmetic residue, not reported as a finding.
- **Clock authority:** lease writes use Go wall time, expiry predicates use SQLite time — same kernel clock on the supported single-host topology; cross-host sharing would be caught by host-digest birth mismatch anyway.

Zero changes made to target or workspace.