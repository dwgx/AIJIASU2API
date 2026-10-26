#!/bin/sh
# Render the nginx admin htpasswd file from the root-only operator inventory.
#
# The inventory is a plain-text list of `username:password` pairs, one per
# line, mode 0600, never committed. This script hashes each password with
# OpenSSL's apr1 implementation (supported by nginx's auth_basic_user_file)
# and writes the result atomically with mode 0600. It never prints a password
# or a hash.
#
# Usage:
#   sudo sh deploy/production/render-admin-htpasswd.sh [inventory] [destination]
#
# Defaults:
#   inventory    /etc/ajs/admin-operators
#   destination  /etc/nginx/ajs-admin.htpasswd
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
inventory=${1:-/etc/ajs/admin-operators}
dest=${2:-/etc/nginx/ajs-admin.htpasswd}

[ -r "$inventory" ] || {
    echo "render-admin-htpasswd: inventory not readable: $inventory" >&2
    echo "render-admin-htpasswd: copy admin-operators.example to /etc/ajs/admin-operators (mode 0600) first" >&2
    exit 2
}

command -v openssl >/dev/null 2>&1 || {
    echo "render-admin-htpasswd: openssl is required to hash passwords" >&2
    exit 2
}

tmp=$(mktemp) || exit 2
trap 'rm -f "$tmp"' EXIT HUP INT TERM
umask 077

count=0
lineno=0
seen=" "
while IFS= read -r line || [ -n "$line" ]; do
    lineno=$((lineno + 1))
    line=$(printf '%s' "$line" | tr -d '\r')
    stripped=$(printf '%s' "$line" | sed 's/^[[:space:]]*//')
    case $stripped in
        ''|'#'*) continue ;;
    esac
    case $line in
        *:*) ;;
        *)
            echo "render-admin-htpasswd: line $lineno has no ':' separator" >&2
            exit 2
            ;;
    esac
    user=${line%%:*}
    password=${line#*:}
    user=$(printf '%s' "$user" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
    if [ -z "$user" ] || [ -z "$password" ]; then
        echo "render-admin-htpasswd: line $lineno needs a non-empty username and password" >&2
        exit 2
    fi
    if [ "${#user}" -gt 64 ]; then
        echo "render-admin-htpasswd: username on line $lineno is longer than 64 characters" >&2
        exit 2
    fi
    case $user in
        *[!A-Za-z0-9._@+-]*)
            echo "render-admin-htpasswd: username '$user' uses characters outside A-Z a-z 0-9 . _ - @ +" >&2
            exit 2
            ;;
    esac
    case $seen in
        *" $user "*)
            echo "render-admin-htpasswd: duplicate username '$user'" >&2
            exit 2
            ;;
    esac
    seen="$seen$user "

    # Refuse the placeholders the repository ships and obviously weak
    # passwords. The daemon's shared bearer has a 32-character floor; a
    # per-operator password is a real login secret, so keep a 12-character
    # floor as the minimum honest bar.
    lower=$(printf '%s' "$password" | tr 'A-Z' 'a-z')
    case $lower in
        *replace-me*|*change-me*|*changeme*|*change_me*|*placeholder*)
            echo "render-admin-htpasswd: password for '$user' is a placeholder" >&2
            exit 2
            ;;
    esac
    case $lower in
        password|secret|admin|test|letmein)
            echo "render-admin-htpasswd: password for '$user' is a placeholder" >&2
            exit 2
            ;;
    esac
    if [ "${#password}" -lt 12 ]; then
        echo "render-admin-htpasswd: password for '$user' is shorter than 12 characters" >&2
        exit 2
    fi
    hash=$(openssl passwd -apr1 "$password") || {
        echo "render-admin-htpasswd: openssl failed to hash the password for '$user'" >&2
        exit 2
    }
    printf '%s:%s\n' "$user" "$hash" >> "$tmp"
    count=$((count + 1))
done < "$inventory"

[ "$count" -gt 0 ] || {
    echo "render-admin-htpasswd: inventory has no operators: $inventory" >&2
    exit 2
}

mkdir -p "$(dirname -- "$dest")"
if command -v install >/dev/null 2>&1; then
    install -m 0600 "$tmp" "$dest"
else
    cp "$tmp" "$dest"
    chmod 0600 "$dest"
fi

echo "render-admin-htpasswd: wrote $dest for $count operator(s) (mode 0600; secrets not printed)"
