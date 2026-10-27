#!/usr/bin/env bash
# Enforce the supply-chain pin (R6): every third-party `uses:` in
# .github/workflows must name a full 40-hex commit, not a mutable tag or
# branch. Local reusable workflows (`uses: ./.github/workflows/...`) are the
# one allowed exception.
#
# Run locally or from the verify workflow:
#   bash scripts/check-workflow-pins.sh
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
status=0

for wf in "$repo_root"/.github/workflows/*.yml; do
  [ -f "$wf" ] || continue
  while IFS= read -r ref; do
    [ -n "$ref" ] || continue
    case "$ref" in
      ./*) continue ;;
    esac
    if ! printf '%s\n' "$ref" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*@[0-9a-f]{40}$'; then
      printf 'unpinned action in %s: %s\n' "${wf#"$repo_root"/}" "$ref" >&2
      status=1
    fi
  done < <(sed -nE 's/^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*([^[:space:]#]+).*/\2/p' "$wf")
done

if [ "$status" -eq 0 ]; then
  printf 'workflow pins: OK (all third-party uses are 40-hex commits)\n'
fi
exit "$status"
