#!/bin/sh
# Render the nginx admin htpasswd file from the root-only operator inventory.
#
# The inventory is a plain-text list of `username:password` pairs, one per
# line, mode 0600, never committed. This script hashes each password with
# OpenSSL's apr1 implementation (supported by nginx's auth_basic_user_file)
# and writes the result atomically as root:<nginx-worker-group> mode 0640.
# It never prints a password or a hash.
#
# Why group-readable: nginx reads auth_basic_user_file in its worker process,
# not in the root master. The worker runs as the `user` from nginx.conf
# (www-data on Debian/Ubuntu, nginx on RHEL). A root:root 0600 file makes
# every valid operator request fail with HTTP 500 and an "open() ...
# failed (13: Permission denied)" error in the worker log. Mode 0640 with the
# worker group is the minimum that works; world-readable output is refused.
#
# Usage:
#   sudo sh deploy/production/render-admin-htpasswd.sh [inventory] [destination] [group]
#
# Defaults:
#   inventory    /etc/ajs/admin-operators
#   destination  /etc/nginx/ajs-admin.htpasswd
#   group        detected from the nginx `user` directive (AJS_NGINX_CONF,
#                default /etc/nginx/nginx.conf), then `nginx -V`; override
#                with AJS_HTPASSWD_GROUP (AJS_NGINX_GROUP alias) or the third
#                argument.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
inventory=${1:-/etc/ajs/admin-operators}
dest=${2:-/etc/nginx/ajs-admin.htpasswd}
group_override=${3:-${AJS_HTPASSWD_GROUP:-${AJS_NGINX_GROUP:-}}}

group_exists() {
    if command -v getent >/dev/null 2>&1; then
        getent group "$1" >/dev/null 2>&1
    else
        grep -q "^$1:" /etc/group 2>/dev/null
    fi
}

group_for_user() {
    user=$1
    if command -v getent >/dev/null 2>&1; then
        gid=$(getent passwd "$user" 2>/dev/null | cut -d: -f4)
        if [ -n "$gid" ]; then
            name=$(getent group "$gid" 2>/dev/null | cut -d: -f1)
            if [ -n "$name" ]; then
                printf '%s\n' "$name"
                return 0
            fi
        fi
    fi
    if command -v id >/dev/null 2>&1; then
        id -gn "$user" 2>/dev/null && return 0
    fi
    return 1
}

detect_group() {
    if [ -n "$group_override" ]; then
        printf '%s\n' "$group_override"
        return 0
    fi

    conf=${AJS_NGINX_CONF:-}
    if [ -n "$conf" ]; then
        [ -r "$conf" ] || {
            echo "render-admin-htpasswd: AJS_NGINX_CONF is not readable: $conf" >&2
            return 1
        }
    else
        for candidate in /etc/nginx/nginx.conf "$script_dir/nginx/nginx.conf"; do
            if [ -r "$candidate" ]; then
                conf=$candidate
                break
            fi
        done
    fi

    if [ -n "$conf" ]; then
        user_line=$(sed -n 's/^[[:space:]]*user[[:space:]][[:space:]]*\([^;]*\);.*$/\1/p' "$conf" | sed -n '1p')
        if [ -n "$user_line" ]; then
            nginx_user=$(printf '%s\n' "$user_line" | awk '{print $1}')
            nginx_group=$(printf '%s\n' "$user_line" | awk '{print $2}')
            if [ -n "$nginx_group" ]; then
                printf '%s\n' "$nginx_group"
                return 0
            fi
            if [ -n "$nginx_user" ] && group_for_user "$nginx_user"; then
                return 0
            fi
        fi
    fi

    if command -v nginx >/dev/null 2>&1; then
        nginx_group=$(nginx -V 2>&1 | tr ' ' '\n' | sed -n 's/^--group=//p' | sed -n '1p')
        if [ -n "$nginx_group" ]; then
            printf '%s\n' "$nginx_group"
            return 0
        fi
    fi

    for fallback in www-data nginx; do
        if group_exists "$fallback"; then
            printf '%s\n' "$fallback"
            return 0
        fi
    done
    return 1
}

file_mode() {
    stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null
}

file_owner() {
    stat -c '%U' "$1" 2>/dev/null || stat -f '%Su' "$1" 2>/dev/null
}

file_group() {
    stat -c '%G' "$1" 2>/dev/null || stat -f '%Sg' "$1" 2>/dev/null
}

# Every directory between / and the destination must be searchable by the
# nginx worker group (or by other, as /etc usually is). A 0700 root directory
# in the chain defeats a 0640 file just as surely as a 0600 file does.
check_traversal() {
    dir=$1
    while :; do
        if [ ! -d "$dir" ]; then
            echo "render-admin-htpasswd: destination directory is not a directory: $dir" >&2
            return 1
        fi
        mode=$(file_mode "$dir")
        case $mode in
            '')
                echo "render-admin-htpasswd: cannot stat destination directory: $dir" >&2
                return 1
                ;;
        esac
        if [ $((0$mode & 0011)) -eq 0 ]; then
            echo "render-admin-htpasswd: $dir is not searchable by group '$worker_group'; nginx workers cannot reach $dest" >&2
            return 1
        fi
        [ "$dir" = "/" ] && break
        parent=$(dirname -- "$dir")
        [ "$parent" = "$dir" ] && break
        dir=$parent
    done
    return 0
}

worker_group=$(detect_group) || {
    echo "render-admin-htpasswd: cannot determine the nginx worker group; set AJS_HTPASSWD_GROUP or fix AJS_NGINX_CONF" >&2
    exit 2
}

group_exists "$worker_group" || {
    echo "render-admin-htpasswd: nginx worker group does not exist: $worker_group" >&2
    exit 2
}
if command -v getent >/dev/null 2>&1; then
    resolved_group=$(getent group "$worker_group" 2>/dev/null | cut -d: -f1)
    [ -n "$resolved_group" ] && worker_group=$resolved_group
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "render-admin-htpasswd: must run as root so the output is owned root:$worker_group (use sudo)" >&2
    exit 2
fi

[ -r "$inventory" ] || {
    echo "render-admin-htpasswd: inventory not readable: $inventory" >&2
    echo "render-admin-htpasswd: copy admin-operators.example to /etc/ajs/admin-operators (mode 0600) first" >&2
    exit 2
}

command -v openssl >/dev/null 2>&1 || {
    echo "render-admin-htpasswd: openssl is required to hash passwords" >&2
    exit 2
}

dest_dir=$(dirname -- "$dest")
mkdir -p -- "$dest_dir" || exit 2
[ ! -d "$dest" ] || {
    echo "render-admin-htpasswd: destination is a directory: $dest" >&2
    exit 2
}
check_traversal "$dest_dir" || exit 2

# The temporary file lives in the destination directory so the final rename
# is atomic. umask 077 keeps it private until the explicit 0640 below.
umask 077
tmp=$(mktemp "$dest_dir/.ajs-admin.htpasswd.XXXXXX") || exit 2
trap 'rm -f "$tmp"' EXIT HUP INT TERM

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

chown root:"$worker_group" "$tmp" || {
    echo "render-admin-htpasswd: cannot set root:$worker_group ownership on the rendered file" >&2
    exit 2
}
chmod 0640 "$tmp" || {
    echo "render-admin-htpasswd: cannot set mode 0640 on the rendered file" >&2
    exit 2
}

mode=$(file_mode "$tmp")
case $mode in
    '')
        echo "render-admin-htpasswd: cannot verify the rendered file mode" >&2
        exit 2
        ;;
esac
if [ $((0$mode & 0007)) -ne 0 ]; then
    echo "render-admin-htpasswd: refusing world-readable output (mode $mode); expected 0640" >&2
    exit 2
fi
if [ $((0$mode & 0777)) -ne $((0640)) ]; then
    echo "render-admin-htpasswd: refusing unsafe output mode $mode; expected 0640" >&2
    exit 2
fi

mv -f "$tmp" "$dest" || {
    echo "render-admin-htpasswd: cannot install $dest" >&2
    exit 2
}

mode=$(file_mode "$dest")
owner=$(file_owner "$dest")
group=$(file_group "$dest")
if [ "$mode" != "640" ] || [ "$owner" != "root" ] || [ "$group" != "$worker_group" ]; then
    echo "render-admin-htpasswd: post-install verification failed: $dest is $owner:$group mode $mode (expected root:$worker_group mode 0640)" >&2
    exit 2
fi

echo "render-admin-htpasswd: wrote $dest for $count operator(s) (root:$worker_group, mode 0640; secrets not printed)"
