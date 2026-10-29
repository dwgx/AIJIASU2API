#!/usr/bin/env bash
# Verify a GitHub Release before deploying it.
#
#   ./verify-release.sh --tag v0.1.0                       # full verification
#   ./verify-release.sh --tag v0.1.0 --require-attestation # strictest
#   ./verify-release.sh --tag v0.1.0 --allow-unverified    # checksums only
#
# What it does, in order, and fails closed at every step:
#   1. downloads the six release assets with `gh release download`;
#   2. verifies SHA256SUMS with sha256sum (coreutils) or shasum, and checks
#      that the checksum file covers every one of the six assets;
#   3. checks MANIFEST.txt: version == tag, commit is a full SHA equal to the
#      commit at the tag, worktree == clean;
#   4. verifies build provenance for every asset with `gh attestation verify`
#      and the release attestation with `gh release verify-asset`.
#
# Attestation policy:
#   default                build provenance is required for all six assets;
#                          the release attestation is verified when the release
#                          has one (GitHub only creates it for immutable
#                          releases) and reported as absent otherwise.
#   --require-attestation  both are required; a missing release attestation is
#                          a failure.
#   --allow-unverified     both are skipped; checksums and MANIFEST are still
#                          enforced. This is the only opt-out.
# A missing `gh` or a `gh` without attestation support is a failure unless
# --allow-unverified is given.
#
# Exit codes: 0 verified, 1 any mismatch or failed check, 2 usage error.
#
# `gh` reads its own credential store; this script reads no secret, prints no
# secret and passes none in argv.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

ASSETS=(
  ajs-accountd-linux-amd64
  ajs-accountd-windows-amd64.exe
  ajs-accountd-darwin-arm64
  dashboard-dist.tar.gz
  MANIFEST.txt
  SHA256SUMS
)

die() {
  printf 'verify-release.sh: %s\n' "$*" >&2
  exit 1
}

usage() {
  _code=${1:-2}
  cat >&2 <<'EOF'
usage: ./verify-release.sh --tag <tag> [options]
  --tag <tag>            release tag to verify (default: AJS_IMAGE_TAG from
                         deploy/.env; "latest" is refused)
  --repo <owner/repo>    GitHub repository (default: AJS_GH_REPO from
                         deploy/.env, GITHUB_REPOSITORY, or the git origin)
  --dir <path>           download directory (default: a fresh mktemp -d under
                         ${TMPDIR:-/tmp}; an existing --dir must be owned by
                         the current user and not group/world-writable)
  --require-attestation  require the release attestation too (immutable release)
  --allow-unverified     skip attestation verification (checksums + MANIFEST
                         are still enforced)
EOF
  exit "$_code"
}

TAG=""
REPO=""
DIR=""
STRICT=0
ALLOW_UNVERIFIED=0

while [ $# -gt 0 ]; do
  case "$1" in
    --tag)
      [ $# -ge 2 ] || usage
      TAG=$2
      shift 2
      ;;
    --repo)
      [ $# -ge 2 ] || usage
      REPO=$2
      shift 2
      ;;
    --dir)
      [ $# -ge 2 ] || usage
      DIR=$2
      shift 2
      ;;
    --require-attestation)
      STRICT=1
      shift
      ;;
    --allow-unverified)
      ALLOW_UNVERIFIED=1
      shift
      ;;
    -h|--help)
      usage 0
      ;;
    *)
      usage
      ;;
  esac
done

usage_error() {
  printf 'verify-release.sh: %s\n' "$*" >&2
  exit 2
}

if [ "$STRICT" = 1 ] && [ "$ALLOW_UNVERIFIED" = 1 ]; then
  usage_error "--require-attestation and --allow-unverified are mutually exclusive"
fi

# .env is read, never sourced: only these three keys matter and a verify run
# must not inherit runtime secrets into its environment.
env_value() {
  [ -f "$SCRIPT_DIR/.env" ] || return 0
  grep -E "^$1=" "$SCRIPT_DIR/.env" 2>/dev/null | tail -1 | cut -d= -f2- || true
}

if [ -z "$TAG" ]; then
  TAG=$(env_value AJS_IMAGE_TAG)
fi
[ -n "$TAG" ] || usage_error "no --tag given (and AJS_IMAGE_TAG is unset in deploy/.env)"
[ "$TAG" != latest ] || usage_error "refusing 'latest': pass the release tag to verify, e.g. --tag v0.1.0"
case "$TAG" in
  *[!A-Za-z0-9._-]*) usage_error "unsupported tag characters: $TAG" ;;
esac

if [ -z "$REPO" ]; then
  REPO=$(env_value AJS_GH_REPO)
fi
if [ -z "$REPO" ]; then
  REPO=${GITHUB_REPOSITORY:-}
fi
if [ -z "$REPO" ]; then
  _url=$(git -C "$SCRIPT_DIR" remote get-url origin 2>/dev/null || true)
  case "$_url" in
    *github.com[:/]*)
      REPO=${_url#*github.com}
      REPO=${REPO#:}
      REPO=${REPO#/}
      REPO=${REPO%.git}
      ;;
  esac
fi
case "$REPO" in
  */*/*|/*|*/|'') usage_error "cannot determine the GitHub repo; pass --repo OWNER/REPO" ;;
  */*) ;;
  *) usage_error "cannot determine the GitHub repo; pass --repo OWNER/REPO" ;;
esac

# The assets are trusted after verification, so they must not sit in a path
# another local user could have pre-created or can modify. Without --dir use
# a fresh mktemp directory (mode 700); with --dir accept only a directory
# owned by the current user that is not group- or world-writable.
dir_owner() {
  stat -c %u "$1" 2>/dev/null || stat -f %u "$1" 2>/dev/null
}

dir_mode() {
  stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1" 2>/dev/null
}

if [ -z "$DIR" ]; then
  DIR=$(mktemp -d "${TMPDIR:-/tmp}/ajs-release-verify.XXXXXX") \
    || die "cannot create a temporary download directory"
  chmod 700 "$DIR"
else
  if [ -e "$DIR" ]; then
    [ -d "$DIR" ] || die "--dir is not a directory: $DIR"
    _owner=$(dir_owner "$DIR")
    case "$_owner" in
      ''|'?') die "cannot determine the owner of --dir on this platform; omit --dir to use a private mktemp directory" ;;
    esac
    [ "$_owner" = "$(id -u)" ] \
      || die "--dir must be owned by the current user (uid $(id -u)), found uid $_owner: $DIR"
    _mode=$(dir_mode "$DIR")
    case "$_mode" in
      ''|'?') die "cannot determine the mode of --dir on this platform; omit --dir to use a private mktemp directory" ;;
    esac
    case "${_mode: -2}" in
      *[2367]*) die "--dir must not be group- or world-writable (mode $_mode): $DIR" ;;
    esac
  else
    mkdir -p "$DIR" || die "cannot create --dir: $DIR"
    chmod 700 "$DIR"
  fi
fi

command -v gh >/dev/null 2>&1 \
  || die "gh CLI not found; asset download and attestation verification need it (https://cli.github.com)"

# --- 1. download -----------------------------------------------------------
PATTERNS=()
for _name in "${ASSETS[@]}"; do
  PATTERNS+=(--pattern "$_name")
done

printf 'Downloading %s from %s into %s\n' "$TAG" "$REPO" "$DIR"
if ! _out=$(gh release download "$TAG" --repo "$REPO" --dir "$DIR" --clobber \
    "${PATTERNS[@]}" 2>&1); then
  printf '%s\n' "$_out" >&2
  die "could not download the six assets of $TAG from $REPO (missing tag or incomplete release?)"
fi

for _name in "${ASSETS[@]}"; do
  [ -s "$DIR/$_name" ] || die "asset is missing or empty after download: $DIR/$_name"
done

# --- 2. checksums ----------------------------------------------------------
if command -v sha256sum >/dev/null 2>&1; then
  CHECK_CMD=(sha256sum -c SHA256SUMS)
elif command -v shasum >/dev/null 2>&1; then
  CHECK_CMD=(shasum -a 256 -c SHA256SUMS)
else
  die "need sha256sum or shasum to verify SHA256SUMS"
fi

if ! _out=$(cd "$DIR" && "${CHECK_CMD[@]}" 2>&1); then
  printf '%s\n' "$_out" >&2
  die "SHA256SUMS does not match the downloaded assets in $DIR"
fi

# sha256sum -c only proves the lines it read; make sure every asset is one.
for _name in "${ASSETS[@]}"; do
  [ "$_name" = SHA256SUMS ] && continue
  awk -v name="$_name" '$2 == name { found = 1 } END { exit found ? 0 : 1 }' \
    "$DIR/SHA256SUMS" \
    || die "SHA256SUMS does not cover $_name"
done

# --- 3. MANIFEST identity --------------------------------------------------
manifest_field() {
  awk -v key="$1:" 'index($0, key) == 1 { sub(/^[^:]*: /, ""); print; exit }' \
    "$DIR/MANIFEST.txt"
}

M_VERSION=$(manifest_field version)
M_COMMIT=$(manifest_field commit)
M_WORKTREE=$(manifest_field worktree)

[ "$M_VERSION" = "$TAG" ] \
  || die "MANIFEST.txt version is '$M_VERSION', expected '$TAG'"
case "$M_COMMIT" in
  ''|*[!0-9a-f]*) die "MANIFEST.txt commit is not a lowercase hex SHA: '$M_COMMIT'" ;;
esac
[ "${#M_COMMIT}" -eq 40 ] \
  || die "MANIFEST.txt commit is not a full 40-character SHA: '$M_COMMIT'"
[ "$M_WORKTREE" = clean ] \
  || die "MANIFEST.txt worktree is '$M_WORKTREE'; a release must be built from a clean worktree"

TAG_COMMIT=$(gh api "repos/$REPO/commits/$TAG" --jq .sha 2>/dev/null || true)
[ -n "$TAG_COMMIT" ] \
  || die "could not resolve the commit at tag $TAG via the GitHub API; the MANIFEST commit cannot be bound to the tag"
[ "$TAG_COMMIT" = "$M_COMMIT" ] \
  || die "MANIFEST.txt commit $M_COMMIT does not match the commit $TAG_COMMIT at tag $TAG"

# --- 4. attestations -------------------------------------------------------
if [ "$ALLOW_UNVERIFIED" = 1 ]; then
  PROVENANCE=skipped
  RELEASE_ATTEST=skipped
else
  gh attestation verify --help >/dev/null 2>&1 \
    || die "installed gh has no 'gh attestation verify'; upgrade gh or pass --allow-unverified"

  for _name in "${ASSETS[@]}"; do
    if ! _out=$(gh attestation verify "$DIR/$_name" --repo "$REPO" 2>&1); then
      printf '%s\n' "$_out" >&2
      die "no valid build-provenance attestation for $_name (tag $TAG predates attestations?); pass --allow-unverified to accept unverified assets"
    fi
  done
  PROVENANCE=verified

  # The release attestation is only generated for immutable releases, so its
  # absence is reported unless --require-attestation makes it mandatory.
  if _out=$(gh release verify-asset "$TAG" "$DIR/${ASSETS[0]}" --repo "$REPO" 2>&1); then
    RELEASE_ATTEST=verified
    for _name in "${ASSETS[@]:1}"; do
      if ! _out=$(gh release verify-asset "$TAG" "$DIR/$_name" --repo "$REPO" 2>&1); then
        printf '%s\n' "$_out" >&2
        die "release attestation does not cover $_name in $TAG"
      fi
    done
  elif [ "$STRICT" = 1 ]; then
    printf '%s\n' "$_out" >&2
    die "no release attestation for $TAG; --require-attestation needs an immutable release"
  elif printf '%s' "$_out" | grep -qi 'no attestations found'; then
    RELEASE_ATTEST=absent
  else
    printf '%s\n' "$_out" >&2
    die "release attestation check failed for $TAG"
  fi
fi

# --- summary ---------------------------------------------------------------
printf '\nverify-release.sh: %s verified\n' "$TAG"
printf '  repo:        %s\n' "$REPO"
printf '  dir:         %s\n' "$DIR"
printf '  checksums:   SHA256SUMS OK (%s files)\n' "$((${#ASSETS[@]} - 1))"
printf '  manifest:    version %s, commit %s, worktree %s\n' \
  "$M_VERSION" "$M_COMMIT" "$M_WORKTREE"
printf '  provenance:  %s\n' "$PROVENANCE"
printf '  release:     %s\n' "$RELEASE_ATTEST"
