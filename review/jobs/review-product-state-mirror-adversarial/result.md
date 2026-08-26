Context pack read. Frozen commits verified. Now inspecting the primary sources myself.Primary sources read. Now verifying struct shapes, discovery requirements, runcontrol interactions, and dispatch wiring to test specific hypotheses.Wiring confirmed. Now checking fixture requirements (`discoverWorkspace`, `resolveSprintContained`, `FlowStatePath`) before building proofs.Binary built at the frozen commit. Now Proof 1: does `storage migrate --dry-run` mutate an existing run-control DB?The dry-run exited 8 (failed item, expected for `{}` content) which aborted the script under `set -e`. Re-checking schema state after the dry-run:**Proof 1 confirmed**: dry-run created `product_states`, `product_state_items`, and the order index in the existing DB (`['app_schema']` → all four objects). Now testing the read-only variant:**Proof 1b confirmed**: on a read-only DB, dry-run reports every record `failed` ("attempt to write a readonly database") and exits 8 instead of previewing. Now Proof 2 — torn read across `Load`'s two autocommit statements:Reader started before first commit — seeding first, then re-running:**Proof 2 confirmed** — torn read after just 14 iterations: header from revision 1 observed with revision 2's item set, a record state that never existed as a commit. Now Proof 3 — the migrate/live-writer lost-update shadow (deterministic interleaving):**Proof 3 confirmed**: post-interleave, loads return the stale imported snapshot while the newest committed state lives only in the shadowed file; healing happens only if another writer save occurs later. Now Proof 4 — legacy v0 artifacts vs migrate exit status:**Proof 4 confirmed**: perpetual exit 8 on every migrate run for workspaces containing legacy v0 flow states, while the identical-vintage legacy execute state is cleanly skipped. Final counter-evidence sweep before writing findings:Saves are serialized in-process (`saveMu`, run_loop.go:157) but cross-process reads/writes are an explicitly supported pattern — that's the honest trigger frame for the torn read. Verifying the target repo stayed untouched, then writing up:Target untouched; all proofs ran against a copy at the frozen commit. Findings:

---

# Review: `product-state-mirror` (adversarial, fresh-context)

**Verdict: 4 substantive findings (1 high, 2 medium, 1 low-medium), all mechanically verified against `5d54525`; plus defended non-issues.**

---

## F1 — HIGH · Authority flip has no recency arbitration: migrate/restore permanently shadow newer committed file state

**Claim.** Once any `(kind,scope)` row exists, every load takes it unconditionally (`study/state.go:28-35`, `sprint/state.go:21-31`, `sprint/execute_state.go:36-46`). The only producers of rows are validated saves and `storage migrate` — but migrate imports a snapshot read *before* its insert (`storage_commands.go:153→155`, `:176→178`), and nothing anywhere compares row vs. file recency or re-imports. A writer whose save began before the import committed probes "no row" (`state.go:219`, `execute_state.go:106`), writes the newest state to the **file only**, and that newest state is silently shadowed forever by the stale imported row.

**Proven** (deterministic white-box interleaving, package `sprint`, copy of frozen commit):
```
migrate reads file X → live writer commits Y to file (probe said no-row) →
migrate inserts X → LoadFlowState returns requirements=missing (stale X);
file holds requirements=complete (Y), never consulted again
```
The divergence heals only if *another* writer save happens later. If the shadowed write was terminal (study `Complete`, all-stages-terminal flow checkpoint — i.e., the run's last write, `study/state.go:66-68`, `state.go:225-228`), the completed outcome is invisible indefinitely: resume re-runs finished work; review/smoke/QA gates act on mid-flight state.

**Same end-state via a supported recovery path:** `runcontrol.RestoreBackup` swaps the entire DB file (`runcontrol/migration.go:296-352`), reverting product rows to backup-era content while newer terminal checkpoints sit shadowed beside them. Repo-wide grep confirms zero reconciliation/re-import code exists (only the 26 routing/migrate call sites). Completed sprints/studies resurrect as in-flight after restore.

**Trigger.** `storage migrate` run while a sprint/study is active (nothing forbids it; docs are silent), or any post-backup restore.
**Counter-evidence found.** Next-save healing exists (shown in test) but requires a subsequent write; validators pass on the stale-but-well-formed row, so nothing detects it.
**Severity** high (silent loss of newest committed product state / resurrection of completed work); **confidence** high mechanism (proven), medium likelihood.
**Regression test.** The interleaving test above asserting `Load` returns the newer content (fails today); plus an integration test: save terminal → snapshot DB → advance → restore snapshot → assert load reflects terminal file or an explicit documented re-import step.

## F2 — MEDIUM-HIGH · `Store.Load` is not atomic: readers observe never-committed header/item hybrids

**Claim.** `Load` runs the header SELECT and items SELECT as two autocommit statements with no transaction (`productstate/store.go:129,135`). Each gets its own WAL snapshot, so a Save committing between them yields old-header + new-items — a record that never existed as a commit.

**Proven:** concurrency probe hit a torn read after **14 iterations** — `header={"revision":1}` observed together with revision 2's item set.

**Impact.** For `FlowState`, verification evidence (`Review`/`Smoke`/`QA`) lives in the header while stage progress is items → stale evidence paired with advanced stages decodes fine and passes `ValidateFlowState` (it checks structure, not cross-freshness), then feeds gates. Same skew class for study (`Complete` in header, task statuses as items) and execute.

**Trigger.** Any concurrent same-record save+load across processes — explicitly supported design (WAL + busy_timeout on both pools): e.g., `sprint execute` persisting periodically while `sprint status` polls, or `study run`'s serialized persister (`run_loop.go:157`) racing a read-side command. In-process saves are mutex-serialized but loads from other processes aren't participants.
**Counter-evidence.** Reverse direction impossible (header read first); single-threaded CLI flows unaffected.
**Severity** medium-high (silent corruption class); **confidence** high mechanism (proven), medium practical frequency.
**Regression test.** The consistency-invariant probe test (header rev ⇒ exact matching item set) wrapped in one read transaction would pass trivially; fails today.

## F3 — MEDIUM · `--dry-run` mutates the database it previews, and cannot preview read-only workspaces

**Claim.** Dry-run skips `OpenSQLite`/`Ensure` (`storage_commands.go:59-68`) but the per-record probes (`:93`, `:147`, `:170`) reach `productstate.Existing` → `open` → `createSchema` (`store.go:79`), executing DDL against any existing DB.

**Proven (e2e, binary at frozen commit):**
- Pre-existing `run-control.db` containing only `app_schema`: after `storage migrate --dry-run --json`, `sqlite_master` contains `product_states`, `product_state_items`, `product_state_items_order`. The preview created schema objects.
- Same command against a **read-only** DB with two valid studies: both items reported `"failed","error":"attempt to write a readonly database (8)"`, exit **8** — a pure preview fails because its own probes try to write.

Docs frame `--dry-run` as "Preview the migration" (`docs/migration-product-state.md:6-10`); help text claims no mutation exemption either. Previewing a workspace whose authority artifact you dare not touch is exactly when dry-run gets used.
**Counter-evidence.** Tables-without-rows don't flip authority, so harm is contract violation + failure mode, not data loss.
**Severity** medium; **confidence** high (reproduced twice).
**Regression test.** e2e: pre-existing DB + dry-run ⇒ assert `sqlite_master` unchanged and exit 0 with `would_import`; read-only variant ⇒ clean preview.

## F4 — LOW-MEDIUM · Legacy v0 flow states make `storage migrate` fail forever (exit 8 every run)

**Claim.** v0 map-era flow files — a shape the codebase explicitly tolerates elsewhere for compatibility (`legacyFlowState`, `sprint/state.go:131-148`) — are reported `failed` by migrate on every invocation, so such workspaces can never reach exit 0, while the identical-vintage legacy execute state got an explicit skip (`LegacyTerminalExecuteStatus` gate at `storage_commands.go:166-169`, commit `8ee9d9c`).

**Proven (e2e):** fixture with `{"version":1,...}` flow + `{"schemaVersion":0,"status":"complete"}` execute: run 1 → `sprint_flow failed ("missing schemaVersion")`, `sprint_execute skipped`, exit 8; run 2 → byte-identical result. Contradicts the doc's incremental/rerunnable framing (`migration-product-state.md:21-26`).
**Severity** low-medium (permanent automation-visible failure, inconsistent legacy policy); **confidence** high (reproduced).
**Regression test.** e2e migrate over a legacy-v0 flow fixture expecting `skipped` + exit 0.

---

## Defended non-issues (hypotheses raised, counter-evidence found)

- **`schema_version` column unread on load:** harmless — header JSON carries `schemaVersion`/`schema_version`; all three validators reject mismatches post-decode (`ValidateRunState` :125, `ValidateFlowState` :298, `ValidateExecuteRunState` :188). Version skew handled like files.
- **Plain-Unmarshal (unknown-field-tolerant) DB decode vs strict file grammar:** unreachable from current writers (validators gate every save); foreign-version rows rejected by validator. Only a workspace-local actor could inject such rows — one who could equally edit JSON files directly.
- **`header_hash`/`payload_hash` never verified on read:** decorative integrity columns; no documented detection contract; correct as upsert guards.
- **Duplicate item keys within a Save colliding via upsert:** all three validators reject duplicate task IDs/stages pre-save.
- **Scope injection via names:** project/slug pass `IsSafeName` (no separators, discovery-derived); study scope is a directory name.
- **DB file perms from `Ensure`:** created inside 0700 dir; runcontrol's `OpenSQLite` re-enforces 0600/`enforcePrivateMode` on next open; exposure needs unrelated prior drift.
- **`context.Background()` throughout store ops:** bounded by busy_timeout(5s); retry-by-rerun is the documented recovery; no correctness impact.
- **Two pools on one file:** WAL + immediate txlock + busy_timeout serialize writers; no nested acquisition, no deadlock pattern found.
- **Ordinal gaps after item sweeps / `updated_at` not bumped on items-only change:** relative order preserved; cosmetic only.

Proof artifacts (test files + fixtures + binary) are under `/tmp/opencode/psm-probe/`; target repo verified clean at `5d54525`.