#!/usr/bin/env python3
"""Verify immutable measurements and derive the public report (no output repair)."""
import argparse
import copy
import hashlib
import importlib.util
import json
import math
import re
import statistics
from pathlib import Path

HERE = Path(__file__).resolve().parent
Q4 = '359d7dd4bcdab3d86b87d73ac27966f4dbb9f5efdfcc75d34a8764a09474fae7'
Q8 = 'qwen3:4b-thinking-2507-q8_0'
Q8_DIGEST = '44647463104281dcb560e8d3962c440fea54c4dacc242bf435dc9a0f313d3d48'
spec = importlib.util.spec_from_file_location('day28_report', HERE.parent/'day-28/report.py')
old = importlib.util.module_from_spec(spec)
spec.loader.exec_module(old)
digest = old.digest
check_answer = old.check_answer
checks = old.checks
error_kind = old.error_kind


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def canonical_profiles():
    baseline = re.search(r'const AnswerSystemPrompt = `([^`]+)`', (HERE/'prompt.go').read_text()).group(1)
    short = re.search(r'const ShortPrompt = `([^`]+)`', (HERE/'profiles.go').read_text()).group(1)
    return {p[0]: dict(zip(('id','model','temperature','num_predict','num_ctx','prompt'),p)) for p in [
        ('baseline','qwen3:4b',0,2048,16384,baseline),
        ('temperature','qwen3:4b',0.2,2048,16384,baseline),
        ('limits','qwen3:4b',0,1024,8192,baseline),
        ('prompt','qwen3:4b',0,2048,16384,short),
        ('combined','qwen3:4b',0,1024,8192,short),
        ('q8',Q8,0,1024,8192,short)]}


def resource(trace, duration=None):
    samples = trace.get('samples') or []
    if samples:
        require(trace['interval_ms']==250, 'sampling interval changed')
    for s in samples:
        require(isinstance(s['at_ms'],int) and s['at_ms']>=0, 'negative sample timestamp')
        if duration is not None:
            require(s['at_ms']<=duration+1000, 'sample outside attempt')
        for k in ('size','size_vram','context_length','rss_kib'):
            require(isinstance(s.get(k,0),int) and s.get(k,0)>=0, 'invalid resource '+k)
        require(isinstance(s.get('runner_pids',[]),list), 'runner PIDs missing')
    require(trace.get('rss_max_kib',0)==max((s.get('rss_kib',0) for s in samples),default=0), 'RSS maximum mismatch')
    require(trace.get('memory_bytes',0)==max((s.get('size',0) for s in samples),default=0), 'memory maximum mismatch')


def schema(chunks):
    items=[]
    for c in chunks:
        quotes=list(dict.fromkeys(q.strip() for parts in (c['text'].split('\n\n'),c['text'].split('\n')) for q in parts if q.strip()))
        props={k:{'type':'string','enum':[c[k]]} for k in ('source','section','chunk_id')}
        props['quote']={'type':'string','enum':quotes}
        items.append({'type':'object','properties':props,'required':['source','section','chunk_id','quote'],'additionalProperties':False})
    return {'type':'object','properties':{'answer':{'type':'string'},'unknown':{'type':'boolean'},'clarification':{'type':'string'},'sources':{'type':'array','items':{'anyOf':items},'maxItems':len(chunks)}},'required':['answer','unknown','clarification','sources'],'additionalProperties':False}


def verify_result(r,p,retrieval):
    require(r['question']==retrieval['question'] and r['context']==retrieval['context'] and r.get('embedding')==retrieval.get('embedding'), 'shared retrieval changed')
    for k in ('retrieval_ms','generation_ms','total_ms'):
        require(isinstance(r[k],(int,float)) and math.isfinite(r[k]) and r[k]>=0, 'invalid timing '+k)
    require(r['retrieval_ms']==retrieval['retrieval_ms'], 'retrieval timing changed')
    attempts=r['attempts']
    require(len(attempts)<=2, 'too many attempts')
    if r['context']:
        require(attempts or r.get('error'), 'generation missing')
        require(r['total_ms']==r['retrieval_ms']+r['generation_ms'], 'total timing mismatch')
    for i,a in enumerate(attempts):
        req=a['request']
        require(req['model']==p['model'], 'request model changed')
        require(all(req[k] is False for k in ('stream','think','truncate','shift')), 'request safety option changed')
        require(req['options']=={'temperature':p['temperature'],'seed':28,'num_predict':p['num_predict'],'num_ctx':p['num_ctx']}, 'profile options mismatch')
        require(req['format']==schema(r['context']), 'schema mismatch')
        msgs=req['messages'];require(len(msgs)==2+2*i, 'retry message count')
        require(msgs[0]=={'role':'system','content':p['prompt']}, 'prompt changed')
        user=msgs[1];prefix='Фрагменты (JSON):\n';suffix='\nВопрос: '+r['question']
        require(user['role']=='user' and user['content'].startswith(prefix) and user['content'].endswith(suffix), 'question message changed')
        require(json.loads(user['content'][len(prefix):-len(suffix)])==[{k:c[k] for k in ('source','section','chunk_id','text')} for c in r['context']], 'message context changed')
        if i:
            first=attempts[0];res=first.get('response') or {}
            require(bool(first.get('error')) and res.get('done') and res.get('done_reason')=='stop' and not res.get('error'), 'invalid retry')
            require(msgs[2]=={'role':'assistant','content':res['message']['content']} and msgs[3]=={'role':'user','content':'Исправь только JSON и дословные цитаты, не добавляя фактов. Ошибка проверки: '+first['error']}, 'retry message changed')
            require(msgs[:2]==first['request']['messages'], 'retry base messages changed')
        require(a['duration_ms']>=0, 'negative attempt time')
        resource(a['resources'],a['duration_ms'])
        require(bool(a['resources'].get('samples')), 'resource samples absent during generation')
        for sample in a['resources']['samples']:
            if sample.get('size',0)>0:
                require(sample['context_length']==p['num_ctx'], 'positive allocation context mismatch')
    resource(r['resources'])
    require((r['resources'].get('samples') or [])==[s for a in attempts for s in (a['resources'].get('samples') or [])], 'combined samples changed')
    require(r['resources'].get('rss_max_kib',0)==max((a['resources'].get('rss_max_kib',0) for a in attempts),default=0), 'combined RSS mismatch')
    require(r['resources'].get('memory_bytes',0)==max((a['resources'].get('memory_bytes',0) for a in attempts),default=0), 'combined memory mismatch')
    if r.get('answer'):
        require(not r.get('error'), 'answer and terminal error coexist')
        check_answer(r['answer'],r['context'])
        if attempts:
            a=attempts[-1];res=a.get('response') or {}
            require(not a.get('error') and res.get('done') and res.get('done_reason')=='stop' and res.get('model')==p['model'] and res.get('message',{}).get('role')=='assistant', 'invalid terminal response')
            require(json.loads(res['message']['content'])==r['answer'], 'terminal answer mismatch')
        else:
            require(not r['context'] and r.get('reason')=='empty_context', 'unexplained answer without model')
    else:
        require(bool(r.get('error')), 'missing answer and error')
        require(r.get('error_kind')==error_kind(r), 'error classification mismatch')


def verify_retrieval(r,meta,indexed):
    er=r['embedding'];require(er['request']=={'model':'bge-m3','input':r['question'],'truncate':False}, 'embedding request mismatch')
    resp=er['response'];require(not resp.get('error') and len(resp['embeddings'])==1,'embedding failure')
    vector=resp['embeddings'][0];require(len(vector)==meta['dimension'] and all(math.isfinite(x) for x in vector), 'embedding dimensions invalid')
    norm=math.sqrt(sum(x*x for x in vector));require(norm>0,'zero embedding');normalized=[x/norm for x in vector]
    ranked=sorted(((sum(x*y for x,y in zip(normalized,c['embedding'])),c) for c in indexed),key=lambda z:(-z[0],z[1]['chunk_id']))[:10]
    wanted=[dict({k:c[k] for k in ('source','section','chunk_id','text')},similarity=s) for s,c in ranked if s>=meta['cos_threshold']][:meta['top_k']]
    require(len(wanted)==len(r['context']), 'retrieval length mismatch')
    for got,want in zip(r['context'],wanted):
        require(all(got[k]==want[k] for k in ('source','section','chunk_id','text')) and math.isclose(got['similarity'],want['similarity'],rel_tol=1e-10,abs_tol=1e-12),'retrieval provenance mismatch')


def select_profiles(runs, summaries):
    """Gate candidates before forming a common successful timing population."""
    summaries=copy.deepcopy(summaries)
    by_id={s['id']:s for s in summaries}
    supported={p:{(r['id'],r['repeat']) for r in runs if r['profile']==p and r['assessment']['facts']=='correct' and r['assessment']['semantic']=='supported'} for p in by_id}
    def pairs(profile,predicate):
        return {(r['id'],r['repeat']) for r in runs if r['profile']==profile and predicate(r)}
    base_errors=pairs('baseline',lambda r:bool(r['result'].get('error')))
    base_refusals=pairs('baseline',lambda r:r['assessment']['facts']=='refusal')
    candidates=[]
    for p,s in by_id.items():
        s['correct_supported']=len(supported[p])
        new_errors=pairs(p,lambda r:bool(r['result'].get('error')))-base_errors
        lost_refusals=base_refusals-pairs(p,lambda r:r['assessment']['facts']=='refusal')
        quality_loss=max(0,len(supported['baseline'])-len(supported[p]))
        reasons=[]
        if new_errors: reasons.append('новые ошибки')
        if lost_refusals: reasons.append('потеря корректных отказов')
        if quality_loss: reasons.append('меньше правильных поддержанных ответов')
        candidates.append({'id':p,'eligible':not reasons,'new_error_pairs':len(new_errors),'lost_correct_refusal_pairs':len(lost_refusals),'correct_supported':len(supported[p]),'quality_loss':quality_loss,'reasons':reasons})
    eligible=[c['id'] for c in candidates if c['eligible']]
    require('baseline' in eligible, 'baseline candidate absent')
    common=set.intersection(*(supported[p] for p in eligible))
    for p,s in by_id.items():
        times=[r['result']['total_ms'] for r in runs if r['profile']==p and (r['id'],r['repeat']) in common]
        s['successful_median_ms']=statistics.median(times) if common and common<=supported[p] else None
    winner=min((by_id[p] for p in eligible),key=lambda s:(-s['correct_supported'],s['successful_median_ms'] if s['successful_median_ms'] is not None else math.inf,s['memory_bytes'] if s['memory_bytes'] else math.inf,s['id']))
    return summaries,winner,common,eligible,candidates


def build(runpath,semanticpath,require_cold=False):
    raw=runpath.read_bytes();data=json.loads(raw);meta=data['meta']
    require(meta['mode']=='live' and meta['diagnostic'] is False and meta.get('completed_at'), 'not completed primary live capture')
    require(meta['top_k']==3 and meta['cos_threshold']==0.45 and meta['embedding']['model']=='bge-m3', 'retrieval settings changed')
    require(meta.get('timing_protocol') and meta['resource_sampling_ms']==250, 'measurement protocol missing')
    original=(HERE.parent/'day-22/eval/questions.json').read_bytes()
    require(meta['questions_sha256']==digest(original), 'questions hash mismatch')
    expected=[]
    for q in json.loads(original):
        expected.append({'id':q['id'],'kind':q['kind'],'question':q['question'],'expectation':q.get('expectation',''),'facts':[{k:v for k,v in f.items() if k in ('name','patterns')} for f in q['facts']] if q.get('facts') is not None else None})
    require(data['questions']==expected, 'questions or facts changed')
    questions={q['id']:q for q in expected};require(len(questions)==10, 'question count changed')
    indexraw=(HERE.parent/'day-21/index/structure.json').read_bytes();require(meta['index_sha256']==digest(indexraw),'index hash mismatch')
    indexed=json.loads(indexraw)['chunks']
    profiles={p['id']:p for p in data['profiles']};allowed=canonical_profiles()
    require(len(profiles)==len(data['profiles']) and list(profiles)==list(allowed), 'complete frozen profile set required')
    require(meta['profile_order']==','.join(profiles), 'frozen profile order changed')
    for name,p in profiles.items():
        require(name in allowed and {k:p[k] for k in allowed[name]}==allowed[name], 'frozen profile mismatch '+name)
        identity=meta['identities'][p['model']];require(identity['model']==p['model'] and identity['endpoint'].startswith('http://127.0.0.1:'), 'model identity invalid')
        require(identity['digest']==Q4 if name!='q8' else identity['digest']==Q8_DIGEST, 'pinned digest mismatch')
        details=meta['model_details'][p['model']]['details'];quant='Q8_0' if name=='q8' else 'Q4_K_M'
        require(details['quantization_level']==quant, 'quantization mismatch')
        require(identity.get('size_bytes',0)>0, 'model disk size missing')
    retrievals={x['id']:x['result'] for x in data['retrievals']}
    require(len(data['retrievals'])==len(questions) and set(retrievals)==set(questions), 'retrieval set incomplete')
    for row in data['retrievals']:
        require(row['repeat']==0 and row['profile']=='retrieval', 'retrieval row invalid')
        r=row['result'];require(r['question']==questions[row['id']]['question'] and not r.get('error') and not r['attempts'],'invalid retrieval')
        verify_retrieval(r,meta,indexed)
    controls=data['controls']
    require(len(controls)==3*len(profiles), 'warmup controls incomplete')
    require([c['profile'] for c in controls]==[list(profiles)[(j+n-1)%len(profiles)] for n in (1,2,3) for j in range(len(profiles))], 'rotating profile order changed')
    for c in controls:
        p=profiles[c['profile']];req=c['request'];res=c.get('response') or {}
        require(c['kind']=='warmup' and not c.get('error') and c['duration_ms']>=0, 'failed warmup')
        require(req['model']==p['model'] and req['options']=={'temperature':p['temperature'],'seed':28,'num_predict':p['num_predict'],'num_ctx':p['num_ctx']} and all(req[k] is False for k in ('stream','think','truncate','shift')), 'warmup configuration mismatch')
        require(res.get('done') and res.get('done_reason')=='stop' and res.get('model')==p['model'] and not res.get('error'), 'warmup completion mismatch')
    keys=[(x['profile'],x['id'],x['repeat']) for x in data['runs']]
    require(len(keys)==len(set(keys)) and set(keys)=={(p,q,n) for p in profiles for q in questions for n in (1,2,3)}, 'repeat matrix incomplete or duplicate')
    require(semanticpath.exists(), 'independent semantic review pending')
    review=json.loads(semanticpath.read_bytes());require(review['run_sha256']==digest(raw),'semantic hash mismatch')
    semantic={}
    for j in review['judgments']:
        require(j['key'] not in semantic and j['verdict'] in ('supported','unsupported','unknown') and j['reason'].strip(), 'invalid semantic judgment')
        semantic[j['key']]=j
    require(set(semantic)=={f"{x['profile']}/{x['id']}/{x['repeat']}" for x in data['runs'] if x['result'].get('answer')}, 'semantic coverage mismatch')
    for row in data['runs']:
        r=row['result'];verify_result(r,profiles[row['profile']],retrievals[row['id']])
        judgment=semantic.get(f"{row['profile']}/{row['id']}/{row['repeat']}",{})
        if r.get('answer'):
            require((judgment['verdict']=='unknown')==r['answer']['unknown'], 'semantic refusal mismatch')
        row['assessment']=checks(r,questions[row['id']],judgment)
    # First collect quality counts; the gate defines the timing population below.
    supported={p:{(x['id'],x['repeat']) for x in data['runs'] if x['profile']==p and x['assessment']['facts']=='correct' and x['assessment']['semantic']=='supported'} for p in profiles}
    summary=[]
    for p in profiles:
        rows=[x for x in data['runs'] if x['profile']==p];rs=[x['result'] for x in rows]
        summary.append({'id':p,'total':len(rs),'valid':sum(bool(r.get('answer')) for r in rs),'correct_supported':len(supported[p]),'correct_refusals':sum(x['assessment']['facts']=='refusal' for x in rows),'lost_answers':sum(x['assessment']['facts']=='lost_answer' for x in rows),'unsupported':sum(x['assessment']['semantic']=='unsupported' for x in rows),'errors':sum(bool(r.get('error')) for r in rs),'median_ms':statistics.median(r['total_ms'] for r in rs),'successful_median_ms':None,'memory_bytes':max(r['resources'].get('memory_bytes',0) for r in rs),'rss_max_kib':max(r['resources'].get('rss_max_kib',0) for r in rs),'disk_bytes':meta['identities'][profiles[p]['model']]['size_bytes'],'resource_sample_errors':sum(bool(sample.get('error')) for r in rs for sample in (r['resources'].get('samples') or [])),'rss_available':any(sample.get('rss_kib',0)>0 and sample.get('runner_pids') for r in rs for sample in (r['resources'].get('samples') or []))})
    summary,winner,comparable,comparable_profiles,candidates=select_profiles(data['runs'],summary)
    base=next(s for s in summary if s['id']=='baseline')
    require(all(s['rss_available'] for s in summary), 'runner RSS unavailable for an entire profile')
    improvements=[]
    if winner['correct_supported']>base['correct_supported']: improvements.append('качество')
    if comparable and winner['successful_median_ms']<base['successful_median_ms']: improvements.append('время сопоставимых правильных ответов')
    if winner['memory_bytes'] and winner['memory_bytes']<base['memory_bytes']: improvements.append('выделение памяти генератору по /api/ps size')
    ready=winner['id']!='baseline' and bool(improvements)
    reason=('Измеренный выигрыш: '+', '.join(improvements)+'.') if ready else 'Улучшение относительно свежей базы не подтверждено; передача оптимизированной витрины заблокирована.'
    if ready:
        reason += f" Правильные поддержанные ответы: {base['correct_supported']} → {winner['correct_supported']} из {base['total']}; корректные отказы: {base['correct_refusals']} → {winner['correct_refusals']}; ошибки: {base['errors']} → {winner['errors']}."
        if comparable:
            reason += f" На {len(comparable)} одинаковых успешных парах медиана: {base['successful_median_ms']/1000:.2f} → {winner['successful_median_ms']/1000:.2f} с."
        if comparable and winner['successful_median_ms']>base['successful_median_ms']:
            reason += f" Компромисс: выбранный профиль медленнее на этих парах на {(winner['successful_median_ms']/base['successful_median_ms']-1)*100:.1f}%; выигрыш качества не является ускорением."
        base_questions={r['id'] for r in data['runs'] if r['profile']=='baseline' and r['assessment']['facts']=='correct' and r['assessment']['semantic']=='supported'}
        winner_questions={r['id'] for r in data['runs'] if r['profile']==winner['id'] and r['assessment']['facts']=='correct' and r['assessment']['semantic']=='supported'}
        reason += f" Подтверждённые ответы есть для {len(base_questions)} → {len(winner_questions)} разных вопросов; каждый вопрос запускался {max(r['repeat'] for r in data['runs'])} раза."
        if base['memory_bytes'] and winner['memory_bytes']:
            reason += f" Максимум size генератора: {base['memory_bytes']/1073741824:.2f} → {winner['memory_bytes']/1073741824:.2f} GiB."
        if not comparable:
            reason += " Общих правильных содержательных пар среди допущенных профилей нет; ускорение полезных ответов не подтверждено."
        reason += " Сначала исключены новые ошибки и потеря корректных отказов, затем сопоставлены качество, время и выделение памяти."
    if ready and winner['rss_max_kib']>base['rss_max_kib']:
        reason += f" Максимум суммы RSS runner вырос: {base['rss_max_kib']/1048576:.2f} → {winner['rss_max_kib']/1048576:.2f} GiB; общий расход памяти не считаем уменьшившимся."
    cold_controls=[]
    for filename,pid in (('cold-baseline.json','baseline'),('cold-selected.json',winner['id'])):
        path=runpath.parent/filename
        if not path.exists():
            require(not require_cold, 'cold control pending: '+filename)
            continue
        coldraw=path.read_bytes();cold=json.loads(coldraw);cm=cold['meta'];p=profiles[pid]
        require({k:cold['profile'][k] for k in allowed[pid]}==allowed[pid], 'cold profile mismatch')
        require(cold['before_ps']['models']==[] and cold['wall_ms']>=0 and cold['started_at'], 'cold unload evidence missing')
        require(cm['mode']=='live' and cm['index_sha256']==meta['index_sha256'] and cm['dimension']==meta['dimension'] and cm['top_k']==3 and cm['cos_threshold']==0.45, 'cold metadata mismatch')
        require(cm['generation']['model']==p['model'] and cm['generation']['digest']==meta['identities'][p['model']]['digest'], 'cold generator identity mismatch')
        require(cm['embedding']['model']=='bge-m3' and cm['embedding']['digest']==meta['embedding']['digest'], 'cold embedding identity mismatch')
        require(cm['options']=={'temperature':p['temperature'],'seed':28,'num_predict':p['num_predict'],'num_ctx':p['num_ctx']}, 'cold options mismatch')
        r=cold['result'];require(r['question']=='Что такое системный промпт?', 'cold question changed')
        verify_retrieval(r,cm,indexed)
        verify_result(r,p,r)
        require(r.get('answer') and not r.get('error'), 'cold control failed')
        embedding_load=r['embedding']['response'].get('load_duration')
        generation_load=r['attempts'][0]['response'].get('load_duration') if r['attempts'] else None
        require(embedding_load is not None and generation_load is not None and embedding_load>=0 and generation_load>=0, 'cold load timing absent')
        cold_controls.append({'file':filename,'profile':pid,'sha256':digest(coldraw),'wall_ms':cold['wall_ms'],'total_ms':r['total_ms'],'retrieval_ms':r['retrieval_ms'],'generation_ms':r['generation_ms'],'embedding_load_ms':embedding_load/1e6,'generation_load_ms':generation_load/1e6})
    data['summary']={'profiles':summary,'winner':winner['id'],'reason':reason,'ready':ready,'comparable_supported_pairs':len(comparable),'known_set':True,'cold_controls':cold_controls,'comparable_profiles':comparable_profiles,'candidates':candidates,'eligible_candidates':len(comparable_profiles),'rejected_candidates':len(candidates)-len(comparable_profiles)}
    data['run_sha256']=digest(raw);data['semantic_sha256']=digest(semanticpath.read_bytes())
    lines=['# День 29 — оптимизация локального RAG','',f"Raw SHA256: `{digest(raw)}`.",'',reason,'','Повторная проверка на известном наборе: не независимый unseen-бенчмарк. Статистическое превосходство не заявляется.','',f"Сопоставимых правильных содержательных пар вопрос/повтор: {len(comparable)}. Быстрые отказы исключены из критерия скорости.",'','| Профиль | Валидно | Правильные поддержанные | Корректные отказы | Потерянные ответы | Неподдержанные | Ошибки | Медиана всех, мс | Медиана сопоставимых, мс | size, байт | RSS, КиБ | Диск, байт |','|---|---|---|---|---|---|---|---|---|---|---|---|']
    for s in summary:
        lines.append('| '+ ' | '.join(str(s[k]) if s[k] is not None else 'н/д' for k in ('id','valid','correct_supported','correct_refusals','lost_answers','unsupported','errors','median_ms','successful_median_ms','memory_bytes','rss_max_kib','disk_bytes'))+' |')
    lines+=['',f"Выбранный профиль: `{winner['id']}`.",'','size — максимум /api/ps для выбранного генератора. RSS — максимум суммы RSS всех Ollama runner, включая embedding. Выборки каждые 250 мс могут пропускать пики; ошибки выборок сохранены. На Mac общая память: size, size_vram и RSS не складываются; RSS не является полным потреблением GPU. Энергия не измерялась. Все исходные ответы, отказы, ошибки и повторные попытки сохранены.','']
    lines += [f"Допущено профилей: {len(comparable_profiles)} из {len(candidates)}. Популяция сравнения времени: {', '.join(comparable_profiles)}.",'']
    for candidate in candidates:
        lines += [f"Профиль `{candidate['id']}`: {'допущен' if candidate['eligible'] else 'отклонён'}; новые ошибки {candidate['new_error_pairs']}, потерянные корректные отказы {candidate['lost_correct_refusal_pairs']}, недостающие правильные поддержанные ответы {candidate['quality_loss']}. Причины: {', '.join(candidate['reasons']) or 'нет'}.",'']
    for c in cold_controls:
        lines += [f"Холодный контроль `{c['profile']}`: полное время {c['total_ms']} мс, генерация {c['generation_ms']} мс, загрузка embedding {c['embedding_load_ms']:.2f} мс, загрузка генератора {c['generation_load_ms']:.2f} мс. SHA256: `{c['sha256']}`.",'']
    return data,'\n'.join(lines)


def main():
    ap=argparse.ArgumentParser();ap.add_argument('--run',type=Path,default=HERE/'run.json');ap.add_argument('--semantic',type=Path,default=HERE/'semantic-review.json');ap.add_argument('--check',action='store_true');args=ap.parse_args()
    data,text=build(args.run,args.semantic,require_cold=args.check)
    targets={'showcase.json':json.dumps(data,ensure_ascii=False,indent=2)+'\n','RESULTS.md':text,'config.json':json.dumps({'mode':'replay','profiles':data['profiles'],'winner':data['summary']['winner'],'summary':data['summary']},ensure_ascii=False,indent=2)+'\n'}
    targets['showcase.sha256']=digest(targets['showcase.json'].encode())+'\n'
    for name,content in targets.items():
        path=HERE/name
        if args.check: require(path.read_text()==content,name+' is stale')
        else: path.write_text(content)
    print(json.dumps(data['summary'],ensure_ascii=False))
    require(data['summary']['ready'],'no measured optimization; release readiness blocked')

if __name__=='__main__': main()
