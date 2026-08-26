Smoke files are unchanged since the frozen commit; working tree reads are valid. Reading the primary files.Primary files read. Now tracing supporting wiring: fingerprint refresh, strict flags, path helpers, flow-state validation, process runner env handling, publication.SaveFlowState validates before rename, so the discarded `refreshEvidenceFingerprint` error degrades gracefully — downgraded. Now checking process runner env semantics, mutation locking, publication, and the TUI smoke-start wiring.CLI wires runtime; TUI/web smoke-start doesn't. Checking whether any test pins this behavior or proves it works.TRD gives two strong contract anchors (§18.9.2 identity-change-fails-smoke; §18.9.3 TUI parity). Checking for superseding amendments, then the CLI arg parser and runtime request wiring.Strong finding confirmed on env fail-open (nil `cmd.Env` inherits parent). Checking `reviewFixture` fidelity and the runtime metrics wiring, then verifying build/tests.Final verification round: env allowlist config surface, and the platform teardown file smoke depends on.Two quick completeness checks on F2's reach, then I'll write up.# Sprint Smoke Gate — Deep Independent Review

Target: `ultraplan-go` @ `c6f01cf` (smoke files identical at HEAD). Build, vet, and `go test ./internal/sprint/ -run Smoke` pass. Context pack read; all claims below re-verified against source and tests directly.

## Findings

### F1 — Environment allowlist fails open to full parent-env inheritance
**Severity: Medium-High | Confidence: High | Security/trust boundary**

- **Claim:** When every allowlisted environment variable resolves empty/unset, `smokeEnvironment` returns a nil slice (`smoke_protocol.go:635-641`, `var env []string` never appended), which flows into `pprocess.Request.Env`, where `cmd.Env = append([]string(nil), req.Env...)` (`internal/platform/process/process.go:86`) yields nil → Go inherits the **entire parent environment** in the harness child.
- **Bad outcome:** The external smoke harness process receives all parent secrets (provider keys, tokens) — the exact leak "bounded environment forwarding" (TRD §18.9.2) and `smokeEnvironment` exist to prevent. Failure is silent: no diagnostic fires.
- **Trigger:** Operator sets `smoke.environment` to a custom allowlist (supported: `config.go:555,572-573`) whose vars are unset in the invoking shell; or any context lacking all five defaults. Both discovery (`smoke.go:93-97`) and run (`smoke.go:150`) inherit fully.
- **Execution proof:** Replicated the exact composition in `/tmp/opencode/envdemo`: nil Env child sees parent-only marker var (`INHERITED-FULL-ENV`); empty-slice Env correctly gets `EMPTY-ENV`.
- **Counter-evidence searched:** Default settings usually include PATH so common paths build a non-nil env; test `TestDefaultSmokeEnvironmentPreservesInterpreterPath` only covers the non-empty case — it cannot catch this. No code distinguishes "no values" from "no allowlist".
- **Regression test:** `smokeEnvironment(settings-with-unset-vars, …)` must return non-nil empty slice; add a process-level test asserting a child spawned via `DirectRunner{Env: <that slice>}` observes an empty environment.

### F2 — TUI/web smoke-start is constructed without a runtime and can never succeed
**Severity: Medium-High | Confidence: High | Operability / TRD §18.9.3 parity violation**

- **Claim:** `OperationSmokeStart` builds its service as `sprint.NewService(root).WithPublisher(...).WithSmokeSettings(...)` — no `WithRuntime` (`internal/app/operation_runner.go:80`), unlike every sibling case that uses `sprintRuntimeService` (`sprint_commands.go:754-779`). Non-dry-run `RunSmoke` unconditionally calls `authorSmokeSuite`, which returns `smoke_author_runtime` on nil runtime (`smoke_author.go:21-23`; reached via `smoke.go:82-87`).
- **Bad outcome:** TUI nav items "Run Smoke [EXTERNAL]" and the diagnostic-override variant (`tui/model.go:497-498`) and the shipped web operation ("Smoke test", `web/static/app.js:184`, route registered in `operations_contract_test.go:46`) always fail after preflight and after `saveSmokeAttempt` records a Running attempt (`smoke.go:31`) — durable failed-attempt litter plus misleading guidance "Configure the smoke model/runtime" though it *is* configured (CLI uses it at `sprint_commands.go:571`). `operationRuntimeIdentity` even advertises "configured smoke author and harness" for this op (`operations.go:657`). Violates TRD §18.9.3 "TUI… expose the same… smoke scope".
- **Counter-evidence searched:** Web dry-run works (`operations.go:504-510`, authoring skipped); TUI full-flow smoke works via `sprintRuntimeService` (`operation_runner.go:37,46`). No test executes OperationSmokeStart end-to-end, so nothing disproves or pins the breakage.
- **Regression test:** App-level test running `sharedOperationRunner` for `OperationSmokeStart` against a fixture workspace with a fake harness must complete with a committed verdict.

### F3 — Evidence root equal to harness root silently disarms the authoring-scope snapshot
**Severity: Medium | Confidence: High (mechanics) | Validation bypass of TRD §18.9.2**

- **Claim:** `validateSmokeManifest` rejects authoring path `"."` but never checks evidence roots against the harness root: `smokePathsOverlap(".","src/tests")` is false for every candidate (`smoke_protocol.go:236-248,581-585`), and `canonicalDirectoryInside(root, root)` passes because `inside()` accepts `rel == "."` (`artifacts.go:77-83`). With `evidence.runs = "."`, `smokeHarnessSnapshot` hits `inside(prepared.RunsRoot, path)` on the top directory and `SkipDir`s the entire tree (`smoke_author.go:353-356`) — before and after snapshots are both empty.
- **Bad outcome:** `changedSmokeHarnessPaths` returns `[]`, so the manifest-allowlist loop (`smoke_author.go:84-88`) is vacuous: the author agent may modify **any** harness file undetected; TRD §18.9.2 "Any other harness change … fails smoke" is not enforced, and drift isn't published either. Additionally runs/issues containment (`canonicalFileInside(p.RunsRoot, …)`, `smoke.go:347-357`) accepts any file in the harness as evidence.
- **Trigger:** Cataloged/harness-supplied manifest with `"evidence":{"runs":".","issues":"."}` — a shape nothing validates; a hostile or merely buggy harness upgrade can flip it unnoticed since every other gate stays green.
- **Counter-evidence searched:** Authoring-path overlap check does catch evidence roots that are proper ancestors of authoring dirs; only exact-root equality escapes. Runtime permission policy still restricts writes, but that policy is the same single defense the snapshot tripwire was built to backstop.
- **Regression test:** `validateSmokeManifest` must reject an evidence root whose canonical path equals the harness root (and roots equal to each other); snapshot test asserting non-empty coverage for such a manifest.

### F4 — Disabled freshness/identity switches contradict TRD and make smoke.md attest checks that do not run
**Severity: Medium (contract) + Low-Medium (false attestation) | Confidence: High**

- **Claim:** `strictCompletedReviewSnapshotFreshness`, `strictCompletedSmokeSnapshotFreshness`, `strictSmokeAuthorProtectedSnapshots` are compile-time `false` (`freshness_policy.go:11-14`). Consequences: (a) governed-input changes after review completion do not stale review/smoke — `prepareSmokeStatic` (`smoke_protocol.go:186-194`) and `Status` (`service.go:269-289`) only digest-check the artifact itself — contrary to TRD lines 2077 and 1846 ("A fingerprint mismatch makes the artifact stale"); (b) `authorSmokeSuite` skips target/project identity capture entirely (`smoke_author.go:39-50,89-106`), yet `RenderSmoke` unconditionally writes "Product source and governed sprint inputs were identity-checked before and after authoring" (`smoke.go:572`) into the durable governance artifact, and publication spreads it (`publication.go:111-119`).
- **Bad outcome:** A review completed against requirements R1 gates smoke after requirements were swapped to R2 (only content-format findings or artifact-digest edits would trip it); and every smoke.md records an integrity attestation that never executed.
- **Counter-evidence searched:** The in-code comment documents a deliberate temporary relaxation for *completed-state* staleness, but TRD carries no amendment, and the comment's claim that "the smoke harness authoring allowlist remain[s] enforced" does not cover the protected-target/project identity check, which is wholly absent. Residual controls: restricted permission policy + `UnsupportedCount` abort (`smoke_author.go:110-112`), review-fingerprint cross-check (`service.go:286`).
- **Regression test:** Either re-enable attribution-based checks or change RenderSmoke wording to match reality; contract test that editing a governed input post-review forces `VerificationStatus.Review.Fresh == false` (currently fails).

### F5 — Unanchored placeholder scan discards fully validated smoke results at commit time
**Severity: Low-Medium | Confidence: High | Availability of the commit gate**

- **Claim:** `commitSmoke` validates rendered output via `ValidateSmokeContent` → `containsPlaceholder`, a bare case-insensitive substring match for `todo`/`tbd` anywhere (`index.go:218-221`, `smoke.go:441-443`). Harness-authored open-issue fields (title/summary/theory/evidence/action) and the operator's `--override-reason` are embedded verbatim by `RenderSmoke` (`smoke.go:488,529-541`).
- **Bad outcome:** A run that passed every protocol/evidence check fails to commit because e.g. an issue summary contains "`// TODO` guard" — smoke.md is not updated, the verdict is lost, flow state records AttemptFailed, and guidance says "Generate a complete evidence-backed summary", which is false. Delivery gating blocks until prose is reworded and the external suite rerun.
- **Counter-evidence searched:** Fails closed (prior artifact preserved — verified by `TestQASmokeParity…PreservesItOnMalformedRun` pattern at `smoke_test.go:491-494`), but no control prevents the false rejection; no word-boundary logic exists.
- **Regression test:** `RenderSmoke` output containing an issue titled "capacity TBD" must either commit or the scan must be boundary-aware.

### F6 — Harness-controlled text is embedded into smoke.md without newline/structure sanitization
**Severity: Low | Confidence: High | Artifact integrity**

- **Claim:** `printable()` only trims and swaps backticks (`smoke.go:698-705`); issue free-text fields, evidence `Kind` strings, and prerequisite descriptions from the run response are interpolated raw into markdown (`smoke.go:502,511,526,536-541,555`). A response containing `\n` sequences injects arbitrary headings/lines into the persisted, published governance record. Parsed verdict can't be overridden (first-match `fieldBacktick`), but forged evidence lines/findings can be.
- **Counter-evidence:** Harness is semi-trusted and locally cataloged; paths and hashes are strictly validated while content is not — an asymmetry, not an exploit primitive by itself. Consequence limited to audit-record pollution.
- **Regression test:** Response fields containing `\n## Forged` render neutralized (single-line) in smoke.md.

### F7 — Evidence/issue hashing is unbounded during validation
**Severity: Low | Confidence: High | Reliability**

- **Claim:** `validateSmokeRun` stats then `hashFile`s each evidence/issue file with no size cap (`smoke.go:359-367,391`), and `refreshEvidenceFingerprint` re-hashes them (`verify.go:330-347`); `hashFile` reads whole-file (`smoke.go:707-713`). Every other reader in this surface bounds at 64 MiB (`smoke_author.go:200,371`; `verify.go:433`).
- **Bad outcome:** A multi-GB file under the declared runs/issues root (harness bug or stray output) OOMs or stalls the CLI mid-validation after the harness already ran.
- **Regression test:** Evidence file above a stated cap must produce the bounded-read smoke error class instead of unbounded allocation.

## Defended non-issues

- **All-skipped suite ⇒ verdict `pass`:** identities/counts are set-equality validated (`smoke.go:312-343`); default selection requires complete mapping with enumerated tests; narrow explicit scopes are forced diagnostic-only (`smoke_protocol.go:495-520`), so they can't commit as authoritative. TRD is silent on skip semantics.
- **Zero-test selection committing as pass:** impossible for non-diagnostic commits — complete mappings' suites are validated non-empty (`smoke_protocol.go:351-353`) and default blocked otherwise (`smoke_protocol.go:522`).
- **Discarded `refreshEvidenceFingerprint` error in `commitSmoke` (`smoke.go:463`):** `SaveFlowState` validates *before* rename (`state.go:248,286`), so an empty InputFingerprint fails safely with reconciliation error and prior state intact — no brick.
- **Single heartbeat vs 2h expiry:** `attemptExpired` checks OwnerPID liveness first (`verify.go:467-479`); live runs hold the mutation lease so reconcilers can't interleave (`locks.go:112-126`).
- **Blocked/not-applicable smokes publish without roadmap mark:** consistent with review-stage publication behavior; roadmap marking is pass-gated (`smoke.go:39-48` vs `publication.go:115-117`) — asymmetry is intentional evidence transparency.
- **Double `prepareSmokeStatic` on the success path:** executes under the mutation lease; worst case is an honest post-commit publication error.
- **Dry-run spawning discovery:** read-only preview semantics; documented next-action contract.
- **CLI arg surface:** `--timeout` capped at 24h, scope-flag exclusivity, override-reason coupling all enforced (`sprint_commands.go:909-961`).

The two highest-priority fixes are F1 (fail-open env trust boundary) and F2 (dead smoke-start on two of three frontends); F3 is the subtlest — it needs only one manifest field to disable the surface's central authoring-integrity tripwire.