#!/usr/bin/env bash
# Registers this Mac as the self-hosted GitHub Actions runner for day 32's AI review for
# exactly as long as this script runs, and undoes all of it on exit (Ctrl-C): the runner
# is removed from the repository, DAY32_REVIEW goes back to "off" and the fork-approval
# policy goes back to what it was. A runner on a public repository executes whatever a
# pull request contains, so it must not outlive the recording.
set -euo pipefail

REPO=mikeplotnikov/ai-advent-challenge-9
VERSION=2.338.0
# From the release notes of actions/runner v2.338.0, "<!-- BEGIN SHA osx-arm64 -->".
SHA256=df4cebda25c86a886ed204e49fee63f5c2e7cec5f447b5c98440a826bbdf9df2
DIR=${DAY32_RUNNER_DIR:-$HOME/actions-runner-day32}
NAME="day32-$(hostname -s)"

[ "$(uname -s)-$(uname -m)" = "Darwin-arm64" ] || { echo "нужен Mac на Apple Silicon" >&2; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh не авторизован: gh auth login" >&2; exit 1; }
command -v go >/dev/null || { echo "нет go в PATH" >&2; exit 1; }
curl -sf http://127.0.0.1:11434/api/tags | grep -q '"bge-m3' ||
  { echo "Ollama с bge-m3 не отвечает: ollama serve (и один раз ollama pull bge-m3)" >&2; exit 1; }

if [ ! -x "$DIR/run.sh" ]; then
  mkdir -p "$DIR"
  tarball="$DIR/actions-runner-osx-arm64-$VERSION.tar.gz"
  echo "Скачиваю actions/runner $VERSION…"
  curl -fsSL -o "$tarball" "https://github.com/actions/runner/releases/download/v$VERSION/actions-runner-osx-arm64-$VERSION.tar.gz"
  if ! echo "$SHA256  $tarball" | shasum -a 256 -c - >/dev/null; then
    rm -f "$tarball"
    echo "контрольная сумма раннера не совпала — не запускаю" >&2
    exit 1
  fi
  tar xzf "$tarball" -C "$DIR"
  rm -f "$tarball"
fi

policy_was=$(gh api "repos/$REPO/actions/permissions/fork-pr-contributor-approval" --jq .approval_policy)

runner_pid=""

cleanup() {
  # A second Ctrl-C while the runner is being taken down must not interrupt it.
  trap '' INT TERM HUP
  trap - EXIT
  if [ -n "$runner_pid" ] && kill -0 "$runner_pid" 2>/dev/null; then
    kill -TERM "$runner_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
  fi
  echo
  echo "Снимаю раннер…"
  gh variable set DAY32_REVIEW --body off -R "$REPO" || echo "! DAY32_REVIEW не выключена — выключите: gh variable set DAY32_REVIEW --body off -R $REPO" >&2
  if [ -f "$DIR/.runner" ]; then
    if token=$(gh api -X POST "repos/$REPO/actions/runners/remove-token" --jq .token); then
      (cd "$DIR" && ./config.sh remove --token "$token") || echo "! раннер не снят с регистрации" >&2
    else
      echo "! не получен токен снятия раннера" >&2
    fi
  fi
  gh api -X PUT "repos/$REPO/actions/permissions/fork-pr-contributor-approval" -f approval_policy="$policy_was" >/dev/null ||
    echo "! политика форков не восстановлена (была $policy_was)" >&2
  echo "Итог: раннеров в репозитории $(gh api "repos/$REPO/actions/runners" --jq .total_count || echo '?')," \
    "DAY32_REVIEW=$(gh variable get DAY32_REVIEW -R "$REPO" || echo '?')," \
    "политика форков $(gh api "repos/$REPO/actions/permissions/fork-pr-contributor-approval" --jq .approval_policy || echo '?')"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

gh api -X PUT "repos/$REPO/actions/permissions/fork-pr-contributor-approval" -f approval_policy=all_external_contributors >/dev/null
token=$(gh api -X POST "repos/$REPO/actions/runners/registration-token" --jq .token)
(cd "$DIR" && ./config.sh --unattended --url "https://github.com/$REPO" --token "$token" \
  --name "$NAME" --labels day-32 --work _work --replace)
gh variable set DAY32_REVIEW --body on -R "$REPO"

echo "Раннер $NAME работает, AI-ревью включено. Ctrl-C — снять раннер и выключить ревью."
# In the background with `wait`, so Ctrl-C, kill and a closed terminal reach the trap at
# once instead of after the runner happens to exit.
(cd "$DIR" && exec ./run.sh) &
runner_pid=$!
wait "$runner_pid"
