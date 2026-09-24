"""Makes sure a showcase question left a record, run by .github/workflows/day-18-agent.yml.

The strict hourly limit counts records, so a run whose agent wrote none (crash, bad flags,
timeout) would not count. Env: QUESTIONS, REQUEST_ID, QUESTION, OUTCOME.
"""
import datetime, json, os
path, rid = os.environ['QUESTIONS'], os.environ['REQUEST_ID']
records = json.load(open(path)) if os.path.exists(path) else []
if any(r.get('request_id') == rid for r in records):
    print('запись вопроса есть'); raise SystemExit(0)
print('агент не записал вопрос — дописываю запись об ошибке, чтобы лимит её посчитал')
records.append({
    'request_id': rid, 'at': datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
    'question': os.environ.get('QUESTION', ''), 'answer': '',
    'error': f"агент не записал ответ (шаг вопроса: {os.environ.get('OUTCOME') or 'не выполнялся'})",
    'model': '', 'model_calls': 0, 'tool_calls': [],
    'tokens': {'prompt': 0, 'cached': 0, 'output': 0, 'per_call': []}, 'cost': 0, 'cost_known': True,
})
# Atomic like the Go writer: a cut-off run must not leave a truncated file, which the
# agent would then refuse to touch. No lock needed — runs are serialized and the agent
# is not writing at this point.
tmp = path + '.tmp'
with open(tmp, 'w') as f:
    json.dump(records[-100:], f, ensure_ascii=False, indent=2)
    f.write('\n')
    f.flush(); os.fsync(f.fileno())
os.replace(tmp, path)
