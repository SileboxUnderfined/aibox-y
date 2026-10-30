#!/bin/sh
# Run on the speaker from an unpacked aibox-pkg; install.sh does this over SSH.
# Finds the plugin host (legacy YAM or Luna), replaces the plugin files, keeps
# settings in /data/opt/aibox and starts the service. Alice is switched to
# AIBox only with --chat, or stays switched if she already was.
set -eu
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
state=${AIBOX_DIR:-/data/opt/aibox}
api=http://127.0.0.1:8099/api
ip=${AIBOX_HOST:-<speaker-ip>}
health=http://127.0.0.1:8443/healthz
cfg=/system/vendor/quasar/quasar.cfg
guard=/etc/init.d/S99zzaibox-guard
chat=0
for arg; do
 case "$arg" in
  --chat) chat=1 ;;
  *) echo "неизвестный параметр: $arg" >&2; exit 2 ;;
 esac
done
say() { echo "aibox: $*"; }
die() { echo "aibox: $*" >&2; exit 1; }
post() { wget -q -T 10 -O /dev/null --post-data='' "$api/plugins/aibox/$1"; }

# Luna answers /api/plugins/sources, legacy yamd does not; both serve /api/plugins.
if [ -n "${AIBOX_PLUGINS:-}" ]; then
 plugins=$AIBOX_PLUGINS host=custom
elif wget -q -T 5 -O /dev/null "$api/plugins/sources"; then
 plugins=/data/quazar/luna/data/plugins host=Luna
elif wget -q -T 5 -O /dev/null "$api/plugins"; then
 plugins=/data/quazar/yam/data/plugins host='YAM 0.1'
else
 die "на :8099 никто не отвечает. Поставь и запусти Luna (yam install yam/luna) или старый YAM 0.1 и повтори."
fi
[ -d "$plugins" ] || die "$host запущен, но нет папки $plugins; укажи папку плагинов в AIBOX_PLUGINS"
target=$plugins/aibox
say "менеджер плагинов: $host, папка: $target"

# /data is small: the old copy is replaced, not kept, so count it as free space.
need=$(du -sk "$src" | cut -f1)
free=$(df -k "$plugins" | awk 'NR==2 {print $4}')
old=0
[ -d "$target" ] && old=$(du -sk "$target" | cut -f1)
[ $((free + old)) -gt $((need + 1024)) ] || die "мало места на /data: нужно ${need} КБ, есть $((free + old)) КБ"

applied=0
grep -q '127.0.0.1:8443' "$cfg" && applied=1
# The guard would start the old binary mid-swap.
[ -f "$guard" ] && sh "$guard" stop
post stop 2>/dev/null || true
# The host stops it asynchronously; an old process still answering would pass
# the health check below.
i=0
while [ "$i" -lt 10 ] && wget -q -T 2 -O /dev/null "$health"; do
 i=$((i + 1))
 sleep 1
done
rm -rf "$target"
mkdir -p "$target"
cp -a "$src/." "$target/"
chmod 755 "$target"/*.sh "$target/bin/aibox"
mkdir -p "$state"
chmod 0700 "$state"
echo "$target" > "$state/package"
say "файлы на месте ($need КБ)"

if [ "$host" = Luna ] && command -v curl >/dev/null; then
 settings=$(curl -fsS "$api/settings" || true)
 case "$settings" in
  *'"allow_plugin_binaries":false'*)
   echo "$settings" | sed 's/"allow_plugin_binaries":false/"allow_plugin_binaries":true/' |
    curl -fsS -X PUT -H 'Content-Type: application/json' --data-binary @- "$api/settings" >/dev/null &&
    say 'в Luna включена галочка «Разрешить бинарники плагинов»'
   ;;
 esac
fi
[ -f "$state/config.json" ] || cp "$target/config.example.json" "$state/config.json"
[ "$chat" = 1 ] && sed -i 's/"mode": *"observe"/"mode": "chat"/' "$state/config.json"
post service/enable || true
i=0
until wget -q -T 3 -O /dev/null "$health"; do
 i=$((i + 1))
 if [ "$i" -ge 15 ]; then
  [ "$applied" = 1 ] && [ -f "$guard" ] && sh "$guard" start
  die "AIBox не запустился. Включи «Разрешить бинарники плагинов» в настройках $host (http://$ip:8099) и повтори. Лог: logread | grep -i aibox"
 fi
 sleep 2
done
say "AIBox запущен"

if [ "$applied" = 1 ]; then
 [ -f "$guard" ] && sh "$guard" start
 say 'Алиса уже работает через AIBox'
elif [ "$chat" = 1 ]; then
 sh "$target/aiboxctl.sh" apply
else
 say "Алиса пока ходит в Яндекс напрямую. Переключить: запусти установщик с --chat"
fi
grep -q '"mode": *"chat"' "$state/config.json" || say "чаты выключены (режим observe); включить: запусти установщик с --chat"
say "веб-панель: http://$ip:8100"
exit 0
