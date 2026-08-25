# Context Pack: `product-state-mirror` — Product-state SQLite mirror and storage migrate

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: durability-core. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

`internal/productstate` is a small generic KV mirror: two tables in the workspace-local SQLite file `.ultraplan/run-control.db` holding one row per `(kind, scope)` with a header JSON blob plus ordered per-item payload rows. Three product record types use it:

| kind | scope | header | items | source JSON checkpoint |
|---|---|---|---|---|
| `study_run` | `<study name>` | RunState minus Tasks | one per task ID | `studies/<name>/.ultraplan/run-state.json` |
| `sprint_flow` | `<project>/<slug>` | FlowState minus Stages | one per stage name | `projects/<p>/sprints/<s>/flow-state.json` |
| `sprint_execute` | `<project>/<slug>` | ExecuteRunState minus Tasks | one per task ID | `projects/<p>/sprints/<s>/.run-state.json` |

Authority model (as implemented): **if a row exists for a record, that row is authoritative on every load** ("row-wins"); the JSON file remains in place as a compatibility checkpoint, rewritten by save paths only at terminal/complete states. The `storage migrate` CLI imports existing files into the DB one-way and idempotently (per-record skip when a row already exists). Records never imported stay file-backed; there is no export/demote path. The DB file is shared with `internal/runcontrol` (run-journal-fencing surface), which owns its own tables in the same file.

## 2. Entrypoints and control flow

### 2.1 Store primitives (`internal/productstate/store.go`, whole package = 206 lines)
- Constants/state: `DatabaseRelativePath = ".ultraplan/run-control.db"` (:19), `ErrNotFound` (:21), `Item{Key,Ordinal,Payload}` (:23), `Record{Kind,Scope,SchemaVersion,Header,Items}` (:29), process-wide cache `stores sync.Map` keyed by absolute root (:39).
- `Existing(root)` (:41): stats the DB path; not-exist ⇒ `(nil, false, nil)`; other stat error ⇒ error; else `open(root)` returning `(store, err == nil, err)`.
- `Ensure(root)` (:53): unconditionally `open(root)` (creates `.ultraplan/` 0700 and the DB file via the driver if absent). Sole callers: the three domain save-database helpers and `runStorage`.
- `open(root)` (:55): abs-path cache lookup; DSN params `_busy_timeout=5000`, `_foreign_keys=on`, `_journal_mode=WAL`, `_synchronous=FULL`, `_txlock=immediate`; modernc.org/sqlite v1.57.0 driver; `SetMaxOpenConns(4)`; runs `createSchema` (`CREATE TABLE IF NOT EXISTS`, :92-116) on every open — including opens reached through `Existing`; `LoadOrStore` race loser closes its duplicate handle. No `Close` method exists anywhere in the package.
- Schema: `product_states(kind, scope, schema_version, header_json, header_hash, updated_at, PK(kind,scope))`; `product_state_items(kind, scope, item_key, ordinal, payload_json, payload_hash, PK(kind,scope,item_key), FK→product_states ON DELETE CASCADE)`; index `(kind,scope,ordinal)`; `updated_at` is RFC3339Nano UTC.
- `Has` (:118): `SELECT 1 … WHERE kind,scope`.
- `Load` (:127): two separate autocommit statements — header row first (`sql.ErrNoRows` ⇒ `ErrNotFound`), then items `ORDER BY ordinal`. No transaction wraps the pair; stored `header_hash`/`payload_hash` are not compared on read.
- `Save` (:150): rejects empty Kind/Scope, `SchemaVersion < 1`, or empty Header before opening an immediate transaction (deferred rollback); sha256 of header; upsert with change-guard `WHERE header_hash <> excluded OR schema_version <> excluded` (no-op when identical); per-item upsert guarded on payload hash + ordinal; empty item key aborts mid-transaction; then collects existing item keys not present in the incoming set and DELETEs them inside the same tx (full-replace semantics for items); commit.

### 2.2 Sprint adapters (`internal/sprint/state_database.go`)
- Kinds `sprintFlowStateKind="sprint_flow"`, `sprintExecuteStateKind="sprint_execute"` (:13-14); scope `sprintStateScope(s) = s.Project + "/" + s.Slug` (:17).
- `loadSprintRecord` (:57): `Existing` (err or disabled ⇒ no record), `Load`, `ErrNotFound` ⇒ not-found. `loadFlowStateDatabase` (:19)/`loadExecuteStateDatabase` (:38): plain `json.Unmarshal` header into `FlowState`/`ExecuteRunState`, then each item payload into `StageState`/`ExecuteTaskRecord`, appended to `state.Stages`/`state.Tasks` in ordinal order. No `DisallowUnknownFields`, no single-value check, no v1 migration, no pre-code-context interpretation on this branch.
- `saveFlowStateDatabase` (:69)/`saveExecuteStateDatabase` (:99): stamps `UpdatedAt=time.Now().UTC()`; resolves the canonical file path (containment-checked) and runs the full domain validator (`ValidateFlowState`/`ValidateExecuteRunState`) BEFORE any DB write; marshals header copy with Stages/Tasks nil'd; items keyed by stage name / task ID with slice index as ordinal; `Ensure`; `Store.Save(context.Background(), …)`.
- `SprintStateInDatabase` (:129)/`FlowStateInDatabase` (:137)/`ExecuteStateInDatabase` (:140): `Existing`+`Has`. `MigrateFlowStateToDatabase`/`MigrateExecuteStateToDatabase` (:143-147) are thin aliases over the save helpers.

### 2.3 Study adapter (`internal/study/state_database.go`)
- Kind `studyRunStateKind="study_run"`; scope is bare `study.Name` (:13). Root derivation `studyWorkspaceRoot(study) = filepath.Dir(filepath.Dir(study.Path))` (:15) — two levels above `studies/<name>/.ultraplan/run-state.json`, i.e. the workspace root.
- `loadRunStateDatabase` (:16): Existing/Load/plain Unmarshal header + TaskState items, same shape as sprint adapters. `saveRunStateDatabase` (:43): stamps UpdatedAt, `compactRunStateDiagnostics`, `ValidateRunState(state, RunStatePath(study))`, header = clone minus Tasks, items per task ID, Ensure, Save with `context.Background()`. `RunStateInDatabase` (:70); `MigrateRunStateToDatabase` (:78) aliases the saver.

### 2.4 Authority routing in the owning packages
- Study (`study/state.go`): `LoadRunState` (:27) tries the DB first; found ⇒ `ValidateRunState` then return (no diagnostics compaction on this branch). Miss ⇒ read file, plain `json.Unmarshal` (malformed ⇒ `ErrRunStateMalformed`), validate, compact diagnostics, oversized-content GC guard (:52-55). `SaveRunState` (:59): `RunStateInDatabase` true ⇒ `saveRunStateDatabase`; file rewrite follows ONLY when `state.Complete` (:66-68); otherwise return after DB write. No row yet ⇒ file-only atomic write (temp+fsync+rename, :73-110+).
- Sprint flow (`sprint/state.go`): `LoadFlowState` (:20) DB-first; found ⇒ containment-resolve path, `ValidateFlowState`, return — the file branch's v1→v2 migration and pre-code-context interpretation do NOT apply to DB records. Miss ⇒ strict file grammar (version ∈ {1,2}, DisallowUnknownFields, single JSON value; v1 migrated in memory only). `SaveFlowState`: evidence-preservation backfill of nil Review/Smoke/QA from prior state happens FIRST (:205-218), then routing (:219-230): row exists ⇒ DB save; file checkpoint written additionally only when `flowStateCheckpoint(state)` — every stage terminal (complete/failed/skipped) and non-empty (:233-240); no row ⇒ file-only.
- Sprint execute (`sprint/execute_state.go`): `LoadExecuteRunState` (:35) DB-first with `ValidateExecuteRunState` on the DB branch; file branch plain Unmarshal + validate. `SaveExecuteRunState` (:105): row exists ⇒ DB save; file checkpoint only when `executeStateCheckpoint(state)` — non-empty and all tasks terminal (:120-130); else file-only.
- Net effect: once a row exists, loads never consult the file again; saves update both authorities but keep the FILE stale during non-terminal transitions (checkpoint-gated).

### 2.5 `storage migrate` CLI (`internal/app/storage_commands.go`, dispatched from `app/app.go:159-160`)
- Arg grammar (:33-54): no args/`--help`/`-h` ⇒ help text; subcommand must be `migrate` (else ExitUsage=2); flags `--dry-run`, `--json`, `--help/-h`; anything else ⇒ ExitUsage.
- Unless dry-run (:59-68): `runcontrol.OpenSQLite(ctx, root.Path, SQLiteOptions{})` — creates `.ultraplan/` 0700, DB file 0600 O_EXCL with symlink/regular-file checks, verifies pragmas, may run runcontrol schema migration with timestamped backups; then `productstate.Ensure` adds the product tables. Errors ⇒ classified ExitWorkspace=4. Dry-run performs neither step (but see §7: existence probes still touch an already-present DB).
- Result accumulation (:69-80): `storageMigrationResult{SchemaVersion: 1, DryRun, Imported/Skipped/Failed counts, Items}`; statuses `imported`/`would_import`/`skipped`/`failed`.
- Study pass (:81-107): `DiscoverStudies` (name-sorted) ⇒ hard ExitWorkspace error on failure. Per study: stat `run-state.json` — not-exist ⇒ silent continue; other error ⇒ failed item. `study.RunStateInDatabase` — error ⇒ failed item; true ⇒ skipped. Else `study.LoadRunState(item)` (full validation; DB miss guaranteed since row absent); when load OK and not dry-run ⇒ `MigrateRunStateToDatabase`; append imported/would_import/failed accordingly.
- Sprint pass (:108-121): `DiscoverProjects` (name-sorted) ⇒ hard ExitWorkspace error; discovery failure per project ⇒ failed project-kind item, continue; `migrateSprintState` per sprint (slug-sorted):
  - flow (:144-162): `FlowStatePath` resolution failure ⇒ block silently skipped; stat not-exist ⇒ silent skip; other stat error ⇒ failed. `FlowStateInDatabase` err/true ⇒ failed/skipped. Else `LoadFlowState` (strict file grammar incl. in-memory v1 migration), import via `MigrateFlowStateToDatabase` unless dry-run.
  - execute (:163-185): same path/stat pattern; NEW: `LegacyTerminalExecuteStatus(root, sp)` legacy ⇒ skipped item and early return (added post-feature, commit 8ee9d9c "Skip legacy terminal sprint state during migration"); otherwise `ExecuteStateInDatabase` gate then `LoadExecuteRunState` + `MigrateExecuteStateToDatabase`.
- Output (:122-135): `--json` ⇒ single encoded result object; text mode prints aligned status/kind/scope/path (+error) lines and a summary line. Any failed item ⇒ classified ExitPartial=8 ("state artifact(s) failed validation or import") after full output (:136-138).
- Per-record commit granularity: each import is an independent `Store.Save` transaction; there is no overall migration transaction. Re-running skips already-imported records (idempotent). Files are never modified or deleted by the command.

## 3. Inputs / outputs

Inputs: workspace tree (the three JSON state files, project/study/sprint directory layout), DB file presence and rows, wall clock (`time.Now().UTC()` stamped at every DB save), CLI args (`--workspace` via `discoverWorkspace`, `--dry-run`, `--json`). Outputs: rows in `product_states`/`product_state_items` (created/updated/deleted-items within transactions), creation of `.ultraplan/run-control.db` (+WAL/SHM sidecar files) when absent via Ensure/OpenSQLite, terminal-checkpoint rewrites of source JSON files by save paths (owned by the flow/execute/study surfaces, not this pack), stdout report + exit class (0 / 2 usage / 4 workspace / 8 partial), sentinel `productstate.ErrNotFound`.

## 4. Authoritative state

- Tables `product_states` and `product_state_items` inside `.ultraplan/run-control.db`, coexisting with runcontrol's own tables (`app_schema`, `runs`, `events`, `attempts`, `operation_aliases`, `reconciliation_log`, …) in the SAME file. Two independent `database/sql` pools address it: productstate (cached per absolute root, ≤4 conns, never closed) and runcontrol (per OpenSQLite call, default 4/max 16). Both use WAL + synchronous FULL + immediate txlock + busy_timeout 5000ms.
- Column roles: `header_json`/`payload_json` carry state; `schema_version` mirrors the domain schema version (flow 2, execute 1, study 1) and participates in the header change-guard; `header_hash`/`payload_hash` (sha256) exist for the upsert WHERE guards only — no code path reads them back or verifies them against payloads; `updated_at` written (RFC3339Nano UTC, stamp time of the save/import, NOT inherited from the file's UpdatedAt) and never read.
- Item identity: stage enum name (flow), task ID (execute/study); ordinals are slice positions at save time; deleted stages/tasks are removed from the DB by Save's stale-key sweep.
- Authority decision inputs, exhaustively: DB file exists (stat) AND row exists for `(kind, scope)` ⇒ DB authoritative for that record. Everything else stays file-backed. There is no config flag, marker table, or version gate involved in the flip.
- The file remains a live artifact for migrated records only at terminal states (checkpoint writes); mid-flight transitions leave the file at its last terminal snapshot while the DB advances.

## 5. Invariants (as implemented)

- Domain validation precedes persistence: every DB write path runs the same validator used for files (`ValidateRunState`/`ValidateFlowState`/`ValidateExecuteRunState`) against the contained canonical path, so DB rows satisfy the same grammar as validated files at write time.
- Record sanity at the store boundary: non-empty kind/scope/header, `SchemaVersion ≥ 1`, non-empty item keys; violations reject the whole Save transaction.
- Atomicity: each Save is one immediate transaction covering header upsert, all item upserts, and stale-item deletions; readers see pre- or post-state per SQLite isolation.
- Change-guarded writes: byte-identical headers/payloads (same hash + version/ordinal) perform no UPDATE (hash-guarded upserts), so repeated saves do not churn `updated_at`.
- Full-replace item semantics: Save's final sweep deletes any stored item key absent from the incoming set, mirroring file-overwrite semantics.
- Reconstruction invariant: Load appends items strictly in `ordinal` order, so reassembled Stages/Tasks order matches the saving slice order.
- Row-wins authority: Load paths consult the DB first and short-circuit; Save paths route by `*InDatabase` probes. A record cannot be DB-authoritative for reads and file-authoritative for writes simultaneously.
- One-way migration: import validates with existing validators, never mutates or removes source files, and skips records already present; invalid files produce failed items plus partial exit without any mutation.
- Checkpoint gating: DB-authoritative saves rewrite files only under per-type terminal predicates (`Complete` for study; all-stages-terminal for flow; all-tasks-terminal & non-empty for execute).

## 6. Trust boundaries

- Stored rows re-enter as authoritative product state with unconditional row-wins on read: whatever process can write the DB file can silently redirect every subsequent load/gate decision for that record. The DB file sits in the same workspace-private directory as the JSON checkpoints (`.ultraplan/` 0700, DB 0600 when created through runcontrol's `preparePrivateDatabase`), so the trust transition assumes workspace-local attacker equivalence — same actor who could edit the JSON files directly.
- Decode asymmetry: DB-branch loads use plain `json.Unmarshal` for all three types (unknown fields tolerated, multiple/trailing values unchecked), whereas the sprint-flow FILE branch enforces DisallowUnknownFields + single-value grammar. Domain validators still run after DB loads (study/state.go:31, sprint/state.go:28, execute_state.go:43).
- Import trust: `storage migrate` trusts file content only after the existing validators accept it; validation failures are surfaced per-item, not fatal to the run.
- Integrity columns are decorative at read time: `header_hash`/`payload_hash` are never rechecked against stored blobs by any reader.
- Cancellation plumbing: every store call site passes `context.Background()` (study/state_database.go:67, sprint/state_database.go:96,126, and inside store Has/Load callers) — operation contexts (web shutdown, run cancellation) do not propagate into these queries; only `runcontrol.OpenSQLite` receives the real ctx from `runStorage`.
- Permission enforcement is asymmetric between pools: runcontrol Lstat-checks for symlinks/non-regular files and chmods dir+file to 0700/0600; productstate's `open` does MkdirAll(0700) and lets the SQLite driver create the file with the process umask, with no symlink/mode verification on the Existing path.

## 7. External effects & lifecycle semantics

- Effects confined to the workspace DB file and (via owning packages) terminal checkpoint files. First `Ensure`/`OpenSQLite` creates `.ultraplan/run-control.db` plus `-wal`/`-shm` sidecars; productstate adds only its two tables/indexes (`CREATE TABLE IF NOT EXISTS`), also executed on opens through `Existing` of an existing file.
- Process lifecycle: stores are cached per absolute root for the life of the process and hold up to 4 open connections each; nothing closes them (no Close API). Multiple UltraPlan processes share the file through WAL + busy_timeout(5s) + immediate transactions.
- Crash/restart story: committed imports persist (WAL, FULL sync); a crash mid-migration leaves a prefix of records imported — rerunning skips those (documented incremental behavior). No fencing token or lease of its own; mutual exclusion with runcontrol writers is purely SQLite locking.
- Retention/backup interplay (run-journal-fencing owns these): retention compaction (`PRAGMA wal_checkpoint(PASSIVE)`, `incremental_vacuum`) and quota enforcement operate on the whole file including product tables; runcontrol schema migrations create bounded timestamped `run-control.db.bak.*` copies (max 3 × 512 MiB) that contain product rows; `RestoreBackup` swaps the entire file, so restored snapshots revert product-state rows wholesale.
- Cancellation/retry: `runStorage` has no ctx checks between per-record imports (discovery loops are sequential; store calls use Background ctx); retrying the command after partial failure is the recovery mechanism. Dry-run reports would_import/skip/failed based on current DB contents without creating the DB.
- Error surfacing: infrastructural failures (workspace discovery, OpenSQLite, Ensure, DiscoverStudies/Projects) abort with ExitWorkspace before/without output items; per-record failures accumulate into the report and yield ExitPartial while remaining records still import.

## 8. Immediate surface dependencies

- `run-journal-fencing` (critical, durability-core): same DB file; `runStorage` calls `runcontrol.OpenSQLite` for its create/migrate/verify side effects even though the repository value is unused beyond Close; backup/restore/quota machinery treats product tables as opaque bytes of the file. Seams registered: none dedicated here; this surface appears in `seam-productstate-sprint-mirror` and `seam-productstate-study-mirror`.
- `sprint-flow-state`: `LoadFlowState`/`SaveFlowState` delegate their first branch to this surface (sprint/state.go:21, 219-231); checkpoint gating (`flowStateCheckpoint`) decides file freshness; DB-loaded records bypass v1/pre-code-context interpretation owned there.
- `sprint-execute-resume`: `LoadExecuteRunState`/`SaveExecuteRunState` route through this surface (execute_state.go:36, 106-118); migrate consults `LegacyTerminalExecuteStatus` (execute_state.go:81) to skip historical summaries.
- `study-runloop-scheduler` / `study-task-execution`: `LoadRunState`/`SaveRunState` route through this surface (study/state.go:28, 60-69); `studyWorkspaceRoot` assumes the standard `<root>/studies/<name>/.ultraplan/run-state.json` layout.
- `project-catalog` / `workspace-scaffold-defaults`: `DiscoverStudies`/`DiscoverProjects`/`DiscoverSprints` (all name-sorted) define migrate traversal; `discoverWorkspace` resolves the target root.
- Consumers of the flipped authority are downstream gates (review/smoke/QA/verify/status) described in the sprint-flow-state pack §6; they observe DB-authoritative values transparently through the same Load functions.

## 9. Contracts (CURRENT-CONTRACT evidence)

In-repo docs (target repo):
- `docs/migration-product-state.md` (added with the feature): "UltraPlan can store mutable study and sprint execution state in the workspace database instead of rewriting complete JSON documents"; preview/import via `scripts/migrate-product-state.sh <ws> [--dry-run]` (thin wrapper exec'ing `ultraplan --workspace <path> storage migrate "$@"`); "It validates each file with the existing product validator before importing it. Invalid files remain unchanged and produce a partial-failure exit status. Re-running the command skips records already in the database."; "After import, SQLite is authoritative for that record. Existing JSON files stay in place as compatibility checkpoints. Unmigrated records remain file-backed, so migration can be performed incrementally."; "The normalized tables are `product_states` and `product_state_items`. Study tasks, sprint stages, and sprint execute tasks occupy separate ordered rows. Large generated reports, prompts, and diagnostics remain files."
- `docs/architecture.md:187-194` ("Durable run control"): `.ultraplan/run-control.db` recorded; "`internal/runcontrol` owns operational identity, lifecycle, owner leases, fencing, cancellation… Sprint, study, smoke, runtime, lock, artifact, and Git modules remain authoritative for their own product state; run control only projects their safe correlations and status." (Describes the shared file split; the product tables implement the "remain authoritative" side.)
- `docs/recovery.md:236-241`: schema migrations create private timestamped backups next to `.ultraplan/run-control.db` (max three retained, 512 MiB each); restore is offline and swaps the file — bounds the mirror's exposure to backup/restore mechanics.
- `docs/cli-reference.md`: contains no `storage` section; the command's only user-facing docs are `migration-product-state.md` and the in-command help text.
- `docs/plans/ultraplan-local-server-experiment-plan.md` §D4/D5 (HISTORY/FUTURE-INTENT): planned an explicit `storage.mode: filesystem|sqlite` config and `storage migrate --from filesystem --to sqlite --workspace … --dry-run` with provenance recording, stable IDs, artefact revisions, and "switch configured authority only after validation succeeds". The implemented surface differs factually: no mode configuration exists, authority flips implicitly on row presence, flags are only `--dry-run|--json`, and no provenance/IDs/revisions are recorded.

Workspace docs:
- TRD.md L2235 (product persistence classification; FUTURE-INTENT framing for later phases): classify authored artifacts vs derived data; "select one product-artifact authority at composition; prohibit generic virtual filesystems, silent dual writes, and premature sync." The implemented mirror is an explicit dual-home (DB authoritative + gated file checkpoints), documented in `migration-product-state.md`.
- TRD.md L57/L126 treat "SQLite product persistence"/"any later SQLite authority" as requiring explicit product-mode decisions — context for the implemented-vs-planned contrast above.

HISTORY: introduced wholesale in commit `02e2ec4e93bd` "Move mutable product state to SQLite" (2026-08-21, four days before freeze; 10 files, +766 lines, includes the doc and script); one follow-up `8ee9d9c` "Skip legacy terminal sprint state during migration" (+4 lines in storage_commands.go). No other commits touch these paths before the frozen HEAD.

## 10. Tests (evidence map)

- `internal/productstate/` contains exactly one file (`store.go`) and **no test file**.
- Repo-wide search at the frozen commit finds **zero test references** to: `productstate`, `Ensure(`, any `*InDatabase` helper, `MigrateRunStateToDatabase`/`MigrateFlowStateToDatabase`/`MigrateExecuteStateToDatabase`, `runStorage`, or the string `storage migrate`. The only `run-control.db` mentions in tests (`runcontrol/sqlite_test.go`, `migration_test.go`, `retention_test.go`, `fault_test.go`, `process_integration_test.go`) exercise the runcontrol pool/schema/backups, not the product tables.
- App-level suites that exercise study/sprint flows (e.g. `app/sprint_commands_test.go`, `app/study_commands_test.go`) never create `.ultraplan/run-control.db`, so all covered flows take the file-backed branches; the DB-authoritative branches (load row-wins, DB save + checkpoint gating, migrate import/skip/fail matrix, `--json` report, ExitPartial aggregation) have no automated coverage at freeze. This is consistent with the sprint-flow-state pack's noted gap ("no test exercising the DB-authoritative save/checkpoint branch").
- Baseline: full `go test ./...` green at the frozen commit (review/baseline), i.e. absence-of-coverage is not masked by failures elsewhere.

## 11. Explicit unknowns / open questions (for later reviewers)

1. Read-consistency scope of `Store.Load`: header query and items query run as separate autocommit statements with no transaction; whether a concurrent committed Save between them can yield a header/items mix (e.g. stale header with new item set, or partially observed delete-sweep results) depends on SQLite per-statement snapshot semantics — unstated anywhere.
2. Whether plain-Unmarshal decoding of DB payloads (unknown-field tolerant, unlike the flow file branch) is deliberate leniency for forward compatibility or incidental; DB rows also bypass the v1/pre-code-context interpretation layers entirely.
3. Intent of the integrity columns: `header_hash`/`payload_hash` are written and used as upsert guards but never verified on read; whether corruption detection was intended is undocumented.
4. Cancellation posture: all store operations use `context.Background()`; long migrations and blocked writes (busy_timeout window) ignore caller cancellation. Whether shutdown paths rely on process exit instead is unstated.
5. Permission/symlink asymmetry: a DB created by `productstate.Ensure` alone (before any runcontrol Open) gets driver-default file permissions and no symlink/mode verification, unlike runcontrol's enforced 0600/regular-file contract on the same path.
6. Pool/lifetime model: per-root store cache never closes connections and caps at 4; combined with runcontrol's own pool on the same file, worst-case concurrent connection count and busy-timeout behavior under the web server's concurrency are uncharacterized.
7. Migrate edge behavior: path-resolution failures for a sprint (FlowStatePath/ExecuteRunStatePath errors) emit NO report item at all; stat not-exist is likewise silent — whether the report should distinguish "nothing to import" from "could not inspect" is unspecified.
8. Import timestamp semantics: saved rows receive `UpdatedAt = time.Now()` rather than the source file's UpdatedAt; history/provenance of the original state is not preserved anywhere (contrast with the planned importer's "record import provenance" in §D5).
9. Authority reversal: no command, API, or documented procedure moves a record back to file authority (or re-imports newer file content over an existing row, since import skips on row presence); whether file checkpoints drifting ahead of the DB is reachable in supported flows is unstated.
10. `storageMigrationResult.SchemaVersion` is hardcoded 1 with no documented meaning (report format version vs record schema), and `--json` output shape has no published contract outside the struct tags.
11. Interaction window between migrate and live processes: nothing prevents running `storage migrate` while a server holds DB-authoritative records; per-record transactions make interleaving safe at row granularity, but skip-vs-refresh decisions under concurrent writes are unspecified.

— End of context pack. Descriptive only; no defect claims made or implied.
