# Admin attempt logs

Admin UI/API and passive exporter deployed on 2026-09-17 after user approval.
Image: `sub2api:0.2.5-probe-logs-20260917a`. Not pushed to GitHub.

The admin usage screen has a read-only **尝试日志 / Probe attempt logs** dialog.
It supports account/model filters, 50-entry cursor pages, optional 10-second
refresh and a maximum of 2,000 retained entries. History browsing pauses
refresh. Returned length 312 is red in both the dialog and admin usage rows;
length is not a claim about model quality or upstream risk-control status.

`GET /api/v1/admin/usage/probe-logs` uses the existing admin authentication.
Only typed metadata is exposed. Raw state, credentials, prompts and response
bodies are not exported. There is no manual probe or proxy-selection endpoint.

`turn_state_log_export.py` independently reads the existing worker's rotated
JSONL files and publishes sanitized metadata to two dedicated Redis keys:

- `openai:probe-admin:v1:events`: newest first, max 2,000, TTL seven days.
- `openai:probe-admin:v1:status`: process status, TTL 45 seconds.

The optional `sub2api-turnstate-logs.service` runs this passive exporter every
10 seconds. It neither sends model requests nor changes worker checkpoints,
proxy bindings or scheduling. Redis/export failure does not affect probing.
The service file uses the existing production paths and ubuntu service user;
review before installation. The unit is installed and enabled on the approved
production host. The original probe process was not restarted.

The worker source also records allowlisted error code/type for non-SSE error
responses and a bounded Server header. Existing Retry-After/backoff handling
is retained. This worker-only metadata patch remains local (not deployed), so
existing logs cannot retrospectively recover missing fields. The deployed
exporter reads the existing fields without altering probe execution.

Verification completed locally:

- 54 frontend tests: usage table, dialog, usage view and locale completeness.
- Frontend production build and new-file ESLint checks.
- 16 offline Python tests, including metadata privacy and rotation retention.
- Full service/repository/admin handler/DTO/config Go test packages.
- Server and route packages compile with generated Wire dependency wiring.
- Race tests for the new log reader/service.
- Live API: HTTP 200, 50-entry page, working filters and non-overlapping cursor
  pages, unauthenticated requests 401, invalid filters 400, metadata allowlist.

Only the application was recreated and the passive exporter installed. No
additional model probe, account binding, base version change or GitHub push
was performed. A manual/unconditional 429 retry was not implemented.
