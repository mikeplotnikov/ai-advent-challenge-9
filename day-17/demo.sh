#!/usr/bin/env bash
# Day 17 demo driver: a screencast that explains itself in captions, so the recording
# needs no voice-over — same shape as day-07/day-08 demo.sh. The site asks for "рабочий
# код, демонстрация результата и объяснение"; nothing says the explanation is spoken.
#
#   ./day-17/demo.sh            # hands-off: timed pauses, just hit record
#   ./day-17/demo.sh --manual   # advances on Enter, for pacing it yourself
#   PACE=1.4 ./day-17/demo.sh   # slower captions (default 1.0)
#
# Run it from the repository root. It makes real calls: DeepSeek (four questions, about
# two model calls each — fractions of a cent) and cbr.ru. The remote scene talks to the
# showcase's public MCP endpoint and is skipped with a note if that endpoint is down.
set -u

MANUAL=0
[ "${1:-}" = "--manual" ] && MANUAL=1
PACE="${PACE:-1.0}"
REMOTE="${REMOTE:-https://challeng.mikeproject.dev/api/day17-mcp}"

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

# Built once before the camera rolls: `go run` would print nothing for seconds while it
# compiles, and the server is a separate program on purpose — the agent starts it.
mkdir -p bin
go build -o bin/day17 ./day-17 || exit 1
go build -o bin/day17-mcp-server ./day-17/mcp-server || exit 1
go build -o bin/day16 ./day-16 || exit 1
./bin/day17 -dump > bin/day17-dump.json || exit 1

clear 2>/dev/null || printf '\n\n'
printf '%s%s\n' "$BOLD" "День 17 — Первый инструмент MCP${OFF}"
printf '%sСвой MCP-сервер вокруг API курсов ЦБ РФ и свой агент, который сам решает,%s\n' "$DIM" "$OFF"
printf '%sкогда и какой инструмент вызвать, и строит ответ на его результате.%s\n' "$DIM" "$OFF"
pause 3.6

say "Сервер — отдельная программа. Смотрим, что он регистрирует: клиентом дня 16, по stdio"
run ./bin/day16 -command ./bin/day17-mcp-server
note "Два инструмента: курсы на дату и пересчёт суммы. Обязательные параметры сервер объявил сам."

say "Описание входных параметров — JSON Schema из tools/list, её и увидит модель"
run jq '.tools[] | select(.name=="convert_currency") | .inputSchema' bin/day17-dump.json
note "Каждый параметр описан; схему сервер вывел из Go-структуры, агент ничего о ней не знает заранее."

say "Агент: tools/list → схемы уходят в DeepSeek → модель САМА выбирает вызов → tools/call → ответ"
run ./bin/day17 -command ./bin/day17-mcp-server "Сколько рублей стоили 250 долларов США 1 сентября 2026 года по курсу ЦБ?"
note "Модель выбрала convert_currency и заполнила аргументы. Число в ответе — из результата инструмента."

say "Воскресенье: курс в этот день ЦБ не устанавливал — инструмент говорит об этом, модель объясняет"
run ./bin/day17 -command ./bin/day17-mcp-server "Какой курс евро был по ЦБ в воскресенье 20 сентября 2026?"

say "Контроль: тот же вопрос про доллары, но без инструментов — MCP не поднимается вовсе"
run ./bin/day17 -no-tools "Сколько рублей стоили 250 долларов США 1 сентября 2026 года по курсу ЦБ?"
note "Без инструментов у модели нет источника курса на дату. Сравните с ответом выше: там число пришло из tools/call."

say "Вопрос не про курсы — инструменты есть, но модель их не зовёт"
run ./bin/day17 -command ./bin/day17-mcp-server "Что такое кросс-курс?"

say "Тот же сервер на удалённой машине: витрина, Streamable HTTP, публичный адрес"
if curl -fsS -m 10 -X POST -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"ping"}' "$REMOTE" >/dev/null 2>&1; then
  run ./bin/day17 -endpoint "$REMOTE" "Какой официальный курс юаня и тенге установил ЦБ на 1 сентября 2026?"
  note "Go-агент на официальном SDK и сервер витрины на JS говорят на одном протоколе."
else
  note "Удалённый сервер сейчас не отвечает — сцена пропущена."
fi

if [ -f day-17/RESULTS.md ]; then
  say "Замер надёжности: 30 прогонов на группу — как часто модель зовёт нужный инструмент и использует результат"
  run sed -n '/^| Группа/,/^$/p' day-17/RESULTS.md
  note "Полная таблица, контрольная рука и цена — в day-17/RESULTS.md."
fi

printf '\n%sКод: github.com/mikeplotnikov/ai-advent-challenge-9/tree/main/day-17%s\n' "$BOLD" "$OFF"
printf '%sВитрина: challeng.mikeproject.dev/day-17/%s\n' "$BOLD" "$OFF"
pause 3
