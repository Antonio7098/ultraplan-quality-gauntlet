The repo HEAD is one commit past the frozen target; I'll export the frozen commit to a scratch dir so the target repo stays untouched.Frozen tree exported. Now reading the primary source files.Now the deeper files — `run_control.go` and the `runcontrol` package internals.Code reading is largely complete. Now I'll verify the two strongest hypotheses empirically with scratch tests in the frozen copy (not the real repo): (1) `follow` silently streaming a partial journal after compaction, (2) RFC3339Nano-as-TEXT ordering inversions in SQLite.The partial-stream defect is proven end-to-end. Now verifying the companion `--after`-ahead behavior and the timestamp-ordering mechanism.The ordering test needs correcting — the predicate result actually confirmed pagination self-consistency; only display ordering inverts. Fixing the scratch test to assert the true behavior, and checking `main.go` wiring:All verification complete. Here is the review result.

---

# Review: `run-cli-observation` (security/misuse lens)

Reviewed at frozen commit `5d54525b` via read-only export to `/tmp/opencode/frozen` (target repo untouched). Context pack read, then all primary paths plus `runcontrol` internals (`sqlite.go`, `lifecycle.go`, `sanitize.go`, `local_log.go`, `model.go`, `retention.go`, `migration.go`, `id.go`, `metrics.go`, `errors.go`), callers (`operations.go`, `durable_operations.go`, `web/run_handlers.go`, `operation_handlers.go`, `tui/app.go`), docs, TRD §12.3/§18C, and the app-level tests. Two defect hypotheses were proven empirically with scratch tests run against the frozen tree.

## F1 — `run follow` silently begins partial replays and silently polls on unavailable cursors (contract violation)

**Claim.** The follow loop discards the pre-loop snapshot's replay facts and never compares `--after` against `OldestRetainedSequence`/`HistoryComplete`/`LastSequence`. It therefore does exactly what TRD §12.3 forbids: *"an unavailable cursor produces a typed retention-gap result … rather than silently beginning a partial stream."* The web door implements both typed results (`cursor_ahead`, `replay_gap` — internal/web/run_handlers.go:968-979); the CLI door has no counterpart.

**Observable bad outcomes** (all reproduced):
1. Compacted journal, `--after 0`: follow emits only the retained tail and exits 0 with no gap marker. Reproduced end-to-end: seed run with 3 message events + terminal event, advance injected clock past the 84 h compaction cutoff, `Compact(64)` deletes the messages, then `run follow <id> --after 0 --json` prints exactly `"sequence":4` and exits OK. Note this is not exotic: `compactRunJournal` advances the boundary *during active runs* once a run exceeds 4096 events / 16 MiB (retention.go:58-95, invoked from Append at sqlite.go:687), so joining any chatty in-progress run late yields a silently truncated timeline.
2. `--after` beyond `LastSequence` on a non-terminal run: polls at 1 Hz forever with zero output, zero feedback, until the run terminates or the operator interrupts. Reproduced with a 1.6 s cancellation context: clean exit 0, empty stdout, kept polling. The web API returns typed `cursor_ahead` 409 for the identical request.

**Trigger/preconditions.** (a) follow a run whose `oldest_retained_sequence > 1`; (b) pass `--after > LastSequence` while the run is active.

**Evidence.** internal/app/run_commands.go:152 (pre-loop `Snapshot` result discarded via `_`), :162-182 (`Events` then termination check `snapshot.Lifecycle.IsTerminal() && after >= snapshot.LastSequence` — no gap/ahead checks anywhere), internal/runcontrol/sqlite.go:819-821 (events below the boundary simply absent from the query result), retention.go:88-92 (boundary advance), TRD.md §12.3 (workspace commit `ab12dc38`) and §18C failure-matrix row "cursor expiry".

**Execution path.** `runFollow` → `Snapshot` (validated, then thrown away) → loop{`Events(after,512)` → print → `Snapshot` → terminate-or-wait}. Nothing in the loop consumes `snapshot.OldestRetainedSequence` or `HistoryComplete`.

**Existing controls / counter-evidence searched.** `run show` does render `Oldest retained sequence` and `History complete` (run_commands.go:417-421), and cli-reference.md:492-520 only promises "replays committed events"; the data needed for detection exists and another door exposes it — but TRD §12.3 states the replay contract for Sprint-35 product-facing records generally (not per-transport), §18C's required matrix includes cursor expiry, and the web implementation demonstrates intended semantics. Docs' "compacted record remains truthful about its current snapshot" covers `show`, not silent partial streams.

**Severity / confidence.** Medium (operator draws wrong conclusions from an incomplete timeline presented as complete; contract violation on a primary observation door) / behavior High (empirically proven), contract applicability Medium-High.

**Regression proof.** My scratch tests `zz_gauntlet_follow_gap_test.go` and `zz_gauntlet_follow_ahead_test.go` (in `/tmp/opencode/frozen/internal/app/`, passing) assert current defective behavior; after a fix they should be inverted to require a typed gap/ahead marker (mirroring `cursor_ahead`/`replay_gap`) on stderr/JSON and non-silent exit or warning line.

## F2 — RFC3339Nano timestamps stored as TEXT misorder under SQLite BINARY collation

**Claim.** `formatTime` uses Go's `RFC3339Nano` layout, which trims trailing fractional zeros (sqlite.go:1049). `runs.updated_at`/`finished_at`/`accepted_at` are TEXT ordered lexicographically (`ORDER BY updated_at DESC, run_id DESC`, lifecycle.go:233), so a whole-second encoding (`…T10:00:05Z`) sorts as *newer* than a later neighbor with fractional seconds (`…T10:00:05.1Z`) because `'Z' > '.'`.

**Observable bad outcome.** Proven with the project's own SQLite driver: DESC query returns the chronologically older row first; `run list` can present two runs in inverted recency order whenever their encodings differ in fraction length with prefix relationship. Same collation feeds `retentionCandidates`' string-compared cutoffs (`finished_at <= formatTime(cutoff)`, retention.go:163) and reconcile scan order — sub-second boundary skew only.

**Counter-evidence searched.** Keyset pagination is *not* broken: I verified `updated_at < ?` uses the same BINARY collation as `ORDER BY`, so cursors derived from stored strings page consistently (no skips/duplicates) — my second assertion confirms exclusion consistency. Practical trigger frequency on Linux ns clocks is low (needs whole-second/trailing-zero timestamps meeting a prefix-compatible neighbor); injected/test clocks and coarse-clock VMs make it realistic.

**Evidence.** sqlite.go:1049 (`formatTime`), lifecycle.go:230-233, 255 (cursor built from raw stored string — consistent), retention.go:163. Scratch test `/tmp/opencode/frozen/internal/runcontrol/zz_gauntlet_order_test.go` reproduces the inversion.

**Severity / confidence.** Low (display-order correctness; no data loss) / mechanism High, field frequency Low-Medium.

**Regression proof.** A fix (fixed-width fraction formatting, or numeric/julianday comparison) makes the scratch test fail today's inversion expectation.

## Defended non-issues (checked, with counter-evidence)

- **Cancellation reason trust transition**: store validates only bounds/`\x00\r\n` (lifecycle.go:77-80), but every reachable caller is constrained — CLI whitelist (run_commands.go:394-401), web/TUI hardcode `"user_requested"` (run_handlers.go:898,923; operation_handlers.go:209,392; tui/app.go:110,124), internal fixed string (sprint_usecases.go:993). No attacker-controlled source today; latent only.
- **Direct event INSERTs bypassing `sanitizeEventDraft`** (RequestCancellation/AcknowledgeCancellation/ProposeTerminal/reconcileUnclaimed, lifecycle.go:106,167; sqlite.go:764; lifecycle.go:452): payloads are code constants, bounded via `marshalBounded`; the two-layer gate holds for all currently reachable writers.
- **Support export leakage**: O_EXCL|0600, ≤1 MiB pre-write check, payload-free event headers, allowlisted evidence classes, log-field redaction to `[omitted]` (local_log.go:76-83), symlink rejection; test pins 0600 + absence of seeded payload and workspace root (run_commands_test.go:47-65).
- **Cursor/ID injection**: cursors ≤1024 decoded bytes, canonical RunID base32 grammar, timestamp re-validation, fully parameterized SQL (lifecycle.go:278-291; id.go:73-89).
- **`Events` limit abuse through `RunUseCases`**: clamped 1..512 (sqlite.go:813-818).
- **ANSI escape reachability of raw-printed target fields**: sources are slug-validated (operations.go:574-590) or operator's own argv; serve enforces loopback-only bind (serve_commands.go:132) — no cross-user taint under the local trust model.
- **Symlink/chmod TOCTOU in `.ultraplan`**: requires write access inside a 0700 owner-only directory, i.e., same uid already controls the files.
- **Read-path startup Reconcile mutating durable state**: intended reconciliation-at-open per TRD §18C, not an inspection-side defect.

## Minor notes (not filed as defects)

- Ctrl-C landing *inside* a contended store call (busy_timeout up to 5 s) exits 6 with an error line instead of follow's clean observation-only exit 0 — `ctx.Err()` passes through classifyStoreError unwrapped (sqlite.go:1103-1105) and only the select arm swallows it (run_commands.go:188-193).
- `run diagnostics --json` sets envelope `status` to `health.Status` while the other four subcommands hard-code `"ok"` (run_commands.go:264 vs :91,:125,:221,:226) — unpinned because run envelopes are outside the stable-JSON list (cli-reference.md:474-491).
- Support-export size check measures pre-newline bytes (:352 vs :366) — off-by-one at the exact 1 MiB boundary only.

Verification: `go test ./internal/app -run 'TestGauntletFollow' -count=1` and `go test ./internal/runcontrol -run TestGauntletRFC3339NanoTextOrdering -count=1` both pass against the frozen tree (scratch copies only; original repos unmodified).