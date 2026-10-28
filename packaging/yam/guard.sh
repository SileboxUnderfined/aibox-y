#!/bin/sh
# AIBox guard. While Alice points at the local proxy, a dead proxy means a
# mute speaker, so this loop restarts AIBox when it stops answering.
# Installed by "aiboxctl.sh apply" as /etc/init.d/S99zzaibox-guard.
# setup.sh records where the plugin host keeps the package (legacy YAM or Luna).
package=$(cat /data/opt/aibox/package 2>/dev/null || echo /data/quazar/yam/data/plugins/aibox)
health=http://127.0.0.1:8443/healthz
yam=http://127.0.0.1:8099/api/plugins/aibox
cfg=/system/vendor/quasar/quasar.cfg
pidfile=/tmp/aibox-guard.pid

loop() {
 fails=0
 while :; do
  sleep 15
  # Nothing to guard once Alice talks to Yandex directly again.
  grep -q '127.0.0.1:8443' "$cfg" || { fails=0; continue; }
  if wget -q -T 5 -O /dev/null "$health"; then fails=0; continue; fi
  fails=$((fails+1))
  [ "$fails" -lt 2 ] && continue
  logger -t aibox-guard "proxy not answering, restarting"
  wget -q -T 5 -O /dev/null --post-data='' "$yam/service/start" 2>/dev/null
  sleep 20
  if ! wget -q -T 5 -O /dev/null "$health"; then
   # YAM itself is down or stuck: run the proxy directly.
   logger -t aibox-guard "YAM did not start it, starting directly"
   start-stop-daemon -S -b -x /bin/sh -- "$package/start.sh"
  fi
  fails=0
 done
}

case "${1:-start}" in
 start)
  [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null && exit 0
  loop </dev/null >/dev/null 2>&1 &
  echo $! > "$pidfile"
  ;;
 stop)
  [ -f "$pidfile" ] && kill "$(cat "$pidfile")" 2>/dev/null
  rm -f "$pidfile"
  ;;
 run) loop ;;
esac
