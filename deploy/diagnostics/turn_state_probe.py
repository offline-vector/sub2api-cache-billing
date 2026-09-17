#!/usr/bin/env python3
"""Server-side turn-state diagnostic. Credentials/state stay in memory only.

Optional publication uses a separate synthetic-only bootstrap pool in Redis,
never the gateway's per-client execution-scope state cache.
Run on the deployment host; configuration comes from --config or stdin.
"""
import argparse
import fcntl
import json
import logging
import math
import os
import re
import subprocess
import sys
import time
import uuid
import tempfile
import hashlib
import random
import signal
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from email.utils import parsedate_to_datetime
from datetime import datetime, timezone
from logging.handlers import RotatingFileHandler
from pathlib import Path


class RuntimeState:
    """Single worker, bounded safe logs and durable cooldowns; no raw tokens."""

    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.lock = (self.directory / 'worker.lock').open('a')
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self.lock.close()
            raise RuntimeError('worker_already_running') from None
        self.path = self.directory / 'status.json'
        # Invalid persisted state fails closed instead of silently clearing 429.
        try:
            self.data = json.loads(self.path.read_text()) if self.path.exists() else {}
            if not isinstance(self.data, dict):
                raise ValueError('invalid_runtime_checkpoint')
        except Exception:
            self.lock.close()
            raise
        self.data.setdefault('cooldowns', {})
        self.data.setdefault('stopped', [])
        self.data.setdefault('latest', {})
        self.log_path = self.directory / 'events.jsonl'
        self.handler = RotatingFileHandler(self.log_path, maxBytes=10*1024*1024,
                                           backupCount=4, encoding='utf-8')
        self.handler.setFormatter(logging.Formatter('%(message)s'))

    def save(self):
        with tempfile.NamedTemporaryFile(mode='w', dir=self.directory, delete=False) as temp:
            json.dump(self.data, temp)
            temp.flush()
            os.fsync(temp.fileno())
        os.replace(temp.name, self.path)

    def emit(self, value):
        value = dict(value, time=datetime.now(timezone.utc).isoformat())
        if 'account_id' in value and 'model' in value and 'event' not in value:
            self.data['latest'][f"{value['account_id']}:{value['model']}"] = value
        self.data['updated_at'] = value['time']
        self.data['last_event'] = value
        self.save()
        line = json.dumps(value)
        self.handler.emit(logging.LogRecord('probe', logging.INFO, '', 0, line, (), None))
        print(line, flush=True)

    def cooldown(self, key, delay, streak, route):
        self.data['cooldowns'][str(key[0])] = {
            'until': time.time()+delay, 'streak': streak, 'model': key[1], 'route': route,
        }
        self.save()

    def clear_cooldown(self, account_id):
        self.data['cooldowns'].pop(str(account_id), None)
        self.save()

    def stop_pair(self, key):
        value = list(key)
        if value not in self.data['stopped']:
            self.data['stopped'].append(value)
        self.save()

    def close(self):
        self.handler.close()
        self.lock.close()


def query_json(sql):
    result = subprocess.run([
        'docker', 'exec', '-i', 'sub2api-postgres', 'sh', '-c',
        'exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"',
    ], input=sql, text=True, capture_output=True, timeout=15)
    if result.returncode or not result.stdout.strip():
        raise RuntimeError('database_read_failed')
    return json.loads(result.stdout)


def query_account(account_id):
    sql = ("SELECT json_build_object('id',id,'credentials',credentials,"
           "'extra',extra,'proxy_id',proxy_id) FROM accounts "
           f"WHERE id={int(account_id)} AND platform='openai' AND type='oauth' "
           "AND status='active' AND deleted_at IS NULL;")
    return query_json(sql)


def active_proxy_ids():
    return query_json("SELECT COALESCE(json_agg(id), '[]') FROM proxies "
                      "WHERE status='active' AND deleted_at IS NULL "
                      "AND (expires_at IS NULL OR expires_at > now()) "
                      "AND protocol IN ('socks5','http','https');")


def proxy_config(proxy_id):
    proxy = query_json(f"SELECT row_to_json(p) FROM proxies p WHERE id={int(proxy_id)} "
                       "AND status='active' AND deleted_at IS NULL "
                       "AND (expires_at IS NULL OR expires_at > now());")
    protocol = {'socks5': 'socks5h', 'http': 'http', 'https': 'https'}.get(proxy['protocol'])
    if not protocol or not 1 <= int(proxy['port']) <= 65535:
        raise ValueError('invalid_proxy_configuration')
    host = proxy['host']
    if any(c in host for c in '/@\n\r?#'):
        raise ValueError('invalid_proxy_host')
    if ':' in host and not host.startswith('['):
        host = '['+host+']'
    return ['proxy = '+json.dumps(f"{protocol}://{host}:{int(proxy['port'])}"),
            'proxy-user = ' + json.dumps((proxy.get('username') or '') + ':' + (proxy.get('password') or ''))]


def valid_state(context, now):
    return context.get('state', '') if now < context.get('expires_at', 0) else ''


def capture_state(context, state, now):
    if len(state) != 292:
        return False
    digest = hashlib.sha256(state.encode()).hexdigest()
    # Lifetime starts on first observation, not the most recent echo. Remember
    # previously observed bytes for this diagnostic session, even after expiry.
    seen = context.setdefault('seen', {})
    expires = seen.setdefault(digest, now+3600)
    if expires <= now or context.get('state') == state:
        return False
    context.update(state=state, expires_at=expires)
    return True


def parse_response(raw):
    status, headers = None, {}
    remaining = raw.replace('\r\n', '\n')
    # curl --include can include the CONNECT response or informational 1xx.
    # Do not interpret model-generated body text as a response header.
    while remaining.startswith('HTTP/'):
        block, separator, remaining = remaining.partition('\n\n')
        lines = block.splitlines()
        match = re.fullmatch(r'HTTP/[\d.]+ (\d+)(?: .*)?', lines[0])
        if not match or not separator:
            break
        status = int(match.group(1))
        headers = {}
        for line in lines[1:]:
            key, sep, value = line.partition(':')
            if sep:
                headers[key.strip().lower()] = value.strip()
    return status, headers, remaining


def probe(account_id, model, version, proxy_id=None, state_context=None, expected_text=None):
    account = query_account(account_id)
    credentials = account['credentials']
    if not credentials.get('access_token'):
        return {'account_id': account_id, 'error': 'missing_access_token'}
    if account['proxy_id'] is not None:
        return {'account_id': account_id, 'error': 'expected_direct_route'}
    mapping = credentials.get('model_mapping') or {}
    if mapping and mapping.get(model) != model:
        return {'account_id': account_id, 'error': 'model_not_allowed_or_remapped'}
    if state_context is None:
        state_context = {}
    provider_id = credentials.get('chatgpt_account_id', '')
    provider_hash = hashlib.sha256(provider_id.encode()).hexdigest() if provider_id else ''
    if state_context.get('provider_account_hash') and state_context['provider_account_hash'] != provider_hash:
        state_context.clear() # local account row was re-authorized to another provider account
    state_context['provider_account_hash'] = provider_hash
    session = state_context.setdefault('session', str(uuid.uuid4()))
    headers = {
        'Authorization': 'Bearer ' + credentials['access_token'],
        'Content-Type': 'application/json',
        'Accept': 'text/event-stream',
        'OpenAI-Beta': 'responses=experimental',
        'originator': 'codex-tui',
        'version': version,
        'User-Agent': f'codex-tui/{version} (Ubuntu 22.4.0; x86_64) xterm-256color',
        'session_id': session,
        'chatgpt-account-id': credentials.get('chatgpt_account_id', ''),
    }
    retained = valid_state(state_context, time.monotonic())
    if retained:
        headers['x-codex-turn-state'] = retained
    payload = {
        'model': model, 'stream': True, 'store': False,
        'instructions': 'Reply briefly.',
        'input': [{'role': 'user', 'content': [{'type': 'input_text', 'text':
            'Reply exactly with '+expected_text if expected_text else 'Reply with OK only.'}]}],
        'reasoning': {'effort': 'low'},
    }
    # Pass secrets through stdin, not command-line arguments or temporary files.
    config = '\n'.join([
        'url = "https://chatgpt.com/backend-api/codex/responses"',
        'request = "POST"', 'silent', 'show-error', 'include',
        'connect-timeout = 10', 'max-time = 45',
        *['header = ' + json.dumps(k + ': ' + v) for k, v in headers.items() if v],
        'data-binary = ' + json.dumps(json.dumps(payload)),
        'noproxy = ""',
        *(proxy_config(proxy_id) if proxy_id is not None else ['proxy = ""']),
    ])
    start = time.monotonic()
    result = subprocess.run(['curl', '--disable', '--config', '-'], input=config,
                            capture_output=True, text=True, timeout=50)
    out = {'account_id': account_id, 'model': model, 'route': f'proxy:{proxy_id}' if proxy_id else 'direct',
           'sent_state_length': len(headers.get('x-codex-turn-state', '')), 'elapsed_s': round(time.monotonic()-start, 2),
           'curl_exit': result.returncode}
    out['http_status'], response_headers, body = parse_response(result.stdout)
    server = response_headers.get('server', '')
    if re.fullmatch(r'[a-zA-Z0-9_./: ()-]{1,120}', server):
        out['upstream_server'] = server
    # Metadata only for non-SSE error responses; never persist message/body text.
    if out['http_status'] and out['http_status'] >= 400:
        try:
            document = json.loads(body)
            error = document.get('error') if isinstance(document, dict) else None
            if isinstance(error, dict):
                for key in ('code', 'type'):
                    value = error.get(key)
                    if isinstance(value, str) and re.fullmatch(r'[a-zA-Z0-9_.-]{1,120}', value):
                        out['error_' + key] = value
        except ValueError:
            pass
    if 'retry-after' in response_headers:
        value = response_headers['retry-after']
        try:
            out['retry_after_s'] = max(0, float(value))
        except ValueError:
            try:
                out['retry_after_s'] = max(0, (parsedate_to_datetime(value) - datetime.now(timezone.utc)).total_seconds())
            except (ValueError, TypeError):
                pass
    state = response_headers.get('x-codex-turn-state', '')
    out['received_state_length'] = len(state)
    out['new_292'] = False
    if state and out['http_status'] == 200:
        out['new_292'] = capture_state(state_context, state, time.monotonic())
    out['preferred_remaining_s'] = max(0, int(state_context.get('expires_at', 0)-time.monotonic()))
    out['completed'] = False
    response_text = ''
    delta_text = ''
    for line in body.splitlines():
        if not line.startswith('data:'):
            continue
        try:
            event = json.loads(line[5:].strip())
        except (ValueError, TypeError):
            continue
        kind = event.get('type', '')
        if kind == 'response.output_text.delta':
            delta_text = (delta_text + str(event.get('delta', '')))[:8192]
        if kind == 'response.completed':
            out['completed'] = True
            out['response_model'] = event.get('response', {}).get('model')
            usage = event.get('response', {}).get('usage', {})
            out['usage'] = {k: usage.get(k) for k in ('input_tokens', 'output_tokens', 'total_tokens')}
            for item in event.get('response', {}).get('output', []):
                for part in item.get('content', []):
                    if part.get('type') == 'output_text':
                        response_text = (response_text + str(part.get('text', '')))[:8192]
        if kind in ('error', 'response.failed'):
            error = event.get('error') or event.get('response', {}).get('error') or {}
            out['error_code'] = error.get('code') or event.get('code')
            out['error_type'] = error.get('type')
    if expected_text:
        out['verification_output_matches'] = (response_text or delta_text).strip() == expected_text
    return out


# Credentials and raw state are passed over stdin to redis-cli, never OS args.
# This writer is intentionally absent from the real-user gateway.
PUBLISH_STATE_LUA = '''
local v = cjson.decode(ARGV[1])
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000 + math.floor(tonumber(clock[2])/1000)
if v.source ~= 'synthetic_probe' or v.cross_session_verified ~= true or #v.state ~= 292 then return -1 end
if v.issued_at_ms > now or v.expires_at_ms-v.issued_at_ms > 3600000 then return -1 end
local first = redis.call('GET', KEYS[2])
if first then
  v.issued_at_ms = math.min(v.issued_at_ms, tonumber(first))
  v.expires_at_ms = math.min(v.expires_at_ms, v.issued_at_ms+3600000)
else
  redis.call('SET', KEYS[2], tostring(v.issued_at_ms), 'EX', 604800, 'NX')
end
if v.expires_at_ms <= now then return -2 end
local old = redis.call('GET', KEYS[1])
if old then
  local previous = cjson.decode(old)
  if previous.state == v.state or previous.issued_at_ms >= v.issued_at_ms then return 0 end
end
redis.call('SET', KEYS[1], cjson.encode(v), 'PXAT', math.floor(v.expires_at_ms))
return 1
'''


def publish_shared_state(account_id, model, state, expires_at, provider_hash, redis_db=0):
    if len(state) != 292 or model not in ('gpt-6-astra', 'gpt-5.6-sol') or not re.fullmatch('[a-f0-9]{64}', provider_hash):
        raise ValueError('invalid_shared_probe_state')
    remaining = expires_at-time.monotonic()
    if not 0 < remaining <= 3600:
        return False
    expires_ms = int((time.time()+remaining)*1000)
    record = {'account_id': account_id, 'model': model, 'state': state,
              'source': 'synthetic_probe', 'cross_session_verified': True,
              'provider_account_hash': provider_hash,
              'issued_at_ms': expires_ms-3600000, 'expires_at_ms': expires_ms}
    key = f'openai:probe-turn-state:v1:{int(account_id)}:{model}'
    seen_key = key+':seen:'+hashlib.sha256(state.encode()).hexdigest()
    result = subprocess.run([
        'docker', 'exec', '-i', 'sub2api-redis',
        'redis-cli', '--raw', '-n', str(int(redis_db)), '-x',
        'EVAL', PUBLISH_STATE_LUA, '2', key, seen_key,
    ], input=json.dumps(record), capture_output=True, text=True, timeout=15)
    if result.returncode or result.stdout.strip() not in ('1', '0', '-2'):
        raise RuntimeError('shared_state_publish_failed')
    return result.stdout.strip() == '1'


def verify_and_publish(account_id, model, version, context, redis_db=0):
    state = valid_state(context, time.monotonic())
    if len(state) != 292:
        return {'account_id': account_id, 'model': model, 'error': 'state_expired_before_verification'}
    # Different synthetic session, unique expected output, no donor history.
    # This checks a bounded sample, not a proof of provider token semantics.
    verification = {'state': state, 'expires_at': context['expires_at'],
                    'provider_account_hash': context.get('provider_account_hash', ''),
                    'session': str(uuid.uuid4())}
    marker = 'STATE_CHECK_'+uuid.uuid4().hex
    out = probe(account_id, model, version, None, verification, marker)
    out['cross_session_check'] = True
    out['shared_state_published'] = False
    if out.get('completed') and out.get('sent_state_length') == 292 and out.get('verification_output_matches') and out.get('response_model') == model and verification.get('provider_account_hash'):
        out['shared_state_published'] = publish_shared_state(account_id, model, state, context['expires_at'], verification['provider_account_hash'], redis_db)
    return out


def is_rate_limited(out):
    return out.get('http_status') == 429 or out.get('error_code') in (
        'rate_limit_exceeded', 'rate_limit_error', 'usage_limit_reached', 'too_many_requests')


def rate_delay(out, streak, interval):
    retry_after = out.get('retry_after_s', 0)
    if not math.isfinite(retry_after):
        retry_after = 0
    return max(retry_after, interval, min(900, 30*2**min(streak-1, 5)))


def choose_proxy(ids, previous):
    choices = [p for p in ids if p != previous] or ids
    return random.SystemRandom().choice(choices) if choices else None


def select_ready(active, due, account_due, counts, rate_retry, now, workers):
    ready, used = [], set()
    for key in sorted(active, key=lambda k: (due[k], counts[k], k)):
        account = key[0]
        if account in rate_retry and key != rate_retry[account]:
            continue
        if due[key] <= now and account_due[account] <= now and account not in used:
            ready.append(key)
            used.add(account)
            if len(ready) == workers:
                break
    return ready


def run(config, runtime):
    ids = list(dict.fromkeys(int(a) for a in config['account_ids']))
    models = list(dict.fromkeys(config.get('models', ['gpt-6-astra', 'gpt-5.6-sol'])))
    if not ids or len(ids) > 200 or not models or set(models)-{'gpt-6-astra','gpt-5.6-sol'}:
        raise ValueError('invalid_probe_scope')
    # None means no attempt-count cap, as explicitly requested. No hidden retries.
    attempts = config.get('max_attempts')
    if attempts is not None and (not isinstance(attempts, int) or attempts < 1):
        raise ValueError('invalid_attempt_limit')
    workers = int(config.get('concurrency', 4))
    if not 1 <= workers <= 4:
        raise ValueError('invalid_concurrency')
    interval = max(30, float(config.get('interval_seconds', 60)))
    renew_interval = max(60, min(2700, float(config.get('renew_interval_seconds', 2700))))
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    keys = [(a,m) for a in ids for m in models]
    counts = {k: 0 for k in keys}
    contexts = {k: {} for k in keys}
    due = {k: 0 for k in keys}
    route = {k: None for k in keys}
    seeking = set(keys) if config.get('start_with_proxy', True) else set()
    verifying = set()
    account_due = {a: 0 for a in ids}
    rate_counts = {a: 0 for a in ids}
    previous_proxy = {a: None for a in ids}
    active = set(keys) - {tuple(k) for k in runtime.data['stopped']}
    rate_retry = {}
    for account, saved in runtime.data['cooldowns'].items():
        a = int(account)
        key = (a, saved['model'])
        if key not in active:
            continue
        account_due[a] = time.monotonic()+max(0, saved['until']-time.time())
        rate_counts[a] = saved['streak']
        route[key] = saved['route']
        previous_proxy[a] = saved['route']
        rate_retry[a] = key
        seeking.discard(key)
    emit = runtime.emit
    emit({'event': 'batch_start', 'report': str(runtime.log_path), 'account_ids': ids, 'models': models,
          'concurrency': workers, 'max_attempts': attempts, 'interval_seconds': interval,
          'renew_interval_seconds': renew_interval,
          'synthetic_shared_pool_enabled': bool(config.get('publish_shared_state', False))})
    def run_one(key):
        a, model = key
        try:
            if key in verifying and config.get('publish_shared_state', False):
                out = verify_and_publish(a, model, config['version'], contexts[key], config.get('redis_db', 0))
            else:
                out = probe(a, model, config['version'], route[key], contexts[key])
        except Exception as exc:
            out = {'account_id': a, 'transient_error': type(exc).__name__}
        out['model'] = model
        return out
    deadline = time.monotonic()+float(config.get('run_seconds', 0)) if config.get('run_seconds') else float('inf')
    with ThreadPoolExecutor(max_workers=workers) as pool:
        while active and not stop.is_set() and time.monotonic() < deadline:
            active = {k for k in active if attempts is None or counts[k] < attempts}
            now = time.monotonic()
            ready = select_ready(active, due, account_due, counts, rate_retry, now, workers)
            if not ready:
                stop.wait(1)
                continue
            for k in ready:
                if contexts[k].get('state') and not valid_state(contexts[k], now):
                    verifying.discard(k)
                    if k[0] not in rate_retry:
                        seeking.add(k)
            # Only proxy-seeking requests pick a random current inventory entry.
            # A 429 preserves the route and pauses BOTH models of that account.
            try:
                proxies = active_proxy_ids() if any(k in seeking for k in ready) else []
            except Exception as exc:
                emit({'event': 'proxy_inventory_unavailable', 'error_type': type(exc).__name__})
                stop.wait(interval)
                continue
            for k in ready:
                if k in seeking:
                    if not proxies:
                        emit({'event': 'no_active_proxy', 'account_id': k[0], 'model': k[1]})
                        due[k] = now+interval
                        continue
                    route[k] = choose_proxy(proxies, previous_proxy[k[0]])
                    previous_proxy[k[0]] = route[k]
            ready = [k for k in ready if due[k] <= now]
            if stop.is_set() or time.monotonic() >= deadline:
                break
            futures = {pool.submit(run_one, k): k for k in ready}
            for future in as_completed(futures):
                k = futures[future]
                out = future.result()
                a = out['account_id']
                counts[k] += 1
                out['attempt'] = counts[k]
                now = time.monotonic()
                due[k] = now+interval
                account_due[a] = now+interval
                if is_rate_limited(out):
                    rate_counts[a] += 1
                    delay = rate_delay(out, rate_counts[a], interval)
                    account_due[a] = now+delay
                    rate_retry[a] = k
                    seeking.discard(k) # retry this same route, not a new IP
                    # Persist before emitting or scheduling anything else.
                    runtime.cooldown(k, delay, rate_counts[a], route[k])
                    emit(out)
                    emit({'event': 'rate_limit_backoff', 'account_id': a, 'wait_seconds': delay, 'route_unchanged': True})
                    continue
                emit(out)
                if out.get('transient_error') or out.get('curl_exit') or out.get('http_status', 0) in (None, 500, 502, 503, 504):
                    if route[k] is not None and a not in rate_retry:
                        seeking.add(k)
                    continue
                rate_counts[a] = 0
                rate_retry.pop(a, None)
                runtime.clear_cooldown(a)
                if out.get('error') or out.get('http_status') in (400, 401, 403, 404):
                    active.remove(k)
                    runtime.stop_pair(k)
                    emit({'event': 'account_model_stopped', 'account_id': a, 'model': k[1], 'reason': 'configuration_or_auth_error'})
                    continue
                if k in verifying:
                    emit({'event': 'direct_replay_checked', 'account_id': a,
                          'model': k[1],
                          'sent_state_length': out.get('sent_state_length'),
                          'received_state_length': out.get('received_state_length'),
                          'completed': out.get('completed'), 'response_model': out.get('response_model')})
                    verifying.discard(k)
                if valid_state(contexts[k], now):
                    seeking.discard(k)
                    if route[k] is not None or (out.get('new_292') and not out.get('cross_session_check') and config.get('publish_shared_state', False)):
                        route[k] = None
                        verifying.add(k)
                    elif not config.get('auto_renew', True):
                        active.remove(k)
                    else:
                        # Echoed 292/returned 312 never extend retained expiry.
                        remaining = contexts[k]['expires_at']-now
                        due[k] = now+min(renew_interval, max(interval, remaining-300))
                else:
                    seeking.add(k)
        emit({'event': 'batch_stopped' if active else 'batch_complete',
              'attempts': {f'{a}:{m}': v for (a,m),v in counts.items()}})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path)
    parser.add_argument('--runtime-dir', type=Path, required=True)
    args = parser.parse_args()
    with (args.config.open() if args.config else sys.stdin) as source:
        config = json.load(source)
    runtime = RuntimeState(args.runtime_dir)
    try:
        run(config, runtime)
    finally:
        runtime.close()


if __name__ == '__main__':
    main()
