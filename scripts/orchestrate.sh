#!/usr/bin/env bash
set -uo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
export PATH="$HOME/go-toolchain/bin:$HOME/.opencode/bin:$PATH"
parallel="${QG_PARALLEL:-6}"
runner="${QG_RUNNER:-agentwrap}"
idle_timeout="${QG_IDLE_TIMEOUT:-20m}"
task_timeout="${QG_TASK_TIMEOUT:-90m}"
if [[ ! -x ./bin/qgauntlet ]]; then go build -o bin/qgauntlet ./cmd/qgauntlet || exit 1; fi
./bin/qgauntlet recover || exit 1
./bin/qgauntlet doctor || exit 1
while true; do
  stage="$(./bin/qgauntlet next)" || exit 1
  if [[ "$stage" == "complete" ]]; then
    ./bin/qgauntlet index || true
    ./scripts/checkpoint.sh complete || true
    echo "quality gauntlet complete"
    exit 0
  fi
  echo "=== stage: $stage ==="
  if [[ "$stage" == "baseline" ]]; then
    ./bin/qgauntlet baseline; rc=$?
    ./bin/qgauntlet index || true
    ./scripts/checkpoint.sh baseline || true
    [[ $rc -eq 0 ]] || exit $rc
    continue
  fi
  max_passes=15; max_attempts=15
  if [[ "$stage" == "surface-review" ]]; then max_passes=30; max_attempts=30; fi
  pass=1
  while [[ $pass -le $max_passes ]]; do
    ./bin/qgauntlet run --stage "$stage" --runner "$runner" --parallel "$parallel" --retry-failed --max-attempts "$max_attempts" --idle-timeout "$idle_timeout" --task-timeout "$task_timeout"
    rc=$?
    ./bin/qgauntlet index || true
    ./scripts/checkpoint.sh "$stage-pass-$pass" || true
    after="$(./bin/qgauntlet next)"
    [[ "$after" != "$stage" ]] && break
    echo "stage $stage still incomplete after pass $pass (run exit=$rc); retrying" >&2
    pass=$((pass+1))
  done
  if [[ "$(./bin/qgauntlet next)" == "$stage" ]]; then echo "stage $stage exhausted retry passes" >&2; exit 1; fi
done
