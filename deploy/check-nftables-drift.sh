#!/bin/sh
# AJS panel nftables drift check (read-only).
#
# Compares the AJS rule in /etc/nftables.conf on the target host with the block owned by
# deploy/nftables/ajs-panel.nft, ignoring comments and blank lines.
#
# Usage: sh deploy/check-nftables-drift.sh [host]     (default host: hmc)
#        sh deploy/check-nftables-drift.sh --self-test
# Exit:  0 = OK, or OK-with-note for an allowlisted KNOWN_DIVERGENCE
#        1 = drift (MISSING or EXTRA)
#        2 = error (ssh, unreadable or malformed asset, bad arguments)
#
# This script never writes to the remote host and never reloads nftables. It compares the
# config file, not the running ruleset.

set -u

remote_conf=/etc/nftables.conf
marker='tcp dport { 2261, 18082 }'

# --- KNOWN_DIVERGENCE allowlist -------------------------------------------------------------
# Accepted live-only drift, reported as OK-with-note and exit 0. Each entry is one line:
#
#     <reason>|<exact normalised rule line>
#
# A live rule is accepted when its normalised rule equals the entry rule, or when the live
# line carries the entry's reason as a trailing "#" comment or as an nft comment "..."
# attribute. An entry whose rule equals the repo rule is only accepted by its label: a bare
# duplicate stays EXTRA. Anything not on this list is still EXTRA and exits 1. Keep entries
# narrow and delete them once the host and the repo converge.
#
# Entry 1 is the Owner-labelled HMC line: the AJS-120 block hand-added to
# hmc:/etc/nftables.conf:115-118, pending merge into ops/hmc/nftables.conf.
# Reason: AJS-120 block pending merge (owner: APC).
KNOWN_DIVERGENCE='
AJS-120 block pending merge (owner: APC)|ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
'

host=hmc
mode=check

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || {
    printf 'host:  %s\nasset: (unknown)\nstate: ERROR - cannot resolve script directory\n' "$host"
    exit 2
}
asset=$script_dir/nftables/ajs-panel.nft

usage() {
    cat <<'EOF'
usage: sh deploy/check-nftables-drift.sh [host]
       sh deploy/check-nftables-drift.sh --self-test
EOF
}

fail() {
    printf 'host:  %s\nasset: %s\nstate: ERROR - %s\n' "$host" "$asset" "$1"
    exit 2
}

case $# in
    0)
        ;;
    1)
        case $1 in
            --self-test) mode=self-test ;;
            --help|-h) usage; exit 0 ;;
            -*) fail "unknown option: $1" ;;
            *) host=$1 ;;
        esac
        ;;
    *)
        fail "expected at most one argument (host or --self-test)"
        ;;
esac

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

# Like normalize_rules, but emits "<rule>\t<label>". The label is the comment block
# immediately above the rule plus any trailing "# ..." comment or nft comment "..."
# attribute. Labels are only used to recognise KNOWN_DIVERGENCE entries.
parse_live_rules() {
    awk '
        function trim(s) {
            sub(/^[[:space:]]+/, "", s)
            sub(/[[:space:]]+$/, "", s)
            return s
        }
        {
            sub(/\r$/, "")
            line = $0
            if (line ~ /^[[:space:]]*$/) {
                pending = ""
                next
            }
            if (line ~ /^[[:space:]]*#/) {
                c = line
                sub(/^[[:space:]]*#[[:space:]]*/, "", c)
                pending = pending (pending == "" ? "" : " ") c
                next
            }
            label = pending
            pending = ""
            # nft comment "..." attribute: keep it as a label, not as part of the rule.
            if (match(line, /comment[[:space:]]+"[^"]*"/)) {
                attr = substr(line, RSTART, RLENGTH)
                label = label (label == "" ? "" : " ") attr
                line = substr(line, 1, RSTART - 1) substr(line, RSTART + RLENGTH)
            }
            # trailing shell-style comment
            if (match(line, /[[:space:]]+#/)) {
                c = substr(line, RSTART + 1)
                sub(/^[[:space:]]*#?[[:space:]]*/, "", c)
                label = label (label == "" ? "" : " ") c
                line = substr(line, 1, RSTART - 1)
            }
            line = trim(line)
            gsub(/[[:space:]]+/, " ", line)
            label = trim(label)
            gsub(/[[:space:]]+/, " ", label)
            if (line == "") next
            print line "\t" label
        }
    '
}

release_tmp() {
    [ "${_owns_tmp:-0}" -eq 1 ] && rm -rf "$_tmp"
}

# check_pair <asset-file> <live-file> <host-label> <remote-path> [scratch-dir]
# Prints the verdict; returns 0 = OK/OK-with-note, 1 = drift, 2 = error.
check_pair() {
    _asset=$1
    _live=$2
    _host=$3
    _remote=$4
    if [ -n "${5-}" ]; then
        _tmp=$5
        _owns_tmp=0
    else
        _tmp=$(mktemp -d) || {
            printf 'host:  %s\nasset: %s\nstate: ERROR - cannot create a temporary directory\n' "$_host" "$_asset"
            return 2
        }
        _owns_tmp=1
    fi
    _repo=$_tmp/repo.rules
    _live_all=$_tmp/live.all
    _live_ajs=$_tmp/live.ajs
    _live_text=$_tmp/live.rules
    _extras=$_tmp/extras.tsv
    _allow=$_tmp/allow.tsv
    _classified=$_tmp/classified.tsv

    if [ ! -r "$_live" ]; then
        printf 'host:  %s\nasset: %s\nstate: ERROR - cannot read %s\n' "$_host" "$_asset" "$_live"
        release_tmp
        return 2
    fi

    if ! normalize_rules < "$_asset" > "$_repo" 2>/dev/null; then
        printf 'host:  %s\nasset: %s\nstate: ERROR - cannot normalise %s\n' "$_host" "$_asset" "$_asset"
        release_tmp
        return 2
    fi

    _repo_count=$(awk 'END { print NR }' "$_repo")
    if [ "$_repo_count" -ne 1 ]; then
        printf 'host:  %s\nasset: %s\nstate: ERROR - asset must contain exactly one rule line, found %s\n' \
            "$_host" "$_asset" "$_repo_count"
        release_tmp
        return 2
    fi

    _repo_rule=$(cat "$_repo")
    if ! grep -Fq -- "$marker" "$_repo"; then
        printf 'host:  %s\nasset: %s\nstate: ERROR - asset rule does not contain marker: %s\n' \
            "$_host" "$_asset" "$marker"
        release_tmp
        return 2
    fi

    if ! parse_live_rules < "$_live" > "$_live_all" 2>/dev/null; then
        printf 'host:  %s\nasset: %s\nstate: ERROR - cannot parse %s\n' "$_host" "$_asset" "$_live"
        release_tmp
        return 2
    fi

    awk -F'\t' -v m="$marker" 'index($1, m) { print $1 "\t" $2 }' "$_live_all" > "$_live_ajs"
    if [ ! -s "$_live_ajs" ]; then
        printf 'host:  %s\nasset: %s\nstate: MISSING - %s has no "%s" rule\n' \
            "$_host" "$_asset" "$_remote" "$marker"
        release_tmp
        return 1
    fi

    cut -f1 "$_live_ajs" > "$_live_text"
    if ! grep -Fqx -- "$_repo_rule" "$_live_text"; then
        printf 'host:  %s\nasset: %s\nstate: MISSING - live AJS rule differs from the repo asset\n' \
            "$_host" "$_asset"
        release_tmp
        return 1
    fi

    # Every live AJS rule except one occurrence of the repo rule is an extra. A duplicate of
    # the repo rule is drift too; only an allowlisted KNOWN_DIVERGENCE can excuse it.
    awk -F'\t' -v repo="$_repo_rule" '
        !used && $1 == repo { used = 1; next }
        { print $1 "\t" $2 }
    ' "$_live_ajs" > "$_extras"

    printf '%s\n' "$KNOWN_DIVERGENCE" |
        awk '{ sub(/\r$/, "") } NF && $0 !~ /^[[:space:]]*#/' > "$_allow"

    awk -F'\t' -v OFS='\t' -v repo="$_repo_rule" '
        FILENAME == ARGV[1] {
            line = $0
            i = index(line, "|")
            if (i > 0) {
                n++
                reason[n] = substr(line, 1, i - 1)
                rule[n] = substr(line, i + 1)
            }
            next
        }
        {
            er = $1
            lab = $2
            hit = 0
            for (i = 1; i <= n; i++) {
                # An entry may name a distinct live-only rule. A duplicate of the repo
                # rule is never accepted by rule equality alone: it needs the label.
                if (er == rule[i] && er != repo) {
                    print "known", reason[i], er
                    hit = 1
                    break
                }
                if (lab != "" && index(lab, reason[i]) > 0) {
                    print "known", reason[i], er
                    hit = 1
                    break
                }
            }
            if (!hit) print "unknown", er, lab
        }
    ' "$_allow" "$_extras" > "$_classified"

    _unknown=$(awk -F'\t' '$1 == "unknown" { n++ } END { print n + 0 }' "$_classified")
    _known=$(awk -F'\t' '$1 == "known" { n++ } END { print n + 0 }' "$_classified")

    if [ "$_unknown" -gt 0 ]; then
        _detail=$(awk -F'\t' '
            $1 == "unknown" {
                s = s (s == "" ? "" : "; ") $2
                if ($3 != "") s = s " [" $3 "]"
            }
            END { print s }
        ' "$_classified")
        if [ "$_known" -gt 0 ]; then
            _notes=$(awk -F'\t' '
                $1 == "known" { s = s (s == "" ? "" : "; ") $2 " (" $3 ")" }
                END { print s }
            ' "$_classified")
            printf 'host:  %s\nasset: %s\nstate: EXTRA - live has an AJS line the repo block does not own: %s; accepted KNOWN_DIVERGENCE: %s\n' \
                "$_host" "$_asset" "$_detail" "$_notes"
        else
            printf 'host:  %s\nasset: %s\nstate: EXTRA - live has an AJS line the repo block does not own: %s\n' \
                "$_host" "$_asset" "$_detail"
        fi
        release_tmp
        return 1
    fi

    if [ "$_known" -gt 0 ]; then
        _notes=$(awk -F'\t' '
            $1 == "known" { s = s (s == "" ? "" : "; ") $2 " (" $3 ")" }
            END { print s }
        ' "$_classified")
        printf 'host:  %s\nasset: %s\nstate: OK-with-note - live block matches (%s rule, comments ignored); KNOWN_DIVERGENCE: %s\n' \
            "$_host" "$_asset" "$_repo_count" "$_notes"
        release_tmp
        return 0
    fi

    # A matching rule may itself carry an allowlisted label (for example an nft comment
    # attribute). Report that as OK-with-note rather than silently dropping the label.
    _repo_label=$(awk -F'\t' -v repo="$_repo_rule" '$1 == repo { print $2; exit }' "$_live_ajs")
    _repo_note=$(printf '%s\n' "$KNOWN_DIVERGENCE" | awk -F'|' -v lab="$_repo_label" '
        NF >= 2 { if (lab != "" && index(lab, $1) > 0) { print $1; exit } }
    ')
    if [ -n "$_repo_note" ]; then
        printf 'host:  %s\nasset: %s\nstate: OK-with-note - live block matches (%s rule, comments ignored); KNOWN_DIVERGENCE: %s (%s)\n' \
            "$_host" "$_asset" "$_repo_count" "$_repo_note" "$_repo_rule"
        release_tmp
        return 0
    fi

    printf 'host:  %s\nasset: %s\nstate: OK - live block matches (%s rule, comments ignored)\n' \
        "$_host" "$_asset" "$_repo_count"
    release_tmp
    return 0
}

# run_self_test exercises the comparison engine with fixtures: a plain match, an owner-labelled
# known divergence, an unlabelled extra, an unlabelled duplicate, a missing rule and a malformed
# asset. It never invokes ssh.
run_self_test() {
    _st=$(mktemp -d) || {
        printf 'self-test: FAIL - cannot create a temporary directory\n'
        return 1
    }
    _pass=0
    _fail=0
    _total=0
    _work=$_st/work
    mkdir -p "$_work" || {
        printf 'self-test: FAIL - cannot create %s\n' "$_work"
        rm -rf "$_st"
        return 1
    }

    _asset=$_st/asset.nft
    cat > "$_asset" <<'EOF'
# fixture: the owned AJS rule
ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
EOF

    _bad_asset=$_st/asset-bad.nft
    cat > "$_bad_asset" <<'EOF'
# fixture: malformed asset with two rule lines
ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
ip saddr 192.168.11.0/24 tcp dport 2262 accept
EOF

    _live_ok=$_st/live-ok.conf
    cat > "$_live_ok" <<'EOF'
table inet egress {
    chain input {
        # unrelated comments are ignored
        ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
    }
}
EOF

    _live_known_rule=$_st/live-known-rule.conf
    cat > "$_live_known_rule" <<'EOF'
table inet egress {
    chain input {
        ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
        # The Owner-labelled HMC line, pending merge into ops/hmc/nftables.conf.
        ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept # AJS-120 block pending merge (owner: APC)
    }
}
EOF

    _live_extra=$_st/live-extra.conf
    cat > "$_live_extra" <<'EOF'
table inet egress {
    chain input {
        ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
        ip saddr 192.168.12.0/24 tcp dport { 2261, 18082 } accept
    }
}
EOF

    _live_duplicate=$_st/live-duplicate.conf
    cat > "$_live_duplicate" <<'EOF'
table inet egress {
    chain input {
        ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
        ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
    }
}
EOF

    _live_missing=$_st/live-missing.conf
    cat > "$_live_missing" <<'EOF'
table inet egress {
    chain input {
        ip saddr 192.168.11.0/24 tcp dport 22 accept
    }
}
EOF

    case_run() {
        _name=$1
        _want_rc=$2
        _want_text=$3
        _live=$4
        _asset_file=${5:-$_asset}
        _out=$(check_pair "$_asset_file" "$_live" "self-test" "/etc/nftables.conf" "$_work")
        _rc=$?
        _total=$((_total + 1))
        if [ "$_rc" -ne "$_want_rc" ]; then
            printf 'self-test: FAIL - %s: exit %s, want %s\n%s\n' "$_name" "$_rc" "$_want_rc" "$_out"
            _fail=$((_fail + 1))
            return 0
        fi
        if ! printf '%s\n' "$_out" | grep -Fq -- "$_want_text"; then
            printf 'self-test: FAIL - %s: output missing "%s"\n%s\n' "$_name" "$_want_text" "$_out"
            _fail=$((_fail + 1))
            return 0
        fi
        _pass=$((_pass + 1))
        printf 'self-test: ok - %s (exit %s)\n' "$_name" "$_rc"
    }

    case_run "match -> OK" 0 "state: OK - live block matches" "$_live_ok"
    case_run "owner-labelled known divergence -> OK-with-note" 0 "state: OK-with-note" "$_live_known_rule"
    case_run "unlabelled extra -> EXTRA" 1 "state: EXTRA" "$_live_extra"
    case_run "unlabelled duplicate -> EXTRA" 1 "state: EXTRA" "$_live_duplicate"
    case_run "missing rule -> MISSING" 1 "state: MISSING" "$_live_missing"
    case_run "malformed asset -> ERROR" 2 "state: ERROR" "$_live_ok" "$_bad_asset"

    if [ "$_fail" -ne 0 ]; then
        printf 'self-test: FAIL (%s/%s)\n' "$_pass" "$_total"
        rm -rf "$_st"
        return 1
    fi
    printf 'self-test: PASS (%s/%s)\n' "$_pass" "$_total"
    rm -rf "$_st"
    return 0
}

if [ "$mode" = self-test ]; then
    run_self_test
    exit $?
fi

[ -r "$asset" ] || fail "cannot read $asset"

tmp_dir=$(mktemp -d) || fail "cannot create a temporary directory"
cleanup() {
    [ -n "$tmp_dir" ] && rm -rf "$tmp_dir"
}
trap cleanup 0 HUP INT TERM

live_conf=$tmp_dir/live.conf
ssh -o BatchMode=yes -o ConnectTimeout=10 -- "$host" "cat $remote_conf" > "$live_conf" 2>/dev/null ||
    fail "ssh $host could not read $remote_conf"

check_pair "$asset" "$live_conf" "$host" "$remote_conf" "$tmp_dir"
exit $?
