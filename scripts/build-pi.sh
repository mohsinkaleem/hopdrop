#!/usr/bin/env bash
# Cross-compile `hop` for a Raspberry Pi (64-bit Pi OS) and optionally deploy it.
#   scripts/build-pi.sh                 build only (dist/hop-linux-arm64)
#   scripts/build-pi.sh pi@raspberrypi  build, copy, install the systemd service, restart
# Needs only Go. For 32-bit Pi OS use GOARCH=arm GOARM=7.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=dist/hop-linux-${GOARCH:-arm64}
GOOS=linux GOARCH="${GOARCH:-arm64}" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$BIN" ./cmd/hop
echo "built $BIN ($(du -h "$BIN" | cut -f1))"

HOST="${1:-}"
[ -z "$HOST" ] && exit 0

scp "$BIN" deploy/hopdrop.service "$HOST:/tmp/"
ssh "$HOST" "set -e
  sudo install -m 755 /tmp/$(basename "$BIN") /usr/local/bin/hop
  sed \"s/__USER__/\$USER/\" /tmp/hopdrop.service | sudo tee /etc/systemd/system/hopdrop.service >/dev/null
  sudo systemctl daemon-reload
  sudo systemctl enable hopdrop
  sudo systemctl restart hopdrop
  sleep 1; systemctl --no-pager --lines=5 status hopdrop"
