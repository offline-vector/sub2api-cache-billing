# Cache Billing Fork

This fork syncs upstream Sub2API v0.2.8 while retaining the operator controlled
OpenAI cache billing policy and user scoped exemption whitelist.

## Synchronization boundary

- Includes the complete upstream history through v0.2.8, including native
  turn-state handling, OpenCode Go usage windows, model support, scheduler,
  connection, billing, backup, and administration changes.
- Preserves `openai_cache_billing_ratio`, upstream usage audit fields, the user
  exemption whitelist, and its save behavior.
- Keeps the custom 292 probing, shared state injection, renewal worker, and
  probe log interface reverted. Upstream native turn-state behavior remains
  aligned with upstream; no state length filter is added.
- Keeps customer and upstream usage cost display at eight decimal places.

The v0.2.8 release has no separate cache billing migration conflict; the
existing audit columns and settings remain part of this fork.
