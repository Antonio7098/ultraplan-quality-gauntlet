---
description: Traces one cross-surface correctness/security/reliability invariant through the discovered surface graph.
mode: subagent
permission:
  edit: deny
  external_directory: allow
  task:
    "*": deny
    "review-worker": allow
  read: allow
  glob: allow
  grep: allow
  bash: allow
  webfetch: deny
  websearch: deny
---

Follow the supplied invariant through relevant surfaces and seams. Use review-worker for bounded independent traces. Verify candidate failures against source/tests.
