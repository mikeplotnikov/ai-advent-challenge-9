"""Generate RESULTS.md from the exact captured runs, without model calls."""
import hashlib
import json
import argparse
from pathlib import Path

parser=argparse.ArgumentParser()
parser.add_argument("--check",action="store_true",help="Verify generated files without modifying them")
args=parser.parse_args()
root = Path(__file__).resolve().parent
current = json.loads((root / "showcase.json").read_text())
baseline = json.loads((root / "experiments/nonthinking.json").read_text())
lines = ["# День 26 — измеренный запуск", "", f"Прогон: {current['created_at']}. Модель: `{current['model']}`.",
         f"Ollama {current['ollama_version']}, {current['platform']}. Endpoint: `{current['endpoint']}`.",
         f"Digest: `{current['digest']}`.", "", "| Запрос | Ответ API | Проверка | Полное время, с | Загрузка, с | Вход / выход, токены | Генерация, ток/с |",
         "|---|---|---|---:|---:|---:|---:|"]
for item in current["results"]:
    r = item["response"]
    if not r["done"] or r["done_reason"] != "stop":
        raise SystemExit("Capture contains incomplete API response")
    rate = r["eval_count"] * 1e9 / r["eval_duration"] if r["eval_duration"] else 0
    lines.append(f"| {item['title']} | завершён | {'PASS' if item['passed'] else 'FAIL'} | {item['wall_ms']/1000:.2f} | {r['load_duration']/1e9:.2f} | {r['prompt_eval_count']} / {r['eval_count']} | {rate:.1f} |")
passed = sum(x["passed"] for x in current["results"])
base_passed = sum(x["passed"] for x in baseline["results"])
metadata=current["model_metadata"]
if "completion" not in metadata["capabilities"] or metadata.get("remote_model") or metadata.get("remote_host"):
    raise SystemExit("Capture does not describe local completion model")
params = current["results"][0]["request"]
settings = {"think": params["think"], **params["options"]}
identity = (root/"showcase.json").read_bytes() == (root/"experiments/nonthinking.json").read_bytes()
expected_sha=hashlib.sha256((root/"showcase.json").read_bytes()).hexdigest()+"\n"
lines += ["", f"Итог: завершённых ответов — {len(current['results'])}; {passed}/{len(current['results'])} проверок ответа PASS.",
          f"Архивная копия nonthinking.json: {base_passed}/{len(baseline['results'])} проверок PASS; файлы {'побайтно идентичны (это один запуск)' if identity else 'различаются'}.",
          "", f"Настройки сохранённого прогона: `{json.dumps(settings,ensure_ascii=False,sort_keys=True)}`. Время загрузки каждого вызова указано в таблице; первый вызов и последующие оцениваются отдельно.",
          "", "Дополнительная попытка описана в README по терминальному выводу experiments/thinking-attempt.txt; полного capture нет. Это не независимый бенчмарк. Скорость включает все выходные токены; полное время и скорость генерации измеряют разные части вызова.",
          "", "Модель локально установлена; /api/show содержит completion и не содержит remote_model/remote_host. Runner запрещает внешние endpoint, proxy и redirects. Интернет физически не отключался, трафик daemon не записывался.",
          "", "## Целостность исходных данных", ""]
for name in ["showcase.json", "experiments/nonthinking.json"]:
    lines.append(f"- `{name}` SHA-256: `{hashlib.sha256((root/name).read_bytes()).hexdigest()}`.")
expected_report="\n".join(lines)+"\n"
if args.check:
    if (root/"RESULTS.md").read_text()!=expected_report:
        raise SystemExit("RESULTS.md is stale")
    if (root/"showcase.sha256").read_text()!=expected_sha:
        raise SystemExit("showcase.sha256 is stale")
    print("Generated report and capture digest PASS")
else:
    (root/"RESULTS.md").write_text(expected_report)
    (root/"showcase.sha256").write_text(expected_sha)
