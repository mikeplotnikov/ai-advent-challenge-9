"""Generate human-readable metrics directly from the captured application replies."""
import hashlib
import json
import sys
from pathlib import Path

root = Path(__file__).resolve().parent
raw = (root / 'showcase.json').read_bytes()
data = json.loads(raw)
assert data['kind'] == 'recorded-local-web-chat' and len(data['turns']) == 2
assert data['turns'][0]['request']['messages'][1]['content'] == 'Запомни: наш учебный клуб называется «Гоферы». Ответь одним коротким предложением'
rows = []
for i, t in enumerate(data['turns']):
    r = t['response']
    assert r['done'] and r['done_reason'] == 'stop' and r['message']['content'].strip()
    assert r['model'] == t['identity']['model'] == t['request']['model'] == 'qwen3:4b'
    assert t['identity']['endpoint'] == 'http://127.0.0.1:11434'
    assert t['request']['options']['num_predict'] == 2048
    assert t['request']['stream'] is False and t['request']['think'] is False
    assert len(t['request']['messages']) == 2 + i * 2
    if i:
        assert t['request']['messages'][1] == data['turns'][0]['request']['messages'][1]
        assert t['request']['messages'][2] == data['turns'][0]['response']['message']
    assert '</think>' in r['message']['content'], 'Captured reasoning disclosure is stale'
    rows.append(f"| {i+1} | {t['wall_ms']/1000:.2f} | {r['eval_count']} | {len(t['request']['messages'])} | {r['done_reason']} |")
assert data['turns'][1]['response']['message']['content'].rstrip().endswith('Гоферы')
sha = hashlib.sha256(raw).hexdigest()
text = '''# День 27 — результаты живого приложения

Два запроса выполнены через браузер → Go /api/chat → локальная Ollama → Qwen.
Вторая реплика передаёт первую реплику и исходный ответ без переписывания.

| Реплика | Время /api/chat, с | Выходные токены | Сообщения вместе с system | Завершение |
|---|---:|---:|---:|---|
''' + '\n'.join(rows) + f'''

Модель: `{data['turns'][0]['identity']['model']}`; digest `{data['turns'][0]['identity']['digest']}`.
Начало: `{data['turns'][0]['created_at']}`. SHA256 записи: `{sha}`.

Оба ответа содержат рассуждения и тег `</think>`, хотя think=false и промпт просит
краткий ответ. Запись сохранена полностью: завершённость API не означает соблюдения
формата или общую точность модели. Второй ответ заканчивается названием «Гоферы».
Время не включает /api/tags и /api/show перед генерацией; токены сообщает Ollama.
Физическое отключение интернета и сетевой трафик daemon не измерялись.

Пересчёт: `python3 day-27/report.py --check`. Запись: `showcase.json`.
'''
outputs = {'RESULTS.md': text, 'showcase.sha256': sha + '\n'}
for name, value in outputs.items():
    path = root / name
    if '--check' in sys.argv:
        assert path.read_text() == value, f'{name} is stale'
    else:
        path.write_text(value)
print('PASS: two completed local replies; context preserved; generated metrics/SHA match')
