# Event Display 改造 Spec

## 1. 现状与问题

### 1.1 当前链路

每个 handler（read / write / edit / bash / create_task）在操作前后发布两个事件：

- `ToolBeforeEvent` — 操作前展示"将要做什么"
- `ToolAfterEvent` — 操作后展示"结果是什么"

事件的核心承载字段是 `command string`，每个 handler 通过 `getShell()` 或类似方法构造一个**类 shell 命令的字符串**传给事件：

| handler | 当前 `command` 构造方式 | 问题 |
|---|---|---|
| **read** | `"read foo.go"` 或 `"read foo.go-10:30"` | 可读，但元信息扁平 |
| **write** | `fmt.Sprintf("write %s %s", filename, content)` — 整个文件内容内联 | 大文件内容直接糊在标题行 |
| **edit** | `"edit foo.go --diff <diff>"` 且做了 `\n→\\n` 转义 | 字面量 `\n` 被输出，不可读 |
| **bash** | `"/bin/bash -c ls -la"` | 暴露平台 shell 路径 |
| **create_task** | `"create_task title"` | 勉强可读但与其他不统一 |

### 1.2 事件处理器的渲染缺陷

**ToolEventBeforeHandler**（`tool_before_event.go`）：

```go
fmt.Print("\033[1m")
fmt.Print("\033[38;5;15m")   // 前景白
fmt.Print("\033[42;5;250m")  // 背景浅灰
var content = fmt.Sprintf(" %s \033[0m \n\n ", toolEvent.command)
```

- 无工具类型区分，所有命令统一灰底白字
- 无图标 / 无结构化排版
- 如果 `command` 很长（write 带内容），直接撑爆终端

**ToolAfterEventHandler**（`tool_after_event.go`）：

```go
if strings.HasSuffix(toolEvent.command, ".md") { … }
else if strings.HasSuffix(toolEvent.command, ".go") { … }
else if strings.HasSuffix(toolEvent.command, ".json") { … }
else { /* .txt fallback */ }
```

- 通过 `command` 字符串后缀猜测内容格式——完全不可靠
- 仅在 `debug=true` 时显示（大部分用户看不到）

**BashEventHandler**（`bash_event.go`）：

- 格式化 `"# command\ntask id: X\ncontent"` 后走 glamour 渲染
- **但从未被发布**，是死代码——bash handler 发的是 `ToolAfterEvent`

### 1.3 总结问题

| # | 问题 | 严重度 |
|---|---|---|
| P0 | `command` 字段承载混合语义（元信息 + 内容），无法结构化渲染 | 高 |
| P0 | write handler 把整个文件内容内联到标题行 | 高 |
| P0 | edit handler 对 diff 做 `\n→\\n` 转义导致字面 `\n` 输出 | 高 |
| P1 | 所有 before 事件无图标 / 无工具类型区隔 | 中 |
| P1 | bash before 暴露平台路径 `/bin/bash -c` | 低 |
| P1 | `BashEvent` 死代码 | 低 |
| P2 | after 事件仅在 debug 模式显示 | 中 |

---

## 2. 设计目标

1. **结构化事件**：before / after 事件携带工具类型、文件路径、元信息等结构化字段，而不是扁平的 shell 命令字符串
2. **Markdown 原生渲染**：每个事件处理器按工具类型输出带 Markdown 格式的终端内容（利用已有 glow/glamour 能力）
3. **一致的视觉风格**：before 事件用图标 + 加粗路径，after 事件用代码块/摘要
4. **移除字面 `\n`**：编辑 diff 应保留可读换行
5. **Before 事件始终可见**，After 事件受 debug 控制保持不变（但渲染增强）

---

## 3. 事件结构改造

### 3.1 ToolBeforeEvent — 新增结构化字段

```go
// Internal/event/tool_before_event.go

type ToolBeforeEvent struct {
    id        int64
    tool      string   // "read" | "write" | "edit" | "bash" | "create_task"
    file      string   // 文件路径（文件操作）；bash/create_task 为空
    meta      string   // 附加信息：行范围、片段数、bash命令本身
    reasoning string   // 模型的推理内容
}
```

**渲染模板**（每类工具独立）：

```
📖 Read `path/file.go` (lines 10-50)     ← 文件路径加反引号 + 行范围
📝 Write `path/file.go` (2.3 KB)          ← 显示文件大小
✏️ Edit `path/file.go` (3 fragments)      ← 显示段数
💻 $ ls -la                                ← bash 命令直接展示
🎯 create_task: "任务标题"                 ← 子任务标题
```

- 每行前放 Emoji（考虑终端兼容，选用简单 Emoji 集）
- `reasoning` 放在下方缩进显示：

```
📖 Read `main.go` (lines 15-40)
   > 需要检查 handle 函数的具体实现   ← reasoning 用灰色斜体/块引用
```

### 3.2 ToolAfterEvent — 新增结构化字段

```go
// Internal/event/tool_after_event.go

type ToolAfterEvent struct {
    id      int64
    tool    string   // "read" | "write" | "edit" | "bash"
    file    string   // 文件路径
    ok      bool     // 是否成功
    summary string   // 单行摘要（如 "edited main.go: 120→125 lines"）
    content string   // 详细内容（read 返回的文件内容 / bash 的输出 / edit 的摘要）
    exitCode int    // bash 专用：退出码
}
```

**渲染模板**：

| 工具 | 成功渲染 | 失败渲染 |
|---|---|---|
| **read** | ` ```go↩  ← 文件扩展名推导语言`<br>文件内容<br>` ` `` ` | ` ❌ Read `path` failed: 错误信息` |
| **write** | 摘要行（` ✅ Wrote 2048 bytes to path/file.go`） | 同上 |
| **edit** | 摘要行（` ✅ Edited path/file.go: 120 → 125 lines, applied 2 hunks`） | ` ❌ Edited path/file.go failed: …` |
| **bash** | ` ``` ↩ exit-code: 0`<br>输出内容<br>` ` `` ` | ` ``` ↩ exit-code: 1`<br>输出内容<br>` ` `` ` |
| **create_task** | 不单独展示 after（由 task_complate 处理） | — |

---

## 4. 处理器改造方案

### 4.1 ReadHandler

**当前 `getShell()`**：
```go
func (t ReadHandler) getShell(opt *ReadOption) string {
    return fmt.Sprintf("read %s-%d:%d", opt.Filename, opt.Start, opt.End)
}
```

**改造后**：不再需要 `getShell()`，直接构造 `ToolBeforeEvent`：

```go
func (t ReadHandler) Handle(cmd command.CommandOption) (error, any) {
    opt, _ := cmd.(*ReadOption)
    file := opt.Filename
    meta := ""
    if opt.Start > 0 || opt.End > 0 {
        meta = fmt.Sprintf("lines %d-%d", opt.Start, opt.End)
    } else {
        meta = "entire file"
    }
    publish(t.Publisher, event.NewToolBeforeEvent("read", file, meta, opt.Reasoning, opt.Id))
    content, err := t.read(opt)
    publish(t.Publisher, event.NewToolAfterEvent("read", file, content, err == nil, "", content, opt.Id, 0))
    return nil, ResultFor(opt, content, err)
}
```

### 4.2 WriteHandler

**当前**：把整个 content 内联到 shell 字符串：
```go
shell := fmt.Sprintf("write %s %s", opt.Filename, opt.Content)
```

**改造后**：显示文件名 + 大小（替代原始内容）：

```go
meta := fmt.Sprintf("%d bytes", len(opt.Content))
publish(t.Publisher, event.NewToolBeforeEvent("write", opt.Filename, meta, opt.Reasoning, opt.Id))
```

### 4.3 EditHandler

**当前**：对 diff 做 `\n→\\n` 转义，产生字面 `\n`：

```go
oneLine := strings.ReplaceAll(fragment.Diff, "\n", "\\n")
```

**改造后**：不再构造 shell 字符串，meta 显示片段数量：

```go
meta := fmt.Sprintf("%d fragment(s)", len(opt.Contents))
publish(t.Publisher, event.NewToolBeforeEvent("edit", opt.Filename, meta, opt.Reasoning, opt.Id))
```

### 4.4 BashHandler

**当前**：`cmdStr = "/bin/bash -c ls -la"` 暴露平台路径

**改造后**：只展示命令本身：

```go
publish(t.Publisher, event.NewToolBeforeEvent("bash", "", opt.Content, opt.Reasoning, opt.Id))
```

After 事件带上 exitCode：

```go
exitCode := 0
var exitErr *exec.ExitError
if errors.As(runErr, &exitErr) {
    exitCode = exitErr.ExitCode()
}
publish(t.Publisher, event.NewToolAfterEvent("bash", "", report, runErr == nil, summary, report, opt.Id, exitCode))
```

### 4.5 CreateTaskHandler

**当前**：`"create_task title"`

**改造后**：

```go
meta := fmt.Sprintf("%d sub-tasks", len(opt.TaskTarget))
if len(opt.TaskTarget) > 0 {
    meta = fmt.Sprintf("「%s」等 %d sub-tasks", opt.TaskTarget[0].Title, len(opt.TaskTarget))
}
publish(t.Publisher, event.NewToolBeforeEvent("create_task", "", meta, opt.Reasoning, opt.Id))
```

---

## 5. 事件处理器渲染方案

### 5.1 ToolBeforeEventHandler — 新渲染

利用 **[glow/glamour](https://github.com/charmbracelet/glamour)** 或直接构造 ANSI 字符串。推荐结构化的 Markdown 模板：

```
{emoji} **{tool_name}** `{file_path}` ({meta})
{reasoning}
```

具体实现：

```go
func (b ToolEventBeforeHandler) Handle(ent event.Event) {
    e, _ := ent.(ToolBeforeEvent)

    // 1. 构建标题行（工具名 + 路径 + 元信息）
    title := e.renderTitle()  // 见下方表格

    // 2. 用 glamour 渲染标题
    renderer, _ := glamour.NewTermRenderer(
        glamour.WithStyles(styles.ASCIIStyleConfig),
        glamour.WithWordWrap(-1),
    )
    md := fmt.Sprintf("**%s**", title)
    if e.reasoning != "" {
        md += fmt.Sprintf("\n> %s", e.reasoning)
    }
    rendered, _ := renderer.Render(md)
    fmt.Print(rendered)
}

func (e ToolBeforeEvent) renderTitle() string {
    switch e.tool {
    case "read":
        return fmt.Sprintf("📖 Read `%s` (%s)", e.file, e.meta)
    case "write":
        return fmt.Sprintf("📝 Write `%s` (%s)", e.file, e.meta)
    case "edit":
        return fmt.Sprintf("✏️ Edit `%s` (%s)", e.file, e.meta)
    case "bash":
        return fmt.Sprintf("💻 $ %s", e.meta)
    case "create_task":
        return fmt.Sprintf("🎯 %s", e.meta)
    }
}
```

### 5.2 ToolAfterEventHandler — 新渲染

```go
func (b ToolAfterEventHandler) Handle(ent event.Event) {
    e, _ := ent.(ToolAfterEvent)

    if !e.ok {
        // 失败统一展示
        fmt.Printf("❌ **%s** `%s` failed: %s\n", e.tool, e.file, e.summary)
        return
    }

    switch e.tool {
    case "read":
        lang := detectLang(e.file)
        md := fmt.Sprintf("```%s\n%s\n```", lang, e.content)
        b.renderMarkdown(md)

    case "write":
        fmt.Printf("✅ %s\n", e.summary)

    case "edit":
        fmt.Printf("✅ %s\n", e.summary)

    case "bash":
        md := fmt.Sprintf("```\n$ %s\n```\n", e.summary)  // summary 保存命令原文
        if e.content != "" {
            md += fmt.Sprintf("```\n%s\n```", e.content)
        }
        md += fmt.Sprintf("\n_exit code: %d_", e.exitCode)
        b.renderMarkdown(md)
    }
}
```

### 5.3 BashEvent（清理）

**删除 `BashEvent` / `BashEventHandler`**，因 bash handler 已改用 `ToolAfterEvent`。同步移除：

- 注册处的 `bus.Subscribe(event2.BashEvent{}, …)` 一行
- 测试中的对应注册

### 5.4 语言检测（辅助函数）

用文件后缀映射到 markdown 代码块语言标识：

```go
func detectLang(filePath string) string {
    switch {
    case strings.HasSuffix(filePath, ".go"):  return "go"
    case strings.HasSuffix(filePath, ".md"):  return "markdown"
    case strings.HasSuffix(filePath, ".json"): return "json"
    case strings.HasSuffix(filePath, ".yaml"),
         strings.HasSuffix(filePath, ".yml"):  return "yaml"
    case strings.HasSuffix(filePath, ".sh"):   return "bash"
    case strings.HasSuffix(filePath, ".py"):   return "python"
    case strings.HasSuffix(filePath, ".js"):   return "javascript"
    case strings.HasSuffix(filePath, ".ts"):   return "typescript"
    case strings.HasSuffix(filePath, ".html"): return "html"
    case strings.HasSuffix(filePath, ".css"):  return "css"
    case strings.HasSuffix(filePath, ".toml"): return "toml"
    case strings.HasSuffix(filePath, ".mod"):  return "go"
    default: return ""
    }
}
```

---

## 6. 清理项

| 清理项 | 文件 | 说明 |
|---|---|---|
| **删除 `BashEvent` / `BashEventHandler`** | `internal/event/bash_event.go` | 整文件删除；从 `agent_runtime.go`、`handler_test.go` 移除注册 |
| **删除 `getShell()` 方法** | `read.go` | 不再需要 |
| **删除 `getShell()` 方法** | `edit.go` | 不再需要 |
| **删除内联 content 构造** | `write.go` | 不再 `fmt.Sprintf("write %s %s", …)` |
| **删除 `\n→\\n` 转义** | `edit.go` | `getShell()` 方法内的 `strings.ReplaceAll` 随方法删除 |

---

## 7. 渲染效果预览

### 7.1 原始效果（现状）

```
❯  read main.go-10:30     ← 灰底白字，无区分
task id:1 command:read result:...
```

### 7.2 改造后效果

```
📖 Read `main.go` (lines 10-30)         ← 加粗路径 + 范围
   > 需要确认 handle 函数签名             ← 推理内容灰体缩进

```go
func handle(ctx Context) error {
    return nil
}
```
```

```
💻 $ go test ./internal/handler -run TestEdit

> 确认 edit handler 的边界情况

```
ok  	orca/internal/handler	0.325s
```

_exit code: 0_
```

```
✏️ Edit `docs/README.md` (3 fragments)
   > 更新架构说明部分

✅ Edited docs/README.md: 45 → 52 lines, applied 3 hunks
```

---

## 8. 向后兼容

- `ToolBeforeEvent` / `ToolAfterEvent` 的 `GetName()` 返回值不变，已有订阅者不会断
- 事件发送方 handler 和接收方 handler 同步改造（不拆分为两阶段）
- `CommandResult` 结构不变，模型看到的工具结果不受影响
- `BashEvent` 是死代码，删除不影响任何功能

---

## 9. 实施步骤

| 步骤 | 内容 | 涉及文件 |
|---|---|---|
| 1 | 改造 `ToolBeforeEvent` 结构 + 新 handler 渲染 | `internal/event/tool_before_event.go` |
| 2 | 改造 `ToolAfterEvent` 结构 + 新 handler 渲染（含 `detectLang`） | `internal/event/tool_after_event.go` |
| 3 | 清理/删除 `BashEvent` | `internal/event/bash_event.go` + 注册处 |
| 4 | 改造 `read.go` handler | `internal/handler/read.go` |
| 5 | 改造 `write.go` handler | `internal/handler/write.go` |
| 6 | 改造 `edit.go` handler | `internal/handler/edit.go` |
| 7 | 改造 `bash.go` handler | `internal/handler/bash.go` |
| 8 | 改造 `createtask.go` handler | `internal/handler/createtask.go` |
| 9 | 更新注册：删除 BashEvent | `internal/agent/agent_runtime.go` |
| 10 | 更新测试 | `internal/handler/handler_test.go` |
| 11 | 手动验证视觉输出 | 终端运行测试 |