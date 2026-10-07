# Cache Billing Fork

This fork syncs upstream Sub2API v0.2.14 while retaining the operator controlled
OpenAI cache billing policy and user scoped exemption whitelist.

## Synchronization boundary

- Includes the complete upstream history through v0.2.14, including EasyPay
  callback replay protection, fresh-install administrator credential validation,
  and API-key model discovery in generated remote Codex configurations.
  Retains all v0.2.12 and v0.2.13 platform, recharge, security, and billing fixes.
- Preserves `openai_cache_billing_ratio`, upstream usage audit fields, the user
  exemption whitelist, and its save behavior.
- Keeps the custom 292 probing, shared state injection, renewal worker, and
  probe log interface reverted. Upstream native turn-state behavior remains
  aligned with upstream; no state length filter is added.
- Keeps customer and upstream usage cost display at eight decimal places.

This fork retains migrations `900` and `901` and the cache billing audit fields
restored during the v0.2.10 synchronization. There are no new fork migrations.
v0.2.14 adds no database migrations. The v0.2.12 migrations for recharge bonuses
and TypeSafe constraints remain intact. Fresh-install credential requirements
do not reset existing administrators or prevent initialized instances starting.
Account statistics retain original upstream token buckets while respecting
upstream's account-level long-context pricing gate. Free Fast requests without
pricing retain upstream's zero-cost usage-log behavior.

## Configuration and user exemptions

The administrator Gateway settings page saves `openai_cache_billing_ratio`
immediately. `gateway.openai_cache_billing_ratio` (environment variable
`GATEWAY_OPENAI_CACHE_BILLING_RATIO`) is the startup fallback. The valid range
is `(0, 1]`; invalid values fall back to `1.0`.

The exemption whitelist contains **user IDs**, not upstream account IDs.
Its legacy storage key is `rewrite_message_cache_control_account_whitelist`.
Exempt users retain ratio `1.0` and original cache billing.

```text
billable_cache_read = floor(clamped_upstream_cache_read * ratio)
billable_input      = upstream_total_input - cache_creation - billable_cache_read
```

The policy moves the remaining cache tokens to normal input billing, preserving
the total input count. It applies to successful OpenAI text traffic, including
HTTP, streaming, passthrough, and WebSocket requests. Images, video, audio,
cyber-policy failures, and other platforms are excluded. Failed terminal events
remain unchanged. Publish the pricing policy in customer-facing terms before
enabling a ratio below `1.0`.

## Audit and updates

Migrations `900` and `901` retain original upstream input/cache token counts,
the applied ratio, and upstream total cost in usage logs and aggregates.
Customer deduction uses billable buckets; upstream account quota and provider
usage accounting use the original metering. Administrator usage, dashboard,
and exports show both views; regular user APIs omit upstream internals.

The online updater targets `offline-vector/sub2api-cache-billing` releases.
If the repository is private, configure a read-only `UPDATE_GITHUB_TOKEN`.
Keep fork release tags separate from upstream tags when fetching:

```bash
git fetch upstream --no-tags main
git fetch upstream --no-tags refs/tags/vNEXT:refs/upstream-tags/vNEXT
```

Before deploying, review migrations and billing conflicts, run backend and
frontend checks, verify a PostgreSQL backup, and pin the image digest. Preserve
audit columns during rollback. Generated Ent conflicts must be resolved in the
schema followed by `go generate ./ent`, rather than manually editing generated
files.
