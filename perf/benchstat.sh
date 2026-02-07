#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "Usage: perf/benchstat.sh <old.txt> <new.txt> [more files...]" >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"${ROOT_DIR}/bin/go-local" run golang.org/x/perf/cmd/benchstat@latest "$@"
