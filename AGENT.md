# AGENT.md — opencode-proxy-api

## Project Overview

Standalone Go proxy for opencode.ai. Exposes three API formats (OpenAI Chat Completions, OpenAI Responses API, Anthropic Messages) through a single HTTP server. Translates between Claude and OpenAI formats where needed.

## Key Files

| File | Purpose |
|---|---|
| `cmd/server/main.go` | Entry point. Loads .env, starts HTTP server. Routes: /v1/chat/completions, /v1/messages, /v1/responses, /v1/models, /health |
| `config/config.go` | Config struct loaded from env vars. OPCODE_API_KEY, OPCODE_ALLOWED_MODELS (comma-separated), OPCODE_LISTEN, OPCODE_UPSTREAM_BASE, OPCODE_UPSTREAM_TOKEN |
| `internal/proxy/handler.go` | HTTP handler. CORS, auth, model filtering, routing to upstream, response translation |
| `internal/translate/claude_to_openai.go` | Claude Messages → OpenAI Chat Completions (request direction). Handles: system→separate message, tool_use→tool_calls, tool_result→tool messages, fixMissingToolResponses |
| `internal/translate/openai_to_claude.go` | OpenAI → Claude Messages (response direction). SSE streaming translator. Both streaming and non-streaming paths |
| `internal/reasoning/inject.go` | Injects `reasoning_content: " "` placeholder for DeepSeek/Kimi models |

## Architecture

```
Client ──► /v1/messages ──► Claude→OpenAI ──► opencode.ai /zen/v1/chat/completions ──► OpenAI→Claude ──► Client
Client ──► /v1/chat/completions ──► (passthrough, optional reasoning inject) ──► opencode.ai /zen/v1/chat/completions
Client ──► /v1/responses ──► (full passthrough) ──► opencode.ai /zen/v1/responses
```

## Translation Details

### Claude→OpenAI (request)
- `tool` role → `user` role
- `tool_use` content blocks → `tool_calls` on assistant message
- `tool_result` content blocks → separate tool-role messages
- `fixMissingToolResponses`: injects `"[No response received]"` for unmatched tool_call_ids
- tool_result `content` field → OpenAI tool message content

### OpenAI→Claude (response, streaming)
- SSE format: `event: <type>\ndata: <json>\n\n`
- `reasoning_content` / `reasoning` → `thinking` blocks (thinking_delta events)
- `tool_calls` → `tool_use` content blocks (incremental `input_json_delta`)
- DO NOT re-emit accumulated tool args in buildStopEvents — incremental deltas are already streamed

## Common Bugs to Avoid

1. **input_json_delta double-emit**: buildStopEvents must NOT emit accumulated tool args — they were already streamed incrementally. Only emit content_block_stop.
2. **tool_result.content field**: tool_result uses `content` (not `input`) in the Claude API. ContentBlock struct must have both fields.
3. **Go slice pass-by-value**: fixMissingToolResponses must return the slice because append may reallocate.
4. **SSE event prefix**: Anthropic SDK requires `event: <type>\n` before `data: <json>\n\n`.

## Testing

```bash
# Start server
OPCODE_LISTEN=:9191 go run ./cmd/server &

# SDK-based tests (reproducible)
bash test/run.sh http://localhost:9191 deepseek-v4-flash-free
```

## Reference

When in doubt, refer to [9router](https://github.com/decolua/9router) — the original Node.js implementation.
