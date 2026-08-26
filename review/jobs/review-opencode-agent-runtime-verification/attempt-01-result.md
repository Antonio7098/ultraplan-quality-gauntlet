Context pack read. Now reading all primary sources at the frozen commit.Now the test files:Sources read. Now verifying SDK-side behavior in the module cache (ListModels plumbing through the stack, OPENCODE_DB ordering, fallback-request merge, Events channel close semantics).Key confirmations so far. Now checking the SDK reconcile/db-command shape, fallback-request merge, and how callers use deletion:Two strong leads confirmed. Now examining consumers (study/sprint/run-control) for reachability and late-event handling:Baseline tests first, then I'll build an out-of-tree harness to verify candidates:Baseline green. Building an out-of-tree verification harness (target repo untouched):Two candidates verified already. Fixing the two harness issues to complete verification:The ambient environment itself just demonstrated the hazard — this shell carries a stray `OPENCODE_DB` from an earlier review attempt, and the deletion hook inherited it blindly. Making the harness precise:Candidate 1 fully proven. Now verifying Candidate 7 (XDG pruning divergence):XDG divergence confirmed. Checking one last lead — whether the startup GC result is consumed anywhere:Final counter-evidence sweep before writing up:All verification complete; target repo untouched (clean tree at frozen commit). Harness artifacts live in `/tmp/opencode/verify`. Final report:

---

# Findings — `opencode-agent-runtime` @ `5d54525`

## F1. Session deletion + checkpoint/VACUUM execute against a different database than the one sessions were created in (no `OPENCODE_DB` wiring on the delete path)

**Claim.** Runs are DB-isolated via request metadata → subprocess env (`runtime.go:580` sets `opencode.database_path`; SDK `opencode/runtime.go:155-160` turns it into `OPENCODE_DB`, stripping any ambient value via `setEnvValue`). The deletion hook builds its commands with `cmd.Env = append(os.Environ(), c.Agentwrap.Env...)` (`opencode.go:108`, `:182`) — no store path, no `--dir`, no ambient sanitization. Every deletion command therefore targets whatever database the *ambient* environment resolves to, never the scoped `<sha256(owner)>/opencode.db` that holds the sessions being deleted.

**Verified** (`/tmp/opencode/verify`, `TestDeletionCommandsLackStoreDBWiring`): for a prepared store-scoped session, the batch ran `db DELETE FROM event_sequence WHERE aggregate_id='sess-1'`, `session delete sess-1`, `PRAGMA wal_checkpoint(TRUNCATE)`, `VACUUM`, second checkpoint — all with `OPENCODE_DB` unset while `toAgentwrapRequest(req).Metadata["opencode.database_path"]` correctly pointed at the scoped store.

**Aggravating corroboration:** this review shell's own ambient env carries a stray `OPENCODE_DB=.../.quality-gauntlet/jobs/review-opencode-agent-runtime-verification/attempt-01/opencode.db` from an earlier attempt; the first harness run inherited it and the deletion commands silently targeted that unrelated scratch DB. Destructive SQL plus `VACUUM`/`wal_checkpoint(TRUNCATE)` redirect wherever ambient env points. Runs are protected against exactly this leakage; deletes are not.

**Observable bad outcomes.**
1. By-ID deletion never frees session data in scoped stores; it reports success against the wrong DB. Reachable production sites deleting by ID: `review.go:643-646`, `session_state.go:222-227`, `flow.go:51`, study fallback `service.go:60→78-90`.
2. `checkpoint(TRUNCATE)`+`VACUUM` run against a foreign database (user's interactive OpenCode DB under default resolution, or arbitrary ambient target), blocking its writers up to 20×250 ms and compacting data UltraPlan doesn't own.
3. Spurious batch failures when the foreign DB is busy abort mid-loop, leaving earlier sessions deleted and `cleanupPlanningStageSession` from clearing its state key (`session_state.go:223-226`).
4. Error truth: `checkpointOpenCode` (`opencode.go:151-167`) discards per-attempt exit errors and reports `"database remained busy"` after 20 tries even when the real failure is a missing executable or non-zero exit — reproduced accidentally in the harness (5.17 s, empty detail, root cause invisible).
5. Output-shape fragility: the parser expects ≥3 whitespace fields with third-from-last `== "0"` from `db <sql>` without `--format json` — inconsistent with agentwrap's own `db --format json` usage (`opencode/runtime.go:668`); JSON-shaped output burns 5 s then fails the whole batch (verified: `TestCheckpointParserFailsOnJSONOutput` vs `…SucceedsOnTableOutput`).
6. `DeleteSessions(ctx, [])` / all-whitespace IDs still run the full checkpoint→VACUUM→checkpoint cycle against the foreign DB (`opencode.go:113-123` runs unconditionally).

**Counter-evidence searched:** no code injects `OPENCODE_DB` into config env dynamically (single repo-wide hit is the metadata key); no `--dir`/cwd alignment exists (commands inherit the ultraplan process CWD); zero test coverage of the hook, checkpoint loop, or prune-on-success wiring (grep of `*_test.go`: no references). Pack unknown #4/#11 flag adjacent symptoms but not this mismatch.

**Severity: high** (silent no-op reclamation + destructive maintenance of unowned databases). **Confidence: high** on the wiring divergence and its reachability (in-tree, verified); medium on exact end-to-end behavior only because `<exe> db/session delete` semantics are external.

**Regression test:** executable-stub recording argv/env (as built): assert every deletion invocation receives `OPENCODE_DB=<scoped store path>` matching the owner hash used at creation, and that a non-zero `db` exit propagates as the checkpoint error instead of "remained busy".

## F2. `Adapter.Health` silently drops required-capability enforcement when `Capabilities()` errors

**Claim/Bad outcome.** `health.go:74` — `if caps, err := a.Capabilities(ctx); err == nil { … }` — on error, skips appending capability checks *and* skips the enforcement loop at `:97-101`, returning `(report, nil)` with `Status: ready`. **Verified** (`TestHealthSwallowsCapabilitiesErrorAndSkipsRequiredCaps`): failing `Capabilities()` + `req.Capabilities=["permissions"]` ⇒ `ready`, zero capability entries, nil error. Consumers (`health_commands.go:117-149`) then print success with no capability rows; operators get green health while required capabilities were never evaluated. Transient failures (binary missing, spawn hiccup) convert "cannot verify" into "verified OK".

**Trigger/preconditions:** any `Capabilities()` error combined with passing (or absent) required health checks.
**Controls searched:** none downstream — `run_usecases.go:55` delegates to repository health, not caps; StartRun preflight would catch it later per-run, so the harm is misleading verification signal, not unsafe execution.
**Severity: medium-low; Confidence: high.** Regression: unit test asserting Health surfaces an error/warning when required capabilities were requested but the capability probe fails.

## F3. Secret redaction applied inconsistently across mapped diagnostics; one field neither truncated nor redacted

**Claim/Bad outcome.** `config.RedactValue` is applied only to SDKError fields and attempt details (`runtime.go:759-761`, `events.go:64-67`). Sibling diagnostic surfaces carry provider/model-generated text unredacted: `Result.Warnings` (`runtime.go:619`), `PolicySummary.ExhaustedReason`/decision Reason/Detail (`events.go:83,:90-91`), `ValidationSummary.Details` ← validation failure `Observed` i.e. model output (`events.go:106`; SDK `validation.go:684-689`), `PermissionSummary.UnsupportedReasons` (`events.go:123`), artifact descriptions (`events.go:36`). `RepairSummary.ExhaustedReason` (`events.go:145`) bypasses truncation too, violating the documented ≤4096 diagnostic bound. **Verified** (`TestDiagnosticsRedactionInconsistency`): `"leak api_key=SK-SECRET-VALUE here"` flows verbatim through all five surfaces while `mapSDKError` returns `[REDACTED]` for identical content. These summaries persist into durable run journals and the web timeline.

**Trigger:** secret-shaped strings echoed in warnings/validation output/model text — the exact content class the redaction control exists for, enforced on adjacent fields of the same mapping layer.
**Severity: medium-low (control gap, defense-in-depth); Confidence: high** on inconsistency, medium on exploitability.

## F4. Abandoned collector goroutine delivers `OnEvent` callbacks after `StartRun` returns

**Claim/Bad outcome.** On the 5s-grace synthesized-cancel path (`runtime.go:362-377`) — and via the 1s drain cap on `:400` generally — StartRun returns while the goroutine at `:332-344` still ranges `run.Events()` and invokes `req.OnEvent`. **Verified** (`TestOnEventFiresAfterStartRunReturnsViaGraceBranch`): callback invoked after StartRun returned a terminal cancelled result. Downstream consequences: (a) `controlledRuntime`'s wrapper (`run_control.go:204-251`) can persist journal events between `base.StartRun` returning (:302) and `cancel()` (:317) / terminal proposal (:337), i.e., writes racing or following terminal-outcome proposals; (b) study's closure (`study/run.go:163-172`) calls `checkpoint(event.SessionID)` → `onSession`, resurrecting session resume rows after `deleteCompletedSessions` just removed them, pointing continuation at deleted sessions (mitigated one level down by fresh-fallback, at the cost of a burned attempt); (c) the natural contract "no callbacks after StartRun returns" is broken for every consumer.

**Counter-evidence:** with the real adapter, `Cancel` TERM/KILLs the process group so the stream closes within ~2s+grace, bounding the window; no crash/race results because consumers mutex-guard. Still an ordering guarantee violation with state-mutating side effects, untested (pack gap confirms).
**Severity: medium; Confidence: high** on mechanism (reproduced), medium on consumer blast radius.

## F5. GC has two permanent blind spots: active-state stores with reused PIDs, and directories with unreadable `store.json`

**Claim/Bad outcome.** Removal requires `State != RuntimeStoreActive` for quota, aggressive, and expiry paths (`store.go:221-223`); dead-owner flipping additionally requires `!processAlive(PID)` (`:211`). A crashed run whose PID was recycled leaves an active store that survives *all* GC forever — maxAge, 2 GiB quota, and aggressive sweeps included. **Verified** (`TestActiveStoreWithLivePIDNeverRemovedDespiteAge`): year-old active store with an unrelated live PID survives aggressive cleanup. PID reuse is routine on long-lived hosts (pid_max wraparound). Second hole: `InspectRuntimeStores` silently skips dirs whose `store.json` is missing/corrupt (`:187-189`), so those dirs (DB bytes included) are invisible to inspection, GC, and quota accounting indefinitely. Compensating controls claimed elsewhere (30-min staleness) apply only after liveness fails, so neither hole has one. Sprint discards `CleanupRuntimeStores` results entirely (`runtime_metrics.go:119`); study at least feeds diagnostics (`run_loop.go:60-61`, `:381`).

**Severity: medium (resource leak, disk-pressure resilience); Confidence: high** on semantics, moderate on trigger frequency.

## F6. Log pruning targets the wrong root whenever ambient `XDG_DATA_HOME` differs from `$HOME/.local/share`

**Claim/Bad outcome.** Subprocess env is `os.Environ()+cfg.Env` (SDK `process.go:22-24`), so an ambient `XDG_DATA_HOME` directs OpenCode logs there; `pruneOpenCodeLogs` consults only cfg env (`envValue`, `opencode_maintenance.go:26`) and falls back to `$HOME/.local/share` (`:28-32`), ignoring the ambient variable. **Verified** (`TestPruneTargetsWrongRootWhenAmbientXDGAmbientSet`): stale log under ambient XDG root survives; pruner happily deletes from its wrong HOME-based target. Net effect: the 48h/128MiB policy permanently no-ops on the real log directory (unbounded growth), while pruning activity looks successful. Errors are also discarded at the only call site (`opencode.go:89 _ =`).
**Severity: medium-low operability; Confidence: high** (logic verified; requires ambient `XDG_DATA_HOME`, common outside bare-bones hosts).

---

## Defended non-issues (searched, counter-evidence found)

- **ModelLister plumbing**: assertion on the wrapper stack succeeds; ObservingRuntime/ValidatingRuntime/PolicyRunner/requestVariantRuntime all forward (`observability.go:192`, `policy.go:335`, `validation.go:196`, `opencode.go:212`). Not broken.
- **Backup-fallback metadata loss**: `fallbackRequest` starts from the original request and overlays only non-empty fields (`policy.go:791-812`); backup alternative carries Provider/Model only, so `OPENCODE_DB`/cache metadata survive. Invariant 6 holds.
- **SQL injection via session ID**: `sqliteString` quote-doubling is sufficient for the single fixed string-literal context; argv passing avoids shell interpretation; variant values are config-sourced single argv elements.
- **Sanitize bounds, ring semantics, terminal-output separation, RawOmitted handling**: pinned by tests and consistent on inspection.
- **Dead-owner flip restarting the retention clock** (`store.go:216` rewrites UpdatedAt): one-time 72h delay per abandoned store, consistent with resumability intent — not report-worthy.
- **Study-store GC absence**: false hypothesis — `run_loop.go:60/:381` covers study scopes.

**Cross-boundary note (outside this repo, for the ledger):** the SDK's durable-DB reconcile (`opencode/runtime.go:644-672`) also queries via `r.env` without the per-run `OPENCODE_DB`, so reconcile reads the same wrong database as F1 whenever store scoping is used; worth flagging to the agentwrap surface review.