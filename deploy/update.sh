#!/usr/bin/env bash
# One-command release update/rollback for the control plane.
#
#   ./update.sh --tag v0.1.0        # deploy a specific GHCR release tag
#   ./update.sh --allow-unverified  # skip image attestation verification
#   ./update.sh --allow-latest      # permit AJS_IMAGE_TAG=latest
#   ./update.sh --rollback          # restore the image IDs recorded before the
#                                   # last update (update-state.json)
#
# A release tag is required. An unset AJS_IMAGE_TAG and an explicit "latest"
# are both refused, because the floating alias is not bound to a release: only
# --allow-latest deploys it. The tag is validated to [A-Za-z0-9._-].
#
# Every update verifies the build provenance of both images with
# `gh attestation verify oci://...` before any container is switched, then
# records the resolved digests in update-state.json. Verification fails
# closed: a missing `gh`, a `gh` without attestation support, or an image
# without a valid attestation aborts the update before it touches the
# running deployment. --allow-unverified is the explicit opt-out and is
# recorded as "verified": false in the state file.
#
# The switch is digest-pinned through AJS_ACCOUNTD_DIGEST and
# AJS_DASHBOARD_DIGEST (compose.release.yml), so the containers that start
# are the exact bytes that were verified, not merely whatever the tag
# currently points at. Both variables are owned by this script for its own
# run; inherited values are cleared so a stale pin cannot deploy something
# other than --tag.
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
  _code=${1:-2}
  cat >&2 <<'EOF'
usage: ./update.sh [--tag <tag>] [--verify | --allow-unverified] [--allow-latest]
       ./update.sh --rollback
  --tag <tag>          update to a GHCR release tag (default: AJS_IMAGE_TAG;
                       a release tag is required, 'latest' is refused)
  --verify             require image attestations (the default)
  --allow-unverified   skip image attestation verification; the resolved
                       digests are still recorded in update-state.json
  --allow-latest       permit AJS_IMAGE_TAG=latest (floating alias) for an update
  --rollback           restore the image IDs recorded before the last update
EOF
  exit "$_code"
}

# --help must work before .env exists: the file is only required to resolve
# the GHCR refs, which every other mode needs (rollback included).
for _arg in "$@"; do
  case "$_arg" in
    -h|--help) usage 0 ;;
  esac
done

[ -f .env ] || die "deploy/.env not found; create it first: cp .env.example .env"

# shellcheck disable=SC1091
set -a
. ./.env
set +a

MODE=update
VERIFY=1
VERIFY_SET=0
ALLOW_SET=0
ALLOW_LATEST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --tag)
      [ $# -ge 2 ] || usage
      AJS_IMAGE_TAG=$2
      export AJS_IMAGE_TAG
      shift 2
      ;;
    --verify)
      VERIFY=1
      VERIFY_SET=1
      shift
      ;;
    --allow-unverified)
      VERIFY=0
      ALLOW_SET=1
      shift
      ;;
    --allow-latest)
      ALLOW_LATEST=1
      shift
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

if [ "$VERIFY_SET" = 1 ] && [ "$ALLOW_SET" = 1 ]; then
  printf 'update.sh: --verify and --allow-unverified are mutually exclusive\n' >&2
  exit 2
fi
if [ "$MODE" = rollback ] && { [ "$VERIFY_SET" = 1 ] || [ "$ALLOW_SET" = 1 ]; }; then
  printf 'update.sh: verification flags apply to updates; --rollback restores recorded images\n' >&2
  exit 2
fi
if [ "$MODE" = rollback ] && [ "$ALLOW_LATEST" = 1 ]; then
  printf 'update.sh: --allow-latest applies to updates; --rollback does not pull\n' >&2
  exit 2
fi

# A release tag is required: an implicit or explicit "latest" deploys whatever
# the alias points at, which no digest or attestation record can pin to a
# release. --allow-latest is the documented opt-in.
[ -n "${AJS_IMAGE_TAG:-}" ] \
  || die "no tag: pass --tag <tag> or set AJS_IMAGE_TAG in deploy/.env (implicit latest is refused)"
TAG=$AJS_IMAGE_TAG
case "$TAG" in
  *[!A-Za-z0-9._-]*) die "unsupported tag characters: $TAG" ;;
esac
if [ "$MODE" = update ] && [ "$TAG" = latest ] && [ "$ALLOW_LATEST" != 1 ]; then
  die "refusing the floating tag 'latest': pass a release tag, or --allow-latest to deploy the alias"
fi

# The digests this script resolves replace any pin inherited from the
# environment; compose.release.yml still honours both for direct use.
unset AJS_ACCOUNTD_DIGEST AJS_DASHBOARD_DIGEST

# GHCR requires a lowercase namespace, and the release workflow lowercases it
# too. Export the value so compose.release.yml builds exactly the ref this
# script pulled.
GHCR_OWNER=$(printf '%s' "${AJS_GHCR_OWNER:-dwgx}" | tr '[:upper:]' '[:lower:]')
export AJS_GHCR_OWNER=$GHCR_OWNER
ACCOUNTD_REF="ghcr.io/${GHCR_OWNER}/ajs-accountd:${TAG}"
DASHBOARD_REF="ghcr.io/${GHCR_OWNER}/ajs-dashboard:${TAG}"
# Untagged forms: the key Docker uses in RepoDigests and the attestation
# subject-name the release workflow signs.
ACCOUNTD_IMAGE="ghcr.io/${GHCR_OWNER}/ajs-accountd"
DASHBOARD_IMAGE="ghcr.io/${GHCR_OWNER}/ajs-dashboard"

# Attestation lookup scope: AJS_GH_REPO or GITHUB_REPOSITORY when set, else
# the git origin when deploy/ sits in a checkout. An owner-only scope is
# weaker but still binds the image to this account; it is printed below.
GH_REPO=""
if [ -n "${AJS_GH_REPO:-}" ]; then
  GH_REPO=$AJS_GH_REPO
elif [ -n "${GITHUB_REPOSITORY:-}" ]; then
  GH_REPO=$GITHUB_REPOSITORY
else
  _url=$(git remote get-url origin 2>/dev/null || true)
  case "$_url" in
    *github.com[:/]*)
      GH_REPO=${_url#*github.com}
      GH_REPO=${GH_REPO#:}
      GH_REPO=${GH_REPO#/}
      GH_REPO=${GH_REPO%.git}
      ;;
  esac
fi
case "$GH_REPO" in
  */*/*|/*|*/) GH_REPO="" ;;
esac

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

# Digest of the pulled image behind a ref, empty when unknown.
# $1 = image ref as written in compose (repo:tag).
# Docker reports RepoDigests as "repo@sha256:..." (no tag), so the trailing
# ":tag" is stripped before matching. Every ref here ends in ":tag", which is
# what makes the single strip safe for registry ports (ghcr.io has none).
resolve_digest() {
  _repo=${1%:*}
  _digests=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$1" 2>/dev/null || true)
  printf '%s\n' "$_digests" | awk -v prefix="${_repo}@" 'index($0, prefix) == 1 { print substr($0, length(prefix) + 1); exit }'
}

# $1 = image repo without tag, $2 = digest; nonzero when no valid attestation
# is found. The OCI subject is repo@digest, matching the attestation's
# subject-name (which the release workflow sets without a tag).
verify_image() {
  _uri="oci://$1@$2"
  if [ -n "$GH_REPO" ]; then
    _out=$(gh attestation verify "$_uri" --repo "$GH_REPO" 2>&1) || {
      printf '%s\n' "$_out" >&2
      return 1
    }
  else
    _out=$(gh attestation verify "$_uri" --owner "$GHCR_OWNER" 2>&1) || {
      printf '%s\n' "$_out" >&2
      return 1
    }
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
  echo "  recorded digests: accountd $(state_field accountd_digest), dashboard $(state_field dashboard_digest)"
  exit 0
fi

# Fail before pulling anything when verification is required but impossible.
if [ "$VERIFY" = 1 ]; then
  command -v gh >/dev/null 2>&1 \
    || die "gh CLI not found; install it, or pass --allow-unverified to skip image attestation verification"
  gh attestation verify --help >/dev/null 2>&1 \
    || die "installed gh has no 'gh attestation verify'; upgrade gh or pass --allow-unverified"
fi

echo "Updating to ${TAG}..."

# Record the image IDs currently deployed BEFORE pulling, so a bad
# release can be undone with --rollback. Overwritten by every update, so
# rollback is exactly one step.
ACCOUNTD_ID=$(current_image_id ajs-accountd "$ACCOUNTD_REF")
DASHBOARD_ID=$(current_image_id ajs-dashboard "$DASHBOARD_REF")

compose pull

ACCOUNTD_DIGEST=$(resolve_digest "$ACCOUNTD_REF")
DASHBOARD_DIGEST=$(resolve_digest "$DASHBOARD_REF")
[ -n "$ACCOUNTD_DIGEST" ] \
  || die "could not resolve the digest of ${ACCOUNTD_REF}; is the tag published?"
[ -n "$DASHBOARD_DIGEST" ] \
  || die "could not resolve the digest of ${DASHBOARD_REF}; is the tag published?"

if [ "$VERIFY" = 1 ]; then
  if [ -n "$GH_REPO" ]; then
    SCOPE="repo:${GH_REPO}"
  else
    SCOPE="owner:${GHCR_OWNER}"
  fi
  echo "Verifying image attestations (${SCOPE})..."
  verify_image "$ACCOUNTD_IMAGE" "$ACCOUNTD_DIGEST" \
    || die "no valid attestation for ${ACCOUNTD_IMAGE}@${ACCOUNTD_DIGEST}; pass --allow-unverified only if you accept unverified images"
  verify_image "$DASHBOARD_IMAGE" "$DASHBOARD_DIGEST" \
    || die "no valid attestation for ${DASHBOARD_IMAGE}@${DASHBOARD_DIGEST}; pass --allow-unverified only if you accept unverified images"
  VERIFIED=true
else
  SCOPE=skipped
  VERIFIED=false
  echo "Attestation verification skipped (--allow-unverified)."
fi

UPDATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# Atomic replace: a crash mid-write must not leave a half state file that
# --rollback would read. The temp file is 0600 and lives beside the target so
# mv is a rename on the same filesystem.
STATE_TMP="${STATE_FILE}.tmp.$$"
trap 'rm -f "$STATE_TMP"' EXIT
( umask 077
  printf '{\n  "updated_at": "%s",\n  "tag": "%s",\n  "verified": %s,\n  "attestation_scope": "%s",\n  "accountd_image_id": "%s",\n  "accountd_digest": "%s",\n  "dashboard_image_id": "%s",\n  "dashboard_digest": "%s"\n}\n' \
    "$UPDATED_AT" "$TAG" "$VERIFIED" "$SCOPE" \
    "$ACCOUNTD_ID" "$ACCOUNTD_DIGEST" "$DASHBOARD_ID" "$DASHBOARD_DIGEST" > "$STATE_TMP"
) || die "cannot write $STATE_TMP"
mv -f "$STATE_TMP" "$STATE_FILE" || die "cannot replace $STATE_FILE"

# Pin the verified digests for this run: compose.release.yml appends them to
# the tag ref, so the containers start from exactly these bytes.
export AJS_ACCOUNTD_DIGEST=$ACCOUNTD_DIGEST
export AJS_DASHBOARD_DIGEST=$DASHBOARD_DIGEST

compose up -d --no-build

wait_healthy http://127.0.0.1:2261/healthz \
  || die "accountd did not become healthy; roll back with: ./update.sh --rollback"
wait_healthy http://127.0.0.1:18081/ \
  || die "dashboard did not answer; roll back with: ./update.sh --rollback"

echo "Update complete:"
echo "  tag:        ${TAG}"
echo "  accountd:   ${ACCOUNTD_REF} -> ${ACCOUNTD_DIGEST}"
echo "  dashboard:  ${DASHBOARD_REF} -> ${DASHBOARD_DIGEST}"
if [ "$VERIFIED" = true ]; then
  echo "  attestations: verified (${SCOPE})"
else
  echo "  attestations: checksums verified; provenance skipped"
fi
echo "  previous:   accountd ${ACCOUNTD_ID}, dashboard ${DASHBOARD_ID} (./update.sh --rollback)"
