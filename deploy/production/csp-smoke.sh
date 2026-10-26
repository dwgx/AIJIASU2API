#!/usr/bin/env bash
# AJS CSP browser smoke (F50).
#
# Drives a headless Chromium-family browser against an ALREADY RUNNING staged
# nginx fixture and proves the shipped Content-Security-Policy does not break
# the portal. This helper never starts, reloads or stops nginx: check.sh owns
# the fixture's lifecycle, passes the URL, and maps the fixture Host name to
# loopback with --resolve. No npm dependency is involved.
#
# Usage:
#   bash deploy/production/csp-smoke.sh \
#     --url https://panel.example.com:18443/portal/login \
#     [--resolve panel.example.com:127.0.0.1] \
#     [--expect 'id="ajs-portal-login"'] \
#     [--cert /path/fixture-fullchain.pem] \
#     [--label 'portal login'] [--timeout 120] [--browser /usr/bin/chromium]
#
# Exit:
#   0 = the expected content rendered and the browser logged no CSP refusal
#   1 = the page loaded but is broken, the CSP refused a resource, or the
#       browser could not reach the fixture
#   2 = bad usage
#   3 = no usable browser (absent or unable to start; the caller decides:
#       dev SKIP, production FAIL)
#
# This helper only reports a pass it observed in the rendered DOM. It requires
# --resolve to a 127.x loopback address and requires the URL host to be that
# same fixture host, so no exception can ever be applied to a real origin.
# With --cert the browser trusts only that certificate's SPKI
# (`--ignore-certificate-errors-spki-list`); without it the fallback is the
# loopback-scoped `--ignore-certificate-errors`.
set -u

url=""
expect='id="root"'
label=""
timeout_s=120
browser=${AJS_CHROMIUM_BIN:-}
resolve=""
cert=""

while [ $# -gt 0 ]; do
    case $1 in
        --url|--expect|--label|--timeout|--browser|--resolve|--cert)
            if [ $# -lt 2 ]; then
                printf 'csp-smoke: %s requires a value\n' "$1" >&2
                exit 2
            fi
            case $1 in
                --url) url=$2 ;;
                --expect) expect=$2 ;;
                --label) label=$2 ;;
                --timeout) timeout_s=$2 ;;
                --browser) browser=$2 ;;
                --resolve) resolve=$2 ;;
                --cert) cert=$2 ;;
            esac
            shift 2
            ;;
        -h|--help)
            awk 'NR > 1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "$0"
            exit 0
            ;;
        *)
            printf 'csp-smoke: unknown argument: %s\n' "$1" >&2
            exit 2
            ;;
    esac
done

if [ -z "$url" ]; then
    printf 'csp-smoke: --url is required\n' >&2
    exit 2
fi
case $url in
    https://*) ;;
    *)
        printf 'csp-smoke: --url must be https:// (the fixture terminates TLS)\n' >&2
        exit 2
        ;;
esac
case $timeout_s in
    ''|*[!0-9]*)
        printf 'csp-smoke: --timeout must be a number of seconds\n' >&2
        exit 2
        ;;
esac
[ -n "$label" ] || label=${url#https://}

# The fixture mapping is mandatory: without it the certificate exception would
# be unrestricted and the helper could be pointed at a real HTTPS origin.
if [ -z "$resolve" ]; then
    printf 'csp-smoke: --resolve host:127.0.0.1 is required (loopback fixture only)\n' >&2
    exit 2
fi
case $resolve in
    *:*) ;;
    *)
        printf 'csp-smoke: --resolve must be host:address, got: %s\n' "$resolve" >&2
        exit 2
        ;;
esac
resolve_host=${resolve%:*}
resolve_addr=${resolve##*:}
if [ -z "$resolve_host" ] || [ -z "$resolve_addr" ]; then
    printf 'csp-smoke: --resolve must be host:address, got: %s\n' "$resolve" >&2
    exit 2
fi
case $resolve_addr in
    127.*) ;;
    *)
        printf 'csp-smoke: --resolve must map to a 127.x loopback address, got: %s\n' "$resolve_addr" >&2
        exit 2
        ;;
esac
url_hostport=${url#https://}
url_hostport=${url_hostport%%/*}
case $url_hostport in
    \[*) url_host=${url_hostport%%]*}; url_host=${url_host#\[} ;;
    *:*) url_host=${url_hostport%:*} ;;
    *) url_host=$url_hostport ;;
esac
if [ "$url_host" != "$resolve_host" ]; then
    printf 'csp-smoke: --url host %s does not match --resolve host %s\n' \
        "${url_host:-<none>}" "$resolve_host" >&2
    exit 2
fi

# The certificate exception exists only for the disposable loopback fixture
# whose self-signed certificate cannot be trusted. The checks above make that
# the only reachable case. With --cert the exception is pinned to that
# certificate's SPKI; AJS_CSP_STRICT_TLS=1 disables the unpinned fallback.
cert_exception=1
if [ "${AJS_CSP_STRICT_TLS:-0}" = "1" ]; then
    cert_exception=0
fi
spki=""
if [ -n "$cert" ]; then
    if [ ! -f "$cert" ]; then
        printf 'csp-smoke: --cert file not found: %s\n' "$cert" >&2
        exit 2
    fi
    if ! command -v openssl >/dev/null 2>&1; then
        printf 'csp-smoke: --cert needs openssl to compute the SPKI pin\n' >&2
        exit 2
    fi
    spki=$(openssl x509 -in "$cert" -pubkey -noout 2>/dev/null \
        | openssl pkey -pubin -outform der 2>/dev/null \
        | openssl dgst -sha256 -binary 2>/dev/null \
        | openssl base64 -A 2>/dev/null)
    if [ -z "$spki" ]; then
        printf 'csp-smoke: cannot compute the SPKI pin for %s\n' "$cert" >&2
        exit 2
    fi
fi

find_browser() {
    local candidate
    if [ -n "$browser" ]; then
        if [ -x "$browser" ]; then
            printf '%s\n' "$browser"
            return 0
        fi
        return 1
    fi
    for candidate in chromium chromium-browser google-chrome google-chrome-stable chrome; do
        if command -v "$candidate" >/dev/null 2>&1; then
            command -v "$candidate"
            return 0
        fi
    done
    for candidate in \
        /usr/bin/chromium \
        /usr/bin/chromium-browser \
        /usr/bin/google-chrome \
        /usr/bin/google-chrome-stable \
        /snap/bin/chromium \
        /opt/google/chrome/chrome; do
        if [ -x "$candidate" ]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

requested_browser=$browser
if ! browser=$(find_browser); then
    if [ -n "$requested_browser" ]; then
        printf 'csp-smoke: SKIP no usable browser: %s is not executable\n' "$requested_browser"
    else
        printf 'csp-smoke: SKIP no usable browser: set AJS_CHROMIUM_BIN or install chromium/google-chrome\n'
    fi
    exit 3
fi

work=$(mktemp -d) || {
    printf 'csp-smoke: FAIL %s: cannot create a temporary directory\n' "$label"
    exit 1
}
cleanup() {
    if [ -n "${work:-}" ]; then
        rm -rf "$work"
    fi
    return 0
}
trap cleanup 0 HUP INT TERM

# The sandbox stays on unless the browser runs as root (Chrome refuses that) or
# the first attempt cannot start it (containers/WSL without user namespaces);
# the retry is recorded in the failure path, never silently reported as a pass.
default_sandbox=1
if [ "$(id -u)" -eq 0 ]; then
    default_sandbox=0
fi

run_attempt() {
    local headless=$1 sandbox=$2
    local -a args=(
        "$browser" "$headless"
        --disable-gpu
        --disable-dev-shm-usage
        --no-first-run
        --no-default-browser-check
        --disable-background-networking
        --disable-component-update
        --disable-sync
        --disable-extensions
        --disable-default-apps
        --metrics-recording-only
        --mute-audio
        --hide-scrollbars
        --window-size=1280,900
        "--user-data-dir=$work/profile"
        --virtual-time-budget=5000
        --dump-dom
        --enable-logging=stderr
        --log-level=0
    )
    if [ -n "$spki" ]; then
        # Trust exactly the fixture certificate's key, nothing else.
        args+=("--ignore-certificate-errors-spki-list=$spki")
    elif [ "$cert_exception" = 1 ]; then
        # Fallback, still scoped to the enforced 127.x fixture mapping.
        args+=(--ignore-certificate-errors)
    fi
    if [ "$sandbox" = 0 ]; then
        args+=(--no-sandbox)
    fi
    if [ -n "$resolve_host" ]; then
        args+=("--host-resolver-rules=MAP $resolve_host $resolve_addr")
    fi
    args+=("$url")
    if command -v timeout >/dev/null 2>&1; then
        timeout "$timeout_s" "${args[@]}" >"$work/dom.html" 2>"$work/browser.log"
    else
        "${args[@]}" >"$work/dom.html" 2>"$work/browser.log"
    fi
}

dom_bytes=0
last_rc=0
attempt=0
while [ "$attempt" -lt 3 ]; do
    attempt=$((attempt + 1))
    case $attempt in
        1) run_attempt --headless=new "$default_sandbox" ;;
        2) run_attempt --headless=new 0 ;;
        *) run_attempt --headless 0 ;;
    esac
    last_rc=$?
    dom_bytes=$(wc -c <"$work/dom.html" 2>/dev/null | tr -d ' \n')
    case $dom_bytes in
        ''|*[!0-9]*) dom_bytes=0 ;;
    esac
    if [ "$dom_bytes" -gt 0 ]; then
        break
    fi
done

if [ "$dom_bytes" -eq 0 ]; then
    # A present browser that could not navigate is a gate failure; only a
    # browser that could not start at all is reported as unusable.
    nav_re='net::ERR_|ERR_(CONNECTION_REFUSED|NAME_NOT_RESOLVED|ADDRESS_UNREACHABLE|CONNECTION_TIMED_OUT|SSL_PROTOCOL_ERROR|CERT_)|net_error'
    if nav_err=$(grep -Em1 "$nav_re" "$work/browser.log"); then
        printf 'csp-smoke: FAIL %s: browser could not reach the fixture: %s\n' \
            "$label" "$(printf '%s' "$nav_err" | cut -c1-300)"
        exit 1
    fi
    printf 'csp-smoke: SKIP no usable browser: %s could not start (exit %s): %s\n' \
        "$browser" "$last_rc" \
        "$(tail -c 300 "$work/browser.log" 2>/dev/null | tr '\n' ' ')"
    exit 3
fi

# Any of these means the policy refused something the bundle needs. The check
# runs on the browser's own console log, so a blocked inline style or a blocked
# external module is a failure even when the rest of the page still renders.
refuse_re='Refused to (load|execute|apply|run|connect|frame|evaluate)|Content Security Policy directive|violates the following Content Security Policy'
if refusal=$(grep -Em1 "$refuse_re" "$work/browser.log"); then
    printf 'csp-smoke: FAIL %s: CSP refused a resource: %s\n' "$label" "$(printf '%s' "$refusal" | cut -c1-300)"
    exit 1
fi

if grep -Eq 'ERR_(CONNECTION_REFUSED|NAME_NOT_RESOLVED|ADDRESS_UNREACHABLE|CONNECTION_TIMED_OUT)|This site can.t be reached' "$work/dom.html"; then
    printf 'csp-smoke: FAIL %s: browser could not reach the fixture URL\n' "$label"
    exit 1
fi

if ! grep -Eq -- "$expect" "$work/dom.html"; then
    printf 'csp-smoke: FAIL %s: rendered DOM does not contain %s\n' "$label" "$expect"
    printf 'csp-smoke: DOM head: %s\n' "$(head -c 400 "$work/dom.html" 2>/dev/null | tr '\n' ' ')"
    exit 1
fi

printf 'csp-smoke: PASS %s: rendered %s with no CSP refusal (%s bytes DOM, %s)\n' \
    "$label" "$expect" "$dom_bytes" "$browser"
exit 0
