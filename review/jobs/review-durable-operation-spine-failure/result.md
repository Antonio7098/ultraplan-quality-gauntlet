Now let me examine the adapters (web/TUI) and the SQLite open defaults, plus the remaining tests.The TUI swaps `opctx` for `accepted.Context` but keeps `m.cancel = cancel` from the original context. Let me trace how cancellation is invoked in the TUI.No escape hatch exists for aliases. Now let me verify the two strongest hypotheses with executable reproductions. First, the repository interface shape:The spy never cancels the parent — fixing and rerunning with a bounded test timeout:Build is warm now. Running the repro tests in the background:Repro 1 confirmed. Now repro 2 (manager-tier silent tick failure):Both repros confirmed. Cleaning up the repro file and closing out the QA-handoff seam checks:All verification complete; target tree left untouched (`git status` clean). Final report:

---

# Findings: `durable-operation-spine` — failure/concurrency review

## F1 — Trailing omission flush runs on the already-cancelled `runCtx`, misclassifying user cancellation/timeout as `persistence_degraded` and replacing the runtime's error

**Severity:** Medium · **Confidence:** High (reproduced)

**Claim.** In `controlledRuntime.StartRun` (internal/app/run_control.go:303-316), after `base.StartRun` returns, any pending coalesced-progress count is flushed with `appendRunEventWithRetry(runCtx, …)` (:305). When the run ends because the parent context was cancelled or its deadline expired, `runCtx` is already dead, so the flush always fails with `context.Canceled`. That failure is assigned to `persistenceErr` (:313), which (a) skips the normal `terminalOutcome` arbitration (:334) and proposes `TerminalPersistenceLost`/"durable event persistence failed" on the detached context (:322-327), and (b) **replaces** the original `runErr` in the returned error (:331).

**Observable bad outcome (repro).** Spy runtime emits two identical progress events (<250 ms apart → second coalesced), then returns `ctx.Err()` on parent cancellation. Result: durable terminal = `persistence_degraded` ("durable event persistence failed") instead of `cancelled`; `StartRun` returns `persist progress omission: context canceled` instead of the runtime result/error pair. The SSE/UI layer classifies the same joined error via `errors.Is(…, context.Canceled)` (internal/web/operations.go:288) as **cancelled** — so the live document and the durable `/runs/<id>` record disagree for the same operation. Timeouts misclassify identically (`timed_out` → `persistence_degraded`). Routine cancellations during chatty provider streams (duplicate part/status updates are common) land here.

**Path.** parent ctx done → `base.StartRun` returns `context.Canceled` → flush Append hits canceled `BeginTx` (non-retryable) → `persistenceErr` set → persistence_lost branch → wrong terminal + masked error.

**Controls/counter-evidence checked.** Manager-tier finish flushes under a detached 30 s caller ctx (durable_operations.go:78-83) — correct pattern, showing the runtime tier's use of `runCtx` is the outlier. No test covers this path (pack §10 gaps agree). Not fixed by callers: they cannot distinguish fabricated persistence errors from real ones because the wrap chain contains `context.Canceled`.

**Fix/regression.** Flush trailing omissions on `context.WithoutCancel(ctx)` (or a detached ≤5 s ctx) like every other end-of-run write; add a regression test: duplicate-progress spy + cancelled parent ⇒ `snapshot.Terminal.Outcome == cancelled` and returned error preserves the runtime's cancellation error.

## F2 — Manager-tier control-loop failures are silently swallowed and terminalize as ordinary *user* cancellations (no diagnostic, no degraded marker); mid-run append loss can even end `succeeded`

**Severity:** Medium · **Confidence:** High (reproduced)

**Claim.** In `controlOperation` (internal/app/durable_operations.go:235-261), a single failed `Snapshot`, `AcknowledgeCancellation`, `Heartbeat`, or `Reconcile` call (none of which get the 5 s retry that appends get) does `owned.cancel(); return` — no error surfaced to any caller, no diagnostic event, no `persistence_degraded` proposal. The only `TerminalPersistenceLost` in the manager tier is the start-append path (:126). After such a silent kill, `FinishOperation` maps the resulting `context.Canceled` to plain `cancelled`/"operation cancelled" (:291-292).

**Observable bad outcome (repro).** Repository wrapper fails exactly one `Snapshot` poll after accept: operation dies within ~1 s, durable journal = `[lifecycle, terminal]` — zero warning/diagnostic events, terminal reads as a deliberate user cancellation. An infra outage is indistinguishable from a user pressing cancel, violating the documented spine invariant ("mid-run … failure … ends in a `persistence_degraded` terminal", surface pack §5/§7) and TRD L2226 "safe behavior when acceptance, mid-run append, heartbeat, or terminal persistence fails". Aggravators, by direct code reading: (a) after `RecordOperationEvent`'s append failure cancels the ctx (:213-215), nothing poisons the operation — subsequent appends on fresh `Background` contexts can still succeed, yielding a mixed committed/dropped journal with no marker; if product work completes without re-checking ctx, `FinishOperation(nil)` records **succeeded** over a severed event stream; (b) a first-tick failure also permanently disables cancellation observation (loop exits), so later `RequestCancellation`s are never acked; (c) a failed omission-flush at finish converts a fully successful run into `failed` + CLI `ExitRuntime` exit (:277-279, :81). Contrast: the inner runtime tier records and terminalizes these same failures explicitly (run_control.go:264-298, :322-331) — the asymmetry is unpinned by any test.

**Fix/regression.** On tick/append failure in the manager tier, propose `TerminalPersistenceLost` on a detached ctx (mirroring :121-129/:322-331) and/or append a warning before cancelling; pin with a fault-injected repository test asserting the terminal outcome is not bare `cancelled`.

## F3 — Deterministic TUI digests make retry of a failed/cancelled operation impossible until governed inputs change; aliases have no recovery/force path

**Severity:** Medium-Low · **Confidence:** High on behavior, Medium that it's unintended

**Claim.** TUI digest = sha256(CanonicalRequest + NUL + InputFingerprint) (internal/tui/app.go:236-238); canonical JSON and fingerprint are deterministic functions of the normalized request plus governed planning files (operations.go:360-372, :663-697). On resubmit, `Accept` hits the unique alias (run-control.db `operation_aliases`, sqlite.go:306-314) → `ErrConflict` → `Existing:true` with the old lifecycle (durable_operations.go:106-112) → TUI shows "matching durable operation already exists" and refuses to run (tui/app.go:243-249). There is no override: the `recovery_code`/`recovery_guidance` columns are never written or read anywhere; aliases die only with retention deletion of the run (~days at defaults).

**Observable bad outcome.** A TUI operation that failed on a transient provider outage — or was cancelled before mutating anything — leaves governed inputs unchanged; every re-preparation reproduces the identical digest, and the user is hard-blocked from retrying the same logical operation (including across TUI restarts) while CLI (empty digest, durable_operations.go:55) and web (per-token digests, security.go-derived key) allow immediate retries of the identical request. A claim-failure row (accepted-unclaimed until the 45 s reconciler grace) poisons the alias the same way. Trigger requires no attacker, just a failed run + retry — squarely in the retry/idempotency lens.

**Counter-evidence checked.** The dedup itself is clearly intentional double-submit protection; but blocking post-terminal retries conflicts with TRD's failure-matrix framing (completion/cancellation races must resolve without bricking the logical operation) and has no documented escape hatch. Adapters project `Existing` read-only (web refresh path, TUI static card) — safe otherwise.

**Fix/regression.** Scope acceptance dedup to non-terminal lifecycles (resolve alias; if terminal, accept a fresh run correlated to the old one), or thread an explicit force/retry flag into the TUI digest; pin with an accept→finish→re-accept-same-digest test asserting a new RunID.

## F4 — `"sk-"` substring matching wholesale-redacts innocuous tool IO containing ubiquitous `task-*`/`risk-*` identifiers, at two layers

**Severity:** Low · **Confidence:** High

**Claim.** Both redaction gates treat any string merely *containing* `sk-` as secret-bearing: `redactObservableValue` replaces the entire tool argument/result/error value with `[REDACTED]` (run_control.go:704-711), and storage-side `unsafeEventValue` drops the whole value into an omission (sanitize.go:98-102, consumed at sanitize.go:36-44). In a domain whose identifiers are literally `task-42`, `risk-based`, `desk-…`, nearly every tool observation touching a task reference triggers this (`strings.Contains("task-42", "sk-")` → true).

**Observable bad outcome.** Tool-call journals — the core observability product of this spine — routinely degrade to `[REDACTED]`/omitted for non-secret content, defeating debugging while training users to ignore redaction markers (alarm fatigue). Over-redaction is fail-closed by design intent, but the marker granularity (whole value vs. masked span) plus a match pattern that collides with primary domain vocabulary is a concrete, permanent data-loss defect in the committed journal, not a hypothetical.

**Fix/regression.** Anchor the token pattern (word-boundary/secret-prefix forms like `sk-[A-Za-z0-9]{20,}`) and mask matched spans instead of discarding whole values; unit-test that `tool_arguments={"task":"task-7"}` persists visibly.

---

## Defended non-issues (checked, not reported)

- **Alias-conflict `Existing` context lacks `ParentRun`/QA ownership** (durable_operations.go:111): all three adapters treat it as terminal/read-only (TUI card, web doc+RefreshPath); QA runner fails closed via `qaOwnershipFromContext` if anyone did execute with it.
- **QA writer-token handoff**: struct-equality fence + generation monotonicity, `WithoutCancel` heartbeat bounded at 30 s, `checkWriter` gating every publish — sound; stale-generation rejection test-pinned.
- **Coalescing count preservation**: omissions accumulate across warning/finish boundaries under `eventMu`; wall-clock vs event-time clock divergence between the two coalescers only ever *under*-coalesces (never loses counts).
- **Commit-before-delivery**: enforced in both tiers and pinned by `TestControlledRuntimeAcceptsClaimsAndCommitsBeforeDelivery`.
- **Single-winner terminal CAS**: `ProposeTerminal` guarded by `current_attempt_id + terminal_outcome IS NULL`; losers get `won=false` without error; post-terminal appends correctly rejected immutable.
- **Heartbeat cadence vs lease**: ≤~6 s effective heartbeat interval vs 15 s lease absorbs several busy-timeout stalls; expired-but-alive owners are only marked `stalled`, ownership intact.
- **Web degrade mode / hub dedup map bounds**: designed fallback with `ErrWebUnavailable` handling; dedup entries reaped with records (10 min terminal retention).
- **Zero-value retention policy**: `normalizeRetentionPolicy` substitutes defaults; config parse errors cannot produce a quota-zero store.
- **Orphaned TUI `m.cancel`** after adopting `accepted.Context`: harmless — durable cancellation routes through `CancelRun`/`RequestCancellation` when `ActiveRunID != ""`.
- **Per-instance `m.owned` scoping**: unknown-ID finish is a safe nil-op (degraded-mode relies on it); abandonment handled by lease expiry + reconciliation by design.

Verification artifacts: both repros were run against the frozen target (`5d54525`) as throwaway files since deleted; `git status` clean in both target and workspace.