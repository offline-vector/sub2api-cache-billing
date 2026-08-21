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

Existing `input_tokens` and `cache_read_tokens` remain the customer-billable
buckets. Historical rows retain zero raw fields and ratio `1.0`; new rows always
write all three audit values. The admin usage API exposes the audit fields; the
regular user usage API does not expose upstream internals.

## Deployment and Rollback

1. Back up PostgreSQL and verify the migration on staging.
2. Build an image from this repository and pin it by immutable commit or digest.
3. Start with ratio `1.0` and verify HTTP, SSE, Chat Completions, and WebSocket logs.
4. Change the ratio only after publishing the pricing policy. The backend must be
   restarted; PostgreSQL and Redis do not need a restart.
5. Roll back behavior by restoring `1.0` and restarting the backend. Do not remove
   the audit columns during an incident.

Do not use the official online updater on a custom deployment: it replaces the
fork image with an upstream image and silently removes this behavior. Point any
update mechanism at a custom image built from this repository.

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
Check for an upstream migration number collision before carrying migration `900`
forward. Run the full backend and security suites before building an image.
