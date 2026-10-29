#!/usr/bin/env bash
# Reproducible release packaging for ajs-accountd and the operator dashboard.
#
#   bash deploy/package.sh --version 0.1.0 --out dist
#
# Produces, in --out:
#   ajs-accountd-linux-amd64      (GOOS=linux   GOARCH=amd64, CGO disabled)
#   ajs-accountd-windows-amd64.exe (GOOS=windows GOARCH=amd64, CGO disabled)
#   ajs-accountd-darwin-arm64     (GOOS=darwin  GOARCH=arm64, CGO disabled)
#   dashboard-dist.tar.gz         (the existing live, token-free dashboard bundle)
#   deploy-kit.tar.gz             (LF-normalized copy of the deploy/ tree)
#   MANIFEST.txt                  (version, commit, toolchain, flags, hashes)
#   SHA256SUMS                    (sha256sum -c compatible)
#
# No Docker, no network, no CGO. The daemon module replaces ajsproxy with
# ../emulator, so both module trees must be present in this checkout.
#
# Reproducibility: binaries are built with -trimpath, -buildvcs=false,
# -ldflags="-s -w -buildid= -X main.buildVersion=<version>
# -X main.buildCommitValue=<commit>", -mod=readonly and GOTOOLCHAIN=local. Both
# tar archives are staged with normalized mtimes and a
# sorted file list, and every text file in them is rewritten to LF before the
# tar is created. That last step matters on Windows: core.autocrlf=true turns
# .nft/.conf files into CRLF, and nft(8) rejects CRLF line terminators.
# SOURCE_DATE_EPOCH defaults to the HEAD commit time, so the same Go toolchain,
# clean tree and tar implementation produce byte-identical artifacts.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

export TZ=UTC

die() {
  printf 'package.sh: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
usage: deploy/package.sh [options]

  --version <version>       release version stamped into the binaries as
                            main.buildVersion and recorded in MANIFEST.txt
                            (default: git describe --tags --always --dirty,
                            else 0.0.0-dev)
  --out <dir>               output directory (default: <repo>/dist)
  --dashboard-dist <dir>    prebuilt live dashboard bundle to package
                            (default: <repo>/dashboard/dist)
  --build-dashboard         build the live token-free bundle with
                            deploy/production/build-bundles.sh before packaging
  -h, --help                show this help

Artifacts: ajs-accountd-linux-amd64, ajs-accountd-windows-amd64.exe,
ajs-accountd-darwin-arm64, dashboard-dist.tar.gz, deploy-kit.tar.gz,
MANIFEST.txt, SHA256SUMS.
EOF
}

VERSION=""
OUT_DIR="$ROOT/dist"
DASHBOARD_DIST=""
DASHBOARD_DIST_SET=0
BUILD_DASHBOARD=0
BUILD_TMP=""

while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      [ $# -ge 2 ] || { usage; exit 2; }
      VERSION=$2
      shift 2
      ;;
    --version=*)
      VERSION=${1#*=}
      shift
      ;;
    --out)
      [ $# -ge 2 ] || { usage; exit 2; }
      OUT_DIR=$2
      shift 2
      ;;
    --out=*)
      OUT_DIR=${1#*=}
      shift
      ;;
    --dashboard-dist)
      [ $# -ge 2 ] || { usage; exit 2; }
      DASHBOARD_DIST=$2
      DASHBOARD_DIST_SET=1
      shift 2
      ;;
    --dashboard-dist=*)
      DASHBOARD_DIST=${1#*=}
      DASHBOARD_DIST_SET=1
      shift
      ;;
    --build-dashboard)
      BUILD_DASHBOARD=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'package.sh: unknown argument: %s\n' "$1" >&2
      usage
      exit 2
      ;;
  esac
done

[ -n "$OUT_DIR" ] || die "--out must not be empty"

if [ -z "$VERSION" ]; then
  if command -v git >/dev/null 2>&1; then
    VERSION=$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || true)
  fi
  [ -n "$VERSION" ] || VERSION=0.0.0-dev
fi

# The version is also the linker value: -X main.buildVersion=<version> is one
# linker argument, so whitespace would split it and a quote or backslash would
# change how the Go command parses -ldflags. Reject those before any build
# starts instead of failing with a confusing linker error or, worse, stamping a
# truncated version into the binary.
case "$VERSION" in
  *[[:space:]]*) die "version must not contain whitespace: '$VERSION'" ;;
esac
case "$VERSION" in
  *[!A-Za-z0-9._+/-]*) die "version may contain only letters, digits, '.', '_', '+', '/' and '-': '$VERSION'" ;;
esac

COMMIT=unknown
WORKTREE=unknown
if command -v git >/dev/null 2>&1 && git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  COMMIT=$(git -C "$ROOT" rev-parse --verify HEAD 2>/dev/null || printf 'unknown')
  if [ -n "$(git -C "$ROOT" status --porcelain --untracked-files=no 2>/dev/null)" ]; then
    WORKTREE=dirty
  else
    WORKTREE=clean
  fi
fi

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
  if command -v git >/dev/null 2>&1; then
    SOURCE_DATE_EPOCH=$(git -C "$ROOT" log -1 --format=%ct 2>/dev/null || true)
  fi
  case "${SOURCE_DATE_EPOCH:-}" in
    ''|*[!0-9]*) SOURCE_DATE_EPOCH=$(date +%s) ;;
  esac
fi
case "$SOURCE_DATE_EPOCH" in
  ''|*[!0-9]*) die "SOURCE_DATE_EPOCH must be a non-negative integer" ;;
esac
export SOURCE_DATE_EPOCH

command -v go >/dev/null 2>&1 \
  || die "go not found in PATH (Go 1.26.10 or newer is required by daemon/go.mod)"
command -v tar >/dev/null 2>&1 || die "tar not found in PATH"
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die "need sha256sum or shasum to write SHA256SUMS"
fi

GO_VERSION=$(go version)
DASHBOARD_VERSION=unknown
if [ -f "$ROOT/dashboard/package.json" ]; then
  DASHBOARD_VERSION=$(awk -F'"' '/"version"[[:space:]]*:/ {print $4; exit}' "$ROOT/dashboard/package.json")
  [ -n "$DASHBOARD_VERSION" ] || DASHBOARD_VERSION=unknown
fi
# The commit is a linker value too, so it is constrained the same way as the
# version: a git revision is hex (full or abbreviated), and anything else
# becomes "unknown" instead of reaching -ldflags. Without this the packaged
# binary would have no commit to print, because -buildvcs=false leaves the
# toolchain with no VCS metadata to embed.
case "$COMMIT" in
  ''|unknown) COMMIT=unknown ;;
  *[!0-9a-f]*) COMMIT=unknown ;;
esac

# -X main.buildVersion=<version> is the release identity (AJS-506): the daemon
# reports it from /healthz and `ajs-accountd -version`, so a downloaded binary
# names the tag it was cut from instead of api.DefaultVersion. -X
# main.buildCommitValue=<commit> (AJS-510) supplies the commit -version and the
# Lab overview print: with -buildvcs=false the toolchain embeds none. Both
# values are validated above, so they cannot split or alter the linker
# arguments. The remaining flags keep the build reproducible.
LDFLAGS="-s -w -buildid= -X main.buildVersion=$VERSION -X main.buildCommitValue=$COMMIT"
BUILD_FLAGS='CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -mod=readonly'

ARTIFACT_NAMES=(
  ajs-accountd-linux-amd64
  ajs-accountd-windows-amd64.exe
  ajs-accountd-darwin-arm64
  dashboard-dist.tar.gz
  MANIFEST.txt
  SHA256SUMS
)

# The deploy kit is an operator convenience, not one of the six release
# files. It still gets a digest in MANIFEST.txt so the checksum chain covers
# it transitively through SHA256SUMS.
DEPLOY_KIT_NAME=deploy-kit.tar.gz

# Redistribution notices that must travel with the dashboard archive, the
# deploy kit and both container images (R2). Keep this list in one place: the
# archive checks below fail closed when one is dropped.
NOTICE_FILES=(
  LICENSE
  NOTICE
  THIRD-PARTY-NOTICES.md
)

# MANIFEST records a stable logical description of the dashboard input, never
# the mktemp staging path: two --build-dashboard runs in different temporary
# directories must produce identical MANIFEST.txt and SHA256SUMS (R8).
DASHBOARD_SOURCE='prebuilt live bundle (verified ajs-build.json)'

# Keep the target table in one place: "GOOS GOARCH output-name".
TARGETS=(
  'linux amd64 ajs-accountd-linux-amd64'
  'windows amd64 ajs-accountd-windows-amd64.exe'
  'darwin arm64 ajs-accountd-darwin-arm64'
)

format_epoch() {
  if date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ >/dev/null 2>&1; then
    date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ
  else
    date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ
  fi
}

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

file_size() {
  wc -c < "$1" | tr -d '[:space:]'
}

# A dashboard bundle is only packageable when it proves it is the live,
# token-free artifact. Vite treats every VITE_USE_MOCK value except "false"
# as mock mode, so checking for the string "VITE_USE_MOCK=false" in a build
# script is not enough: verify the manifest emitted by the build.
verify_dashboard_bundle() {
  _dir=$1
  [ -f "$_dir/index.html" ] || die "dashboard bundle has no index.html: $_dir"
  _meta="$_dir/ajs-build.json"
  [ -f "$_meta" ] || die "dashboard bundle has no ajs-build.json; rebuild it with deploy/production/build-bundles.sh (live recipe)"
  if command -v node >/dev/null 2>&1; then
    node - "$_meta" <<'NODE' || die "dashboard bundle is not a live, token-free build: $_meta"
const fs = require('fs')
const manifest = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
if (manifest.schemaVersion !== 1 ||
    manifest.mode !== 'live' ||
    manifest.mock !== false ||
    manifest.panelToken !== false) {
  console.error('dashboard manifest: ' + JSON.stringify(manifest))
  process.exit(1)
}
NODE
  else
    grep -q '"mode": "live"' "$_meta" \
      && grep -q '"mock": false' "$_meta" \
      && grep -q '"panelToken": false' "$_meta" \
      || die "dashboard bundle is not a live, token-free build: $_meta"
  fi
}

# Fail closed when an archive would ship without the required notices. Both
# tars are created from a staging directory with `find . -type f`, so members
# are exactly ./, ./<name>; -x makes the check unambiguous.
assert_notice_members() {
  _tar=$1
  _label=$2
  _members=$(tar -tzf "$_tar") || die "cannot list $_label: $_tar"
  for _notice in "${NOTICE_FILES[@]}"; do
    printf '%s\n' "$_members" | grep -qxF "./$_notice" \
      || die "$_label is missing $_notice"
  done
}

# Rewrite CRLF (and any stray CR) to LF for every text file under a staged
# tree. Binary files are detected with grep -I and skipped, and file modes are
# preserved so shell scripts stay executable. This is the archive-time
# guarantee: the checkout may be CRLF because core.autocrlf=true, but the tar
# that leaves this script is always LF.
normalize_text_lf() {
  local _dir=$1 _file _mode _tmp
  find "$_dir" -type f -print0 | while IFS= read -r -d '' _file; do
    # Empty files and binaries are not text; grep -I returns non-zero for both.
    # Text files are rewritten unconditionally: Git Bash's grep treats CRLF as
    # a line ending, so `grep -q $'\r'` is not a portable CR detector. tr is
    # byte-oriented and leaves an already-LF file byte-identical.
    if ! grep -Iq . "$_file" 2>/dev/null; then
      continue
    fi
    _mode=$(stat -c '%a' "$_file" 2>/dev/null || stat -f '%Lp' "$_file" 2>/dev/null || true)
    _tmp="$_file.lf.$$"
    if ! tr -d '\r' < "$_file" > "$_tmp"; then
      rm -f "$_tmp"
      die "cannot normalize line endings: $_file"
    fi
    if [ -n "$_mode" ]; then
      chmod "$_mode" "$_tmp" 2>/dev/null || true
    fi
    mv -f "$_tmp" "$_file"
  done
}

# Build the dashboard with the one live production recipe when asked, or when
# the default bundle is absent but its node_modules are installed. Never runs
# npm install: a fresh checkout needs `npm ci` from the committed lockfile
# (packaging.md). An explicit --dashboard-dist is never silently replaced.
if [ "$BUILD_DASHBOARD" = 1 ] && [ "$DASHBOARD_DIST_SET" = 1 ]; then
  die "--build-dashboard and --dashboard-dist are mutually exclusive"
fi

if [ "$BUILD_DASHBOARD" = 1 ]; then
  command -v npm >/dev/null 2>&1 || die "--build-dashboard needs npm in PATH"
  [ -f "$ROOT/dashboard/package-lock.json" ] || die "dashboard/package-lock.json missing; npm ci cannot run"
  [ -d "$ROOT/dashboard/node_modules" ] || die "dashboard/node_modules missing; run 'cd dashboard && npm ci'"
  [ -z "${VITE_PANEL_TOKEN:-}" ] || die "refusing to build with VITE_PANEL_TOKEN set; the bundle must stay token-free"
  BUILD_TMP=$(mktemp -d "${TMPDIR:-/tmp}/ajs-dashboard-build.XXXXXX") || die "cannot create a temporary build directory"
  printf 'Building live dashboard bundle via deploy/production/build-bundles.sh\n'
  bash "$SCRIPT_DIR/production/build-bundles.sh" --out "$BUILD_TMP/bundles" >&2
  DASHBOARD_DIST="$BUILD_TMP/bundles/portal"
  DASHBOARD_SOURCE='built by deploy/production/build-bundles.sh (live recipe)'
fi

if [ -z "$DASHBOARD_DIST" ]; then
  DASHBOARD_DIST="$ROOT/dashboard/dist"
fi

if [ ! -f "$DASHBOARD_DIST/index.html" ]; then
  if [ "$DASHBOARD_DIST_SET" = 1 ]; then
    die "no dashboard bundle at $DASHBOARD_DIST; build it first or drop --dashboard-dist"
  fi
  if command -v npm >/dev/null 2>&1 && [ -d "$ROOT/dashboard/node_modules" ]; then
    [ -f "$ROOT/dashboard/package-lock.json" ] || die "dashboard/package-lock.json missing; npm ci cannot run"
    [ -z "${VITE_PANEL_TOKEN:-}" ] || die "refusing to build with VITE_PANEL_TOKEN set; the bundle must stay token-free"
    BUILD_TMP=$(mktemp -d "${TMPDIR:-/tmp}/ajs-dashboard-build.XXXXXX") || die "cannot create a temporary build directory"
    printf 'package.sh: %s has no index.html; building the live bundle\n' "$DASHBOARD_DIST" >&2
    bash "$SCRIPT_DIR/production/build-bundles.sh" --out "$BUILD_TMP/bundles" >&2
    DASHBOARD_DIST="$BUILD_TMP/bundles/portal"
    DASHBOARD_SOURCE='built by deploy/production/build-bundles.sh (live recipe)'
  else
    die "no dashboard bundle at $DASHBOARD_DIST; run 'cd dashboard && npm ci && npm run build', pass --dashboard-dist, or use --build-dashboard"
  fi
fi
DASHBOARD_DIST=$(CDPATH= cd -- "$DASHBOARD_DIST" && pwd)
verify_dashboard_bundle "$DASHBOARD_DIST"

mkdir -p "$OUT_DIR" || die "cannot create output directory: $OUT_DIR"
OUT_DIR=$(CDPATH= cd -- "$OUT_DIR" && pwd)

for name in "${ARTIFACT_NAMES[@]}"; do
  rm -f "$OUT_DIR/$name"
done
rm -f "$OUT_DIR/$DEPLOY_KIT_NAME"

STAGE_DIR=""
cleanup() {
  if [ -n "$STAGE_DIR" ]; then
    rm -rf "$STAGE_DIR"
  fi
  if [ -n "$BUILD_TMP" ]; then
    rm -rf "$BUILD_TMP"
  fi
}
trap cleanup EXIT

build_target() {
  goos=$1
  goarch=$2
  name=$3
  printf '  %-7s %-6s -> %s\n' "$goos" "$goarch" "$name"
  (
    cd "$ROOT/daemon"
    env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOTOOLCHAIN=local \
      go build -trimpath -buildvcs=false -mod=readonly \
      -ldflags="$LDFLAGS" -o "$OUT_DIR/$name" ./cmd/ajs-accountd
  )
  [ -s "$OUT_DIR/$name" ] || die "build produced no $name"
}

printf 'Building ajs-accountd %s\n' "$VERSION"
for target in "${TARGETS[@]}"; do
  # shellcheck disable=SC2086 # the three fields contain no whitespace
  set -- $target
  build_target "$1" "$2" "$3"
done

printf 'Packaging dashboard bundle from %s\n' "$DASHBOARD_DIST"
STAGE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ajs-dashboard.XXXXXX") \
  || die "cannot create a temporary staging directory"
cp -R "$DASHBOARD_DIST/." "$STAGE_DIR/"
# The bundle directory is build output and does not contain the repository
# notices; copy them in before the archive is created and assert below.
for _notice in "${NOTICE_FILES[@]}"; do
  [ -s "$ROOT/$_notice" ] || die "required notice file is missing or empty: $_notice"
  cp -p "$ROOT/$_notice" "$STAGE_DIR/$_notice"
done
normalize_text_lf "$STAGE_DIR"
# Normalize mtimes so the archive does not depend on when the dashboard was
# built. 1970-01-01 is the SOURCE_DATE_EPOCH convention; tar readers only care
# that the value is stable.
find "$STAGE_DIR" -exec touch -t 197001010000.00 {} +

TAR_OPTS=()
TAR_VERSION=$(tar --version 2>/dev/null || true)
case "$TAR_VERSION" in
  *"GNU tar"*) TAR_OPTS=(--owner=0 --group=0 --numeric-owner --format=ustar) ;;
esac
(
  cd "$STAGE_DIR"
  # Empty-array expansion is unsafe under `set -u` on bash 3.2 (macOS), so
  # branch instead of relying on "${TAR_OPTS[@]}" with no elements.
  if [ ${#TAR_OPTS[@]} -gt 0 ]; then
    find . -type f -print | LC_ALL=C sort | tar -czf "$OUT_DIR/dashboard-dist.tar.gz" "${TAR_OPTS[@]}" -T -
  else
    find . -type f -print | LC_ALL=C sort | tar -czf "$OUT_DIR/dashboard-dist.tar.gz" -T -
  fi
)
[ -s "$OUT_DIR/dashboard-dist.tar.gz" ] || die "dashboard tar is empty"
assert_notice_members "$OUT_DIR/dashboard-dist.tar.gz" "dashboard-dist.tar.gz"
rm -rf "$STAGE_DIR"
STAGE_DIR=""

# Package the operator-facing deploy kit. The archive contains the deploy/
# tree under a top-level deploy/ directory, so extracting it and running
# `sudo -E bash deploy/production/check.sh` (root is required) works exactly
# like the checkout. With Git available only tracked files are copied; the
# fallback excludes local secrets and build output by name, so a deploy/.env,
# inventory.yml, *.pem or rendered admin-token snippet never leaks into a
# release artifact.
printf 'Packaging deploy kit\n'
STAGE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ajs-deploy-kit.XXXXXX") \
  || die "cannot create a temporary staging directory"
KIT_DIR="$STAGE_DIR/deploy"
mkdir -p "$KIT_DIR"
if command -v git >/dev/null 2>&1 && git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  while IFS= read -r -d '' _rel; do
    [ -f "$ROOT/$_rel" ] || continue
    _dst="$STAGE_DIR/${_rel}"
    mkdir -p "$(dirname -- "$_dst")"
    cp -p "$ROOT/$_rel" "$_dst"
  done < <(git -C "$ROOT" ls-files -z -- deploy)
else
  while IFS= read -r -d '' _rel; do
    _dst="$STAGE_DIR/${_rel}"
    mkdir -p "$(dirname -- "$_dst")"
    cp -p "$ROOT/$_rel" "$_dst"
  done < <(cd "$ROOT" && find deploy -type f \
      ! -path 'deploy/dist/*' \
      ! -path 'deploy/production/dist/*' \
      ! -name '.env' \
      ! -name 'inventory.yml' \
      ! -name '*.pem' \
      ! -name '*.key' \
      ! -name 'token-test.conf' \
      ! -name 'ajs-admin-token.conf' \
      -print0)
fi
[ -n "$(find "$KIT_DIR" -type f -print -quit)" ] || die "deploy kit is empty"
# The kit archive carries the repository notices next to deploy/, so the
# extracted tree is self-describing (R2).
for _notice in "${NOTICE_FILES[@]}"; do
  [ -s "$ROOT/$_notice" ] || die "required notice file is missing or empty: $_notice"
  cp -p "$ROOT/$_notice" "$STAGE_DIR/$_notice"
done
normalize_text_lf "$STAGE_DIR"
find "$STAGE_DIR" -exec touch -t 197001010000.00 {} +
(
  cd "$STAGE_DIR"
  if [ ${#TAR_OPTS[@]} -gt 0 ]; then
    find . -type f -print | LC_ALL=C sort | tar -czf "$OUT_DIR/$DEPLOY_KIT_NAME" "${TAR_OPTS[@]}" -T -
  else
    find . -type f -print | LC_ALL=C sort | tar -czf "$OUT_DIR/$DEPLOY_KIT_NAME" -T -
  fi
)
[ -s "$OUT_DIR/$DEPLOY_KIT_NAME" ] || die "deploy kit tar is empty"
assert_notice_members "$OUT_DIR/$DEPLOY_KIT_NAME" "$DEPLOY_KIT_NAME"
rm -rf "$STAGE_DIR"
STAGE_DIR=""

# MANIFEST records the inputs and the per-artifact digests. It is written
# before SHA256SUMS so the checksum file can cover it.
{
  printf 'ajs-accountd packaging manifest\n'
  printf 'version: %s\n' "$VERSION"
  printf 'commit: %s\n' "$COMMIT"
  printf 'worktree: %s\n' "$WORKTREE"
  printf 'source_date_epoch: %s\n' "$SOURCE_DATE_EPOCH"
  printf 'built_utc: %s\n' "$(format_epoch "$SOURCE_DATE_EPOCH")"
  printf 'go: %s\n' "$GO_VERSION"
  printf 'dashboard_version: %s\n' "$DASHBOARD_VERSION"
  printf 'dashboard_mode: live\n'
  printf 'dashboard_manifest: ajs-build.json %s bytes %s\n' \
    "$(file_size "$DASHBOARD_DIST/ajs-build.json")" "$(hash_file "$DASHBOARD_DIST/ajs-build.json")"
  printf 'build_flags: %s -ldflags="%s"\n' "$BUILD_FLAGS" "$LDFLAGS"
  printf 'targets:\n'
  for target in "${TARGETS[@]}"; do
    # shellcheck disable=SC2086
    set -- $target
    printf '  %s: GOOS=%s GOARCH=%s CGO_ENABLED=0\n' "$3" "$1" "$2"
  done
  printf 'dashboard_source: %s\n' "$DASHBOARD_SOURCE"
  printf 'notices: %s\n' "${NOTICE_FILES[*]}"
  printf 'deploy_kit: %s %s bytes %s\n' \
    "$DEPLOY_KIT_NAME" "$(file_size "$OUT_DIR/$DEPLOY_KIT_NAME")" "$(hash_file "$OUT_DIR/$DEPLOY_KIT_NAME")"
  printf 'artifacts:\n'
  for name in "${ARTIFACT_NAMES[@]}"; do
    case "$name" in
      MANIFEST.txt|SHA256SUMS) continue ;;
    esac
    printf '  %s %s bytes %s\n' \
      "$name" "$(file_size "$OUT_DIR/$name")" "$(hash_file "$OUT_DIR/$name")"
  done
} > "$OUT_DIR/MANIFEST.txt"

(
  cd "$OUT_DIR"
  for name in "${ARTIFACT_NAMES[@]}"; do
    [ "$name" = SHA256SUMS ] && continue
    printf '%s  %s\n' "$(hash_file "$OUT_DIR/$name")" "$name"
  done
) > "$OUT_DIR/SHA256SUMS"

if command -v sha256sum >/dev/null 2>&1; then
  CHECK_CMD='sha256sum -c SHA256SUMS'
else
  CHECK_CMD='shasum -a 256 -c SHA256SUMS'
fi

printf '\nPackaged %s into %s\n' "$VERSION" "$OUT_DIR"
for name in "${ARTIFACT_NAMES[@]}"; do
  printf '  %-32s %s bytes\n' "$name" "$(file_size "$OUT_DIR/$name")"
done
printf '  %-32s %s bytes\n' "$DEPLOY_KIT_NAME" "$(file_size "$OUT_DIR/$DEPLOY_KIT_NAME")"
printf '\nVerify with: (cd %q && %s)\n' "$OUT_DIR" "$CHECK_CMD"
