#!/bin/sh
# Render the nginx admin bearer snippet from AJS_ADMIN_TOKEN.
#
# The token is never written to the repository and never sent to a browser:
# nginx reads this root-only file at config load and injects the Authorization
# header on every admin /v1/ request.
#
# Usage:
#   sudo AJS_ADMIN_TOKEN="$(cat /etc/ajs/admin.token)" \
#       sh deploy/production/render-admin-token.sh [destination]
#
# Default destination: /etc/nginx/snippets/ajs-admin-token.conf
# The file is written with mode 0600 and is deliberately not readable by
# unprivileged users (nginx workers do not need it; only the master does).
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template=$script_dir/nginx/snippets/ajs-admin-token.conf.template
dest=${1:-/etc/nginx/snippets/ajs-admin-token.conf}

[ -r "$template" ] || {
    echo "render-admin-token: template not found: $template" >&2
    exit 2
}

token=${AJS_ADMIN_TOKEN:-}
if [ -z "$token" ] && [ -r /etc/ajs/admin.token ]; then
    token=$(cat /etc/ajs/admin.token)
fi
if [ -z "$token" ]; then
    echo "render-admin-token: set AJS_ADMIN_TOKEN (or /etc/ajs/admin.token)" >&2
    exit 2
fi

# The daemon refuses tokens shorter than 32 characters and the placeholders it
# ships. Mirror that rule here so nginx cannot inject a token the daemon will
# reject.
case $token in
    change-me|changeme|change_me|admin|test|password|secret|local-test-token|replace-me-with-openssl-rand-hex-32)
        echo "render-admin-token: refusing placeholder token" >&2
        exit 2
        ;;
esac
if [ "${#token}" -lt 32 ]; then
    echo "render-admin-token: AJS_ADMIN_TOKEN must be at least 32 characters" >&2
    exit 2
fi
case $token in
    *[!A-Za-z0-9._~+/=-]*)
        echo "render-admin-token: token contains characters that are unsafe in an nginx header" >&2
        exit 2
        ;;
esac

tmp=$(mktemp) || exit 2
trap 'rm -f "$tmp"' EXIT HUP INT TERM
umask 077
sed "s|\${AJS_ADMIN_TOKEN}|$token|" "$template" > "$tmp"

mkdir -p "$(dirname -- "$dest")"
if command -v install >/dev/null 2>&1; then
    install -m 0600 "$tmp" "$dest"
else
    cp "$tmp" "$dest"
    chmod 0600 "$dest"
fi

echo "render-admin-token: wrote $dest (mode 0600; token not printed)"
