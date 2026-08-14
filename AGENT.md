# AGENT.md — opencode-proxy-api

## Project Overview

Standalone Go proxy for opencode.ai. Exposes three API formats (OpenAI Chat Completions, OpenAI Responses API, Anthropic Messages) through a single HTTP server. All formats are forwarded to matching upstream `/zen/v1/*` paths with no format translation.

## Key Files

| File | Purpose |
|---|---|
| `cmd/server/main.go` | Entry point. Loads .env, starts HTTP server. Routes: /v1/chat/completions, /v1/messages, /v1/responses, /v1/models, /health |
| `config/config.go` | Config from env: OPCODE_API_KEY, OPCODE_ALLOWED_MODELS, OPCODE_LISTEN, OPCODE_UPSTREAM_BASE, OPCODE_UPSTREAM_TOKEN, OPCODE_UPSTREAM_TIMEOUT, OPCODE_PROXY (http/https/socks5/socks5h). Builds upstream http.Client via NewUpstreamClient. |
| `config/proxy_pool.go` | Proxy pool for automatic 429 rotation. Single `OPCODE_PROXY` treated as 1-element pool for unified logic. |
| `build.sh` / `build.cmd` | Cross-compile helpers; default package `./cmd/server`, binary name `opencode-proxy` |
| `internal/proxy/handler.go` | HTTP handler. CORS, auth, model filtering, request normalization, routing to upstream |
| `internal/translate/claude_request.go` | Request-only adapter for `/v1/messages`: Anthropic tools/tool_use → OpenAI function tools (upstream still validates OpenAI tool schema) |
| `internal/translate/chat.go` | Request normalizer for `/v1/chat/completions`: converts `role: "developer"` → `role: "system"` for upstream compatibility |
| `internal/translate/dsml.go` | Response adapter for `/v1/messages`: DeepSeek DSML tool-call text → Claude `tool_use` (non-stream + buffered stream) |
| `internal/reasoning/inject.go` | Injects `reasoning_content: " "` placeholder for DeepSeek/Kimi models on chat/completions only |

## Architecture

```
Client ──► /v1/messages ──► (Anthropic tools rewrite, DSML response rewrite) ──► opencode.ai /zen/v1/messages
Client ──► /v1/chat/completions ──► (developer→system, optional reasoning inject) ──► opencode.ai /zen/v1/chat/completions
Client ──► /v1/responses ──► (passthrough) ──► opencode.ai /zen/v1/responses
```

## Notes

- `/v1/chat/completions` normalizes `role: "developer"` → `role: "system"` because upstream DeepSeek models don't recognize the newer OpenAI developer role.
- `/v1/messages` request body gets a narrow tools/tool_use rewrite because upstream still validates `tools[].function.name` (OpenAI shape) even on `/zen/v1/messages`.
- `/v1/messages` responses rewrite DeepSeek DSML tool-call text into Claude `tool_use` blocks. Streaming responses are buffered so DSML can be parsed as a whole.
- For `/v1/messages`, prefer Anthropic content blocks (`content: [{"type":"text","text":"..."}]`); plain string content can fail upstream with "Messages cannot be empty".
- `anthropic-version` and client `x-api-key` headers are forwarded on `/v1/messages`.
- Reasoning injection only applies to `/v1/chat/completions`.
- Single `OPCODE_PROXY` is automatically treated as a 1-element proxy pool, enabling automatic 429 rotation and unified logging.

## Testing

```bash
# Start server
OPCODE_LISTEN=:9191 go run ./cmd/server &

# SDK-based tests (reproducible)
bash test/run.sh http://localhost:9191 deepseek-v4-flash-free
```

## Reference

When in doubt, refer to [9router](https://github.com/decolua/9router) — the original Node.js implementation.
