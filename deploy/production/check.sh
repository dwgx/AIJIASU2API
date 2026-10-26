#!/usr/bin/env bash
# AJS production deploy-kit self-check.
#
# Read-only: it never installs, reloads nginx, edits the host firewall or
# starts the daemon. Everything that needs a live daemon/cert is staged in a
# throwaway temporary directory.
#
# Usage:
#   bash deploy/production/check.sh                  # production gate (default)
#   bash deploy/production/check.sh --dev            # development-only checks
#   AJS_CHECK_MODE=dev bash deploy/production/check.sh
#
# Exit:
#   0 = production gate passed with no skipped checks, or dev checks passed
#   1 = a check failed, or a required production check was skipped
#   2 = bad usage
#
# Production mode requires a real `nginx -t` from host nginx, openssl for the
# throwaway certificate, systemd-analyze, and nft syntax verification. A
# missing required tool is FAIL, never SKIP. Dev mode allows those tools to be
# absent, but its result is printed as DEV-PASS/DEV-FAIL and can never be
# mistaken for a production PASS.
#
# Tools:
#   nginx (or set AJS_NGINX_BIN=/usr/sbin/nginx; Docker with
#   nginx:1.29-alpine + AJS_CHECK_NGINX_DOCKER=1, dev mode)
#   python3 + PyYAML (or ruby, or yq) for YAML validation
#   openssl for the throwaway nginx test certificate
#   systemd-analyze for unit verification
#   nft for firewall syntax verification (unshare -Urn or sudo -n fallback)
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
            sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
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

printf 'check: mode=%s\n' "$mode"

# ---------------------------------------------------------------------------
section "required files"
required="
README.md
check.sh
build-bundles.sh
render-admin-token.sh
render-admin-htpasswd.sh
admin-operators.example
inventory.example.yml
nginx/nginx.conf
nginx/ajs-mime.types
nginx/snippets/ajs-tls.conf
nginx/snippets/ajs-proxy-common.conf
nginx/snippets/ajs-security-headers.conf
nginx/snippets/ajs-admin-token.conf.template
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
for rel in check.sh build-bundles.sh; do
    if bash -n "$kit/$rel" 2>/dev/null; then
        pass "bash -n $rel"
    else
        fail "bash -n $rel"
    fi
done
for rel in render-admin-token.sh render-admin-htpasswd.sh; do
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
cleanup() {
    [ -n "$nginx_tmp" ] && rm -rf "$nginx_tmp"
    [ -n "$auth_tmp" ] && rm -rf "$auth_tmp"
    [ -n "$htpasswd_tmp" ] && rm -rf "$htpasswd_tmp"
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
            sed "s|\${AJS_ADMIN_TOKEN}|$dummy_token|" \
                "$kit/nginx/snippets/ajs-admin-token.conf.template" > "$nginx_tmp/snippets/ajs-admin-token.conf"

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
if grep -q 'ajs-admin-token.conf' "$kit/nginx/sites-available/ajs-admin.conf"; then
    pass "admin vhost includes the rendered bearer snippet"
else
    fail "admin vhost does not include the rendered bearer snippet"
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
section "README inputs"
for token in AJS_TENANT_PORTAL AJS_TENANT_ORIGIN AJS_ADMIN_ORIGINS AJS_TRUSTED_PROXIES AJS_ADMIN_TOKEN AJS_MASTER_KEY X-AJS-Operator render-admin-htpasswd admin-operators DNS TLS; do
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
