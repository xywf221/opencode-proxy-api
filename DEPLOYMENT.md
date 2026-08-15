# 部署指南

## 快速开始

### 1. 准备代理池

```bash
# 创建 proxies.txt
cat > proxies.txt << 'EOF'
socks5h://user1:pass1@proxy1.example.com:1080
socks5h://user2:pass2@proxy2.example.com:1080
socks5h://user3:pass3@proxy3.example.com:1080
EOF

# 权限保护
chmod 600 proxies.txt
```

**重要**: 必须使用 `socks5h://` (远程 DNS 解析),否则会遇到 IPv6 连接问题。

### 2. 启动服务

```bash
# 生产环境
OPCODE_API_KEY=your-secret-key \
OPCODE_PROXY_POOL_FILE=proxies.txt \
./opencode-proxy-session.exe > server.log 2>&1 &
```

### 3. 验证运行

```bash
# 健康检查
curl http://localhost:8080/healthz

# 查看状态
curl -H "Authorization: Bearer your-secret-key" http://localhost:8080/
```

## 环境变量配置

| 变量 | 必需 | 默认值 | 说明 |
|---|---|---|---|
| `OPCODE_API_KEY` | 推荐 | (无) | 客户端认证密钥,未设置则关闭认证 |
| `OPCODE_LISTEN` | 否 | `:8080` | 监听地址 |
| `OPCODE_UPSTREAM_BASE` | 否 | `https://opencode.ai` | 上游基础 URL |
| `OPCODE_UPSTREAM_TOKEN` | 否 | `public` | 上游授权 token |
| `OPCODE_UPSTREAM_TIMEOUT` | 否 | `5m` | 上游请求超时 |
| `OPCODE_PROXY_POOL_FILE` | 推荐 | (无) | 代理池文件路径 |
| `OPCODE_PROXY` | 可选 | (无) | 单个代理 URL(自动转为单元素池) |
| `OPCODE_ALLOWED_MODELS` | 否 | (全部) | 允许的模型,逗号分隔 |

## 代理池配置

### 格式要求

```
# proxies.txt - 每行一个代理 URL
socks5h://user:pass@host:port
http://user:pass@host:port

# 支持注释
# socks5h://disabled@example.com:1080

# 空行会被忽略
```

### 推荐配置

- **最少代理数**: 5-10 个(越多越好,负载更分散)
- **协议**: `socks5h://` > `http://` > `socks5://`
- **认证**: 每个代理应该有独立凭据
- **地理分布**: 同一地区的代理避免被关联

### 单代理模式(不推荐生产)

```bash
# 开发/测试时使用
OPCODE_PROXY=socks5h://user:pass@proxy.example.com:1080 ./opencode-proxy-session.exe
```

单代理会自动转换为单元素代理池,但失去了负载分散和故障转移能力。

## 客户端集成

### OpenAI SDK (Python)

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://your-server:8080/v1",
    api_key="your-secret-key"
)

# 设置会话 ID 以保持代理亲和性
response = client.chat.completions.create(
    model="deepseek-v4",
    messages=[{"role": "user", "content": "Hello"}],
    extra_headers={
        "x-conversation-id": "user-123-session-456"  # 关键!
    }
)
```

### OpenAI SDK (Node.js)

```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: 'http://your-server:8080/v1',
  apiKey: 'your-secret-key'
});

const response = await client.chat.completions.create({
  model: 'deepseek-v4',
  messages: [{role: 'user', content: 'Hello'}]
}, {
  headers: {
    'x-conversation-id': 'user-123-session-456'  // 关键!
  }
});
```

### cURL

```bash
curl -X POST http://your-server:8080/v1/chat/completions \
  -H "x-conversation-id: my-session-id" \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

## 会话管理最佳实践

### 为什么需要 x-conversation-id?

**问题**: 不设置会话 ID 时,代理会从消息内容生成哈希。如果对话历史不断变化,每次请求看起来都是"新会话",就会频繁切换代理,触发 OpenCode 的反爬虫检测。

**解决**: 客户端为每个对话生成一个稳定的 `conversation-id`,整个对话期间保持不变。

### 会话 ID 命名建议

```python
# 方案 1: 用户 + 对话
session_id = f"user-{user_id}-conv-{conversation_id}"

# 方案 2: UUID (存储在数据库)
session_id = str(uuid.uuid4())

# 方案 3: 时间窗口 + 用户 (每天切换一次)
session_id = f"user-{user_id}-{date.today().isoformat()}"
```

**原则**:
- 同一对话的所有请求使用相同 ID
- 不同对话使用不同 ID
- 避免包含敏感信息(用户邮箱等)
- 长度适中(20-100 字符)

### 会话生命周期

```
用户开始新对话
  ↓
生成 session_id
  ↓
所有消息携带这个 session_id
  ↓
代理: 同一 session_id → 同一代理
  ↓
用户结束对话(可选清理)
```

对话结束后,session_id 可以复用(比如用户第二天继续同一对话),不影响功能,只是可能切换到另一个代理。

## 监控和日志

### 关键日志字段

```
time=... level=INFO msg=upstream component=proxy 
  req_id=req_xxx          # 请求唯一标识
  session=sess_yyy        # 会话哈希(前缀 sess_)
  proxy_index=0           # 使用的代理索引
  model=deepseek-v4       # 模型
  status=200              # HTTP 状态码
  duration=1.5s           # 请求耗时
```

### 监控指标

**会话亲和验证**:
```bash
# 检查同一 session 是否使用相同 proxy_index
grep "session=sess_abc" server.log | grep -o "proxy_index=[0-9]*" | sort | uniq
# 预期: 只有一个 proxy_index
```

**代理负载分布**:
```bash
# 统计每个代理处理的请求数
grep "proxy_index=" server.log | cut -d'=' -f4 | cut -d' ' -f1 | sort | uniq -c
# 预期: 相对均匀
```

**错误率**:
```bash
# 429 频率
grep "status=429" server.log | wc -l

# 代理错误
grep "upstream request failed" server.log | wc -l
```

### 推荐告警规则

- 429 比例超过 20% → 检查代理池大小或上游配额
- 单个代理错误率超过 50% → 检查代理健康状态
- 平均响应时间超过 10s → 检查网络或代理性能

## 故障排查

### 问题: 仍然频繁 429

**诊断**:
```bash
# 检查是否有会话 ID
grep "session=" server.log | head

# 检查会话分布
grep "session=" server.log | cut -d'=' -f3 | cut -d' ' -f1 | sort | uniq -c
```

**可能原因**:
1. 客户端没有设置 `x-conversation-id`
2. 每次请求 `x-conversation-id` 都不同
3. 代理池太小(所有会话挤在少数代理上)
4. 上游本身的限流(与代理无关)

**解决**:
- 确保客户端设置稳定的会话 ID
- 扩大代理池(至少 5-10 个)
- 检查上游 API 配额

### 问题: IPv6 连接错误

```
error="socks connect tcp ...->[2606:...]:443: connection refused"
```

**原因**: 使用了 `socks5://` 本地解析 DNS,得到 IPv6 地址,但代理不支持 IPv6。

**解决**: 所有代理 URL 改为 `socks5h://` (h = hostname,远程解析)

### 问题: 代理密码泄漏到日志

**当前状态**: 启动日志会打印代理 URL 含明文密码。

**临时解决**:
- 使用 `OPCODE_PROXY_POOL_FILE` 而非 `OPCODE_PROXY`
- 限制日志文件访问权限: `chmod 600 server.log`

**永久解决**: 等待后续版本的日志脱敏功能。

### 问题: 某个代理突然不可用

**现象**: 日志中某个 `proxy_index` 频繁报错,但服务继续运行。

**当前行为**: 代理池会继续尝试故障代理,没有自动隔离。

**临时解决**:
1. 编辑 `proxies.txt` 注释掉故障代理
2. 重启服务

**永久解决**: 等待后续版本的自动健康检查和故障隔离。

## 性能调优

### 并发连接数

默认每个代理使用 Go 标准 HTTP client,连接池自动管理。如果单个代理压力大:

1. 增加代理池大小
2. 或考虑在代理层做负载均衡

### 超时配置

```bash
# 延长超时(适用于长文本生成)
OPCODE_UPSTREAM_TIMEOUT=10m ./opencode-proxy-session.exe

# 缩短超时(快速失败)
OPCODE_UPSTREAM_TIMEOUT=30s ./opencode-proxy-session.exe
```

**注意**: 流式请求的超时是整个流的总时间,不是首字节时间。

### 内存占用

- 基础: ~10-20 MB
- 每个并发请求: ~1-5 MB(取决于消息大小)
- 代理池: 每个代理 ~100 KB

**建议**: 4 GB 内存可支持 500+ 并发请求。

## 安全加固

### 1. 启用认证

```bash
# 生成强密钥
OPCODE_API_KEY=$(openssl rand -base64 32)
```

### 2. 限制监听地址

```bash
# 仅本地访问
OPCODE_LISTEN=127.0.0.1:8080

# 内网访问
OPCODE_LISTEN=192.168.1.100:8080
```

### 3. 反向代理(推荐)

使用 Nginx/Caddy 添加 HTTPS 和速率限制:

```nginx
upstream opencode {
    server 127.0.0.1:8080;
}

server {
    listen 443 ssl http2;
    server_name api.example.com;
    
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    
    # 速率限制
    limit_req_zone $binary_remote_addr zone=api:10m rate=10r/s;
    limit_req zone=api burst=20;
    
    location / {
        proxy_pass http://opencode;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

### 4. 代理凭据保护

```bash
# 文件权限
chmod 600 proxies.txt
chown app-user:app-group proxies.txt

# 环境变量(避免出现在 ps 输出)
export OPCODE_PROXY_POOL_FILE=/secure/path/proxies.txt
./opencode-proxy-session.exe
```

## systemd 服务配置

```ini
# /etc/systemd/system/opencode-proxy.service
[Unit]
Description=OpenCode Proxy with Session Affinity
After=network.target

[Service]
Type=simple
User=app-user
WorkingDirectory=/opt/opencode-proxy
Environment="OPCODE_API_KEY=your-secret-key"
Environment="OPCODE_PROXY_POOL_FILE=/opt/opencode-proxy/proxies.txt"
Environment="OPCODE_LISTEN=:8080"
ExecStart=/opt/opencode-proxy/opencode-proxy-session.exe
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

启用服务:
```bash
sudo systemctl daemon-reload
sudo systemctl enable opencode-proxy
sudo systemctl start opencode-proxy
sudo systemctl status opencode-proxy
```

## 升级指南

### 从旧版本迁移

**变更点**:
1. 代理配置从环境变量改为文件(推荐)
2. 新增会话亲和机制
3. 请求 headers 自动多样化

**迁移步骤**:
1. 停止旧服务
2. 创建 `proxies.txt` 文件
3. 更新启动脚本使用 `OPCODE_PROXY_POOL_FILE`
4. 启动新版本
5. 验证日志中出现 `session=` 字段

**兼容性**: 所有客户端无需修改即可工作,但强烈建议添加 `x-conversation-id` 头以获得最佳效果。

## 下一步优化

当前版本已实现核心会话亲和和请求多样化,后续可考虑:

1. **健康检查**: 自动检测和隔离故障代理
2. **日志脱敏**: 隐藏代理密码
3. **Metrics API**: 暴露 Prometheus 指标
4. **动态代理池**: 热重载 proxies.txt 无需重启
5. **Retry-After 遵守**: 智能退避而非立刻重试
6. **SSE 转换层集成**: 统一流式处理(已准备好代码,未集成)

建议监控日志,根据实际使用情况决定优先级。
