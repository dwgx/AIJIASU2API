#!/usr/bin/env bash
# Rotating tcpdump capture with a BPF filter file.
#
# Safety properties:
#   * the filter is a file, not an argument, so no credential ever reaches argv;
#   * a filter that looks like it contains a credential is refused;
#   * the default interface is loopback, not `any`;
#   * files are created under umask 077 and hashed after the run.
#
# The filter file holds one complete BPF clause per line; blank lines and
# `#` comments are ignored. Clauses are OR-ed, so each line can be an
# independent host/port group. Example:
#
#   # control API
#   (tcp port 443 or tcp port 4443) and (host login.ajs-app.com or host login.mmegg.com)
#   # one FV endpoint learned from the Servers response
#   host 203.0.113.10 and tcp port 8443
#
# Usage:
#   capture.sh --filter-file filters/control.bpf --name control --iface any --duration 900
#   capture.sh --filter-file filters/loopback.bpf --name dryrun --iface lo --duration 8
#   capture.sh --stop cap/control.pid
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
LAB_ROOT=${AJS_LAB_ROOT:-/srv/dwgxhomecloud/scratch/ajs-51-debugger}
OUT_DIR=${AJS_CAP_DIR:-$LAB_ROOT/cap}
IFACE=lo
FILTER_FILE=$SCRIPT_DIR/filters/control.bpf
NAME=capture
ROTATE_SECONDS=300
RING_FILES=24
DURATION=0
DRY_RUN=0
STOP_PIDFILE=

die() { printf 'capture: %s\n' "$*" >&2; exit 2; }
log() { printf 'capture: %s\n' "$*" >&2; }

usage() {
  sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

while (($#)); do
  case "$1" in
    --filter-file) FILTER_FILE=${2:?--filter-file needs a value}; shift 2 ;;
    --name) NAME=${2:?--name needs a value}; shift 2 ;;
    --iface) IFACE=${2:?--iface needs a value}; shift 2 ;;
    --out) OUT_DIR=${2:?--out needs a value}; shift 2 ;;
    --rotate-seconds) ROTATE_SECONDS=${2:?--rotate-seconds needs a value}; shift 2 ;;
    --ring) RING_FILES=${2:?--ring needs a value}; shift 2 ;;
    --duration) DURATION=${2:?--duration needs a value}; shift 2 ;;
    --stop) STOP_PIDFILE=${2:?--stop needs a value}; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage ;;
    *) die "unknown argument: $1" ;;
  esac
done

if [[ -n $STOP_PIDFILE ]]; then
  [[ -f $STOP_PIDFILE ]] || die "pid file $STOP_PIDFILE does not exist"
  pid=$(cat "$STOP_PIDFILE")
  [[ $pid =~ ^[0-9]+$ ]] || die "pid file $STOP_PIDFILE is not a pid"
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$STOP_PIDFILE"
    log "pid $pid is not running; removed stale pid file"
    exit 0
  fi
  kill -INT "$pid"
  for _ in $(seq 1 100); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.2
  done
  if kill -0 "$pid" 2>/dev/null; then
    log "pid $pid did not stop on SIGINT; sending SIGTERM"
    kill -TERM "$pid" 2>/dev/null || true
  fi
  rm -f "$STOP_PIDFILE"
  log "stopped pid $pid"
  exit 0
fi

command -v tcpdump >/dev/null 2>&1 || die "tcpdump is not installed"
[[ -f $FILTER_FILE ]] || die "filter file $FILTER_FILE does not exist"
[[ $NAME =~ ^[A-Za-z0-9._-]+$ ]] || die "--name must match [A-Za-z0-9._-]+"
[[ $ROTATE_SECONDS =~ ^[1-9][0-9]*$ ]] || die "--rotate-seconds must be a positive integer"
[[ $RING_FILES =~ ^[1-9][0-9]*$ ]] || die "--ring must be a positive integer"
[[ $DURATION =~ ^[0-9]+$ ]] || die "--duration must be a non-negative integer"

if grep -Eiq '(password|passwd|authorization|bearer|secret|token)[[:space:]]*=' "$FILTER_FILE"; then
  die "filter file $FILTER_FILE looks like it contains a credential; BPF filters must be hosts and ports only"
fi

if [[ $(id -u) -ne 0 ]]; then
  if ! command -v capsh >/dev/null 2>&1 || ! capsh --has-p=cap_net_raw >/dev/null 2>&1; then
    die "tcpdump needs root or CAP_NET_RAW; run as root on HMC"
  fi
fi

clauses=()
while IFS= read -r line; do
  line=${line%%#*}
  line=${line#"${line%%[![:space:]]*}"}
  line=${line%"${line##*[![:space:]]}"}
  [[ -z $line ]] && continue
  clauses+=("($line)")
done < "$FILTER_FILE"
((${#clauses[@]})) || die "filter file $FILTER_FILE has no clauses"

BPF=
for clause in "${clauses[@]}"; do
  if [[ -z $BPF ]]; then BPF=$clause; else BPF+=" or $clause"; fi
done

# Compile the filter without capturing. A typo fails here, not after the
# operator has already started a live run.
if ! tcpdump -i "$IFACE" -d "$BPF" >/dev/null 2>&1; then
  log "BPF does not compile against interface $IFACE:"
  tcpdump -i "$IFACE" -d "$BPF" >&2 || true
  exit 2
fi

mkdir -p "$OUT_DIR"
chmod 700 "$OUT_DIR" 2>/dev/null || true
OUT_DIR=$(cd -- "$OUT_DIR" && pwd)
pattern="$OUT_DIR/$NAME-%Y%m%dT%H%M%SZ.pcap"
pidfile="$OUT_DIR/$NAME.pid"

cmd=(tcpdump -i "$IFACE" -n -s 0 -Z "$(id -un)" -G "$ROTATE_SECONDS" -W "$RING_FILES" -w "$pattern" "$BPF")

if ((DRY_RUN)); then
  printf 'iface=%s\n' "$IFACE"
  printf 'filter_file=%s\n' "$FILTER_FILE"
  printf 'filter=%s\n' "$BPF"
  printf 'pattern=%s\n' "$pattern"
  printf 'rotate_seconds=%s ring=%s duration=%s\n' "$ROTATE_SECONDS" "$RING_FILES" "$DURATION"
  printf 'command:'
  printf ' %q' "${cmd[@]}"
  printf '\n'
  exit 0
fi

if [[ -f $pidfile ]]; then
  old=$(cat "$pidfile")
  if [[ $old =~ ^[0-9]+$ ]] && kill -0 "$old" 2>/dev/null; then
    die "capture already running as pid $old ($pidfile)"
  fi
  rm -f "$pidfile"
fi

umask 077
export TZ=UTC
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
log "starting on $IFACE (rotate ${ROTATE_SECONDS}s, ring ${RING_FILES})"

if ((DURATION > 0)); then
  timeout --signal=INT --kill-after=10 "$DURATION" "${cmd[@]}" &
else
  "${cmd[@]}" &
fi
pid=$!
printf '%s\n' "$pid" > "$pidfile"
cleaned=0

cleanup() {
  local rc=$?
  if ((cleaned)); then
    return
  fi
  cleaned=1
  if kill -0 "$pid" 2>/dev/null; then
    kill -INT "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -f "$pidfile"
  chmod 600 "$OUT_DIR/$NAME"-*.pcap 2>/dev/null || true
  {
    printf 'started=%s\n' "$started"
    printf 'ended=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'iface=%s\n' "$IFACE"
    printf 'filter_sha256=%s\n' "$(sha256sum "$FILTER_FILE" | cut -d' ' -f1)"
    printf 'filter=%s\n' "$BPF"
    printf 'tcpdump=%s\n' "$(tcpdump --version 2>&1 | head -1)"
    printf 'uname=%s\n' "$(uname -srm)"
  } > "$OUT_DIR/$NAME.meta"
  if compgen -G "$OUT_DIR/$NAME-*.pcap" >/dev/null; then
    (cd "$OUT_DIR" && sha256sum "$NAME"-*.pcap > "$NAME.sha256")
    log "wrote $(compgen -G "$OUT_DIR/$NAME-*.pcap" | wc -l) file(s); hashes in $OUT_DIR/$NAME.sha256"
  else
    log "no pcap files were written"
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

set +e
wait "$pid"
rc=$?
set -e
case "$rc" in
  0|124|130) rc=0 ;;
  *) log "tcpdump exited with status $rc" ;;
esac
exit "$rc"
