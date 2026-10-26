#!/bin/sh
# Render all three per-operator credentials from one root-only inventory.
# Inventory: username:viewer|operator|admin:token:password (colon-free fields).
# Tokens are random >=32-character secrets, not the operator password.
#
# Outputs (atomic, never printed):
#   /etc/nginx/ajs-admin.htpasswd                  root:<nginx group> 0640
#   /etc/nginx/snippets/ajs-admin-tokens.map       root:root 0600
#   /etc/ajs/admin-operators.hashes                root:<daemon group> 0640
# The daemon reads hashes only; nginx reads its map at reload. Restart both
# after editing roles. Token rotation may use a second same-name/same-role row.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
inventory=${1:-/etc/ajs/admin-operators}
hash_file=${AJS_OPERATOR_HASH_FILE:-/etc/ajs/admin-operators.hashes}
map_file=${AJS_OPERATOR_MAP_FILE:-/etc/nginx/snippets/ajs-admin-tokens.map}
htpasswd_file=${AJS_HTPASSWD_FILE:-/etc/nginx/ajs-admin.htpasswd}
daemon_group=${AJS_DAEMON_GROUP:-ajs}

[ "$(id -u)" -eq 0 ] || { echo "render-admin-operators: must run as root" >&2; exit 2; }
[ -r "$inventory" ] || { echo "render-admin-operators: inventory not readable" >&2; exit 2; }
case ${inventory##*/} in
  *.example) echo "render-admin-operators: refusing an example inventory" >&2; exit 2 ;;
esac
if command -v getent >/dev/null 2>&1; then
  getent group "$daemon_group" >/dev/null 2>&1 || { echo "render-admin-operators: daemon group does not exist: $daemon_group" >&2; exit 2; }
fi
command -v openssl >/dev/null 2>&1 || { echo "render-admin-operators: openssl is required" >&2; exit 2; }

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
install -d -m 0750 -o root -g root "$(dirname "$hash_file")" "$(dirname "$map_file")"
hash_tmp=$(mktemp "$(dirname "$hash_file")/.ajs-operator-hashes.XXXXXX")
map_tmp=$(mktemp "$(dirname "$map_file")/.ajs-operator-map.XXXXXX")
trap 'rm -rf "$work"; rm -f ${hash_tmp:-} ${map_tmp:-}' EXIT HUP INT TERM
install -m 0640 -o root -g "$daemon_group" "$work/hashes" "$hash_tmp"
install -m 0600 -o root -g root "$work/map" "$map_tmp"
mv -f "$hash_tmp" "$hash_file"
hash_tmp=
mv -f "$map_tmp" "$map_file"
map_tmp=
echo "render-admin-operators: rendered $count credentials; reload nginx and restart ajs-accountd"
