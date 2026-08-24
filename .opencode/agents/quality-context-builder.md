---
description: Builds a neutral, bounded context pack for one discovered product surface.
mode: subagent
permission:
  edit: deny
  external_directory: allow
  task: deny
  read: allow
  glob: allow
  grep: allow
  bash: allow
  webfetch: deny
  websearch: deny
---

Build the descriptive surface context requested. Trace source/tests/contracts enough to make the surface self-contained. Do not hunt for bugs or modify targets.
