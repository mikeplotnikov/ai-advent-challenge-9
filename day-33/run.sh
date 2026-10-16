#!/usr/bin/env bash
# Starts the day-33 support chat and a Cloudflare Quick Tunnel, then prints the public link.
# Stop with Ctrl+C: both processes stop and the public URL dies with them.
# http2 (TCP) instead of the default QUIC: UDP to Cloudflare is dropped on this network.
set -euo pipefail
cd "$(dirname "$0")/.."
curl -fsS http://127.0.0.1:11434/api/version >/dev/null || { echo "Ollama is not running: ollama serve"; exit 1; }
go build -o bin/day33 ./day-33
go build -o bin/day33-mcp ./day-33/mcp-server
[ -f day-33/index/docs.json ] || bin/day33 -index
bin/day33 & SERVER=$!
LOG=$(mktemp)
cloudflared tunnel --no-autoupdate --protocol http2 --url http://127.0.0.1:8033 2>"$LOG" & TUNNEL=$!
trap 'kill $SERVER $TUNNEL 2>/dev/null; rm -f "$LOG"' EXIT
URL=""
for _ in $(seq 60); do
  URL=$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' "$LOG" | head -1 || true)
  [ -n "$URL" ] && break
  sleep 1
done
[ -n "$URL" ] || { echo "Tunnel did not start:"; cat "$LOG"; exit 1; }
echo "Tunnel $URL is registering (usually under a minute)..."
for _ in $(seq 90); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "$URL/api/users")" = 200 ] && break
  sleep 2
done
echo
echo "Support chat: $URL"
echo
wait $SERVER
