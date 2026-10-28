#!/bin/sh
# AIBox installer. Runs on your computer (macOS or Linux) and installs or
# updates the plugin on the speaker over SSH:
#
#   curl -fsSL https://raw.githubusercontent.com/SileboxUnderfined/aibox-y/main/install.sh | sh -s -- 192.168.88.202
#
# The speaker-side work is done by setup.sh from the package itself.
set -eu
repo=SileboxUnderfined/aibox-y
port=${AIBOX_SSH_PORT:-2222}
version=${AIBOX_VERSION:-}
package=
host=${AIBOX_HOST:-}
flags=

usage() {
 cat <<'USAGE'
Установка и обновление AIBox на Яндекс Станции по SSH.

  install.sh [параметры] <IP колонки>

  --chat           включить чаты и переключить Алису на AIBox
  --version vX.Y.Z поставить конкретный релиз (по умолчанию последний)
  --package PATH   поставить свою сборку: папку dist/aibox-pkg или архив .tar.gz
  --port N         SSH-порт колонки (по умолчанию 2222)

Настройки, ключи и чаты в /data/opt/aibox при обновлении сохраняются.
Откат на колонке: sh <папка плагина>/aiboxctl.sh rollback
USAGE
}
die() { echo "aibox: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
 case "$1" in
  --chat) flags="$flags --chat" ;;
  --version) [ $# -ge 2 ] || die "--version: нужен тег"; version=$2; shift ;;
  --package) [ $# -ge 2 ] || die "--package: нужен путь"; package=$2; shift ;;
  --port) [ $# -ge 2 ] || die "--port: нужен номер"; port=$2; shift ;;
  -h|--help) usage; exit 0 ;;
  -*) usage >&2; exit 2 ;;
  *) host=$1 ;;
 esac
 shift
done
if [ -z "$host" ]; then
 [ -r /dev/tty ] || die "укажи IP колонки: install.sh 192.168.88.202"
 printf 'IP колонки: ' >/dev/tty
 read -r host </dev/tty
 [ -n "$host" ] || die "IP не указан"
fi
case "$host" in *@*) target=$host ;; *) target=root@$host ;; esac
ip=${host#*@}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
archive=$work/aibox.tar.gz

if [ -n "$package" ]; then
 if [ -d "$package" ]; then
  [ -f "$package/plugin.toml" ] || die "$package не похожа на aibox-pkg (нет plugin.toml)"
  # The speaker side expects the folder to be called aibox-pkg.
  mkdir "$work/src"
  cp -R "$package" "$work/src/aibox-pkg"
  tar czf "$archive" -C "$work/src" aibox-pkg
 else
  cp "$package" "$archive"
 fi
 echo "aibox: ставлю свою сборку $package"
else
 command -v curl >/dev/null || die "нужен curl"
 if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
   sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$version" ] || die "на GitHub ещё нет релизов; собери сам (make package) и запусти с --package dist/aibox-pkg"
 fi
 base=https://github.com/$repo/releases/download/$version
 name=aibox-$version-linux-arm64.tar.gz
 echo "aibox: скачиваю $name"
 curl -fL --progress-bar -o "$work/$name" "$base/$name" || die "не удалось скачать $base/$name"
 curl -fsSL -o "$work/SHA256SUMS" "$base/SHA256SUMS" || die "не удалось скачать SHA256SUMS"
 if command -v sha256sum >/dev/null; then sum=sha256sum; else sum='shasum -a 256'; fi
 (cd "$work" && grep " $name\$" SHA256SUMS | $sum -c - >/dev/null) || die "контрольная сумма $name не совпала"
 mv "$work/$name" "$archive"
fi

echo "aibox: подключаюсь к $target (порт $port)"
# One connection: the archive goes in on stdin and unpacks to /tmp (RAM, not /data).
ssh -p "$port" "$target" "cd /tmp && rm -rf aibox-pkg && gzip -dc | tar xf - &&
 AIBOX_HOST='$ip' sh aibox-pkg/setup.sh$flags; rc=\$?; rm -rf /tmp/aibox-pkg; exit \$rc" <"$archive"
