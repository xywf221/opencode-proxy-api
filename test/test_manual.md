# 手动测试指南

## 验证会话亲和性

### 1. 准备代理池文件

```bash
# 创建 proxies.txt (每行一个代理)
cat > proxies.txt << 'EOF'
socks5h://user:pass@proxy1.example.com:1080
socks5h://user:pass@proxy2.example.com:1080
socks5h://user:pass@proxy3.example.com:1080
EOF
```

### 2. 启动服务

```bash
# 使用代理池
OPCODE_PROXY_POOL_FILE=proxies.txt ./opencode-proxy-session.exe

# 或单代理(自动转为单元素代理池)
OPCODE_PROXY=socks5h://user:pass@proxy.example.com:1080 ./opencode-proxy-session.exe
```

### 3. 测试相同会话使用相同代理

```bash
# 会话 A - 发送 3 次请求
for i in {1..3}; do
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "x-conversation-id: test-session-A" \
    -H "Authorization: Bearer test" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "deepseek-v4",
      "messages": [{"role": "user", "content": "Hello '$i'"}],
      "stream": false
    }'
  echo ""
  sleep 1
done
```

**预期结果**: 日志中这 3 次请求的 `session=sess_xxx` 相同,且使用相同的代理索引

### 4. 测试不同会话使用不同代理

```bash
# 会话 B - 不同的 conversation-id
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "x-conversation-id: test-session-B" \
  -H "Authorization: Bearer test" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4",
    "messages": [{"role": "user", "content": "Different session"}],
    "stream": false
  }'
```

**预期结果**: `session=sess_yyy` 不同于会话 A,可能使用不同的代理索引

### 5. 检查日志中的会话标识

```bash
# 查看日志中的 session 字段
grep "session=" server.log

# 预期输出示例:
# session=sess_abc proxy_index=0  <- 会话 A 第 1 次
# session=sess_abc proxy_index=0  <- 会话 A 第 2 次
# session=sess_abc proxy_index=0  <- 会话 A 第 3 次
# session=sess_xyz proxy_index=1  <- 会话 B
```

## 验证请求特征多样化

### 查看发往上游的 Headers

启用调试日志查看实际发送的请求头:

```bash
# 修改 internal/proxy/handler.go 临时添加调试输出
# 或使用 Wireshark/tcpdump 抓包查看
```

**预期看到**:
- `User-Agent`: 每次请求都不同,格式如 `OpenCode/2.5.1 (Windows x64)`
- `x-opencode-project`: 每次都是新的 UUID
- `x-opencode-request`: 每次都是新的 UUID
- `x-opencode-session`: 同一 conversation-id 保持不变

## 验证自动会话识别(不带 x-conversation-id)

```bash
# 不设置 conversation-id - 从消息内容生成
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4",
    "messages": [{"role": "user", "content": "Stable message"}],
    "stream": false
  }'

# 再次发送相同消息
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4",
    "messages": [{"role": "user", "content": "Stable message"}],
    "stream": false
  }'
```

**预期结果**: 两次请求生成相同的 `session=sess_xxx`,使用相同代理

## 性能测试

### 并发请求 - 验证不同会话分散到不同代理

```bash
# 使用 Apache Bench 或类似工具
for i in {1..10}; do
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "x-conversation-id: load-test-$i" \
    -H "Authorization: Bearer test" \
    -H "Content-Type: application/json" \
    -d '{"model": "deepseek-v4", "messages": [{"role": "user", "content": "Test"}]}' &
done
wait
```

**预期结果**: 
- 10 个不同会话分散到 3 个代理上
- 每个代理处理 3-4 个会话
- 日志显示负载相对均衡

## 故障恢复测试

### 模拟代理失败

1. 启动服务时包含一个无效代理:
   ```
   socks5h://invalid:123@10.255.255.1:9999
   ```

2. 发送请求,观察是否自动跳过失败的代理

**预期结果**: 健康的代理继续工作,请求不会永久失败

## OpenCode 特征验证

### 检查上游是否接受请求

```bash
# 连续发送多个请求到同一会话
for i in {1..20}; do
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "x-conversation-id: stress-test" \
    -H "Authorization: Bearer test" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "deepseek-v4-flash-free",
      "messages": [{"role": "user", "content": "Request '$i'"}],
      "stream": true
    }'
  sleep 0.5
done
```

**对比测试**:
- 之前版本: 连续请求后出现 429 或 connection refused
- 当前版本: 同一会话保持稳定,更少触发限流

## 日志检查清单

✅ 每个请求都有唯一的 `req_id`
✅ 相同 `conversation-id` 生成相同的 `session`
✅ `session` 映射到固定的 `proxy_index`
✅ 不同 `session` 分散到不同代理
✅ 启动日志不显示代理密码(仅显示数量)
✅ 错误日志区分"代理故障"vs"上游业务错误"

## 常见问题排查

### 问题: 仍然频繁 429

**可能原因**:
1. 代理池太小 - 所有会话竞争少量代理
2. 上游模型额度耗尽 - 与代理无关
3. 单个会话请求过于频繁 - 需要客户端限流

**解决**:
- 扩大代理池(至少 5-10 个)
- 检查上游 API 配额
- 客户端添加请求间隔

### 问题: 日志显示 IPv6 connection refused

**原因**: 使用了 `socks5://` 而非 `socks5h://`

**解决**: 所有代理 URL 改为 `socks5h://` (远程 DNS 解析)

### 问题: 会话 ID 不稳定

**原因**: 客户端没有设置 `x-conversation-id` 且每次消息内容都不同

**解决**:
- 客户端显式设置 `x-conversation-id` 头
- 或确保同一对话的首条消息保持一致
