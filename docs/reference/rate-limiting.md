# Dynamic Rate Limiting

PicoClaw prevents 429 errors from LLM provider APIs by enforcing configurable
per-account request-rate limits **before** sending each request. Unlike the
reactive cooldown/fallback system (which activates *after* a 429 is received),
rate limiting is **proactive**.

## How it works

### Token-bucket algorithm

Each resolved account-and-alias candidate gets a token bucket using the `rpm`
policy from its concrete account:

- **Capacity** = `rpm` (burst size equals the per-minute limit)
- **Refill rate** = `rpm / 60` tokens per second
- Tokens are consumed one per LLM call; if the bucket is empty, the call blocks until a token refills or the request context is cancelled

### Call chain integration

```
AgentLoop.callLLM()
  └─ FallbackChain.Execute()         ← iterate candidates
       ├─ CooldownTracker.IsAvailable()   ← skip if post-429 cooldown active
       ├─ RateLimiterRegistry.Wait()      ← NEW: block until token available
       └─ provider.Chat()                 ← actual LLM HTTP call
```

The rate limiter runs **after** the cooldown check and **before** the provider call, so:
- Candidates already in cooldown are skipped entirely (no token consumed)
- Candidates that are available get throttled to the configured RPM

The same check applies in `ExecuteImage`.

### Thread safety

`RateLimiterRegistry` is safe for concurrent use. The per-limiter token bucket uses a fine-grained mutex so concurrent goroutines each acquire their own token independently.

## Configuration

Set `rpm` on concrete accounts in `model_list`; aliases remain independent:

```yaml
version: 4
model_list:
  - model_name: openai-work
    provider: openai
    model: ""
    api_base: https://api.openai.com/v1
    rpm: 3          # max 3 requests per minute
    api_keys:
      - sk-...

  - model_name: anthropic-work
    provider: anthropic
    model: ""
    rpm: 60         # 60 rpm (Anthropic free tier)
    api_keys:
      - sk-ant-...

  - model_name: ollama-local
    provider: ollama
    model: ""
    api_base: http://localhost:11434/v1
    # no rpm → unrestricted

model_aliases:
  - name: chat
    model: gpt-4o
    account_overrides:
      anthropic-work: claude-haiku-4-5
      ollama-local: llama3

agents:
  defaults:
    account_ref: openai-work
    model_name: chat
```

| Field | Type | Default | Description |
|---|---|---|---|
| `rpm` | `int` | `0` | Requests per minute. `0` means no limit. |

### Interaction with fallbacks

When alias fallbacks are configured, each resolved account-and-alias candidate
is rate-limited **independently**:

```yaml
version: 4
model_list:
  - model_name: openrouter-main
    provider: openrouter
    model: ""
    rpm: 5

model_aliases:
  - name: primary
    model: openai/gpt-4o
  - name: fallback
    model: openai/gpt-4o-mini

agents:
  defaults:
    account_ref: openrouter-main
    model_name: primary
    model_fallbacks: [fallback]
```

If the current candidate's bucket is empty and there are more candidates available, PicoClaw skips the locally saturated candidate and tries the next fallback immediately. Only the last remaining candidate waits for a token to refill. If the context deadline is hit while waiting on that last candidate, the wait error propagates.

Rate limiting is keyed by the stable concrete-account plus alias identity, not
only the resolved upstream model string. Different aliases on one account get
separate buckets with that account's `rpm`; account-router choices use the RPM
of the selected concrete account.

### Burst behaviour

The bucket starts **full** (burst = RPM). For `rpm: 3`, the first 3 requests fire instantly; subsequent requests are spaced ~20 s apart.

To reduce burstiness for strict APIs, set a lower `rpm` and rely on the steady-state refill.

## Files changed

| File | What |
|---|---|
| `pkg/providers/ratelimiter.go` | `RateLimiter` (token bucket) + `RateLimiterRegistry` |
| `pkg/providers/ratelimiter_test.go` | Unit tests for limiter and registry |
| `pkg/providers/fallback.go` | `FallbackCandidate.RPM` field; `FallbackChain.rl`; `Wait()` call in `Execute`/`ExecuteImage` |
| `pkg/agent/account_alias_resolution.go` | Resolves exact aliases for concrete accounts, preserves account-plus-alias identity, and propagates account `RPM` into `FallbackCandidate` |
| `pkg/agent/loop.go` | Build `RateLimiterRegistry`, register all agents' candidates, pass to `NewFallbackChain` |
