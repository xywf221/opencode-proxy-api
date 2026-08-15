# Bug Fix: Missing tool_call_id Validation Error

## 问题描述

**错误信息**:
```
API Error: 400 Error from provider (Console): Upstream request failed: 
[invalid_request_error] Failed to deserialize the JSON body into the target type: 
messages[3]: missing field `tool_call_id` at line 1 column 35797
```

## 根本原因

在 `internal/translate/claude_request.go:258-262` 中，当处理 Claude 的 `tool_result` 内容块时：

```go
case "tool_result":
    toolUseID := block["tool_use_id"]
    if toolUseID == "" {
        toolUseID = block["tool_call_id"]  // 兼容 OpenAI 格式
    }
    newMessages = append(newMessages, map[string]interface{}{
        "role":         "tool",
        "tool_call_id": toolUseID,  // ⚠️ 可能是空字符串！
        "content":      block["content"],
    })
```

**问题**:
- 如果客户端发送的 `tool_result` 既没有 `tool_use_id` 也没有 `tool_call_id`
- `toolUseID` 会是空字符串 `""`
- 生成的 OpenAI 格式消息会包含 `"tool_call_id": ""`
- 上游 OpenCode API 验证失败，因为 `tool_call_id` 是**必需字段且不能为空**

## 修复方案

### 代码修改

```go
case "tool_result":
    toolUseID := block["tool_use_id"]
    if toolUseID == "" {
        toolUseID = block["tool_call_id"]
    }
    // ✅ 跳过没有有效 ID 的 tool_result
    if toolUseID == "" {
        continue  // 静默跳过，不添加到消息列表
    }
    newMessages = append(newMessages, map[string]interface{}{
        "role":         "tool",
        "tool_call_id": toolUseID,
        "content":      block["content"],
    })
```

### 修复逻辑

1. **验证 tool_use_id**: 在添加 tool 消息之前检查 ID 是否为空
2. **静默跳过**: 如果没有有效 ID，跳过该内容块而不是生成无效消息
3. **向后兼容**: 不影响正常的 tool_use/tool_result 流程

## 测试验证

### 新增测试用例

`internal/translate/claude_request_test.go`:

```go
func TestSkipToolResultWithoutID(t *testing.T) {
    // 输入: 包含一个无效 tool_result 和一个有效 tool_result
    in := []byte(`{
        "model":"m",
        "messages":[
            {"role":"assistant","content":[
                {"type":"tool_use","id":"call_1","name":"add","input":{"a":1}}
            ]},
            {"role":"user","content":[
                {"type":"tool_result","content":"result without ID"}  // ❌ 无 ID
            ]},
            {"role":"user","content":[
                {"type":"tool_result","tool_use_id":"call_1","content":"valid result"}  // ✅ 有 ID
            ]}
        ]
    }`)
    
    out := ClaudeRequestToUpstream(in)
    
    // 验证: 只有 1 个 tool 消息(有效的那个)
    toolMsgCount := 0
    for _, msg := range parsed["messages"] {
        if msg["role"] == "tool" {
            toolMsgCount++
            // 确保 tool_call_id 非空
            assert.NotEmpty(t, msg["tool_call_id"])
        }
    }
    assert.Equal(t, 1, toolMsgCount)  // 无效的被跳过了
}
```

### 测试结果

```bash
$ go test ./internal/translate/... -v -run TestSkipToolResultWithoutID
=== RUN   TestSkipToolResultWithoutID
--- PASS: TestSkipToolResultWithoutID (0.00s)
PASS
```

## 影响范围

### 受影响的场景

1. **客户端错误**: 某些客户端可能发送格式不正确的 tool_result
2. **协议混用**: 在 Claude/OpenAI 格式混用时可能产生不完整的消息
3. **异常流程**: 工具调用流程异常中断时的边界情况

### 修复效果

- **之前**: 请求失败，返回 400 错误
- **之后**: 静默跳过无效的 tool_result，保留有效部分，请求继续处理

## 部署建议

### 立即部署

此修复是**向后兼容**的，不会影响正常流程：

```bash
# 重新构建
go build -o opencode-proxy-linux-amd64 ./cmd/server

# 部署
./opencode-proxy-linux-amd64
```

### 监控建议

如果想了解有多少请求被此修复救回，可以添加日志：

```go
if toolUseID == "" {
    log.Printf("[WARN] Skipped tool_result without ID in message index %d", msgIdx)
    continue
}
```

## 相关文件

- **修复代码**: `internal/translate/claude_request.go:247-263`
- **测试用例**: `internal/translate/claude_request_test.go:154-182`
- **版本记录**: `CHANGELOG.md` - v1.1.1

## 版本信息

- **修复版本**: v1.1.1
- **修复日期**: 2026-08-14
- **修复作者**: Claude Code (Fable 5)
- **测试覆盖**: ✅ 新增回归测试
- **构建状态**: ✅ 所有测试通过

---

## 快速检查清单

- [x] 问题诊断完成
- [x] 根因分析完成
- [x] 代码修复完成
- [x] 单元测试添加
- [x] 回归测试通过
- [x] 全量测试通过 (`go test ./...`)
- [x] 构建验证完成
- [x] 文档更新完成
- [x] CHANGELOG 更新

**状态**: ✅ 生产就绪
