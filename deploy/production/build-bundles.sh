#!/usr/bin/env bash
# Build the token-free production bundles.
#
# Both the customer portal and the admin console use the same static SPA. The
# build is live (VITE_USE_MOCK=false), same-origin (VITE_API_BASE empty), and
# deliberately has NO VITE_PANEL_TOKEN: a Vite VITE_* value is compiled into
# public JavaScript, so a token must never be baked into either bundle.
#
# The customer origin serves only /portal* paths (nginx enforces that); the
# admin origin is loopback/VPN-only and nginx injects the bearer server-side.
#
# Usage:
#   bash deploy/production/build-bundles.sh [--out DIR]
#
# Output (default deploy/production/dist):
#   DIR/portal/   static bundle for the customer origin
#   DIR/admin/    static bundle for the admin origin
#
# Requirements: Node 24 / npm 11 and an installed dashboard/node_modules.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
dashboard_dir=${AJS_DASHBOARD_DIR:-$repo_root/dashboard}
out_dir=$script_dir/dist

while [ $# -gt 0 ]; do
    case $1 in
        --out)
            [ $# -ge 2 ] || { echo "build-bundles: --out needs a directory" >&2; exit 2; }
            out_dir=$2
            shift 2
            ;;
        -h|--help)
            sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "build-bundles: unknown argument: $1" >&2
            exit 2
            ;;
    esac
done

command -v npm >/dev/null 2>&1 || {
    echo "build-bundles: npm not found; install Node 24 / npm 11" >&2
    exit 2
}

[ -f "$dashboard_dir/package.json" ] || {
    echo "build-bundles: $dashboard_dir/package.json not found" >&2
    exit 2
}

if [ ! -d "$dashboard_dir/node_modules" ]; then
    echo "build-bundles: $dashboard_dir/node_modules missing; run: (cd \"$dashboard_dir\" && npm ci)" >&2
    exit 2
fi

# Refuse to build if the caller exported a token: Vite would inline it into
# public JavaScript before this script could notice.
if [ -n "${VITE_PANEL_TOKEN:-}" ]; then
    echo "build-bundles: refusing to build with VITE_PANEL_TOKEN set; the customer bundle must be token-free" >&2
    exit 1
fi

# Build once, then copy: the portal and admin bundles are the same artifact,
# separated only by the nginx vhost that serves them.
echo "build-bundles: building token-free live bundle (VITE_USE_MOCK=false, no VITE_PANEL_TOKEN)"
(
    cd "$dashboard_dir"
    env VITE_USE_MOCK=false VITE_API_BASE= VITE_PANEL_TOKEN= npm run build
)

[ -f "$dashboard_dir/dist/index.html" ] || {
    echo "build-bundles: build produced no dist/index.html" >&2
    exit 1
}

mkdir -p "$out_dir"
rm -rf "$out_dir/portal" "$out_dir/admin"
cp -a "$dashboard_dir/dist" "$out_dir/portal"
cp -a "$dashboard_dir/dist" "$out_dir/admin"

echo "build-bundles: OK"
echo "  portal: $out_dir/portal"
echo "  admin:  $out_dir/admin"
