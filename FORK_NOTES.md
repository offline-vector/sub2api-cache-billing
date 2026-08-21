# Cache Billing Fork

This private fork is based on upstream Sub2API `v0.1.179` (`75f88be5`) and adds
an operator-controlled OpenAI cache-read billing policy. It does not change
upstream cache routing or total input-token accounting.

## Configuration

```yaml
gateway:
  openai_cache_billing_ratio: 1.0
```

Environment equivalent:

```text
GATEWAY_OPENAI_CACHE_BILLING_RATIO=1.0
```

The administrator Gateway settings page provides `100%`, `90%`, `86%`, and
`80%` presets plus an explicit save action. Saving updates the process-local
atomic value immediately; no backend, PostgreSQL, or Redis restart is required.
The environment value remains the startup fallback when no database setting
has been saved.

The accepted range is `(0, 1]`. Invalid, non-finite, missing, or unsafe runtime
values fail closed to official behavior (`1.0`). A value of `0.60` retains 60%
of provider-reported cache reads in the cache billing bucket and moves the
remaining 40% into normal input billing.

```text
billable_cache_read = floor(clamped_upstream_cache_read * ratio)
billable_input      = upstream_total_input - cache_creation - billable_cache_read
```

The invariant below must always hold:

```text
billable_input + billable_cache_read + cache_creation = upstream_total_input
```

This is a pricing policy. Disclose it in customer-facing pricing and billing
terms before enabling a value below `1.0`.

## Scope

The policy applies only to successful OpenAI text requests, including Responses,
Chat Completions, streaming, non-streaming, passthrough, and Responses WebSocket
traffic. It does not apply to images, video, audio, cyber-policy failures, or
non-OpenAI providers.

Client-visible successful usage is rewritten to the billable cache bucket only
after provider usage has been captured. Failed terminal events are unchanged.

## Audit Data

Migration `900_openai_cache_billing_audit.sql` adds these administrator-only
usage fields:

- `upstream_input_tokens`
- `upstream_cache_read_tokens`
- `cache_billing_ratio`
- `upstream_total_cost`

Migration `901_openai_cache_billing_aggregates.sql` adds the same dual-accounting
data to hourly and daily dashboard aggregates. Existing `input_tokens`,
`cache_read_tokens`, `total_cost`, and `actual_cost` remain the customer-billable
buckets and customer deduction. Account quota, provider usage windows, and the
administrator `A` cost use the upstream view. Administrator usage, dashboard,
trend, model/group/endpoint summaries, and exports show both views; regular user
APIs do not expose upstream internals. Historical rows keep ratio `1.0` and fall
back to their legacy values.

## Deployment and Rollback

1. Back up PostgreSQL and verify the migration on staging.
2. Build an image from this repository and pin it by immutable commit or digest.
3. Start with ratio `1.0` and verify HTTP, SSE, Chat Completions, and WebSocket logs.
4. Change the ratio only after publishing the pricing policy. Saving is immediate
   and does not restart any service.
5. Roll back behavior by saving `1.0`. Do not remove
   the audit columns during an incident.

The online updater is pinned to `offline-vector/sub2api-cache-billing` releases,
so it cannot silently install an official upstream binary. Because the fork is
private, set a least-privilege `UPDATE_GITHUB_TOKEN` with read-only repository
contents access. Release asset authentication is sent only to the exact
`api.github.com` authority and is removed before CDN redirects.

## Upstream Updates

Keep the official repository as `upstream` and the private fork as `origin`:

```bash
git remote add upstream https://github.com/Wei-Shaw/sub2api.git
git fetch upstream --tags
git switch -c custom/cache-billing-ratio-vNEXT upstream/vNEXT
git cherry-pick <fork-commit(s)>
```

Resolve generated Ent conflicts by editing `backend/ent/schema/usage_log.go` and
running `cd backend && go generate ./ent`; do not hand-merge generated files.
Check for an upstream migration number collision before carrying migrations `900` and `901`
forward. Run the full backend and security suites before building an image.
