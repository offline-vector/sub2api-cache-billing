#!/usr/bin/env python3
"""Passive admin log bridge. Never sends model requests or changes worker state."""
import argparse
import hashlib
import json
import math
import re
import signal
import subprocess
import threading
import uuid
from datetime import datetime, timezone
from pathlib import Path

LIMIT = 2000
MAX_TAIL_BYTES = 4 * 1024 * 1024
EVENT_KEY = 'openai:probe-admin:v1:events'
STATUS_KEY = 'openai:probe-admin:v1:status'
STRINGS = ('event', 'model', 'route', 'response_model', 'error_code', 'error_type',
           'transient_error', 'upstream_server')
INTEGERS = ('account_id', 'attempt', 'sent_state_length', 'received_state_length', 'http_status')
NUMBERS = ('elapsed_s', 'retry_after_s', 'wait_seconds')
BOOLEANS = ('completed', 'cross_session_check', 'shared_state_published', 'verification_output_matches')


def project_event(value):
    """Whitelist metadata, reject malformed types and never include bodies/state."""
    if not isinstance(value, dict) or not isinstance(value.get('time'), str):
        return None
    try:
        timestamp = datetime.fromisoformat(value['time'].replace('Z', '+00:00'))
        if timestamp.tzinfo is None:
            return None
    except ValueError:
        return None
    out = {'time': timestamp.isoformat()}
    for key in STRINGS:
        v = value.get(key)
        if isinstance(v, str) and re.fullmatch(r'[a-zA-Z0-9_./: ()-]{1,120}', v):
            out[key] = v
    for key in INTEGERS:
        v = value.get(key)
        if type(v) is int and 0 <= v <= 2**53-1:
            out[key] = v
    for key in NUMBERS:
        v = value.get(key)
        if type(v) in (int, float) and math.isfinite(v) and 0 <= v <= 2**53-1:
            out[key] = v
    for key in BOOLEANS:
        if type(value.get(key)) is bool:
            out[key] = value[key]
    # Stable across reloads/rotations; cursor contains no original state bytes.
    digest = hashlib.sha256(json.dumps(out, sort_keys=True).encode()).hexdigest()
    out['id'] = str(uuid.uuid5(uuid.NAMESPACE_OID, digest))
    return out


def recent_events(directory):
    events, seen = [], set()
    for suffix in ('', '.1', '.2', '.3', '.4'):
        path = directory / ('events.jsonl' + suffix)
        try:
            with path.open('rb') as stream:
                size = stream.seek(0, 2)
                offset = max(0, size - MAX_TAIL_BYTES)
                stream.seek(offset)
                if offset:
                    stream.readline()  # partial first line
                lines = stream.read(MAX_TAIL_BYTES).splitlines()
        except FileNotFoundError:
            continue
        for line in reversed(lines):
            if len(line) > 16384:
                continue
            try:
                event = project_event(json.loads(line))
            except (ValueError, UnicodeDecodeError):
                continue
            if event and event['id'] not in seen:
                seen.add(event['id'])
                events.append(event)
                if len(events) == LIMIT:
                    return events
    return events


EXPORT_LUA = '''
local v = cjson.decode(ARGV[1])
if #v.events > 2000 then return -1 end
redis.call('DEL', KEYS[1])
for _, event in ipairs(v.events) do
  redis.call('RPUSH', KEYS[1], cjson.encode(event))
end
redis.call('EXPIRE', KEYS[1], 604800)
redis.call('SET', KEYS[2], cjson.encode(v.status), 'EX', 45)
return #v.events
'''


def export_snapshot(directory, config_path, redis_db):
    config = json.loads(config_path.read_text())
    check = subprocess.run(['systemctl', 'is-active', '--quiet', 'sub2api-turnstate-probe.service'],
                           capture_output=True, timeout=3)
    status = {
        'running': check.returncode == 0,
        'updated_at': datetime.now(timezone.utc).isoformat(),
        'account_ids': [v for v in config.get('account_ids', []) if type(v) is int and 0 < v <= 2**53-1][:200],
        'models': [v for v in config.get('models', []) if v in ('gpt-6-astra', 'gpt-5.6-sol')],
        'concurrency': int(config.get('concurrency', 0)),
        'interval_seconds': float(config.get('interval_seconds', 0)),
    }
    snapshot = {'events': recent_events(directory), 'status': status}
    result = subprocess.run([
        'docker', 'exec', '-i', 'sub2api-redis', 'redis-cli', '--raw', '-n', str(redis_db), '-x',
        'EVAL', EXPORT_LUA, '2', EVENT_KEY, STATUS_KEY,
    ], input=json.dumps(snapshot, allow_nan=False), text=True, capture_output=True, timeout=5)
    if result.returncode or result.stdout.strip() != str(len(snapshot['events'])):
        raise RuntimeError('admin_log_export_failed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime-dir', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--redis-db', type=int, default=0)
    parser.add_argument('--once', action='store_true')
    args = parser.parse_args()
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    while not stop.is_set():
        try:
            export_snapshot(args.runtime_dir, args.config, args.redis_db)
        except Exception as exc:
            # Never log exception text: OS/provider errors may include private data.
            print('admin_log_export_unavailable:' + type(exc).__name__, flush=True)
            if args.once:
                raise SystemExit(1) from None
        if args.once:
            break
        stop.wait(10)


if __name__ == '__main__':
    main()
