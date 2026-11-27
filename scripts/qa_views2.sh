#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

out_dir=".analysis/views2"
mkdir -p "$out_dir"
log="$out_dir/QA-REPORT.md"

{
  echo "# views_v2 QA report"
  echo
  echo "- Generated: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "- Go: $(go version)"
  echo "- Scope: \`go test -count=1 ./internal/report\`, \`go vet ./internal/report\`"
  echo
  echo "## Working tree"
  echo
  echo '```text'
  git status --short
  echo '```'
  echo
  echo "## Test result"
  echo
  go test -count=1 ./internal/report
  echo
  echo "## Vet result"
  echo
  go vet ./internal/report
  echo
  echo "## Synthetic fixtures"
  echo
  echo 'All `testdata/views2/**` fixtures are synthetic. The real attach test reads'
  echo '`.analysis/views2/attach-real` read-only and verifies raw SHA-256 values do not change.'
} 2>&1 | tee "$log"
