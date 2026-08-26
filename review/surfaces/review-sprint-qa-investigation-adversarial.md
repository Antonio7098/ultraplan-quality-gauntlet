# Surface review: `sprint-qa-investigation` — fresh-context adversarial

Target: ultraplan-go @ `c6f01cf8ebfcfea19fe771dbf7552d838e3b8ca0` (verified via detached scratch worktree; original repo untouched, worktree metadata removed afterwards).
Planning context: ultraplan-workspace @ `ab12dc38059c9bf485f9aced9075bcd7d924cac5`.
Baseline: `go test ./internal/sprint -run TestQA` green at the frozen tree before probing. All probes were temporary untracked test files, run, then deleted.

Stale-context note: the context pack was built against commit `8eef70f…`, which predates Sprint 37. At the frozen commit the surface has grown substantially: `RunQA` gained a smoke-suite selector (`Suite=="smoke"`), an **evidence-producing phase that is enabled by default** for every non-smoke start/resume (`EvidenceProducing: qaCommand.Suite == ""`, sprint_commands.go:441 and operation_runner.go:124), plus new modules `qa_evidence.go`, `qa_investigation.go`, `qa_adjudication.go`, `qa_evaluator.go`, `qa_report.go`, `qa_runtime_validation.go`. Findings below were checked against the frozen sources, not the pack.

---

## F1 — Evidence publication is non-idempotent under its own deterministic identity: any re-run or crash-retry of evidence QA on unchanged inputs deterministically fails with Conflict (High severity / High confidence)

**Claim.** Evidence artifacts are written **immutable** at fixed, ID-derived paths, but their serialized bytes embed wall-clock timestamps while the IDs that determine those paths deliberately exclude time. The second publish of a regenerated bundle for the same semantic attempt therefore always violates the byte-CAS.

**Mechanism.**
- `FreezeQAEvidencePlan` derives the plan ID from kind/theories/expectations/conditions/paths/executable/args/env/timeouts/limits/analyzer-count/three fingerprints — **not** `FrozenAt` (qa_evidence.go:149-162) — yet sets `FrozenAt = now.UTC()` into the persisted struct (qa_evidence.go:148).
- `store.writeRecord(..., immutable=true)` compares full JSON bytes of any existing file and fails with `QAErrorConflict "immutable QA map already exists with different bytes"` on any difference (qa_state.go:633-643; same for `writeBytes`, qa_state.go:808-818).
- `publishEvidence` writes plans (qa_state.go:675), then fixed-path immutable `adjudication.json` (bytes include `CompletedAt` and per-run evidence IDs, qa_state.go:738), `issues.json` (fixed path, qa_state.go:750-754) and `assessment.json` (bytes include `CompletedAt` and per-run accepted IDs, qa_state.go:777). Evidence record IDs additionally mix in the random `os.MkdirTemp` workspace path (qa.go:467, qa_investigation.go:53,126-131), so records differ per run by construction.
- The evidence phase is default-on: every plain `qa start`/`qa resume` and every TUI/web QA operation reaches this code (sprint_commands.go:441, operation_runner.go:124).

**Trigger / observable bad outcome.**
1. Run `qa <p> <s>` successfully once (plans/, adjudication.json, issues.json, assessment.json, qa.md written). Re-run `qa <p> <s>` with **zero input changes** (identical git identity ⇒ identical semantic attempt ID): the entire investigation re-executes, then the final `Publish` fails deterministically on the first plan write. State is rolled back to this run's `synthesizing` snapshot (qa_state.go:482-493), flow pointer follows, and the operator sees `conflict` with recovery guidance *"Wait for the current owner or cancel it through run control"* (qa_types.go:912-913) — wrong: there is no competing owner.
2. Worse: a crash or injected failure **during** `publishEvidence` leaves some plans on disk; every subsequent `qa resume` of the same attempt regenerates plans with fresh `FrozenAt` and hits the same Conflict — the attempt is wedged until the governed inputs change (which mints a new attempt ID). Recovery, the mechanism designed for exactly this, cannot complete.

**Probe (executed against frozen tree).** Two `FreezeQAEvidencePlan` calls differing only in `now` produced identical plan IDs; two successive `QAStore.Publish` calls with regenerated bundles (timestamps 1000s vs 2000s, otherwise identical semantic content) → first succeeded, second returned `AsQAError` category `QAErrorConflict`. Probe file removed after the run.

**Counter-evidence searched.** No caller deletes or versions attempt-dir artifacts between runs (`PruneAttempts` protects the current attempt, qa_state.go:119-134); rollback restores only state.json/flow/qa.md snapshots, never plans/adjudication/assessment (qa_state.go:546-555, 943-961); no test exercises a second evidence publication anywhere in the package.

**Regression test.** Drive two full `RunQA`-shaped publishes (or one publish + one regenerated republish) over the same semantic attempt; assert both succeed (idempotent refresh) or the second is an explicit no-op — currently impossible to pass. Add a crash-retry case asserting resume can finish a partially published evidence phase.

## F2 — Focused shard runs persist authoritative false-terminal results from partial coverage, or block spuriously when follow-ups arise (High severity / High confidence)

**Claim.** `--shard` is accepted on plain `qa start` (parseSprintQAArgs imposes no resume-only restriction, sprint_commands.go:636-646; documented usage cli-reference.md:350; same wiring in TUI/web via `req.Task`). A focused run then:
- runs exactly 1/N shards (focus filter qa.go:624-633);
- synthesizes over the partial shard set — `SynthesizeQA` is phase-blind and aggregates whatever theories exist (qa_synthesis.go:42-56); `hydrateQASynthesisFollowUps` only validates follow-ups (qa.go:562-575);
- **unconditionally publishes `Phase=completed`**, terminal result completed, outcome counts and `NextAction` from the partial synthesis (qa.go:380-387, 408-416);
- and because the evidence phase filters to `Phase==Completed` shards only (qa.go:477-481) and `DeriveQAAssessment` can never observe missing shards (qa_adjudication.go:191-227; `ValidateQAState` accepts `CompletedShards < TotalShards` with a completed phase, qa_types.go:737-754), it persists a **pass/fail assessment, flow-summary assessment, and qa.md report derived from arbitrary partial coverage** (e.g., 1 shard of 10 ⇒ `AssessmentPass`, "QA evidence is current and complete").
- Second shape: if synthesis proposes ≥1 follow-up shard, batch #2 re-applies the stale `FocusShard` filter, matches nothing, and returns `InvalidState "focused shard is absent or already terminal"` (qa.go:634-636) — the whole run is published Blocked after fully succeeding otherwise.

**Probe (executed).** Fixture map with 3 primary shards + boundary; focused batch left 2 shards `mapped`, returned nil error, and both `SynthesizeQA` and `hydrateQASynthesisFollowUps` accepted the partial result (theories=1, others mapped).

**Bad outcome.** Operator-facing state, flow pointer, and the canonical `qa.md` declare QA complete/pass (or fail) from an arbitrary subset; the spurious-block shape converts a successful investigation into a blocked attempt with a misleading error. Counter-evidence searched: no gate ties focus mode to resume-only, no invariant requires full primary completion before the completed publish; cli-reference's own sentence ("Normal QA first runs the bounded read-only map and investigator pass, then…") implies full coverage is the contract being violated.

**Regression test.** Start focused on a multi-shard map: assert either an explicit refusal, or a non-completed phase until remaining shards run; separately drive a focused run whose investigator returns `inconclusive` and assert no spurious InvalidState block.

## F3 — Gated capability shipped as silent default; isolation prerequisite turns previously-working read-only QA into hard failures on capable-less hosts (Medium-High severity / High confidence on facts)

**Facts.** The authoritative workspace still gates this capability: TRD §18D header states the sequence "does not authorize implementation until a later sprint selects it explicitly", item 3 is "Evidence QA and repair"; roadmap.md ("Why This Roadmap Chunk Stops After Sprint 35") explicitly assigns no sprint number past 35 until the durable-run dogfood gate passes, listing "isolated evidence-producing QA" as later gated direction; the workspace contains sprints only through `35-durable-run-observability`. The implementation repo nevertheless ships Sprint-36/37-era behavior on the reviewed branch (commits `bdc22a0` "Add Sprint 37 evidence-producing QA", `19ad73b`, `c6f01cf`; repo-local branches `36-read-only-qa`, `38-bounded-repair` exist) with no promoted sprint/plan recorded in the authoritative workspace, and enables it by **default** for every non-smoke QA run.

**Concrete reliability consequence enabled by the default-on flip.** Admission now requires `IsolationProven`, computed from `pprocess.IsolationCapabilityFacts()` — on Linux that means a functional `bwrap` binary (isolation_linux.go:13-45). On any host without working bubblewrap (minimal containers/CI images), `ValidateQAAdmission` fails (qa_evidence.go:197-199) and `buildQAEvidencePublication`'s error fails the **whole run** via `publishTerminalQAFailure` (qa.go:418-421): all read-only investigation completes, then the attempt is published Blocked. Pre-gate, read-only QA succeeded on such hosts. Secondary governance artifact: `publishEvidence` authors `projects/<p>/sprints/<s>/qa.md` into the sprint root, while ARCHITECTURE.md L280 states Phase-3 keeps "only the current `review.md` and `smoke.md` in the sprint root".

**Severity framing.** Reported as a CURRENT-CONTRACT violation with an attached operational regression, not as style; per doctrine the gated-direction text alone would be FUTURE-INTENT, but the default-on wiring makes the ungated dependency load-bearing today.

**Verification/regression.** Either promote the sprint in the authoritative workspace (documenting bwrap as a system requirement) or gate the evidence phase behind an explicit selector so plain read-only QA retains its pre-gate behavior on incapable hosts.

## F4 — RecoverQA persists Stale on transient map failures (Low-Medium / Medium-High)

`s.RecoverQA` marks the retained attempt `stale`/"governed QA inputs no longer match…" on **any** `QAMap` error, including transient preparation/read failures (qa.go:228-234) — conflating unavailability with staleness in durable state and flow summary. Data survives and a matching-map resume still works (prepareQAAttempt keys on attempt ID only, qa.go:581), so impact is misleading status plus unnecessary staleness signaling. Regression: inject a transient PrepareReview failure during recover; assert recover does not rewrite phase to stale.

## F5 — `state.RegressionCandidates` accumulates across resumed evidence republications (Low / High mechanics, currently masked)

`publishEvidence` increments `state.RegressionCandidates += …` without reset (qa_state.go:760-764) while `IssueCount`/`RejectedCount` are overwritten; the resume path reuses the prior loaded state (qa.go:589-600), so a resumed republication double-counts regression candidates in status projections. Today F1 conflicts before this write lands; fixing F1 mutably exposes this. Reset counters from the current bundle at republication.

---

## Defended / non-issues (checked, not filed)

- **gofmt fact-check vacuity — refuted.** Hypothesis: `gofmt -d` reports drift only via stdout with exit 0, making exit-code-driven evidence (`command_failed` requires nonzero exit, qa_investigation.go:90-98) structurally unable to fail, manufacturing pass records (including the `Executable:"true"` fallback for non-.go shards, qa.go:490-493, which always passes). Empirically refuted on the pinned toolchain: Go 1.26 `gofmt -d` exits **1** when a diff exists, so drift produces Fail→evaluator→issue promotion. Residual note (not filed): older toolchains exiting 0 on drift would silently degrade the check to always-pass; nothing pins the toolchain contract.
- **Writer fencing is genuinely fail-closed.** `checkWriter` rejects any publish without a configured fence (qa_state.go:606-617); `RunQA` always installs one (durable fence from run-control acceptance, or token-equality fallback, qa.go:324-334); tokens are minted only inside the acceptance boundary with heartbeat-on-check (durable_operations.go:140-156).
- **Pointer-last + rollback hold.** State is written before the flow pointer, and the pointer is last-of-all (qa_state.go:555-575); canonical-file rollback restores state/report/flow on late failure. `SaveRecoveredState` refuses active phases (582-587); boot reconcile converts queued/running/synthesizing to Interrupted (locks.go:76-97).
- **Smoke-suite selector is lease-safe and correctly non-persisting.** It delegates to `RunSmoke`, which takes the mutation lease itself (smoke.go:21-25), persists nothing under `verification/`, and its compatibility-selector role is documented (cli-reference.md:544-547).
- **Load grammar/tamper-evidence solid.** Per-component symlink rejection, 0600 exact-match regular files, 128 MiB cap, strict version/disallow-unknown/trailing checks (qa_state.go:211-239, 379-431); state references (including `qa.md`) digest-pinned on every load (256-263, 975-991).
- **Investigator output strictness holds**: five required top-level fields non-null, trailing JSON rejected, size-capped, closed enums; alternate runtimes ignoring `ValidationSpec` fail closed without a product retry loop (qa.go:800-807, 903-946).
- Noted, cosmetic only: `boundedQAProgress` caps *total* emissions (default 100), so very long runs silently drop late progress events including `investigation_complete` (qa.go:1026-1041); the immutable-write conflict message says "map" regardless of record kind (qa_state.go:639).
