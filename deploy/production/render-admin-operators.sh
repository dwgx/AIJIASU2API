#!/bin/sh
# Render all three per-operator credentials from one root-only inventory.
# Inventory: username:viewer|operator|admin:token:password (colon-free fields).
# Tokens are random >=32-character secrets, not the operator password.
#
# Outputs (atomic, never printed):
#   /etc/nginx/ajs-admin.htpasswd                  root:<nginx group> 0640
#   /etc/nginx/snippets/ajs-admin-tokens.map       root:root 0600
#   /etc/ajs/admin-operators.hashes                root:<daemon group> 0640
# The directory holding the hash file is root:<daemon group> 0750: the daemon
# runs as User=ajs Group=ajs and must traverse it to open the 0640 file.
# The daemon reads hashes only; nginx reads its map at reload. Restart both
# after editing roles. Token rotation may use a second same-name/same-role row.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
default_hash_dir=/etc/ajs
inventory=${1:-$default_hash_dir/admin-operators}
hash_file=${AJS_OPERATOR_HASH_FILE:-$default_hash_dir/admin-operators.hashes}
map_file=${AJS_OPERATOR_MAP_FILE:-/etc/nginx/snippets/ajs-admin-tokens.map}
htpasswd_file=${AJS_HTPASSWD_FILE:-/etc/nginx/ajs-admin.htpasswd}
daemon_group=${AJS_DAEMON_GROUP:-ajs}
hash_file_configured=0
if [ -n "${AJS_OPERATOR_HASH_FILE:-}" ]; then hash_file_configured=1; fi
hash_dir=$(dirname -- "$hash_file")
map_dir=$(dirname -- "$map_file")

[ "$(id -u)" -eq 0 ] || { echo "render-admin-operators: must run as root" >&2; exit 2; }
[ -r "$inventory" ] || { echo "render-admin-operators: inventory not readable" >&2; exit 2; }
case ${inventory##*/} in
  *.example) echo "render-admin-operators: refusing an example inventory" >&2; exit 2 ;;
esac
if command -v getent >/dev/null 2>&1; then
  getent group "$daemon_group" >/dev/null 2>&1 || { echo "render-admin-operators: daemon group does not exist: $daemon_group" >&2; exit 2; }
fi
command -v openssl >/dev/null 2>&1 || { echo "render-admin-operators: openssl is required" >&2; exit 2; }

# --- output directory safety -------------------------------------------------
# The renderer changes the hash directory's ownership and mode. Canonicalize
# both output directories before writing anything so a symlink, "." or ".."
# component, or a duplicate slash cannot redirect that repair to an alias of a
# shared directory. Only the canonical leaf directory is ever changed.
canonical_output_dir() {
  cod_label=$1
  cod_dir=$2
  case $cod_dir in
    /*) ;;
    *) echo "render-admin-operators: $cod_label must be absolute: $cod_dir" >&2; return 1 ;;
  esac
  if command -v realpath >/dev/null 2>&1 && realpath -m -- / >/dev/null 2>&1; then
    cod_canon=$(realpath -m -- "$cod_dir") || {
      echo "render-admin-operators: cannot canonicalize $cod_label: $cod_dir" >&2
      return 1
    }
  else
    cod_canon=$(readlink -f -- "$cod_dir") || {
      echo "render-admin-operators: cannot canonicalize $cod_label: $cod_dir" >&2
      return 1
    }
  fi
  [ -n "$cod_canon" ] || {
    echo "render-admin-operators: cannot canonicalize $cod_label: $cod_dir" >&2
    return 1
  }
  [ "$cod_canon" = "$cod_dir" ] || {
    echo "render-admin-operators: $cod_label is not canonical (symlink, '.' or '..'): $cod_dir -> $cod_canon" >&2
    return 1
  }
  case $cod_canon in
    /|/etc|/usr|/bin|/sbin|/lib|/lib64|/boot|/dev|/proc|/sys|/var|/run|/root|/home|/tmp)
      echo "render-admin-operators: refusing system directory as $cod_label: $cod_canon" >&2
      return 1
      ;;
  esac
  cod_cwd=$(pwd -P) || {
    echo "render-admin-operators: cannot determine the working directory" >&2
    return 1
  }
  case $cod_cwd in
    "$cod_canon"|"$cod_canon"/*)
      echo "render-admin-operators: refusing $cod_label that is the working directory or an ancestor: $cod_canon" >&2
      return 1
      ;;
  esac
  printf '%s\n' "$cod_canon"
}

hash_dir_canon=$(canonical_output_dir "hash directory" "$hash_dir") || exit 2
map_dir_canon=$(canonical_output_dir "map directory" "$map_dir") || exit 2

if [ "$hash_file_configured" -eq 0 ] && [ "$hash_dir_canon" != "$default_hash_dir" ]; then
  echo "render-admin-operators: default hash directory must be $default_hash_dir, got $hash_dir_canon" >&2
  exit 2
fi

shared_output_dir=0
if [ "$hash_dir_canon" = "$map_dir_canon" ]; then
  shared_output_dir=1
else
  case $hash_dir_canon in
    "$map_dir_canon"/*)
      echo "render-admin-operators: hash directory is inside the map directory: $hash_dir_canon" >&2
      exit 2
      ;;
  esac
  case $map_dir_canon in
    "$hash_dir_canon"/*)
      echo "render-admin-operators: map directory is inside the hash directory: $map_dir_canon" >&2
      exit 2
      ;;
  esac
fi

work=$(mktemp -d) || exit 2
trap 'rm -rf "$work"' EXIT HUP INT TERM
umask 077
: > "$work/passwords"
: > "$work/hashes"
: > "$work/map"
count=0
lineno=0
seen_roles=" "
seen_hashes=" "
seen_users=" "
previous_role=""
while IFS= read -r line || [ -n "$line" ]; do
  lineno=$((lineno + 1))
  line=$(printf '%s' "$line" | tr -d '\r')
  case $line in ''|'#'*) continue ;; esac
  rest=${line#*:}
  case $rest in
    *:*:*) ;;
    *) echo "render-admin-operators: line $lineno: want username:role:token:password" >&2; exit 2 ;;
  esac
  user=${line%%:*}
  role=${rest%%:*}; rest=${rest#*:}
  token=${rest%%:*}; password=${rest#*:}
  case $password in *:*) echo "render-admin-operators: line $lineno: too many fields" >&2; exit 2 ;; esac
  [ -n "$user" ] && [ -n "$role" ] && [ -n "$token" ] && [ -n "$password" ] || { echo "render-admin-operators: line $lineno: empty field" >&2; exit 2; }
  case $role in viewer|operator|admin) ;; *) echo "render-admin-operators: line $lineno: invalid role" >&2; exit 2 ;; esac
  [ "${#token}" -ge 32 ] || { echo "render-admin-operators: line $lineno: token shorter than 32 characters" >&2; exit 2; }
  case $token in *[!A-Za-z0-9._~+/=-]*) echo "render-admin-operators: line $lineno: unsafe token character" >&2; exit 2 ;; esac
  case $token in *replace-me*|*change-me*|*changeme*|*change_me*) echo "render-admin-operators: line $lineno: placeholder token" >&2; exit 2 ;; esac
  digest=$(printf '%s' "$token" | openssl dgst -sha256 -r | awk '{print $1}')
  case $seen_roles in
    *" $user:"*)
      previous_role=$(sed -n "s/^$user:\([^:]*\):.*/\1/p" "$work/hashes")
      [ "$previous_role" = "$role" ] || { echo "render-admin-operators: conflicting roles for $user" >&2; exit 2; }
      ;;
    *) seen_roles="$seen_roles$user:$role " ;;
  esac
  case $seen_hashes in *" $digest "*) echo "render-admin-operators: duplicate token on line $lineno" >&2; exit 2 ;; esac
  seen_hashes="$seen_hashes$digest "
  printf '%s:%s:%s\n' "$user" "$role" "$digest" >> "$work/hashes"
  # The last same-user token is current at nginx; older hashes remain valid
  # only until the operator completes the documented retirement restart.
  printf '"%s" "Bearer %s";\n' "$user" "$token" >> "$work/map"
  case $seen_users in *" $user "*) ;; *) printf '%s:%s\n' "$user" "$password" >> "$work/passwords"; seen_users="$seen_users$user " ;; esac
  count=$((count + 1))
done < "$inventory"
[ "$count" -gt 0 ] || { echo "render-admin-operators: no operators" >&2; exit 2; }
awk -F'"' '{ line[$2]=$0 } END { for (user in line) print line[user] }' "$work/map" > "$work/map.unique"
mv "$work/map.unique" "$work/map"

# Reuse the htpasswd validator for username, password and duplicate policy.
AJS_HTPASSWD_GROUP=${AJS_HTPASSWD_GROUP:-} sh "$script_dir/render-admin-htpasswd.sh" \
  "$work/passwords" "$htpasswd_file" >/dev/null
# The daemon (User=ajs Group=ajs) must traverse the hash-file directory to
# open the 0640 file; a root:root 0750 parent denies that open with EACCES.
install -d -m 0750 -o root -g "$daemon_group" "$hash_dir_canon"
# GNU install -d also applies -o/-g/-m to an existing directory. The explicit
# chown/chmod below keeps the contract independent of that behavior and fails
# with a clear message when the directory cannot be repaired.
chown root:"$daemon_group" "$hash_dir_canon" || {
  echo "render-admin-operators: cannot set root:$daemon_group on $hash_dir_canon" >&2
  exit 2
}
chmod 0750 "$hash_dir_canon" || {
  echo "render-admin-operators: cannot set mode 0750 on $hash_dir_canon" >&2
  exit 2
}
hash_mode=$(stat -c '%a' "$hash_dir_canon" 2>/dev/null) || hash_mode=
hash_owner=$(stat -c '%U' "$hash_dir_canon" 2>/dev/null) || hash_owner=
hash_group=$(stat -c '%G' "$hash_dir_canon" 2>/dev/null) || hash_group=
if [ "$hash_mode" != 750 ] || [ "$hash_owner" != root ] || [ "$hash_group" != "$daemon_group" ]; then
  echo "render-admin-operators: $hash_dir_canon is ${hash_owner:-?}:${hash_group:-?} mode ${hash_mode:-?} (expected root:$daemon_group mode 0750)" >&2
  exit 2
fi
# The bearer map is read by the root nginx master and stays root:root. A
# shared hash/map directory (same canonical path or the same device+inode)
# keeps the daemon group: the map file itself is 0600 root:root, and resetting
# the directory to root:root would break the daemon's hash-file traversal.
if [ "$shared_output_dir" -eq 0 ] && [ -d "$map_dir_canon" ]; then
  hash_devino=$(stat -c '%d:%i' "$hash_dir_canon" 2>/dev/null) || hash_devino=
  map_devino=$(stat -c '%d:%i' "$map_dir_canon" 2>/dev/null) || map_devino=
  if [ -n "$hash_devino" ] && [ "$hash_devino" = "$map_devino" ]; then
    shared_output_dir=1
  fi
fi
if [ "$shared_output_dir" -eq 0 ]; then
  install -d -m 0750 -o root -g root "$map_dir_canon"
fi
hash_tmp=$(mktemp "$hash_dir_canon/.ajs-operator-hashes.XXXXXX")
map_tmp=$(mktemp "$map_dir_canon/.ajs-operator-map.XXXXXX")
trap 'rm -rf "$work"; rm -f ${hash_tmp:-} ${map_tmp:-}' EXIT HUP INT TERM
install -m 0640 -o root -g "$daemon_group" "$work/hashes" "$hash_tmp"
install -m 0600 -o root -g root "$work/map" "$map_tmp"
mv -f "$hash_tmp" "$hash_file"
hash_tmp=
mv -f "$map_tmp" "$map_file"
map_tmp=
echo "render-admin-operators: rendered $count credentials; reload nginx and restart ajs-accountd"
