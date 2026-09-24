"""Strict hourly limit on showcase questions, run by .github/workflows/day-18-agent.yml.

Runs of that workflow never overlap (its concurrency group), so counting the recorded
showcase questions here is atomic. Env: QUESTIONS (path), QUESTIONS_PER_HOUR, REQUEST_ID,
QUESTION, GITHUB_OUTPUT. Outputs: ask=true|false, full=true when the limit is reached (a
refusal record is then written). An invalid REQUEST_ID fails the step without `full`.
"""
import datetime, json, os, re, sys
out = open(os.environ['GITHUB_OUTPUT'], 'a')
rid = os.environ.get('REQUEST_ID', '')
if not rid:
    out.write('ask=true\n'); sys.exit(0)
if not re.fullmatch(r'[0-9a-f]{32}', rid):
    # Only a manual dispatch can send this (the showcase makes its own ids). A record
    # under an invalid id could never be found, so the run just fails loudly.
    print('request_id не 32 hex — вопрос не задаётся'); out.write('ask=false\n'); sys.exit(1)
path = os.environ['QUESTIONS']
records = json.load(open(path)) if os.path.exists(path) else []
now = datetime.datetime.now(datetime.timezone.utc)
hour_ago = now - datetime.timedelta(hours=1)
recent = [r for r in records if r.get('request_id') and datetime.datetime.fromisoformat(r['at'].replace('Z', '+00:00')) >= hour_ago]
limit = int(os.environ['QUESTIONS_PER_HOUR'])
if len(recent) < limit:
    out.write('ask=true\n'); sys.exit(0)
print(f'вопросов с витрины за час: {len(recent)} из {limit} — лимит исчерпан, агент не запускается')
records.append({
    'request_id': rid, 'at': now.strftime('%Y-%m-%dT%H:%M:%SZ'), 'question': os.environ.get('QUESTION', ''),
    'answer': '', 'error': 'лимит вопросов на час исчерпан', 'model': '', 'model_calls': 0, 'tool_calls': [],
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
out.write('ask=false\n'); out.write('full=true\n')
