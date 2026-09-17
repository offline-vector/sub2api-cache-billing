import unittest
import tempfile
import json
from pathlib import Path
from unittest.mock import patch

import turn_state_probe as probe


class ProbeTest(unittest.TestCase):
    def test_non_sse_429_exports_only_error_metadata(self):
        account = {'proxy_id':None,'credentials':{'access_token':'fake-token','model_mapping':{}}}
        response = type('Result',(),{'returncode':0,'stdout':'HTTP/2 429\nServer: cloudflare\nRetry-After: 180\n\n{"error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"SECRET"}}'})()
        with patch.object(probe,'query_account',return_value=account), patch.object(probe.subprocess,'run',return_value=response):
            out = probe.probe(1,'gpt-6-astra','0.154.0')
        self.assertEqual(out['error_code'],'rate_limit_exceeded')
        self.assertEqual(out['error_type'],'rate_limit_error')
        self.assertEqual(out['upstream_server'],'cloudflare')
        self.assertEqual(out['retry_after_s'],180)
        self.assertTrue(probe.is_rate_limited(out))
        self.assertNotIn('SECRET',str(out))

    def test_proxy_changes_between_attempts(self):
        for _ in range(20):
            self.assertNotEqual(probe.choose_proxy([28, 29, 30], 29), 29)
        self.assertEqual(probe.choose_proxy([28], 28), 28)
        self.assertIsNone(probe.choose_proxy([], None))

    def test_echo_does_not_renew_or_replace_292_with_312(self):
        context = {}
        self.assertTrue(probe.capture_state(context, 'a'*292, 100))
        self.assertFalse(probe.capture_state(context, 'a'*292, 200))
        self.assertFalse(probe.capture_state(context, 'b'*312, 300))
        self.assertEqual(context['expires_at'], 3700)
        self.assertEqual(probe.valid_state(context, 3699), 'a'*292)
        self.assertEqual(probe.valid_state(context, 3700), '')
        self.assertFalse(probe.capture_state(context, 'a'*292, 4000))
        self.assertTrue(probe.capture_state(context, 'c'*292, 4001))
        self.assertEqual(context['expires_at'], 7601)

    def test_state_does_not_cross_context(self):
        first, second = {}, {}
        probe.capture_state(first, 'x'*292, 1)
        self.assertEqual(probe.valid_state(second, 2), '')

    def test_429_respects_retry_after(self):
        self.assertTrue(probe.is_rate_limited({'http_status': 429}))
        self.assertEqual(probe.rate_delay({'retry_after_s': 1800}, 1, 60), 1800)
        self.assertEqual(probe.rate_delay({}, 9, 60), 900)
        self.assertEqual([probe.rate_delay({}, i, 60) for i in range(1, 9)],
                         [60, 60, 120, 240, 480, 900, 900, 900])
        self.assertEqual(probe.rate_delay({'retry_after_s': float('inf')}, 1, 60), 60)

    def test_scheduler_bounds_queue_and_isolates_account_429(self):
        keys = {(a, m) for a in range(1, 25) for m in ('gpt-6-astra', 'gpt-5.6-sol')}
        due = dict.fromkeys(keys, 0)
        account_due = dict.fromkeys(range(1, 25), 0)
        counts = dict.fromkeys(keys, 0)
        retry = {1: (1, 'gpt-6-astra')}
        account_due[1] = 180
        ready = probe.select_ready(keys, due, account_due, counts, retry, 100, 4)
        self.assertEqual(len(ready), 4)
        self.assertEqual(len({k[0] for k in ready}), 4)
        self.assertFalse(any(k[0] == 1 for k in ready))
        ready = probe.select_ready(keys, due, account_due, counts, retry, 180, 4)
        self.assertIn((1, 'gpt-6-astra'), ready)
        self.assertNotIn((1, 'gpt-5.6-sol'), ready)

    def test_runtime_lock_cooldown_and_auth_stop_survive_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = probe.RuntimeState(directory)
            with self.assertRaisesRegex(RuntimeError, 'worker_already_running'):
                probe.RuntimeState(directory)
            with patch.object(probe.time, 'time', return_value=1000):
                runtime.cooldown((1010, 'gpt-6-astra'), 1800, 4, 28)
            runtime.stop_pair((537, 'gpt-5.6-sol'))
            runtime.emit({'account_id': 1010, 'model': 'gpt-6-astra', 'http_status': 429})
            runtime.close()
            restarted = probe.RuntimeState(directory)
            saved = restarted.data['cooldowns']['1010']
            self.assertEqual(saved, {'until': 2800, 'streak': 4, 'model': 'gpt-6-astra', 'route': 28})
            self.assertIn([537, 'gpt-5.6-sol'], restarted.data['stopped'])
            self.assertEqual(restarted.data['latest']['1010:gpt-6-astra']['http_status'], 429)
            self.assertEqual(restarted.path.stat().st_mode & 0o777, 0o600)
            restarted.clear_cooldown(1010)
            self.assertEqual(json.loads(restarted.path.read_text())['cooldowns'], {})
            restarted.close()

    def test_invalid_checkpoint_does_not_clear_cooldown(self):
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, 'status.json').write_text('{invalid')
            with self.assertRaises(ValueError):
                probe.RuntimeState(directory)

    def test_parse_only_headers(self):
        raw = ('HTTP/1.1 200 Connection established\r\n\r\n'
               'HTTP/2 200\r\nX-Codex-Turn-State: good\r\n\r\n'
               'data: {"type":"response.completed"}\nx-codex-turn-state: bad')
        status, headers, body = probe.parse_response(raw)
        self.assertEqual(status, 200)
        self.assertEqual(headers['x-codex-turn-state'], 'good')
        self.assertIn('x-codex-turn-state: bad', body)

    def test_proxy_comes_from_inventory(self):
        with patch.object(probe, 'query_json', return_value={
            'protocol': 'socks5', 'host': '2001:db8::1', 'port': 20001,
            'username': 'name', 'password': 'not-real',
        }) as query:
            config = probe.proxy_config(123)
        self.assertIn('id=123', query.call_args[0][0])
        self.assertIn('socks5h://[2001:db8::1]:20001', config[0])

    def test_probe_echoes_retained_state_on_direct_request(self):
        context = {'session': 'isolated-probe-session'}
        probe.capture_state(context, 's'*292, 100)
        account = {'proxy_id': None, 'credentials': {'access_token': 'fake-token', 'model_mapping': {}}}
        response = type('Result', (), {'returncode': 0, 'stdout': 'HTTP/2 200\n\ndata: {"type":"response.completed","response":{"model":"gpt-6-astra"}}'})()
        with patch.object(probe, 'query_account', return_value=account), \
             patch.object(probe.time, 'monotonic', return_value=101), \
             patch.object(probe.subprocess, 'run', return_value=response) as run:
            out = probe.probe(1, 'gpt-6-astra', '0.154.0', None, context)
        config = run.call_args.kwargs['input']
        self.assertIn('x-codex-turn-state: '+'s'*292, config)
        self.assertIn('proxy = ""', config)
        self.assertEqual(run.call_args.args[0], ['curl', '--disable', '--config', '-'])
        self.assertEqual(out['sent_state_length'], 292)
        self.assertNotIn('s'*292, str(out))

    def test_shared_publication_requires_cross_session_completion_exact_model_and_output(self):
        context = {'session':'donor-session', 'state':'s'*292, 'expires_at':3700, 'provider_account_hash':'a'*64}
        success = {'completed':True, 'sent_state_length':292, 'response_model':'gpt-6-astra', 'verification_output_matches':True}
        for fields, allowed in [(success,True), ({**success,'completed':False},False),
                                ({**success,'response_model':'gpt-5.6-sol'},False),
                                ({**success,'verification_output_matches':False},False)]:
            with patch.object(probe.time,'monotonic',return_value=100), \
                 patch.object(probe,'probe',return_value=dict(fields)) as request, \
                 patch.object(probe,'publish_shared_state',return_value=True) as publish:
                out = probe.verify_and_publish(1010,'gpt-6-astra','0.154.0',context)
            self.assertEqual(publish.called,allowed)
            self.assertEqual(out['shared_state_published'],allowed)
            self.assertIsNone(request.call_args.args[3]) # direct
            sent_context = request.call_args.args[4]
            self.assertNotEqual(sent_context['session'],'donor-session')
            self.assertEqual(sent_context['state'],context['state'])
            self.assertTrue(request.call_args.args[5].startswith('STATE_CHECK_'))
            self.assertEqual(context['session'],'donor-session')

    def test_shared_state_is_stdin_only_and_keeps_original_expiry(self):
        response = type('Result',(),{'returncode':0,'stdout':'1\n'})()
        with patch.object(probe.time,'monotonic',return_value=200), \
             patch.object(probe.time,'time',return_value=10000), \
             patch.object(probe.subprocess,'run',return_value=response) as run:
            self.assertTrue(probe.publish_shared_state(1010,'gpt-6-astra','s'*292,3700,'a'*64))
        record = json.loads(run.call_args.kwargs['input'])
        self.assertEqual(record['expires_at_ms'],13500000)
        self.assertEqual(record['issued_at_ms'],9900000)
        self.assertTrue(record['cross_session_verified'])
        self.assertNotIn('s'*292,str(run.call_args.args))
        self.assertIn('openai:probe-turn-state:v1:1010:gpt-6-astra',run.call_args.args[0])


if __name__ == '__main__':
    unittest.main()
