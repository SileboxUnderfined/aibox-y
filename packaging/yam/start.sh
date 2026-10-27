#!/bin/sh
set -eu
umask 077
package=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
state=${AIBOX_DIR:-/data/opt/aibox}
mkdir -p "$state"
chmod 0700 "$state"
[ -f "$state/config.json" ] || cp "$package/config.example.json" "$state/config.json"
if [ -f "$state/secrets.env" ]; then
 set -a
 . "$state/secrets.env"
 set +a
fi
export SSL_CERT_FILE=${SSL_CERT_FILE:-/system/vendor/quasar/ca-certificates.crt}
export GOMAXPROCS=${GOMAXPROCS:-2}
export GOMEMLIMIT=${GOMEMLIMIT:-48MiB}
cd "$state"
# exec: yamd stops its direct child, not a shell's descendants.
exec "$package/bin/aibox" -config "$state/config.json" "$@"
