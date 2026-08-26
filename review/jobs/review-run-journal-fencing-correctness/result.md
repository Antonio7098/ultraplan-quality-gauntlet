Context pack read; core sources and tests reviewed. Let me establish the baseline and then reproduce candidate defects in a scratch copy.Working tree is clean and `internal/runcontrol` + `internal/app` are identical to the frozen commit `5d54525`. Running the package baseline now:Baseline green. Now writing reproductions in the scratch copy (`/tmp/opencode/repro`) for my top defect hypotheses:All three hypotheses confirmed, and the race test surfaced something bigger than my hypothesis — the failure is the **schema-migration lock**, hit in 39/40 fresh workspaces. Let me tighten repro 1b and check whether migration tests pin this lock behavior as intended:The poison self-heals once wall time passes `accepted_at` — refining the window. Now checking whether tests pin the migration-lock contention behavior (counter-evidence check), plus retention test:Tests pin malformed-lock→busy as intended, and stale-lock reclamation needs a *parseable* identity — so the empty-lock permanent-busy case is deliberate conservatism; but simultaneous first-open contention is unpinned. Quick counter-evidence check on retention/benchmark tests, then final verification of the reconcile-error cascade wiring:All evidence gathered. Cleaning up analysis and finalizing verdicts against counter-evidence found along the way.

---

# Review Report: `run-journal-fencing` (durability-core)

Target: `ultraplan-go`, working tree verified byte-identical to frozen `5d54525` for `internal/runcontrol` + `internal/app`. Baseline `go test ./internal/runcontrol/ -count=1` green. All reproductions ran in a scratch copy at `/tmp/opencode/repro` (`zz_gauntlet_repro_test.go`); target untouched.

## Findings

### F1 — A backward wall-clock step durably poisons active runs: they become unreadable, unappendable, and unterminable, and they block reconciliation of unrelated runs

**Severity: high (workspace-wide operational failure while active) · Confidence: high · Likelihood: low-medium**

**Claim.** Writers stamp `updated_at` unclamped from the wall clock while `Snapshot.Validate` strictly rejects any snapshot where `UpdatedAt < AcceptedAt` (model.go:346-348). Any system-clock step backwards below a run's `accepted_at` (VM/host restore, RTC reset, manual date change, large NTP step) followed by one owner write durably stores a row that fails read-side validation.

**Observable bad outcomes (all reproduced).**
1. Live owner heartbeats once after the step (heartbeat has **no** Validate — lifecycle.go:38-62), silently storing `updated_at < accepted_at`. From then on, every `Snapshot()`/`List` row/CLI `run show|list|follow` and web-hub read of the run fails: `validate: snapshot.timestamps must start at acceptance and move forward`.
2. Every `Append` rolls back (sqlite.go:690-696 Validate inside the tx) and every `ProposeTerminal` fails (sqlite.go:791-797) — the run can neither record events nor be arbitrated to *any* terminal, including interrupted/cleanup_uncertain by the owner itself.
3. The app spine's poll then fails (`internal/app/run_control.go:263-269`) → cancels working context → proposes `persistence_degraded` — which also fails to commit, so the run stays `running` with cancelled work.
4. **Cascade:** when the poisoned run surfaces as an expired-lease reconcile candidate whose probe says missing/mismatch/uncertain, `ProposeTerminal` errors and `Reconcile` aborts the whole pass (`return report, err`, lifecycle.go:401-403). Reproduced: healthy second run behind it was never reconciled (`Scanned:1`). Consequences wired in app: startup open fails closed (`run_control.go:66-70` "startup run reconciliation failed") and every live process's 10 s control-loop reconcile error cancels its own healthy runs (`run_control.go:289-296`).
5. Even pass-1 `reconcileUnclaimed` "succeeds" without validation, committing a terminal row that remains permanently unreadable by normal APIs.

**Trigger/preconditions.** Clock stepped below some run's `accepted_at` + any mutation by that run's owner (heartbeat every 5 s guarantees it for live owners). Poison persists until wall time re-passes `accepted_at` and a writer heals `updated_at`; window length equals the backward offset (hours/days for VM restores). Reproduced both states: error at clock 09:05 vs accepted 10:00; self-heal at 10:30.

**Execution path.** `WallClock.Now` → `formatTime(now)` into `updated_at` (lifecycle.go:48, sqlite.go:563,678) → `loadSnapshot` parses → `Validate` model.go:346 rejects → typed `invalid_argument` from every reader/arbitrator; reconcile abort propagates to open/control loops.

**Controls searched (counter-evidence).** `TestReconcileClockJumpNeverExpiresAnOwnerEarly` pins lease-expiry safety under jumps but never mutates `updated_at` backwards; no clamp/max() exists in any writer; `julianday` expiry math is immune but irrelevant to the stamping path. Schema CHECKs do not compare timestamps.

**Fix/regression test.** Clamp on write (`updated_at = MAX(updated_at, now)` or skip stamping when `now < updated_at`), or relax the read gate to tolerate skew while keeping monotonic intent. Regression: mutableClock accept+claim → step back 1 h → Heartbeat → assert `Snapshot()` succeeds and ProposeTerminal commits; assert Reconcile completes remaining candidates.

### F2 — Per-run journal saturation makes every further Append fail permanently — including the reserved recovery types the hard-quota contract promises to keep writable — and mislabels completed work as failed

**Severity: medium · Confidence: high (mechanism reproduced end-to-end) · Likelihood: medium**

**Claim.** Once one run accumulates >4096 non-removable events (`warning/finding/artifact/lifecycle/recovery/cancellation/terminal`), `compactRunJournal` finds nothing deletable and returns non-retryable `CodeQuota` (retention.go:85-86,94), rolling back the triggering Append. This defeats the reserved-type bypass in `Append`'s hard-quota gate (sqlite.go:618-622, 713-720): the invariant "reserved recovery types are always permitted" holds only at storage level, not at the per-run layer.

**Reproduced.** 4096 `finding` events → append #4097 fails `quota_exceeded retryable=false`; subsequent `warning`, `omission`, `lifecycle` appends all fail identically. Only ownerless paths survive: `RequestCancellation` and `ProposeTerminal` succeed (they never compact).

**Observable bad outcomes.**
- A healthy finding/artifact-heavy run (agentwrap streams map `finding`/`artifact`/`file`/warnings straight through `runtimeEventDraft`) dies spuriously: spine treats append failure as persistence loss → cancels work → records `persistence_degraded` although the disk is fine.
- Misleading terminal outcomes beyond saturation: `FinishOperation` (durable_operations.go:274-281) turns a trailing omission-flush failure into `runErr`, mapping an otherwise **successful** operation to terminal `failed` ("operation failed") — reachable whenever the flush hits the saturated journal.
- Existing test coverage only exercises removable `progress` saturation (retention_test.go:12-36), where compaction recovers; the non-removable case is unpinned.

**Trigger/preconditions.** Single run exceeding 4096 retained events with zero removable types among them.

**Fix/regression test.** Decide the contract: either include reserved types in the compaction-eligible set under pressure, exempt the per-run cap for reserved types (allow overflow), or fail only the offending draft class. Regression: seed 4096 `finding` rows (recursive CTE as in retention_test.go), assert a `warning`/`omission` Append still commits and `FinishOperation` of a successful op yields `succeeded`.

### F3 — First-ever concurrent open of a fresh workspace fails one opener ~always (migration-lock race); a crash inside the lock-write window bricks opens until manual file removal

**Severity: low · Confidence: high (race reproduced 39/40 fresh workspaces) · Likelihood: low**

**Claim.** On a version-0 database, every opener takes `.migrate.lock` via `O_CREATE|O_EXCL` (migration.go:92-110). Two processes opening simultaneously (server boot + CLI/web command in a new workspace) race: the loser gets `EEXIST`, tries stale-reclamation, reads the winner's not-yet-written (empty) identity, refuses removal ("legacy or malformed record cannot authorize lock removal", migration.go:146-149), and fails `CodeBusy` ("another local UltraPlan process owns the schema migration lock"). Reproduced: 39/40 paired concurrent `OpenSQLite` calls on fresh roots produced one failure. Nothing retries: `runControlState.repository` and CLI map open failure to a hard error. Aggravator: a crash between lock create and identity `Write` (migration.go:123) leaves a zero-byte lock that no process can ever reclaim — permanent busy requiring undocumented manual `rm`.

**Counter-evidence honored.** Malformed-lock refusal and proven-stale reclamation are deliberate and pinned (migration_test.go:67-117); failures are typed retryable and self-heal on a staggered retry. Hence low severity — but simultaneous-first-open is a realistic setup scenario (parallel init scripts, editor plugin + server boot) and there is no retry/backoff or operator documentation for lock cleanup.

**Fix/regression test.** On `EEXIST`, short-sleep-and-recheck schema version before declaring busy (the winner removes the lock on completion); treat a zero-byte lock as unreferenced (create-then-write protocol can leave nothing authoritative). Regression: two goroutine `OpenSQLite`s on a fresh root must both succeed; a pre-created empty `.migrate.lock` plus absent owner must open successfully.

## Defended non-issues (hypothesized, then refuted)

- **Unverified `_txlock=immediate` (pack unknown #4):** MAX+1 ordinal/generation allocation is safe anyway — empirically pinned by the cross-process 40-gapless-events integration test, with `UNIQUE(run_id,fencing_generation)`/`(run_id,ordinal)` as backstops producing typed conflicts, never corruption.
- **Injected-clock grace predicates compare RFC3339Nano as TEXT:** test-only distortion (production always takes the `julianday('now')` branch — grep confirms no non-test injector). Production TEXT comparisons (list cursor, retention cutoffs) are self-consistent keysets or immaterial ≤1 s skews.
- **Reconcile batch starvation (unknown #5):** unclaimed backlog requires crashed-between-accept-and-claim processes, drains ≥64/cycle per process; sustained starvation implausible.
- **Ignored `recordReconciliation` errors (#6), unwritable `cancellation_state='uncertain'` (#9), quota-counting of backup/lock files (#10):** diagnostic-only loss, a constant-zero projection, and conservative-direction accounting respectively — no wrong durable outcome.
- **Claim-once permanence + reconciler reusing the dead attempt's fence (#3/#7):** sound precisely because `current_attempt_id` is never cleared and every terminal transition is CAS-guarded `terminal_outcome IS NULL`; losers observe the incumbent (pinned). Late claims get typed Conflict/Terminal.
- **Hard-quota heartbeat refusal ending healthy runs (#8)** matches the documented "owner must stop active work" contract; `persistence_degraded` honestly reflects stopped persistence.
- **Vestigial `accepted`/`claimed` event types and dead alias columns (#1/#2), List N+1 (#13), Windows probe absence (#12), duplicated follow/SSE cadence constants (#14):** no observable incorrectness found; ownership facts live in `attempts`, acceptance in `runs`.

Scratch artifacts left at `/tmp/opencode/repro/ultraplan-go/internal/runcontrol/zz_gauntlet_repro_test.go` for reviewer reproduction; the target repository was not modified.