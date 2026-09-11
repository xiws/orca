# cmd/cli（参数解析与入口）

`cmd/cli` 是 orca 的命令行入口，由 `args.go`（参数解析）和 `main.go`（编排主流程）两个文件组成：前者把命令行
参数解析成结构化对象，后者负责把一次调用编排成一个"任务"交给 runtime 执行。两者通过 `ParseArgs` → `*CliArgs`
串联，因此先讲参数解析，再讲入口主流程。

---

## cmd/cli/args.go —— 参数解析

负责解析命令行参数，把 `os.Args[1:]` 里的一组标志（flag）和位置参数整理成结构化的 `*CliArgs`。它本身职责很轻：
就一个 `ParseArgs` 函数加上两个辅助类型（`StringSlice`、`CliArgs`）。真正读取文件、渲染提示词、建立对话是在
`main.go` 里完成的。

### Go 结构定义

```go
// StringSlice 让 -file / -f 这类标志可重复传入（flag 库对 []string 默认会把多个 -f 折叠成逗号串，
// 这里用一个自定义 Var 类型逐个收集，因此 -f a -f b 得到 ["a","b"]）。
type StringSlice []string

func (s *StringSlice) String() string  // flag 打印帮助信息时的展示：逗号拼接，如 "a,b"
func (s *StringSlice) Set(value string) error // 每传入一次就 append 到末尾

// CliArgs 是解析后的结果，交给主链路。
type CliArgs struct {
    Files        []string // 附加文件名（-f / -file），可重复传入
    SystemPrompt string   // 覆盖用的系统提示文件名（-sp / -s），传的是文件名而非内容
    Model        string   // 指定模型（-m / -model）
    Provider     string   // 指定模型 Provider（-p / -provider）
    Prompt       string   // 用户输入，由所有位置参数用空格拼接而成
}
```

### 支持的标志

解析器构造了一个名为 `orca` 的 `flag.FlagSet`（策略 `ContinueOnError`——报错时直接返回错误而不是退出、也不 panic），
登记如下：

| 标志 | 绑定的值 | 说明 |
| --- | --- | --- |
| `-sp`, `-s` | `SystemPrompt` | 覆盖系统提示，传入的是**文件名**，解析后由主链路去 `os.ReadFile` |
| `-model`, `-m` | `Model` | 指定要用的模型 |
| `-p`, `-provider` | `Provider` | 指定模型 Provider |
| `-file`, `-f` | `Files`（`StringSlice`） | 附加文件，**可重复传入**，每条进入 user 消息成为 `<file>` 元素 |

> 每个标志都同时登记了长、短两种形式（例如 `--model` 与 `-m`），方便不同模型的调用习惯。

### 解析流程（`ParseArgs`）

```go
func ParseArgs() (*CliArgs, error) {
    var files StringSlice
    fs := flag.NewFlagSet("orca", flag.ContinueOnError)

    // 1. 登记各标志（system prompt / model / provider / files，见上表）
    // 2. 解析 os.Args[1:]; 出错（含未知标志）直接 return 该错误，让 main 打印并退出
    if err := fs.Parse(os.Args[1:]); err != nil {
        return nil, err
    }
    // 3. 取剩余位置参数；没有就判定为错误："missing prompt"
    args := fs.Args()
    if len(args) == 0 {
        return nil, fmt.Errorf("missing prompt")
    }
    // 4. 拼成 CliArgs：position 参数用空格连成一个 Prompt，文件等原样收集
    return &CliArgs{
        Files:        files,
        SystemPrompt: *systemPrompt,
        Model:        *model,
        Provider:     provider,
        Prompt:       strings.Join(args, " "),
    }, nil
}
```

要点：

- **错误处理**走返回模式而非直接 exit——`ParseArgs` 把错误原样返回，由 `main` 负责写 stderr 和 `os.Exit(1)`。
- **位置参数即 prompt**：任何未被标志消耗剩下的参数都作为 prompt 文本，多个位置参数用空格拼接（例如 `m "do a"` + `b` → `do a b`）。
- 解析成功即返回 `*CliArgs`；只有 flag 解析失败或位置参数为空两种情况才会带着错误返回。

---

## cmd/cli/main.go —— 入口主流程（`main`）

### 目标

`cmd/cli/main.go` 是 orca 的命令行入口，把用户的一次调用组装成一个"任务"（`agent.Task`），交给 `agent.Runtime`
在多轮模型循环里执行，最后把结果打印到 stdout。它本身的职责很轻：只做**编排**——解析参数 → 建 runtime → 建任务 →
执行 → 输出/报错。参数解析、任务构建、runtime 执行分别放在它的同包文件 `args.go`、它与 `internal/agent` 的协作、和
`agent_runtime.go` 里。

### 主流程（`main`）

```go
func main() {
    args, err := ParseArgs()          // cmd/cli/args.go
    if err != nil { 打印 stderr 并 exit(1) }

    runtime := agent.NewRuntime()
    task, err := CreateTask(args)
    if err != nil { 打印 stderr 并 exit(1) }

    err, result := runtime.RunTask(task)
    if err != nil { 打印 stderr 并 exit(1) }

    fmt.Println(result)
}
```

1. **解析参数** — 调 `args.ParseArgs()`，得到 `*CliArgs`；失败把 `orca: <err>` 写进 stderr 并退出（`os.Exit(1)`）。
2. **创建 runtime** — `agent.NewRuntime()`，初始化命令注册和事件总线，驱动模型循环。
3. **构建任务** — `CreateTask(args)`，把参数组织成一段对话（一条 system + 一条 user + 可选自定义 system）。
4. **执行** — `runtime.RunTask(task)` 跑完循环，返回 `(error, result)`；出错同样 stderr + 退出。
5. **输出** — 成功就把结果 `result` 打印到 stdout。

### 构建任务（`CreateTask`）

`CreateTask` 负责把参数拼成交给 runtime 的一段对话 `*agent.Task`，它由 **一个 system 消息** 和 **一个 user 消息** 组成。

- **system 消息**（两者择一）：
  - 若用 `-s` / `-sp`（`SystemPrompt`）指定了自定义系统提示文件，就 `os.ReadFile` 读其内容作为 system 消息；
  - 否则用默认 system 提示：`utils.GetSystemPrompt(PromptContext{ ProjectPath: utils.GetCurrentPath(), ContextLength: task.SessionInfo.Provider.ContextWindow })`。
- **user 消息**：`userPrompt(args)` 渲染出来的内容。
- **模型信息**：若同时指定了 `-p`（provider）和 `-m`（model），通过 `task.SessionInfo.SetProvider(provider, model)` 写入会话，覆盖默认 provider。

### 渲染用户消息（`userPrompt`）

`userPrompt` 负责把"附加文件"和"请求正文"拼成 user 这一轮：

- 遍历 `args.Files`（`-f` / `-file`，可重复传多个），逐个 `os.ReadFile`，去掉结尾换行后按标签格式写入：
  ```
  <file path="相对路径">文件内容</file>
  ```
- 随后追加请求正文 `args.Prompt`；
- 附加文件读取失败会报错，返回空串。

> 为什么附件是放进 user 消息，而不是作为 tool message：tool message 必须回答"它前一个 assistant 消息发起的工具调用"，而在对话开头根本没有先前的 assistant 消息，服务端无法接受这种结构。所以附件被当成 user 消息里的 `<file>` 元素处理。

### 相关背景：会话与协议（`main.go` 之外的支撑）

`main.go` 只建任务的骨架，但理解它构建的 `Task.SessionInfo`（`*agent.Session`）对读后续 logic 很有帮助：

- `Session.Messages` 是一条对话时间线（`[]llm.ChatMessage`），顺序必须满足协议：**assistant 消息（附带 tool call）之后，必须紧跟每条 tool call 对应的 tool 消息，中间不能插入其他消息**。
- `main` 一开始建的是 `system → user` 这条起始序列；之后由 `Runtime.execute` 不断 `append assistant → 执行 tools → 追加 tool result`。
- `ChatMessage` 的 role 有 `system / user / assistant / tool` 四种；`Session` 负责按顺序把这些消息 append 进去。
