#!/usr/bin/env python3
"""Verify capture provenance and build metrics; never replace raw model output."""
import argparse
import copy
import hashlib
import json
import re
import statistics
import math
from pathlib import Path

HERE = Path(__file__).resolve().parent

def digest(data):
    return hashlib.sha256(data).hexdigest()

def check_answer(a, chunks):
    assert isinstance(a, dict) and set(a) == {'answer', 'unknown', 'clarification', 'sources'}
    assert isinstance(a['unknown'], bool) and isinstance(a['answer'], str) and a['answer'].strip()
    assert isinstance(a['clarification'], str) and isinstance(a['sources'], list)
    if a['unknown']:
        assert a['answer'] == 'Не знаю.' and a['clarification'].strip() and not a['sources']
    else:
        assert not a['clarification'] and a['sources']
        for s in a['sources']:
            assert set(s) == {'source', 'section', 'chunk_id', 'quote'}
            assert all(isinstance(v, str) for v in s.values()) and s['quote'].strip()
            assert any(all(s[k] == c[k] for k in ('source', 'section', 'chunk_id')) and s['quote'] in c['text'] for c in chunks)

def error_kind(r):
    if not r.get('error'):return None
    if r.get('attempts'):
        res=r['attempts'][-1].get('response', {})
        if res and (not res.get('done') or res.get('done_reason')=='length'):return 'incomplete'
        if res.get('done') and res.get('done_reason')=='stop' and not res.get('error') and res.get('message',{}).get('role')=='assistant' and res.get('model')==r['attempts'][-1]['request']['model']:return 'contract'
    return 'technical'

def checks(r, q, semantic):
    if r.get('error'):
        return {'facts': 'contract_error' if error_kind(r)=='contract' else 'technical_error', 'semantic': 'not_applicable', 'reason': r['error']}
    a = r.get('answer')
    if not a:
        return {'facts': 'not_applicable', 'semantic': 'not_applicable', 'reason': r.get('reason', '')}
    if a['unknown']:
        facts = 'lost_answer' if q['kind'] == 'in_base' else 'refusal'
    elif not q.get('facts'):
        facts = 'not_applicable'
    else:
        matches = [any(re.search(p, a['answer'], re.I) for p in fact['patterns']) for fact in q['facts']]
        facts = 'correct' if all(matches) else 'partial' if any(matches) else 'missing'
    return {'facts': facts, 'semantic': semantic.get('verdict', 'pending'), 'reason': semantic.get('reason', '')}

def build(runpath, semanticpath):
    raw = runpath.read_bytes()
    data = json.loads(raw)
    meta = data['meta']
    assert meta['mode'] == 'live' and meta['generation']['model'] == 'qwen3:4b'
    assert meta['cos_threshold']==0.45 and meta['top_k']==3 and meta['embedding']['model']=='bge-m3'
    for model in ['generation', 'embedding']:
        assert meta[model]['digest'] and meta[model]['endpoint'].startswith('http://127.0.0.1:')
    archive = (HERE.parent / 'day-24/showcase.json').read_bytes()
    assert digest(archive) == meta['cloud_sha256']
    cloud = json.loads(archive)
    assert cloud['meta']['hashes']['index'] == meta['index_sha256']
    questions = {q['id']: q for q in data['questions']}
    original_q = (HERE.parent / 'day-22/eval/questions.json').read_bytes()
    assert digest(original_q) == meta['questions_sha256']
    expected_q = []
    for q in json.loads(original_q):
        expected_q.append({'id':q['id'], 'kind':q['kind'], 'question':q['question'], 'expectation':q.get('expectation',''), 'facts':[{k:v for k,v in f.items() if k in ('name','patterns')} for f in q['facts']] if q.get('facts') is not None else None})
    assert data['questions'] == expected_q
    assert len(questions) == 10 and len(data['runs']) == 30
    assert {(r['id'], r['repeat']) for r in data['runs']} == {(q, n) for q in questions for n in range(1,4)}
    archive_ids = {q['question']['id'] for q in cloud['questions']}
    assert len(data['comparisons']) == 10 and {c['id'] for c in data['comparisons']} == archive_ids
    semantic = {}
    if semanticpath.exists():
        review = json.loads(semanticpath.read_text())
        assert review['run_sha256'] == digest(raw)
        for item in review['judgments']:
            assert item['verdict'] in ('supported','unsupported','unknown') and item['reason'].strip()
            assert item['key'] not in semantic
            semantic[item['key']] = item
        expected = {f"local/{r['id']}/{r['repeat']}" for r in data['runs'] if r['result'].get('answer')}
        expected |= {f"comparison/{c['id']}" for c in data['comparisons'] if c['local'].get('answer')}
        assert set(semantic) == expected
    archive_by_id = {q['question']['id']: q for q in cloud['questions']}
    indexpath = HERE.parent / 'day-21/index/structure.json'
    if indexpath.exists():
        indexraw = indexpath.read_bytes()
        assert digest(indexraw) == meta['index_sha256']
        indexed = {c['chunk_id']:c for c in json.loads(indexraw)['chunks']}
    else:
        raise AssertionError('Index required for retrieval provenance; rebuild with go run ./day-21 -index')
    def expected_context(vector):
        norm=math.sqrt(sum(x*x for x in vector))
        assert norm > 0 and len(vector)==meta['dimension'] and all(math.isfinite(x) for x in vector)
        normalized=[x/norm for x in vector]
        ranked=sorted(((sum(x*y for x,y in zip(normalized,c['embedding'])),c) for c in indexed.values()),key=lambda x:(-x[0],x[1]['chunk_id']))[:10]
        return [{'source':c['source'],'section':c['section'],'chunk_id':c['chunk_id'],'text':c['text'],'similarity':sim} for sim,c in ranked if sim>=meta['cos_threshold']][:meta['top_k']]
    def message_context(messages, r):
        assert messages[0]['role']=='system' and messages[1]['role']=='user'
        user=messages[1]['content']; prefix='Фрагменты (JSON):\n'; suffix='\nВопрос: '+r['question']
        assert user.startswith(prefix) and user.endswith(suffix)
        ctx=json.loads(user[len(prefix):-len(suffix)])
        assert ctx==[{k:c[k] for k in ('source','section','chunk_id','text')} for c in r['context']]
    def check_schema(schema, chunks):
        assert schema['type']=='object' and schema['additionalProperties'] is False
        assert set(schema['required'])=={'answer','unknown','clarification','sources'}
        sources=schema['properties']['sources']; assert sources['maxItems']==len(chunks)
        alternatives=sources['items']['anyOf'];assert len(alternatives)==len(chunks)
        for item,c in zip(alternatives,chunks):
            props=item['properties']
            assert all(props[k]['enum']==[c[k]] for k in ('source','section','chunk_id'))
            quotes=list(dict.fromkeys(q.strip() for parts in [c['text'].split('\n\n'),c['text'].split('\n')] for q in parts if q.strip()))
            assert props['quote']['enum']==quotes
    def verify_result(r, fixed=False):
        assert isinstance(r['context'], list) and len(r['context']) <= 3
        if indexed:
            for c in r['context']:
                original=indexed[c['chunk_id']]
                assert all(c[k]==original[k] for k in ['text','source','section'])
        if not fixed:
            assert r['embedding']['request'] == {'model':'bge-m3','input':r['question'],'truncate':False}
            if not r.get('error'):
                er=r['embedding']['response']; assert not er.get('error') and len(er['embeddings'])==1 and len(er['embeddings'][0])==meta['dimension']
                expected=expected_context(er['embeddings'][0]);assert len(expected)==len(r['context'])
                for got,wanted in zip(r['context'],expected):
                    assert all(got[k]==wanted[k] for k in ('source','section','chunk_id','text')) and math.isclose(got['similarity'],wanted['similarity'],rel_tol=1e-10,abs_tol=1e-12)
        attempts = r['attempts']; assert len(attempts) <= 2
        for attempt in attempts:
            req=attempt['request']; assert req['model']==meta['generation']['model'] and req['think'] is False and req['stream'] is False and isinstance(req['format'],dict)
            assert req['options'] == meta['options']
            message_context(req['messages'], r)
            check_schema(req['format'],r['context'])
            assert attempt['duration_ms'] >= 0
        if r.get('answer'):
            check_answer(r['answer'], r['context'])
            if not fixed and not r.get('error'):
                assert r['total_ms'] >= r['retrieval_ms'] and r['total_ms'] >= r['generation_ms']
            if attempts:
                last=attempts[-1]; res=last['response']
                assert not last.get('error') and res['done'] and res['done_reason']=='stop' and res['model']==meta['generation']['model']
                assert json.loads(res['message']['content']) == r['answer']
            else:
                assert not r['context'] and r['reason']=='empty_context'
        else:
            assert r.get('error') or r.get('reason')=='no_archived_generation'
        assert all(r[k] >= 0 for k in ('retrieval_ms','generation_ms','total_ms'))
    for row in data['runs']:
        r=row['result']; assert r['question']==questions[row['id']]['question'];verify_result(r)
        r['checks']=checks(r,questions[row['id']],semantic.get(f"local/{row['id']}/{row['repeat']}",{}))
    for c in data['comparisons']:
        archived=archive_by_id[c['id']];assert c['cloud']==archived
        r=c['local']
        if archived.get('answer_call'):
            assert c['messages']==archived['answer_call']['messages']
            assert r['attempts'][0]['request']['messages']==c['messages']
            verify_result(r,True)
        else:
            assert not r.get('answer') and not r['attempts']
        r['checks']=checks(r,questions[c['id']],semantic.get('comparison/'+c['id'],{}))
    for c in data['comparisons']:
        if c['local'].get('error'):c['local']['error_kind']=error_kind(c['local'])
    results=[r['result'] for r in data['runs']]
    substantive=[r for r in results if r.get('answer') and not r['answer']['unknown']]
    durations=[r['total_ms'] for r in results]
    summary={'total':len(results),'valid':sum(bool(r.get('answer')) for r in results),'errors':sum(bool(r.get('error')) for r in results),'substantive':len(substantive),'supported':sum(r['checks']['semantic']=='supported' for r in substantive),'unsupported':sum(r['checks']['semantic']=='unsupported' for r in substantive),'pending':sum(r['checks']['semantic']=='pending' for r in results),'refusals':sum(bool(r.get('answer')) and r['answer']['unknown'] for r in results),'median_total_ms':statistics.median(durations),'min_total_ms':min(durations),'max_total_ms':max(durations),'groups':len(questions)}
    summary['identical_groups']=sum(len({json.dumps(row['result'].get('answer') or {'error':row['result'].get('error')},ensure_ascii=False,sort_keys=True) for row in data['runs'] if row['id']==qid})==1 for qid in questions)
    summary['median_retrieval_ms']=statistics.median(r['retrieval_ms'] for r in results)
    generated=[r for r in results if r['attempts']]
    summary['generation_runs']=len(generated)
    summary['median_generation_ms']=statistics.median(r['generation_ms'] for r in generated)
    summary['min_generation_ms']=min(r['generation_ms'] for r in generated)
    summary['max_generation_ms']=max(r['generation_ms'] for r in generated)
    summary['min_retrieval_ms']=min(r['retrieval_ms'] for r in results)
    summary['max_retrieval_ms']=max(r['retrieval_ms'] for r in results)
    comparison_results=[c['local'] for c in data['comparisons'] if c['cloud'].get('answer_call')]
    summary['comparison_calls']=len(comparison_results)
    summary['comparison_valid']=sum(bool(r.get('answer')) for r in comparison_results)
    summary['comparison_contract_errors']=sum(error_kind(r)=='contract' for r in comparison_results)
    summary['comparison_technical_errors']=sum(error_kind(r) in ('technical','incomplete') for r in comparison_results)
    summary['fact_counts']={name:sum(r['checks']['facts']==name for r in results) for name in ['correct','partial','missing','lost_answer','refusal']}
    summary['good_refusals']=sum(r['checks']['facts']=='refusal' for r in results)
    summary['lost_answers']=sum(r['checks']['facts']=='lost_answer' for r in results)
    coldpath = HERE/'cold-control.json'
    assert coldpath.exists(), 'Separate cold control is required for this published report'
    if coldpath.exists():
        cold = json.loads(coldpath.read_text())
        assert not cold['unload']['after']['models']
        assert all(cold['unload'][m]['done_reason']=='unload' for m in ['qwen3:4b','bge-m3'])
        verify_result(cold['result'])
        cr=cold['result']
        summary['cold_control_total_ms']=cr['total_ms']
        summary['cold_embedding_load_ms']=cr['embedding']['response']['load_duration']/1e6
        summary['cold_generation_load_ms']=cr['attempts'][0]['response']['load_duration']/1e6
        data['cold_control_sha256']=digest(coldpath.read_bytes())
    data['summary']=summary;data['run_sha256']=digest(raw);data['semantic_sha256']=digest(semanticpath.read_bytes()) if semanticpath.exists() else None
    lines=['# День 28 — локальная LLM + RAG','',f"Raw run SHA256: `{data['run_sha256']}`.",'',f"Завершённых валидных результатов: {summary['valid']}/{summary['total']}; технических сбоев: {summary['errors']}; отказов: {summary['refusals']}.",f"Поддержаны контекстом: {summary['supported']}/{summary['substantive']} содержательных ответов; неподдержанных: {summary['unsupported']}; ожидающих смысловой проверки результатов: {summary['pending']}.",f"Медиана полного времени: {summary['median_total_ms']/1000:.2f} с; диапазон: {summary['min_total_ms']/1000:.2f}–{summary['max_total_ms']/1000:.2f} с.",f"Полностью одинаковый JSON-ответ во всех трёх повторах: {summary['identical_groups']}/{summary['groups']} вопросов.",'','| Вопрос | Повтор | Факты | Смысл | Полное время, мс |','|---|---|---|---|---|']
    lines.insert(8,f"Поиск с эмбеддингом: медиана {summary['median_retrieval_ms']/1000:.2f} с, диапазон {summary['min_retrieval_ms']/1000:.2f}–{summary['max_retrieval_ms']/1000:.2f} с, все {summary['total']} запросов. Генерация: медиана {summary['median_generation_ms']/1000:.2f} с, диапазон {summary['min_generation_ms']/1000:.2f}–{summary['max_generation_ms']/1000:.2f} с, только {summary['generation_runs']} запросов с вызовом модели. Пропущенных ответов in_base: {summary['lost_answers']}.")
    lines.insert(9,f"Корректных отказов вне базы: {summary['good_refusals']}; пропущенных ответов in_base: {summary['lost_answers']}. Эталонные факты: {summary['fact_counts']}.")
    lines.insert(10,f"Отдельное архивное сравнение: валидных Qwen-ответов {summary['comparison_valid']}/{summary['comparison_calls']}; ошибок контракта {summary['comparison_contract_errors']}; технических/незавершённых {summary['comparison_technical_errors']}. Не принятые ответы и обе попытки сохранены; они не входят в 30 основных локальных прогонов.")
    lines.insert(11,f"Отдельный холодный контроль после выгрузки обеих моделей: {summary['cold_control_total_ms']/1000:.2f} с; загрузка embedding {summary['cold_embedding_load_ms']/1000:.2f} с, генератора {summary['cold_generation_load_ms']/1000:.2f} с.")
    for row in data['runs']:
        r=row['result'];lines.append(f"| {row['id']} | {row['repeat']} | {r['checks']['facts']} | {r['checks']['semantic']} | {r['total_ms']} |")
    lines+=['','Все попытки, включая отклонённые цитаты и ошибки, сохранены в raw run. Сравнение DeepSeek — архив от 01.10, одинаковые входные сообщения генерации; разные среды/даты не позволяют вывод о превосходстве скорости. Повторяемость облака не измерялась. Перед основным прогоном выполнялись диагностические запросы, поэтому первый запрос серии не объявляется холодным стартом. load_duration сохранён внутри raw ответов; загрузка не исключена из полного времени. Отдельный холодный контроль сохранён в cold-control.json; его времена приведены выше.','']
    return data, '\n'.join(lines)

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--run',type=Path,default=HERE/'run.json');parser.add_argument('--semantic',type=Path,default=HERE/'semantic-review.json');parser.add_argument('--check',action='store_true');args=parser.parse_args()
    data,text=build(args.run,args.semantic)
    targets={HERE/'showcase.json':json.dumps(data,ensure_ascii=False,indent=2)+'\n',HERE/'RESULTS.md':text}
    targets[HERE/'showcase.sha256']=digest(targets[HERE/'showcase.json'].encode())+'\n'
    for p,s in targets.items():
        if args.check: assert p.read_text()==s, f'{p} is stale'
        else:p.write_text(s)
    print(json.dumps(data['summary'],ensure_ascii=False))
if __name__=='__main__':main()
