#!/usr/bin/env bash
# Day 7 demo driver: a screencast that explains itself in captions, so the recording
# needs no voice-over. The site asks a submission to be "рабочий код, демонстрация
# результата и объяснение"; nothing in the rules says the explanation has to be
# spoken, so it is printed on screen instead.
#
#   ./day-07/demo.sh            # hands-off: timed pauses, just hit record
#   ./day-07/demo.sh --manual   # advances on Enter, for pacing it yourself
#   PACE=1.4 ./day-07/demo.sh   # slower captions (default 1.0)
#
# Run it from the repository root. It makes real calls to DeepSeek (about six, well
# under a cent) and touches only its own session, ".sessions/demo.json".
set -u

MANUAL=0
[ "${1:-}" = "--manual" ] && MANUAL=1
PACE="${PACE:-1.0}"

BOLD=$'\033[1m'; DIM=$'\033[2m'; CYAN=$'\033[36m'; GREEN=$'\033[32m'; ORANGE=$'\033[38;5;209m'; OFF=$'\033[0m'

pause() { # pause <seconds>
  if [ "$MANUAL" = "1" ]; then
    printf '%s' "$DIM"; read -r -p "  ⏎ " _; printf '%s' "$OFF"
  else
    sleep "$(awk -v s="$1" -v p="$PACE" 'BEGIN{printf "%.2f", s*p}')"
  fi
}

say() { # say <caption...> — the explanation the voice would have carried
  printf '\n%s%s▌ %s%s\n' "$BOLD" "$ORANGE" "$*" "$OFF"
  pause 2.6
}

note() { printf '%s  %s%s\n' "$DIM" "$*" "$OFF"; pause 1.6; }

run() { # run <command...> — show it, then actually run it
  printf '\n%s$ %s%s\n' "$CYAN" "$*" "$OFF"
  pause 1.2
  "$@"
  pause 2.2
}

command -v go >/dev/null || { echo "нужен go в PATH"; exit 1; }
[ -f .env ] || { echo "нет .env с ключом DeepSeek — запускать из корня репозитория"; exit 1; }

clear 2>/dev/null || printf '\n\n'
printf '%s%s\n' "$BOLD" "День 7 — Сохранение контекста${OFF}"
printf '%sАгент хранит историю диалога в JSON, при перезапуске поднимает её обратно%s\n' "$DIM" "$OFF"
printf '%sи продолжает разговор так, как будто его не выключали.%s\n' "$DIM" "$OFF"
pause 3.4

say "Стираем следы прошлых прогонов, чтобы начать с чистого листа"
run ./day06 -session demo -forget "Меня зовут Михаил. Запомни: мой любимый номер 47."
note "Строка «память: …» печатается при каждом старте — она говорит, что агент нашёл на диске."

say "Вот что агент записал. Это и есть вся память — обычный JSON"
run cat .sessions/demo.json
note "Системный промпт и модель лежат рядом с сообщениями: история под другой ролью — уже другая история."

say "Процесс завершился. Запускаем ЗАНОВО — это другой процесс, он ничего не помнит сам"
run ./day06 -session demo "Какой у меня любимый номер и как меня зовут?"
note "Агент ответил из истории, которую поднял с диска. Ходов в контексте — уже два."

say "Проверка, которая умеет провалиться: тот же вопрос с выключенной памятью"
run ./day06 -session demo -no-memory "Какой у меня любимый номер и как меня зовут?"
note "Без памяти агент не знает ответа. Если бы знал — сломан был бы измеритель, а не агент."

say "В диалоге /reset стирает и контекст, и файл — «conversation recreation» из урока"
printf '%s  Единственное место, где нужно набрать руками. Четыре строки:%s\n' "$DIM" "$OFF"
printf '%s    Запомни слово «мимоза».%s\n' "$DIM" "$OFF"
printf '%s    /reset%s\n' "$DIM" "$OFF"
printf '%s    Запомни число 108.%s\n' "$DIM" "$OFF"
printf '%s    /exit%s\n' "$DIM" "$OFF"
pause 3.0
printf '\n%s$ ./day06 -session demo%s\n' "$CYAN" "$OFF"
# Typed live rather than piped in. A pipe cannot wait for the agent to answer, so the
# four questions land on screen as a block and the answers arrive afterwards — which
# reads, in a silent video, as if the agent replied to nothing.
./day06 -session demo
pause 1.6
run ./day06 -session demo "Что я просил запомнить?"
note "После сброса на диск попало только то, что было сказано ПОСЛЕ него."

say "Всё это проверяется машинно: два настоящих процесса и отрицательный контроль"
# -count=1 on purpose: a cached "ok" looks, on camera, like nothing ran.
run go test -count=1 -v -run 'Restart|WithoutMemory|TwoRealProcesses' ./day-06/e2e/
note "Тесты собирают бинарь, запускают ДВА процесса и смотрят, что второй отправил в модель."
run go test -count=1 ./...
note "Весь набор целиком, включая отрицательные контроли."

printf '\n%s%s▌ Замер дня: перезапуск не стоит ни одного попадания в кэш поставщика%s\n' "$BOLD" "$ORANGE" "$OFF"
printf '%s  Три прогона по 12 ходов, таблица и разбор — на витрине и в day-07/RESULTS.md%s\n' "$DIM" "$OFF"
printf '%s  https://challeng.mikeproject.dev/day-06/%s\n' "$GREEN" "$OFF"
printf '%s  https://github.com/mikeplotnikov/ai-advent-challenge-9%s\n' "$GREEN" "$OFF"
pause 4.0
