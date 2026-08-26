Context pack read. Now inspecting the primary sources directly.Core sources mapped. Now checking cross-cutting evidence: sidecar handling, Compact callers, config bounds, and the recovery runbook contract.Strong candidate confirmed on the restore path. Now checking config bounds, the periodic loops, and storage migrate composition.Code evidence is strong for three candidates. Now empirically verifying the stale-WAL-over-restored-image mechanism with the same SQLite driver in `/tmp/opencode`.Mechanism empirically confirmed with the target's exact SQLite driver. Let me do two final counter-evidence sweeps before reporting.# Review: `run-recovery-retention` (failure/concurrency lens)

Baseline `go test ./internal/runcontrol/` green at the frozen tree. Findings below survived active counter-evidence search (callers, guards, tests, docs); a defended non-issue list follows.

---

## F1 — RestoreBackup replays stale `-wal` onto the restored image: restore silently fails or hybridizes the database

**Claim.** `RestoreBackup` renames the backup over `run-control.db` but never removes pre-existing `run-control.db-wal` / `-shm` sidecars (migration.go:347-351). SQLite WAL recovery has no incarnation binding to the main file, so leftover frames from a crashed writer are recovered against the restored image on next open.

**Observable bad outcome (reproduced with modernc.org/sqlite v1.57.0, the target's driver).** Pre-migration image checkpointed and byte-copied ("backup"); schema-v1 objects + rows committed (frames in `-wal`); process "crashes" (no clean close); backup restored over main DB per the documented procedure; next open yields `user_version=1`, the post-migration row resurrected, and `PRAGMA integrity_check` = **"ok"**. The rollback silently didn't happen and the product's own integrity gate certifies the wrong database. Same-page-size stale WALs replay cleanly; mismatched ones corrupt or truncate — all three outcomes defeat the restore's purpose.

**Trigger.** Any unclean death of an UltraPlan process before restore (crash, OOM/SIGKILL, power loss) leaves sidecars behind. `docs/recovery.md:236-243` requires only "stop every UltraPlan process" and warns against copying "while a WAL writer is active" — it never mentions sidecar removal, so the documented runbook walks straight into this. The test that pins restore (`migration_test.go:119-152`) closes cleanly first, which deletes `-wal`; it cannot catch this.

**Path.** crash → sidecar persists → operator runs offline restore → `os.Rename(tempPath, databasePath)` touches only the main path → next `OpenSQLite` recovers stale WAL frames over the restored pages.

**Controls searched / counter-evidence.** No code path in `internal/runcontrol` deletes `-wal`/`-shm` (repo-wide grep: only an unrelated dev script does). `preparePrivateDatabase` and `verifyPragmas` don't detect main/sidecar divergence. Not doc-covered, not test-covered, not code-guarded.

**Severity:** high (the one recovery door for durability-core, silent failure mode). **Confidence:** high (mechanism reproduced end-to-end).

**Regression test.** In `TestBackupRestoreFixture` style: commit post-backup writes, abandon the handle without `Close()`, copy `db`+`-wal` bytes aside, write both back after `RestoreBackup`, reopen, assert `user_version==0`/run count matches the backup. Fix shape: remove `databasePath+"-wal"`/`"-shm"` after the rename while still offline.

## F2 — Lexicographic backup pruning deletes the just-created migration backup after a backward clock step

**Claim.** Backup names are wall-clock stamps (`migration.go:194`) and pruning sorts names ascending and removes from the front until 3 remain (`migration.go:282-289`), with no mtime tiebreak. After the clock steps backward (NTP correction, VM snapshot rollback, manual change), the fresh stamp sorts oldest and is pruned first.

**Observable bad outcome.** With 3 existing backups plus a backward-stepped clock, `migrateSchema` creates the safety copy of the current pre-migration state (`migration.go:67`), then — after `createInitialSchema` has already mutated the database — `pruneMigrationBackups` (`migration.go:81`) deletes exactly that fresh copy. The open succeeds; the workspace is left migrated with zero backups of its prior state, silently violating the "backup before first-time schema creation" invariant (pack §5).

**Trigger.** One clock step backward between the last backup and the current migration of a legacy (`user_version=0`) database. Forward jumps merely mis-select which 3 survive; backward steps destroy the new artifact.

**Evidence.** `createMigrationBackup` stamps with `clock.Now()`; `pruneMigrationBackups` runs unconditionally at the end of every version<1 open; `sort.Strings(backups)` is the sole ordering.

**Counter-evidence.** Tests use fixed/injected monotonic clocks (`sqlite_test.go:429`, `retention_test.go:40`), so ordering is always chronological there; no production guard exists.

**Severity:** medium-high (voids the migration safety artifact exactly when it matters; durability-core). **Confidence:** high (pure code logic), likelihood low-medium.

**Verification.** Seed 3 backups stamped T+1h/T+2h/T+3h, run `migrateSchema` with injected clock set to T−1y, assert the just-created backup still exists (fails today). Fix: prune by `ModTime`, or exempt the backup created in the current call.

## F3 — Stale-lock reclaim unlinks by path from previously-read contents: concurrent migrator's live lock can be deleted

**Claim.** `removeStaleMigrationLock` reads contents, proves the recorded owner dead, then calls `os.Remove(path)` (`migration.go:150-163`) without re-verifying that the file at that path is still the one it inspected. Between process B's read and B's unlink, process A (executing the same reclaim path against the same stale lock) can acquire and create its own lock; B deletes A's live lock by name.

**Observable bad outcome.** Two processes both believe they hold the schema-migration lock and concurrently execute `checkpointWAL` → `createMigrationBackup` → `createInitialSchema`. A's deferred release (`migration.go:137`) then unlinks B's lock mid-flight, extending exclusion loss to a third opener. Concrete consequences: duplicate/redundant backups; a torn main-file copy if one process checkpoints `TRUNCATE` while the other's `copyPrivateFile` is mid-read; transient `CodeBusy`/`CodeUnavailable` aborts of legitimate opens (e.g., racing prunes hitting ENOENT). Durable corruption is *not* demonstrated — schema creation is transactional and idempotent — but the "lock existence means exclusive schema-write rights" invariant breaks silently, and `acquireMigrationLock` never revalidates (no fstat-after-open, no content check) so A cannot even notice.

**Trigger.** ≥2 processes opening a `user_version=0` database while a proven-stale lock exists — e.g., first boot after a crash/reboot where several ultraplan invocations start together. Window spans B's `/proc` probe (~ms) overlapping A's remove-and-reacquire.

**Severity:** medium-low. **Confidence:** high on mechanism; impact bounded by idempotent DDL and O_EXCL backups.

**Verification.** Deterministic repro via injected probe delay in `removeStaleMigrationLock`; fix shape: re-stat/re-read immediately before unlink, or rename-then-inspect (`rename` the lock to a unique temp name under the original EEXIST proof, validate, delete).

## F4 — Reconcile aborts the whole pass on any candidate error, and callers cancel live owned work in response

**Claim.** Phase-B candidate processing returns hard on any non-fence error (lifecycle.go:391-395 markStalled, :400-403 ProposeTerminal — including retryable `CodeBusy`/`CodeQuota`). Both periodic callers treat any Reconcile error as fatal to *their own* healthy operation: `controlledRuntime` sets `persistenceErr` and cancels the running workload (run_control.go:290-296, later proposed as `persistence_degraded`, :322-331); `durableOperationManager.controlOperation` calls `owned.cancel()` directly (durable_operations.go:256-258).

**Observable bad outcome.** A transient store hiccup while reconciling *another* stale run — busy beyond the 5 s timeout during a checkpoint storm, or SQLITE_FULL — aborts reconciliation, and an unrelated, fully healthy owner cancels its live operation and marks it degraded/failed. One noisy tombstone cleanup takes down active work.

**Trigger.** Write-lock contention exceeding `_busy_timeout=5000` or disk pressure during any reconcile pass on a workspace with ≥2 concurrent operations.

**Counter-evidence searched.** Fence errors are correctly filtered (`errors.Is ErrTerminal/ErrStaleFence → continue`), so the common races don't trigger this; startup reconcile failing hard (app/run_control.go:66-70) is defensible fail-closed. The cross-operation blast radius via the two periodic loops remains unjustified.

**Severity:** medium (low frequency, high impact per event: live work killed). **Confidence:** medium-high on mechanism; realistic-trigger assessment medium.

**Verification.** Unit test: seed one expired-lease candidate whose `markStalled` hits a forced busy error plus one healthy owned run; drive `controlOperation`'s ticker; assert the healthy run survives a failed tenant-wide reconcile. Fix shape: skip-and-log per-candidate transient errors instead of aborting, or make callers tolerate reconcile-pass failures.

## F5 — Quota accounting counts immutable migration backups; Compact cannot reclaim them, wedging acceptance

**Claim.** `storageBytes` sums every regular file prefixed `run-control.db` (retention.go:41-54), including `.bak.*` backups (≤512 MiB each, ≤3 kept, retained indefinitely — pruning only ever runs inside the version<1 migration). Soft/hard gates (sqlite.go:384-394, :618-622, lifecycle.go:24-27) and `Health.StorageBytes` consume this number; the only automatic responder, `Compact`, deletes events/runs and can never shrink a backup.

**Observable bad outcome.** Migrating a large legacy DB under a modest configured quota (config floor 64 MiB): a ~40 MiB backup alone pushes usage past soft (48 MiB) or hard (64 MiB). Every subsequent `Accept` runs the futile pressure→Compact→re-measure ladder then refuses new runs; `Health` reports degraded/failed with no breakdown distinguishing reclaimable journal bytes from immutable backup bytes. Remedy is undocumented-at-the-code-level manual deletion (recovery.md:230-231's "free space outside the active database" gestures at it), and the 16 MiB reserved headroom for recovery writes can be consumed by the very backups created to enable recovery.

**Severity:** low-medium (manual recovery exists; no data loss). **Confidence:** high on arithmetic, medium that it's defect-vs-intent (docs never state backups count toward quota).

**Verification.** Extend `TestSoftQuotaRejectsAcceptanceAndHealthReportsReservedHeadroom` with a `run-control.db.bak.<stamp>` fixture; assert Accept behavior and whether Health exposes any signal to act on. Fix shape: exclude `.bak.` from quota math, or count them separately in Health diagnostics.

## F6 — First-ever concurrent open loses the create race with a spurious retryable failure

**Claim.** `preparePrivateDatabase` handles only `ErrNotExist` before `O_EXCL` creation (sqlite.go:173-181); when two processes cold-open the same workspace, the loser gets `EEXIST` → `classifyStoreError("create_database", …)` → `CodeUnavailable` and the whole command fails, although the winner succeeded and proceeding was safe.

**Observable bad outcome.** Two simultaneous first commands in a fresh workspace (scripted fan-out, editor plugin + CLI): one exits with "create private run-control database failed … file exists" despite nothing being wrong. Contrast the migration lock path, which explicitly special-cases `fs.ErrExist`.

**Severity:** low. **Confidence:** high.

**Verification.** Barrier-goroutine test on a temp root; fix: treat `EEXIST` on create as "now exists," fall through to Lstat validation.

## F7 — One oversized line in the local log disables support export entirely

**Claim.** `ReadLocalLogs` caps lines at 4 KiB via scanner buffer (local_log.go:138); a longer line produces `bufio.ErrTooLong` → `CodeCorrupt` (local_log.go:151-152). `writeRunSupportExport` propagates it (run_commands.go:329-332), so `run diagnostics --support-export` fails wholesale.

**Observable bad outcome.** The primary documented diagnostic door (recovery.md:219-223 pushes operators to support-export) becomes unusable because of a single >4 KiB line — writable by anything else touching the file (hand edit, other tooling); the writer itself never emits one (`Log` drops >4 KiB records, local_log.go:86), so app-originated files are fine and the failure surfaces only when an operator most needs evidence.

**Severity:** low (diagnostics denial; local-trust file). **Confidence:** high mechanism, low likelihood.

**Verification.** Append a 5 KiB line to the fixture log; assert export still succeeds skipping the bad line.

## F8 — Unconditional `PRAGMA integrity_check` on every repository open scales O(database size)

**Claim.** Every version-current open runs full `integrity_check` (migration.go:36-41, :250-266) through the pure-Go driver; every CLI invocation opens fresh (five run doors, `storage migrate`), so command latency grows linearly with journal size up to quota scale (default ceiling 512 MiB).

**Bad outcome:** multi-second `ultraplan run list` on mature workspaces; no budget, sampling, or benchmark exists (benchmark_test.go covers append/replay only). Reported honestly as unbudgeted-scaling risk rather than measured defect.

**Severity:** low-medium. **Confidence:** high that cost is incurred and unbounded; medium that it crosses materiality thresholds in practice.

---

## Defended non-issues

- **Forged `.migrate.lock` identity** (attacker writes a dead PID): no trust transition — a POSIX-local writer could delete the file directly; design already fails closed on uncertainty (migration.go:146-157).
- **Probe ctx-cancel producing a spurious `cleanup_uncertain` terminal**: impossible to commit — the cancelled context also fails `ProposeTerminal`'s `BeginTx`, aborting the pass (lifecycle.go:386-403).
- **Phase-A batch starving phase-B leases**: needs sustained >64 unclaimed accepts per 10 s tick; pathological, self-drains oldest-first.
- **RFC3339Nano trailing-zero text comparison** in retention cutoffs/pagination: production grace predicates use numeric `julianday`; remaining TEXT comparisons require exact whole-second timestamps (probability ~0 for wall clocks) to misorder.
- **`recordReconciliation` discarded errors** (lifecycle.go:352, :411): decisions are already committed; realistic insert failures (busy/full) coincide with pass-abort conditions elsewhere, and FK violations are unreachable (NULL or existing attempt).
- **`Compact` reachable only via Accept pressure**: retention is lazy but storage stays bounded by the quota gates themselves; quiet workspaces don't lose correctness.
- **Sanitize oversize collapse changing draft.Type**: internal producers never send lifecycle-transition drafts; warning stub preserves omission accounting.
- **`DELETE … LIMIT`**: both uses place LIMIT inside `IN (SELECT … LIMIT ?)` subqueries — portable SQLite, not the compile-time-gated form.