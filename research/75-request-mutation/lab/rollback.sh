#!/usr/bin/env bash
# AJS-15 lab rollback. Run inside the Linux lab (WSL/VM) as root.
#   ./rollback.sh check     verify that no lab artifact is still active
#   ./rollback.sh restore   stop lab processes and restore host state
#   ./rollback.sh purge     restore + delete captured lab data
#
# Every step is idempotent. The script never contacts a vendor host.
set -euo pipefail

LAB_ROOT="${LAB_ROOT:-/run/ajs-lab}"
BACKUP="${BACKUP:-/var/backups/ajs-lab}"
CAPTURES="${CAPTURES:-/var/lib/ajs-lab/captures}"
HOSTS="${HOSTS:-/etc/hosts}"
CA_CRT="${CA_CRT:-/usr/local/share/ca-certificates/ajs-lab-ca.crt}"
NETNS="${NETNS:-ajs-lab}"
PORTS="${PORTS:-443 4443 8443 8444}"

say() { printf '%s\n' "$*"; }
die() { printf 'FAIL %s\n' "$*" >&2; exit 1; }

stop_processes() {
  pkill -f 'lab/labctl.py' 2>/dev/null || true
  pkill -f 'lab/send_mutant.py' 2>/dev/null || true
  pkill -f 'ajs_lab.py' 2>/dev/null || true
  pkill -f 'ajiasu.*lab.conf' 2>/dev/null || true
  sleep 0.2
}

remove_netns() {
  if ip netns list 2>/dev/null | grep -qx "$NETNS"; then
    ip netns del "$NETNS"
  fi
}

restore_hosts() {
  if [ -f "$BACKUP/hosts.bak" ]; then
    install -m 0644 "$BACKUP/hosts.bak" "$HOSTS"
  fi
}

remove_ca() {
  rm -f "$CA_CRT"
  if command -v update-ca-certificates >/dev/null 2>&1; then
    update-ca-certificates --fresh >/dev/null 2>&1 || true
  fi
}

purge_captures() {
  rm -rf "$CAPTURES"
}

check() {
  local bad=0
  if [ -f "$BACKUP/hosts.bak" ] && ! cmp -s "$BACKUP/hosts.bak" "$HOSTS"; then
    say "FAIL hosts differs from backup"; bad=1
  fi
  if [ -e "$CA_CRT" ]; then say "FAIL lab CA still installed"; bad=1; fi
  if ip netns list 2>/dev/null | grep -qx "$NETNS"; then say "FAIL netns $NETNS still present"; bad=1; fi
  for p in $PORTS; do
    if ss -lnt 2>/dev/null | awk '{print $4}' | grep -qE "[:.]$p$"; then
      say "FAIL port $p still listening"; bad=1
    fi
  done
  if pgrep -f 'lab/labctl.py' >/dev/null 2>&1; then
    say "FAIL lab process still running"; bad=1
  fi
  [ "$bad" -eq 0 ] && say "OK rollback=1 hosts=clean ca=absent netns=absent ports=free"
  return "$bad"
}

restore() {
  stop_processes
  remove_netns
  restore_hosts
  remove_ca
  rm -rf "$LAB_ROOT"
  check
}

case "${1:-restore}" in
  check) check ;;
  restore) restore ;;
  purge) restore; purge_captures; say "OK captures=purged" ;;
  *) die "usage: $0 {check|restore|purge}" ;;
esac
