Context pack read. Now inspecting actual sources independently.Sources read. Now verifying key mechanics: runcontrol's handling of the same file, locking around savers, docs contracts, and helper semantics.The mutation lease exists but I need to see if migrate takes it. Checking lease mechanics, docs contracts, and helpers.RestoreBackup confirmed. Now empirically probing the driver's DB-file creation mode (the permission asymmetry claim) with the same DSN the store uses.Permission probe done. Now checking study lock semantics (cross-process?) and discovery layout.To make the stickiness concrete, I'll build the binary and demonstrate authority flip + unrecoverable staleness on a real workspace. First, the state shapes:Building the frozen binary for a live demonstration:The status command rewrote the DB row — a lead worth chasing. Inspecting what status actually does:DeriveStages confirmed artifact-derived. Now empirically testing the dry-run mutation hypothesis on a DB lacking product tables:No probes ran without records. Retrying with a state file present so `FlowStateInDatabase` executes:Dry-run mutation confirmed empirically. Now proving the `Load` split-read tear with a standalone harness (`productstate` has no internal deps):Torn reads confirmed (4/36). Now demonstrating the availability-coupling failure (corrupt DB takes down file-backed records):Review complete. All findings verified against frozen target `5d54525` with live binary demonstrations where possible.

# Findings — `product-state-mirror` (failure/concurrency lens)

## F1 — `storage migrate` imports are unfenced against every other writer class (HIGH severity, HIGH confidence)

**Claim:** `runStorage`/`migrateSprintState` read and write DB-authoritative product rows without acquiring any of the cross-process mutation locks that all other writers honor, and the skip-on-row-exists retry semantics make a lost race permanently sticky.

**Observable bad outcome:** A terminal run state can be permanently overwritten by a stale mid-flight snapshot with no detection, no re-import path, and no tool to repair short of manual SQLite surgery. For execute states, startup recovery (`ReconcileInterruptedMutation`, internal/sprint/locks.go:46-63) then durably records spurious `recovery-interrupted` failures for tasks that actually completed.

**Trigger:** Running `storage migrate` while any study run or sprint command is active. Neither `docs/migration-product-state.md` nor the help text requires quiescing; the doc explicitly advertises incremental migration.

**Evidence & path:**
- Migrate probes → loads → commits with no lock: internal/app/storage_commands.go:93-106 (study), :147-158 (flow), :166-181 (execute). No `acquireMutationContext` / `AcquireRunLoopLock` anywhere in the file.
- Every other writer is fenced: sprint mutation lease = in-process map + cross-process file lock (internal/sprint/service.go:89-110, used by flow.go:117,201; execute.go:37,131; review.go:411; smoke.go:25; verify.go:41; qa.go:179,315); study run loop lock = O_EXCL pid-checked lock file (internal/study/locks.go:34+).
- Race interleaving: migrate's probe at :93/:147/:170 returns false → engine's final save also sees no row and writes **file-only** (study/state.go:60-70, sprint/state.go:219-230) → migrate commits its earlier-loaded snapshot (:104/:155/:178). From then on row-wins loads serve the stale snapshot forever.
- Stickiness demonstrated live on the frozen binary: imported mid-flight flow row; advanced the file to all-terminal (the exact end-state of the interleaving); re-running `storage migrate` reports `"skipped"` — no refresh exists. Repo-wide grep: only `storage_commands.go` touches `Migrate*ToDatabase`; no demote/re-import path anywhere.

**Counter-evidence searched:** SQLite serialization only covers row-granular physics, not the file-vs-DB logical race; engine self-heals only if another save happens, which terminal completion precludes. Validators can't catch it (both snapshots individually valid).

**Regression test:** Hold `acquireVerificationFileLock` for a sprint (or create a `run-loop.lock`), then run `storage migrate` on a workspace with a valid unimported state file; assert skip-or-explicit-conflict rather than import. Fails today.

## F2 — `Store.Load` spans two autocommit statements: torn header/item reads (MEDIUM severity, mechanism certain — reproduced)

**Claim:** Header and items are read in separate snapshots (internal/productstate/store.go:129 then :135, no transaction); a concurrent committed Save between them yields mixed-epoch records.

**Observable bad outcome:** Readers (status polls, gates — never lease-held) receive state where header evidence (Review/Smoke/QA pointers, Complete flag) belongs to one epoch and stage/task items to another. Validators don't cross-check header↔items epochs, so the tear passes validation and feeds decisions. Two *unleased* writer pairs exist (status synthesis at service.go:292 and migrate itself), so a torn load can also feed a Save and persist corruption via the evidence backfill (sprint/state.go:204-218).

**Reproduction:** Standalone harness over the copied store (1 writer alternating epoch-tagged headers + item sets, 4 readers): `reads=32 torn=4` in 20s — both mixed header/item epochs and wrong item-set membership observed.

**Controls searched:** No transaction, no hash recheck on read (hashes are upsert-guard-only), no retry, no test coverage. Lease exclusion protects sprint-writer-vs-sprint-writer only; readers are excluded from nothing.

**Regression test:** Store-level concurrency test asserting header epoch == every item epoch and item-set membership matches the header epoch under concurrent Save/Load; fails within seconds.

## F3 — `--dry-run` mutates an existing database (LOW-MEDIUM severity, HIGH confidence — reproduced)

**Claim:** Dry-run deliberately skips `OpenSQLite`/`Ensure` (storage_commands.go:59-68), but the per-record existence probes go through `Existing`→`open`, which unconditionally executes `createSchema` DDL (store.go:79).

**Reproduced:** Workspace with a run-control-style DB containing only `runs`: before `['runs']` → `storage migrate --dry-run --json` reports `would_import` → after `['runs','product_states','product_state_items','product_state_items_order']`. The "preview" added schema objects to a durability-critical shared file (plus WAL-mode pragma application on open).

**Contract:** doc presents `--dry-run` as "Preview the migration"; the command's own structure shows intent that dry-run not touch the DB. No functional authority flip occurs (rows, not tables, flip authority), which bounds severity.

**Regression test:** Create DB without product tables, run dry-run, assert `sqlite_master` unchanged. Fails today.

## F4 — Read-path commands persist artifact-derived state into the sole authority (MEDIUM-LOW severity; mechanism demonstrated live, reachability moderate)

**Claim:** Post-flip, `Service.Status` — nominally a read command — synthesizes stages from artifact presence via `DeriveStages` (service.go:263, 1484-1546) and saves them through `SaveFlowState` into the DB-authoritative record whenever `statusWrites` is on (service.go:291-295).

**Demonstrated:** On the frozen binary, after importing a mid-flight flow row, one `sprint demo s1 status` invocation rewrote the authoritative row from `[complete, ready, missing…]` to `[ready, missing×6]` (artifact files absent in fixture). Pre-migration this behavior wrote a file that stayed continuously fresh; post-migration the file checkpoint is intentionally stale mid-flight (checkpoint gating, sprint/state.go:225-240), so derivation glitches now poison the only consulted authority with no detection and no fallback — the legacy safety net was disabled by this surface's design. Divergence triggers include artifact cleanup/restores/partial failures; each subsequent status recomputes the same wrong result, so it never heals.

**Honest framing:** the deriving writer is inherited from `sprint-flow-state`; the defect attributable here is that the mirror's authority flip amplified its blast radius without adding any guard (e.g., suppressing derived writes for DB-authoritative records or gating `statusWrites` on record provenance).

**Regression test:** Import a mid-flight record whose stored statuses outrun artifact presence; run a status-writing command; assert stored statuses unchanged. Fails today.

## F5 — Shared-DB health now gates all product surfaces, including never-migrated file-backed records, with opaque errors (MEDIUM severity, HIGH confidence — reproduced)

**Claim:** Every load/save first calls `Existing`; any open error (corruption, non-DB file, permission) hard-fails the whole product surface even when the requested record has zero DB rows and its truth lives entirely in the intact JSON file.

**Reproduced:** Corrupt `.ultraplan/run-control.db` (no product tables at all) in a workspace with a valid file-backed `flow-state.json`: `ultraplan sprint demo s1 status` → `sprint.status: file is not a database (26)`. Pre-feature, this command never touched that file. The error names neither the file nor the component; recovery implies deleting the DB, which also destroys journal history and any migrated rows.

**Counter-evidence considered:** fail-closed is defensible for row-authoritative records (avoids split-brain), but extending that failure domain to purely file-backed records is a new availability coupling introduced by this surface, and the error context loss is an operability defect regardless.

**Regression test:** Valid un-migrated sprint + garbage DB file; expect either file-backed service with a contextual warning or an error naming `.ultraplan/run-control.db`. Today: raw driver error, hard failure.

## Minor / hardening notes

- **Permission/symlink enforcement asymmetry (hardening):** productstate's `open` does `MkdirAll(0700)` only-at-creation, no chmod/symlink checks, vs runcontrol's enforced 0700/0600 on the same path (sqlite.go:154-194). Empirically, driver-created DB = `0644` inside a pre-existing `0755` dir. Not currently reachable through shipped paths (every creation site is preceded by `OpenSQLite` enforcement or creates nothing), so reported as latent inconsistency, not a vulnerability.
- **Import provenance overwrite:** adapters stamp `UpdatedAt=time.Now()` (state_database.go:44,70,100) so imported rows lose source timestamps; `updated_at` is write-only anyway — informational.
- **Silent skip of path-unresolvable sprints** (storage_commands.go:145,164 emit no item): defended non-issue — `resolveSprintContained` is purely lexical (workspace/discovery.go:69-77, no symlink resolution) and discovered names cannot escape, so resolution failure is effectively unreachable.
- **`context.Background()` throughout store calls:** acceptable for CLI (process death rolls back via SQLite); becomes a liveness question only when the planned server surface arrives.

Zero test coverage of any of these paths at freeze confirmed (repo-wide: no test references any mirror symbol, `Ensure`, `*InDatabase`, or `storage migrate`).