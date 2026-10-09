"""Synthetic resource/profile variations over archived measurements: no model calls."""
import copy
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('report',HERE/'report.py')
report=importlib.util.module_from_spec(spec);spec.loader.exec_module(report)

class CaptureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        old=json.loads((HERE.parent/'day-28/run.json').read_bytes())
        sem=json.loads((HERE.parent/'day-28/semantic-review.json').read_bytes())
        cls.data={'meta':copy.deepcopy(old['meta']),'profiles':[],'questions':old['questions'],'retrievals':[],'runs':[],'controls':[]}
        m=cls.data['meta'];m.update(diagnostic=False,completed_at='fixture',resource_sampling_ms=250,timing_protocol='synthetic fixture')
        m['identities']={'qwen3:4b':{'model':'qwen3:4b','endpoint':'http://127.0.0.1:11434','digest':report.Q4,'size_bytes':1000}}
        m['model_details']={'qwen3:4b':{'details':{'quantization_level':'Q4_K_M'}},report.Q8:{'details':{'quantization_level':'Q8_0'}}}
        m['identities'][report.Q8]={'model':report.Q8,'endpoint':'http://127.0.0.1:11434','digest':report.Q8_DIGEST,'size_bytes':2000}
        m['profile_order']=','.join(report.canonical_profiles())
        bykey={x['key']:x for x in sem['judgments']}
        cls.judgments=[]
        for p in report.canonical_profiles():
            profile=report.canonical_profiles()[p];cls.data['profiles'].append(profile)
            for row in old['runs']:
                r=copy.deepcopy(row['result'])
                if p=='baseline' and row['repeat']==1:
                    retrieval=copy.deepcopy(r);retrieval['attempts']=[];retrieval.pop('answer',None)
                    cls.data['retrievals'].append({'id':row['id'],'repeat':0,'profile':'retrieval','result':retrieval})
                shared=next(x['result'] for x in cls.data['retrievals'] if x['id']==row['id'])
                for field in ('question','context','embedding','retrieval_ms'):r[field]=copy.deepcopy(shared[field])
                trace={'interval_ms':250,'samples':[],'rss_max_kib':0,'memory_bytes':0}
                for a in r['attempts']:
                    a['request']['model']=profile['model'];a['response']['model']=profile['model']
                    a['request']['options']={'temperature':profile['temperature'],'seed':28,'num_predict':profile['num_predict'],'num_ctx':profile['num_ctx']}
                    a['request']['truncate']=False;a['request']['shift']=False
                    a['request']['messages'][0]['content']=profile['prompt']
                    a['resources']={'interval_ms':250,'samples':[{'at_ms':0,'size':100 if p=='baseline' else 80,'size_vram':50,'context_length':profile['num_ctx'],'rss_kib':200,'runner_pids':[999]}],'rss_max_kib':200,'memory_bytes':100 if p=='baseline' else 80}
                    trace['samples']+=a['resources']['samples'];trace['rss_max_kib']=200;trace['memory_bytes']=100 if p=='baseline' else 80
                r['resources']=trace
                if r['context']:
                    r['generation_ms']=100 if p=='baseline' else (50 if p=='prompt' else 75);r['total_ms']=r['retrieval_ms']+r['generation_ms']
                cls.data['runs'].append({'id':row['id'],'repeat':row['repeat'],'profile':p,'result':r})
                if r.get('answer'):
                    j=copy.deepcopy(bykey[f"local/{row['id']}/{row['repeat']}"]);j['key']=f"{p}/{row['id']}/{row['repeat']}";cls.judgments.append(j)

        for n in (1,2,3):
            for j in range(6):
                name=list(report.canonical_profiles())[(j+n-1)%6];p=report.canonical_profiles()[name]
                cls.data['controls'].append({'profile':name,'kind':'warmup','duration_ms':1,'request':{'model':p['model'],'options':{'temperature':p['temperature'],'seed':28,'num_predict':p['num_predict'],'num_ctx':p['num_ctx']},'stream':False,'think':False,'truncate':False,'shift':False},'response':{'done':True,'done_reason':'stop','model':p['model']}})

    def run_fixture(self,mutate=None,semantic_mutate=None,cold=False,require_cold=False,cold_mutate=None):
        data=copy.deepcopy(self.data)
        if mutate: mutate(data)
        with tempfile.TemporaryDirectory() as tmp:
            run=Path(tmp)/'run.json';run.write_text(json.dumps(data,ensure_ascii=False))
            semantic={'run_sha256':report.digest(run.read_bytes()),'judgments':copy.deepcopy(self.judgments)}
            if semantic_mutate:semantic_mutate(semantic)
            sp=Path(tmp)/'semantic.json';sp.write_text(json.dumps(semantic))
            if cold:
                for filename,pid in (('cold-baseline.json','baseline'),('cold-selected.json','prompt')):
                    row=next(x for x in data['runs'] if x['profile']==pid and x['id']=='q05' and x['repeat']==1)
                    p=report.canonical_profiles()[pid];cm=copy.deepcopy(data['meta'])
                    cm['generation']=cm['identities'][p['model']]
                    cm['options']={'temperature':p['temperature'],'seed':28,'num_predict':p['num_predict'],'num_ctx':p['num_ctx']}
                    value={'meta':cm,'profile':p,'before_ps':{'models':[]},'wall_ms':1000,'started_at':'fixture','result':copy.deepcopy(row['result'])}
                    r=value['result'];old_question=r['question'];r['question']='Что такое системный промпт?'
                    r['embedding']['request']['input']=r['question']
                    for a in r['attempts']:
                        a['request']['messages'][1]['content']=a['request']['messages'][1]['content'].removesuffix(old_question)+r['question']
                    if cold_mutate:cold_mutate(value)
                    (Path(tmp)/filename).write_text(json.dumps(value))
            return report.build(run,sp,require_cold=require_cold)

    def test_positive_control(self):
        data,text=self.run_fixture();self.assertTrue(data['summary']['ready']);self.assertEqual(data['summary']['winner'],'prompt');self.assertIn('известном',text)

    def test_corruptions_detected(self):
        mutations={
            'missing_profile':lambda d:d['profiles'].pop(),
            'profile':lambda d:d['profiles'][0].update(num_ctx=42),
            'raw_answer':lambda d:d['runs'][0]['result']['answer'].update(answer='Подмена'),
            'context':lambda d:d['runs'][0]['result']['context'][0].update(text='Подмена'),
            'repeat':lambda d:d['runs'].pop(),
            'question_hash':lambda d:d['meta'].update(questions_sha256='0'*64),
            'index_hash':lambda d:d['meta'].update(index_sha256='0'*64),
            'response':lambda d:d['runs'][0]['result']['attempts'][-1]['response'].update(done_reason='length'),
            'resource_max':lambda d:d['runs'][0]['result']['resources'].update(rss_max_kib=201),
            'positive_resource_context':lambda d:d['runs'][0]['result']['attempts'][0]['resources']['samples'][0].update(context_length=8192),
            'truncate':lambda d:d['runs'][0]['result']['attempts'][0]['request'].update(truncate=True),
            'prompt':lambda d:d['runs'][0]['result']['attempts'][0]['request']['messages'][0].update(content='x'),
            'digest':lambda d:d['meta']['identities']['qwen3:4b'].update(digest='0'*64),
            'quant':lambda d:d['meta']['model_details']['qwen3:4b']['details'].update(quantization_level='Q8_0'),
        }
        for name,mutation in mutations.items():
            with self.subTest(name=name), self.assertRaises((AssertionError,KeyError,ValueError)):
                self.run_fixture(mutation)

    def test_whole_profile_omission_rejected(self):
        def omit(d):
            d['profiles']=[p for p in d['profiles'] if p['id']!='q8']
            d['runs']=[r for r in d['runs'] if r['profile']!='q8']
            d['controls']=[c for c in d['controls'] if c['profile']!='q8']
            d['meta']['profile_order']=','.join(p['id'] for p in d['profiles'])
        def omit_judgments(s):
            s['judgments']=[j for j in s['judgments'] if not j['key'].startswith('q8/')]
        with self.assertRaisesRegex(AssertionError,'complete frozen profile set'):
            self.run_fixture(mutate=omit,semantic_mutate=omit_judgments)

    def test_semantic_hash_and_coverage(self):
        for mutation in (lambda s:s.update(run_sha256='0'*64),lambda s:s['judgments'].pop()):
            with self.assertRaises(AssertionError):self.run_fixture(semantic_mutate=mutation)

    def test_cold_controls_boundary(self):
        with self.assertRaisesRegex(AssertionError,'cold control pending'):
            self.run_fixture(require_cold=True)
        data,_=self.run_fixture(cold=True,require_cold=True)
        self.assertEqual(len(data['summary']['cold_controls']),2)
        with self.assertRaisesRegex(AssertionError,'cold options mismatch'):
            self.run_fixture(cold=True,require_cold=True,cold_mutate=lambda c:c['meta']['options'].update(num_ctx=3))
        with self.assertRaisesRegex(AssertionError,'cold unload'):
            self.run_fixture(cold=True,require_cold=True,cold_mutate=lambda c:c['before_ps'].update(models=[{}]))

    def test_all_zero_rss_rejected(self):
        def zero(d):
            for row in d['runs']:
                r=row['result'];r['resources']['rss_max_kib']=0
                for sample in r['resources']['samples']:sample['rss_kib']=0;sample['runner_pids']=[]
                for attempt in r['attempts']:
                    attempt['resources']['rss_max_kib']=0
                    for sample in attempt['resources']['samples']:sample['rss_kib']=0;sample['runner_pids']=[]
        with self.assertRaisesRegex(AssertionError,'runner RSS unavailable'):
            self.run_fixture(mutate=zero)

    def test_resource_timestamp(self):
        trace={'interval_ms':250,'samples':[{'at_ms':2000,'rss_kib':1,'size':2,'runner_pids':[1]}],'rss_max_kib':1,'memory_bytes':2}
        with self.assertRaises(AssertionError):report.resource(trace,10)

class SelectionTests(unittest.TestCase):
    def select(self, variants):
        rows=[];summaries=[]
        for profile,good,refusal,errors,ms in variants:
            summaries.append({'id':profile,'correct_supported':len(good),'memory_bytes':100,'successful_median_ms':None})
            for q in ('a','b','c','outside'):
                rows.append({'profile':profile,'id':q,'repeat':1,'result':{'total_ms':ms,**({'error':'failure'} if q in errors else {})},'assessment':{'facts':'refusal' if q=='outside' and refusal else ('correct' if q in good else 'missing'),'semantic':'supported' if q in good else 'unknown'}})
        return report.select_profiles(rows,summaries)

    def test_faster_new_error_rejected(self):
        _,winner,_,_,candidates=self.select([('baseline',{'a','b'},True,set(),100),('bad',{'a','b'},True,{'c'},1)])
        self.assertEqual(winner['id'],'baseline');self.assertEqual(candidates[1]['new_error_pairs'],1)

    def test_lost_refusal_rejected(self):
        _,winner,_,_,candidates=self.select([('baseline',{'a','b'},True,set(),100),('bad',{'a','b'},False,set(),1)])
        self.assertEqual(winner['id'],'baseline');self.assertEqual(candidates[1]['lost_correct_refusal_pairs'],1)

    def test_disjoint_ineligible_cannot_suppress_speed(self):
        summaries,winner,common,population,candidates=self.select([('baseline',{'a','b'},True,set(),100),('good',{'a','b'},True,set(),50),('bad',{'c'},True,set(),1)])
        self.assertEqual(winner['id'],'good');self.assertEqual(common,{('a',1),('b',1)})
        self.assertEqual(population,['baseline','good']);self.assertEqual(candidates[2]['quality_loss'],1)
        self.assertIsNone(next(s for s in summaries if s['id']=='bad')['successful_median_ms'])
        self.assertEqual(winner['successful_median_ms'],50)

    def test_selected_allocation_breaks_quality_speed_tie_not_rss(self):
        rows=[{'profile':p,'id':'a','repeat':1,'result':{'total_ms':100},'assessment':{'facts':'correct','semantic':'supported'}} for p in ('baseline','smaller')]
        summaries=[{'id':'baseline','correct_supported':1,'memory_bytes':100,'rss_max_kib':10}, {'id':'smaller','correct_supported':1,'memory_bytes':80,'rss_max_kib':200}]
        _,winner,common,_,_=report.select_profiles(rows,summaries)
        self.assertEqual(winner['id'],'smaller');self.assertEqual(common,{('a',1)})

    def test_quality_outranks_speed(self):
        _,winner,common,_,_=self.select([('baseline',{'a','b'},True,set(),100),('fast',{'a','b'},True,set(),1),('quality',{'a','b','c'},True,set(),1000)])
        self.assertEqual(winner['id'],'quality');self.assertEqual(common,{('a',1),('b',1)})

if __name__=='__main__':unittest.main()
