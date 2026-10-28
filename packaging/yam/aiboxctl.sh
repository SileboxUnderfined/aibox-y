#!/bin/sh
# Run on the speaker over SSH. Switches Alice between the AIBox proxy and the
# original Yandex endpoint. Installing the plugin alone never touches firmware.
set -eu
package=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
state=${AIBOX_DIR:-/data/opt/aibox}
cfg=/system/vendor/quasar/quasar.cfg
health=http://127.0.0.1:8443/healthz
action=${1:?usage: aiboxctl.sh status|apply|rollback}
case "$action" in
 status)
  grep -o '"uniProxyUrl" *: *"[^"]*"' "$cfg"
  wget -q -O - "$health" && echo || echo 'proxy is not running'
  exit 0
  ;;
 apply)
  wget -q -O /dev/null "$health" || { echo 'proxy is not running, refusing to switch' >&2; exit 1; }
  [ -f "$state/quasar.cfg.orig" ] || cp "$cfg" "$state/quasar.cfg.orig"
  ;;
 rollback) ;;
 *) echo 'usage: aiboxctl.sh status|apply|rollback' >&2; exit 2 ;;
esac
# The firmware root is read-only; always try to put it back. It stays busy
# while something holds a file open for writing, so a failed remount only warns.
readonly_root() { mount -o remount,ro / 2>/dev/null || echo 'warning: / is busy and stays writable until reboot' >&2; }
mount -o remount,rw /
trap 'sync; readonly_root' EXIT HUP INT TERM
"$package/bin/aibox" -speaker-endpoint "$action" -speaker-config "$cfg" -speaker-backup "$state/endpoint-backup.json"
guard=/etc/init.d/S99zzaibox-guard
if [ "$action" = apply ]; then
 cp "$package/guard.sh" "$guard" && chmod 755 "$guard"
else
 [ -f "$guard" ] && sh "$guard" stop
 rm -f "$guard"
fi
sync
readonly_root
trap - EXIT HUP INT TERM
/system/vendor/quasar/q restart >/dev/null 2>&1 || true
[ "$action" = apply ] && sh /etc/init.d/S99zzaibox-guard start
echo "done: $action, quasar restarted"
