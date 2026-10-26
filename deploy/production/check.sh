#!/usr/bin/env bash
# AJS production deploy-kit self-check.
#
# Read-only: it never installs, reloads nginx, edits the host firewall or
# starts the daemon. Everything that needs a live daemon/cert is staged in a
# throwaway temporary directory.
#
# Usage:
#   sudo -E bash deploy/production/check.sh          # production gate (default)
#   bash deploy/production/check.sh --dev            # development-only checks
#   AJS_CHECK_MODE=dev bash deploy/production/check.sh
#
# Exit:
#   0 = production gate passed with no skipped checks, or dev checks passed
#   1 = a check failed, a required production check was skipped, or a
#       non-root production invocation was refused
#   2 = bad usage
#
# Production mode requires root: the AJS-412 renderer proof creates root-owned
# 0640/0600 artifacts and switches to the nginx worker identity to read them.
# A non-root production run exits 1 before any checks and prints the exact
# `sudo -E` command; it can never report a production PASS. Production also
# requires a real `nginx -t` from host nginx, openssl for the throwaway
# certificate, systemd-analyze, nft syntax verification, and a Chromium-family
# browser for the CSP browser boot (AJS_CHROMIUM_BIN or chromium/chrome on
# PATH). A missing required tool is FAIL, never SKIP. Dev mode allows those
# tools to be absent and skips the root-only ownership proof with an explicit
# dev marker, but its result is printed as DEV-PASS/DEV-FAIL and can never be
# mistaken for a production PASS.
#
# Tools:
#   nginx (or set AJS_NGINX_BIN=/usr/sbin/nginx; Docker with
#   nginx:1.29-alpine + AJS_CHECK_NGINX_DOCKER=1, dev mode)
#   python3 + PyYAML (or ruby, or yq) for YAML validation
#   openssl for the throwaway nginx test certificate
#   systemd-analyze for unit verification
#   nft for firewall syntax verification (unshare -Urn or sudo -n fallback)
#   chromium/chrome (or AJS_CHROMIUM_BIN) for the CSP browser boot; required in
#   production mode, an explicit SKIP in dev mode when absent
set -u

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || {
    echo "check: cannot resolve script directory" >&2
    exit 1
}
kit=$script_dir
repo_root=$(CDPATH= cd -- "$kit/../.." && pwd) || repo_root=.

mode=${AJS_CHECK_MODE:-production}
while [ $# -gt 0 ]; do
    case $1 in
        --production|--prod) mode=production; shift ;;
        --dev|--development) mode=dev; shift ;;
        -h|--help)
            awk 'NR > 1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "$0"
            exit 0
            ;;
        *)
            printf 'check: unknown argument: %s (use --production, --dev or --help)\n' "$1" >&2
            exit 2
            ;;
    esac
done
case $mode in
    production|dev) ;;
    *)
        printf 'check: AJS_CHECK_MODE must be production or dev, got: %s\n' "$mode" >&2
        exit 2
        ;;
esac

# The AJS-412 renderer proof creates root-owned 0640/0600 artifacts and then
# switches to the nginx worker identity to prove it can read them. That is a
# real privilege requirement, not a missing optional tool: refuse a non-root
# production invocation up front instead of accumulating PASS lines and then
# ending in a required-skip FAIL. Dev mode remains usable without root.
if [ "$mode" = production ] && [ "$(id -u)" -ne 0 ]; then
    printf 'check: FAIL: production mode requires root for the AJS-412 ownership proof\n' >&2
    printf 'check: run it as: sudo -E bash %s\n' "$0" >&2
    exit 1
fi

checks=0
failures=0
skips=0
required_skips=0

pass() { checks=$((checks + 1)); printf 'PASS  %s\n' "$*"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); printf 'FAIL  %s\n' "$*"; }
skip() {
    skips=$((skips + 1))
    if [ "$mode" = production ]; then
        checks=$((checks + 1))
        failures=$((failures + 1))
        required_skips=$((required_skips + 1))
        printf 'FAIL  %s (required in production mode)\n' "$*"
    else
        printf 'SKIP  %s (dev mode)\n' "$*"
    fi
}
section() { printf '\n== %s ==\n' "$*"; }

# F50 acceptance policy: the single source of truth for the static and runtime
# CSP assertions below. The shared snippet must carry exactly this value, and
# no other CSP header may exist in the kit (two CSP headers intersect, so a
# stale duplicate silently breaks the bundle).
csp_policy="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

# Prove a file is readable by a specific identity. Root bypasses permission
# checks, so a mode/owner assertion alone cannot show that the service user can
# open the file: this drops to the uid/gid and clears supplementary groups.
# Returns 0 when readable, 1 when the identity is denied, 2 when this host
# offers no way to switch identity (the caller decides skip vs required FAIL).
identity_can_read() {
    local file=$1 user=$2 group=$3 uid gid
    gid=$(getent group "$group" 2>/dev/null | cut -d: -f3)
    uid=$(id -u "$user" 2>/dev/null)
    [ -n "$gid" ] && [ -n "$uid" ] || return 2
    if command -v setpriv >/dev/null 2>&1; then
        setpriv --reuid="$uid" --regid="$gid" --clear-groups -- \
            sh -c 'test -r "$1" && head -c 1 "$1" >/dev/null' sh "$file" 2>/dev/null && return 0
        return 1
    fi
    if command -v runuser >/dev/null 2>&1; then
        runuser -u "$user" -- sh -c 'test -r "$1" && head -c 1 "$1" >/dev/null' sh "$file" 2>/dev/null && return 0
        return 1
    fi
    if command -v su >/dev/null 2>&1; then
        su -s /bin/sh -c "test -r '$file' && head -c 1 '$file' >/dev/null" "$user" 2>/dev/null && return 0
        return 1
    fi
    return 2
}

printf 'check: mode=%s\n' "$mode"

# ---------------------------------------------------------------------------
section "required files"
required="
README.md
check.sh
csp-smoke.sh
build-bundles.sh
render-admin-token.sh
render-admin-htpasswd.sh
render-admin-operators.sh
admin-operators.example
inventory.example.yml
nginx/nginx.conf
nginx/ajs-mime.types
nginx/snippets/ajs-tls.conf
nginx/snippets/ajs-proxy-common.conf
nginx/snippets/ajs-security-headers.conf
nginx/snippets/ajs-admin-token.conf.template
nginx/snippets/ajs-admin-operator-token.conf
nginx/snippets/ajs-admin-operator.conf
nginx/sites-available/ajs-portal.conf
nginx/sites-available/ajs-admin.conf
nginx/sites-available/ajs-default.conf
systemd/ajs-accountd.service
systemd/ajs-accountd.env.example
firewall/ajs-production.nft
"
for rel in $required; do
    if [ -s "$kit/$rel" ]; then
        pass "present: $rel"
    else
        fail "missing or empty: $rel"
    fi
done

# ---------------------------------------------------------------------------
section "shell syntax"
for rel in check.sh build-bundles.sh csp-smoke.sh; do
    if bash -n "$kit/$rel" 2>/dev/null; then
        pass "bash -n $rel"
    else
        fail "bash -n $rel"
    fi
done
for rel in render-admin-token.sh render-admin-htpasswd.sh render-admin-operators.sh; do
    if sh -n "$kit/$rel" 2>/dev/null; then
        pass "sh -n $rel"
    else
        fail "sh -n $rel"
    fi
done

# ---------------------------------------------------------------------------
section "YAML"
yaml_checked=0
for f in "$kit"/*.yml "$kit"/*.yaml; do
    [ -f "$f" ] || continue
    yaml_checked=$((yaml_checked + 1))
    if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; then
        if python3 - "$f" <<'PY' >/dev/null 2>&1
import sys
import yaml

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as fh:
    data = yaml.safe_load(fh)
if not isinstance(data, dict):
    raise SystemExit("top level must be a mapping")
for key in ("portal", "admin", "tls", "daemon", "firewall"):
    if key not in data:
        raise SystemExit("missing top-level key: " + key)
if not isinstance(data.get("portal"), dict) or not data["portal"].get("origin"):
    raise SystemExit("portal.origin is required")
if not isinstance(data.get("admin"), dict) or not data["admin"].get("origin"):
    raise SystemExit("admin.origin is required")
if not data["admin"].get("operators_file"):
    raise SystemExit("admin.operators_file is required")
if not data["admin"].get("htpasswd_file"):
    raise SystemExit("admin.htpasswd_file is required")
if not data["admin"].get("strict_rbac") or not data["admin"].get("daemon_hash_file"):
    raise SystemExit("strict RBAC and daemon_hash_file are required")
PY
        then
            pass "YAML parses and has the required keys: $(basename "$f")"
        else
            fail "YAML invalid or missing required keys: $(basename "$f")"
        fi
    elif command -v ruby >/dev/null 2>&1; then
        if ruby -ryaml -e 'd = YAML.load_file(ARGV[0]); raise "not a mapping" unless d.is_a?(Hash)' "$f" >/dev/null 2>&1; then
            pass "YAML parses (ruby): $(basename "$f")"
        else
            fail "YAML invalid (ruby): $(basename "$f")"
        fi
    elif command -v yq >/dev/null 2>&1; then
        if yq eval '.' "$f" >/dev/null 2>&1; then
            pass "YAML parses (yq): $(basename "$f")"
        else
            fail "YAML invalid (yq): $(basename "$f")"
        fi
    else
        fail "no YAML parser available (install python3+PyYAML, ruby, or yq)"
    fi
done
[ "$yaml_checked" -gt 0 ] || fail "no YAML file found to validate"

# ---------------------------------------------------------------------------
section "secret literals"
secret_failures=0
while IFS= read -r -d '' f; do
    rel=${f#"$kit"/}
    case $rel in
        dist/*|dist-test/*|node_modules/*) continue ;;
    esac
    if grep -qE -- "-----BEGIN ([A-Z0-9]+ )?PRIVATE"" KEY-----" "$f"; then
        fail "private key material found in $rel"
        secret_failures=$((secret_failures + 1))
    fi
    if grep -qE -- "-----BEGIN CERTIFI""CATE-----" "$f"; then
        fail "certificate material found in $rel"
        secret_failures=$((secret_failures + 1))
    fi

    # A non-placeholder value assigned to a known secret variable.
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        value=${line#*=}
        value=${value%%#*}
        value=$(printf '%s' "$value" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
        case $value in
            ''|*'$'*|*'<'*|*'__'*|*'...'*|*replace-me*|*change-me*|*changeme*|*change_me*|*local-test-token*|*your-*|*example*) ;;
            *)
                fail "possible secret assignment in $rel: ${line%%=*}=<redacted>"
                secret_failures=$((secret_failures + 1))
                ;;
        esac
    done < <(grep -nE '^[[:space:]]*(export[[:space:]]+)?(AJS_ADMIN_TOKEN|AJS_MASTER_KEY|VITE_PANEL_TOKEN)=' "$f" 2>/dev/null || true)

    # A bearer value that is not a shell/template placeholder.
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        case $line in
            *'${AJS_ADMIN_TOKEN}'*|*'$AJS_ADMIN_TOKEN'*|*'Bearer <'*|*'Bearer ...'*|*'Bearer $'*) ;;
            *)
                fail "possible bearer literal in $rel: $(printf '%s' "$line" | cut -c1-160)"
                secret_failures=$((secret_failures + 1))
                ;;
        esac
    done < <(grep -nE 'Authorization[[:space:]]+"?Bearer[[:space:]]+[A-Za-z0-9._~+/=-]{16,}' "$f" 2>/dev/null || true)

    # A bare 64-hex literal is either a token or a key. Allow explicit
    # placeholder/example/fingerprint lines only.
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        case $line in
            *placeholder*|*example*|*Example*|*dummy*|*fingerprint*|*sha256*|*SHA256*|*checksum*|*commit*) ;;
            *)
                fail "possible 64-hex secret in $rel: $(printf '%s' "$line" | cut -c1-160)"
                secret_failures=$((secret_failures + 1))
                ;;
        esac
    done < <(grep -nE '[0-9a-fA-F]{64}' "$f" 2>/dev/null || true)
done < <(find "$kit" -type f -print0)

if [ "$secret_failures" -eq 0 ]; then
    pass "no private key, certificate, token or 64-hex literal in the kit"
fi

# The token template must contain only the shell placeholder, never a value.
if grep -qE 'Bearer[[:space:]]+"?[A-Za-z0-9]' "$kit/nginx/snippets/ajs-admin-token.conf.template"; then
    fail "admin token template contains a literal bearer value"
else
    pass "admin token template contains only the \${AJS_ADMIN_TOKEN} placeholder"
fi

# ---------------------------------------------------------------------------
section "nginx configuration (nginx -t)"
nginx_tmp=""
auth_tmp=""
htpasswd_tmp=""
unit_tmp=""
rbac_tmp=""
cleanup() {
    [ -n "$nginx_tmp" ] && rm -rf "$nginx_tmp"
    [ -n "$auth_tmp" ] && rm -rf "$auth_tmp"
    [ -n "$htpasswd_tmp" ] && rm -rf "$htpasswd_tmp"
    [ -n "$unit_tmp" ] && rm -rf "$unit_tmp"
    [ -n "$rbac_tmp" ] && rm -rf "$rbac_tmp"
    if command -v csp_stop >/dev/null 2>&1; then csp_stop; fi
    [ -n "${csp_tmp:-}" ] && rm -rf "$csp_tmp"
    return 0
}
trap cleanup 0 HUP INT TERM

nginx_bin=${AJS_NGINX_BIN:-}
if [ -z "$nginx_bin" ]; then
    nginx_bin=$(command -v nginx 2>/dev/null || true)
fi
if [ -z "$nginx_bin" ]; then
    for candidate in /usr/sbin/nginx /usr/local/sbin/nginx /usr/local/bin/nginx; do
        if [ -x "$candidate" ]; then nginx_bin=$candidate; break; fi
    done
fi

# nginx worker identity: auth_basic_user_file is opened by the worker, not the
# root master. Use the host's installed config when present, otherwise the kit
# copy that nginx -t stages. Both the htpasswd runtime proof and the operator
# auth probe use this identity.
if [ -n "${AJS_NGINX_CONF:-}" ]; then
    worker_conf=$AJS_NGINX_CONF
elif [ -r /etc/nginx/nginx.conf ]; then
    worker_conf=/etc/nginx/nginx.conf
else
    worker_conf=$kit/nginx/nginx.conf
fi
worker_user_line=$(sed -n 's/^[[:space:]]*user[[:space:]][[:space:]]*\([^;]*\);.*$/\1/p' "$worker_conf" 2>/dev/null | sed -n '1p')
worker_user=$(printf '%s\n' "$worker_user_line" | awk '{print $1}')
worker_group=$(printf '%s\n' "$worker_user_line" | awk '{print $2}')
[ -n "$worker_user" ] || worker_user=www-data
if [ -z "$worker_group" ] && command -v getent >/dev/null 2>&1; then
    worker_gid=$(getent passwd "$worker_user" 2>/dev/null | cut -d: -f4)
    if [ -n "$worker_gid" ]; then
        worker_group=$(getent group "$worker_gid" 2>/dev/null | cut -d: -f1)
    fi
fi
if [ -z "$worker_group" ] && command -v id >/dev/null 2>&1; then
    worker_group=$(id -gn "$worker_user" 2>/dev/null || true)
fi

nginx_mode=""
if [ -n "$nginx_bin" ] && [ -x "$nginx_bin" ]; then
    nginx_mode=host
elif [ "${AJS_CHECK_NGINX_DOCKER:-0}" = "1" ] && command -v docker >/dev/null 2>&1; then
    if [ "$mode" = production ]; then
        fail "production gate requires host nginx; Docker nginx staging is development-only"
    else
        nginx_mode=docker
    fi
else
    skip "nginx not installed; production gate requires a real nginx -t (install nginx or set AJS_NGINX_BIN)"
fi

if [ -n "$nginx_mode" ]; then
    if ! command -v openssl >/dev/null 2>&1; then
        skip "openssl not found; cannot generate the throwaway nginx test certificate"
    else
        nginx_tmp=$(mktemp -d) || { fail "cannot create temporary nginx directory"; nginx_tmp=""; }
        if [ -n "$nginx_tmp" ]; then
            mkdir -p "$nginx_tmp/sites-enabled" "$nginx_tmp/snippets" "$nginx_tmp/logs" \
                "$nginx_tmp/certs/panel.example.com" "$nginx_tmp/certs/admin.example.com" \
                "$nginx_tmp/temp/client_body" "$nginx_tmp/temp/proxy" \
                "$nginx_tmp/temp/fastcgi" "$nginx_tmp/temp/uwsgi" "$nginx_tmp/temp/scgi"
            cp "$kit/nginx/nginx.conf" "$nginx_tmp/nginx.conf"
            cp "$kit/nginx/ajs-mime.types" "$nginx_tmp/ajs-mime.types"
            cp "$kit/nginx/snippets/"*.conf "$nginx_tmp/snippets/" 2>/dev/null || true
            cp "$kit/nginx/sites-available/ajs-portal.conf" "$nginx_tmp/sites-enabled/"
            cp "$kit/nginx/sites-available/ajs-admin.conf" "$nginx_tmp/sites-enabled/"
            cp "$kit/nginx/sites-available/ajs-default.conf" "$nginx_tmp/sites-enabled/"

            # nginx resolves auth_basic_user_file at config load. Stage a
            # throwaway htpasswd beside the rewritten /etc/nginx root; the
            # committed kit never contains a real hash or password.
            printf 'check-operator:%s\n' "$(openssl passwd -apr1 'check-only-password')" > "$nginx_tmp/ajs-admin.htpasswd"
            chmod 600 "$nginx_tmp/ajs-admin.htpasswd"

            # Throwaway token: 64 'a' characters, never a committed literal.
            dummy_token=$(printf 'a%.0s' {1..64})
            # Per-operator map: only authenticated check-operator receives a
            # credential; there is no shared/default Authorization value.
            printf '"check-operator" "Bearer %s";\n' "$dummy_token" > "$nginx_tmp/snippets/ajs-admin-tokens.map"
            chmod 600 "$nginx_tmp/snippets/ajs-admin-tokens.map"

            openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
                -subj "/CN=panel.example.com" \
                -keyout "$nginx_tmp/certs/panel.example.com/privkey.pem" \
                -out "$nginx_tmp/certs/panel.example.com/fullchain.pem" >/dev/null 2>&1
            openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
                -subj "/CN=admin.example.com" \
                -keyout "$nginx_tmp/certs/admin.example.com/privkey.pem" \
                -out "$nginx_tmp/certs/admin.example.com/fullchain.pem" >/dev/null 2>&1

            # Config-visible root differs in Docker mode because the staged
            # tree is bind-mounted at /work.
            if [ "$nginx_mode" = docker ]; then nginx_root=/work; else nginx_root=$nginx_tmp; fi

            # Rewrite the host paths to the throwaway tree.
            for f in "$nginx_tmp/nginx.conf" "$nginx_tmp/sites-enabled/"*.conf; do
                sed -i.bak "s|/etc/nginx|$nginx_root|g; s|/etc/ajs/tls|$nginx_root/certs|g; s|/var/log/nginx|$nginx_root/logs|g" "$f"
                rm -f "$f.bak"
            done
            if [ "$nginx_mode" = docker ]; then
                sed -i.bak 's/^user .*/user nginx;/' "$nginx_tmp/nginx.conf"
                rm -f "$nginx_tmp/nginx.conf.bak"
            elif [ "$(id -u)" -ne 0 ]; then
                # The stock config names www-data, which may not exist off
                # Debian/Ubuntu. Run the staged test as the invoking user so
                # nginx can chown its temp directories in both a direct run
                # and the unshare fallback.
                sed -i.bak -E "s|^[[:space:]]*user[[:space:]]+[^;]+;|user $(id -un);|" "$nginx_tmp/nginx.conf"
                rm -f "$nginx_tmp/nginx.conf.bak"
            fi

            # nginx -t still opens the pid file and every compiled-in temp
            # path. On a stock Debian/Ubuntu nginx those are /run/nginx.pid
            # and /var/lib/nginx/*, which a non-root gate cannot create. Pin
            # the pid and all temp paths inside the throwaway tree: this is
            # the staging fix, not a relaxation of nginx -t.
            sed -i.bak -E "s|^[[:space:]]*pid[[:space:]]+[^;]+;|pid $nginx_root/nginx.pid;|" "$nginx_tmp/nginx.conf"
            rm -f "$nginx_tmp/nginx.conf.bak"
            awk -v root="$nginx_root" '
                /^[[:space:]]*http[[:space:]]*\{/ {
                    print
                    print "    client_body_temp_path " root "/temp/client_body;"
                    print "    proxy_temp_path " root "/temp/proxy;"
                    print "    fastcgi_temp_path " root "/temp/fastcgi;"
                    print "    uwsgi_temp_path " root "/temp/uwsgi;"
                    print "    scgi_temp_path " root "/temp/scgi;"
                    next
                }
                { print }
            ' "$nginx_tmp/nginx.conf" > "$nginx_tmp/nginx.conf.new" \
                && mv "$nginx_tmp/nginx.conf.new" "$nginx_tmp/nginx.conf"

            nginx_output=""
            nginx_rc=1
            nginx_via="host nginx"
            if [ "$nginx_mode" = host ]; then
                nginx_output=$("$nginx_bin" -t -p "$nginx_tmp/" -c "$nginx_tmp/nginx.conf" 2>&1)
                nginx_rc=$?
                # nginx -t opens the listening sockets even in test mode. A
                # non-root user cannot bind :80/:443 on a normal host, so retry
                # in an isolated user+network namespace where the test process
                # is root with CAP_NET_BIND_SERVICE. This is still the real
                # nginx binary parsing the same staged configuration.
                if [ "$nginx_rc" -ne 0 ] && [ "$(id -u)" -ne 0 ] \
                    && command -v unshare >/dev/null 2>&1; then
                    # Inside `unshare -Ur` only uid 0 is mapped, so the test
                    # config must run as root there; the direct attempt above
                    # still uses the invoking user.
                    cp "$nginx_tmp/nginx.conf" "$nginx_tmp/nginx.unshare.conf"
                    sed -i.bak -E 's|^[[:space:]]*user[[:space:]]+[^;]+;|user root;|' "$nginx_tmp/nginx.unshare.conf"
                    rm -f "$nginx_tmp/nginx.unshare.conf.bak"
                    if command -v ip >/dev/null 2>&1; then
                        nginx_output=$(unshare -Urn sh -c \
                            "ip link set lo up 2>/dev/null || true; '$nginx_bin' -t -p '$nginx_tmp/' -c '$nginx_tmp/nginx.unshare.conf'" 2>&1)
                    else
                        nginx_output=$(unshare -Urn "$nginx_bin" -t -p "$nginx_tmp/" -c "$nginx_tmp/nginx.unshare.conf" 2>&1)
                    fi
                    nginx_rc=$?
                    if [ "$nginx_rc" -eq 0 ]; then nginx_via="unshare -Urn nginx"; fi
                fi
                if [ "$nginx_rc" -ne 0 ] && [ "$(id -u)" -ne 0 ] \
                    && command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
                    nginx_output=$(sudo -n "$nginx_bin" -t -p "$nginx_tmp/" -c "$nginx_tmp/nginx.conf" 2>&1)
                    nginx_rc=$?
                    if [ "$nginx_rc" -eq 0 ]; then nginx_via="sudo nginx"; fi
                fi
            else
                nginx_output=$(docker run --rm -v "$nginx_tmp:/work" -w /work nginx:1.29-alpine \
                    nginx -t -p /work/ -c /work/nginx.conf 2>&1)
                nginx_rc=$?
                nginx_via="docker nginx:1.29-alpine"
            fi
            if [ "$nginx_rc" -eq 0 ]; then
                if [ "$nginx_mode" = host ]; then
                    nginx_label=$("$nginx_bin" -v 2>&1 | head -1)
                else
                    nginx_label="nginx:1.29-alpine"
                fi
                pass "nginx -t: portal and admin vhosts ($nginx_label; $nginx_via)"
            else
                fail "nginx -t failed: $(printf '%s' "$nginx_output" | tr '\n' ' ' | cut -c1-500)"
            fi

            # Runtime host-boundary probe. nginx -t only parses; it cannot
            # prove that an unknown Host is refused or that the HTTP redirect
            # uses the fixed canonical name. Start the staged configuration
            # and probe it on the real :80/:443 listeners.
            if [ "$nginx_rc" -eq 0 ] && [ "$nginx_mode" = host ]; then
                panel_host=$(awk '$1 == "server_name" {gsub(/;/, "", $2); print $2; exit}' "$kit/nginx/sites-available/ajs-portal.conf")
                [ -n "$panel_host" ] || panel_host=panel.example.com
                cat > "$nginx_tmp/probe.sh" <<'AJS_NGINX_PROBE'
#!/bin/sh
set -u
root=$1
config=$2
nginx_bin=$3
ip_bin=${4:-}
panel_host=${5:-panel.example.com}
if [ -n "$ip_bin" ]; then "$ip_bin" link set lo up 2>/dev/null || true; fi
"$nginx_bin" -p "$root/" -c "$config" >/dev/null 2>&1 || { echo "nginx_start_failed"; exit 1; }
cleanup() { "$nginx_bin" -p "$root/" -c "$config" -s stop >/dev/null 2>&1 || true; }
trap cleanup EXIT HUP INT TERM
i=0
while [ "$i" -lt 100 ]; do
    if curl -s -o /dev/null --max-time 1 -H "Host: $panel_host" http://127.0.0.1/healthz 2>/dev/null; then
        break
    fi
    i=$((i + 1))
    sleep 0.1
done
http_unknown=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 -H 'Host: attacker.invalid' http://127.0.0.1/ 2>/dev/null || true)
https_unknown=$(curl -sS -k -o /dev/null -w '%{http_code}' --max-time 3 --resolve attacker.invalid:443:127.0.0.1 https://attacker.invalid/ 2>/dev/null || true)
https_ip=$(curl -sS -k -o /dev/null -w '%{http_code}' --max-time 3 https://127.0.0.1/ 2>/dev/null || true)
canonical=$(curl -sS -o /dev/null -D - --max-time 3 -H "Host: $panel_host" http://127.0.0.1/some/path 2>/dev/null | awk 'BEGIN{IGNORECASE=1} /^HTTP\//{code=$2} /^Location:/{sub(/\r$/,""); loc=$2} END{print code "|" loc}')
portal_health=$(curl -sS -k -o /dev/null -w '%{http_code}' --max-time 3 --resolve "$panel_host:443:127.0.0.1" "https://$panel_host/healthz" 2>/dev/null || true)
printf 'http_unknown=%s\n' "$http_unknown"
printf 'https_unknown=%s\n' "$https_unknown"
printf 'https_ip=%s\n' "$https_ip"
printf 'canonical=%s\n' "$canonical"
printf 'portal_health=%s\n' "$portal_health"
AJS_NGINX_PROBE
                probe_config="$nginx_tmp/nginx.conf"
                probe_out=""
                probe_rc=1
                probe_ran=0
                if [ "$(id -u)" -eq 0 ] && command -v unshare >/dev/null 2>&1 && command -v ip >/dev/null 2>&1; then
                    # A network namespace keeps the probe off the host's real
                    # :80/:443 listeners, which may already belong to a running
                    # nginx. As real root, nginx can still setuid to the
                    # configured worker user, so keep the config unchanged.
                    probe_out=$(unshare -n sh "$nginx_tmp/probe.sh" "$nginx_root" "$nginx_tmp/nginx.conf" "$nginx_bin" "$(command -v ip)" "$panel_host" 2>&1)
                    probe_rc=$?
                    probe_ran=1
                    if [ "$probe_rc" -ne 0 ]; then
                        # Namespace unavailable/blocked: fall back to the real
                        # listeners, which is valid only when they are free.
                        probe_out=$(sh "$nginx_tmp/probe.sh" "$nginx_root" "$nginx_tmp/nginx.conf" "$nginx_bin" "$(command -v ip 2>/dev/null || true)" "$panel_host" 2>&1)
                        probe_rc=$?
                    fi
                elif command -v unshare >/dev/null 2>&1 && command -v ip >/dev/null 2>&1; then
                    # Inside the user+network namespace uid 0 is the only
                    # mapped id, so nginx must run as root there.
                    cp "$nginx_tmp/nginx.conf" "$nginx_tmp/nginx.probe.conf"
                    sed -i.bak -E 's|^[[:space:]]*user[[:space:]]+[^;]+;|user root;|' "$nginx_tmp/nginx.probe.conf"
                    rm -f "$nginx_tmp/nginx.probe.conf.bak"
                    probe_config="$nginx_tmp/nginx.probe.conf"
                    probe_out=$(unshare -Urn sh "$nginx_tmp/probe.sh" "$nginx_root" "$probe_config" "$nginx_bin" "$(command -v ip)" "$panel_host" 2>&1)
                    probe_rc=$?
                    probe_ran=1
                elif [ "$(id -u)" -eq 0 ]; then
                    probe_out=$(sh "$nginx_tmp/probe.sh" "$nginx_root" "$probe_config" "$nginx_bin" "$(command -v ip 2>/dev/null || true)" "$panel_host" 2>&1)
                    probe_rc=$?
                    probe_ran=1
                else
                    skip "nginx runtime host-boundary probe needs root or unshare+ip"
                fi
                if [ "$probe_ran" -eq 1 ] && [ -n "$probe_out" ]; then
                    http_unknown=$(printf '%s\n' "$probe_out" | sed -n 's/^http_unknown=//p')
                    https_unknown=$(printf '%s\n' "$probe_out" | sed -n 's/^https_unknown=//p')
                    https_ip=$(printf '%s\n' "$probe_out" | sed -n 's/^https_ip=//p')
                    canonical=$(printf '%s\n' "$probe_out" | sed -n 's/^canonical=//p')
                    portal_health=$(printf '%s\n' "$probe_out" | sed -n 's/^portal_health=//p')
                    if [ "$http_unknown" = "444" ] || [ "$http_unknown" = "000" ]; then
                        pass "unknown Host on :80 is refused (444/connection closed)"
                    else
                        fail "unknown Host on :80 was not refused: status=${http_unknown:-<none>}"
                    fi
                    if [ "$https_unknown" = "421" ]; then
                        pass "unknown Host on :443 is refused (421 Misdirected Request)"
                    else
                        fail "unknown Host on :443 was not refused: status=${https_unknown:-<none>}"
                    fi
                    if [ "$https_ip" = "421" ]; then
                        pass "IP-literal Host on :443 is refused (421)"
                    else
                        fail "IP-literal Host on :443 was not refused: status=${https_ip:-<none>}"
                    fi
                    if [ "$canonical" = "301|https://$panel_host/some/path" ]; then
                        pass "HTTP redirect uses the fixed canonical portal origin"
                    else
                        fail "HTTP canonical redirect wrong: ${canonical:-<none>}"
                    fi
                    if [ "$portal_health" = "200" ]; then
                        pass "canonical portal Host still reaches /healthz (200)"
                    else
                        fail "canonical portal /healthz status=${portal_health:-<none>}"
                    fi
                elif [ "$probe_ran" -eq 1 ] && [ "$probe_rc" -ne 0 ]; then
                    fail "nginx runtime probe failed: $(printf '%s' "$probe_out" | tr '\n' ' ' | cut -c1-300)"
                fi
            fi
            rm -rf "$nginx_tmp"
            nginx_tmp=""
        fi
    fi
fi

# Static checks that do not need nginx installed.
if grep -q 'return 404;' "$kit/nginx/sites-available/ajs-portal.conf"; then
    pass "portal vhost has a terminal 404 (no admin routes)"
else
    fail "portal vhost has no terminal 404"
fi
if grep -qE '^[[:space:]]*location[[:space:]]+/v1/[[:space:]]*\{' "$kit/nginx/sites-available/ajs-portal.conf"; then
    fail "portal vhost proxies the generic admin /v1/ namespace"
else
    pass "portal vhost proxies only /v1/portal/ and payment callbacks"
fi
if grep -q 'ajs-admin-operator-token.conf' "$kit/nginx/sites-available/ajs-admin.conf" && \
   grep -q 'ajs-admin-tokens.map' "$kit/nginx/nginx.conf" && \
   grep -qE 'proxy_set_header[[:space:]]+Authorization[[:space:]]+\$ajs_operator_authorization;' "$kit/nginx/snippets/ajs-admin-operator-token.conf"; then
    pass "admin vhost injects the authenticated operator's mapped bearer"
else
    fail "admin vhost is missing per-operator bearer injection"
fi
if grep -qE '^[[:space:]]*auth_basic[[:space:]]+"AJS admin";' "$kit/nginx/sites-available/ajs-admin.conf" && \
   grep -qE '^[[:space:]]*auth_basic_user_file[[:space:]]+/etc/nginx/ajs-admin.htpasswd;' "$kit/nginx/sites-available/ajs-admin.conf"; then
    pass "admin vhost requires per-operator HTTP Basic auth"
else
    fail "admin vhost is missing auth_basic / auth_basic_user_file"
fi
if grep -q 'ajs-admin-operator.conf' "$kit/nginx/sites-available/ajs-admin.conf" && \
   grep -qE 'proxy_set_header[[:space:]]+X-AJS-Operator[[:space:]]+\$remote_user;' "$kit/nginx/snippets/ajs-admin-operator.conf"; then
    pass "admin vhost forwards X-AJS-Operator from \$remote_user"
else
    fail "admin vhost does not forward X-AJS-Operator from \$remote_user"
fi
if grep -q '0640' "$kit/render-admin-htpasswd.sh" && \
   grep -qE 'chown[[:space:]]+root:' "$kit/render-admin-htpasswd.sh" && \
   grep -q 'AJS_HTPASSWD_GROUP' "$kit/render-admin-htpasswd.sh"; then
    pass "htpasswd renderer targets root:<nginx-group> mode 0640 with a group override"
else
    fail "htpasswd renderer is missing the root:<nginx-group> 0640 or group override"
fi
if grep -qE 'world-readable|world readable|0007' "$kit/render-admin-htpasswd.sh" && \
   grep -q 'replace-me' "$kit/render-admin-htpasswd.sh"; then
    pass "htpasswd renderer refuses world-readable output and placeholders"
else
    fail "htpasswd renderer is missing the world-readable or placeholder guard"
fi
if grep -qE 'allow[[:space:]]+127\.0\.0\.1;' "$kit/nginx/sites-available/ajs-admin.conf" && \
   grep -qE 'deny[[:space:]]+all;' "$kit/nginx/sites-available/ajs-admin.conf"; then
    pass "admin vhost has a loopback/VPN allow policy ending in deny all"
else
    fail "admin vhost is missing the allow/deny policy"
fi
if grep -qE 'listen[[:space:]]+80[[:space:]]+default_server;' "$kit/nginx/sites-available/ajs-default.conf" && \
   grep -q 'return 444;' "$kit/nginx/sites-available/ajs-default.conf"; then
    pass "default HTTP server refuses unknown Host with 444"
else
    fail "default HTTP server is missing (listen 80 default_server + return 444)"
fi
if grep -qE 'listen[[:space:]]+443[[:space:]]+ssl[[:space:]]+default_server;' "$kit/nginx/sites-available/ajs-default.conf" && \
   grep -q 'return 421;' "$kit/nginx/sites-available/ajs-default.conf"; then
    pass "default HTTPS server refuses unknown Host with 421"
else
    fail "default HTTPS server is missing (listen 443 ssl default_server + return 421)"
fi
if grep -qE '^[[:space:]]*return[[:space:]]+301[[:space:]]+https://[^$[:space:]]+\$request_uri;' "$kit/nginx/sites-available/ajs-portal.conf" && \
   ! grep -q 'return 301 https://\$host' "$kit/nginx/sites-available/ajs-portal.conf"; then
    pass "portal HTTP redirect uses the fixed canonical origin"
else
    fail "portal HTTP redirect still reflects \$host or lacks the canonical origin"
fi
if grep -qE '^[[:space:]]*location[[:space:]]+=[[:space:]]+/readyz[[:space:]]*\{' "$kit/nginx/sites-available/ajs-portal.conf" && \
   grep -q 'proxy_pass http://ajs_accountd;' "$kit/nginx/sites-available/ajs-portal.conf"; then
    pass "portal edge proxies /readyz to the daemon"
else
    fail "portal edge does not proxy /readyz"
fi
if grep -qE '^[[:space:]]*location[[:space:]]+=[[:space:]]+/healthz[[:space:]]*\{' "$kit/nginx/sites-available/ajs-portal.conf" && \
   grep -q 'return 200 "ok\\n";' "$kit/nginx/sites-available/ajs-portal.conf"; then
    pass "portal /healthz remains a static edge liveness response"
else
    fail "portal /healthz is no longer a static edge liveness response"
fi
if grep -q 'Strict-Transport-Security' "$kit/nginx/snippets/ajs-security-headers.conf"; then
    pass "HSTS header present"
else
    fail "HSTS header missing"
fi

# --- Content-Security-Policy (F50) -----------------------------------------
csp_snippet="$kit/nginx/snippets/ajs-security-headers.conf"
csp_vhosts="$kit/nginx/sites-available/ajs-portal.conf $kit/nginx/sites-available/ajs-admin.conf $kit/nginx/sites-available/ajs-default.conf"

# The snippet is the only place a CSP header may exist: two CSP headers
# intersect, so a stale duplicate silently breaks the bundle. The scan splits
# every line on ';' and compares the header name case-insensitively, so a
# second directive on the same line or a case variant cannot hide.
csp_hits=$(
    grep -rliE 'add_header' --include='*.conf' --include='*.template' \
        --exclude-dir=dist --exclude-dir=dist-test "$kit" 2>/dev/null \
    | while IFS= read -r csp_file; do
        awk '
        {
            line = $0
            sub(/#.*/, "", line)
            n = split(line, parts, ";")
            for (i = 1; i <= n; i++) {
                stmt = parts[i]
                sub(/^[ \t]+/, "", stmt)
                sub(/[ \t]+$/, "", stmt)
                if (stmt ~ /^[Aa][Dd][Dd]_[Hh][Ee][Aa][Dd][Ee][Rr][ \t]/) {
                    sub(/^[^ \t]+[ \t]+/, "", stmt)
                    name = stmt
                    sub(/[ \t].*/, "", name)
                    gsub(/"/, "", name)
                    if (tolower(name) == "content-security-policy") {
                        printf "%s:%d %s\n", FILENAME, FNR, name
                    }
                }
            }
        }' "$csp_file"
    done
)
csp_header_count=$(printf '%s\n' "$csp_hits" | grep -c . 2>/dev/null || true)
if [ "${csp_header_count:-0}" = "1" ] \
    && printf '%s\n' "$csp_hits" | grep -Fq "$csp_snippet:"; then
    pass "exactly one CSP header directive in the kit (no second, conflicting policy)"
else
    fail "expected exactly one CSP header directive in the kit, found ${csp_header_count:-0}: $(printf '%s' "$csp_hits" | tr '\n' ';')"
fi

# The coverage model below assumes no other included snippet adds headers: a
# snippet that did would be inherited into blocks the walk treats as bare.
csp_other_snippet_add=$(grep -lE '^[[:space:]]*add_header[[:space:]]' "$kit/nginx/snippets/"*.conf 2>/dev/null \
    | grep -vF "$csp_snippet" || true)
if [ -z "$csp_other_snippet_add" ]; then
    pass "only the security-headers snippet declares add_header (inheritance model holds)"
else
    fail "another snippet declares add_header; the coverage walk does not resolve includes: $csp_other_snippet_add"
fi

csp_snippet_policy=$(sed -n 's/^[[:space:]]*add_header[[:space:]]*Content-Security-Policy[[:space:]]*"\(.*\)"[[:space:]]*always;[[:space:]]*$/\1/p' "$csp_snippet")
if [ "$csp_snippet_policy" = "$csp_policy" ]; then
    pass "snippet carries the exact CSP policy with always"
else
    fail "snippet CSP differs from the acceptance policy: ${csp_snippet_policy:-<missing>}"
fi

csp_directive() {
    printf '%s' "$1" | tr ';' '\n' | sed -n "s/^[[:space:]]*$2[[:space:]]*//p" | sed 's/[[:space:]]*$//'
}
csp_script_src=$(csp_directive "$csp_snippet_policy" script-src)
if [ "$csp_script_src" = "'self'" ]; then
    pass "CSP script-src is 'self' only (no unsafe-inline, no unsafe-eval)"
else
    fail "CSP script-src is not 'self' only: ${csp_script_src:-<missing>}"
fi
csp_style_src=$(csp_directive "$csp_snippet_policy" style-src)
if [ "$csp_style_src" = "'self' 'unsafe-inline'" ]; then
    pass "CSP style-src is 'self' 'unsafe-inline' (the documented inline-style exception)"
else
    fail "CSP style-src is not 'self' 'unsafe-inline': ${csp_style_src:-<missing>}"
fi
case $csp_snippet_policy in
    *unsafe-eval*) fail "CSP allows unsafe-eval" ;;
    *) pass "CSP does not allow unsafe-eval" ;;
esac
case $csp_snippet_policy in
    *http://*|*https://*|*'*'*) fail "CSP names a remote or wildcard origin" ;;
    *) pass "CSP names no remote or wildcard origin" ;;
esac

# Structural coverage: nginx drops the inherited add_header set as soon as a
# location declares one of its own, so every location must either include the
# snippet itself or inherit a server-level include, and no block may include it
# twice. This walks all three shipped vhosts and reports every location or
# server that would answer without the policy (or with it twice).
csp_coverage=$(awk '
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
function report(kind, line, name, msg) {
    printf "%s %s:%d %s%s\n", kind, FILENAME, line, name, msg
}
{
    line = $0
    sub(/#.*/, "", line)
    if (line ~ /^[ \t]*$/) next
    opens = gsub(/\{/, "{", line)
    closes = gsub(/\}/, "}", line)

    if (!in_server) {
        if (line ~ /^[ \t]*server[ \t]*\{/) {
            in_server = 1; server_line = FNR; server_inc = 0; in_loc = 0
        }
        next
    }

    if (!in_loc && line ~ /^[ \t]*location[ \t]/) {
        name = line
        sub(/[ \t]*\{.*/, "", name)
        name = trim(name)
        if (closes > 0) {
            inc = (line ~ /ajs-security-headers\.conf/) ? 1 : 0
            add = (line ~ /add_header[ \t]/) ? 1 : 0
            if (!inc && !server_inc) report("BAD", FNR, name, " [no security-header include and none inherited]")
            else if (add && !inc) report("BAD", FNR, name, " [declares add_header, dropping the inherited headers, without the include]")
            else report("OK", FNR, name, "")
            next
        }
        in_loc = 1; loc_line = FNR; loc_inc = 0; loc_add = 0; loc_name = name
        next
    }

    if (in_loc) {
        if (line ~ /include[ \t]+[^;]*ajs-security-headers\.conf/) loc_inc++
        if (line ~ /add_header[ \t]/) loc_add = 1
        if (closes > 0 && opens == 0) {
            if (loc_inc > 1) report("BAD", loc_line, loc_name, " [includes the security-header snippet more than once]")
            else if (!loc_inc && !server_inc) report("BAD", loc_line, loc_name, " [no security-header include and none inherited]")
            else if (loc_add && !loc_inc) report("BAD", loc_line, loc_name, " [declares add_header, dropping the inherited headers, without the include]")
            else report("OK", loc_line, loc_name, "")
            in_loc = 0
        }
        next
    }

    if (line ~ /include[ \t]+[^;]*ajs-security-headers\.conf/) server_inc++
    if (closes > 0 && opens == 0) {
        if (server_inc > 1) report("BAD", server_line, "server", " [includes the security-header snippet more than once]")
        else if (!server_inc) report("BAD", server_line, "server", " [server block has no ajs-security-headers.conf include]")
        in_server = 0
    }
}' $csp_vhosts 2>/dev/null)

csp_bad=$(printf '%s\n' "$csp_coverage" | grep -c '^BAD' 2>/dev/null || true)
csp_ok=$(printf '%s\n' "$csp_coverage" | grep -c '^OK' 2>/dev/null || true)
if [ "${csp_bad:-0}" -eq 0 ] && [ "${csp_ok:-0}" -gt 0 ]; then
    pass "CSP reaches every server and location in all three vhosts ($csp_ok locations: local include or inherited)"
else
    fail "CSP coverage gaps: $(printf '%s\n' "$csp_coverage" | grep '^BAD' | tr '\n' ';' | cut -c1-500)"
fi

csp_named_bad=""
while IFS='|' read -r csp_file csp_name; do
    [ -n "$csp_file" ] || continue
    if ! printf '%s\n' "$csp_coverage" | grep -Eq "^OK ${csp_file}:[0-9]+ ${csp_name}\$"; then
        csp_named_bad="$csp_named_bad ${csp_name}"
    fi
done <<EOF
$kit/nginx/sites-available/ajs-portal.conf|location = /portal
$kit/nginx/sites-available/ajs-portal.conf|location /portal/
$kit/nginx/sites-available/ajs-portal.conf|location /assets/
$kit/nginx/sites-available/ajs-admin.conf|location /
$kit/nginx/sites-available/ajs-admin.conf|location /assets/
$kit/nginx/sites-available/ajs-admin.conf|location /v1/
EOF
if [ -z "$csp_named_bad" ]; then
    pass "CSP on the named response paths: portal HTML/assets and admin HTML/API"
else
    fail "CSP missing from named response paths:$csp_named_bad"
fi

# ajs-default.conf is the Host-rejection boundary, not a content origin: it
# must stay content-free (no location block, document root, alias, rewrite or
# upstream) and answer only 444/421, so the CSP invariant covers every serving
# path and no unprobed location can serve a body.
csp_default_file="$kit/nginx/sites-available/ajs-default.conf"
csp_default_content=$(grep -nE '^[[:space:]]*(location|root|alias|index|try_files|rewrite|proxy_pass|fastcgi_pass|uwsgi_pass|scgi_pass|grpc_pass|error_page)[[:space:]]' "$csp_default_file" 2>/dev/null || true)
csp_default_codes=$(grep -oE '^[[:space:]]*return[[:space:]]+[0-9]{3}' "$csp_default_file" 2>/dev/null \
    | awk '{print $2}' | sort -u | tr '\n' ' ')
csp_default_bad_codes=""
for csp_code in $csp_default_codes; do
    case $csp_code in
        421|444) ;;
        *) csp_default_bad_codes="$csp_default_bad_codes $csp_code" ;;
    esac
done
csp_default_returns=$(grep -cE '^[[:space:]]*return[[:space:]]' "$csp_default_file" 2>/dev/null || true)
if [ -z "$csp_default_content" ] && [ -z "$csp_default_bad_codes" ] \
    && [ "${csp_default_returns:-0}" -ge 2 ]; then
    pass "ajs-default.conf stays a content-free default-deny listener (444/421 only)"
else
    fail "ajs-default.conf may serve content or a non-444/421 status:${csp_default_content:+ content:$(printf '%s' "$csp_default_content" | tr '\n' ';')}${csp_default_bad_codes:+ codes:$csp_default_bad_codes}"
fi
if grep -q 'limit_req_zone' "$kit/nginx/nginx.conf" && grep -q 'limit_req ' "$kit/nginx/sites-available/ajs-portal.conf"; then
    pass "rate-limit zones and portal limit_req present"
else
    fail "rate-limit zones/limit_req missing"
fi
if grep -q 'X-Forwarded-For[[:space:]]*\$remote_addr' "$kit/nginx/snippets/ajs-proxy-common.conf"; then
    pass "X-Forwarded-For is overwritten with the socket peer"
else
    fail "X-Forwarded-For is not overwritten with \$remote_addr"
fi

# ---------------------------------------------------------------------------
section "admin operator auth (runtime)"
# Prove the admin edge itself enforces per-operator HTTP Basic auth and forwards
# the authenticated username. The upstream is a tiny Python server; an
# unauthenticated request must leave its log empty, which is the "never reaches
# the daemon" assertion.
if [ -z "$nginx_bin" ] || [ ! -x "$nginx_bin" ]; then
    skip "admin operator auth runtime test needs host nginx"
elif ! command -v curl >/dev/null 2>&1; then
    skip "admin operator auth runtime test needs curl"
elif ! command -v python3 >/dev/null 2>&1; then
    skip "admin operator auth runtime test needs python3"
else
    auth_tmp=$(mktemp -d) || { fail "cannot create admin auth test directory"; auth_tmp=""; }
    if [ -n "$auth_tmp" ]; then
        auth_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
        upstream_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
        cat > "$auth_tmp/upstream.py" <<'PY'
import http.server
import sys

log = sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log, "a", encoding="utf-8") as fh:
            fh.write(self.headers.get("X-AJS-Operator", "") + "\n")
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *args):
        pass

http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
        python3 "$auth_tmp/upstream.py" "$upstream_port" "$auth_tmp/seen" &
        upstream_pid=$!
        upstream_ready=0
        for _ in $(seq 1 50); do
            if curl -s -o /dev/null "http://127.0.0.1:$upstream_port/"; then
                upstream_ready=1
                break
            fi
            sleep 0.1
        done
        : > "$auth_tmp/seen"
        printf 'alice:%s\nbob:%s\n' \
            "$(openssl passwd -apr1 'alice-runtime-password')" \
            "$(openssl passwd -apr1 'bob-runtime-password')" > "$auth_tmp/htpasswd"
        auth_user_directive=""
        if [ "$(id -u)" -eq 0 ]; then
            # Run the probe with the real production worker identity. The
            # htpasswd must be root:<worker-group> 0640 or the worker gets
            # EACCES and every valid login returns 500 (AJS-408 condition 1).
            chown root:"${worker_group:-$(id -gn)}" "$auth_tmp/htpasswd"
            chmod 0640 "$auth_tmp/htpasswd"
            chmod 0711 "$auth_tmp"
            auth_user_directive="user $worker_user;"
        else
            chmod 0600 "$auth_tmp/htpasswd"
        fi
        cat > "$auth_tmp/nginx.conf" <<EOF
daemon off;
$auth_user_directive
pid $auth_tmp/nginx.pid;
error_log $auth_tmp/error.log;
events { worker_connections 32; }
http {
    access_log off;
    client_body_temp_path $auth_tmp/client_body;
    proxy_temp_path $auth_tmp/proxy;
    fastcgi_temp_path $auth_tmp/fastcgi;
    uwsgi_temp_path $auth_tmp/uwsgi;
    scgi_temp_path $auth_tmp/scgi;
    server {
        listen 127.0.0.1:$auth_port;
        auth_basic "AJS admin";
        auth_basic_user_file $auth_tmp/htpasswd;
        location / {
            proxy_pass http://127.0.0.1:$upstream_port;
            proxy_set_header X-AJS-Operator \$remote_user;
        }
    }
}
EOF
        "$nginx_bin" -p "$auth_tmp/" -c "$auth_tmp/nginx.conf" &
        nginx_pid=$!
        ready=0
        for _ in $(seq 1 50); do
            if curl -s -o /dev/null "http://127.0.0.1:$auth_port/"; then
                ready=1
                break
            fi
            sleep 0.1
        done
        if [ "$upstream_ready" -ne 1 ]; then
            fail "admin operator auth runtime test: upstream did not start"
        elif [ "$ready" -ne 1 ]; then
            fail "admin operator auth runtime test: nginx did not start"
        else
            unauth=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$auth_port/")
            if [ "$unauth" = "401" ] && [ ! -s "$auth_tmp/seen" ]; then
                pass "unauthenticated admin request is refused before the upstream"
            else
                fail "unauthenticated admin request status=$unauth upstream=$(wc -c < "$auth_tmp/seen" 2>/dev/null || echo missing)"
            fi
            alice_status=$(curl -s -o /dev/null -w '%{http_code}' -u 'alice:alice-runtime-password' "http://127.0.0.1:$auth_port/")
            bob_status=$(curl -s -o /dev/null -w '%{http_code}' -u 'bob:bob-runtime-password' "http://127.0.0.1:$auth_port/")
            if [ "$alice_status" = "200" ] && [ "$bob_status" = "200" ] && \
               grep -qx 'alice' "$auth_tmp/seen" && grep -qx 'bob' "$auth_tmp/seen"; then
                pass "two distinct Basic operators reach the upstream as distinct X-AJS-Operator values"
            else
                fail "operator forwarding failed (alice=$alice_status bob=$bob_status seen=$(tr '\n' ',' < "$auth_tmp/seen" 2>/dev/null))"
            fi
        fi
        kill "$nginx_pid" "$upstream_pid" 2>/dev/null || true
        wait "$nginx_pid" "$upstream_pid" 2>/dev/null || true
        rm -rf "$auth_tmp"
        auth_tmp=""
    fi
fi

# ---------------------------------------------------------------------------
section "htpasswd worker readability (runtime)"
# nginx opens auth_basic_user_file in its worker process, not in the root
# master. A root:root 0600 file therefore produces HTTP 500 with
# "open() ... failed (13: Permission denied)" after a correct password even
# though nginx -t passes. Render a disposable htpasswd through the real
# renderer and prove the actual worker identity from nginx.conf can read it.
htpasswd_tmp=""
if [ -z "$nginx_bin" ] || [ ! -x "$nginx_bin" ]; then
    skip "htpasswd worker-read test needs host nginx"
else
    if ! id -u "$worker_user" >/dev/null 2>&1; then
        skip "htpasswd worker-read test: nginx worker user '$worker_user' does not exist"
    elif [ -z "$worker_group" ]; then
        skip "htpasswd worker-read test: cannot resolve the group for nginx worker '$worker_user'"
    elif [ "$(id -u)" -ne 0 ] && ! (command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null); then
        skip "htpasswd worker-read test needs root or passwordless sudo"
    else
        htpasswd_tmp=$(mktemp -d) || { fail "cannot create htpasswd worker-read directory"; htpasswd_tmp=""; }
        if [ -n "$htpasswd_tmp" ]; then
            chmod 0711 "$htpasswd_tmp"
            printf 'worker-check:%s\n' 'worker-check-password-409' > "$htpasswd_tmp/inventory"
            chmod 0600 "$htpasswd_tmp/inventory"

            render_rc=1
            if [ "$(id -u)" -eq 0 ]; then
                AJS_NGINX_CONF="$worker_conf" sh "$kit/render-admin-htpasswd.sh" \
                    "$htpasswd_tmp/inventory" "$htpasswd_tmp/htpasswd" \
                    >"$htpasswd_tmp/render.out" 2>&1
                render_rc=$?
            elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
                sudo -n env AJS_NGINX_CONF="$worker_conf" sh "$kit/render-admin-htpasswd.sh" \
                    "$htpasswd_tmp/inventory" "$htpasswd_tmp/htpasswd" \
                    >"$htpasswd_tmp/render.out" 2>&1
                render_rc=$?
            fi

            if [ "$render_rc" -ne 0 ]; then
                fail "htpasswd worker-read test: renderer failed (root or passwordless sudo is required): $(tr '\n' ' ' < "$htpasswd_tmp/render.out" 2>/dev/null | cut -c1-300)"
            else
                ht_mode=$(stat -c '%a' "$htpasswd_tmp/htpasswd" 2>/dev/null || stat -f '%Lp' "$htpasswd_tmp/htpasswd" 2>/dev/null)
                ht_owner=$(stat -c '%U' "$htpasswd_tmp/htpasswd" 2>/dev/null || stat -f '%Su' "$htpasswd_tmp/htpasswd" 2>/dev/null)
                ht_group=$(stat -c '%G' "$htpasswd_tmp/htpasswd" 2>/dev/null || stat -f '%Sg' "$htpasswd_tmp/htpasswd" 2>/dev/null)
                if [ "$ht_mode" = "640" ] && [ "$ht_owner" = "root" ] && [ "$ht_group" = "$worker_group" ]; then
                    pass "render-admin-htpasswd.sh produced root:$worker_group mode 0640 for the nginx worker"
                else
                    fail "rendered htpasswd is $ht_owner:$ht_group mode $ht_mode (expected root:$worker_group mode 640)"
                fi
                if [ -n "$ht_mode" ] && [ $((0$ht_mode & 0007)) -eq 0 ]; then
                    pass "rendered htpasswd is not world-readable (mode $ht_mode)"
                else
                    fail "rendered htpasswd is missing or world-readable (mode ${ht_mode:-<none>})"
                fi
                if grep -Fq '$apr1$' "$htpasswd_tmp/render.out"; then
                    fail "render-admin-htpasswd.sh printed an apr1 hash"
                else
                    pass "render-admin-htpasswd.sh printed no password or hash"
                fi

                read_rc=1
                read_via=""
                if [ "$(id -u)" -eq 0 ]; then
                    worker_uid=$(id -u "$worker_user")
                    worker_gid=$(getent group "$worker_group" 2>/dev/null | cut -d: -f3)
                    [ -n "$worker_gid" ] || worker_gid=$(id -g "$worker_user")
                    if command -v setpriv >/dev/null 2>&1; then
                        setpriv --reuid="$worker_uid" --regid="$worker_gid" --clear-groups -- \
                            sh -c 'test -r "$1" && head -c 1 "$1" >/dev/null' sh "$htpasswd_tmp/htpasswd" 2>/dev/null
                        read_rc=$?
                        read_via="setpriv"
                    elif command -v runuser >/dev/null 2>&1; then
                        runuser -u "$worker_user" -- sh -c 'test -r "$1" && head -c 1 "$1" >/dev/null' sh "$htpasswd_tmp/htpasswd" 2>/dev/null
                        read_rc=$?
                        read_via="runuser"
                    elif command -v su >/dev/null 2>&1; then
                        su -s /bin/sh -c "test -r '$htpasswd_tmp/htpasswd' && head -c 1 '$htpasswd_tmp/htpasswd' >/dev/null" "$worker_user" 2>/dev/null
                        read_rc=$?
                        read_via="su"
                    fi
                elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
                    sudo -n -u "$worker_user" -g "$worker_group" -- \
                        sh -c 'test -r "$1" && head -c 1 "$1" >/dev/null' sh "$htpasswd_tmp/htpasswd" 2>/dev/null
                    read_rc=$?
                    read_via="sudo -u $worker_user"
                fi
                if [ "$read_rc" -eq 0 ]; then
                    pass "nginx worker '$worker_user' can read the rendered htpasswd ($read_via)"
                else
                    fail "nginx worker '$worker_user' cannot read the rendered htpasswd ($ht_owner:$ht_group mode $ht_mode)"
                fi
            fi
            rm -rf "$htpasswd_tmp"
            htpasswd_tmp=""
        fi
    fi
fi

# ---------------------------------------------------------------------------
section "CSP browser boot (F50)"
# The shipped vhosts are staged a second time on unprivileged loopback ports
# with a disposable document root. `nginx -t` above validates the real
# :80/:443 configuration; this fixture serves the same location blocks, the
# same snippets and the same TLS policy, so the assertions below test the
# shipped files instead of a hand-written test config. Exactly one nginx runs
# at a time: the `nginx -t` stage and its namespace probe have finished before
# this fixture starts, and this fixture is stopped before the next section.
# check.sh owns the process; csp-smoke.sh only drives the browser.
csp_tmp=""
csp_stop() {
    if [ -n "${csp_tmp:-}" ] && [ -f "$csp_tmp/nginx.pid" ] \
        && [ -n "${nginx_bin:-}" ] && [ -x "${nginx_bin:-/nonexistent}" ]; then
        "$nginx_bin" -p "$csp_tmp/" -c "$csp_tmp/nginx.conf" -s stop >/dev/null 2>&1 || true
    fi
    return 0
}
csp_free_port() {
    if command -v python3 >/dev/null 2>&1; then
        python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()' 2>/dev/null
    else
        printf '%s\n' $((20000 + (RANDOM + ${1:-0} * 4099 + 7) % 20000))
    fi
}
if [ -z "${nginx_bin:-}" ] || [ ! -x "${nginx_bin:-/nonexistent}" ]; then
    skip "CSP runtime gate needs host nginx (it serves the shipped vhosts)"
elif ! command -v openssl >/dev/null 2>&1; then
    skip "CSP runtime gate needs openssl for the throwaway fixture certificate"
elif ! command -v curl >/dev/null 2>&1; then
    skip "CSP runtime gate needs curl"
else
    csp_tmp=$(mktemp -d) || { fail "cannot create the CSP fixture directory"; csp_tmp=""; }
    if [ -n "$csp_tmp" ]; then
        csp_http_port=$(csp_free_port 1)
        csp_https_port=$(csp_free_port 2)
        csp_admin_port=$(csp_free_port 3)
        # A port that was free and stays unbound: the fixture's upstream.
        csp_dead_port=$(csp_free_port 4)
        # Re-roll whichever port collides so a collision can always resolve.
        csp_ports_ok=1
        csp_attempts=0
        while :; do
            csp_collision=0
            if [ "$csp_https_port" = "$csp_http_port" ]; then
                csp_https_port=$(csp_free_port 2)
                csp_collision=1
            fi
            if [ "$csp_admin_port" = "$csp_http_port" ] || [ "$csp_admin_port" = "$csp_https_port" ]; then
                csp_admin_port=$(csp_free_port 3)
                csp_collision=1
            fi
            if [ "$csp_dead_port" = "$csp_http_port" ] || [ "$csp_dead_port" = "$csp_https_port" ] \
                || [ "$csp_dead_port" = "$csp_admin_port" ]; then
                csp_dead_port=$(csp_free_port 4)
                csp_collision=1
            fi
            [ "$csp_collision" -eq 0 ] && break
            csp_attempts=$((csp_attempts + 1))
            if [ "$csp_attempts" -ge 50 ]; then
                csp_ports_ok=0
                break
            fi
        done
        for csp_p in "$csp_http_port" "$csp_https_port" "$csp_admin_port" "$csp_dead_port"; do
            case $csp_p in
                ''|*[!0-9]*) csp_ports_ok=0 ;;
            esac
        done
        if [ "$csp_ports_ok" -ne 1 ]; then
            fail "CSP fixture: cannot allocate four distinct unprivileged loopback ports"
            rm -rf "$csp_tmp"
            csp_tmp=""
        fi
    fi
    if [ -n "$csp_tmp" ]; then
        mkdir -p "$csp_tmp/sites-enabled" "$csp_tmp/snippets" "$csp_tmp/logs" \
            "$csp_tmp/certs/panel.example.com" "$csp_tmp/certs/admin.example.com" \
            "$csp_tmp/temp/client_body" "$csp_tmp/temp/proxy" "$csp_tmp/temp/fastcgi" \
            "$csp_tmp/temp/uwsgi" "$csp_tmp/temp/scgi" \
            "$csp_tmp/www/portal/assets" "$csp_tmp/www/admin/assets"
        cp "$kit/nginx/nginx.conf" "$csp_tmp/nginx.conf"
        cp "$kit/nginx/ajs-mime.types" "$csp_tmp/ajs-mime.types"
        cp "$kit/nginx/snippets/"*.conf "$csp_tmp/snippets/"
        cp "$kit/nginx/sites-available/ajs-portal.conf" "$csp_tmp/sites-enabled/"
        cp "$kit/nginx/sites-available/ajs-admin.conf" "$csp_tmp/sites-enabled/"
        cp "$kit/nginx/sites-available/ajs-default.conf" "$csp_tmp/sites-enabled/"

        # Throwaway operator credentials: the committed kit never contains a
        # real hash, password or bearer.
        printf 'check-operator:%s\n' "$(openssl passwd -apr1 'check-only-password')" > "$csp_tmp/ajs-admin.htpasswd"
        printf '"check-operator" "Bearer %s";\n' "$(printf 'a%.0s' {1..64})" > "$csp_tmp/snippets/ajs-admin-tokens.map"
        openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=panel.example.com" \
            -keyout "$csp_tmp/certs/panel.example.com/privkey.pem" \
            -out "$csp_tmp/certs/panel.example.com/fullchain.pem" >/dev/null 2>&1
        openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=admin.example.com" \
            -keyout "$csp_tmp/certs/admin.example.com/privkey.pem" \
            -out "$csp_tmp/certs/admin.example.com/fullchain.pem" >/dev/null 2>&1

        # Disposable document root. The portal page mimics the real bundle: an
        # external module script (script-src 'self') plus an inline <style>
        # element and a style attribute (style-src 'unsafe-inline'). The login
        # form only exists if the module executed, so a blocked script is a
        # missing marker, and a blocked inline style is a browser refusal.
        cat > "$csp_tmp/www/portal/index.html" <<'HTML'
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>AJS portal fixture</title>
    <link rel="icon" href="data:,">
    <style>
      body { font-family: system-ui, sans-serif; margin: 2rem; }
    </style>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/assets/app.js"></script>
  </body>
</html>
HTML
        cat > "$csp_tmp/www/portal/assets/app.js" <<'JS'
const root = document.getElementById('root');
const form = document.createElement('form');
form.id = 'ajs-portal-login';
form.method = 'post';
form.action = '/v1/portal/login';
form.setAttribute('style', 'display: flex; gap: 8px;');
const email = document.createElement('input');
email.type = 'email';
email.name = 'email';
email.autocomplete = 'username';
const submit = document.createElement('button');
submit.type = 'submit';
submit.textContent = 'Sign in';
form.append(email, submit);
root.append(form);
JS
        cat > "$csp_tmp/www/admin/index.html" <<'HTML'
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>AJS admin fixture</title>
    <link rel="icon" href="data:,">
  </head>
  <body>
    <div id="root" data-ajs-origin="admin"></div>
    <script type="module" src="/assets/app.js"></script>
  </body>
</html>
HTML
        cat > "$csp_tmp/www/admin/assets/app.js" <<'JS'
const root = document.getElementById('root');
root.setAttribute('data-ajs-admin-root', 'rendered');
JS

        # Rewrite the installed paths into the throwaway tree.
        for f in "$csp_tmp/nginx.conf" "$csp_tmp/sites-enabled/"*.conf; do
            sed -i.bak \
                -e "s|/etc/nginx|$csp_tmp|g" \
                -e "s|/etc/ajs/tls|$csp_tmp/certs|g" \
                -e "s|/var/log/nginx|$csp_tmp/logs|g" \
                -e "s|/var/lib/ajs/production/portal|$csp_tmp/www/portal|g" \
                -e "s|/var/lib/ajs/production/admin|$csp_tmp/www/admin|g" \
                "$f"
            rm -f "$f.bak"
        done
        # The fixture must never bind :80/:443, the fixed admin port, or any
        # non-loopback address: it runs unprivileged on ephemeral loopback
        # ports so it cannot be reached from off-host.
        sed -i.bak -E \
            -e "s|^[[:space:]]*listen[[:space:]]+80;|    listen 127.0.0.1:$csp_http_port;|" \
            -e "s|^[[:space:]]*listen[[:space:]]+443 ssl http2;|    listen 127.0.0.1:$csp_https_port ssl http2;|" \
            -e "s|^[[:space:]]*listen[[:space:]]+127\.0\.0\.1:8443 ssl http2;|    listen 127.0.0.1:$csp_admin_port ssl http2;|" \
            -e "s|^[[:space:]]*listen[[:space:]]+80 default_server;|    listen 127.0.0.1:$csp_http_port default_server;|" \
            -e "s|^[[:space:]]*listen[[:space:]]+443 ssl default_server;|    listen 127.0.0.1:$csp_https_port ssl default_server;|" \
            "$csp_tmp/sites-enabled/ajs-portal.conf" "$csp_tmp/sites-enabled/ajs-admin.conf" \
            "$csp_tmp/sites-enabled/ajs-default.conf"
        rm -f "$csp_tmp/sites-enabled/"*.bak
        csp_bad_listen=$(grep -hE '^[[:space:]]*listen[[:space:]]' "$csp_tmp/sites-enabled/"*.conf 2>/dev/null \
            | grep -vE '^[[:space:]]*listen[[:space:]]+127\.0\.0\.1:' || true)
        if [ -z "$csp_bad_listen" ]; then
            pass "CSP fixture listens on 127.0.0.1 ephemeral ports only"
        else
            fail "CSP fixture listen is not loopback-only: $(printf '%s' "$csp_bad_listen" | tr '\n' ';')"
        fi
        # Pin the upstream to a closed loopback port so the API-error response is
        # deterministically an edge 502, never a status from a real daemon.
        sed -i.bak -E "s|127\.0\.0\.1:2261|127.0.0.1:$csp_dead_port|g" "$csp_tmp/nginx.conf"
        rm -f "$csp_tmp/nginx.conf.bak"

        sed -i.bak -E "s|^[[:space:]]*pid[[:space:]]+[^;]+;|pid $csp_tmp/nginx.pid;|" "$csp_tmp/nginx.conf"
        rm -f "$csp_tmp/nginx.conf.bak"
        awk -v root="$csp_tmp" '
            /^[[:space:]]*http[[:space:]]*\{/ {
                print
                print "    client_body_temp_path " root "/temp/client_body;"
                print "    proxy_temp_path " root "/temp/proxy;"
                print "    fastcgi_temp_path " root "/temp/fastcgi;"
                print "    uwsgi_temp_path " root "/temp/uwsgi;"
                print "    scgi_temp_path " root "/temp/scgi;"
                next
            }
            { print }
        ' "$csp_tmp/nginx.conf" > "$csp_tmp/nginx.conf.new" \
            && mv "$csp_tmp/nginx.conf.new" "$csp_tmp/nginx.conf"
        if [ "$(id -u)" -ne 0 ]; then
            sed -i.bak -E "s|^[[:space:]]*user[[:space:]]+[^;]+;|user $(id -un);|" "$csp_tmp/nginx.conf"
            rm -f "$csp_tmp/nginx.conf.bak"
        fi
        chmod -R a+rX "$csp_tmp" 2>/dev/null || true
        chmod 0777 "$csp_tmp/logs" "$csp_tmp/temp/"* 2>/dev/null || true

        csp_nginx_t_out=$("$nginx_bin" -t -p "$csp_tmp/" -c "$csp_tmp/nginx.conf" 2>&1)
        csp_nginx_t_rc=$?
        if [ "$csp_nginx_t_rc" -eq 0 ]; then
            pass "CSP fixture: nginx -t accepts the shipped vhosts on unprivileged ports"
        else
            fail "CSP fixture nginx -t failed: $(printf '%s' "$csp_nginx_t_out" | tr '\n' ' ' | cut -c1-400)"
        fi

        if [ "$csp_nginx_t_rc" -eq 0 ]; then
            "$nginx_bin" -p "$csp_tmp/" -c "$csp_tmp/nginx.conf" >"$csp_tmp/start.log" 2>&1
            csp_up=0
            csp_i=0
            while [ "$csp_i" -lt 100 ]; do
                if curl -sS -k -o /dev/null --max-time 1 \
                    --resolve "panel.example.com:$csp_https_port:127.0.0.1" \
                    "https://panel.example.com:$csp_https_port/healthz" 2>/dev/null; then
                    csp_up=1
                    break
                fi
                csp_i=$((csp_i + 1))
                sleep 0.1
            done
            if [ "$csp_up" -ne 1 ]; then
                fail "CSP fixture nginx did not start: $(tail -c 400 "$csp_tmp/start.log" 2>/dev/null | tr '\n' ' ')"
            else
                csp_header_value() {
                    # HTTP header names are case-insensitive and a second CSP
                    # header must never pass: this prints every CSP header value
                    # it finds, so duplicates make the comparison fail.
                    awk '{
                        line = $0
                        sub(/\r$/, "", line)
                        i = index(line, ":")
                        if (i > 1) {
                            name = tolower(substr(line, 1, i - 1))
                            if (name == "content-security-policy") {
                                value = substr(line, i + 1)
                                sub(/^[ \t]+/, "", value)
                                print value
                            }
                        }
                    }' "$1"
                }
                csp_expect() {
                    local label=$1 want=$2 headers=$3 got_status got_policy
                    got_status=$(sed -n '1s/^HTTP\/[0-9.]*[[:space:]]*\([0-9][0-9][0-9]\).*/\1/p' "$headers")
                    got_policy=$(csp_header_value "$headers")
                    got_count=$(printf '%s\n' "$got_policy" | grep -c . 2>/dev/null || true)
                    if [ "${got_count:-0}" -ne 1 ]; then
                        fail "CSP appears ${got_count:-0} time(s) on $label (want exactly 1)"
                        return 1
                    fi
                    if [ "$got_policy" != "$csp_policy" ]; then
                        fail "CSP wrong on $label: $(printf '%s' "$got_policy" | tr '\n' '|' | cut -c1-300)"
                        return 1
                    fi
                    if [ "$got_status" != "$want" ]; then
                        fail "CSP gate expected HTTP $want on $label, got HTTP ${got_status:-<none>}"
                        return 1
                    fi
                    pass "CSP on $label (HTTP $got_status, exact policy, always)"
                    return 0
                }

                csp_hdr="$csp_tmp/h.hdr"
                csp_body="$csp_tmp/b.body"

                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "panel.example.com:$csp_https_port:127.0.0.1" \
                    "https://panel.example.com:$csp_https_port/portal" 2>/dev/null || true
                csp_expect "portal HTML (/portal)" 200 "$csp_hdr"
                if grep -q 'id="root"' "$csp_body"; then
                    pass "portal HTML fixture served from the staged document root"
                else
                    fail "portal HTML fixture is missing its root marker"
                fi

                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "panel.example.com:$csp_https_port:127.0.0.1" \
                    "https://panel.example.com:$csp_https_port/assets/app.js" 2>/dev/null || true
                csp_expect "portal asset (/assets/app.js)" 200 "$csp_hdr"

                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "panel.example.com:$csp_https_port:127.0.0.1" \
                    "https://panel.example.com:$csp_https_port/portal/login" 2>/dev/null || true
                csp_expect "portal SPA deep link (/portal/login)" 200 "$csp_hdr"

                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "panel.example.com:$csp_https_port:127.0.0.1" \
                    "https://panel.example.com:$csp_https_port/v1/accounts" 2>/dev/null || true
                csp_expect "portal 404 (/v1/accounts is not proxied)" 404 "$csp_hdr"

                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "admin.example.com:$csp_admin_port:127.0.0.1" \
                    -u 'check-operator:check-only-password' \
                    "https://admin.example.com:$csp_admin_port/" 2>/dev/null || true
                csp_expect "admin HTML (/)" 200 "$csp_hdr"
                if grep -q 'data-ajs-origin="admin"' "$csp_body"; then
                    pass "admin HTML fixture served from the staged document root"
                else
                    fail "admin HTML fixture is missing its origin marker"
                fi

                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "admin.example.com:$csp_admin_port:127.0.0.1" \
                    "https://admin.example.com:$csp_admin_port/" 2>/dev/null || true
                csp_expect "admin 401 (no operator credentials)" 401 "$csp_hdr"

                # The upstream is pinned to a closed loopback port, so this is
                # deterministically an nginx-generated 502 and not a status
                # from a daemon that happens to listen on the production port.
                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "admin.example.com:$csp_admin_port:127.0.0.1" \
                    -u 'check-operator:check-only-password' \
                    "https://admin.example.com:$csp_admin_port/v1/accounts" 2>/dev/null || true
                csp_expect "admin API error (/v1/accounts, upstream down)" 502 "$csp_hdr"

                # The default-deny listener must answer an unknown Host, and its
                # 421 must carry the policy; 444 closes the connection without
                # sending a response at all.
                curl -sS -k --max-time 10 -D "$csp_hdr" -o "$csp_body" \
                    --resolve "attacker.invalid:$csp_https_port:127.0.0.1" \
                    "https://attacker.invalid:$csp_https_port/" 2>/dev/null || true
                csp_expect "default-deny 421 (unknown Host)" 421 "$csp_hdr"

                csp_unknown_http=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 \
                    -H 'Host: attacker.invalid' \
                    "http://127.0.0.1:$csp_http_port/" 2>/dev/null || true)
                if [ "$csp_unknown_http" = "000" ] || [ -z "$csp_unknown_http" ]; then
                    pass "default-deny 444 closes the connection for an unknown Host"
                else
                    fail "default-deny 444 did not close the connection: status=${csp_unknown_http:-<none>}"
                fi

                if [ ! -f "$kit/csp-smoke.sh" ]; then
                    fail "CSP browser boot: csp-smoke.sh is missing"
                else
                    csp_smoke_out=$(bash "$kit/csp-smoke.sh" \
                        --url "https://panel.example.com:$csp_https_port/portal/login" \
                        --resolve "panel.example.com:127.0.0.1" \
                        --cert "$csp_tmp/certs/panel.example.com/fullchain.pem" \
                        --expect 'id="ajs-portal-login"' \
                        --label "portal login" \
                        --timeout 180 2>&1)
                    csp_smoke_rc=$?
                    csp_smoke_line=$(printf '%s' "$csp_smoke_out" | tr '\n' ' ' \
                        | sed 's/^csp-smoke: \(PASS\|SKIP\) //; s/[[:space:]]*$//')
                    case $csp_smoke_rc in
                        0) pass "CSP browser boot: $csp_smoke_line" ;;
                        3) skip "CSP browser boot: $csp_smoke_line" ;;
                        *) fail "CSP browser boot: $csp_smoke_line" ;;
                    esac
                fi
            fi
            csp_stop
        fi
        rm -rf "$csp_tmp"
        csp_tmp=""
    fi
fi

# ---------------------------------------------------------------------------
section "token-free bundle path"

if grep -q 'VITE_USE_MOCK=false' "$kit/build-bundles.sh"; then
    pass "bundle build sets VITE_USE_MOCK=false"
else
    fail "bundle build does not set VITE_USE_MOCK=false"
fi
if grep -qE 'VITE_PANEL_TOKEN=[^[:space:]\\]' "$kit/build-bundles.sh"; then
    fail "bundle build assigns a non-empty VITE_PANEL_TOKEN"
else
    pass "bundle build keeps VITE_PANEL_TOKEN empty"
fi
if grep -q 'VITE_API_BASE=' "$kit/build-bundles.sh"; then
    pass "bundle build uses a same-origin API base"
else
    fail "bundle build does not set VITE_API_BASE"
fi
if grep -q 'ajs-build.json' "$kit/build-bundles.sh"; then
    pass "bundle build verifies the live/mock build manifest"
else
    fail "bundle build does not verify ajs-build.json"
fi
if [ -f "$repo_root/dashboard/package.json" ]; then
    if [ -f "$repo_root/dashboard/package-lock.json" ]; then
        pass "dashboard has a committed package-lock.json"
    else
        fail "dashboard/package-lock.json is missing; npm ci cannot run on a fresh checkout"
    fi
    if grep -q 'npm ci' "$kit/build-bundles.sh"; then
        pass "bundle build documents the npm ci install path"
    else
        fail "bundle build does not document npm ci"
    fi
fi
if [ -f "$repo_root/dashboard/Dockerfile" ]; then
    if grep -q '^ARG VITE_USE_MOCK=false' "$repo_root/dashboard/Dockerfile"; then
        pass "dashboard Dockerfile defaults to a live build"
    else
        fail "dashboard Dockerfile does not default VITE_USE_MOCK=false"
    fi
    if grep -q '^ARG VITE_PANEL_TOKEN' "$repo_root/dashboard/Dockerfile"; then
        fail "dashboard Dockerfile accepts a VITE_PANEL_TOKEN build argument"
    else
        pass "dashboard Dockerfile has no VITE_PANEL_TOKEN build argument"
    fi
    if grep -q 'npm ci' "$repo_root/dashboard/Dockerfile"; then
        pass "dashboard Dockerfile installs from the lockfile with npm ci"
    else
        fail "dashboard Dockerfile does not use npm ci"
    fi
fi
if [ -d "$repo_root/.github/workflows" ]; then
    if grep -rqE 'npm install([[:space:]]|$)' "$repo_root/.github/workflows"; then
        fail "a workflow still runs npm install instead of npm ci"
    else
        pass "workflows use npm ci, not npm install"
    fi
fi

# ---------------------------------------------------------------------------
section "systemd unit"
if command -v systemd-analyze >/dev/null 2>&1; then
    unit_tmp=$(mktemp -d) || unit_tmp=""
    if [ -n "$unit_tmp" ]; then
        sed 's|/usr/local/bin/ajs-accountd|/bin/true|; s|/etc/ajs/accountd.env|/dev/null|' \
            "$kit/systemd/ajs-accountd.service" > "$unit_tmp/ajs-accountd.service"
        if command -v timeout >/dev/null 2>&1; then
            systemd_cmd="timeout 20s systemd-analyze"
        else
            systemd_cmd="systemd-analyze"
        fi
        if $systemd_cmd verify "$unit_tmp/ajs-accountd.service" >"$unit_tmp/out" 2>&1; then
            pass "systemd-analyze verify ajs-accountd.service"
        else
            fail "systemd-analyze verify: $(tr '\n' ' ' < "$unit_tmp/out" | cut -c1-500)"
        fi
        rm -rf "$unit_tmp"
        unit_tmp=""
    else
        # A production gate must never omit a required check: without a
        # directory the unit is not verified, so this is a FAIL, not a skip.
        fail "cannot create a temporary directory for systemd-analyze verify"
    fi
else
    skip "systemd-analyze not installed; unit syntax not verified"
fi
for directive in NoNewPrivileges ProtectSystem=strict ProtectHome RestrictAddressFamilies CapabilityBoundingSet SystemCallFilter; do
    if grep -q "$directive" "$kit/systemd/ajs-accountd.service"; then
        pass "hardening directive present: $directive"
    else
        fail "hardening directive missing: $directive"
    fi
done

# ---------------------------------------------------------------------------
section "firewall matches deploy/nftables"
normalize_nft() {
    sed -e 's/#.*//' -e 's/[[:space:]]\+/ /g' -e 's/^ //' -e 's/ $//' | grep -v '^$'
}
repo_rule=$(normalize_nft < "$repo_root/deploy/nftables/ajs-panel.nft" | grep -F 'tcp dport { 2261, 18082 }' | head -1)
prod_rules=$(normalize_nft < "$kit/firewall/ajs-production.nft")
if [ -n "$repo_rule" ] && printf '%s\n' "$prod_rules" | grep -Fxq -- "$repo_rule"; then
    pass "production firewall contains the exact deploy/nftables AJS LAN rule"
else
    fail "production firewall does not match deploy/nftables rule: ${repo_rule:-<none>}"
fi
if grep -q 'table inet ajs_production' "$kit/firewall/ajs-production.nft"; then
    pass "firewall declares table inet ajs_production"
else
    fail "firewall table declaration missing"
fi
if command -v nft >/dev/null 2>&1; then
    nft_out=""
    nft_rc=1
    nft_via=""
    if nft_out=$(nft -c -f "$kit/firewall/ajs-production.nft" 2>&1); then
        nft_rc=0; nft_via="nft"
    elif [ "$(id -u)" -ne 0 ] && command -v unshare >/dev/null 2>&1 \
        && nft_out=$(unshare -Urn nft -c -f "$kit/firewall/ajs-production.nft" 2>&1); then
        nft_rc=0; nft_via="unshare -Urn nft"
    elif [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1 \
        && nft_out=$(sudo -n nft -c -f "$kit/firewall/ajs-production.nft" 2>&1); then
        nft_rc=0; nft_via="sudo -n nft"
    fi
    if [ "$nft_rc" -eq 0 ]; then
        pass "nft -c accepts the production firewall ($nft_via)"
    else
        fail "nft -c rejects the production firewall: $(printf '%s' "$nft_out" | tr '\n' ' ' | cut -c1-500)"
    fi
else
    skip "nft not installed; production gate requires nft -c"
fi

# ---------------------------------------------------------------------------
section "daemon readiness (/readyz)"
ready_tmp=$(mktemp -d) || ready_tmp=""
daemon_bin="${AJS_ACCOUNTD_BIN:-}"
if [ -n "$daemon_bin" ] && [ ! -x "$daemon_bin" ]; then
    fail "AJS_ACCOUNTD_BIN is not executable: $daemon_bin"
    daemon_bin=""
fi
if [ -z "$daemon_bin" ]; then
    daemon_bin=$(command -v ajs-accountd 2>/dev/null || true)
fi
for candidate in "$repo_root/daemon/ajs-accountd" "$repo_root/daemon/ajs-accountd-linux-amd64" "$kit/dist/ajs-accountd"; do
    if [ -z "$daemon_bin" ] && [ -x "$candidate" ]; then daemon_bin=$candidate; fi
done
if [ -z "$daemon_bin" ] && command -v go >/dev/null 2>&1 && [ -n "$ready_tmp" ]; then
    if (cd "$repo_root/daemon" && go build -o "$ready_tmp/ajs-accountd" ./cmd/ajs-accountd) >"$ready_tmp/build.log" 2>&1; then
        daemon_bin="$ready_tmp/ajs-accountd"
    fi
fi
if [ -z "$daemon_bin" ] || [ -z "$ready_tmp" ]; then
    skip "daemon binary unavailable; set AJS_ACCOUNTD_BIN or install go to prove /readyz"
    [ -n "$ready_tmp" ] && rm -rf "$ready_tmp"
else
    ready_token=$(printf 'b%.0s' {1..64})
    ready_port=""
    if command -v python3 >/dev/null 2>&1; then
        ready_port=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)
    fi
    [ -n "$ready_port" ] || ready_port=$((20000 + ($$ % 20000)))
    ready_state="$ready_tmp/state"
    mkdir -p "$ready_state" || fail "cannot create readiness state directory"

    start_ready_daemon() {
        AJS_LISTEN="127.0.0.1:$ready_port" \
        AJS_ADMIN_TOKEN="$ready_token" \
        AJS_STATE_DIR="$ready_state" \
        AJS_SNAPSHOT_INTERVAL=3600 \
        AJS_LOG_LEVEL=error \
        "$daemon_bin" >"$ready_tmp/daemon.log" 2>&1 &
        ready_pid=$!
        ready_up=0
        i=0
        while [ "$i" -lt 100 ]; do
            if ! kill -0 "$ready_pid" 2>/dev/null; then break; fi
            if curl -fsS --max-time 1 "http://127.0.0.1:$ready_port/healthz" >/dev/null 2>&1; then
                ready_up=1
                break
            fi
            i=$((i + 1))
            sleep 0.1
        done
        if [ "$ready_up" -ne 1 ]; then return 1; fi
        # A concurrent check.sh may have picked the same ephemeral port. Prove
        # this listener is our daemon by authenticating with our unique token.
        auth_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 1 \
            -H "Authorization: Bearer $ready_token" \
            "http://127.0.0.1:$ready_port/v1/accounts" 2>/dev/null || true)
        [ "$auth_code" = "200" ]
    }

    stop_ready_daemon() {
        kill -TERM "$ready_pid" 2>/dev/null || true
        i=0
        while kill -0 "$ready_pid" 2>/dev/null && [ "$i" -lt 50 ]; do
            i=$((i + 1))
            sleep 0.1
        done
        kill -KILL "$ready_pid" 2>/dev/null || true
        wait "$ready_pid" 2>/dev/null || true
    }

    if start_ready_daemon; then
        ready_code=$(curl -sS -o "$ready_tmp/ready-ok.json" -w '%{http_code}' --max-time 3 \
            "http://127.0.0.1:$ready_port/readyz" 2>/dev/null || true)
        if [ "$ready_code" = "200" ] && grep -q '"status":"ready"' "$ready_tmp/ready-ok.json" 2>/dev/null; then
            pass "daemon /readyz is 200 ready with a writable state directory"
        else
            fail "daemon /readyz with writable state: status=${ready_code:-<none>} body=$(tr '\n' ' ' < "$ready_tmp/ready-ok.json" 2>/dev/null | cut -c1-300)"
        fi
        stop_ready_daemon

        chmod 0555 "$ready_state" 2>/dev/null || true
        if start_ready_daemon; then
            ready_code=$(curl -sS -o "$ready_tmp/ready-bad.json" -w '%{http_code}' --max-time 3 \
                "http://127.0.0.1:$ready_port/readyz" 2>/dev/null || true)
            live_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 \
                "http://127.0.0.1:$ready_port/healthz" 2>/dev/null || true)
            if [ "$ready_code" = "503" ] && grep -q '"status":"not_ready"' "$ready_tmp/ready-bad.json" 2>/dev/null && \
               grep -q '"name":"state"' "$ready_tmp/ready-bad.json" 2>/dev/null; then
                pass "daemon /readyz is 503 not_ready when the state directory is unwritable"
            else
                fail "daemon /readyz with unwritable state: status=${ready_code:-<none>} body=$(tr '\n' ' ' < "$ready_tmp/ready-bad.json" 2>/dev/null | cut -c1-300)"
            fi
            if [ "$live_code" = "200" ]; then
                pass "daemon /healthz stays 200 (liveness) while /readyz is 503"
            else
                fail "daemon /healthz with unwritable state: status=${live_code:-<none>}"
            fi
            stop_ready_daemon
        else
            fail "daemon did not start with an unwritable state directory: $(tail -c 300 "$ready_tmp/daemon.log" 2>/dev/null | tr '\n' ' ')"
        fi
        chmod 0755 "$ready_state" 2>/dev/null || true
    else
        fail "daemon did not start for /readyz proof: $(tail -c 300 "$ready_tmp/daemon.log" 2>/dev/null | tr '\n' ' ')"
    fi
    rm -rf "$ready_tmp"
fi

# ---------------------------------------------------------------------------
section "AJS-412 production renderer"
# TestAJS412ProductionRenderer (shell acceptance): valid inventory produces
# separate Basic/bearer/hash artifacts; no plaintext reaches the daemon file.
# The daemon opens the hash file as the unit's User=/Group=, so the proof uses
# that identity: root:root 0640 passes a mode-only assertion and still leaves
# strict RBAC unable to read the hashes.
daemon_user=$(sed -n 's/^[[:space:]]*User=//p' "$kit/systemd/ajs-accountd.service" 2>/dev/null | sed -n '1p')
daemon_group=$(sed -n 's/^[[:space:]]*Group=//p' "$kit/systemd/ajs-accountd.service" 2>/dev/null | sed -n '1p')
[ -n "$daemon_user" ] || daemon_user=ajs
[ -n "$daemon_group" ] || daemon_group=$daemon_user
if [ "$(id -u)" -ne 0 ]; then
    if [ "$mode" = production ]; then
        fail "TestAJS412ProductionRenderer: root is required for the ownership assertion (run with sudo -E)"
    else
        skip "TestAJS412ProductionRenderer: ownership assertion needs root (DEV-ONLY skip; production uses sudo -E)"
    fi
elif ! command -v openssl >/dev/null 2>&1 || ! command -v getent >/dev/null 2>&1; then
    skip "TestAJS412ProductionRenderer needs openssl and getent"
elif ! getent group "$daemon_group" >/dev/null 2>&1; then
    skip "TestAJS412ProductionRenderer: daemon group '$daemon_group' does not exist, so the hash file cannot be proven readable"
elif ! id -u "$daemon_user" >/dev/null 2>&1; then
    skip "TestAJS412ProductionRenderer: daemon user '$daemon_user' does not exist, so the hash file cannot be proven readable"
else
    rbac_tmp=$(mktemp -d) || rbac_tmp=""
    if [ -z "$rbac_tmp" ]; then
        fail "cannot create a temporary directory for the AJS-412 renderer proof"
    else
        # The htpasswd renderer refuses a destination directory that no group
        # can search; production /etc/nginx is group-searchable. Keep the
        # fixture group-searchable for that step only -- the renderer then owns
        # the final hash-directory mode, which is asserted below.
        chmod 0710 "$rbac_tmp"
        rbac_token=$(openssl rand -hex 32)
        rbac_digest=$(printf '%s' "$rbac_token" | openssl dgst -sha256 -r | awk '{print $1}')
        printf 'alice:viewer:%s:alice-runtime-password\n' "$rbac_token" > "$rbac_tmp/inventory"
        chmod 0600 "$rbac_tmp/inventory"
        if AJS_OPERATOR_HASH_FILE="$rbac_tmp/operators.hashes" \
           AJS_OPERATOR_MAP_FILE="$rbac_tmp/operators.map" \
           AJS_HTPASSWD_FILE="$rbac_tmp/htpasswd" \
           AJS_DAEMON_GROUP="$daemon_group" AJS_HTPASSWD_GROUP="$(id -gn)" \
           sh "$kit/render-admin-operators.sh" "$rbac_tmp/inventory" >"$rbac_tmp/render.log" 2>&1; then
            # AJS-428 contract: the renderer leaves the hash directory
            # root:<daemon group> 0750 so the daemon (User=ajs Group=ajs) can
            # traverse it; a root:root parent denies the open with EACCES even
            # when the file bits are correct. Assert the mode the renderer
            # produced instead of repairing it, so a pre-AJS-428 renderer fails
            # here rather than passing on a world-traversable fixture.
            rbac_dir_stat=$(stat -c '%U:%G %a' "$rbac_tmp" 2>/dev/null)
            if [ "$rbac_dir_stat" = "root:$daemon_group 750" ]; then
                pass "TestAJS412ProductionRenderer: hash directory is root:$daemon_group 0750"
            else
                fail "TestAJS412ProductionRenderer: hash directory is ${rbac_dir_stat:-missing}, want root:$daemon_group 0750 (AJS-428 renderer contract)"
            fi
            rbac_hash_stat=$(stat -c '%U:%G %a' "$rbac_tmp/operators.hashes" 2>/dev/null)
            if [ "$(stat -c '%a' "$rbac_tmp/operators.hashes")" = 640 ] && \
               [ "$(stat -c '%U' "$rbac_tmp/operators.hashes")" = root ] && \
               [ "$(stat -c '%G' "$rbac_tmp/operators.hashes")" = "$daemon_group" ] && \
               [ "$(stat -c '%a' "$rbac_tmp/operators.map")" = 600 ] && \
               [ "$(stat -c '%a' "$rbac_tmp/htpasswd")" = 640 ] && \
               grep -qx "alice:viewer:$rbac_digest" "$rbac_tmp/operators.hashes" && \
               grep -q '\$apr1\$' "$rbac_tmp/htpasswd"; then
                pass "TestAJS412ProductionRenderer: roles, hashes, map and root:$daemon_group 0640 ownership"
            else
                fail "TestAJS412ProductionRenderer: invalid artifacts, modes or ownership (${rbac_hash_stat:-missing} on operators.hashes)"
            fi
            rbac_read_rc=0
            identity_can_read "$rbac_tmp/operators.hashes" "$daemon_user" "$daemon_group" || rbac_read_rc=$?
            case $rbac_read_rc in
                0) pass "TestAJS412ProductionRenderer: daemon identity $daemon_user:$daemon_group can read the hash file" ;;
                2) skip "TestAJS412ProductionRenderer: cannot switch to $daemon_user:$daemon_group on this host, so hash-file readability is unproven" ;;
                *) fail "TestAJS412ProductionRenderer: daemon identity $daemon_user:$daemon_group cannot read the hash file (${rbac_hash_stat:-missing})" ;;
            esac
            # Negative control: the pre-AJS-428 root:root 0750 parent must fail
            # the same read. Without it the proof above would also pass for a
            # directory mode the daemon cannot traverse.
            if chgrp root "$rbac_tmp" 2>/dev/null && chmod 0750 "$rbac_tmp" 2>/dev/null; then
                rbac_neg_rc=0
                identity_can_read "$rbac_tmp/operators.hashes" "$daemon_user" "$daemon_group" || rbac_neg_rc=$?
                case $rbac_neg_rc in
                    1) pass "TestAJS412ProductionRenderer: root:root 0750 parent denies the daemon (negative control)" ;;
                    2) skip "TestAJS412ProductionRenderer: cannot switch to $daemon_user:$daemon_group for the directory negative control" ;;
                    *) fail "TestAJS412ProductionRenderer: the daemon read the hash file through a root:root 0750 parent" ;;
                esac
                chgrp "$daemon_group" "$rbac_tmp" 2>/dev/null && chmod 0750 "$rbac_tmp" 2>/dev/null
            else
                fail "TestAJS412ProductionRenderer: cannot stage the root:root 0750 negative control"
            fi
            if ! grep -q "$rbac_token" "$rbac_tmp/operators.hashes" && \
               ! grep -q "$rbac_token" "$rbac_tmp/render.log" && \
               ! grep -q 'alice-runtime-password' "$rbac_tmp/render.log"; then
                pass "TestAJS412ProductionRenderer: daemon hashes and log contain no plaintext"
            else
                fail "TestAJS412ProductionRenderer: plaintext token/password leaked"
            fi
            # Two hashes may overlap during rotation, but nginx must have exactly
            # one current key for the person, not a duplicate-key syntax error.
            next_token=$(openssl rand -hex 32)
            printf 'alice:viewer:%s:alice-runtime-password\nalice:viewer:%s:alice-runtime-password\n' "$rbac_token" "$next_token" > "$rbac_tmp/inventory"
            if AJS_OPERATOR_HASH_FILE="$rbac_tmp/operators.hashes" \
               AJS_OPERATOR_MAP_FILE="$rbac_tmp/operators.map" \
               AJS_HTPASSWD_FILE="$rbac_tmp/htpasswd" \
               AJS_DAEMON_GROUP="$daemon_group" AJS_HTPASSWD_GROUP="$(id -gn)" \
               sh "$kit/render-admin-operators.sh" "$rbac_tmp/inventory" >"$rbac_tmp/rotate.log" 2>&1 && \
               [ "$(wc -l < "$rbac_tmp/operators.hashes")" -eq 2 ] && \
               [ "$(wc -l < "$rbac_tmp/operators.map")" -eq 1 ] && \
               [ "$(wc -l < "$rbac_tmp/htpasswd")" -eq 1 ] && \
               grep -q "$next_token" "$rbac_tmp/operators.map"; then
                pass "TestAJS412ProductionRenderer: two-generation rotation has one current nginx token"
            else
                fail "TestAJS412ProductionRenderer: two-generation rendering failed"
            fi
        else
            fail "TestAJS412ProductionRenderer: $(tr '\n' ' ' < "$rbac_tmp/render.log" | cut -c1-300)"
        fi
        rm -rf "$rbac_tmp"
        rbac_tmp=""
    fi
fi

# ---------------------------------------------------------------------------
section "README inputs"
for token in AJS_TENANT_PORTAL AJS_TENANT_ORIGIN AJS_ADMIN_ORIGINS AJS_TRUSTED_PROXIES AJS_ADMIN_TOKEN AJS_RBAC_STRICT AJS_ADMIN_OPERATORS_FILE AJS_MASTER_KEY X-AJS-Operator render-admin-htpasswd render-admin-operators admin-operators DNS TLS; do
    if grep -q "$token" "$kit/README.md"; then
        pass "README documents: $token"
    else
        fail "README missing: $token"
    fi
done

# ---------------------------------------------------------------------------
printf '\n== summary ==\n'
if [ "$mode" = production ]; then
    if [ "$failures" -eq 0 ] && [ "$skips" -eq 0 ]; then
        printf 'check: PASS (%s checks, 0 skipped)\n' "$checks"
        exit 0
    fi
    printf 'check: FAIL (%s failed, %s required skips, %s checks)\n' \
        "$failures" "$required_skips" "$checks"
    exit 1
fi

if [ "$failures" -eq 0 ]; then
    printf 'check: DEV-PASS (%s checks, %s skipped; not a production gate)\n' "$checks" "$skips"
    exit 0
fi
printf 'check: DEV-FAIL (%s failed, %s skipped, %s checks)\n' "$failures" "$skips" "$checks"
exit 1
