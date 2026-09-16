# otter provider

通过 [otter](https://github.com/xiws/otter) 平台接入 ChatGPT / Gemini / DeepSeek：
orca 以 Go 库方式引用 otter 的 `pkg/provider`，直接复用 otter 已登录的凭据，
把模型的网页会话包装成 orca 的 `Requester`，无需自建 HTTP 服务。

## 配置

### models.json

```json
"otter": {
  "name": "Otter (Web)",
  "api": "otter",
  "models": [
    { "id": "deepseek", "name": "DeepSeek (otter)", "contextWindow": 65536,  "supportsTools": false },
    { "id": "chatgpt",  "name": "ChatGPT (otter)",  "contextWindow": 32768,  "supportsTools": false },
    { "id": "gemini",   "name": "Gemini (otter)",   "contextWindow": 1048576, "supportsTools": false }
  ]
}
```

- provider 级 `api: "otter"` 由 `internal/llm/requester.go` 的 `NewRequester` 分发到
  `NewOtterRequester`；其余（含未知值）保持走 OpenAI 兼容客户端。
- **模型 `id` 即 otter 平台名**：`deepseek` / `chatgpt` / `gemini`。
- `baseUrl` / `apiKey` 不使用，连接由 otter 的 platform provider 自己负责。
- `supportsTools: false`：web 平台没有原生 function calling，工具调用改走
  文本 JSON 协议（见下文）。

### 切换

```json
// .orca/setting.json
{ "defaultProvider": "otter", "defaultModel": "deepseek" }
```

或命令行指定：`orca -p otter -m deepseek "<任务>"`。

## 架构

```
cmd/cli ──▶ agent.Runtime ──▶ llm.NewRequester（按 api 分发）
                                   │
                                   ▼
                          otterRequester（internal/llm/otter.go）
                                   │  SendStream
                                   ▼
                          otter pkg/provider ──▶ 平台网页会话
```

`otter.go` 只依赖 `otterBackend` 这一窄接口（`Name` + `SendStream`），
真实实现由 `buildOtterBackend(platform)` 在首次 `Request` 时懒构建
（构建可能触发登录，失败以 `Result.Error` 返回）；测试通过
`newOtterRequester(info, build)` 注入 fake。

## 会话与增量映射

otter 平台侧会话是**有状态**的：平台自己记住历史，后续消息必须引用上一条回复的 id。
适配器的做法：

- 每个 orca 任务对应平台上一个会话。适配器只在**实例内存**里跟踪
  `delivered`（已发送消息数）、`chatSessionID`、`parentMessageID(Str)`、
  `RemoteMetadata`，**不写 `~/.otter/sessions`**（那是 otter CLI 的会话存储）。
- orca 每轮传全量历史，适配器只把 `messages[delivered:]` 增量发送：
  - 首轮 = system 消息（可多条，`\n\n` 拼接）+ user 消息；
  - 后续轮 = 工具结果，渲染为 `Tool result for <name>:\n<content>`
    （`name` 从之前 assistant 消息的 `ToolCalls` 里按 `ToolCallID` 回查）；
  - assistant 消息跳过——平台已记下模型说过的话，重发会重复对话。
- 平台差异：
  - **deepseek**：会话 id 由服务端生成，首轮先 `CreateRemoteSession`
    换取 `chatSessionID`；
  - **chatgpt / gemini**：会话随首条消息建立，`done` 事件回报
    `RemoteConversationID`。
- 回复流里的 `meta` / `done` 事件携带下一轮要引用的
  `ResponseMessageID(Str)` / `RemoteConversationID` / `RemoteMetadata` /
  `TokenUsage`；这些状态**只在整轮成功后提交**，失败（`error` 事件）则
  `delivered` 不前进，调用方重试会重发同一段。

超时：单请求 5 分钟（`otterRequestTimeout`），登录 / 建会话 30 秒
（`otterLoginTimeout`）。`SendRequest.Files` 不使用。

## 认证复用

凭据完全复用 otter 的配置文件 `~/.otter/config.json`，初始化逻辑与 otter CLI
（`pkg/cli/send.go` 的 `initXxxProvider`）保持一致，任一边登录、两边可用：

| 平台 | 凭据优先级 |
|---|---|
| deepseek | `OTTER_DEEPSEEK_AUTH_TOKEN` > config `auth_token` > account/password 自动登录（密码可用 `OTTER_DEEPSEEK_PASSWORD`），新 token 回写 config.json |
| chatgpt | cookie 持久化文件（可回退到本机 Chrome）> `OTTER_CHATGPT_ACCESS_TOKEN` / `OTTER_CHATGPT_SESSION_TOKEN` > config 里的 token |
| gemini | cookie 持久化文件（可回退到本机 Chrome）+ config 的 language |

未配置 deepseek 凭据时按 otter 的指引提示：
`otter config set deepseek.account <账号>`、`otter config set deepseek.password <密码>`，
或 `otter auth login deepseek`。

## 工具调用：文本 JSON 协议

web 平台无原生 function calling，因此 `supportsTools: false` 的模型走文本协议：

1. `Session.AppendToolPrompt()`（internal/agent/core/session.go）在
   `SupportsTools=false` 时追加一条 system 消息，内容为
   `handler.ToolPrompt()`——命令清单由 `handler.Commands()` 生成
   （read / write / edit / bash / create_task），prompt 与解析器不会漂移。
   `cmd/cli/main.go` 与子任务入口都会调用，`-sp` 自定义提示同样生效。
2. runtime 的 `execute` 拿到回复后，若无原生 `ToolCalls`，用
   `parse.ParseTextCalls`（internal/agent/parse/parse.go）从回复文本提取命令：
   - 平衡括号扫描 JSON 值（字符串 / 转义感知），支持单个对象或对象数组、代码围栏；
   - 只接受 `command` 值属于 `handler.Commands()` 白名单的对象；
   - 嵌套在更大 JSON 里的命令对象视为示例，不执行；
   - `Arguments` = 原对象去掉 `command` 键（保留 `id` / `data` 包裹形状）。
3. 提取出的命令经 `llm.EnsureToolCallIDs` 补齐 id 后，走与原生
   tool calling 完全相同的执行链路（assistant 消息 + 每条一个 tool 结果）。

## 限制与注意事项

- **依赖图**：otter `pkg/provider` → `pkg/api` 会把 tls-client
  （fhttp / utls / quic-go-utls）、wazero、x/crypto 等带进构建图；
  playwright（otter 的浏览器自动化部分）不在 orca 的导入子图内，不会引入。
- **toolchain**：otter 要求 go >= 1.26.7，orca 的 `go.mod` 同步升到 1.26.7；
  旧版本 Go 由 `GOTOOLCHAIN=auto` 自动拉取。
- **依赖方式**：`go.mod` 中 `replace github.com/xiws/otter => ../otter`，
  面向本地协同开发；发布时可改为 `go get github.com/xiws/otter@<版本>` 并去掉 replace。
- **文本协议误报**：最终回答里的示例 JSON 若恰好是白名单命令会被执行——
  已用 `handler.Commands()` 白名单收敛，且回退只在模型没有原生 ToolCalls
  且 provider 声明 `supportsTools: false` 时启用。
- **平台侧管控**：web 平台的上下文长度与限流由平台自己管理，
  models.json 里的 `contextWindow` 仅用于展示与提示。
- **会话不持久**：适配器状态只在内存，进程重启后新任务会重新建平台会话。
