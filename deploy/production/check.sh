#!/usr/bin/env bash
# AJS production deploy-kit self-check.
#
# Read-only: it never installs, reloads nginx, edits the host firewall or
# starts the daemon. Everything that needs a live daemon/cert is staged in a
# throwaway temporary directory.
#
# Usage: bash deploy/production/check.sh
# Exit:  0 = all checks pass (or an optional tool is absent and was skipped)
#        1 = at least one check failed
#
# Optional tools:
#   nginx (or Docker with nginx:1.29-alpine + AJS_CHECK_NGINX_DOCKER=1)
#   python3 + PyYAML (or ruby, or yq) for YAML validation
#   openssl for the throwaway nginx test certificate
#   systemd-analyze for unit verification
#   nft for firewall syntax verification
set -u

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || {
    echo "check: cannot resolve script directory" >&2
    exit 1
}
kit=$script_dir
repo_root=$(CDPATH= cd -- "$kit/../.." && pwd) || repo_root=.

checks=0
failures=0

pass() { checks=$((checks + 1)); printf 'PASS  %s\n' "$*"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); printf 'FAIL  %s\n' "$*"; }
skip() { printf 'SKIP  %s\n' "$*"; }
section() { printf '\n== %s ==\n' "$*"; }

# ---------------------------------------------------------------------------
section "required files"
required="
README.md
check.sh
build-bundles.sh
render-admin-token.sh
inventory.example.yml
nginx/nginx.conf
nginx/ajs-mime.types
nginx/snippets/ajs-tls.conf
nginx/snippets/ajs-proxy-common.conf
nginx/snippets/ajs-security-headers.conf
nginx/snippets/ajs-admin-token.conf.template
nginx/sites-available/ajs-portal.conf
nginx/sites-available/ajs-admin.conf
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
if sh -n "$kit/render-admin-token.sh" 2>/dev/null; then
    pass "sh -n render-admin-token.sh"
else
    fail "sh -n render-admin-token.sh"
fi

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
cleanup() { [ -n "$nginx_tmp" ] && rm -rf "$nginx_tmp"; }
trap cleanup 0 HUP INT TERM

nginx_bin=$(command -v nginx 2>/dev/null || true)
nginx_mode=""
if [ -n "$nginx_bin" ]; then
    nginx_mode=host
elif [ "${AJS_CHECK_NGINX_DOCKER:-0}" = "1" ] && command -v docker >/dev/null 2>&1; then
    nginx_mode=docker
else
    skip "nginx not installed; install nginx or set AJS_CHECK_NGINX_DOCKER=1 (uses nginx:1.29-alpine)"
fi

if [ -n "$nginx_mode" ]; then
    if ! command -v openssl >/dev/null 2>&1; then
        skip "openssl not found; cannot generate the throwaway test certificate"
    else
        nginx_tmp=$(mktemp -d) || { fail "cannot create temporary nginx directory"; nginx_tmp=""; }
        if [ -n "$nginx_tmp" ]; then
            mkdir -p "$nginx_tmp/sites-enabled" "$nginx_tmp/snippets" "$nginx_tmp/logs" \
                "$nginx_tmp/certs/panel.example.com" "$nginx_tmp/certs/admin.example.com" \
                "$nginx_tmp/temp/client_body_temp" "$nginx_tmp/temp/proxy_temp" \
                "$nginx_tmp/temp/fastcgi_temp" "$nginx_tmp/temp/uwsgi_temp" "$nginx_tmp/temp/scgi_temp" \
                "$nginx_tmp/client_body_temp" "$nginx_tmp/proxy_temp" \
                "$nginx_tmp/fastcgi_temp" "$nginx_tmp/uwsgi_temp" "$nginx_tmp/scgi_temp"
            cp "$kit/nginx/nginx.conf" "$nginx_tmp/nginx.conf"
            cp "$kit/nginx/ajs-mime.types" "$nginx_tmp/ajs-mime.types"
            cp "$kit/nginx/snippets/"*.conf "$nginx_tmp/snippets/" 2>/dev/null || true
            cp "$kit/nginx/sites-available/ajs-portal.conf" "$nginx_tmp/sites-enabled/"
            cp "$kit/nginx/sites-available/ajs-admin.conf" "$nginx_tmp/sites-enabled/"

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

            # Rewrite the host paths to the throwaway tree.
            for f in "$nginx_tmp/nginx.conf" "$nginx_tmp/sites-enabled/"*.conf; do
                sed -i "s|/etc/nginx|$nginx_tmp|g; s|/etc/ajs/tls|$nginx_tmp/certs|g; s|/var/log/nginx|$nginx_tmp/logs|g" "$f"
            done
            if [ "$nginx_mode" = docker ]; then
                sed -i 's/^user .*/user nginx;/' "$nginx_tmp/nginx.conf"
            fi

            nginx_output=""
            nginx_rc=1
            if [ "$nginx_mode" = host ]; then
                nginx_output=$("$nginx_bin" -t -p "$nginx_tmp/" -c "$nginx_tmp/nginx.conf" 2>&1)
                nginx_rc=$?
            else
                nginx_output=$(docker run --rm -v "$nginx_tmp:/work" -w /work nginx:1.29-alpine \
                    nginx -t -p /work/ -c /work/nginx.conf 2>&1)
                nginx_rc=$?
            fi
            if [ "$nginx_rc" -eq 0 ]; then
                pass "nginx -t: portal and admin vhosts"
            else
                fail "nginx -t failed: $(printf '%s' "$nginx_output" | tr '\n' ' ' | cut -c1-500)"
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
if grep -qE 'allow[[:space:]]+127\.0\.0\.1;' "$kit/nginx/sites-available/ajs-admin.conf" && \
   grep -qE 'deny[[:space:]]+all;' "$kit/nginx/sites-available/ajs-admin.conf"; then
    pass "admin vhost has a loopback/VPN allow policy ending in deny all"
else
    fail "admin vhost is missing the allow/deny policy"
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
if [ "${AJS_CHECK_NFT:-0}" = "1" ] && command -v nft >/dev/null 2>&1; then
    if nft -c -f "$kit/firewall/ajs-production.nft" >/dev/null 2>&1; then
        pass "nft -c accepts the production firewall"
    else
        fail "nft -c rejects the production firewall"
    fi
else
    skip "nft syntax check disabled (set AJS_CHECK_NFT=1 on a host with nft and root)"
fi

# ---------------------------------------------------------------------------
section "README inputs"
for token in AJS_TENANT_PORTAL AJS_TENANT_ORIGIN AJS_TRUSTED_PROXIES AJS_ADMIN_TOKEN AJS_MASTER_KEY DNS TLS; do
    if grep -q "$token" "$kit/README.md"; then
        pass "README documents: $token"
    else
        fail "README missing: $token"
    fi
done

# ---------------------------------------------------------------------------
printf '\n== summary ==\n'
if [ "$failures" -eq 0 ]; then
    printf 'check: PASS (%s checks)\n' "$checks"
    exit 0
fi
printf 'check: FAIL (%s failed of %s checks)\n' "$failures" "$checks"
exit 1
