#!/usr/bin/env bash
# Day 18 demo driver: a screencast that explains itself in captions, same shape as
# day-17/demo.sh. Scenes: the model creates a watch → the resident agent polls and issues
# digests on its own → a question about the aggregate → the watch is stopped. The 24/7
# part lives in GitHub Actions; the script ends by pointing at it and at the showcase.
#
#   ./day-18/demo.sh            # hands-off: timed pauses, just hit record
#   ./day-18/demo.sh --manual   # advances on Enter, for pacing it yourself
#   PACE=1.4 ./day-18/demo.sh   # slower captions (default 1.0)
#   DAEMON_SECONDS=200 ./day-18/demo.sh   # how long the resident scene runs (default 200)
#
# Run it from the repository root. It makes real calls: DeepSeek (a few questions and two
# or three digests — fractions of a cent) and cbr.ru, which some VPN exits cannot reach.
# It uses its own state directory and wipes it first, so every recording starts clean.
set -u

MANUAL=0
[ "${1:-}" = "--manual" ] && MANUAL=1
PACE="${PACE:-1.0}"
DAEMON_SECONDS="${DAEMON_SECONDS:-200}"
STATE="bin/day18-demo"
STORE="$STATE/store.json"
DIGESTS="$STATE/digests.json"

BOLD=$'\033[1m'; DIM=$'\033[2m'; CYAN=$'\033[36m'; ORANGE=$'\033[38;5;209m'; OFF=$'\033[0m'

pause() {
  if [ "$MANUAL" = "1" ]; then
    printf '%s' "$DIM"; read -r -p "  ⏎ " _; printf '%s' "$OFF"
  else
    sleep "$(awk -v s="$1" -v p="$PACE" 'BEGIN{printf "%.2f", s*p}')"
  fi
}

say() {
  printf '\n%s%s▌ %s%s\n' "$BOLD" "$ORANGE" "$*" "$OFF"
  pause 2.6
}

note() { printf '%s  %s%s\n' "$DIM" "$*" "$OFF"; pause 1.8; }

run() {
  printf '\n%s$ %s%s\n' "$CYAN" "$*" "$OFF"
  pause 1.2
  "$@"
  pause 2.4
}

command -v go >/dev/null || { echo "нужен go в PATH"; exit 1; }
command -v jq >/dev/null || { echo "нужен jq в PATH (brew install jq)"; exit 1; }
[ -f .env ] || { echo "нет .env с ключом DeepSeek — запускать из корня репозитория"; exit 1; }
curl -fsS -m 15 -o /dev/null -A 'ai-advent-day-18-demo' https://www.cbr.ru/scripts/XML_daily.asp \
  || { echo "cbr.ru не отвечает с этой машины (VPN?) — сцены с ЦБ не получатся"; exit 1; }

# Built once before the camera rolls: `go run` would print nothing for seconds while it
# compiles, and the server is a separate program — the agent starts it.
mkdir -p bin
go build -o bin/day18 ./day-18 || exit 1
go build -o bin/day18-mcp ./day-18/mcp-server || exit 1
rm -rf "$STATE"
SERVER="bin/day18-mcp -store $STORE"

clear 2>/dev/null || printf '\n\n'
printf '%s%s\n' "$BOLD" "День 18 — Планировщик и фоновые задачи${OFF}"
printf '%sMCP-инструмент, который выполняется по расписанию, хранит данные в JSON и отдаёт%s\n' "$DIM" "$OFF"
printf '%sагрегат, — и агент, который работает 24/7 и периодически выдаёт сводку.%s\n' "$DIM" "$OFF"
pause 3.6

say "Прошу агента следить за курсами. Модель сама выбирает create_watch и заполняет аргументы"
run ./bin/day18 -command "$SERVER" "Следи за курсами доллара, евро и юаня — проверяй ЦБ каждую минуту"
note "Наблюдение создано, первый опрос ЦБ сделан сразу — он в результате инструмента."

say "Хранилище — обычный JSON: наблюдение и его опросы"
run jq '.watches[] | {id, codes, every_minutes, status, polls: (.polls | length)}' "$STORE"

say "Резидентный агент: сервер САМ опрашивает ЦБ раз в минуту, агент раз в 2 минуты выпускает сводку"
note "Строки [планировщик …] пишет сервер — модель в опросах не участвует. Сводку пишет модель через get_watch_summary."
printf '\n%s$ ./bin/day18 -daemon -digest-every 2m -window 1h -command "%s" -digests %s%s\n' "$CYAN" "$SERVER" "$DIGESTS" "$OFF"
pause 1.2
./bin/day18 -daemon -digest-every 2m -window 1h -command "$SERVER" -digests "$DIGESTS" &
DAEMON=$!
sleep "$DAEMON_SECONDS"
kill -INT "$DAEMON" 2>/dev/null
wait "$DAEMON" 2>/dev/null
pause 2.4
note "Демон остановлен сигналом. Каждая сводка записана в digests.json: текст, агрегат, токены, цена."

say "Агрегат — это то, что возвращает инструмент: опросы, публикации ЦБ, изменение курса"
run jq '.[-1].summary.watches[0] | {polls, publications, currencies: [.currencies[] | {code, last, change, change_pct}]}' "$DIGESTS"

say "Спрашиваю про последний час — тот же инструмент агрегата, ответ из данных"
run ./bin/day18 -command "$SERVER" "Что с курсами за последний час?"

say "Останавливаю наблюдение. Данные остаются — по остановленному наблюдению агрегат всё ещё считается"
run ./bin/day18 -command "$SERVER" "Останови наблюдение"

say "Настоящие 24/7 — GitHub Actions раз в час: опросы, сводка раз в 3 часа, состояние в ветке day-18-state"
if command -v gh >/dev/null; then
  run gh run list -R mikeplotnikov/ai-advent-challenge-9 -w day-18-agent.yml -L 8
fi
note "Живое состояние агента — на странице витрины challeng.mikeproject.dev/day-18/."

printf '\n%sКод: github.com/mikeplotnikov/ai-advent-challenge-9/tree/main/day-18%s\n' "$BOLD" "$OFF"
printf '%sВитрина: challeng.mikeproject.dev/day-18/%s\n' "$BOLD" "$OFF"
pause 3
