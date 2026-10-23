#!/usr/bin/env bash
# One-command release update/rollback for the control plane.
#
#   ./update.sh                 # deploy AJS_IMAGE_TAG from .env (default latest)
#   ./update.sh --tag v0.1.0    # deploy a specific GHCR release tag
#   ./update.sh --rollback      # restore the image IDs recorded before the
#                               # last update (update-state.json)
#
# Requires deploy/.env. No .env value is printed; no credential is ever
# passed in argv. The GHCR refs are resolved after .env is sourced and
# --tag is parsed, so both affect what gets pulled.

set -euo pipefail

cd "$(dirname "$0")"

STATE_FILE=update-state.json

die() {
  echo "update.sh: $*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
usage: ./update.sh [--tag <tag>] | --rollback
  --tag <tag>    update to a GHCR release tag (default: AJS_IMAGE_TAG or latest)
  --rollback     restore the image IDs recorded before the last update
EOF
  exit 2
}

[ -f .env ] || die "deploy/.env not found; create it first: cp .env.example .env"

# shellcheck disable=SC1091
set -a
. ./.env
set +a

MODE=update
while [ $# -gt 0 ]; do
  case "$1" in
    --tag)
      [ $# -ge 2 ] || usage
      AJS_IMAGE_TAG=$2
      export AJS_IMAGE_TAG
      shift 2
      ;;
    --rollback)
      MODE=rollback
      shift
      ;;
    *)
      usage
      ;;
  esac
done

TAG=${AJS_IMAGE_TAG:-latest}
ACCOUNTD_REF="ghcr.io/${AJS_GHCR_OWNER:-dwgx}/ajs-accountd:${TAG}"
DASHBOARD_REF="ghcr.io/${AJS_GHCR_OWNER:-dwgx}/ajs-dashboard:${TAG}"

compose() {
  docker compose -f compose.yml -f compose.release.yml "$@"
}

# Image ID behind a container (running or not), else behind the local
# image ref, else "none". $1 = container name, $2 = image ref.
current_image_id() {
  id=$(docker inspect --format '{{.Image}}' "$1" 2>/dev/null || true)
  if [ -n "$id" ]; then
    echo "$id"
    return 0
  fi
  id=$(docker image inspect --format '{{.Id}}' "$2" 2>/dev/null || true)
  if [ -n "$id" ]; then
    echo "$id"
  else
    echo none
  fi
}

# $1 = URL expected to answer HTTP 200; bounded retries (~60 s).
wait_healthy() {
  i=0
  while [ "$i" -lt 30 ]; do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$1" || true)
    if [ "$code" = "200" ]; then
      return 0
    fi
    i=$((i + 1))
    sleep 2
  done
  return 1
}

# $1 = key in STATE_FILE; prints its value, empty if absent.
state_field() {
  grep -o "\"$1\": *\"[^\"]*\"" "$STATE_FILE" 2>/dev/null | cut -d'"' -f4 || true
}

if [ "$MODE" = rollback ]; then
  [ -f "$STATE_FILE" ] || die "no $STATE_FILE: nothing to roll back to"
  OLD_ACCOUNTD_ID=$(state_field accountd_image_id)
  OLD_DASHBOARD_ID=$(state_field dashboard_image_id)
  [ -n "$OLD_ACCOUNTD_ID" ] || die "no recorded accountd image ID in $STATE_FILE"
  [ -n "$OLD_DASHBOARD_ID" ] || die "no recorded dashboard image ID in $STATE_FILE"
  [ "$OLD_ACCOUNTD_ID" != none ] || die "recorded accountd image ID is none; nothing to restore"
  [ "$OLD_DASHBOARD_ID" != none ] || die "recorded dashboard image ID is none; nothing to restore"

  echo "Rolling back to the images recorded before the last update..."
  docker tag "$OLD_ACCOUNTD_ID" "$ACCOUNTD_REF"
  docker tag "$OLD_DASHBOARD_ID" "$DASHBOARD_REF"
  compose up -d --no-build

  wait_healthy http://127.0.0.1:2261/healthz \
    || die "accountd did not become healthy after rollback; see: docker compose -f compose.yml -f compose.release.yml logs accountd"
  wait_healthy http://127.0.0.1:18081/ \
    || die "dashboard did not answer after rollback; see: docker compose -f compose.yml -f compose.release.yml logs dashboard"

  echo "Rollback complete:"
  echo "  accountd  $(docker inspect --format '{{.Image}}' ajs-accountd)"
  echo "  dashboard $(docker inspect --format '{{.Image}}' ajs-dashboard)"
  exit 0
fi

echo "Updating to ${TAG}..."

# Record the image IDs currently deployed BEFORE pulling, so a bad
# release can be undone with --rollback. Overwritten by every update, so
# rollback is exactly one step.
ACCOUNTD_ID=$(current_image_id ajs-accountd "$ACCOUNTD_REF")
DASHBOARD_ID=$(current_image_id ajs-dashboard "$DASHBOARD_REF")
UPDATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
printf '{\n  "updated_at": "%s",\n  "tag": "%s",\n  "accountd_image_id": "%s",\n  "dashboard_image_id": "%s"\n}\n' \
  "$UPDATED_AT" "$TAG" "$ACCOUNTD_ID" "$DASHBOARD_ID" > "$STATE_FILE"

compose pull
compose up -d --no-build

wait_healthy http://127.0.0.1:2261/healthz \
  || die "accountd did not become healthy; roll back with: ./update.sh --rollback"
wait_healthy http://127.0.0.1:18081/ \
  || die "dashboard did not answer; roll back with: ./update.sh --rollback"

echo "Update complete:"
echo "  tag:       ${TAG}"
echo "  accountd:  ${ACCOUNTD_REF} -> $(docker image inspect --format '{{.Id}}' "$ACCOUNTD_REF")"
echo "  dashboard: ${DASHBOARD_REF} -> $(docker image inspect --format '{{.Id}}' "$DASHBOARD_REF")"
echo "  previous:  accountd ${ACCOUNTD_ID}, dashboard ${DASHBOARD_ID} (./update.sh --rollback)"
