#!/bin/sh
# AJS panel nftables drift check (read-only).
#
# Compares the AJS rule in /etc/nftables.conf on the target host with the block owned by
# deploy/nftables/ajs-panel.nft, ignoring comments and blank lines.
#
# Usage: sh deploy/check-nftables-drift.sh [host]     (default host: hmc)
# Exit:  0 = OK, 1 = drift (MISSING or EXTRA), 2 = error (ssh, unreadable or malformed asset).
#
# This script never writes to the remote host and never reloads nftables. It compares the
# config file, not the running ruleset.

set -u

host=${1:-hmc}
remote_conf=/etc/nftables.conf
marker='tcp dport { 2261, 18082 }'

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || {
    printf 'host:  %s\nasset: (unknown)\nstate: ERROR - cannot resolve script directory\n' "${host}"
    exit 2
}
asset=$script_dir/nftables/ajs-panel.nft

tmp_dir=
cleanup() {
    [ -n "$tmp_dir" ] && rm -rf "$tmp_dir"
}
trap cleanup 0 HUP INT TERM

fail() {
    printf 'host:  %s\nasset: %s\nstate: ERROR - %s\n' "$host" "$asset" "$1"
    exit 2
}

case $host in
    -*) fail "host must not start with '-'" ;;
esac

[ -r "$asset" ] || fail "cannot read $asset"

# Non-comment, non-blank lines, with CRLF and repeated whitespace normalised.
normalize_rules() {
    awk '
        { sub(/\r$/, "") }
        /^[[:space:]]*#/ { next }
        /^[[:space:]]*$/ { next }
        {
            gsub(/[[:space:]]+/, " ")
            sub(/^ /, "")
            sub(/ $/, "")
            print
        }
    '
}

tmp_dir=$(mktemp -d) || fail "cannot create a temporary directory"
repo_rules=$tmp_dir/repo.rules
live_rules=$tmp_dir/live.rules
diff_out=$tmp_dir/diff.out

normalize_rules < "$asset" > "$repo_rules" || fail "cannot normalise $asset"

repo_count=$(awk 'END { print NR }' "$repo_rules")
[ "$repo_count" -eq 1 ] || fail "asset must contain exactly one rule line, found $repo_count"
grep -Fq -- "$marker" "$repo_rules" || fail "asset rule does not contain marker: $marker"

live_conf=$(ssh -o BatchMode=yes -o ConnectTimeout=10 -- "$host" "cat $remote_conf" 2>/dev/null) ||
    fail "ssh $host could not read $remote_conf"

printf '%s\n' "$live_conf" | normalize_rules | grep -F -- "$marker" > "$live_rules" 2>/dev/null

if cmp -s "$repo_rules" "$live_rules"; then
    printf 'host:  %s\nasset: %s\nstate: OK - live block matches (%s rule, comments ignored)\n' \
        "$host" "$asset" "$repo_count"
    exit 0
fi

if [ ! -s "$live_rules" ]; then
    printf 'host:  %s\nasset: %s\nstate: MISSING - %s has no "%s" rule\n' \
        "$host" "$asset" "$remote_conf" "$marker"
    exit 1
fi

# Live has at least one marker line that differs from the repo rule. Report the unowned live
# lines as EXTRA, and say so when the repo rule is also absent from live.
diff -u "$repo_rules" "$live_rules" > "$diff_out" 2>/dev/null
extra=$(grep '^+[^+]' "$diff_out" | sed 's/^+//')
missing=$(grep '^-[^-]' "$diff_out" | sed 's/^-//')

if [ -n "$extra" ]; then
    detail=$(printf '%s\n' "$extra" | awk 'BEGIN { sep = "" } { printf "%s%s", sep, $0; sep = "; " }')
    if [ -n "$missing" ]; then
        printf 'host:  %s\nasset: %s\nstate: EXTRA - live has an AJS line the repo block does not own (%s); repo rule also missing from live\n' \
            "$host" "$asset" "$detail"
    else
        printf 'host:  %s\nasset: %s\nstate: EXTRA - live has an AJS line the repo block does not own: %s\n' \
            "$host" "$asset" "$detail"
    fi
    exit 1
fi

printf 'host:  %s\nasset: %s\nstate: MISSING - live AJS rule differs from the repo asset\n' \
    "$host" "$asset"
exit 1
