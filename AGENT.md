# Session Affinity & Request Fingerprinting

This update implements conversation-aware proxy selection and request diversification to avoid upstream anti-bot detection.

## Key Changes

### 1. Session Affinity (`internal/session/session.go`)

**Problem**: OpenCode detects when the same IP rapidly switches between many different proxy IPs (previous round-robin behavior).

**Solution**: Hash-based session affinity - same conversation always uses the same proxy.

- Extracts stable session ID from:
  1. Explicit headers (`x-conversation-id`, `x-session-id`)
  2. JSON metadata fields
  3. Hash of first user message (stable across multi-turn conversations)
  4. Random UUID (fallback)

- `SessionHash()` provides consistent hashing for proxy selection

### 2. Proxy Pool Health Tracking (`config/proxy_pool.go`)

**New methods**:
- `ClientForSession(sessionID)`: Returns a client bound to a specific proxy based on session hash
- `MarkSuccess(proxyURL)`: Resets failure counter
- `MarkFailure(proxyURL, statusCode)`: Tracks failures, marks unhealthy after 3 failures
- `HealthStatus()`: Returns health state of all proxies

**Behavior**:
- Unhealthy proxies are skipped during session-affinity selection
- Only real proxy failures count (connection errors, timeouts, 5xx)
- Business errors (401, 403, 429, 4xx) don't mark proxy unhealthy

### 3. Request Fingerprinting (`internal/proxy/handler.go`)

**Diversified headers** sent to upstream:
```
x-opencode-session: sess_<stable-hash>  // Same for conversation
x-opencode-request: <uuid>              // Unique per request
x-opencode-project: proj_<random>       // Rotates between requests
User-Agent: OpenCode/0.9.14 (darwin-arm64) // Randomized version/platform
```

This makes each request look like it's from a real OpenCode client while maintaining session continuity.

### 4. Automatic Proxy Rotation

- Still rotates on 429 (rate limit) to distribute load
- Now tracks which proxy caused the error
- Success responses mark the proxy healthy

## Usage

### With Single Proxy (now treated as 1-proxy pool)

```bash
export OPCODE_PROXY=socks5h://user:pass@proxy.example.com:1080
./opencode-proxy-linux-amd64
```

The single proxy will be used for all sessions (obviously), but health tracking still applies.

### With Proxy Pool

```bash
export OPCODE_PROXY_POOL_FILE=proxies.txt
./opencode-proxy-linux-amd64
```

**proxies.txt**:
```
socks5h://user:pass@proxy1.example.com:1080
socks5h://user:pass@proxy2.example.com:1080
socks5h://user:pass@proxy3.example.com:1080
```

Each conversation will consistently use the same proxy (based on session hash), until that proxy becomes unhealthy.

## Testing

```bash
# Start server with proxy pool
export OPCODE_PROXY_POOL_FILE=test/proxies.txt
export OPCODE_UPSTREAM_TOKEN=your-token
./opencode-proxy-linux-amd64

# Run affinity test
bash test/test_session_affinity.sh
```

Check logs for `session=sess_<hash>` - same conversation should show same session ID across requests.

## Why This Fixes "请求几次就被标记"

**Before**: Same source IP → different proxy IP every request → looks like bot/scraper → banned

**After**: Same source IP → same proxy IP per conversation → looks like normal user with stable network → not banned

The key insight from `opencode2api`: OpenCode doesn't just check your IP, it checks for **abnormal IP switching patterns**. By keeping proxy-session affinity, each conversation looks like a single user on a single connection.
