#!/usr/bin/env bash
# Day 8 demo driver: a screencast that explains itself in captions, so the recording
# needs no voice-over. Same shape as day-07/demo.sh — the site asks for "рабочий код,
# демонстрация результата и объяснение", and nothing says the explanation has to be
# spoken.
#
#   ./day-08/demo.sh            # hands-off: timed pauses, just hit record
#   ./day-08/demo.sh --manual   # advances on Enter, for pacing it yourself
#   PACE=1.4 ./day-08/demo.sh   # slower captions (default 1.0)
#
# Run it from the repository root. It makes about ten real calls to DeepSeek (roughly
# a third of a cent) and touches only its own sessions, ".sessions/demo8*.json".
#
# The two runs it does NOT repeat live are the 25-turn growth curve (three minutes of
# waiting on camera) and the 1.5M-token request that the provider refuses (a 5 MB
# upload and 23 seconds of nothing). Both are shown from the files the real runs
# wrote — day-08/growth-run.txt and day-08/window-run.txt.
set -u

MANUAL=0
[ "${1:-}" = "--manual" ] && MANUAL=1
PACE="${PACE:-1.0}"

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

note() { printf '%s  %s%s\n' "$DIM" "$*" "$OFF"; pause 1.6; }

run() {
  printf '\n%s$ %s%s\n' "$CYAN" "$*" "$OFF"
  pause 1.2
  "$@"
  pause 2.2
}

command -v go >/dev/null || { echo "нужен go в PATH"; exit 1; }
[ -f .env ] || { echo "нет .env с ключом DeepSeek — запускать из корня репозитория"; exit 1; }
[ -f day-08/growth-run.txt ] || { echo "нет day-08/growth-run.txt — сначала прогон роста"; exit 1; }

go build -o day06 ./day-06 || exit 1

clear 2>/dev/null || printf '\n\n'
printf '%s%s\n' "$BOLD" "День 8 — Работа с токенами${OFF}"
printf '%sАгент считает, сколько весит запрос ДО отправки, ведёт расход беседы%s\n' "$DIM" "$OFF"
printf '%sи знает, что делать, когда контекст перестал помещаться.%s\n' "$DIM" "$OFF"
pause 3.4

say "Ход первый. Агент печатает вес запроса ДО того, как за него заплатит"
run ./day06 -tokens -session demo8 -forget "Меня зовут Михаил. Запомни: мой любимый номер 47."
note "«до отправки» — наш счётчик; «оценка входа N (+d)» в строке расхода — он же рядом с фактом от API."
note "Последняя строка — расход всей беседы, а не одного хода."

say "Ход второй в той же беседе. История появилась — и она теперь часть КАЖДОГО запроса"
run ./day06 -tokens -session demo8 "Назови моё имя и число."
note "Вопрос — десятки токенов, история — сотни. Платим за неё заново на каждом ходу."

say "Расход беседы переживает перезапуск процесса: он лежит рядом с историей"
run ./day06 -session demo8 -totals
run grep -o '"spend":{[^}]*}' .sessions/demo8.json

say "Так это выглядит на длинной дистанции — 25 ходов одной беседы"
run sed -n '1,4p;26,29p' day-08/growth-run.txt
note "Вход вырос в 71 раз, а сумма входа за беседу — 78 700 токенов при последнем запросе в 6 406."
note "Цена хода при этом почти не выросла: растущий префикс уходит в кэш поставщика."

# The two ceiling demos use a deliberately long question so that they fire on the
# numbers rather than on the model's mood: the refusal has to happen before any call,
# and the trim has to drop an exchange that is definitely there. Both ceilings were
# checked against the real weights (day-08/RESULTS.md), not guessed.
LONG="Опиши подробно, что влияет на размер контекста в диалоге с языковой моделью. "
LONG="$LONG$LONG$LONG$LONG$LONG$LONG"

say "Поломка 1. Свой потолок и политика «отказать»: вызова нет, денег нет"
run ./day06 -session demo8-refuse -forget -max-context 120 -on-overflow refuse "$LONG"
note "Отказ печатает всю раскладку: система + история + вопрос + формат против потолка."
note "Модель не вызывалась вообще — потолок проверяется до отправки, поэтому отказ бесплатный."

say "Поломка 2. Та же ситуация, политика «обрезать». Ход первый помещается"
run ./day06 -tokens -session demo8-trim -forget -max-context 400 -on-overflow trim "$LONG"

say "Ход второй уже не помещается — агент отвечает, но забывает начало"
run ./day06 -tokens -session demo8-trim -max-context 400 -on-overflow trim "$LONG И приведи пример."
note "«отброшено обменов» в строке расхода — это и есть забывание, купленное ради ответа."
note "На замере обрезка обнулила кэш: сдвиг префикса — полная цена входа, см. day-08/RESULTS.md."

say "Поломка 3. Потолок генерации: ответ обрывается на полуслове и перестаёт быть JSON"
run ./day06 -session demo8-cut -forget -json -max-tokens 16 "Верни один объект JSON с полями city, country и population про Саратов."
note "finish_reason=length. API отработал штатно — сломался разбор ответа, а не вызов."

say "Поломка 4. Настоящее окно модели. Это записанный прогон: 1,5 млн токенов в одном сообщении"
run cat day-08/window-run.txt
note "Контрольная ступень на 50 000 прошла — значит отказ на верхней ступени про размер, а не про стенд."
note "Поставщик сам называет предел: 1 048 576 токенов. Ответ 400, ни одного токена не потрачено."

say "Итог: три числа задания — запрос, история, ответ — и то, что с ними делает агент"
run ./day06 -session demo8 -totals
printf '\n%sКод: internal/agent/tokens.go · замеры: day-08/RESULTS.md%s\n' "$DIM" "$OFF"
