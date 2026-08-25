This job was previously attempted (15 times, all failed per the index) and a stale map exists in `review/maps/` written against the pre-bind target commit `f0fcd0c`. Current frozen target is `f73c4dc`. Let me scope what changed between those commits before launching discovery workers.Delta is tiny (`prompt_context.go` budget + agentwrap dep bump), so prior maps are structurally relevant but this must be an independent pass verified at `f73c4dc`. Setting up the plan and launching bounded discovery workers.Frame confirmed: 17 packages, ~54k LOC production Go, clear state-file families. Launching five bounded discovery workers in parallel.All five briefs returned. Now spot-checking the load-bearing spine claims directly at `f73c4dc`.Spine claims all verified at the frozen commit. Composing and writing the map now.Job `map-02-state-authorities` complete. Map written to `review/maps/map-02-state-authorities.md` (replacing the stale pre-bind version) and to `review/jobs/map-02-state-authorities/attempt-01-result.md`. Target and workspace untouched.

**Map summary — 16 candidate surfaces across 6 domains, 9 seams:**

| Domain | Surfaces | Risk |
|---|---|---|
| R · Run-control & product-state SQLite | run-lifecycle-fencing; recovery-migration-retention; product-state dual-home seam | critical, high, high |
| S · Study state | runloop-scheduler-state; dual-home checkpoint parity; session checkpointing/stores | critical, high, normal |
| F · Sprint flow/execute | flow-stage-machine-publication; mutation-lease-reconciliation; execute-resume-state | high ×3 |
| V · Verification/QA | verify-digest-gating; qa-private-store-fencing; review-smoke-session artifacts | critical, high, normal |
| W · Serving ephemera | web-operation-hub-shutdown-markers; controlled-runtime fencing wrapper; runtime-store hygiene | high, high, normal |
| C · Context tier | config-precedence; workspace-layout-gitpub | normal |

**Highest-value structural facts for downstream phases:**
- **One file, two packages**: `runcontrol` and `productstate` hold independent pools on `.ultraplan/run-control.db`; productstate verifies no pragmas, never pings, has no Close/versioning.
- **Existence-based dual-home activation**: DB file + row silently flips three state families to DB-authoritative with asymmetric mirror rules (study mirrors only on Complete); loaders are DB-first with unconditional row-wins.
- **Coverage void**: `productstate` is at 0.0% coverage with no test file, and all three DB-authoritative branches are unreachable from existing suites (no fixture creates the DB outside runcontrol's own tests).
- **Durability tiers classified**: `.stage-sessions.json` is the only atomic-rename write with no fsync; `summary.md`/`execute.md` are plain WriteFile; tasks.jsonl rewrites whole file per append, unbounded.
- **Shutdown semantics**: cleanup-uncertain markers written deliberately leaseless only after a 10s drain deadline, consumed fail-closed at next startup; single NotifyContext(INT,TERM) in main; TUI not ctx-torn-down.
- Verified frozen-delta at f73c4dc: 512 KiB prompt budget present; all spine claims spot-checked directly.