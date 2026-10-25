#!/usr/bin/env bash
# Reproducible release packaging for ajs-accountd and the operator dashboard.
#
#   bash deploy/package.sh --version 0.1.0 --out dist
#
# Produces, in --out:
#   ajs-accountd-linux-amd64      (GOOS=linux   GOARCH=amd64, CGO disabled)
#   ajs-accountd-windows-amd64.exe (GOOS=windows GOARCH=amd64, CGO disabled)
#   ajs-accountd-darwin-arm64     (GOOS=darwin  GOARCH=arm64, CGO disabled)
#   dashboard-dist.tar.gz         (the existing dashboard/dist bundle)
#   MANIFEST.txt                  (version, commit, toolchain, flags, hashes)
#   SHA256SUMS                    (sha256sum -c compatible)
#
# No Docker, no network, no CGO. The daemon module replaces ajsproxy with
# ../emulator, so both module trees must be present in this checkout.
#
# Reproducibility: binaries are built with -trimpath, -buildvcs=false,
# -ldflags="-s -w -buildid=", -mod=readonly and GOTOOLCHAIN=local. The
# dashboard tar is staged with normalized mtimes and a sorted file list.
# SOURCE_DATE_EPOCH defaults to the HEAD commit time, so the same Go
# toolchain, clean tree and tar implementation produce byte-identical
# artifacts.

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

  --version <version>       release version recorded in MANIFEST.txt
                            (default: git describe --tags --always --dirty,
                            else 0.0.0-dev)
  --out <dir>               output directory (default: <repo>/dist)
  --dashboard-dist <dir>    prebuilt dashboard bundle to package
                            (default: <repo>/dashboard/dist)
  --build-dashboard         run `npm run build` in dashboard/ before packaging
  -h, --help                show this help

Artifacts: ajs-accountd-linux-amd64, ajs-accountd-windows-amd64.exe,
ajs-accountd-darwin-arm64, dashboard-dist.tar.gz, MANIFEST.txt, SHA256SUMS.
EOF
}

VERSION=""
OUT_DIR="$ROOT/dist"
DASHBOARD_DIST=""
DASHBOARD_DIST_SET=0
BUILD_DASHBOARD=0

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
  || die "go not found in PATH (Go 1.26 or newer is required by daemon/go.mod)"
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
LDFLAGS='-s -w -buildid='
BUILD_FLAGS='CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -mod=readonly'

ARTIFACT_NAMES=(
  ajs-accountd-linux-amd64
  ajs-accountd-windows-amd64.exe
  ajs-accountd-darwin-arm64
  dashboard-dist.tar.gz
  MANIFEST.txt
  SHA256SUMS
)

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

# Build the dashboard when asked, or when the default bundle is absent but
# its node_modules are already installed. Never runs npm install: a fresh
# checkout needs the documented install step (packaging.md). An explicit
# --dashboard-dist is never silently replaced by a build of dashboard/dist.
if [ "$BUILD_DASHBOARD" = 1 ] && [ "$DASHBOARD_DIST_SET" = 1 ]; then
  die "--build-dashboard writes dashboard/dist; use --dashboard-dist only with a prebuilt bundle"
fi

if [ "$BUILD_DASHBOARD" = 1 ]; then
  command -v npm >/dev/null 2>&1 || die "--build-dashboard needs npm in PATH"
  printf 'Building dashboard bundle (mock mode unless VITE_* is set)\n'
  ( cd "$ROOT/dashboard" && npm run build )
fi

if [ -z "$DASHBOARD_DIST" ]; then
  DASHBOARD_DIST="$ROOT/dashboard/dist"
fi

if [ ! -f "$DASHBOARD_DIST/index.html" ]; then
  if [ "$DASHBOARD_DIST_SET" = 1 ]; then
    die "no dashboard bundle at $DASHBOARD_DIST; build it first or drop --dashboard-dist"
  fi
  if command -v npm >/dev/null 2>&1 && [ -d "$ROOT/dashboard/node_modules" ]; then
    printf 'package.sh: %s has no index.html; building the dashboard\n' "$DASHBOARD_DIST" >&2
    ( cd "$ROOT/dashboard" && npm run build )
  else
    die "no dashboard bundle at $DASHBOARD_DIST; run 'cd dashboard && npm install && npm run build', pass --dashboard-dist, or use --build-dashboard"
  fi
fi
DASHBOARD_DIST=$(CDPATH= cd -- "$DASHBOARD_DIST" && pwd)

mkdir -p "$OUT_DIR" || die "cannot create output directory: $OUT_DIR"
OUT_DIR=$(CDPATH= cd -- "$OUT_DIR" && pwd)

for name in "${ARTIFACT_NAMES[@]}"; do
  rm -f "$OUT_DIR/$name"
done

STAGE_DIR=""
cleanup() {
  if [ -n "$STAGE_DIR" ]; then
    rm -rf "$STAGE_DIR"
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
    find . -print | LC_ALL=C sort | tar -czf "$OUT_DIR/dashboard-dist.tar.gz" "${TAR_OPTS[@]}" -T -
  else
    find . -print | LC_ALL=C sort | tar -czf "$OUT_DIR/dashboard-dist.tar.gz" -T -
  fi
)
[ -s "$OUT_DIR/dashboard-dist.tar.gz" ] || die "dashboard tar is empty"
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
  printf 'build_flags: %s -ldflags="%s"\n' "$BUILD_FLAGS" "$LDFLAGS"
  printf 'targets:\n'
  for target in "${TARGETS[@]}"; do
    # shellcheck disable=SC2086
    set -- $target
    printf '  %s: GOOS=%s GOARCH=%s CGO_ENABLED=0\n' "$3" "$1" "$2"
  done
  printf 'dashboard_dist: %s\n' "$DASHBOARD_DIST"
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
printf '\nVerify with: (cd %q && %s)\n' "$OUT_DIR" "$CHECK_CMD"
