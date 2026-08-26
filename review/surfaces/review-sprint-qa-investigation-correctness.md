# Review: `sprint-qa-investigation` — Correctness lens

- Target: ultraplan-go @ `c6f01cf8ebfcfea19fe771dbf7552d838e3b8ca0` (frozen; verified ancestor-of-HEAD, tree exported read-only to `/tmp/opencode/rev-qa-sqi`)
- Workspace context: ultraplan-workspace @ `ab12dc38059c9bf485f9aced9075bcd7d924cac5`
- Lens: correctness (wrong outcomes, invariant violations, state inconsistencies, misleading success)
- Method: tests-first baseline (green), then five falsification repros written against the frozen tree in `/tmp/opencode/rev-qa-sqi/internal/sprint/zz_repro_sqi_test.go` (all reproduce on current code). Target and workspace repos untouched.
- Caveat: the context pack was built against `8eef70f` — 5 commits stale vs the assigned freeze (~2,276 added QA lines, incl. the entire v2 evidence/adjudication/report subsystem). Pack unknowns #4/#5/#7 were re-derived independently here; several are now confirmed defects.

## F1 — Canonical assessment ignores retained shard blockers and has no coverage-completeness gate (HIGH, high confidence)

Claim: RunQA derives its canonical assessment from a blocker-free, self-referential evidence set, so a run with uninvestigated shards publishes `phase=completed` plus a pass/fail assessment and a qa.md report as if coverage were complete.

Bad outcome: `qa.md` says "Accepted: N/N" and "QA evidence is current and complete"; flow-state QA pointer carries `assessment=pass`, while one or more mapped shards were never investigated. Because `countTerminalQAShards` counts Blocked as terminal, even the shard counters read full completion. A real defect in an uninvestigated shard is silently absent from the record that downstream gates consume.

Triggers (two independent paths):
1. Runtime-blocked shard: any per-shard failure (runtime exhaustion, invalid output after repair, turn limit, unowned check, drift) marks the shard Blocked without failing the batch; remaining completed shards produce passing evidence.
2. Focus mode: `qa start --shard X` runs 1/N shards; synthesis is phase-blind; evidence plans are created only for `Phase==Completed` shards, so a single-shard run yields a complete-looking evidence set.

Path:
- `internal/sprint/qa.go:537` — `DeriveQAAssessment(status.Review, records, adjudication, &status.Smoke, nil)` is the only caller and passes `nil` blockers although `SynthesizeQA` collects every retained shard blocker (`qa_synthesis.go:46-48`) precisely for this parameter.
- `internal/sprint/qa_adjudication.go:201-211` — completeness checks measure `len(adjudication.AcceptedIDs) != len(evidence)` against the plan set the caller itself constructed from completed shards only (`qa.go:477-480`); nothing compares evidence to map/shard coverage.
- `internal/sprint/qa.go:426-429` — phase stays Completed for `AssessmentPass`/`PassWithFindings`; final publish writes state+synthesis+report+flow pointer.
- Repro: `TestReproAssessmentIgnoresBlockedShardsWhenCallerDropsBlockers` — exact caller shape returns `pass`; the identical call with blockers returns non-pass, isolating the caller defect.

Controls/counter-evidence searched: `ValidateQAAdmission.MapComplete` only covers map-time blocked paths (`Coverage.BlockedPaths`), not runtime blocks; no post-batch "all shards terminal-completed" gate exists anywhere between `runQAShardBatch` and the final publish.

Severity: high (misleading success on the surface whose job is trustworthy verdicts). Confidence: high.
Regression test: end-to-end RunQA with one failing runtime + passing remainder must persist `CanonicalAssessment ∈ {blocked, incomplete}` (and focused runs must never yield `pass` with fewer accepted evidence records than primary shards).

## F2 — The only cataloged check punishes detection: gofmt drift blocks the requesting shard (HIGH, high confidence)

Claim: `go-format-diff` (`gofmt -d`) exits 1 when it finds drift (verified on Go 1.26: drift→1, clean→0). `RunApprovedQACheck` maps any runner error to `RuntimeUnavailable`, so successful detection of a formatting problem fails the whole shard.

Bad outcome: an investigator that follows the prompt's encouragement to request a product-owned check ("If an existing product-owned check is useful, request its ID") gets its shard Blocked and all of its theories discarded whenever the changed code is unformatted or has a syntax error. Additionally, this path attaches neither `Blocker` nor the attempt record to the persisted shard (repro logs `blocker=<nil> attempts=0`), so operators see an undiagnosed blocked shard.

Trigger/path: investigator output `check_requests:[go-format-diff]` → `qa_prompt.go:275-283` wraps DirectRunner's "process exited with code 1" as `QAErrorRuntimeUnavailable` → `qa.go:840-844` returns before theories are built → batch maps to Blocked.
Inconsistency: the evidence-producing path maps the same exit to `QAEvidenceFail`→issue candidate (`qa_investigation.go:90-95`, `qa.go:525-531`), i.e., finding-as-evidence there, finding-as-runtime-failure here.
Evidence: `TestReproFormatCheckDetectionBlocksRequestingShard` (real DirectRunner + drifted file). Existing test pins only ownership/drift-tampering with a fake runner that never returns exit errors (`qa_prompt_test.go:122-160`).

Counter-evidence searched: no code treats ExitError specially for catalog checks; no doc states nonzero-by-finding should block. Severity: high (the shipped check is net-harmful exactly when it works). Confidence: high.
Regression: runOneQAShard with a runner returning exit 1 for the descriptor must retain theories and record the command summary as a finding (or the check contract must define nonzero as Fail), and the blocked path must persist Blocker+attempt diagnostics.

## F3 — Plain `qa start` destroys retained terminal-shard evidence in place (MEDIUM-HIGH, high mechanism confidence)

Claim: a fresh (non-resume) start over unchanged inputs reuses the same semantic attempt directory and republishes every map shard (mapped phase, no theories) over terminal records, irrecoverably destroying prior LLM-produced theories before replacement work exists.

Bad outcome: operator reruns `qa start` (double-click, CI retry); if the second run later fails mid-batch, the previously completed investigation's theories/attempts are gone; recovery can only mark interrupted. Retention doctrine (RetainedAttempts=8, immutable maps) is defeated for shards.

Trigger/path: `prepareQAAttempt` fresh branch (`qa.go:603-609`) always republishes `Map+Shards+State`; shard writes are non-immutable overwrites keyed by deterministic IDs in the same dir (`qa_state.go:512-528`). Amplifier: the resume fallthrough also lands here when `LoadState` fails transiently (`qa.go:579-581`), converting a resume into a destructive full re-run. Repro: `TestReproFreshStartOverwritesCompletedShardEvidence`.
Counter-argument noted: "start = redo" is a defensible product choice; but redo under a content-addressed attempt identity makes the destruction silent and unrecoverable, which no doc claims. Severity: medium-high. Confidence: high on behavior, medium on contract violation.
Regression: fresh start over a completed current attempt must retain prior terminal shard bytes (new attempt dir, or refuse, or merge per resume semantics).

## F4 — Volatile git index/status hashes inside the byte-CAS'd map spurious-conflict legitimate restarts (MEDIUM, high confidence)

Claim: `BuildQAMap` embeds `GitIndex=hash(git diff --cached)` and `GitWorktree=hash(git status --short)` into map JSON (`qa_map.go:124,389-401`), but attempt/map IDs derive only from fingerprints+paths. Staging-area transitions change those hashes while `targetIdentity` (content-based, verify.go:349+) stays constant.

Bad outcome: edit → `qa start` completes → `git add` → `qa start` again ⇒ `Conflict: immutable QA map already exists with different bytes` (repro `TestReproVolatileGitFieldsConflictImmutableMap`). Recovery text is wrong for this cause ("Wait for the current owner or cancel it through run control"); no product path clears it (RecoverQA sees equal semantic ID ⇒ untouched; resume works but plain start stays wedged until the index is restored).
Severity: medium (permanent-looking failure with misleading guidance from a mundane git operation). Confidence: high.
Regression: two BuildQAMap outputs differing only in Target.GitIndex/GitWorktree must either share bytes (drop volatile fields from persisted form) or produce distinct identities.

## F5 — Focused run + proposed follow-ups blocks an otherwise-successful run (MEDIUM, high confidence)

Claim: with `FocusShard` set, batch #2 (follow-ups appended by synthesis) reapplies the focus filter; no pending shard matches ⇒ `InvalidState "focused shard is absent or already terminal"` ⇒ whole run terminalized as blocked via `terminalQAState`.

Bad outcome: the focused shard completed and persisted, then the run fails spuriously because synthesis proposed follow-up work (`qa.go:391-401` → `runQAShardBatch` filter `qa.go:629-636`). Cross-shard/inconclusive theories from the focused shard trigger it. Static path verification (no repro written; same code shape as tested focus behavior at `qa_test.go:171`).
Severity: medium. Confidence: high on mechanism.
Regression: focused runs must skip follow-up batches (or run them unfocused) instead of failing.

## F6 — RecoverQA persists Stale on transient failures (LOW-MEDIUM, high confidence)

Claim: any `QAMap` error during recover — including unreadable files, git lock contention (`PrepareReview`/`qaGitIdentity` subprocesses) — permanently flips a matching, possibly completed state to `Stale` via `SaveRecoveredState` (`qa.go:228-234`). Conflates input staleness with unavailability; next action tells the operator to remap although nothing changed. Context-pack unknown #5 confirmed as shipped behavior.
Severity: low-medium. Confidence: high on mechanics.
Regression: recover must distinguish error classes; only fingerprint/ID mismatch may persist Stale.

## F7 — Follow-up shards bypass lowered TotalShards/PendingEntries budgets (LOW, high confidence)

Claim: `SynthesizeQA` caps follow-ups only by `FollowUpShards` (`qa_synthesis.go:120-121`); neither `ValidateQAState` nor Publish compares grown totals against configured bounds (repro `TestReproFollowUpShardsExceedTotalShardsBudget`: map 3 ≤ TotalShards 3, final total 4). Violates the lower-only budget contract for operator-lowered configs; unreachable at defaults (32+1+4 < 44).
Severity: low. Confidence: high.
Regression: publish/state validation must enforce CompletedShards/TotalShards ≤ budgets.TotalShards/PendingEntries.

## F8 — Evaluator-flipped evidence carries unverifiable provenance digests (LOW, high confidence)

Claim: `evaluateFailedEvidence` binds evaluator observations to `fingerprintQAValue(record)` computed while `Outcome=fail` (`qa_evaluator.go:19`); the published record is then mutated to pass/fail-final (`qa.go:512-520`), so adjudication `Evaluators[].EvidenceDigest` refers to bytes that exist nowhere on disk. Audit cannot recompute what evaluators judged; digest-pinning discipline used everywhere else is broken for this field.
Severity: low (auditability, not safety). Confidence: high.
Regression: flip must be represented in the record (e.g., evaluation event with its own digest) so published artifacts can verify evaluator bindings.

## Defended / non-issues

1. **Evaluator fail→pass majority flip**: deliberate and pinned by `TestQAFailedShardRequiresThreeFreshCompleteEvaluators`; frozen plan conditions make a faithful evaluator answer fail, and a single rogue vote cannot flip (2/3 needed). Risk acknowledged rather than defect; see F8 for its provenance gap.
2. **Publish with zero-value Flow hard-fails**: SaveFlowState backfills pointers only, not schema/stages. All production callers pass a loaded FlowState (RunQA, prepareQAAttempt, publishTerminalQAFailure); latent API guard, fail-closed, not reachable.
3. **Smoke-suite path in RunQA**: bypasses the QA store/fence but `RunSmoke` takes its own mutation lease and persists its own state; ephemeral QAState is projection-only.
4. **Writer fence**: fail-closed when unset (`checkWriter`, qa_state.go:610-612); heartbeat uses `WithoutCancel`+30s so terminal publication survives cancellation — intended; token compared field-for-field including fencing generation.
5. **Publication order & rollback**: map(CAS)→shards→synthesis→evidence→state-last→flow-pointer-last holds; canonical-file rollback snapshots restore state.json/qa.md/flow-state.json on late failure (pinned by tests). Leftover immutable plan/evidence files after rollback are harmless garbage removed by retention.
6. **Resume determinism across interrupted follow-up batches**: follow-up IDs are parent-theory-derived, so re-synthesis after crash reproduces IDs; disk-terminal follow-ups are skipped, queued ones overwritten consistently.
7. **Strict load grammar** (0600-exact regular files, per-component Lstat symlink rejection, version gates, DisallowUnknownFields, trailing-value rejection, digest-pinned artifact refs): pinned by tests and re-verified by reading.
8. **Latent, currently unreachable**: `state.RegressionCandidates++` accumulates across republications where sibling counts are assigned (`qa_state.go:759-764`); masked today because evidence republication for the same attempt conflicts first (immutable records embed wall-clock timestamps). Becomes live if that conflict is ever relaxed.
9. **`--suite smoke` argument matrix**, exit-class mapping (canceled/deadline→8, runtime/persistence→6, other QA→5), CancelQA run-ownership validation, boot reconcile lease discipline, and bounded progress emission: verified consistent with cli-reference contract.

## Verification artifacts

- Frozen-tree scratch copy + repro suite: `/tmp/opencode/rev-qa-sqi` (commit `c6f01cf…`; `zz_repro_sqi_test.go`, 5 tests, all PASS against current code and designed to fail once each defect is fixed).
- Baseline: `go test ./internal/sprint/ ./internal/app/ -count=1` green before repros; green with repros included.
- Target and workspace repos verified unmodified.
