import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import turn_state_log_export as export


class LogExportTest(unittest.TestCase):
    def test_projection_is_metadata_only_and_stable(self):
        source = {'time':'2026-09-17T12:00:00Z', 'account_id':1010, 'model':'gpt-6-astra',
                  'received_state_length':312, 'http_status':429, 'route':'proxy:28',
                  'retry_after_s':60, 'state':'SECRET', 'access_token':'SECRET',
                  'prompt':'SECRET', 'credentials':{'secret':'SECRET'},
                  'completed':False, 'error_code':'rate_limit_exceeded'}
        got = export.project_event(source)
        self.assertEqual(got['received_state_length'],312)
        self.assertEqual(got['http_status'],429)
        self.assertEqual(got['id'],export.project_event(source)['id'])
        self.assertNotIn('SECRET',json.dumps(got))
        self.assertNotIn('prompt',got)
        self.assertIsNone(export.project_event({'time':'invalid'}))
        self.assertIsNone(export.project_event({'time':'2026-09-17'}))
        malformed = export.project_event({**source,'error_code':'secret\ncontent','retry_after_s':float('inf'),
                                          'sent_state_length':True,'elapsed_s':-1,'completed':'true'})
        for key in ('error_code','retry_after_s','sent_state_length','elapsed_s','completed'):
            self.assertNotIn(key,malformed)

    def test_rotation_retention_partial_lines_and_newest_first(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            def row(i):
                return json.dumps({'time':'2026-09-17T12:00:00Z','attempt':i,'http_status':200})+'\n'
            (directory/'events.jsonl.1').write_text(''.join(row(i) for i in range(2100)))
            (directory/'events.jsonl').write_text(row(2100)+row(2101)+'{"partial":')
            got=export.recent_events(directory)
            self.assertEqual(len(got),2000)
            self.assertEqual([v['attempt'] for v in got[:3]],[2101,2100,2099])
            self.assertEqual(got[-1]['attempt'],102)

    def test_export_never_requests_models_or_modifies_worker(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp)
            config=directory/'config.json'
            config.write_text(json.dumps({'account_ids':[1010],'models':['gpt-6-astra'],
                                          'concurrency':4,'interval_seconds':60,'secret':'SECRET'}))
            result=type('Result',(),{'returncode':0,'stdout':'0\n'})()
            with patch.object(export.subprocess,'run',return_value=result) as run:
                export.export_snapshot(directory,config,0)
            self.assertEqual(run.call_count,2)
            self.assertEqual(run.call_args_list[0].args[0],['systemctl','is-active','--quiet','sub2api-turnstate-probe.service'])
            call=run.call_args_list[1]
            self.assertNotIn('SECRET',call.kwargs['input'])
            self.assertNotIn('curl',str(run.call_args_list))
            self.assertNotIn('restart',str(run.call_args_list))
            self.assertEqual(json.loads(call.kwargs['input'])['status']['account_ids'],[1010])


if __name__ == '__main__':
    unittest.main()
