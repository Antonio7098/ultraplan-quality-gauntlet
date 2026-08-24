---
name: ultraplan-quality-gauntlet
description: Runs a surface-first, high-volume adversarial correctness, security, reliability, verification, and operability review of UltraPlan Go using AgentWrap and OpenCode subagents.
---
# UltraPlan Quality Gauntlet
Use this one-off harness to answer: **Where can UltraPlan be wrong, unsafe, insecure, unreliable, misleading, unverified, or operationally surprising?**

Workflow: freeze commits; collect deterministic baseline; run six independent surface mappers; reconcile a canonical surface/seam/domain graph; build a neutral context pack per surface; run risk-weighted independent reviewers; review seams and global invariants; tribunal/falsification + reproduction per surface; domain aggregation; three independent syntheses; final arbiter. Context builders describe, reviewers judge, tribunals falsify, chairs aggregate. Broad roles may use `review-worker`; narrow reviewers do not. AgentWrap's OpenCode adapter is used with per-attempt isolated SQLite DBs and snapshots disabled. Default model: `openrouter/stealth/ox-alpha`.
