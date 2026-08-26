Context pack read. Now inspecting the tests to establish what's actually pinned before hunting defects.Baseline suite green (5.3s). Fixing the WAL-replay reproduction to emulate a crash leftover properly:Baseline green (`go build`, `go vet`, full `internal/runcontrol` suite pass). Review complete. Findings below, strongest first; each survived counter-evidence search.

---

## F1. `RestoreBackup` ignores leftover `-wal`/`-shm`; a stale WAL silently chimerizes the restored database — and `integrity_check` reports `ok`

**Claim.** `RestoreBackup` (migration.go:347) renames the temp copy over `run-control.db` only. Pre-existing `run-control.db-wal`/`-shm` sidecars are neither removed nor checked anywhere in the function. SQLite binds WAL frames to page numbers, not to a database generation, so on next open stale frames replay over the restored image.

**Observable bad outcome.** Restored state is neither the backup nor the crashed state: committed-but-not-checkpointed pre-crash transactions resurrect on top of the restored snapshot. Demonstrated empirically (python/sqlite, engine-level semantics): restore backup image + stale `-wal` → both the restored row *and* the discarded post-backup row visible; `PRAGMA integrity_check` → `ok`. Physical validation (`validateBackupIntegrity`, migration.go:354-362) validates only the backup, never the target environment.

**Trigger.** Exactly the primary restore scenario: crash/kill-9/power-loss leaves a non-empty `-wal` (clean shutdown would have removed it), operator follows recovery.md:236-243 ("stop every process… use the tested restore path"). Stopping processes does not delete a sidecar left by a crash.

**Evidence/path.** `os.Rename(tempPath, databasePath)` migration.go:347; no sidecar handling in RestoreBackup (296-352), `preparePrivateDatabase` (sqlite.go:154-191), or OpenSQLite DSN setup (sqlite.go:73-80). No test restores with sidecars present (confirmed; pack §10 agrees).

**Counter-evidence searched.** Runbook prose "Never copy only the database while a WAL writer is active" (recovery.md:241) addresses *active* writers, not crash remnants; no code or doc instructs sidecar removal; modernc driver adds no cleanup.

**Severity:** high (corrupts the one operation whose purpose is corruption recovery; silent, passes its own verification). **Confidence:** high (mechanism reproduced).
**Regression test:** create DB, checkpoint, copy aside as backup; reopen, commit new data, SIGKILL helper leaving `-wal`; call `RestoreBackup`; assert reopened store equals backup contents exactly (today it will not).

## F2. Product-created migration backups count toward quota and are permanently unreclaimable — self-inflicted hard-quota outage

**Claim.** `storageBytes` sums every regular file prefixed `run-control.db` (retention.go:41-53), which includes `.bak.*` backups. `createMigrationBackup` keeps up to three ≤512 MiB copies (migration.go:22,196; prune-to-3 at 268-291). `pruneMigrationBackups` runs only inside the version<1 lock path; the steady-state fast path returns early (migration.go:36-41), so after the one-time migration the backups persist forever and nothing ever deletes them (repo-wide grep: only create/prune/restore touch `.bak.`).

**Observable bad outcome.** Legacy/foreign DB of size S migrates → durable footprint ≈ 2S–4S counted against default hard quota 512 MiB (model.go:425). For S ≥ ~248 MiB soft breach blocks all accepts; for S ≥ ~256 MiB hard breach additionally refuses every heartbeat (lifecycle.go:24-28), and each owner control loop kills its live operation within ≤5 s (run_control.go:280-285; durable_operations.go:248-252). Every subsequent `Accept` also burns a doomed `Compact(ctx,64)` pass (sqlite.go:384-395) that can never reclaim file-level bytes. Diagnostics say only "hard quota reached; active owners must stop" (sqlite.go:908) with no attribution to backups.

**Trigger.** First open over any pre-existing `user_version=0` SQLite with user objects (prototype leftovers, dev databases, stray foreign file — generic table names make these plausible).

**Counter-evidence searched.** recovery.md:230-231 "free space outside the active database" generically sanctions moving files out, but nothing connects the symptom to product-created backups, and Health/StorageBytes give no breakdown. Compaction/checkpoint/vacuum cannot shrink the backups.

**Severity:** medium-high (total durability outage incl. destruction of healthy active work), moderate prevalence. **Confidence:** high (plain arithmetic; all sites cited).
**Regression test:** seed a 300 MiB legacy-schema DB, migrate via `OpenSQLite`, assert `Health().Status != failed` and `Accept` succeeds (today both fail).

### F2a. Contract contradiction: the reserved 16 MiB is documented as protecting heartbeats; the code kills heartbeats at exactly that threshold

recovery.md:231-233: "reserves 16 MiB for heartbeat, cancellation, recovery, and terminal writes." Cancellation (lifecycle.go:70), terminal arbitration (sqlite.go:722) and reserved-type appends (sqlite.go:620,713-720) do bypass quota gates; `Heartbeat` does not — it refuses at `usage >= HardQuotaBytes` (lifecycle.go:26). The reserve actually protects physical headroom for reserved *event* writes; heartbeat renewal (~100 bytes, no event) is refused even though usable headroom exists. Low frequency (requires hard breach), but the doc promises the opposite of the behavior at the moment it matters. **Low-medium / high confidence.** Fix either the gate (heartbeat is not a growth vector) or the contract text.

## F3. Version-0 foreign schema is blindly merged via `CREATE TABLE IF NOT EXISTS`; colliding names yield a "successful" open and permanently opaque runtime failures

**Claim.** In the v0-under-lock branch, `createInitialSchema` (sqlite.go:344-366) executes `initialSchema`, whose tables are all `CREATE TABLE IF NOT EXISTS` (sqlite.go:230-341). A pre-existing foreign DB with same-named tables (`runs`, `events`, `attempts`, `app_schema` are generic names) but different columns is left untouched, `app_schema`/`user_version` are stamped 1, `verifySchemaRecord`+`integrityCheck` pass (both version/physical only), and `OpenSQLite` succeeds.

**Observable bad outcome.** First `Accept` fails with driver-level SQL errors (e.g., wrong column count) classified `CodeUnavailable retryable` (classifyStoreError default, sqlite.go:1108-1110) — operators retry a permanently wedged workspace; error truth is worse than the clean `ErrUnsupportedSchema` rejection given to version>1 databases (migration.go:30-31). The trust boundary "corrupt/hostile pre-existing DB must not be silently replaced" holds for replacement but is violated in spirit: hostile input is silently co-opted instead of rejected.

**Evidence/path.** migration.go:58-74 → sqlite.go:353 (`tx.ExecContext(initialSchema)`) → verifySchemaRecord (236-248, checks only the version row it just wrote) → integrityCheck (250-266, physical only). No column-compatibility probe exists anywhere post-create. Backup (F2 path) preserves recoverability.

**Counter-evidence searched.** `hasApplicationSchema` triggers backup for any user object (so the merge is deliberate), but no code or doc defines compatibility expectations; corrupt non-SQLite bytes *are* cleanly rejected (test-pinned, migration_test.go:154) — only parseable colliding schemas fall through the gap.

**Severity:** medium-low (prevalence low, persistence high, error truth poor). **Confidence:** high on mechanism.
**Regression test:** create v0 DB with `CREATE TABLE runs (id INTEGER)`, open, assert typed rejection (or a schema-compatibility probe) instead of successful open; today open succeeds.

## F4. Reconciliation-evidence writes are silently discarded, and the report is discarded too — decisions can leave no durable trace

**Claim.** `_ = r.recordReconciliation(...)` at lifecycle.go:352 and :411 drops insert errors. All three production callers discard the returned `ReconcileReport` (app/run_control.go:66, :290; durable_operations.go:256), so the `reconciliation_log` row is the *only* durable record of a decision (TRD.md:2296 requires diagnostics to expose last decision evidence).

**Observable bad outcome.** Under multi-process contention the out-of-tx insert (lifecycle.go:521) can hit `SQLITE_BUSY` beyond the 5 s timeout; the run is still terminalized/stalled, but no evidence row, no log line, no counter records the loss. `run diagnostics --support-export` later shows a gap where a decision occurred; phase-B rows also record proposals that lost their fence race as if decided (:400-412 appends `report.Decisions` regardless of `won`). Failure of the evidence sink is indistinguishable from "no decision made".

**Counter-evidence searched.** Failing the whole reconcile pass on evidence-insert error would arguably be worse (blocking durability work for a diagnostics row); but zero signaling — not even a debug log or metric — is not a designed tradeoff anywhere in docs/TRD.

**Severity:** low-medium (observability of exactly the states recovery.md tells operators to rely on). **Confidence:** high (code facts undisputed).
**Regression test:** inject busy/readonly failure on the evidence insert; assert a log/metric surfaces it (today nothing does).

## F5. Empty/garbage `.migrate.lock` wedges the workspace with false error truth; remediation undocumented

**Claim.** `removeStaleMigrationLock` fails closed on unparseable content (migration.go:146-149) → `acquireMigrationLock` returns `CodeBusy` "another local UltraPlan process owns the schema migration lock" (migration.go:104-106). A crash between `O_EXCL` create (:94) and identity write/sync leaves a 0-length/torn file that no process owns; every subsequent open fails Busy with a message asserting a live owner that doesn't exist. Nothing in `docs/` mentions the lock file (grep: zero hits).

**Counter-evidence.** Fail-closed matches TRD.md:2378 ("stale lock detection should be conservative"); the defect is error truth and operability, not the conservativeness: identical message/code for "alive owner" vs "unverifiable junk", no distinguishing diagnostic, no documented manual-recovery step.

**Severity:** low (rare window; total wedge until manual `rm`). **Confidence:** high.
**Regression/verification:** distinct message or log field when lock content is unparseable; docs entry naming the file and safe removal procedure.

---

## Defended non-issues (checked, not reported as defects)

- **Compact reachable only from Accept's 80% branch** (sqlite.go:387 — sole non-test caller, grep-verified): time-based aging therefore never runs unattended, but recovery.md:231 documents compaction as pressure-triggered and bounded storage is still achieved; retention windows act only as relative cutoffs under pressure. Documented design, not drift.
- **Lexicographic RFC3339Nano comparisons** (`finished_at <= ?`, cursor ordering): misorder requires a whole-second timestamp adjacent to a fractional one; wall-clock nanos make production collisions measure-zero, injected-clock tests use aligned formats, and grace predicates use `julianday()` parsing instead. Latent hazard only.
- **Ctx cancellation during reconcile probes** cannot win a wrongful `cleanup_uncertain` terminal: the follow-up `ProposeTerminal` transaction fails on the cancelled ctx before its CAS commits (lifecycle.go:386-403 with sqlite.go:738-746). Transient OS probe errors mapping to uncertainty is TRD-sanctioned conservatism.
- **Per-run 4096-event bound wedging chatty journals** (retention.go:85-87, non-retryable CodeQuota): deliberate bounded-history contract; terminal arbitration bypasses Append and always remains available.
- **`ReadLocalLogs` sliding window** traced correct for all limits incl. 1; `copy` is overlap-safe.
- **Sanitize byte-truncation splitting UTF-8** yields U+FFFD substitution at marshal — lossy but safe, omission accounting question is cosmetic.
- **Concurrent `Compact` stage-3 delete racing another process's candidate update** can spuriously fail an Accept with retryable CodeConflict — window is milliseconds inside a 37-day-old cohort; noted, below reporting threshold.
- **Backups counting toward Health.StorageBytes display** (distinct from the F2 gating harm), `markStalled` `updated_at` churn affecting list ordering, support-export silent truncation (health counters co-reported), `--json` envelope status divergence, and 1 MiB local-log silent drop-past-cap (documented tradeoff, recovery.md:219-223) — cosmetic/adjacent-surface nits excluded per doctrine.