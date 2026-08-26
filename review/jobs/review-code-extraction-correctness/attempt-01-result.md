Context pack read. Now inspecting the actual source and tests.Context absorbed. Now verifying wiring details and hunting specific hypotheses empirically.Building fixture workspace in `/tmp/opencode` to empirically probe hypotheses: symlink escape via basename fallback, unbounded range expansion, missing-table double-count, header-only table status, fenced-block scanning.Frozen commit == HEAD, tree clean on surface paths. Running probes:A and B both confirmed. Continuing with C/D/E plus a direct-path variant of the symlink leak:A2 also leaks (explicit-path form), but there are two anomalies to pin down: the stray `validation failed` line in A2, and why D classified as validation. Re-running cleanly with separated streams:All hypotheses now pinned down. Verifying the baseline suite is green and checking contract wording for the validation/partial split:Contract confirmed at reasoning.md:176 (Decision 6): "symlink-resolved escapes are unresolved validation diagnostics, not silently followed." One last characterization probe for the range-expansion severity:All evidence gathered. Findings below.

---

## Findings

### F1 — Basename fallback silently follows file symlinks that escape source roots (contract violation; content disclosure)

**Claim.** The direct-path resolution gate rejects symlink-resolved escapes (`existingContainedFile` → `containedPath`, resolver.go:101-111, 113-134, EvalSymlinks-backed), but the basename fallback accepts them: `basenameMatches` (resolver.go:144-169) collects any `DirEntry` whose name matches where `entry.IsDir()` is false — which includes symlinks to files outside the root — and acceptance checks only lexical `inside(source.Root, path)` (resolver.go:90). The matched path is then read by `readSnippet` via plain `os.ReadFile` (service.go:113), following the link.

**Observable bad outcome (reproduced against the built binary).** With `sources/repo/leak.go -> /tmp/opencode/outside/secret.txt` and report citing `` `leak.go:1` `` (or explicit form `sources/repo/leak.go:1`, which falls through direct-gate rejection into the fallback):
```
Resolved: sources/repo/leak.go
    1: TOPSECRET-line1
Unresolved: 0        exit=0 (status "ok")
```
Content from outside the workspace is emitted as a successfully audited snippet. The same citation shape through the *direct* path gate is correctly rejected — two resolution paths with different security semantics.

**Contract.** Sprint 13 reasoning.md Decision 6 (:176): "`..` escapes, absolute paths outside accepted roots, or **symlink-resolved escapes are unresolved validation diagnostics, not silently followed**." TRD doctrine: commands must reject paths escaping the workspace. Also violates the surface's own invariant (context pack §9.1). plan.md:113 claims symlink-containment test coverage `[x]`; no such test exists.

**Trigger/preconditions.** An escaping file symlink anywhere under a resolved source root, plus a citation whose basename matches it. Plausible sources: agent-written trees or cloned material under `studies/<s>/sources/**`; the report itself only supplies the basename.

**Severity:** medium (cross-boundary read marked ok; requires symlink presence). **Confidence:** high (reproduced end-to-end; no post-acceptance re-check anywhere).

**Regression test:** fixture with `sub/link.go -> /outside/x` cited by basename; assert unresolved ("symlink escape" or "file not found"), status partial/ok-free output contains no target content. Today it leaks.

### F2 — Report-controlled unbounded range expansion crashes the process (panic/OOM)

**Claim.** `parseLineSpec` eagerly materializes `make([]int, 0, end-start+1)` and appends every element (parser.go:77-81) before any file-size knowledge; `end` is bounded only by `int64`. Above ~3.5e13 lines this panics; below it, memory/time scale linearly before the reference fails anyway at `readSnippet`.

**Reproduced:**
- `` `main.go:1-40000000000000` `` → `panic: runtime error: makeslice: cap out of range` at parser.go:77; process dies with raw stack trace, exit 2 — not a classified outcome, `failOrOK` never runs, no output.
- `` `main.go:1-200000000` `` → 1.27 GB RSS, 5.34 s wall clock, then "line 5 out of range" (exit 8). Linear scaling ⇒ ~1e10 exhausts memory.

**Bad outcome:** a single crafted (or agent-hallucinated) citation turns an audit command into an unrecoverable crash or OOM event instead of a per-reference diagnostic that AC-59 requires for "malformed line specs".

**Counter-evidence searched:** no cap on range magnitude anywhere (`parsePositiveLine` checks only n≥1, parser.go:97-103); list form is bounded by citation length (safe); tests cover inverted ranges but not magnitude.

**Severity:** medium-low (local DoS/crash). **Confidence:** certain (reproduced both regimes).

**Regression test:** cite `f.go:1-100000000000000` expecting a "malformed/out-of-range line spec" diagnostic; plus a bound assertion (e.g., reject ranges above file size or a fixed max) so expansion can't precede bounds knowledge.

### F3 — Aggregate `Unresolved` double-counts missing-source-table failures and mixes categories

**Claim.** For a tableless report, `extractReport` appends one report-level diagnostic (service.go:57) *and* one identical-reason `Unresolved` per reference (service.go:66); `Extract` folds both into `Result.Unresolved` (service.go:20-24). Reproduced: 2 references → `Unresolved: 3` in text output; JSON shows 3 entries for 2 failed citations (header-only variant: 2 entries for 1).

**Bad outcome:** the summary cardinality operators/scripts triage on overstates distinct failures and conflates report-level conditions with per-reference ones; a consumer counting `unresolved[]` to enumerate broken citations gets refs+1.

**Counter-evidence:** JSON also carries `references[].unresolved` separately, so the data exists to disambiguate — but the aggregate field and the rendered `Unresolved: N` line are the natural reading and are wrong as counts of anything.

**Severity:** low (misleading output). **Confidence:** certain (arithmetic, reproduced).

**Regression test:** tableless report with N citations asserting `len(result.Unresolved)` equals the intended semantic (N references, or documented N+1) — currently undocumented and inconsistent with the per-reference view.

### F4 — Parser ignores Markdown structure: citations and source tables inside code fences are extracted as real inputs

**Claim.** `inlineCitationPattern.FindAllStringSubmatch(content, -1)` (parser.go:28) and the line-by-line table scan (parser.go:109) run over raw content with zero fence tracking. Reproduced: a report containing only ```go-fenced example `` `main.go:2` `` yields a phantom unresolved reference and validation exit 5; symmetrically, an example sources table inside a fence would be parsed as real source roots (resolvable ones would even steer resolution).

**Bad outcome:** a report that merely documents citation/table syntax (a natural thing for agents writing how-to sections) fails with phantom references or resolves against bogus roots; the parser already respects Markdown enough to ignore `[link](main.go#L1)` (test :63-90), making fence-blindness an internal inconsistency, not a design constant.

**Counter-evidence:** no AC/TRD line mandates fence awareness (pack unknown #3); fixtures never cover fences. So this is an edge-case correctness gap, not a contract breach — hence low severity. **Confidence:** high mechanically, medium practical frequency.

**Regression test:** report quoting `` `x.go:1` `` and a sample table inside fences; expect References: 0 / Sources parsed only from real tables (or an explicit documented decision that fences are scanned).

---

## Defended non-issues

- **Header-only/broken source tables vs missing table:** all three variants (absent, header-only, every-root-unresolvable) classify as validation exit 5 — verified empirically (JSON probe). Consistent with AC-53's "no *parseable* sources table" wording; pack's #7 boundary is conformant, not a defect.
- **Direct-path symlink gate itself:** correctly rejects escaping links via EvalSymlinks before fallback ever runs (evidenced by A/A2 flow reaching the fallback). The TOCTOU symlink-swap window between `containedPath` and read is real but inherent to a non-atomic local audit tool with static assumptions (pack #6); no concurrent adversary model on this surface — noted limit, not reported as defect.
- **Output-before-status ordering:** full results reach stdout/file on exits 5/8 (verified in probes C/D/E; matches Decision 5 and tests).
- **Arg parsing edges:** `--output=x`, last-flag-wins, unknown-flag rejection, `-h` only as sole arg — all behave as help text and usage class promise.
- **Determinism:** ref/source/basename sort keys verified in code; JSON field order struct-fixed; basename cache correctly invocation-scoped per Decision 6.
- **List-spec memory:** `,` lists allocate proportionally to citation length (report-bounded); no blowup analogue to F2.
- **Baseline:** `go test ./internal/codeextract ./internal/app` green; surface tree clean at frozen commit `50d6f0d`.

**Summary:** 4 findings (1 security/contract violation, 1 crash vector, 2 low-severity output/parsing defects), each reproduced against the frozen implementation. Highest-priority fix is F1 (apply the same EvalSymlinks containment to basename-fallback acceptances) and F2 (bound ranges before expansion).