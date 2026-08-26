Context pack absorbed. Now reading the primary sources myself.Sources absorbed. Reading the remaining primary paths and supporting files.Now the app-layer wiring and remaining package files.App wiring understood. Checking the remaining platform files, tests, and searching for space-reclamation pragmas and Compact callers.No `auto_vacuum`/VACUUM exists for the run-control store — important lead. Reading the tests to map what's actually pinned before running experiments.Now reading the authoritative runbook contract, then building an out-of-tree harness for empirical verification.Contract confirmed. Now checking module coordinates and building an out-of-tree verification harness.Writing the harness programs.Internal-package restriction — copying the frozen surface sources into the harness module instead (working tree is identical to the frozen commit for this surface).Growth plateaued below quota and runs were never terminalized (retention correctly skips active runs). Adjusting: more runs, terminalize before aging.Retention-deletion-cannot-shrink-file is confirmed (delta=0 with zero runs). But appends stopped early at ~45/run — need to see why before scaling. Making the error visible.Mystery solved — the per-run trim guillotines 256 events per pass, leaving small tails. Reshaping: 8 runs × 250 appends ≈ 16 MiB live each → high-water well above soft quota.Finding 1 nailed down empirically: full retention drain leaves byte-identical file sizes and permanent accept lockout. Now building the hot-WAL restore experiment.Crash state staged exactly as SIGKILL leaves it (hot WAL, stale SHM, no live writer). Running the documented offline restore.Resurrection proven: the "restored" workspace came back with the crashed run intact and `status=ok`. Running the contrast case (sidecars removed) plus a deterministic state-regression probe.The probe's `changed=0` is the *event* count; the UPDATE's internal `RowsAffected()==1` committed the regression `tombstone → compacted`. State-machine violation reproduced deterministically. Now the restore contrast case and remaining small files.Contrast confirms the missing control is precisely stale-sidecar handling. Closing coverage on the remaining small files and verifying the target tree is untouched.All verification complete; target tree untouched (`git status` clean). Final report:

---

# Findings — `run-recovery-retention` adversarial review

**Method note.** Context pack read, then all primary sources re-inspected independently. Hypotheses were tested against callers/guards/tests; three survived as defects and were **empirically reproduced** out-of-tree (harness module in `/tmp/opencode/rcprobe` linking a byte-identical copy of the frozen package — `git diff 5d54525..HEAD -- internal/runcontrol` is empty; target and workspace untouched).

---

## F1 — HIGH: `RestoreBackup` silently resurrects pre-restore committed state when a hot WAL exists

**Claim.** `RestoreBackup` renames the backup over `run-control.db` but never removes or accounts for `run-control.db-wal`/`-shm` (migration.go:347). On next open, SQLite crash-recovery replays the stale WAL frame chain onto the restored image, silently overwriting the rollback.

**Observable bad outcome (reproduced end-to-end with frozen source + pinned driver).**
1. Setup: workspace created via `OpenSQLite`; run A accepted; clean close; main DB copied to `run-control.db.bak.20260101T000000000Z`.
2. Crash simulation: reopen, accept run B + append 3 events, `SIGKILL` before close → `run-control.db-wal` (370 KB) and `-shm` left behind, exactly as a real crash leaves them. No active writer exists — this satisfies recovery.md:238-241's "stop every UltraPlan process… never copy only the database while a WAL writer *is active*".
3. `RestoreBackup(...)` returns **success**.
4. Fresh open: **both runs retained** (`retained runs after restore: 2`, run B present with its 3 events), `Health.Status = ok`. The exact data the operator rolled back is back, with no error anywhere.
5. Contrast run (same staging, sidecars removed after rename): exactly 1 run retained, run B gone — proving the missing control is precisely stale-sidecar handling.

**Trigger/preconditions.** Any restore performed when the DB has a non-empty WAL — i.e., after any crash/kill (the primary scenario a restore runbook exists for), or any unclean stop. The WAL need only contain frames beyond the last auto-checkpoint (~1000 pages window); committed transactions in it replay over the restored image. Frames are individually checksum-valid and pages are page-consistent, so `integrityCheck` passes and no corruption error surfaces — silent resurrection, worst case.

**Execution path.** `RestoreBackup` → `os.Rename(tempPath, databasePath)` (migration.go:347) with `run-control.db-wal` untouched → next `OpenSQLite` → driver WAL recovery applies valid frames → restored pages overwritten by pre-crash images.

**Existing controls / counter-evidence searched.** Name-shape/Lstat/size checks, read-only integrity validation, temp+fsync+rename (migration.go:303-351) — all pass here; none address sidecars. Runbook warns only about *active* writers. The test that pins restore (`migration_test.go:119-152`) calls `repository.Close()` first, which deletes the WAL — so the suite structurally cannot catch this (pack unknown #5 adjudicated: real defect). Schema-version validation absence is fail-closed downstream (next open rejects/migrates), not relevant here.

**Contract violated.** Assignment trust boundary "corrupt/hostile pre-existing DB files must not be silently replaced" — the restore result itself is silently replaced; recovery.md:236-243 restoration semantics; TRD:2354-2356 (crashed owner now reappears as a fresh active run instead of preserved interrupted evidence).

**Severity / confidence:** High / High.

**Proving regression test:** stage crash-WAL state as above, run `RestoreBackup`, assert `Snapshot(post-backup-run)` → `ErrNotFound` and run count equals pre-backup count. Fix direction: remove `-wal`/`-shm` after rename (workspace is documented offline at that moment), or refuse restore when sidecars exist without explicit operator acknowledgment.

---

## F2 — HIGH: Quota gates measure physical bytes that no product path can ever reclaim — permanent acceptance lockout after one fill episode

**Claim.** All quota arithmetic keys off `storageBytes()` (retention.go:35-56: sum of every regular `.ultraplan/run-control.db*` file). Retention deletes rows, but SQLite never returns freed pages to the OS without `auto_vacuum`/`VACUUM`; `Compact`'s reclamation pragmas are `wal_checkpoint(PASSIVE)` (does not truncate) and `incremental_vacuum(64)` (a **no-op** — `auto_vacuum` is never set anywhere in the repo; grep-verified, the only VACUUM in-tree targets the unrelated OpenCode DB). Therefore measured usage is monotonic non-decreasing regardless of how much history retention drains.

**Observable bad outcome (reproduced).** With hard=96 MiB (soft=80 MiB): grew 5 runs of real fenced journal traffic to 82.5 MiB (accept refused at run 6 via sqlite.go:384-395 ladder); terminalized all runs; injected clock +40 days; ran `Compact` until fully drained — `deletedRuns=5, deletedEvents=1250, retainedRuns=0`. Result: **file sizes byte-identical (delta=0)**, `Health.Status=degraded` with zero active runs and zero retained history, and `Accept` → `ErrQuota` ("soft quota prevents new acceptance", retryable) **forever**. No CLI/web/TUI/timer invokes `Compact` outside this branch (grep-complete: sqlite.go:387), so aging also only occurs under accept pressure that cannot succeed.

**Aggravators (verified in source).**
- `storageBytes` counts migration backups (≤3 × 512 MiB, migration.go:268-291) and any same-prefix fixture toward the gates — backups consume the very headroom reserved for recovery writes; recovery.md:230 tells operators to "free space outside the active database" while the product counts those files inside it. Even `--support-export .ultraplan/run-control.db.export.json` would inflate quota.
- Default-config window analysis: pressure fires at 409.6 MiB, soft refusal at 496 MiB — throughout that window every Accept pays a full compaction scan yet gains zero measurable headroom, then crosses into permanent refusal.
- `compactRunJournal` trims in 256-event guillotines per pass even when only the byte cap is exceeded (excess computed from count only, retention.go:68-74), maximizing churn/freelist without affecting measured size either way (bounded ≤32 passes, so not separately charged).

**Counter-evidence searched.** Docs acknowledge operator intervention post-quota (recovery.md:230-233), but the same contract states "UltraPlan begins compaction at 80 percent" precisely to manage pressure (TRD:2498 bounded-retention requirement; Health escalates degraded→failed on these numbers). Nothing in-tree lowers the metric — not expiry, not tombstoning, not checkpoints. The reserved 16 MiB headroom protects lifecycle writes only until physical exhaustion (SQLITE_FULL backstop, fault-pinned).

**Severity / confidence:** High / High (mechanism certain; reproduced).

**Proving regression test:** seed > soft-quota of terminal history, drain `Compact` past full+tombstone horizons, assert `storageBytes < softQuota` and a subsequent `Accept` succeeds (fails today at delta==0/ErrQuota). Fix directions: `PRAGMA auto_vacuum=INCREMENTAL` at schema creation (+ full checkpoint after vacuum), periodic `VACUUM` under pressure, or account free-page bytes (`freelist_count × page_size`) in `storageBytes`.

---

## F3 — MEDIUM-LOW: `compactTerminalRun` CAS omits `record_state`, regressing `tombstone → compacted`; row-vanished race aborts the whole compaction and the caller's Accept

**Claim.** The snapshot update guards only on terminality (retention.go:194-201: `WHERE run_id = ? AND terminal_outcome IS NOT NULL`). A compactor whose candidate list predates another actor's transition can move a run backwards out of `tombstone` — a state whose retained-content class it no longer matches (accepted/claimed/lifecycle/recovery events already deleted at tombstone stage, retention.go:185-188).

**Reproduced deterministically.** Normal ladder drives run to `record_state=tombstone`; invoking `compactTerminalRun(run, RecordCompacted)` — exactly what a stale concurrent stage-1 worker executes — commits successfully and flips the snapshot back to `record_state=compacted` (internal UPDATE `RowsAffected()==1`).

**Production trigger.** `Compact`'s only caller is Accept's ≥80% pressure branch (sqlite.go:387); two processes accepting concurrently at pressure interleave stage scans across processes (each stage re-queries, but selection→transaction windows remain). Sibling race in the same function: if the candidate row is deleted (stage-3 expiry by another process) between scan and transaction, `changed != 1` returns `CodeConflict` which aborts the **entire** `Compact` and therefore the caller's `Accept` (spurious conflict exit-class failure; self-heals on app retry, but the abort discards already-completed stages' work reporting).

**Consequence.** Misleading retention/diagnostics state (`run show`, support export) that can persist indefinitely since re-tombstoning requires another pressure-triggered pass; violates the monotonic state projection implied by TRD:2368 and the ladder's own ordering (test `retention_test.go:38` pins forward-only transitions single-threaded, so the guard gap is untested).

**Counter-evidence searched.** Stage-2 re-includes `'compacted'`, so data loss does not occur and the mislabel eventually self-corrects *if* another Compact runs — mitigates but does not remove the incorrect durable projection or the Accept abort.

**Severity / confidence:** Medium-Low / High.

**Proving regression test:** drive a run to tombstone; from a second repository call `Compact` with a pre-staged stale candidate (or call the internal op directly); assert `record_state` remains `tombstone` (today it flips) and that a vanished candidate yields skip-not-abort. Fix: add `AND record_state IN ('full','compacted')` (resp. expected source state) to the CAS and treat `changed==0` as benign skip.

---

## F4 — LOW: Reconciliation decisions are reported without their durable evidence rows

`_ = r.recordReconciliation(...)` discards insert failures (lifecycle.go:352, :411) while `report.Decisions` still lists the decision. Under multi-process load — exactly when reconciliation runs — a transient `SQLITE_BUSY` on the insert produces a taken action with no `reconciliation_log` row, diverging support-export evidence (`ReconciliationEvidence`) from the decisions actually made, against TRD:2208 ("records evidence") and the diagnostics requirement of "last decision evidence" (TRD:2296). Counter-evidence: FK failures are unlikely (attempt rows persist until expiry), so exposure is transient-busy-class; report counters stay consistent. Severity Low / confidence High on code, Medium on realized impact. Proving test: inject busy on `reconciliation_log` insert, assert decision is retried or surfaced rather than silently evidenced-away.

---

## Defended non-issues (hypotheses raised, then disproven — kept for reviewer cross-check)

- **Migration checkpoint/backup window vs concurrent openers** (pack unknown #4): safe — every writer routes through `migrateSchema`; ordinary openers of a v0 DB block on the lock (Busy, fail-closed), so no writer can mutate inside the checkpoint→copy window.
- **Lock-file identity parsing**: partial/empty content (`{}` included — `validateProbePID` rejects pid≤0 → uncertainty) fails closed to Busy; forged dead-PID deletion is capability-equivalent to direct file deletion within the POSIX-local trust model; reclaim requires exact probe contradiction (migration.go:140-165; test-pinned :94).
- **Empty/partial attempt identities**: land on `cleanup_uncertain`, never `interrupted` — `reconcileProcessDecision` checks completeness before outcome mapping (lifecycle.go:481-495) and `NewProcessOwner` degrades PID-only (process.go:35-41) which probes as unavailable-or-incomplete.
- **Ctx cancellation poisoning reconcile outcomes**: a canceled probe proposes `cleanup_uncertain`, but the subsequent `ProposeTerminal` on the same dead context cannot commit — pass aborts cleanly.
- **`enforcePrivateMode` symlink-chasing**: all chmod targets are Lstat-guarded non-symlinks beforehand (sqlite.go:154-191, local_log.go:43-57, migration backup O_EXCL).
- **Health backlog SQL argument ordering** and production `julianday` predicates: verified correct; injected-clock RFC3339Nano string-comparison skew is sub-second and test-only.
- **Restore of wrong-schema backup**: fails closed at next open (unsupported/corrupt typed errors).
- **Prune lexicographic order under backward clock steps** (pack unknown #3): real ordering flaw, but requires >3 existing backups *and* a step-back during a legacy-migration event — below reporting threshold; noted as accepted risk.
- **`ReadLocalLogs` sliding window, sanitizer omission accounting, per-run trim bounds (≤32×256)**: verified correct/bounded per contracts; `fault_test.go` pins no-stale-success under SQLITE_FULL/query_only.