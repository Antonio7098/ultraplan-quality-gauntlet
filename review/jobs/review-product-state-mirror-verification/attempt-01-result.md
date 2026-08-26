Context pack read. Note: the pack cites commit `8eef70f4` but my assignment freezes at `5d54525` (current HEAD). Let me verify the state and read all primary sources.HEAD matches my assignment (`5d54525`). The pack cites an older freeze; I'll verify the relevant files didn't change between commits. Now reading all primary sources.Diff between pack's commit and HEAD on these files is empty — pack applies. Reading the routing files and migrate command.I've now read all primary sources. Key hypotheses forming: (a) DB-availability coupling hard-fails file-backed records, (b) migrate-vs-live-writer race causing permanent state regression, (c) torn reads in `Load`, (d) misleading dry-run summary, (e) zero test coverage. Let me gather supporting evidence.Zero test coverage confirmed. Now reading `OpenSQLite`, discovery, and helper functions.Doc confirms "Re-running skips records already in the database" and files stay as checkpoints. Now let me check the dispatch context, runcontrol fencing/lease primitives, and the state structs needed for runtime experiments.I have the full picture. Now let me set up runtime experiments on a scratch copy (target repo untouched).Writing experiment tests on the copy.X1/X1b/X2 all confirmed at runtime. Now the torn-read experiment (X3) and the CLI dry-run check (X4).Torn read confirmed decisively (~15% of reads hybrid under concurrency). Now X4 — CLI behavior checks.Dry-run summary defect confirmed ("1 imported" while nothing was imported). Counter-checks: exit codes, invalid-file handling, and whether any locking exists that migrate ignores.All evidence gathered. Verifying the target repo is untouched, then writing up findings.# Findings: `product-state-mirror` (verification/operability lens)

All experiments were run against a scratch copy of the frozen tree (`5d54525`) in `/tmp/opencode/upt-x`; the target and workspace repos are untouched. The context pack's commit label (`8eef70f4`) predates HEAD, but `git diff` shows zero changes to this surface between them; all pack facts hold.

---

## F1 — Any shared-DB failure hard-fails ALL study/sprint load AND save operations, including never-migrated file-backed records

**Severity: High · Confidence: High** (code path + two runtime reproductions)

**Claim.** `Existing()` (`internal/productstate/store.go:41-51`) stats the DB and, if present, runs `open()` → `createSchema` DDL unconditionally (`store.go:79`, `92-116`). Every error propagates: `loadSprintRecord` (`internal/sprint/state_database.go:58-61`) returns it, `LoadFlowState` aborts before ever consulting the file (`sprint/state.go:21-22`), same for study (`study/state.go:28-29`). Save paths are equally coupled: `FlowStateInDatabase`/`RunStateInDatabase` errors abort the save entirely even when the record has no row and the file-only branch would be correct (`sprint/state.go:219-220`, `study/state.go:60-62`). There is no fallback, no sentinel error class, no degradation.

**Observable bad outcome (reproduced).**
```
X1  load: BLOCKED with error: file is not a database (26)     ← record was fully file-backed
X1  save: BLOCKED with error: file is not a database (26)
X1b load (no row for this record): BLOCKED: unable to open database file (14)  ← chmod-drifted DB
```
A corrupt or permission-drifted `.ultraplan/run-control.db` bricks every study/sprint command — status, verify, resume, QA gates — for **every** record, including ones still purely file-backed and unaffected by the mirror. Before this feature, none of these operations touched a database.

**Trigger/preconditions.** DB file exists but is unreadable/corrupt/read-only: crash-truncated creation, restore-as-different-user perms, an old main file restored beside a stale `-wal` sidecar (classic NOTADB), read-only mounts, chmod drift after backups. Note also that a *read* probe performs schema DDL on open, so even read-only-by-design databases fail loads.

**Counter-evidence searched.** No caller distinguishes store errors from domain errors (raw sqlite strings like `file is not a database (26)` surface to users with no hint the optional mirror is the culprit); no code path retries file-backed reads on store failure.

**Regression test.** Fixture workspace with valid file-backed state + garbage/0600-unreadable `run-control.db`: assert loads either succeed via file fallback or fail with a classified error naming the DB as cause; today both halves fail the first assertion.

---

## F2 — `storage migrate` racing a live writer permanently shadows newer state behind a stale imported row

**Severity: Medium-High · Confidence: High mechanism / Moderate frequency**

**Claim.** Authority flips on row presence alone (`store.go:41-51`, routing in `sprint/state.go:219`, `study/state.go:60`); import skips when a row exists (`storage_commands.go:93-101`, `147-151`); there is **no mutual exclusion**: `runStorage` acquires nothing (`storage_commands.go:33-140`), while the codebase's own convention for protecting active product state — `AcquireRunLoopLock` (`study/locks.go:34`, held by run-loops `run_loop.go:31` and by `serve reconcile` `cleanup_uncertain.go:74`) — is ignored by the importer. Nothing in help text or `docs/migration-product-state.md` warns against migrating during a run.

**Observable bad outcome (deterministic replay of the exact branch sequence, reproduced).**
```
writer probe RunStateInDatabase=false → migrate commits row from stale file snapshot S1
→ writer resumes on stale probe result and completes its save to the file only (S2)
⇒ LoadRunState returns run-1 forever; file holds run-2;
  every future storage migrate reports "skipped"; no supported path repairs this.
```
Newest task progress is silently rolled back and stays wrong: the shadowing is permanent because re-import skips on row presence. Recovery requires manual SQL deletion of rows.

**Execution path.** `SaveRunState`'s probe→write window (`study/state.go:59-70`) straddles the importer's per-record `Store.Save` commit; interleaving is reachable whenever an operator migrates while a run-loop or server mutates state — precisely the incremental-adoption scenario the doc encourages.

**Regression test.** The X2 interleaving test: after replay, assert `LoadRunState.RunID == "run-2"`; fails today.

---

## F3 — `Store.Load` reads header and items as two autocommit statements: hybrid records under concurrent saves

**Severity: Medium · Confidence: High mechanism / Medium impact**

**Claim.** `Load` (`store.go:127-148`) issues the header SELECT and the items SELECT as separate snapshots with no transaction. A concurrent committed `Save` between them yields a header/items mix; stored `header_hash`/`payload_hash` are never rechecked on read, and validators don't cross-check header fields against item content (`ValidateRunState` `study/state.go:121-166`; `ValidateFlowState` `sprint/state.go:294-367` checks stages only internally).

**Measured.** Concurrent saver alternating epochs A(1 item)/B(2 items): **2,954 of 20,000 loads returned hybrid records**, e.g. `header_tag=B header_n=2 items=1 item_tags=A`. For real records this pairs e.g. a stale `Complete=false` header with completed tasks (or stale Review/Smoke completion evidence with reset stages), silently feeding resume/gating decisions.

**Fix/regression.** Wrap both queries in one deferred read transaction (WAL gives a stable snapshot); test asserts header-epoch == item-epoch across N reads under a concurrent saver.

---

## F4 — Dry-run summary line claims records were imported

**Severity: Low-Medium · Confidence: High** (captured runtime output)

`appendItem` counts `would_import` into `result.Imported` (`storage_commands.go:72-74`) and the text summary prints it unqualified (`storage_commands.go:134`):
```
$ ultraplan --workspace ws storage migrate --dry-run
would_import study_run      sample …/run-state.json
Product-state migration: 1 imported, 0 skipped, 0 failed      ← nothing was imported
```
JSON mode carries `dry_run:true`, but the human-facing summary — the mode operators will actually eyeball — states a false completion fact. Counter-checked: exit code (0), per-item statuses, and no-DB-creation in dry-run are all correct.

---

## F5 — Entire surface ships with zero automated coverage

**Severity: High (verification gap amplifying F1–F4) · Confidence: High**

No test file exists in `internal/productstate`; repo-wide grep finds zero test references to any mirror symbol, `runStorage`, or `storage migrate`; app-level suites never create `run-control.db`, so every DB-authoritative branch (row-wins loads, checkpoint-gated saves, import/skip/fail matrix, ExitPartial aggregation) was dead code under `go test ./...` at freeze. F1–F4 are each exactly the kind of defect one focused table-test would have caught.

---

## F6 — Non-dry-run `storage migrate` silently runs runcontrol schema migration (incl. timestamped backups)

**Severity: Low · Confidence: High.** `runStorage` calls `OpenSQLite` (`storage_commands.go:60`) which executes `migrateSchema` and may create up to three ≤512 MiB `run-control.db.bak.*` copies (`runcontrol/sqlite.go:112-117`, `migration.go:195-199`) plus full runcontrol tables in workspaces that never opted into run control. Neither the command help nor `docs/migration-product-state.md` mentions it. Surprising disk/layout side effects from a "product import" command.

---

## Defended non-issues (searched, counter-evidence found)

- **Plain `json.Unmarshal` on DB branches vs strict file grammar:** rows are only written through validated typed structs; leniency is forward-safe, not exploitable beyond the existing workspace-local trust equivalence.
- **DB loads bypassing v1 migration / pre-code-context interpretation:** unreachable with stale-version content — every write path validates current `SchemaVersion` before persisting, and import migrates v1→v2 in memory first (`sprint/state.go:68-76`, validators in all three savers).
- **Idempotency/skip contract:** verified live — second run reports `skipped`, invalid files left unmodified with ExitPartial=8, files never mutated by the command.
- **Ensure-created file permissions vs runcontrol's 0600 regime:** practically unreachable in-repo; every `Ensure` caller requires a pre-existing row or runs after `OpenSQLite`, which creates the file first inside a 0700 directory.
- **Silent skip when `FlowStatePath` errors:** discovery sanitizes names upstream (`project.IsSafeName`, `discovery.go:25`), making the path-error branch unreachable for discovered sprints.
- **`LegacyTerminalExecuteStatus` migrate gating:** correctly scoped to `schemaVersion==0` legacy summaries; cannot mask current-format files.