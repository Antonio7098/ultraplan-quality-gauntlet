Target verified clean at frozen commit. Prior maps (state-authorities, test-topology) reviewed for overlap — my lens is execution *lifecycles*. Launching six bounded review-worker discoveries in parallel.All six discoveries returned. Now direct spot-checks of the spine claims at the frozen SHA before synthesis.One more precision check on the retry helper semantics before writing the map.All spine claims verified at `f73c4dc`. Writing the surface map.map-04-execution-paths complete. Deliverables:

- `review/maps/map-04-execution-paths.md` (canonical)
- `review/jobs/map-04-execution-paths/result.md` (job result)

**Method**: target verified clean at `f73c4dc`; six parallel bounded review-worker discoveries (app durable-op spine, runtime→OpenCode binary hop, study run-loop, sprint flow/execute/QA/review/smoke, web+TUI hub lifecycle, observation/process/gitpub) + direct spot-checks of every load-bearing spine claim at the frozen SHA. Facts only; no findings.

**Map shape**:
- **Part 0 — shared skeleton**: two-tier durable spine (outer operation run per command via `AcceptOperation`; inner runtime run per agent call via `controlledRuntime.StartRun`, correlated by `ParentRun`), with verified mechanics: 250ms content-hash progress coalescing, persistence-gated event delivery, 1s/5s/10s control cadence under 15s leases, detached-30s terminal proposals.
- **11 surfaces in 6 domains**: durable-core (DC1 critical, DC2), agent-execution (AE1), study (ST1), sprint-delivery (SP1–SP5), interfaces (IF1 high, IF2), observation/publication (OB1/OB2).
- **9 seams**, notably: outer↔inner double fencing, the unobserved-after-5s-grace binary teardown hop, QA writer-token→durable-heartbeat handoff, marker-before-projection shutdown ordering, commit-then-publish invariant.
- **12 lifecycle invariants** for downstream reviewers (persist-before-launch, dedup-before-capacity, single-winner terminal CAS, three dedup keying regimes on one alias constraint, etc.), each anchored to file:line and pinning tests.

Key structural facts surfaced for later phases: no app-level test drives the heartbeat tick loop over time; the entire runtime→binary hop below `Adapter.StartRun` is fake-tested only (NewOpenCode, checkpoint/VACUUM deletion, Health/Capabilities zero direct coverage); challenger phase exists but has no production call site; `runs list` has no record_state predicate so tombstoned runs stay listable.