# Changelog

## [v1.1.1] - 2026-08-14

### Fixed
- **Tool result validation error**: Skip `tool_result` blocks without `tool_use_id`/`tool_call_id` to prevent upstream 400 errors
  - Error was: `missing field 'tool_call_id' at line 1 column X`
  - Now: Invalid tool results are silently dropped instead of causing request failures
  - Added regression test: `TestSkipToolResultWithoutID`
  - File: `internal/translate/claude_request.go:247-263`

## [Unreleased]

### Added
- **SSE Converter Package** (`internal/sse/`): 统一的 Server-Sent Events 转换框架
  - `Reader`: SSE 事件流解析器,支持多行 data 和 event 类型
  - `Writer`: SSE 事件流写入器,自动处理多行数据
  - `Converter`: 流式转换器,支持逐事件转换、过滤、字段注入
  - 内置 DSML 重写集成
  - Transform 函数支持:过滤事件、注入字段、链式组合
  - 完整测试覆盖 (reader, writer, converter, transforms)

### Changed
- **单代理自动转代理池**: `OPCODE_PROXY` 现在自动被视为 1 元素代理池
  - 统一 429 轮换逻辑
  - 统一日志输出格式
  - 不需要为单个代理创建文件

### Fixed
- **修复 `developer` 角色错误**: `/v1/chat/completions` 现在自动将 `role: "developer"` 转换为 `role: "system"`
  - OpenAI 2024 新增的 developer 角色上游不支持
  - 透明转换,客户端无需修改

### Improved
- **代理池增强**:
  - 支持 `<inline-single-proxy>` 标记复用池逻辑
  - 更好的错误日志(凭据脱敏)
  - 线程安全的轮换机制

### Security
- **警告**: `OPCODE_PROXY` 的凭据会在启动时打印到日志
  - 建议生产环境使用 `OPCODE_PROXY_POOL_FILE` 配合文件权限保护
  - 或使用日志过滤器脱敏

### Documentation
- 更新 `AGENT.md`: 添加 SSE 包、环境变量表、代理使用建议
- 新增 `internal/sse/README.md`: 完整的 SSE 转换器使用文档
- 添加代理 URL 格式说明和 `socks5h://` 推荐理由

## [Previous]

### 2025-01-XX
- Rewrite Anthropic tools for upstream OpenAI schema on /v1/messages
- Passthrough messages/responses; drop local translation
- Add upstream proxy support, build scripts, and Go 1.25
- Fix Claude translation edge cases
- Handle Claude message tool and usage translation
