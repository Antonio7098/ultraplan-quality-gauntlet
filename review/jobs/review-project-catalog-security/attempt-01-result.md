Context pack read. Note the target HEAD (`5d54525`) matches the assignment's frozen commit; the context pack was written against an earlier one, so I'll verify everything against actual source. Reading all primary files now.Source read. Now verifying key helpers, contracts, call sites, and the `.ultra/` convention before judging candidates.`.ultra/` is a real legacy on-disk layout (tests write files there), with prompts telling agents to treat it as equivalent to root-relative. Now reading the CLI contract docs and the smoke call site, plus all four test files.Delivery-marking test only pins canonical `Status` casing — my case-sensitivity hypothesis stands uncountered so far. Checking the remaining tests, exit-code constants, the real workspace layout (for the `.ultra/` path convention), and the scaffold templates that author roadmaps.Real catalogs contain fenced blocks and unrecognized sections — checking whether fence-blindness produces phantom entries on today's actual data, and what the real roadmaps' status lines look like.The Aren catalog legitimately references sibling-repo paths (`../Aren/...`) — worth testing against the real workspace. First, the Aren roadmap appears to have no status lines at all; reading both real roadmaps.This is significant: the real Aren roadmap uses `## Sprint N` headings (H2), `###` subsections, and has **no** Slug/Status metadata at all — a shape the closed-world grammar parses to *zero sprints with zero issues*. And the real `ultraplan-go/roadmap.md` apparently lacks `>` status/slug lines too. Verifying both empirically: building the target CLI to `/tmp` (read-only run against the real workspace) and reproducing the marker case-sensitivity defect in a scratch module.Real `ultraplan-go/roadmap.md` uses conformant H3 sprints but has **no** Slug/Status metadata lines at all. Running read-only validate against the real workspace to capture actual behavior:Both real projects fail validation today (171 and 3 findings) — loud, contract-consistent reporting of legacy/nonconforming data; that's the system working, not a defect. Now proving the delivery-marker case-sensitivity defect end-to-end in a scratch module (verbatim copies of `roadmap.go`/`roadmap_status.go`, target untouched), and checking `resolveSprintInputs` counter-evidence for the silent-skip path.Counter-evidence confirmed for context: sprint resolution reads directories, not roadmap sections — so nothing upstream of delivery marking validates roadmap conformance. Now the repro:Finding proven end-to-end. Continuing the sweep: checking whether workspace-level reasoning overrides are validated anywhere (validation.go:89 blind spot candidate), the user-guide exit-code contract, and the marker's history.Checking whether any smoke/review gate requires roadmap conformance (counter-evidence for F2's reachability), then baseline tests:All evidence gathered. Verifying I left the target untouched, then reporting.# Review report — `project-catalog` (security/misuse lens)

Target verified at `5d54525`; target repo left untouched (repro lives in `/tmp/opencode/markerrepro`, binary built to `/tmp/opencode/`). Baseline `go test ./internal/project ./internal/app` green. Behavior cross-checked against the real frozen planning workspace via read-only CLI runs.

---

## Finding 1 — Delivery marking silently corrupts roadmaps whose Status line uses any non-canonical casing (grammar-valid input)

**Severity:** Medium-high · **Confidence:** High (proven by repro against verbatim source copies)

**Claim.** `ParseRoadmap` accepts metadata keys case-insensitively (`roadmap.go:236` lowercases the key; `> status:` / `> STATUS:` / `> Status :` parse cleanly and emit **no issue**, so validation blesses such roadmaps), but `MarkRoadmapSprintDelivered`'s status-line detector is exact-case literal `Status:` (`roadmap_status.go:11` regex `^(\s*>\s*Status:\s*).*$` — note also no space allowed before the colon) while its slug detector *is* case-tolerant (`roadmap_status.go:56` uses `strings.ToLower`). For a grammar-valid roadmap with `> slug:` followed by lowercase `>status: active`, the marker finds no status line, takes the insert path, and inserts `"> Status: delivered"` **above** the existing status line. The parser's last-writer-wins metadata semantics then read the old lowercase line last: status stays `active`.

**Observable bad outcome** (all proven by failing repro tests):
- `MarkRoadmapSprintDelivered` returns `(true, nil)` — success — while the file still parses as not-delivered.
- The file is left with **two** status lines; every subsequent passing smoke run re-inserts another duplicate (idempotence broken, unbounded growth).
- Smoke publishes delivery (`smoke.go:39-47`) while roadmap/web/resume consumers see planned/active — a delivered sprint remains re-executable and the authoritative planning document permanently disagrees with flow-state.
- No diagnostic exists anywhere: validation emits no finding for the casing, and no code re-reads the file after the write (`smoke.go:45` discards the bool).

**Trigger/preconditions.** A governed roadmap whose sprint has a Slug line followed by a Status line spelled other than exactly `Status:` (lowercase, all-caps, or space-before-colon — all accepted by the parser with zero issues). Order matters: if the odd-cased line precedes the Slug line, insertion lands after it and works by accident — behavior is inconsistent for identical documents.

**Evidence & path.** `internal/project/roadmap.go:229-266` (case-insensitive keys, valid values → no issue) vs `internal/project/roadmap_status.go:11,52-76` (case-sensitive scan + insert-after-slug); sole caller `internal/sprint/smoke.go:39-47`. Counter-evidence searched: no pre-normalization of casing, no post-write verification, tests pin only canonical casing (`roadmap_test.go:103-132`).

**Regression test.** Extend `TestMarkRoadmapSprintDelivered` with `> status:`/`> STATUS:`/`> Status :` variants asserting: post-write file contains exactly one status-matching line, re-parse yields `delivered`, second call returns `changed=false`. Current code fails all three (repro: `/tmp/opencode/markerrepro/repro/repro_test.go`).

**Fix direction.** Make the status-line regex case-insensitive and colon-spacing tolerant (`(?i)^\s*>\s*status\s*:\s*`), matching the parser's own grammar.

## Finding 2 — Silent zero-parse tolerance lets smoke publish delivery that is never recorded, with no gate anywhere

**Severity:** Low-medium · **Confidence:** High on mechanism; Medium on reachability

**Claim.** `MarkRoadmapSprintDelivered` returns `(false, nil)` when the roadmap parses to **zero sprints** (`roadmap_status.go:32-37` "legacy tolerance"), and the only caller discards the bool (`smoke.go:45`). Nothing else in the chain consumes `ParseRoadmap` or gates on roadmap conformance — sprint resolution reads directories only (`service.go:956-991`), so a project can pass the entire governed chain and have delivery silently skipped whenever the roadmap's shape isn't recognized.

**Reachability is real, not hypothetical:** the frozen workspace contains exactly such a document — `projects/aren-phase-01-execution-lifecycle/roadmap.md` declares sprints as H2 under H1 waves with no Slug/Status metadata; `ParseRoadmap` yields 0 sprints and **0 issues** (phases without sprints are dropped silently, `roadmap.go:189-196`; non-`Sprint N:` H3s close silently, `roadmap.go:143-147`). Read-only validate run confirms: aren produces only 3 findings (missing sprints/, two `../Aren` catalog escapes) — none about the roadmap shape. If that project reaches a passing smoke, marking is skipped with zero signal, forever.

**Bad outcome.** Sprint completes smoke; roadmap never records delivered; web/status/resume treat it as runnable; no log, finding, or publication annotation distinguishes "legacy skip" from "marked".

**Counter-evidence.** The tolerance is deliberate and commented; today's other project would error loudly instead (slugless-but-nonempty roadmap → `roadmap_status.go:37`). That asymmetry (error for nonzero-parse, silence for zero-parse) is itself the defect: the silent branch absorbs mis-authored current-generation roadmaps along with legacy ones.

**Regression test.** Roadmap with H2 sprints + existing sprint dir: assert `MarkRoadmapSprintDelivered` errors (or reports a distinguishable result), and/or `smokeError("roadmap_reconciliation")` surfaces rather than `(false,nil)` being swallowed.

## Finding 3 — Warn-only validation exits 5 while stdout says `Validation: ok`

**Severity:** Low · **Confidence:** High

`runProject` exits `ExitValidation`(5) on *any* findings (`project_commands.go:66-68`) including warn-only results, while stdout prints `Validation: ok` from `StatusFromValidation` semantics (`validation.go:98-104`). Reachable state: delivered sprint whose directory was archived/deleted emits exactly one warn (`validation.go:130-148`, pinned by `TestValidateProjectStatusAwareSprintDirectoryChecks`) → exit 5 + "Validation: ok". Scripts honoring the documented exit classes correctly treat a healthy project as broken; humans/scripts scraping stdout are told ok. Neither `docs/cli-reference.md:119-143` nor user-guide pins severity-vs-exit, so this is an internal inconsistency, not a contract breach — but the two signals contradict each other on the same invocation. Minimal fix: print `Validation: warnings` (or exit 0 on warn-only), plus a contract line in cli-reference.

## Finding 4 — Catalog parsing lacks fenced-block awareness; display-only examples become governing entries

**Severity:** Low · **Confidence:** Medium (mechanism certain from source; trigger requires an example block)

Unlike the roadmap parser (fence tracking pinned by `TestParseRoadmapIgnoresFencedContent`), `ParseProjectIndex` has no fence handling and matches headings after `TrimSpace` (`index.go:24-35`), so a fenced/indented example containing `## Active Contract Pool` (or any recognized heading) plus table rows is ingested as real entries — steering downstream selection joins, the exactly-one-smoke-harness requirement, and path stat checks, with no distinguishing mark. Real catalogs already embed fenced text blocks (verified in the workspace index), so the distance to trigger is one documentation edit by a human or catalog-maintaining agent. Fold-in: the deprecated-field regex scans raw content equally blindly (`validation.go:45`). Fix: track ``` / ~~~ toggles and skip fenced regions, mirroring `roadmap.go:113-116`.

---

## Defended non-issues (checked, with counter-evidence)

- **Lexical-only containment vs EvalSymlinks divergence**: symlinked dirs pointing outside pass `ResolveInside`, but planting the symlink and authoring the catalog require the same local trust domain — no trust transition; smoke-harness paths do use EvalSymlinks containment correctly (`validation.go:168-196` resolves root+manifest, Rel-prefix check post-resolution). Seam-tracked; no exploitable boundary here.
- **Absolute harness roots anywhere on disk**: deliberate contract (deep-smoke protocol; pinned by test); manifest must exist inside the EvalSymlinks-resolved root, so arbitrary roots yield loud findings.
- **`.ultra/` prefix duality**: real on-disk legacy layout (fixtures write `.ultra/**` physically); normalization strips it only for the cross-project/area-doc checks while containment uses the physical path — consistent, and `.ultra/projects/<other>/...` *is* caught after stripping.
- **Unrecognized-section rows silently dropped** (e.g., the real "Available Studies" table): unstated in docs but never promised validated; tolerance is uniform.
- **Workspace-tier reasoning overrides unvalidated by `project validate`** (`validation.go:89` skips when project-tier absent): docs scope the verb to *project* overrides; resolution failures surface loudly at render time.
- **RefError→exit 5 mapping, prefix resolution, nested-dir discovery, last-writer-wins atomic rename (temp+fsync+rename, mode-preserving), `%q` output escaping, unbounded local file reads**: each either test-pinned design or below the substantive-defect bar (dir-fsync durability nit noted, not reported).

Highest-priority fix is Finding 1: one-line regex change, closes a silent-corruption hole in the package's only write path.