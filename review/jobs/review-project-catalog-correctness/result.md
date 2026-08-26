Context pack was built at an ancestor commit; `internal/project/**` and the project command files are unchanged through the frozen target, so it stays valid. Now reading the source directly.Now the tests and supporting helpers:Source and tests read. Now the shared helpers (`workspace.ResolveInside`, `DefaultOverrideFile`) and the smoke caller of `MarkRoadmapSprintDelivered`, plus the CLI contract docs:The smoke caller is clear. Now checking how the sprint chain enumerates sprints (`ResolveSprint`) to assess the zero-sprint tolerance path, plus the CLI exit-code contract docs:`ResolveSprint` enumerates sprint **directories**, so smoke can resolve a sprint even when the roadmap entry is broken — important for the delivery-marking path. Now checking how downstream stages consume catalog sections, to gauge blast radius of silent-parse behaviors:Downstream blast radius confirmed. Now I'll empirically probe the candidate defects using a Go **overlay** (test file injected from `/tmp/opencode`, so the target repo tree stays untouched):All four probes reproduced. One more accuracy check on the zero-sprint tolerance reachability — how `resolveSprintInputs` resolves a sprint (roadmap consulted or not):`resolveSprintInputs` resolves sprints purely from directory listings — the roadmap is never consulted before delivery marking. That composes the zero-sprint tolerance with silent parse drops. Baseline verification next:Baseline green, probes reproduced, contracts checked. Review complete — writing up.

---

# Findings: `project-catalog` deep review (ultraplan-go @ `5d54525`)

Probes were run via `go test -overlay` with the injected file living in `/tmp/opencode`; the target tree was never modified. Existing `internal/project` + `internal/app` tests pass at the frozen commit.

---

## F-1 — Mistyped `### Sprint N:` headings are silently dropped; `validate` reports OK on a broken governing document

- **Category:** correctness / parsing-validation / misleading success · **Severity:** P2 · **Confidence:** confirmed (reproduced)
- **Claim:** Any H3 heading that doesn't exactly match `^Sprint (\d+): (.+)$` closes the current sprint region with **no issue emitted**, unlike every other closed-world grammar violation (unknown H4 subsections, metadata keys, statuses all emit issues).
- **Observable bad outcome:** Reproduced: roadmap containing `### sprint 1: lowercase` and `### Sprint 2 No Colon` parses to **0 sprints, 0 issues**; `Service.Validate` returns **status=ok, findings=0**. The sprints vanish from every roadmap-driven consumer (validate slug↔dir join, web roadmap preview, delivery marking) with zero diagnostics anywhere.
- **Trigger/preconditions:** Authoring typo: lowercase `sprint`, missing colon, stray space before colon (`Sprint 1 : X`), or `Sprint 0`. Most damaging for planned sprints whose `sprints/<slug>/` dir doesn't exist yet — then nothing else detects it.
- **Evidence:** `internal/project/roadmap.go:142-147` (`if !ok { currentSprint = nil; continue }` — no `add()` call). Probe log: `parsed sprints=0 issues=0`, `validate status=ok findings=0`.
- **Execution path:** `ParseRoadmap` → drops sprint → `Roadmap.Sprints` excludes it → `validateRoadmap` join sees nothing to flag (dirs absent + planned ⇒ silent, `validation.go:130-149`) → CLI exit 0.
- **Existing controls / counter-evidence:** If the sprint dir exists, `validateRoadmap` emits "sprint directory missing from roadmap" — but the cause string blames a missing `> Slug:` entry, pointing away from the actual typo'd heading. No test pins non-sprint-H3 tolerance as desired; the sanctioned pattern for non-sprint sections is H2-with-prose (`## Contract Gates` fixture, roadmap_test.go:59-62).
- **Composition risk:** if *all* sprints are dropped this way, `MarkRoadmapSprintDelivered` hits the legacy zero-sprint tolerance (`roadmap_status.go:34-36`) and returns `(false, nil)`; the smoke caller (`internal/sprint/smoke.go:39-48`) discards the bool and publishes success while roadmap.md never marks delivery — sprints resolve from **directories** (`internal/sprint/discovery.go:14-32`), so resolution succeeds without a roadmap.
- **Fix direction:** emit an issue (warn minimum, error preferred) for H3 headings inside/near phases that fail `parseSprintHeading`, e.g. "unexpected section heading; use '### Sprint N: Title'".
- **Regression test:** ParseRoadmap cases `### sprint 1: x`, `### Sprint 1 X`, `### Sprint 0: x` each assert ≥1 issue; golden-fixture case asserts still 0 issues (guards against flagging legitimate prose).

---

## F-2 — Rows under an unrecognized `##` section are silently discarded: catalog emptied, containment checks bypassed, `validate` green

- **Category:** parsing-validation / misleading success · **Severity:** P2 · **Confidence:** behavior confirmed; defect judgment high
- **Claim:** An unrecognized heading name (typo or case mismatch) zeroes the section state and every subsequent `|…|` row is skipped with no finding — those rows receive **none** of the per-entry checks (existence, escape containment, cross-project reasoning ownership, smoke-harness manifest).
- **Observable bad outcome:** Reproduced: `## Active Contract Pools` (pluralized typo) containing a row with path `../outside.md` → `entries=1` (only the well-named Source Documents row), **findings=0**; the would-be "catalog path escapes workspace" check never runs because the row never becomes an entry.
- **Trigger/preconditions:** Any heading-name slip: pluralization, case (`## smoke harnesses`), rewording. Real indexes legitimately contain unrecognized prose sections (`## Project Scope`, test-pinned at project_test.go:116), so pure silence can't be dismissed as unreachable authoring style.
- **Downstream blast radius (contract evidence):** the catalog is governing input: sprint-index stage declares itself incomplete when selections are "absent from `project-index.md`" (plan-sprint-protocol.md:154,160); smoke gate hard-fails `exactly one smoke harness is required` (`internal/sprint/smoke_protocol.go:117-121`). All far from root cause, hours later, while `ultraplan project <p> validate` — documented as validating catalog references (cli-reference.md:135-139) — exits 0.
- **Evidence:** `internal/project/index.go:27-35` (heading switch sets `section = recognizedSections[name]`; `section == ""` skips rows; no `add` path exists in this function for unrecognized names).
- **Fix direction:** emit a warn-level finding when an unrecognized `##` heading is followed by table-shaped (`|`-prefixed) rows, naming the heading; optionally case-insensitive section matching.
- **Regression test:** ParseProjectIndex with a typo'd section containing rows asserts a finding mentioning the heading and that recognized-section entries still parse; `## Project Scope` bullet-only section asserts still 0 findings.

---

## F-3 — `MarkRoadmapSprintDelivered` scans raw lines without fence awareness; can rewrite a fenced example instead of the live status line and report success

- **Category:** correctness of sole write path / state inconsistency · **Severity:** P3 · **Confidence:** behavior confirmed (reproduced); practical likelihood low
- **Claim:** The status-line search (`roadmap_status.go:54-66`) and the sprint-end boundary scan (`:46-51`) operate on raw lines, while `ParseRoadmap` ignores fenced blocks. A ``` / ~~~ block between the Slug line and the live `> Status:` line (before any H4) containing `> Status: …` text gets rewritten instead.
- **Observable bad outcome:** Reproduced: sprint with fenced `> Status: planned` example followed by live `> Status: active` → function returns `(true, nil)` having rewritten the line **inside the fence**; re-parse still yields **status=active**. Caller `RunSmoke` treats reconciliation as done and proceeds to publication (`smoke.go:45-58`): published success + roadmap still active. Same blindness lets a fenced `##`/`###` line truncate the sprint body scan early.
- **Trigger/preconditions:** roadmap embeds a markdown example of roadmap metadata inside a fence positioned before the live status line (plausible in process-documentation roadmaps; this repo's own docs embed roadmap snippets).
- **Existing controls:** none — the parser's fence state is discarded (`:21` discards issues and offsets aren't reused).
- **Variants:** CRLF files: regex `.*$` swallows the `\r` (rewrite drops it) and the insertion path writes an LF-only line into a CRLF file — mixed endings; cosmetic, parsers tolerate.
- **Fix direction:** track fence toggles during the raw scan using the same ```` ``` ````/`~~~` toggle rule as the parser, or derive the status-line offset from the parse pass.
- **Regression test:** fenced-example roadmap asserts the live status line is rewritten, fence content byte-identical, and re-parse yields `delivered`.

---

## F-4 — Syntactically invalid project refs exit 4 (workspace/filesystem) instead of the documented reference-error class 5

- **Category:** CLI exit-contract inconsistency · **Severity:** P3 · **Confidence:** high (code path unambiguous; unpinned by tests)
- **Claim:** `ResolveProject` returns a **plain** error (not `RefError`) for unsafe refs like `../api` or `""` (`discovery.go:50-53`); `mapProjectError` promotes only `RefError` to exit 5 (`app/project_commands.go:77-83`), so invalid refs fall to exit 4. cli-reference.md:18-27 defines 5 = "validation/reference error", 4 = "workspace or filesystem error".
- **Observable bad outcome:** `ultraplan project "../api" status` exits 4 — a filesystem-error class — for a pure reference problem, while missing/ambiguous refs on the same verb exit 5 (pinned by `TestProjectMissingAndAmbiguousRefsAreValidationErrors`). Scripts keying class 5 to mean "bad project reference" misroute the failure; the same failure family splits across two classes depending on *why* the ref failed.
- **Counter-evidence sought:** no doc or test pins invalid-ref→4 as intended; help/usage texts don't cover it either (arguably class 2 would fit better than either — but 4 fits neither).
- **Fix direction:** classify invalid refs deliberately (RefError-carrying or explicit class) and pin with a test asserting the exit code.

---

## Defended non-issues / observations

- **Delivery-marking insertion path** (no `Status:` line present): verified correct — inserts `"> Status: delivered"` directly after Slug, preserves Goal/GateItems on re-parse, handles missing trailing newline (probed both variants). Only the *rewrite* branch had test coverage; insertion was untested but sound.
- **Atomic write semantics**: temp-file + chmod-to-original + fsync + rename; mode preservation and idempotence test-pinned. Missing directory fsync is below reporting threshold for a user-owned file.
- **Containment**: absolute catalog paths outside Smoke Harnesses are rejected by `ResolveInside`'s `isInside` Rel check (paths.go:36-41); `../` escapes produce findings (test-pinned); smoke-harness roots/manifests get true EvalSymlinks containment including dir/file checks. Lexical-vs-EvalSymlinks divergence remains a tracked seam but requires a self-inflicted symlink in a user-owned workspace — no trust transition, not reported.
- **Reasoning-default chain**: rel paths are validated against three pinned constants before any join, so traversal is impossible; empty/non-md/dir overrides become errors; three-tier precedence and no-cross-project-inheritance test-pinned.
- **Warn-only `validate` prints `Validation: ok` on stdout yet exits 5** (`project_commands.go:66-68` vs `validation.go:98-104`): kept as an observation, not a defect — the exit-class table documents "validation/reference error" whenever findings exist, the docs never equate the stdout status string with exit 0, no in-tree script parses the string, and the only warn producer (delivered sprint with absent dir) is genuinely actionable. Worth a docs sentence someday.
- **Discovery admits nested/partially-scaffolded dirs as projects**: explicitly pinned by test and help text ("direct non-hidden project roots"); shape enforcement deferred to status/validate by design.
- **No size bounds on catalog/roadmap reads**: inputs are local, user/trusted-agent authored; no attacker-controlled source → per doctrine not a finding.
- **Duplicate-slug delivery returns an error even when all matches are already delivered**: strictness on a malformed document; acceptable, downstream surfaces category `roadmap_reconciliation` cleanly.