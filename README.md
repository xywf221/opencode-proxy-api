# OpenCode Proxy API

企业级 OpenAI/Claude API 反向代理，支持会话亲和性代理池、健康度管理、请求特征多样化。

## 特性

### 核心功能
- ✅ **会话亲和性** - 同一对话始终使用同一代理，避免被反爬虫检测
- ✅ **代理健康管理** - 失败追踪、指数退避、自动故障隔离
- ✅ **请求特征多样化** - 动态 session/request/project 标识，模拟真实客户端
- ✅ **协议转换** - OpenAI Chat Completions ↔ Claude Messages API
- ✅ **DeepSeek DSML** - 自动重写 DSML 工具调用格式
- ✅ **Retry-After 遵守** - 智能限流响应，保护上游
- ✅ **密码安全** - 日志自动脱敏，永不泄漏凭据

### 支持的端点
- `/v1/chat/completions` - OpenAI Chat Completions API
- `/v1/messages` - Anthropic Claude Messages API
- `/v1/models` - 模型列表

## 快速开始

### 1. 配置环境变量

```bash
# 必需
export OPCODE_TOKEN="your-opencode-api-key"

# 可选 - 单个代理
export OPCODE_PROXY="socks5h://user:pass@proxy.example.com:1080"

# 或使用代理池
export OPCODE_PROXY_POOL="/path/to/proxies.txt"

# API 认证
export OPCODE_API_KEY="your-gateway-api-key"

# 监听端口 (默认 8080)
export OPCODE_LISTEN=":8080"
```

### 2. 运行服务

```bash
./opencode-proxy-linux-amd64
```

### 3. 发送请求

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-gateway-api-key" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

## 代理配置

### 单个代理

```bash
# SOCKS5 (推荐 - 域名远程解析)
export OPCODE_PROXY="socks5h://user:pass@proxy.com:1080"

# HTTP
export OPCODE_PROXY="http://user:pass@proxy.com:8080"

# 无认证
export OPCODE_PROXY="socks5h://proxy.com:1080"
```

**⚠️ 重要**: 使用 `socks5h://` 而不是 `socks5://` 避免 IPv6 兼容性问题。

### 诊断: 检查代理出口 IP

启用后，服务启动时会探测当前代理的真实出口地址并打日志；代理池轮询到新代理时也会探测。

```bash
export OPCODE_DIAG_EGRESS=true
```

日志示例:
```
INFO proxy egress proxy=socks5h://***:***@127.0.0.1:7891 egress=2a14:640:2:71d5:...
```

用它确认代理走的是 IPv4 还是 IPv6 出口。默认关闭。

### 429 限流后自动切换出口 (Warp)

当上游连续返回 429 达到阈值时，自动执行一条外部命令——比如你用来手动切换 Warp 出口的命令：

```bash
# 连续 5 次 429 后执行切换 Warp 出口的命令（shell）
export OPCODE_RATE_LIMIT_ACTION="switch-warp-exit.sh"
export OPCODE_RATE_LIMIT_ACTION_THRESHOLD=5
```

- `OPCODE_RATE_LIMIT_ACTION` 是要执行的 shell 命令/脚本（为空则禁用）
- `OPCODE_RATE_LIMIT_ACTION_THRESHOLD` 触发阈值，默认 3
- 任意成功的上游响应都会重置计数器；代理池存在时 429 会先轮换代理
- 执行完成后会打日志（含输出），失败也会打 ERROR 日志

### 代理池

创建 `proxies.txt`:
```txt
socks5h://user1:pass1@proxy1.com:1080
socks5h://user2:pass2@proxy2.com:1080
http://user3:pass3@proxy3.com:8080
```

启用代理池:
```bash
export OPCODE_PROXY_POOL="proxies.txt"
./opencode-proxy-linux-amd64
```

## 会话亲和性

### 为什么需要？

**问题**: 如果每次请求随机切换代理，OpenCode 会检测到"同一 IP 短时间内通过多个不同代理发请求" → 触发反爬虫封禁。

**解决方案**: 同一会话的所有请求使用相同代理，从上游视角看像"一个用户用一条线路在聊天"。

### 如何提供会话标识？

#### 方式 1: HTTP Header (推荐)
```bash
curl -H "x-session-id: conversation-abc-123" \
  http://localhost:8080/v1/chat/completions ...
```

支持的 header (按优先级):
- `x-opencode-session`
- `x-session-id`
- `conversation-id`

#### 方式 2: JSON Metadata
```json
{
  "model": "claude-3-5-sonnet-20241022",
  "messages": [...],
  "metadata": {
    "session_id": "conversation-abc-123"
  }
}
```

#### 方式 3: OpenAI `user` 字段
```json
{
  "model": "gpt-4",
  "messages": [...],
  "user": "user-stable-id-456"
}
```

#### 自动生成

如果未提供，系统会从**首条用户消息内容**生成稳定哈希:
```
"Hello world" → session_a1b2c3d4
```

只要消息内容相同，生成的会话 ID 相同。

## 健康度管理

### 失败隔离

代理失败后自动进入冷却期，避免反复尝试死代理:

| 连续失败次数 | 冷却时间 |
|-------------|---------|
| 1           | 15 秒   |
| 2           | 30 秒   |
| 3           | 60 秒   |
| 4+          | 120 秒  |

### 故障分类

**网络错误** (立即隔离):
- Connection refused
- Timeout
- DNS resolution failure

**HTTP 429** (遵守 Retry-After 后轮换):
- 解析 `Retry-After` header (支持秒数和日期)
- 自动轮换到下一个健康代理

**业务错误** (记录但不隔离):
- HTTP 4xx (客户端错误)
- HTTP 5xx (上游错误)

### 查看健康状态

```bash
curl http://localhost:8080/
```

响应:
```json
{
  "status": "ok",
  "upstream": "https://opencode.ai",
  "mode": "proxy_pool",
  "slots": [
    "socks5h://***:***@proxy1.com:1080",
    "socks5h://***:***@proxy2.com:1080"
  ]
}
```

## 环境变量完整列表

| 变量 | 必需 | 默认值 | 说明 |
|------|------|--------|------|
| `OPCODE_TOKEN` | ✅ | - | OpenCode API Token |
| `OPCODE_UPSTREAM` | ❌ | `https://opencode.ai` | 上游地址 |
| `OPCODE_LISTEN` | ❌ | `:8080` | 监听地址 |
| `OPCODE_API_KEY` | ❌ | - | Gateway 认证密钥 (不设置则无认证) |
| `OPCODE_PROXY` | ❌ | `direct` | 单个代理 URL |
| `OPCODE_PROXY_POOL` | ❌ | - | 代理池文件路径 |
| `OPCODE_UPSTREAM_TIMEOUT` | ❌ | `5m` | 上游超时 |
| `OPCODE_ALLOWED_MODELS` | ❌ | `*` | 允许的模型列表 (逗号分隔) |

## 构建

### 本地构建
```bash
go build -o opencode-proxy ./cmd/server
```

### 交叉编译
```bash
# Linux AMD64
GOOS=linux GOARCH=amd64 go build -o opencode-proxy-linux-amd64 ./cmd/server

# macOS ARM64
GOOS=darwin GOARCH=arm64 go build -o opencode-proxy-darwin-arm64 ./cmd/server

# Windows AMD64
GOOS=windows GOARCH=amd64 go build -o opencode-proxy-windows-amd64.exe ./cmd/server
```

## 测试

```bash
# 运行所有测试
go test ./...

# 测试会话亲和性
bash test/test_session_affinity.sh

# 快速验证
bash test_quick.sh
```

## 故障排查

### 问题: 请求几次就被 429

**症状**: 几次请求后收到 `HTTP 429 Too Many Requests`

**原因**: 
1. 未启用代理池，所有请求来自同一 IP
2. 使用了代理池但未提供会话标识，每次切换代理

**解决**:
```bash
# 确保使用代理池
export OPCODE_PROXY_POOL="proxies.txt"

# 客户端发送稳定会话 ID
curl -H "x-session-id: my-conversation" ...
```

### 问题: 代理间歇性失败

**症状**: 日志出现 `connection refused` 到 IPv6 地址

**原因**: 使用 `socks5://` 本地解析域名，随机返回 IPv6，但代理无 IPv6 出口

**解决**: 使用 `socks5h://` 让代理远程解析域名
```bash
export OPCODE_PROXY="socks5h://user:pass@proxy.com:1080"
```

### 问题: 密码出现在日志中

**检查**: 搜索日志是否有明文密码

**修复**: 最新版本已自动脱敏，输出:
```
proxy=socks5h://***:***@proxy.com:1080
```

如果仍泄漏，请升级到最新版本。

## 安全建议

### 1. 使用专用 API Key
```bash
export OPCODE_API_KEY="$(openssl rand -hex 32)"
```

### 2. 限制模型访问
```bash
export OPCODE_ALLOWED_MODELS="gpt-4,gpt-3.5-turbo,claude-3-5-sonnet-20241022"
```

### 3. 网络隔离
```bash
# 仅监听本地
export OPCODE_LISTEN="127.0.0.1:8080"

# 或使用反向代理 (nginx/caddy)
```

### 4. 定期轮换代理凭据
```bash
# 更新 proxies.txt 后无需重启，会自动重新加载
```

## 架构

```
客户端
  ↓
[Gateway :8080]
  ↓ 会话哈希 → 代理选择
  ↓
[代理池]
  ├─ Proxy 1 (健康度: ✅)
  ├─ Proxy 2 (冷却中: ⏳)
  └─ Proxy 3 (健康度: ✅)
  ↓
[OpenCode API]
```

### 请求流程
1. 接收客户端请求
2. 提取/生成会话标识
3. 根据会话哈希选择代理 (一致性哈希)
4. 添加请求特征 headers
5. 转发到 OpenCode
6. 追踪代理健康度
7. 流式/非流式响应

## 性能指标

- **延迟**: < 50ms (代理选择 + 哈希)
- **吞吐**: > 1000 req/s (单实例)
- **内存**: ~50MB + 200 bytes/代理

## 贡献

欢迎提交 Issue 和 Pull Request!

### 开发
```bash
# 安装依赖
go mod download

# 运行测试
go test -v ./...

# 格式化代码
go fmt ./...

# 静态分析
go vet ./...
```

## 许可证

MIT License

## 参考项目

- [opencode2api](https://github.com/jasonxu114514/opencode2api) - 会话亲和架构参考
- [opencode-free-gate](https://github.com/GuJi08233/opencode-free-gate) - Gateway 模式参考

## 更新日志

查看 [IMPROVEMENTS.md](IMPROVEMENTS.md) 了解详细改进历史。

### v1.1.0 - 2026-08-14
- ✅ 会话亲和性代理选择
- ✅ 请求特征多样化 (动态 headers)
- ✅ 代理健康度管理与故障隔离
- ✅ 指数退避 + Retry-After 遵守
- ✅ 密码泄漏修复
- ✅ 单代理自动池化

### v1.0.0 - 初始版本
- 基础反向代理功能
- OpenAI/Claude 协议转换
- DeepSeek DSML 支持
