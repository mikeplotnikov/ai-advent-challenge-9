#!/usr/bin/env bash
# Runs the reviewer on the reference pull requests: each eval/*.patch is applied on top
# of HEAD in a throwaway worktree and committed, and the review of that commit is saved
# to eval/results/<case>.md. What each case must and must not find: eval/README.md.
# Costs a few cents of DeepSeek; needs Ollama with bge-m3 for the RAG half.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
tmp=$(mktemp -d)
wt="$tmp/wt"
go build -o "$tmp/day32" ./day-32
base=$(git rev-parse HEAD)
git worktree add -q --detach "$wt" "$base"
trap 'git worktree remove --force "$wt"; rm -rf "$tmp"' EXIT
mkdir -p day-32/eval/results
for patch in day-32/eval/*.patch; do
  name=$(basename "$patch" .patch)
  git -C "$wt" checkout -q --detach "$base"
  git -C "$wt" apply "$root/$patch"
  git -C "$wt" add -A
  git -C "$wt" -c user.name=eval -c user.email=eval@localhost commit -q -m "eval: $name"
  echo "== $name"
  "$tmp/day32" -repo "$wt" -base "$base" -head "$(git -C "$wt" rev-parse HEAD)" -summary "" \
    -out "day-32/eval/results/$name.md" || echo "   ревью завершилось ошибкой — см. results/$name.md"
done
