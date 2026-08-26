Context pack read; surface paths are identical between HEAD and the frozen commit, so I can review the working tree directly. Now reading the package sources and tests.Initial hypotheses forming. Now reading the rest of the package plus the CLI layer.Now the test files.Sources and tests read. Now checking helpers, the smoke call site, and contract docs to confirm or refute candidate findings.Two strong candidates confirmed in code. Verifying behavior empirically and checking remaining contracts.Contracts located. Now empirically probing the two rewrite-divergence candidates and the silent-section-drop candidate in a scratch copy of the repo.All probes confirm the hypotheses. Checking two downstream consequences to finish the evidence chain.Evidence chain complete. Running the full package test suites once to confirm baseline greenness, then verifying the target tree is untouched.Review complete. Target tree verified unmodified; all probes were run against a scratch copy in `/tmp/opencode/pg`.

# Findings — `project-catalog` (verification/operability lens)

## F1 — Delivery marking silently fails or corrupts roadmaps whose metadata diverges from the scanner's canonical form — **medium / high confidence**

**Claim:** `MarkRoadmapSprintDelivered` re-implements metadata recognition instead of sharing `ParseRoadmap`'s grammar, and the two disagree on casing, spacing, and fences.

**Proven behaviors** (scratch-copy probes):
- Parser lowercases keys (roadmap.go:236) and accepts `>status:`/`>Slug:` with no space (roadmap.go:230-231); the rewrite regex `^(\s*>\s*Status:\s*)` (roadmap_status.go:11) is case-sensitive and space-form-only. With a valid-per-parser `> status: active`, marking returns `(true, nil)` yet inserts `> Status: delivered` *above* the retained lowercase line — re-parse yields `active`. Same for `> STATUS:`. Every retry appends another duplicate line (unbounded growth, never effective).
- Parser ignores fenced blocks (roadmap.go:113-116); the scanner does not. A fenced example containing `> Status:` between the slug line and first H4 gets rewritten inside the fence (content corrupted, real status still unset, `true` returned). This violates the function's own doc contract "preserves all other roadmap content" (roadmap_status.go:13-14).
- Fully-valid `>Slug:/ >Status:` (no space) → error `sprint "01-x" has no Slug metadata line` (roadmap_status.go:71) — false cause; the slug line exists.

**Trigger/path:** roadmap.md is agent/user-authored; nothing upstream canonicalizes it — `resolveSprintInputs` (internal/sprint/service.go:957-993) never parses the roadmap and the smoke gate checks only the catalog, so any of these forms reaches the rewriter at internal/sprint/smoke.go:45, which discards the `changed` bool. Result: smoke publishes success while the sole delivery record stays `active/planned`.

**Counter-evidence searched:** no product code writes status lines (review.go:1391 is a prompt placeholder); all tests use canonical form only, so the suite cannot catch the divergence; zero-sprint `(false,nil)` tolerance is documented intent (roadmap_status.go:33-37) and separate from this.

**Regression test:** parse→mark→re-parse round-trip over case/no-space/fenced variants asserting final parsed status `delivered`, idempotence, and byte-equality of all other lines.

## F2 — Catalog rows under unrecognized/mistyped headings vanish silently; `validate` says ok — **low-medium / high confidence**

index.go:27-34 sets `section=""` for unrecognized `## <name>` and skips subsequent rows without any finding. Proven: a full Smoke Harnesses table under `## Smoke Harness` produces 1 entry, 0 findings. Consequence chain: `project validate` exits 0 with `Validation: ok`; the sprint-smoke gate later fails with `exactly one smoke harness is required — Add one current Smoke Harnesses row` (internal/sprint/smoke_protocol.go:123) even though the row exists, pointing users away from the heading typo. The roadmap parser in the same package emits issues for unknown subsections/metadata (roadmap.go:184,238) — the asymmetry is intra-package. Regression: emit a finding when a recognized-table-looking block sits under an unrecognized heading.

## F3 — Second table under one section turns its header row into a phantom catalog entry that passes the planning gate — **low / high confidence**

Headers reset only at `##` (index.go:28,40), so a second blank-line-separated table's header row parses as data: entry `{Name:"Document", Path:"Path"}` with **zero** parse findings (probe). The downstream hard gate rejects only parse findings (service.go:984-990), so phantoms enter stage manifests; validate later shows the confusing `entry="Document" path="Path"` not-found error.

## F4 — Warn-only validate exits 5 while stdout prints `Validation: ok` — **low / high confidence (behavior), documented nowhere**

project_commands.go:66-68 exits `ExitValidation` whenever any finding exists; validation.go:98-104 computes `ok` unless an error exists. A warnings-only run prints `Validation: ok` and exits 5. cli-reference.md pins only the generic class table (5 = "validation/reference error"). Exit-code-driven scripts fail healthy projects; stdout-driven scripts see ok. Fix: align printed status with the exit class or document warn semantics.

## F5 — Symlinked project directories are silently invisible to discovery — **low / high confidence**

discovery.go:40 relies on `DirEntry.IsDir()`, which is false for symlinks-to-directories; probe confirms `DiscoverProjects` returns nothing with no diagnostic. Affects list/status/dashboard summaries, reconcile enumeration (internal/platform locks.go:129), and storage migration. Not a containment break (resolution is consistently blind), purely an undocumented silent filter — unlike the deliberate hidden/file/nested filters, which are test-pinned.

## F6 — One unreadable project file blanks the whole dashboard/web project summaries — **low / medium confidence**

ProjectSummaries aborts on the first per-project `Status` error (internal/app/project_usecases.go:39-42), so a single permission-denied roadmap.md removes all projects from the dashboard/web pages instead of degrading per-project. Possibly intentional fail-fast; flagged as a resilience gap.

## F7 — Reverse-direction area-reasoning contract is unverified — **low / medium confidence**

user-guide.md:66-72 says reasoning docs under `projects/<p>/reasoning/` "must be listed in that project's project-index.md", but validation checks only catalog→filesystem existence (validation.go:60-80); unlisted files produce no finding and are invisible to `status`. Users who place a doc correctly but omit the row get `ok` while agent stages silently skip their specialized reasoning.

## Defended / non-issues

- **Zero-sprint `(false,nil)` legacy tolerance** (roadmap_status.go:33-37): commented intent; loud `slug not found` error covers the governed case.
- **Lexical `ResolveInside` vs EvalSymlinks divergence:** symlink escapes let self-owned workspace files into prompts/joins; single-user boundary, harness paths already EvalSymlinks-contained (validation.go:168-196). Tracked seam, no crossing consequence.
- **No size caps on catalog/roadmap reads:** O(file) memory on local user-authored markdown; not a realistic hazard.
- **RefError → exit 5 for missing/ambiguous refs**, external-URL catalog skipping, and last-writer-wins rename semantics: consistent with pinned tests and docs.

Baseline suites green (`go test ./internal/project ./internal/app`).