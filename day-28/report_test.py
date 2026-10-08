import unittest
import report

class CitationContractTests(unittest.TestCase):
    def setUp(self):
        self.chunks=[{'source':'s','section':'section','chunk_id':'id','text':'Дословная строка\nс переносом.'}]
        self.answer={'answer':'Ответ','unknown':False,'clarification':'','sources':[{'source':'s','section':'section','chunk_id':'id','quote':'строка\nс переносом'}]}
    def test_exact_quote_accepted(self):
        report.check_answer(self.answer,self.chunks)
    def test_rephrased_quote_rejected(self):
        self.answer['sources'][0]['quote']='строка с переносом'
        with self.assertRaises(AssertionError):report.check_answer(self.answer,self.chunks)
    def test_foreign_source_rejected(self):
        self.answer['sources'][0]['source']='other'
        with self.assertRaises(AssertionError):report.check_answer(self.answer,self.chunks)
    def test_empty_substantive_sources_rejected(self):
        self.answer['sources']=[]
        with self.assertRaises(AssertionError):report.check_answer(self.answer,self.chunks)
    def test_unknown_has_no_fact_score(self):
        r={'answer':{'answer':'Не знаю.','unknown':True,'clarification':'Уточните','sources':[]}}
        self.assertEqual(report.checks(r,{'kind':'in_base'}, {})['facts'],'lost_answer')
        self.assertEqual(report.checks(r,{'kind':'out_of_base'}, {})['facts'],'refusal')
    def test_error_is_not_refusal(self):
        r={'error':'timeout'}
        self.assertEqual(report.checks(r,{'kind':'out_of_base'}, {})['facts'],'technical_error')

class CaptureProvenanceTests(unittest.TestCase):
    def test_real_capture_is_positive_control(self):
        data,_=report.build(report.HERE/'run.json',report.HERE/'semantic-review.json')
        self.assertEqual(data['summary']['total'],30)
        self.assertEqual(data['summary']['errors'],0)
    def test_tampered_data_is_rejected(self):
        import copy,hashlib,json,tempfile
        from pathlib import Path
        raw=json.loads((report.HERE/'run.json').read_text())
        review=json.loads((report.HERE/'semantic-review.json').read_text())
        def source_mismatch(d):d['runs'][0]['result']['attempts'][0]['request']['messages'][1]['content']='not actual context'
        def ranking_mismatch(d):d['runs'][0]['result']['context'][0]['similarity']=0.99
        def schema_mismatch(d):d['runs'][0]['result']['attempts'][0]['request']['format']['properties']['sources']['items']['anyOf'][0]['properties']['quote']['enum']=['not a literal quote']
        mutations=[lambda d:d['meta'].__setitem__('cloud_sha256','wrong'),lambda d:d['meta'].__setitem__('questions_sha256','wrong'),lambda d:d['meta'].__setitem__('index_sha256','wrong'),lambda d:d['meta'].__setitem__('cos_threshold',0.1),lambda d:d['runs'].pop(),source_mismatch,ranking_mismatch,schema_mismatch,lambda d:d['comparisons'][0].__setitem__('messages',[]),lambda d:d['runs'][0]['result']['answer'].__setitem__('answer','manually rewritten')]
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'run.json';s=Path(tmp)/'semantic.json'
            for change in mutations:
                d=copy.deepcopy(raw);change(d);encoded=json.dumps(d,ensure_ascii=False).encode();p.write_bytes(encoded);r=copy.deepcopy(review);r['run_sha256']=hashlib.sha256(encoded).hexdigest();s.write_text(json.dumps(r,ensure_ascii=False))
                with self.subTest(change=change.__name__),self.assertRaises((AssertionError,KeyError,IndexError)):
                    report.build(p,s)
    def test_fact_outcomes_are_distinct(self):
        q={'kind':'in_base','facts':[{'patterns':['one']},{'patterns':['two']}]}
        self.assertEqual(report.checks({'answer':{'unknown':False,'answer':'one'}},q,{})['facts'],'partial')
        self.assertEqual(report.checks({'answer':{'unknown':False,'answer':'other'}},q,{})['facts'],'missing')
        self.assertEqual(report.checks({'answer':{'unknown':False,'answer':'one two'}},q,{})['facts'],'correct')

if __name__=='__main__':unittest.main()
