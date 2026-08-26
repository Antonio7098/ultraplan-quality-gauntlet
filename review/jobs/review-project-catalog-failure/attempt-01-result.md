Context pack read; no drift on surface paths since it was written. Now reading the actual sources.Now the tests and supporting workspace helpers, plus the smoke call site.Core sources read. Now verifying key hypotheses: the `sp.Path` semantics at the smoke call site, whether roadmap issues block the chain earlier, and the workspace contract docs.`sp.Path` is absolute — that hypothesis dies. Now checking whether any earlier chain stage validates roadmap structure before smoke (reachability of the zero-sprint tolerance), and the mutation-lock granularity.No stage parses roadmap before smoke — only `project-index.md` is hard-checked (`service.go:987-990`). Lock is per-sprint-path (`locks.go:117`). Now checking the mutation-lock mechanism, the real-world index format, and contract docs.The real workspace `project-index.md` itself contains unrecognized sections with tables (`## Available Studies`) — so silent skipping of unknown sections is required tolerance, not a defect. Checking the lock implementation and contract docs now.Lock confirmed per-sprint (`Project--Slug.lock`) — no cross-sprint exclusion for the shared `roadmap.md`. Checking the documented exit-code contract and smoke protocol's delivery-marking promise.Found a parser/rewriter grammar asymmetry worth pinning down. Verifying the smoke preflight doesn't independently check roadmap validity, and confirming the status-line matching details.Smoke preflight never inspects roadmap.md — confirmed. To turn hypotheses into executed evidence without touching the target repo, I'll copy `internal/project` + `internal/workspace` into `/tmp/opencode` and run targeted tests there.All three mechanics confirmed by execution. Two remaining checks: does anything downstream reconcile roadmap-vs-flow-state divergence (existing control), and does `syncDir` do what I think. Plus probing the workspace-tier validation gap.# Review report: `project-catalog` (failure/concurrency lens)

All findings below were verified against the frozen commit (`5d54525`, clean tree) and — where noted — executed as probes in a scratch copy of the package at `/tmp/opencode/pgtest` (target repo untouched).

---

## F1 — Delivery marking silently no-ops on a governed-but-unparseable roadmap; smoke still publishes success

**Claim.** `MarkRoadmapSprintDelivered`'s zero-sprint tolerance (`roadmap_status.go:32-37`) returns `(false, nil)` not only for legacy section-less roadmaps but for any roadmap whose sprint sections fail the closed-world grammar (e.g., `## Phase` heading removed/typo'd while `### Sprint` sections remain; non-numeric sprint headings). The caller (`internal/sprint/smoke.go:39-48`) treats `nil` as reconciliation success and proceeds to publication.

**Bad outcome.** After a passing, published smoke run, `roadmap.md` still says `active`/`planned`. Published evidence and the governing document diverge silently; nothing at smoke time surfaces it. The only detector is an unprompted manual `ultraplan project <p> validate`.

**Trigger/preconditions.** Any user/agent edit that breaks roadmap grammar after planning; reachable because **no chain stage structurally validates roadmap.md** — only `project-index.md` is hard-checked (`service.go:987-990`); smoke preflight checks catalog/harness/manifest/target only (`smoke_protocol.go:111-169`); review feeds roadmap to the LLM as content without parsing (`review.go:224-241`). A sibling slug-mismatch *does* error loudly (`roadmap_status.go:37`), so the silent bucket is exactly the broken-file class.

**Evidence + executed probe.**
```
phaseless: changed=false err=<nil>
CONFIRMED: silent (false,nil), file untouched, no error to smoke caller
```

**Counter-evidence considered.** Comment claims legacy-workspace intent; but a genuinely legacy file has no `### Sprint` sections either, so content could disambiguate. `validate` does flag the grammar issues (backstop), but only if run.

**Severity:** medium. **Confidence:** high (code path + execution).
**Regression test:** phaseless fixture containing `### Sprint` lines must make `MarkRoadmapSprintDelivered` return an error (or `RunSmoke` emit `roadmap_reconciliation`) instead of `(false,nil)`.

## F2 — Lost-update race on shared `roadmap.md`: mutation lease is per-sprint, the write is whole-file

**Claim.** Two concurrent smokes for different sprints of one project both pass gates (lock is `Project--Slug.lock`, `verification_lock.go:27`; lease keyed by sprint path, `locks.go:117`), then both do read→modify→whole-file-rename on the same `roadmap.md` with no version check or retry (`roadmap_status.go:16-80`). Last writer wins; the other delivery marking vanishes. Same mechanism clobbers a concurrent user edit of this user-owned document.

**Bad outcome.** Both smokes return success and publish; roadmap shows one sprint delivered, the other still active. Invisible to all tooling: `validateRoadmap` only cross-checks dir existence vs active/delivered status (`validation.go:126-152`) — dir exists ⇒ no finding; nothing joins flow-state completion to roadmap status (verified: no such consumer exists).

**Evidence + executed probe** (deterministic replay of the overlapping read/write window):
```
writer B changed=true err=<nil>; final statuses: map[01-one:active 02-two:delivered]
CONFIRMED: A's delivery marking lost under interleaving
```

**Counter-evidence considered.** "Non-Goals" defer multi-user collaboration, but multi-process local concurrency is first-class here (the lock and `ReconcileInterruptedMutation` exist precisely for it) — the lock just doesn't cover this shared file. Window is milliseconds but the workflow invites back-to-back sprint smokes.

**Severity:** medium (silent governance corruption, unrecoverable except by human diff or smoke re-run nobody knows to do). **Confidence:** high mechanics, medium likelihood.
**Regression test:** goroutine stress: N×{mark(slug-a), mark(slug-b)} on a two-sprint roadmap; final parse must always show both delivered.

## F3 — Parser accepts any-cased `>status:`; rewriter regex requires exact `Status:` ⇒ marking reports success but status stays active

**Claim.** Parser lowercases metadata keys (`roadmap.go:236`); rewrite matcher `^(\s*>\s*Status:\s*).*$` is case-sensitive (`roadmap_status.go:11`); slug detection in the same function *is* case-insensitive (`:56`) — internal inconsistency evidencing oversight. With `>status: active`, the function inserts `> Status: delivered` after the Slug line, but the original lowercase line re-overrides on re-parse (parser reads all `>` lines until first H4, last wins).

**Bad outcome (executed).**
```
lowercase: changed=true err=<nil>
post state: Status:active ... issues=[]
CONFIRMED: still ACTIVE after successful delivery marking
```
File left with duplicate status lines; every retry rewrites and re-reports `changed=true` with no effect; validate stays green (dir exists).

**Trigger:** hand-edited or agent-authored lowercase/uppercase key. **Existing controls:** none; idempotence check uses parsed status, which never becomes `delivered`.

**Severity:** low-medium. **Confidence:** high (executed).
**Regression test:** lowercase-status fixture ⇒ post-write parse must be `delivered` with exactly one status line.

## F4 — `atomicWriteRoadmap` skips directory fsync, unlike every sibling durable writer

**Claim.** `atomicWriteRoadmap` (`roadmap_status.go:83-109`) fsyncs the temp file but never the directory after rename; the codebase convention `syncDir` (`state.go:399`) is applied by `atomicWriteFile` (`smoke.go:744`), execute state (`execute_state.go:180`), QA state (`qa_state.go:907`), review (`review.go:1739`), study writers. Crash in the post-rename window can roll `roadmap.md` back to pre-delivery content while flow-state (dir-synced) already records delivery — restart shows inconsistent state with no diagnostic; recovery is an informed smoke re-run (idempotent marking would repair).

**Severity:** low (narrow window; ext4 `data=ordered` mitigates). **Confidence:** high (inspection; divergence from own convention).
**Regression test:** hard to test portably; at minimum assert parity via a shared atomic-write helper used by both call sites.

## F5 — Broken workspace-tier reasoning override passes `validate` green

**Claim.** `ValidateProject`'s override check (`validation.go:82-95`) stats only the project-tier path and `continue`s when absent; the full-chain `ResolveReasoningDefault` runs only when the project tier exists — in which case it shadows the workspace tier anyway. So a broken workspace-tier file (empty, non-md, dir) is never validated, while runtime prompt rendering hard-fails on it later (`prompts.go:124,148,151`).

**Bad outcome (executed).**
```
runtime resolution: err=reasoning default "prompts/create-area-reasoning.md" is empty
validate status=ok findings=[]
```
Green health door, red planning chain. Caveat: cli-reference:143 wording ("validates project reasoning overrides") may scope this deliberately; user-guide:66-72 documents the three-tier precedence validate claims to health-check. Fix is trivial: resolve the chain regardless of project-tier presence.

**Severity:** low. **Confidence:** high (executed).
**Regression test:** empty `<root>/prompts/create-area-reasoning.md`, no project tier ⇒ `validate` must report invalid.

---

## Defended non-issues (checked, not reported)

- **Silent skip of unrecognized `##` sections** (`index.go:30-35`): required tolerance — the authoritative workspace's own `project-index.md` contains ungoverned sections *with tables* (`## Available Studies`, `## Prior Decisions`); emitting findings would trip the sprint chain's `parseFindings>0` hard-fail (`service.go:988`) on the real file.
- **Warn-only `validate` exits 5 while printing `Validation: ok`**: exit class 5 = validation error and findings exist; stdout carries the distinguishable status string. Awkward, not contract-breaking.
- **Relative-CWD hazard at `smoke.go:44`**: refuted — `sp.Path` is absolute (`workspace.ResolveInside` normalization in `DiscoverSprints`).
- **No size caps on catalog/roadmap reads**: self-authored local files; hypothetical blow-up, no realistic trigger.
- **Symlink-following / lexical-vs-EvalSymlinks containment divergence**: needs local filesystem attacker; author-trust model; already tracked as a seam elsewhere.
- **Insert-after-slug shift idiom, CRLF tolerance, fenced-block line numbering, exact-match-before-prefix resolution**: all verified correct.
- **Discovery admits partially-scaffolded dirs**: shape enforced at status/validate; pinned intentional by tests.