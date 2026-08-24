#!/usr/bin/env bash
set -u
stage="${1:-progress}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
git add -A review
if git diff --cached --quiet; then exit 0; fi
git commit -m "review: checkpoint ${stage}" || exit 0
if [[ "${QG_PUSH:-1}" == "1" ]]; then git push || echo "warning: checkpoint committed locally but git push failed" >&2; fi
