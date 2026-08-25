Commits match the frozen state. Reading primary paths now.The format is clear. Filling remaining gaps on dependencies and tests.All evidence gathered. Writing the context pack.Context pack complete: `review/surfaces/context-study-runloop-scheduler.md` (mirrored to `review/jobs/context-study-runloop-scheduler/result.md`).

Coverage summary:

- **Purpose**: 9 owned responsibilities — durable slot-refill scheduling, pressure-based admission/GC, retry taxonomy, atomic persistence, resume revalidation chain, PID locking + SIGINT cancel lane, fail-closed reconciliation, append-only ledger, resource diagnostics.
- **Control flow**: full RunLoop walkthrough with line refs (lock → load/reconcile/revalidate/restore → scheduler loop with memory/disk throttles and tiered backfill → runTask lifecycle → epilogue), plus CLI/web/TUI entrypoints, both cancel lanes, and the sibling `RunAll` batch variant.
- **State**: file vs DB authority routing (kind `study_run`), ancillary files, strict validation grammar.
- **Trust boundaries**: persisted state re-read as input, lock-PID-driven signaling, unwritten-marker semantics, sanitization heuristics, Linux assumptions.
- **Contracts**: Sprint 12 ACs/constraints, TRD applicability + §21.2 locks, ARCHITECTURE boundaries, Sprint 35 marked FUTURE-INTENT/partial.
- **Unknowns**: 15 neutral open questions, including that `RunLoopRequest.Continue` is populated by callers but never read by `study.RunLoop`.