# SSE Converter Package

统一的 SSE (Server-Sent Events) 转换组件,用于处理不同格式之间的流式响应转换。

## 架构

```
Client Request → Handler → Upstream /zen → SSE Converter → Client Response
                                              ├─ Reader (解析 SSE)
                                              ├─ Transform (转换逻辑)
                                              └─ Writer (输出 SSE)
```

## 核心组件

### 1. Reader (sse/reader.go)
解析 SSE 事件流:
- `ReadAll(io.Reader)` - 读取所有事件
- 支持多行 `data:` 字段
- 支持 `event:` 类型标记

### 2. Writer (sse/writer.go)
写入 SSE 事件:
- `WriteEvent(eventType, data)` - 写入单个事件
- 自动处理多行数据分割
- 符合 SSE 规范

### 3. Converter (sse/converter.go)
流式转换器:
- 逐事件转换,无需缓冲整个响应
- 支持自定义 `TransformFunc`
- 支持 DSML 工具调用重写
- 支持事件过滤、字段注入等

## 使用示例

### 基础用法 - 透传

```go
import "github.com/xywf221/opencode-proxy-api/internal/sse"

conv := sse.NewConverter(sse.FormatZen, sse.FormatMessages)
bytesWritten, err := conv.Convert(responseWriter, upstreamResponse.Body)
```

### 启用 DSML 重写

```go
conv := sse.NewConverter(
    sse.FormatZen, 
    sse.FormatMessages,
    sse.WithDSMLRewrite(true),
)
conv.Convert(w, r.Body)
```

### 自定义转换 - 过滤事件

```go
// 过滤掉 ping 事件
filterPing := func(eventType, data []byte) []byte {
    if string(eventType) == "ping" {
        return nil  // 返回 nil 表示跳过此事件
    }
    return data
}

conv := sse.NewConverter(
    sse.FormatZen,
    sse.FormatMessages,
    sse.WithTransform(filterPing),
)
```

### 自定义转换 - 注入字段

```go
// 给每个事件添加代理标识
addProxyHeader := func(eventType, data []byte) []byte {
    modified, err := sse.SetJSONField(data, "x-proxy", "opencode-proxy-api")
    if err != nil {
        return data  // 如果不是 JSON,返回原始数据
    }
    return modified
}

conv := sse.NewConverter(
    sse.FormatZen,
    sse.FormatMessages,
    sse.WithTransform(addProxyHeader),
)
```

### 链式转换

```go
// 组合多个转换:过滤 + 注入 + 验证
transforms := sse.ChainTransforms(
    filterPingEvents,
    addProxyMetadata,
    sse.ValidateJSON(),
)

conv := sse.NewConverter(
    sse.FormatZen,
    sse.FormatMessages,
    sse.WithTransform(transforms),
)
```

## 内置 Transform 函数

### 过滤类
- `FilterEventType(allowedTypes...)` - 只保留指定类型的事件
- `ValidateJSON()` - 只保留合法 JSON 事件

### 修改类
- `InjectField(field, value)` - 给每个事件注入 JSON 字段
- `SetJSONField(data, field, value)` - 设置 JSON 字段
- `ExtractJSONField(data, field)` - 提取 JSON 字段

### 调试类
- `LogTransform(logger)` - 记录每个事件
- `CountEvents(every, onCount)` - 统计事件数量
- `Pretty(data)` - 格式化 JSON (调试用)
- `Compact(data)` - 压缩 JSON

### 组合类
- `ChainTransforms(transforms...)` - 链式组合多个转换

## 集成到 Handler

### 方案 1: 替换现有流式代理逻辑

在 `handler.go` 的 SSE 响应部分:

```go
// 旧代码 (直接透传):
// io.Copy(w, resp.Body)

// 新代码 (使用 Converter):
conv := sse.NewConverter(
    sse.FormatZen,
    sse.FormatMessages,
    sse.WithDSMLRewrite(needsDSMLRewrite(req)),
)
conv.Convert(w, resp.Body)
```

### 方案 2: 根据端点选择转换

```go
func (h *Handler) handleSSEResponse(w http.ResponseWriter, resp *http.Response, endpoint string) error {
    var conv *sse.Converter

    switch endpoint {
    case "/v1/messages":
        conv = sse.NewConverter(
            sse.FormatZen,
            sse.FormatMessages,
            sse.WithDSMLRewrite(true),
        )
    case "/v1/chat/completions":
        conv = sse.NewConverter(
            sse.FormatZen,
            sse.FormatChatCompletions,
        )
    default:
        // 透传
        conv = sse.NewConverter(sse.FormatZen, sse.FormatZen)
    }

    _, err := conv.Convert(w, resp.Body)
    return err
}
```

## TransformFunc 签名

```go
type TransformFunc func(eventType, data []byte) []byte
```

**参数:**
- `eventType` - 事件类型 (来自 `event:` 行,可能为空)
- `data` - 事件数据 (来自 `data:` 行,已合并多行)

**返回值:**
- `[]byte` - 转换后的数据 (写入输出)
- `nil` - 跳过此事件 (不写入输出)

## 支持的格式

当前定义的格式常量:

```go
const (
    FormatZen             Format = "zen"              // 上游 opencode.ai/zen 格式
    FormatMessages        Format = "messages"         // Anthropic Messages API
    FormatChatCompletions Format = "chat_completions" // OpenAI Chat Completions
)
```

## 注意事项

### DSML 重写的特殊处理

当启用 `WithDSMLRewrite(true)` 时:
- 整个流会被缓冲 (不是逐事件转换)
- 因为 DSML 工具调用可能跨越多个事件
- 使用 `translate.RewriteClaudeStreamDSML()` 处理

### 非 SSE 响应的处理

如果上游返回的不是 SSE 格式 (非流式):
- `Convert()` 会自动检测
- 调用 `convertNonStreaming()` 处理整个 JSON 响应体
- 仍然应用 DSML 重写和自定义转换

## 性能考虑

1. **零拷贝模式**: 透传时无额外分配
2. **逐事件处理**: 大部分场景下不缓冲整个响应
3. **DSML 例外**: DSML 重写需要完整流 (trade-off 正确性)

## 测试

运行测试:
```bash
go test ./internal/sse/... -v
```

测试覆盖:
- ✅ SSE 解析 (单/多事件,多行数据)
- ✅ SSE 写入 (事件类型,多行数据)
- ✅ 透传模式
- ✅ 事件过滤
- ✅ 字段注入
- ✅ 链式转换
- ✅ JSON 工具函数

## 下一步

1. ✅ 基础 SSE 读写
2. ✅ 转换器框架
3. ✅ DSML 重写集成
4. ⏳ 集成到 handler.go
5. ⏳ 添加指标收集 (事件计数,延迟)
6. ⏳ 添加格式转换 (Zen ↔ Messages ↔ ChatCompletions)
