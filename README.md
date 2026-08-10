# opencode-proxy-api

[![CI](https://github.com/xywf221/opencode-proxy-api/actions/workflows/ci.yml/badge.svg)](https://github.com/xywf221/opencode-proxy-api/actions/workflows/ci.yml)

A lightweight Go proxy for [opencode.ai](https://opencode.ai) that exposes all three API formats — OpenAI Chat Completions, OpenAI Responses API, and Anthropic Messages — through a single endpoint.

This project mirrors the approach used by [9router](https://github.com/decolua/9router) for handling opencode requests. When encountering issues, refer to 9router's implementation for reference.

## Supported Formats

| Format | Endpoint | Upstream Path |
|---|---|---|
| OpenAI Chat Completions | `POST /v1/chat/completions` | `/zen/v1/chat/completions` |
| OpenAI Responses API | `POST /v1/responses` | `/zen/v1/responses` |
| Anthropic Messages | `POST /v1/messages` | `/zen/v1/messages` |

All three endpoints are forwarded to the matching upstream paths. Request and response bodies are passed through as-is (no format translation).

## Quick Start

```bash
# Build (or use ./build.sh / build.cmd)
go build -o opencode-proxy ./cmd/server
./opencode-proxy

# Cross-compile / local install helpers
./build.sh --local                 # Linux/macOS/Git Bash
build.cmd --local                  # Windows cmd
./build.sh --os linux --arch amd64
```

Test it:

```bash
# OpenAI Chat Completions
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hello"}]}'

# OpenAI Responses API
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash-free","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}'

# Anthropic Messages format (prefer content blocks)
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: public" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"deepseek-v4-flash-free","max_tokens":256,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}'
```

## Unit Tests

```bash
go test -race -count=1 ./...
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `OPCODE_API_KEY` | *(empty)* | Require this API key on all incoming requests. |
| `OPCODE_ALLOWED_MODELS` | *(empty)* | Comma-separated allowlist of model names. Empty = all models. |
| `OPCODE_LISTEN` | `:8080` | Listen address. |
| `OPCODE_UPSTREAM_BASE` | `https://opencode.ai` | Upstream base URL. |
| `OPCODE_UPSTREAM_TOKEN` | `public` | Bearer token sent to upstream. |
| `OPCODE_UPSTREAM_TIMEOUT` | `5m` | Upstream HTTP request timeout (Go duration format). |
| `OPCODE_PROXY` | *(empty)* | Outbound proxy for upstream requests. Schemes: `http`, `https`, `socks5`, `socks5h`. |
| `OPCODE_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `OPCODE_LOG_FORMAT` | `text` | Log format: `text` or `json`. |

### Proxy examples

```bash
# SOCKS5 with local DNS resolution
OPCODE_PROXY=socks5://127.0.0.1:1080 ./opencode-proxy

# SOCKS5 with remote DNS (socks5h) — preferred when local DNS is blocked
OPCODE_PROXY=socks5h://127.0.0.1:1080 ./opencode-proxy

# HTTP proxy (optional basic auth)
OPCODE_PROXY=http://user:pass@127.0.0.1:8080 ./opencode-proxy
```

## CI

On every push and pull request to `main`, CI runs across Linux, Windows, and macOS:

- **Lint** — golangci-lint (gofmt, govet, staticcheck, misspell, unused)
- **Build** — cross-platform compilation check
- **Test** — `go test -race -count=1 ./...` on all three platforms
- **Vet** — `go vet ./...`

## Testing

### Go unit tests

```bash
go test -race -count=1 ./...
```

### SDK integration tests

```bash
# Start server
OPCODE_LISTEN=:9191 go run ./cmd/server &

# Run the SDK-based test suite
bash test/run.sh http://localhost:9191 deepseek-v4-flash-free
```

The integration test suite uses the official Anthropic and OpenAI SDKs to verify both streaming and non-streaming paths against a live upstream.

## Reference

When encountering issues or implementing new features, refer to:
- [9router](https://github.com/decolua/9router) — the original Node.js implementation this project is based on
