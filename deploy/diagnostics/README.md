# Turn-state diagnostics

`turn_state_probe.py` runs on the existing deployment host. It reads account and
proxy settings from PostgreSQL, but never updates account settings. Do not run
multiple copies for the same accounts. A runtime-directory lock prevents duplicate
instances using that directory. Stop with SIGTERM; at most four requests are in
flight, without an unbounded queued batch (45-second HTTP timeout plus DB reads).

Pass JSON via `--config /path/config.json` (or stdin), together with
`--runtime-dir /var/lib/sub2api-turnstate`. Example (account IDs must be explicitly selected):

```json
{
  "account_ids": [1010],
  "models": ["gpt-6-astra", "gpt-5.6-sol"],
  "version": "0.154.0",
  "max_attempts": null,
  "concurrency": 4,
  "interval_seconds": 60,
  "renew_interval_seconds": 2700,
  "auto_renew": true,
  "start_with_proxy": true,
  "publish_shared_state": true,
  "redis_db": 0
}
```

`max_attempts: null` removes the attempt cap. `run_seconds` optionally bounds a
validation run in wall-clock time, not number of attempts. Omit it only when
deliberately running an ongoing, potentially billable diagnostic. A report
containing only lengths, routes, usage counts, timestamps and statuses is saved
as `events.jsonl` in the private runtime directory, rotating at 10 MiB with four
backups. `status.json` is an atomic owner-only snapshot of latest results, stopped
pairs and cooldowns. No raw state is persisted.

- Each account/model pair has a separate diagnostic session and in-memory state.
- Acquisition picks a random active, unexpired proxy from IP management, excluding
  the last proxy used for that account when alternatives exist. Distinct proxy
  entries do not by themselves prove distinct exit IPs.
- A 292 response is retained; subsequent verification and renewal use direct
  routing with that header. No account is bound to a proxy.
- With `publish_shared_state: true`, the direct verification uses a NEW synthetic
  session and a unique expected output. Publication requires completion, exact
  output, sent length 292 and a response-declared model matching the requested
  upstream model. A model mismatch or failed verification is not published.
- A returned 312 or an identical 292 never extends the recorded lifetime. A
  **different** 292 can replace it. The one-hour lifetime is a local conservative
  assumption, not a decoded or provider-verified expiry. No expired token is sent.
- Renewal defaults to 45 minutes. It is an attempt to obtain a new token, not a
  guarantee the provider will issue one. After expiry acquisition resumes.
- 429 applies account-wide backoff across both models, honoring Retry-After and
  keeping the same route for that retry. Consecutive defaults are 60, 60, 120,
  240, 480, 900 seconds (900 thereafter); a longer Retry-After wins. The affected
  model retries before its sibling after cooldown. Cooldowns, streaks and retry
  routes survive worker restarts. Other accounts continue. Authentication
  and configuration errors stop the affected account/model; no token refresh or
  credential mutation is attempted.
- Auth/config stopped pairs stay stopped after worker restart; review their
  problem before explicitly removing the matching entry from `status.json`
  while the service is stopped. Do not delete the whole checkpoint: that also
  clears outstanding rate-limit cooldowns. Transient database/transport failures
  retry with the normal interval and do not clear outstanding 429 streaks.
- Raw state and credentials never enter reports or command-line arguments.
  Worker-local state is lost at process exit. Verified synthetic state is stored
  in Redis with its ORIGINAL expiry, under
  `openai:probe-turn-state:v1:<account-id>:<model>`. Treat Redis and its persistence/
  backups as sensitive. Seven-day digest-only tombstones prevent re-observation
  after a worker restart from reissuing the same bytes with a fresh lifetime.
  Identical tokens do not refresh TTL or overwrite the current record.
- State length alone is not evidence of compute allocation or model quality.

## Real-user gateway consumption

`GATEWAY_SHARED_PROBE_TURN_STATE_ENABLED=true` explicitly enables consumption;
the default is false. When no private valid 292 is available, the HTTP, passthrough
and WS header builders read the synthetic pool with a 150ms deadline. They check
account ID, provider-account identity hash, exact upstream model, synthetic source,
verification flag, length and absolute expiry (at most one hour from first issue).
Unavailable/invalid/expired records fall back to the existing request path.

The gateway never writes real-user state to this pool. User-derived 292 remains
private to that account/model/client execution scope, and is preferred within
that scope. Session IDs, payloads, response-continuation ownership and WS socket
isolation are unchanged. Echoed synthetic tokens keep their original expiry and
do not become freshly issued private cache entries. Shared records are read anew
so updates/removal take effect at the next selection. An already-open WS socket
cannot acquire a different HTTP handshake header until a new connection is made;
usage audit reports the actual original handshake, not a later selection.

Cross-session verification is a bounded operational check, not proof of the
provider's opaque token semantics, compute tier or freedom from overload.

## Managed deployment

The included `sub2api-turnstate-probe.service` runs as the existing `ubuntu`
account with Docker access; paths and user are deployment-specific. Install the
script and reviewed config in `/opt/sub2api/diagnostics/`, then install the unit
in `/etc/systemd/system/`. Config contains only account IDs and settings; tokens
are read in memory from the existing DB. Start only after application health
checks pass. The unit restarts after failure with a five-minute delay, with a
three-start/30-minute limit, and stops all children on shutdown.

```sh
sudo systemctl status sub2api-turnstate-probe --no-pager
sudo journalctl -u sub2api-turnstate-probe --since '5 minutes ago' --no-pager
sudo systemctl stop sub2api-turnstate-probe
# Stop and prevent automatic start after reboot:
sudo systemctl disable --now sub2api-turnstate-probe
```

This is separate from the application container and has no PostgreSQL writes or
account/proxy configuration mutations; optional publication writes only its Redis
namespace. Disabling the gateway flag stops consumption independently of probing.
Rollback/stop it independently; an application rollback alone does not stop it.

Offline tests (no provider requests):

```sh
python3 -m unittest discover -s deploy/diagnostics -p 'test_*.py'
```
