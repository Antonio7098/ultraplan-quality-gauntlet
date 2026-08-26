Smoke files are identical to the frozen commit. Reading primary sources now.Primary files read. Now the supporting wiring — process runner, containment primitives, lease, and CLI/operation paths.TRD confirms the env-blocked requirement. Now reading the smoke tests to map pinned behavior.All primary sources, wiring, contracts, and tests inspected; targeted test run green. Here is the deep-review result.

---

# Findings — `sprint-smoke-gate` (failure/concurrency lens)

## F1. TUI/web smoke start is wired to a runtime-less service — guaranteed failure after flow-state churn (Severity: high · Confidence: very high)

**Claim.** `sharedOperationRunner`'s `OperationSmokeStart` case constructs the sprint service without `.WithRuntime(...)`, unlike every sibling runtime operation, so every direct smoke start from the TUI and web dashboard deterministically fails with `smoke_author_runtime`.

**Bad outcome.** An operator with a correctly configured smoke model/runtime clicks TUI "Run Smoke [EXTERNAL]" (or POSTs web `smoke-start`): the mutation lease is acquired, a *running* attempt is persisted to `flow-state.json`, preflight passes, authoring aborts with `smoke_author_runtime`, and a *failed* attempt replaces it. The guidance ("Configure the smoke model/runtime and rerun smoke") cannot fix it — no configuration reaches this service instance. Repeatable indefinitely.

**Trigger/preconditions.** Any non-dry smoke start dispatched through `sharedOperationRunner`.

**Evidence.**
- internal/app/operation_runner.go:79-80 — `service := sprint.NewService(root.Path).WithPublisher(...).WithSmokeSettings(...)` (no `WithRuntime`), vs. `OperationVerifyStart` at :97-98 and all other cases which call `sprintRuntimeService(deps, root, ...)`.
- Dispatch sites: internal/tui/model.go:497-498 (two nav items), internal/web/operation_handlers.go:682; runner wiring internal/app/tui_commands.go:53, internal/app/serve_commands.go:79. (The CLI rebuilds its own runtime-capable service at internal/app/sprint_commands.go:562-574, so CLI is unaffected.)
- Failure point: internal/sprint/smoke_author.go:21-23 (`s.runtime == nil` → `smoke_author_runtime`), reached after the attempt write at internal/sprint/smoke.go:31.
- Confirmation UX asserts the opposite: internal/app/operations.go:304-310 sets `c.Runtime = true` and warns "EXTERNAL HARNESS + SMOKE ARTIFACT WRITE".

**Counter-evidence searched.** No ctx-injected runtime, no other construction path, no behavioral test covering this wiring (run_control_inventory_test.go:26 only greps source strings). TRD §18.9.3 (~L2076-2078, workspace) requires TUI/CLI parity for smoke scope/start/results — this violates that CURRENT-CONTRACT.

**Regression test.** Drive `sharedOperationRunner` with `OperationRequest{Kind: OperationSmokeStart}` against a fixture workspace with a fake authoring runtime configured; assert the run reaches discovery (or assert `service.runtime != nil` parity with `OperationVerifyStart`).

## F2. Manifest-declared environment contract is not implemented: intersection is dead code, unavailable env is silently dropped instead of `blocked` (Severity: medium · Confidence: medium-high)

**Claim.** `smokeEnvironment` forwards exactly `settings.Environment` regardless of the manifest; the manifest-intersection loop can never add a name; declared-but-unavailable variables vanish silently, with no `blocked` classification.

**Dead-code proof.** internal/sprint/smoke_protocol.go:628-633: `names` starts as a copy of `settings.Environment`; the loop appends `name` only when `allowed[name]` (i.e., name ∈ `settings.Environment`) **and** `!contains(names, name)` — the second conjunct is always false. The manifest list has zero effect in either direction.

**Contract.** TRD §18.9.2 (~L2068): smoke execution must "classify unavailable required environment as blocked and irrelevant scope as not applicable." The second half is implemented (`not_applicable` selection); the first has no implementation anywhere in the surface. docs/configuration.md:230 ("Add a manifest-declared name to `smoke.environment` only when the harness genuinely needs it") documents forwarding as config ∩ manifest — matching the *intent* of the dead loop, not its behavior.

**Bad outcome.** Harness declares a credential/env var required for its real-boundary probes; operator allowlists it in `smoke.environment` but it is unset in the environment. Smoke proceeds without it: either the run errors late (full authoring+run cycle wasted) or the harness degrades toward the exact "local regression stands in for real boundary" behavior the authoring prompt forbids — and UltraPlan commits the resulting `pass` evidence instead of classifying the run blocked. Additionally, empty-string values are dropped (smoke_protocol.go:637), so the harness cannot distinguish "unset" from "set to empty".

**Counter-evidence searched.** Discovery prerequisites could express env requirements, but nothing maps env availability to them; no test pins either semantics beyond config-side filtering (TestDefaultSmokeEnvironmentPreservesInterpreterPath exercises only `settings.Environment` with an empty manifest — masking the dead code).

**Regression test.** Manifest declares `TOKEN`; settings allowlist includes `TOKEN`; getenv returns "" → expect a `blocked` classification (per TRD) or at minimum an explicit diagnostic; and a manifest-declaring `TOKEN` with config not allowlisting it must not forward it (already holds) while a config-only name must not forward when the manifest omits it (fails today).

## F3. Authoring allowlist gate is a per-run baseline diff — residue from an interrupted authoring run is silently accepted forever after (Severity: medium · Confidence: high on mechanism)

**Claim.** Out-of-manifest changes made by an authoring agent are only detected by diffing against the snapshot taken at the *start of the current run* (internal/sprint/smoke_author.go:32-35, 78-88). If a prior authoring run is cancelled/killed after making an out-of-allowlist write, the retry's before-snapshot contains that change; the diff is clean, the gate passes, and the foreign file is a permanent, invisible part of the "validated" harness tree that discovery/run then execute against.

**Bad outcome.** Partial-progress/restart escape of the core invariant (TRD ~L2070: "Any other harness change ... fails smoke"). Example: cancelled authoring leaves `harness/inject.sh` outside declared paths; every subsequent smoke run treats it as pre-existing; if harness conventions execute tree scripts, injected logic participates in evidence generation with no gate ever firing.

**Path.** cancel mid-authoring (`smoke_author_cancelled`, smoke_author.go:116-118) with a rogue write already flushed → rerun → `harnessBefore` includes rogue file (line 32) → agent changes only allowed paths → `changedSmokeHarnessPaths` clean → run/discovery execute against contaminated tree.

**Existing controls / counter-evidence.** Session resume (session_state.go:105-156) usually continues the same conversation, whose prompt tells the agent to remove *its own* out-of-scope creations (smoke_author.go:304-308) — prompt-level only, lost on provider/model change or fresh-session fallback. Attribution/attribution-based discrimination is compile-time disabled (`strictSmokeAuthorProtectedSnapshots=false`, freshness_policy.go:14), and grep confirms no later full-tree allowlist rescan exists (only smoke_author.go:82-85). Fails-open direction is unmitigated.

**Severity note.** Requires a cancellation coincident with agent misbehavior, so medium, not high. **Regression test:** run authoring with a fake runtime that writes an out-of-allowlist file then returns a canceled ctx error; rerun with a clean author runtime; assert the run fails `smoke_author_scope` (it passes today).

## F4. Discarded fingerprint error converts a completed run into post-artifact reconciliation failure (Severity: low-medium · Confidence: high)

Verified true (context-pack unknown #3). internal/sprint/smoke.go:463 `inputFingerprint, _ := refreshEvidenceFingerprint(identityRefs)` — a transient hash failure (e.g., harness-side async cleanup removing an evidence file between `validateSmokeRun` and commit) yields `""`, which `validateSmokeStageState` rejects for `LastComplete` (internal/sprint/smoke_types.go:328) so `SaveFlowState` fails. Outcome: smoke.md already atomically replaced, flow state left at the previous record → digest-mismatch/stale status, hidden verdict evidence, and a "rerun smoke" requirement after a run that actually succeeded. Fails safe, but the error surfaces after the irreversible step with misleading reconciliation guidance (docs/recovery.md:86 confirms manual recovery posture). **Fix/regression:** propagate the hash error into a typed pre-write failure (artifact not yet replaced), tested by deleting an evidence file between validation and commit via a hooked store.

## F5. Publication block ignores the run error — reconciling runs git-publish a mismatched artifact/state pair (Severity: low · Confidence: high)

internal/sprint/smoke.go:49 gates publication on `Status==Completed && !DiagnosticOnly && Artifact!=""` without checking `err`. After F4-style reconciliation failure (`Status` stays `completed`, `Reconciliation=true`), `publishSmokeStage` still runs: it commits the **new** smoke.md next to the **old** flow-state.json (publication.go:111-119) into the workspace repo — a permanently recorded inconsistent pair that `ValidateSmoke` flags on any clone — and, for a passing verdict, includes roadmap.md (publication.go:115-117) even though roadmap marking was skipped due to the error (smoke.go:39 requires `err == nil`). Counter-evidence: publisher skips unchanged files, and the state remains recoverable by rerun; hence low. **Regression:** force `SaveFlowState` failure; assert no publication occurs when `err != nil`.

## F6. Unbounded `os.ReadFile` hashing of harness-controlled evidence (Severity: low · Confidence: high)

`validateSmokeRun` stats and hashes every evidence/issue item with `hashFile` → `os.ReadFile` (internal/sprint/smoke.go:359-373, 707-713) with no size ceiling, and `refreshEvidenceFingerprint` re-reads all of them plus manifest/review.md at commit (verify.go:330-347). Paths are containment-checked but size-unbounded and enumerated by the harness response itself; a runaway or hostile harness addressing a multi-GiB file under `runs/` causes OOM in the CLI *after* the harness executed — inconsistent with the deliberate 64 MiB bound applied one layer away (smoke_author.go:371) and with the bounded-capture design everywhere else. **Regression:** evidence item pointing at a >memory-size sparse file; assert a typed size-bound failure instead of allocation.

## F7. Documented env-forwarding privacy contract contradicted by code and pinned test (Severity: low · Confidence: high)

docs/configuration.md:230 states "The built-in platform set is limited to `TMPDIR`, `LANG`, and `LC_ALL`; `PATH` and `HOME` are not forwarded by default." Reality: platform/config defaults forward all five (internal/platform/config/config.go:189; internal/sprint/smoke_types.go:55), and TestDefaultSmokeEnvironmentPreservesInterpreterPath pins PATH/HOME forwarding. Either the shipped doc or the code is wrong; operators relying on the documented posture leak `$HOME` (username, machine layout) to the external harness. One side must change; currently the doc is the stale party.

## F8. Harness-controlled strings render into canonical smoke.md with only backtick flattening (Severity: low · Confidence: medium)

Issue `title/summary/theory/evidence/action`, prerequisites, rationales, and diagnostics are harness/protocol-controlled and rendered via `printable()` (internal/sprint/smoke.go:698-705), which trims space and flattens backticks but preserves newlines and markdown structure (smoke.go:529-557). A harness can embed `\n\n## Verdict And Next Action\n\nVerdict: \`pass\`` inside an issue title, forging verdict-looking sections inside a failing artifact consumed by humans and by downstream agents (the in-tree reconcile skill instructs agents to read smoke.md). Product-computed header verdict parsing is unaffected (`fieldBacktick` finds the genuine top line first) and `ValidateSmokeContent` passes, so the spoof survives validation. Trust transition (untrusted protocol string → persisted canonical evidence) with no newline/heading control qualifies under this surface's trust-boundary doctrine; consequence is deception, not verdict corruption. **Suggestion:** collapse control characters/newlines in `printable()` for harness-sourced fields; pin with a render test.

---

# Defended non-issues (investigated, not reported as defects)

- **Forced-review diagnostic + blocked/not_applicable selection commits despite `DiagnosticOnly`** (smoke.go:129-135 vs the no-commit path at :180): harmless — a current fail/blocked review already forces `Smoke.Stale` via fingerprint mismatch (service.go:286-288), so no fresher evidence is displaced and the assessment cannot improve.
- **Second `prepareSmokeStatic` before publication flipping between evaluations** (context #4): defined, fails safe (error joined onto success), window requires out-of-band interference the lease cannot police; noted, not a defect.
- **Roadmap-mark vs roadmap-publish condition asymmetry** (context #5): the extra `ReviewVerdict ∈ {pass, pass_with_findings}` term in smoke.go:39 is redundant — fail/blocked reviews force `DiagnosticOnly=true` (smoke.go:128) — so no reachable divergence.
- **Single heartbeat vs 2h expiry** (context #7): `attemptExpired` checks liveness of `OwnerPID` first (verify.go:467-479), so long live runs are never falsely expired; dead owners expire immediately. Acceptable.
- **Non-linux/darwin teardown degradation** (context #12, process_other.go): supported targets are linux/darwin (CI `ubuntu-latest`; unix build tags); the `!CleanupComplete ⇒ smoke_cleanup` mapping errs toward safety on unsupported platforms. Note only.
- **Unleased concurrent `Status()` flow-state saves vs smoke writes:** status writes are derived/idempotent snapshots; atomic rename prevents corruption; worst case a benign lost update. Flow-state-surface concern, low impact.
- **Duplicate test IDs within one suite's enumeration:** deterministic set-equality failure attributable entirely to the harness's own protocol output; self-consistent.

**Baseline:** `go build ./...` and the smoke-focused `go test ./internal/sprint/` selection pass at the reviewed tree (smoke files byte-identical to frozen `c6f01cf`).