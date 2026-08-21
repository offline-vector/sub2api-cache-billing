# Security Review

Scope: the cache-billing fork delta on top of Sub2API `v0.1.179`. This is not a
claim that the entire upstream application is vulnerability-free.

## Controls Implemented

- The ratio is operator configuration only. No request header, query parameter,
  API key, group, or account can override it.
- Configuration rejects zero, negative, values above one, NaN, and infinity.
- Runtime normalization fails closed to `1.0` if a hand-built or corrupted config
  bypasses normal validation.
- Upstream usage objects are not mutated before billing/audit capture, preventing
  retry or idempotency paths from applying the transformation twice.
- Cache and cache-creation values are clamped to total input. Billable input cannot
  become negative, and total input remains invariant.
- Client JSON rewrites preserve unknown fields and fail closed to the original
  payload on invalid JSON or rewrite failure.
- Failed/cancelled/incomplete terminal events are not rewritten.
- Raw upstream metering and the applied ratio are stored per usage row and exposed
  only through the administrator API.
- Database constraints enforce a finite positive ratio no greater than one.

## Reviewed Risk Areas

- HTTP streaming and non-streaming Responses
- raw Chat Completions streaming and non-streaming
- passthrough HTTP/SSE paths
- Responses WebSocket downstream writes
- manual SQL single, batched, and best-effort insert column ordering
- Ent schema generation and migration compatibility
- image, video, audio, cyber-policy, and non-OpenAI exclusions
- environment-variable reachability

## Residual Risks

- Upstream can add a new usage JSON shape or protocol path. Unknown shapes remain
  unmodified and therefore default to provider-visible usage until supported.
- Migration constraint validation scans existing partitions. Run it during a low
  DDL-activity window on very large installations and monitor lock waits.
- Historical rows cannot reconstruct provider raw values; their raw fields are
  zero and ratio is `1.0`.
- Aggregate revenue impact depends on customer mix and model prices. A global
  ratio does not increase every customer by the same percentage.
- The upstream service package's full race run currently reports test-harness
  races from parallel tests mutating Gin's global mode (`gin.SetMode`). Focused
  race runs covering this fork's service and repository paths pass; the upstream
  test-only race should be fixed separately before treating a package-wide race
  run as a clean release signal.
- Billing policy changes create contractual and consumer-protection obligations.
  Technical auditability does not replace clear customer disclosure.

## Required Release Gate

Run before every fork release:

```bash
cd backend
go generate ./ent
git diff --exit-code -- ent
go test ./...
go test -race ./internal/service -run 'TestApplyOpenAICacheBillingRatio|TestRewriteOpenAICacheUsage|TestClientVisibleCacheMatchesBillableCache|TestOpenAICacheBillingEligibility|TestOpenAIWSV2Passthrough'
go test -race ./internal/repository -run 'TestUsageLogRepositoryCreate|TestPrepareUsageLogInsert|TestUsageLogInsertQueries|TestScanUsageLogRequestTypeAndLegacyFallback'
govulncheck ./...
```

Also run the existing frontend CI and secret scan the final Git tree. Never commit
production API keys, database URLs, object-storage credentials, or SSH material.
