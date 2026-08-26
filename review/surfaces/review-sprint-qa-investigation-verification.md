# Surface Review: `sprint-qa-investigation` — verification/operability lens

Reviewer: independent verification-lens review (no subagents). Target: ultraplan-go @ `c6f01cf8ebfcfea19fe771dbf7552d838e3b8ca0` (job-frozen bytes; exported to scratch, target repo untouched — working tree has drifted to `22c94f3` with two post-freeze QA commits noted below as HISTORY corroboration only). Workspace context @ `ab12dc38059c9bf485f9aced9075bcd7d924cac5`. Lens: missing/misleading tests, fake-only confidence, observability gaps, error truth, performance/resource hazards, bugs current verification allows.

Baseline: `go test ./internal/sprint/ -run 'TestQA|TestQASynthesis|TestQAMap'` green on frozen bytes before repro work.

---

## F1 — Re-synthesis after follow-up execution proposes shards that were never run; hydrate then fails the entire completed attempt

- **Category**: correctness / reliability of primary path. **Severity P1** (systematic loss of a fully-funded attempt; recurring cost trap). **Confidence: confirmed** (executed against frozen bytes).
- **Claim**: At `c6f01cf`, if any follow-up shard's investigator returns a theory with outcome `inconclusive` or `cross_shard`, the second `SynthesizeQA` derives new follow-up shard proposals that were never executed, and `hydrateQASynthesisFollowUps` rejects them, so `RunQA` publishes a **Blocked** attempt after every shard completed and the whole budget was spent.
- **Trigger**: any follow-up-shard theory outcome ∈ {`inconclusive`, `cross_shard`} — outcomes the product prompt itself invites ("Outcomes are … inconclusive …"; `InconclusiveCondition` is a mandatory field). QA exists precisely for hard-to-settle changes, so this is a mainstream case, not an edge.
- **Execution path**: `RunQA` (internal/sprint/qa.go:387-410): synthesize #1 over primaries → append **all** `synthesis.FollowUpShards` → batch #2 runs them to terminal → re-synthesize over primaries+follow-ups (qa.go:403). `SynthesizeQAWithChallenges` collects follow candidates from **every** theory with those outcomes including theories produced inside follow-up shards (qa_synthesis.go:99-136), so proposal set ⊃ already-run F-shards ∪ new G-shards. `hydrateQASynthesisFollowUps` (qa.go:562-575) requires every proposed ID to exist among executed shards → G absent ⇒ `InvalidState` "a proposed follow-up shard did not reach a retained terminal state" ⇒ `publishTerminalQAFailure` ⇒ attempt Blocked.
- **Repro** (scratch tree, package tests, frozen code only): fixture primaries produce cross_shard/inconclusive theories; simulated batch #2 marks each proposed follow-up Completed with one `inconclusive` theory; second synthesis proposed **4 follow-ups, 2 never executed**; `hydrateQASynthesisFollowUps` returned the rejection above. Repro file: `/tmp/opencode/qa-freeze/internal/sprint/qa_followup_repro_test.go` (`TestQAReproFollowUpResynthesisProposesUnrunShard`, PASS).
- **Observable bad outcome**: exit-class 5 failure, flow pointer Blocked, misleading blocker text; operator resumes per blocker guidance and pays again: resume reloads only `qaMap.Shards` IDs (qa.go:582-588), so persisted follow-up results are ignored and batch #2 re-runs every follow-up from scratch; if investigators again report `inconclusive`, the trap repeats deterministically-shaped.
- **Existing controls / counter-evidence**: budget cap sorts candidates by theory ID and truncates to `FollowUpShards` (default 4) — caps damage but does not prevent unrun proposals; single-batch shape means no infinite loop at freeze; no test covers a follow-up shard returning `inconclusive` (synthesis tests only hydrate retained terminals).
- **HISTORY corroboration**: post-freeze commit `4841b74` "fix bounded QA follow-up synthesis" replaces exactly this sequence with a pending-proposal loop plus `finalizeQASynthesisFollowUps` that replaces proposals with retained terminals.
- **Fix direction**: iterate pending proposals until none (as HEAD does), or exclude follow-up-shard theories from new candidate derivation, or treat unseen proposals as advisory records rather than hydration failures. **Regression test**: RunQA-level test whose follow-up runtime returns `inconclusive`; expect phase Completed (or documented bounded drop), never Blocked-after-full-execution.

## F2 — Map fence is never installed outside tests: every shard pays 3 full governed-map rebuilds + 5 full-tree identity hashes, invisible to the suite

- **Category**: performance/resource hazard + fake-only test confidence. **Severity P2**. **Confidence: high** (wiring proven by grep; cost arithmetic from source).
- **Claim**: production entrypoints install only the writer fence (`app/sprint_commands.go:440`, `app/operation_runner.go:123`; builder chain `app/sprint_commands.go:779` has no `WithQAMapFence`). With `s.qaMapFence == nil`, `validateCurrentQAMap` falls back to a **full deterministic rebuild** (`s.QAMap`) on every check (internal/sprint/qa.go:886-901). `runOneQAShard` calls it three times per shard (before start :727, after run :769, before accept :862), plus two direct `targetIdentity` passes (:746, :756). Each rebuild internally performs `PrepareReview` (hashes all governed inputs), `LoadFlowState`, `targetIdentity` (hashes **every** regular file ≤64 MiB in the target; verify.go:349-439), `qaGitIdentity` (3 git subprocesses; qa_map.go:389-401) and the check catalog build. Net ≈ 5 whole-tree hashing passes + ~18 git spawns **per shard**, ×(44+4) shards ÷ concurrency, all charged to the 60-minute `RunTimeout`.
- **Bad outcome**: on realistically sized targets, validation overhead alone can consume minutes-to-tens-of-minutes per attempt and starve the wall-clock budget while investigators idle; page-cache/git churn scales with target size, not sprint size.
- **Why tests don't see it**: every qa_test.go scenario installs `WithQAMapFence(func(QAMap) error { return nil })` (e.g., qa_test.go:108,145,166,291); grep confirms zero non-test `WithQAMapFence` callers. The shipped code path (fallback rebuild) is never executed by the suite — fake-only confidence, and `service.go:173-178` documents the fence as the intended production mechanism that nobody wires.
- **Counter-evidence**: correctness is unaffected (rebuild-compare equals fence semantics); default-budget runs on small targets complete. Hence P2, not P1.
- **Fix direction**: close the map fence over the accepted map ID at both CLI and TUI/web wiring (cheap comparison), or memoize rebuild inputs. **Regression test**: production-wiring test asserting `qaMapFence != nil` after CLI/TUI service construction; or a no-fence shard test asserting QAMap invocations ≤1 when inputs unchanged.

## F3 — Evidence-producing window has no target-identity guard: fingerprints can be recorded for evidence produced against a changed tree

- **Category**: verification integrity / error truth. **Severity P2**. **Confidence: high** (absence proven; successor commit corroborates).
- **Claim**: default `qa` runs are evidence-producing (`EvidenceProducing: qaCommand.Suite == ""`, app/sprint_commands.go:441). The last per-shard drift check happens right after each investigator run; `buildQAEvidencePublication` (qa.go:442-552) then freezes plans carrying `GovernedInputFingerprint`/`ImplementationFingerprint`/`MapFingerprint`, copies the target into isolated writable copies, runs approved checks, adjudicates, and renders the canonical report — **without any before/after `targetIdentity` comparison of its own**. Drift between the final shard and evidence publication yields issue candidates, assessment, and `qa.md` asserted under fingerprints that no longer describe the checked tree.
- **Trust transition**: fingerprint-pinned evidence claims → adjudication/promotion inputs; a stale claim here is silently trusted downstream.
- **HISTORY corroboration**: post-freeze `208c9d0` adds exactly these guards (before-admission equality with the map fingerprint; after-report equality with before) and fixes the local shadowing of the identifier `targetIdentity` by `pprocess.IdentifyTree` result at qa.go:459 (freeze), which also made an in-function check impossible to express.
- **Existing controls**: per-shard pre/post identity checks bound the window but do not cover it; admission checks review/smoke freshness, not live implementation identity.
- **Fix direction**: backport the `208c9d0` guards. **Regression test**: mutate target between batch completion and evidence build; expect `StaleInput`, no canonical report written.

## F4 — Unleased `Status` refresh read-modify-writes flow-state.json and can revert the leased QA pointer projection

- **Category**: operability/data integrity of the authoritative pointer. **Severity P2**. **Confidence: high mechanism** (code-path proven; interleaving not executed).
- **Claim**: `Service.Status` persists a refreshed flow-state by default (`statusWrites` true; internal/sprint/service.go:291-295) **without acquiring the sprint mutation lease**, preserving `.QA` from its own load time (service.go:268). During a QA run — which holds the lease for up to 90 minutes and republishes the pointer on every shard publication — any concurrent status refresh that loaded flow-state before a QA publish will afterwards save its copy with the **older** pointer (phase/digest), reverting the authoritative projection. `SaveFlowState` is plain atomic-rename, last-writer-wins; nil-backfill (state.go:203-213) heals only the nil case, not the stale-non-nil case.
- **Reachability**: dashboards routinely trigger writes-enabled status during runs: TUI "Sprint Status" nav (internal/tui/model.go:466) and web dispatch (internal/web/operation_handlers.go:628) → `OperationSprintStatus` → `ss.Status(...)` (internal/app/operations.go:439,475,493); sprint listing calls `Status` per sprint (internal/app/sprint_usecases.go:484, `RefreshMayWrite`). The refresh window is wide (`PrepareReview` + artifact reads sit between load and save, service.go:270-289).
- **Bad outcome**: flow-state.json (and the DB-mirrored FlowState header) shows stale `phase`/`StateDigest`/`Fresh` for the QA stage after later publishes; TUI QA card, web run view, and "View QA durable run" navigation act on the stale record until the next QA write or an explicit `qa recover`. Divergence is persistent when it lands on the final publication.
- **Existing controls**: mutation lease serializes *stage* writers, not `Status`; digest mismatch is not validated across pointer/state by consumers.
- **Fix direction**: make `Status` honor the lease or use `WithoutStatusWrites` semantics for refresh-on-read paths, or re-load-and-compare pointer immediately before save. **Regression test**: interleave Load→QA-publish→Status-save; assert final `.QA.StateDigest` equals the latest published digest.

## F5 — Terminal-failure publication drops an already-computed synthesis reference

- **Category**: error-truth/resume efficiency. **Severity P3**. **Confidence: confirmed** (code path).
- **Claim**: `publishTerminalQAFailure` publishes State only (qa.go:554-560). Its call sites at qa.go:409 (hydrate failure) and qa.go:421 (evidence failure) occur **after** a valid synthesis existed. The published blocked state therefore carries `Synthesis == nil` even though every shard record and a valid synthesis were computed; `qa status` reports a blocked attempt without synthesis, and resume redoes follow-up work.
- **Counter-evidence**: pre-synthesis failure sites legitimately have nothing to persist; shard files themselves survive (published per-result).
- **HISTORY corroboration**: post-freeze diff adds `publishTerminalQAFailureWithSynthesis` used at exactly these sites.
- **Fix direction**: persist the synthesis reference on late-terminal failures. **Regression test**: force evidence-phase failure; assert `loaded.Synthesis != nil` and synthesis.json loads.

## F6 — Planted symlink in the verification tree is reported as persistence_failure instead of invalid_state

- **Category**: error truth / exit-class accuracy. **Severity P3**. **Confidence: confirmed**.
- **Claim**: `VerificationBytes`'s walk callback returns `QAErrorInvalidState` "QA state contains a symbolic link" (qa_state.go:63-65), but the outer wrapper reclassifies any walk error as `QAErrorPersistenceFailure` "cannot measure retained QA state" (qa_state.go:74-76), because `errors.Is(inner, fs.ErrNotExist)` is false. A tamper-shaped condition (symlink planted among retained files — reachable precisely because WalkDir covers unknown entries) surfaces as exit class 6 "runtime/persistence" with recovery advice "Restore reliable workspace persistence", misdirecting the operator; validation-class handling (exit 5, inspect state) would be truthful.
- **Fix direction**: preserve inner category (`AsQAError`) when wrapping walk errors. **Regression test**: plant a symlink file in `verification/attempts/<id>/`; assert category `invalid_state`.

## F7 — Progress cap retains the oldest events and silently discards the rest, including the completion event, under documented budgets

- **Category**: observability. **Severity P3**. **Confidence: confirmed** (behavior pinned by the project's own test).
- **Claim**: `boundedQAProgress` stops forwarding after `RecentProgress` events total, keeping the **first** N (qa.go:1026-1041; pinned by TestQATerminalFailurePublicationAndProgressBound, qa_test.go:193-200). The name (and the config table's framing) implies recency. Default runs emit ≈48 progress events, but evidence-producing runs add ~2 events per completed shard, so ≥26 completed shards push past 100 and the CLI stderr stream (`[qa] …`, app/sprint_commands.go:441-447) and TUI/web event feed go silent mid-run; `investigation_complete` is dropped. Work is unaffected; only the human-facing stream dies while the run continues for potentially tens of minutes.
- **Counter-evidence**: callers learn the outcome from the RunQA return value regardless; budgets may lower `RecentProgress` further, making silence intentional-looking — but nothing documents keep-oldest semantics.
- **Fix direction**: ring-buffer (keep latest) or raise/drop-the-cap semantics; rename the budget honestly. **Regression test**: property test asserting a terminal event is observable whenever the stream is capped.

## F8 — `qa resume` with a mismatched prior attempt silently becomes a fresh full attempt

- **Category**: error truth / operator expectation. **Severity P3**. **Confidence: confirmed**.
- **Claim**: `prepareQAAttempt`'s resume branch requires `prior.CurrentAttemptID == qaMap.SemanticAttemptID && prior.Map != nil` (qa.go:579-586); otherwise it falls through to the fresh path — new map publication, all shards reset to Mapped, full re-investigation — with no signal distinguishing "resumed" from "started fresh". Docs frame resume as continuing current valid shards with a new owner (docs/cli-reference.md:357-358). After any input change (the common reason a prior attempt stopped), an operator running `resume` pays the full spend again believing partial work was reused.
- **Counter-evidence**: reuse across differing semantic attempts would be unsound; retention protects history; nothing is corrupted.
- **Fix direction**: explicit `StaleInput`/projection flag when Resume was requested but the attempt differs. **Regression test**: resume after fingerprint change; assert distinguishable result/error.

## F9 — `RecoverQA` permanently marks Stale on transient map/preparation errors

- **Category**: error truth. **Severity P3**. **Confidence: medium**.
- **Claim**: RecoverQA treats **any** `s.QAMap` error identically to a genuine attempt-ID mismatch (qa.go:228-234): a healthy terminal record is flipped to `Stale`, freshness reasons claim "governed QA inputs no longer match", and the pointer is republished. Transient causes (git lock contention, momentarily unreadable governed input, PrepareReview hiccup) thus downgrade a completed sprint's QA verdict, and only a fresh dry-run+attempt restores currency.
- **Existing controls**: fail-closed direction; recover is idempotent and self-consistent once inputs are readable again (pointer stays stale rather than lying).
- **Fix direction**: distinguish map-build unavailability (leave untouched, surface error) from real identity mismatch (mark stale). **Regression test**: inject failing QAMap dependency; assert recover neither mutates phase nor reasons.

---

## Hygiene notes (not defects)

- Dead test scaffolding: `qaRetryRuntime`/`qaOutputRetryRuntime` (qa_test.go:27-73) have zero users at the freeze — leftovers of the removed product-side retry loop; `attempt.Retryable` (classifyQARuntimeFailure) is display-only (app/sprint_usecases.go:1146) and docs promise no runtime retries (cli-reference.md budget table lists only `output_repair_attempts`) — no contract breach, but the scaffolding implies untested behavior exists.
- `PruneAttempts` invalid-entry hard-fail branch (qa_state.go:103-110) has no dedicated test.
- No test drives `RunQA` end-to-end through the durable acceptance boundary with the real writer fence; CLI/TUI coverage is use-case/fake level (context pack gap confirmed).

## Defended non-issues (hypotheses refuted)

1. **Heartbeat-during-cancellation kills the cancelled-state publication** — refuted. `Heartbeat` fails only on terminal outcomes (`outcome IS NULL` / `terminal_outcome IS NULL` guards, internal/runcontrol/lifecycle.go:38-55); `AcknowledgeCancellation` keeps the run non-terminal (lifecycle.go:171-181), and `FinishOperation` proposes the terminal only after `RunQA` returns (durable_operations.go ordering). Cancelled/interrupted states publish successfully through the same fence; the passing cancellation test's always-succeed fence is therefore representative, not masking.
2. **chmod walk escapes into shared sprint directories** — refuted. The loop breaks at the `verification` component (qa_state.go:858-871); `projects/<p>/sprints/<s>` modes are never touched; normalization to 0700 applies only inside the product-owned private tree.
3. **Publish-vs-load synthesis budget lens difference** — unreachable for product-built syntheses (built and validated under map budgets; `ValidateQASynthesis` at Maximum is looser); tampered records fail closed at load.
4. **Resume adopting tampered-but-valid shard JSON** — requires write access to the 0700 private tree (same trust domain as the product); state-referenced artifacts are digest-pinned; not a trust-boundary crossing.
5. **Smoke-suite QA branch bypasses durable QA state** — matches contract: smoke authority remains smoke.md/flow smoke record (cli-reference.md:542-546); the in-memory state is presentation only.

## Verification summary

Findings: 9 (1×P1 confirmed-by-execution, 3×P2, 5×P3). Highest-value actions: fix the follow-up re-synthesis sequence (F1, regression test included above), wire or remove the map-fence abstraction and measure the no-fence path (F2), and close the evidence-window identity gap (F3). All line references are to frozen `c6f01cf` bytes.
