#!/usr/bin/env bash
# The day-32 recording: opens a pull request with the reference defects of
# eval/arch.patch (branch day-32-demo from origin/main, built in a throwaway worktree so
# the working tree is never touched), waits for the AI review run and prints the
# comment. Needs day-32/runner.sh running in another terminal.
#   ./day-32/demo.sh         open the PR and wait for the review
#   ./day-32/demo.sh close   close the PR without merging and delete its branch
set -euo pipefail

REPO=mikeplotnikov/ai-advent-challenge-9
BRANCH=day-32-demo
root=$(git rev-parse --show-toplevel)
cd "$root"
PACE="${PACE:-1.0}"
pause() { sleep "$(LC_ALL=C awk -v s="$1" -v p="$PACE" 'BEGIN{printf "%.2f", s*p}')"; }
say() { printf '\n\033[1m\033[38;5;208m▌ %s\033[0m\n' "$*"; pause 2.5; }

if [ "${1:-}" = close ]; then
  gh pr close "$BRANCH" -R "$REPO" --delete-branch --comment "Демо дня 32 записано — PR закрыт без мержа."
  git branch -D "$BRANCH" 2>/dev/null || true
  exit 0
fi

[ "$(gh variable get DAY32_REVIEW -R "$REPO" 2>/dev/null)" = on ] ||
  { echo "AI-ревью выключено: сначала ./day-32/runner.sh в соседнем терминале" >&2; exit 1; }
git fetch -q origin main
git cat-file -e origin/main:.github/workflows/day-32-review.yml 2>/dev/null ||
  { echo "в origin/main нет workflow дня 32: сначала запушьте main" >&2; exit 1; }
if git show-ref --verify --quiet "refs/heads/$BRANCH"; then
  echo "локальная ветка $BRANCH уже есть — удалите её или переименуйте" >&2
  exit 1
fi
if git ls-remote --exit-code --heads origin "$BRANCH" >/dev/null 2>&1; then
  echo "ветка $BRANCH уже есть на GitHub: ./day-32/demo.sh close" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'git worktree remove --force "$tmp/wt" 2>/dev/null || true; git branch -D "$BRANCH" >/dev/null 2>&1 || true; rm -rf "$tmp"' EXIT
say "День 32 — автоматическое AI-ревью PR: diff + RAG (документация и код) → DeepSeek → комментарий в PR"
say "Пайплайн — GitHub Actions на pull_request; job идёт на этот Mac (self-hosted runner, Ollama + bge-m3)"
sed -n '/^on:/,/^$/p;/runs-on/p;/go run/p' .github/workflows/day-32-review.yml
pause 4
say "Делаю PR с намеренными дефектами: новый день 91 зовёт DeepSeek мимо internal/llm и считает цену константой"
git worktree add -q -b "$BRANCH" "$tmp/wt" origin/main
git -C "$tmp/wt" apply "$root/day-32/eval/arch.patch"
git -C "$tmp/wt" add -A
git -C "$tmp/wt" commit -q -m "Add day 91: ask DeepSeek one question and print its price"
git -C "$tmp/wt" push -q -u origin "$BRANCH"

started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
gh pr create -R "$REPO" --head "$BRANCH" --base main \
  --title "День 91: вопрос к DeepSeek и цена ответа (демо AI-ревью)" \
  --body "Демо дня 32: PR с намеренными дефектами — посмотрим, что найдёт AI-ревью. Мержить не нужно."

say "PR открыт. Жду, когда GitHub запустит AI-ревью на раннере…"
run=""
for _ in $(seq 1 60); do
  run=$(gh run list -R "$REPO" --workflow day-32-review.yml --branch "$BRANCH" --created ">=$started" --limit 1 --json databaseId --jq '.[0].databaseId // empty')
  [ -n "$run" ] && break
  sleep 2
done
[ -n "$run" ] || { echo "запуск workflow не появился за 2 минуты" >&2; exit 1; }
gh run watch "$run" -R "$REPO" --exit-status
say "Ревью опубликовано комментарием в PR: баги, архитектура, рекомендации — со ссылками на правила (D…) и код (C…)"
gh pr view "$BRANCH" -R "$REPO" --comments
say "Полный комментарий: $(gh pr view "$BRANCH" -R "$REPO" --json url --jq .url)"
