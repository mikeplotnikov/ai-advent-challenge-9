#!/usr/bin/env bash
# Day 31 demo driver: a silent screencast that explains itself in captions, same shape
# as day-18/demo.sh. Scenes: the docs that go into RAG → building the index → the
# assistant answering /help over RAG + MCP → a live change (new branch, edited doc) that
# the assistant sees at once, including the stale-index warning. Everything it changes
# in the repository is undone on exit.
#
#   ./day-31/demo.sh            # hands-off: timed pauses, just hit record
#   ./day-31/demo.sh --manual   # advances on Enter between scenes
#   PACE=1.4 ./day-31/demo.sh   # slower captions and typing (default 1.0)
#
# Run it from the repository root with a clean working tree. It makes real calls:
# DeepSeek (four questions, about a tenth of a cent) and the local Ollama (bge-m3).
set -u

MANUAL=0
[ "${1:-}" = "--manual" ] && MANUAL=1
PACE="${PACE:-1.0}"
export PACE
DEMO_BRANCH="demo/day-31-live"
EDITED="docs/conventions.md"

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

run() {
  printf '\n%s$ %s%s\n' "$CYAN" "$*" "$OFF"
  pause 1.2
  "$@"
  pause 2.4
}

command -v go >/dev/null || { echo "нужен go в PATH"; exit 1; }
[ -f .env ] || { echo "нет .env с ключом DeepSeek — запускать из корня репозитория"; exit 1; }
curl -fsS -m 3 -o /dev/null http://127.0.0.1:11434/api/version \
  || { echo "Ollama не отвечает: запустите \`ollama serve\` в другом окне"; exit 1; }
[ -z "$(git status --porcelain)" ] \
  || { echo "рабочая копия не чистая: сцена с правкой docs/ её бы испортила — закоммитьте или уберите изменения"; exit 1; }

START_BRANCH="$(git branch --show-current)"
[ -n "$START_BRANCH" ] || { echo "HEAD отсоединён — переключитесь на ветку"; exit 1; }
cleanup() {
  git checkout -q -- "$EDITED" 2>/dev/null
  if [ "$(git branch --show-current)" = "$DEMO_BRANCH" ]; then
    git switch -q "$START_BRANCH"
  fi
  git branch -q -D "$DEMO_BRANCH" 2>/dev/null
}
trap cleanup EXIT

# Built before the camera rolls: `go run` would print nothing for seconds while it compiles.
mkdir -p bin
go build -o bin/day31 ./day-31 || exit 1
go build -o bin/day31-mcp ./day-31/mcp-server || exit 1

clear 2>/dev/null || printf '\n\n'
printf '%s%s\n' "$BOLD" "День 31 — ассистент разработчика${OFF}"
printf '%sRAG по README и docs/ проекта + git через MCP + команда /help. Ответы пишет DeepSeek.%s\n' "$DIM" "$OFF"
pause 3.6

say "Документация проекта, которая идёт в RAG: README.md и папка docs/"
run ls docs
run head -n 12 docs/mcp-tools.md

say "Индекс: чанки по заголовкам Markdown, эмбеддинги bge-m3 в локальной Ollama"
run ./bin/day31 -index

say "Запускаю ассистента. MCP-сервер git он поднимает сам, по stdio"
printf '\n%s$ ./bin/day31%s\n' "$CYAN" "$OFF"
pause 1.2
./bin/day31 -demo day-31/demo-1.txt
pause 2

say "Меняю проект: новая ветка и правка в docs/ — без перезапуска индекса"
run git switch -c "$DEMO_BRANCH"
printf '\n%s$ echo "- Ветки для экспериментов называются demo/<тема>." >> %s%s\n' "$CYAN" "$EDITED" "$OFF"
pause 1.2
echo "- Ветки для экспериментов называются demo/<тема>." >> "$EDITED"
pause 1.6

say "Ассистент сразу видит новую ветку, а изменённый документ помечает как устаревший в индексе"
printf '\n%s$ ./bin/day31%s\n' "$CYAN" "$OFF"
pause 1.2
./bin/day31 -demo day-31/demo-2.txt
pause 2

say "Готово. Код: github.com/mikeplotnikov/ai-advent-challenge-9 — day-31/, internal/docrag, internal/gitmcp"
pause 3
