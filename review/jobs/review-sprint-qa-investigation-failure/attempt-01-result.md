Build baseline is green. Now reading the state/store layer and map builder.Synthesis tolerates non-terminal shards and regeneration semantics look interesting. Now checking runtime wiring, prompts, and the investigation runner.Now I'll write a reproduction test against the frozen tree copy (in /tmp, not the target repo) to prove the follow-up regeneration wedge.Finding A is proven. Two more repros: focused-start premature completion (C) and resume discarding retained follow-up progress (B).Both proven. Now counter-evidence checks: does the projection layer flag partial completion, does smoke take its own lease, and does the full frozen suite pass cleanly?A prior independent adversarial review exists for this surface. My findings A/B are new; C converges with its F2. Before writing up, let me independently verify that review's high-severity F1 (evidence republish conflict) rather than trusting it.All probes done. Verifying the target repos remain untouched before writing up.# Surface review: `sprint-qa-investigation` (failure/concurrency lens)

Target: ultraplan-go @ `c6f01cf` (reviewed via a read-only archive copy in `/tmp`; both repos verified untouched, the pre-existing `skills.go` modification excluded from scope). Baseline `go test ./internal/sprint` green at the frozen tree. Every finding below was reproduced with a probe against that frozen tree; probes were deleted afterwards. A prior adversarial review exists for this surface; my work was independent. Where I reached the same defect, I say so and cite my own verification.

---

## F1 — Second-round synthesis proposes follow-up shards that were never retained; the run is published Blocked after all investigation completes, and resume loops forever (High / High)

**Claim.** After executing synthesis-proposed follow-up shards, RunQA re-synthesizes over all shards. Follow-up investigators may lawfully return `inconclusive` or `cross_shard` theories (the prompt permits these outcomes for any shard, qa_prompt.go:149; `ValidateQATheory` accepts them, qa_types.go:672). Those theories become new follow-candidates with fresh deterministic shard IDs (qa_synthesis.go:103-105, 119-136), which no executed shard satisfies. The hydration gate then fails closed and the whole attempt is published as blocked.

**Execution path.** qa.go:387-410 at the frozen commit: `SynthesizeQA` → run proposed follow-ups → second `SynthesizeQA(mapResult.Map, shards)` → `hydrateQASynthesisFollowUps` requires every proposed ID to exist in shards with a terminal phase (qa.go:562-575). New IDs fail that check.

**Probe (executed).** Fixture with two round-1 follow-ups, each completing with one `inconclusive` theory: round 2 proposed 4 follow-ups, 2 of them novel unexecuted IDs; `hydrateQASynthesisFollowUps` returned an error. Without the extra theories, round 2 regenerates only the round-1 IDs and passes, so the trigger is specifically follow-up-tier outcomes of `inconclusive`/`cross_shard`.

**Observable bad outcome.** All primary, boundary, and follow-up shards complete; state is then persisted as `blocked` with blocker "a proposed follow-up shard did not reach a retained terminal state" and recovery guidance pointing at generic inspection. RecoverQA does nothing for a blocked phase (qa.go:215-227 has no such case). Resume re-derives the same follow-ups, pays for their investigators again, and wedges identically: a paid loop that only ends when governed inputs change and mint a new attempt ID.

**Counter-evidence searched.** The existing test asserts hydration rejects unretained proposals as correct behavior in isolation (qa_synthesis_test.go:88-115) but never exercises the RunQA double round; the `FollowUpShards=4` budget cap does not exclude novel IDs; no runtime gate restricts follow-up-shard outcomes; no caller filters candidates by retention before hydrating. Post-freeze commits (`4841b74`, `208c9d0`) added exactly this filtering (`pendingQASynthesisFollowUps`, `finalizeQASynthesisFollowUps`), which corroborates the defect while my review stays pinned to `c6f01cf`.

**Regression test.** Drive the frozen three-step sequence (synthesize, execute follow-ups whose investigators return `inconclusive`, re-synthesize, hydrate) and assert either completion or bounded re-execution of only unretained proposals. Today it cannot pass.

## F2 — Any re-run or crash-retry of evidence QA on unchanged inputs deterministically fails on the immutable plan CAS (High / High — independent confirmation of the prior review's F1)

I did not take the earlier claim on faith; I probed it myself. `FreezeQAEvidencePlan` mints an ID that excludes `FrozenAt` yet persists it into the bytes (qa_evidence.go:148-162); `publishEvidence` writes plans, adjudication, issues, and assessment immutable at fixed paths under byte-CAS (qa_state.go:633-643, 675, 738, 750-754, 777); the evidence phase is default-on for every non-smoke start/resume (sprint_commands.go:441, operation_runner.go:124).

**Probe (executed).** Two freezes differing only in instant produced the identical plan ID `qa-v2-plan-b357e4a59d22dfd0aa496313`; the second `writeRecord` returned `conflict: "immutable QA map already exists with different bytes"`.

**Bad outcome.** A plain `qa` re-run with zero input changes re-executes every investigator, then fails at the first plan write; the operator gets conflict recovery advice ("wait for the current owner") when no owner competes. A crash during `publishEvidence` leaves partial plans that rollback never removes (snapshots cover only state.json, qa.md, flow-state.json, qa_state.go:482), wedging all future resumes of that attempt. Same permanent-until-inputs-change shape as F1.

**Regression test.** Publish one evidence bundle, regenerate it with advanced clocks over the same semantic attempt, publish again; assert idempotent success or an explicit refresh path, plus a crash-retry case.

## F3 — Focused start/resume publishes authoritative completed/pass results from arbitrary partial coverage (High / High — converges with the prior review's F2, independently derived and probed)

`--shard` is valid on a fresh start: the parser accepts it (the parser's own test asserts `{"--shard", id}` yields action `run`, sprint_commands_test.go:649), usage text advertises it (sprint_commands.go:1618), web parity allows Task on `OperationQAStart` (operations.go:394-396, operation_runner.go:124), and RunQA has no resume-only gate. The batch then runs 1/N shards (qa.go:629-635); synthesis is phase-blind (qa_synthesis.go:42-56); phase is set to completed unconditionally (qa.go:412); `DeriveQAAssessment` sees only evidence records from completed shards and can return `AssessmentPass` from one record (qa_adjudication.go:191-227); the CLI exits 0 on pass (sprint_commands.go:488-492). No consumer compares CompletedShards to TotalShards anywhere in the package.

**Probe (executed).** Focused batch left 3 of 4 shards mapped, returned nil, and `SynthesizeQA` accepted the partial set.

**Bad outcome.** Flow summary, canonical qa.md, and exit code declare QA passed (or failed) from an arbitrary subset, bypassing the sprint QA gate. The contract itself frames `qa --shard <id>` as a focused control "added only where recovery requires them" (post-execution-qa-and-repair-loop.md §13.2), not as a completion shortcut. Secondary focus-specific shape: if the focused shard yields an inconclusive/cross-shard theory, the follow-up batch re-applies the stale filter, matches nothing, and errors "focused shard is absent or already terminal", publishing the otherwise-successful run as blocked (qa.go:634-636).

**Regression test.** Start focused on a multi-shard map; assert explicit refusal or a non-completed phase until remaining shards run, and no spurious block when follow-ups arise.

## F4 — Resume discards retained follow-up-tier progress and resets TotalShards (Medium / High)

The resume branch of `prepareQAAttempt` rebuilds shards from the map's planned shards only and merges loaded records solely for those IDs (qa.go:579-601). Follow-up shards persisted individually by an interrupted run are never consulted.

**Probe (executed).** Published a map plus a terminal follow-up shard owned by the current attempt; resume returned TotalShards shrunk from 5 to 4 and did not merge the retained shard.

**Bad outcome.** Any interruption after the follow-up tier starts (cancel, wall-clock deadline, crash) makes every subsequent resume re-run paid investigators for already-terminal shards, up to 4 shards x ShardTimeout (20 minutes default) per cycle, against the surface's own promise that resume continues current valid shards. Combined with F1 the tier is permanently unresumable; combined with F2 even a successful re-run dies at publication. Primary-tier progress merges correctly, so the loss is scoped to follow-ups.

**Regression test.** Persist terminal follow-up records, interrupt mid-follow-up execution, resume; assert retained terminal follow-ups are adopted without re-invocation.

## F5 — Terminal-failure publication drops the computed synthesis record (Low / Medium)

`publishTerminalQAFailure` publishes State and Flow only (qa.go:554-560). On an evidence-phase failure, outcome counts exist in state but the Synthesis pointer was never written, so status loses synthesis and follow-up detail while shards remain individually readable. Degraded observability rather than corruption; the post-freeze `WithSynthesis` variant corroborates. Regression: force an evidence error post-synthesis and assert status still loads a synthesis record consistent with state counts.

## Defended non-issues

- **SaveRecoveredState skips the writer fence** (qa_state.go:582-604): deliberate runtime-free recovery mutation, gated by the sprint mutation lease and a non-active phase whitelist; sound.
- **PruneAttempts("") at run start cannot orphan the pointer**: the pointer always references the newest published attempt, which its own prune protected; retention keeps it.
- **Self-referential fence fallback** when `qaWriterFence == nil` (qa.go:324-333) is unreachable through CLI/web wiring, which always installs the durable lease-heartbeat fence (sprint_commands.go:440, operation_runner.go:123, durable_operations.go:140-156).
- **Worker-pool abort protocol is deadlock-free**: results buffered per worker, drained even after publish failure, closer waits the group; cancellation persists the active shard without retry (test-verified at qa_test.go:288-319).
- **Pointer-last publication holds**: crash between state and flow writes is reconciled by RecoverQA's digest comparison; rollback restores the canonical trio atomically.
- **Chmod walk and temp files stay inside the verification subtree**; smoke-suite QA takes smoke's own lease and writes nothing under QA fencing (smoke.go:25).

F1, F3, F4 share one root cause worth stating plainly: the pipeline treats "no pending work in this invocation" as "attempt complete", and treats deterministic regeneration as equivalent to retained evidence. Both equations fail in production-shaped runs.