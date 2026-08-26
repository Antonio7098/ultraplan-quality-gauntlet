Context pack read; source inspected. Now checking the app wiring and the authoritative contract docs before running experiments.Contract confirms: resolution "must only read local files under parsed source roots", and out-of-range lines "are reported as unresolved references". Now checking workspace discovery wiring and the report template, then building the binary for empirical probes.Both candidate findings now get empirical verification. Setting up fixtures and running the built binary (artifacts only under /tmp/opencode; target untouched).Finding 1 reproduced: `/etc/passwd` content extracted through a workspace-internal symlink via the basename fallback, status `ok`. Now running control tests (direct-path form), the FIFO variant, and the giant-range probes.The FIFO probe wedged the shell — checking for leftover processes and state, then re-proving the hang with a hard kill guard.Worse than hypothesized: the fully-qualified citation `sources/repo/outside.txt:1` also leaks — the symlink gate correctly rejects it during direct resolution, then the basename fallback re-admits the same symlink without evaluation. Now the giant-range probes.All probes complete; target repo verified untouched (`git status` clean except pre-existing untracked `.ultraplan/`). Final report:

---

# Review: `code-extraction` (job `review-code-extraction-assurance`)

Target: `ultraplan-go` @ `50d6f0d` (frozen paths identical). Method: independent source/test inspection + black-box reproduction against a binary built from the frozen tree into `/tmp/opencode`. No subagents. All fixtures under `/tmp/opencode/gauntlet`; target and workspace repos unmodified.

## Finding 1 — HIGH / Security: basename fallback silently follows symlinks outside source roots → arbitrary file read

**Claim.** When direct-path resolution fails, `resolve` falls back to `basenameMatches`, which collects *any* non-directory directory entry — including symlinks — without evaluating their targets, and returns the raw walked path which `readSnippet` then opens. This defeats the symlink-aware containment (`containedPath`) that correctly guards direct resolution, and lets a report citation extract arbitrary operator-readable files from anywhere on disk.

**Observable bad outcome (reproduced).** With `ws/sources/repo/outside.txt -> /etc/passwd` (symlink escaping the workspace):

- Citation `` `outside.txt:1` `` → `"status": "resolved"`, snippet line 1 = `root:x:0:0:Super User:/root:/bin/bash`, overall `"status": "ok"`, exit 0.
- Worse: the fully-qualified TRD-canonical citation `` `sources/repo/outside.txt:1` `` **also leaks**. Trace: `existingContainedFile` (resolver.go:76, via `existingContainedFile`→`containedPath`) correctly evaluates the symlink, finds `/etc/passwd` outside the root, and rejects it — then step 4's basename fallback re-admits the very same entry by name (resolver.go:87–94) with zero symlink evaluation. The escape gate is executed and then undone.

**Trigger/preconditions.** Any report citation whose basename matches a symlink (or later, any special file) located inside a resolved source root, whose target lies outside. Symlinks inside source trees are ordinary occurrences (checked-out repos, agent-generated workspaces). Report content is report-controlled input (context pack §10).

**Evidence / execution path.**
- resolver.go:150–163: walk callback skips only dirs and ignored names; `entry.IsDir()` is false for symlink entries (`WalkDir` does not follow), so symlinks are collected as matches. No `entry.Type().IsRegular()` check; the only containment is the tautological lexical `inside(source.Root, path)` (path came from walking that root).
- resolver.go:88–93: single match returned verbatim; service.go:91 → service.go:112–116 `os.ReadFile(resolvedPath)` follows the link.
- Counter-controls checked and ruled out: `existingContainedFile` (resolver.go:101–111) does evaluate symlinks — it works, as the control shows rejection at step 2 before the fallback re-admits; nothing downstream re-validates; no test fixture exercises symlinks in the fallback (existing suite passes with the defect present).

**Contract violation.**
- Sprint 13 requirements Constraints: "Reference resolution must only read local files under parsed source roots … must not traverse outside intended roots."
- Sprint 13 reasoning.md:176 (Decision 6): "…symlink-resolved escapes [are] unresolved validation diagnostics, **not silently followed**."
- Sprint 13 review.md:69/71 marks SEC-FILES-001 "satisfied" on the strength of `containedPath` — the fallback path escapes exactly that control.

**Severity / confidence.** High (arbitrary-file-read into audit output, driven by report text plus any in-tree symlink; content also lands in the optional `--output` file). Confidence: High — reproduced end-to-end both by bare basename and by source-prefixed citation.

**Regression test.** Fixture: source root containing `secret.txt -> <tmp dir outside workspace>/secret.txt`; report citing both `` `secret.txt:1` `` and `` `sources/repo/secret.txt:1` ``. Assert both references unresolved (e.g., reason "path escapes source root") and that no snippet contains the outside file's marker text. Fix: require `entry.Type().IsRegular()` in `basenameMatches` and/or run `existingContainedFile(root, rel)` on each candidate match before acceptance.

## Finding 2 — MEDIUM / Reliability: report-controlled giant line ranges crash the process at parse time (OOM fatal, exit 2)

**Claim.** `parseLineSpec` eagerly materializes the full explicit `[]int` for every range before any file size is known (parser.go:77–80), and `parsePositiveLine` (parser.go:97–103) accepts values up to `MaxInt64`. A single citation like `` `main.go:1-99999999999` `` demands a ~800 GB slice.

**Observable bad outcome (reproduced).**
- Range `1-99999999999`: `fatal error: runtime: out of memory` at `runtime.makeslice` — process aborts with a goroutine dump, **exit code 2** (colliding with `ExitUsage`, so scripts misread the crash as a usage error), no diagnostic, no partial output.
- Range `1-30000000` (~240 MB) under a 128 MB address-space limit: `fatal error: failed to reserve page summary memory` — same abort class. On constrained machines even plausible fat-fingered ranges kill the run.

**Trigger.** Any report passed to `ultraplan code` containing one oversized range citation; N such citations multiply the allocation. Fully report-controlled input.

**Contract violation.** Sprint 13 AC: "Out-of-range line requests … are reported as unresolved references with source report context and do not silently disappear." An OOM abort is not an unresolved-reference diagnostic. Context-pack unknown #5 flagged the design; this confirms the concrete failure.

**Existing controls / counter-evidence.** `readSnippet` does bounds-check per requested line (service.go:119–122) — but only after the allocation has already happened. `end < start` and malformed-range checks don't constrain magnitude.

**Severity / confidence.** Medium (deterministic DoS of the audit command; misleading exit class). Confidence: High — reproduced, two distinct abort modes.

**Regression test.** Report citing `main.go:1-100000000000`; assert graceful result: reference unresolved with reason "line 1 out of range" (or a bounded-range validation diagnostic), status `partial`, bounded RSS/time. Fix: reject spans above a sane cap at parse time, or defer expansion and clamp against actual file length before materializing.

## Finding 3 — LOW-MEDIUM / Operability: fallback accepts non-regular files → indefinite hang

Same root cause as Finding 1 (`basenameMatches` has no regular-file filter). Reproduced: a FIFO named `pipe.txt` inside the source root, cited as `` `pipe.txt:1` ``, blocks `readSnippet`'s `os.ReadFile` in `open()` waiting for a writer — the process produced no output and remained alive past its `timeout 5` wrapper for the full 120 s observation window and had to be SIGKILLed. There is no context/timeout anywhere on this surface (`deps.ctx` unused), so a hostile or corrupt tree turns the audit into a permanent hang. Confidence High for the hang mechanism (blocked open with zero output; not traced further). Fix is identical to Finding 1's.

## Defended non-issues (counter-evidence searched)

- **Direct-path symlink handling is sound**: control run confirms `existingContainedFile`/`containedPath` reject escaping links; only the fallback bypasses it (Finding 1 scope).
- **TOCTOU between `containedPath` evaluation and `ReadFile`**: requires a local writer racing the invocation; not establishable from the threat model (report text cannot create symlinks mid-run). Not reported.
- **Scan-all-pipe-rows source-table false positives** (parser.go:105–142 treats every integer-led pipe row as a source row): grepped all real final reports in-tree; every numeric-led row found is a genuine sources table; the template's other tables are non-numeric-led (`| Field | Value |`). No concrete instance → not reported.
- **Fail-fast on unreadable reports**, **all-sources-unresolvable → validation(5)** boundary, **missing-table refs listed twice** in `Unresolved`, JSON HTML escaping, `rel()`'s `..`-prefix quirk: documented, deterministic, or cosmetic; no observable failure beyond the intended semantics.

## Test-gap summary

The suite (codeextract_test.go, code_commands_test.go) contains zero fixtures with symlinks, special files, or large ranges — all three findings ship green under `go test ./... -race`.